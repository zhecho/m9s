package sync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	gosync "sync"
	"sync/atomic"
	"time"

	imap "github.com/emersion/go-imap/v2"
	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail"
	imapadapter "github.com/zhecho/m9s/internal/mail/imap"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	"github.com/zhecho/m9s/internal/store"
)

// defaultMirrorInterval is how often the local copy is compared with the
// server when nothing announces a change. New mail in INBOX does not wait.
const defaultMirrorInterval = 5 * time.Minute

// mirrorRetryDelay is the pause after the copy failed before it is resumed.
const mirrorRetryDelay = 30 * time.Second

// Engine owns two IMAP connections per account and drives all sync operations.
// The idle connection is dedicated to IMAP IDLE; the cmd connection handles
// all other commands (LIST, STATUS, FETCH, APPEND).
type Engine struct {
	mu         gosync.Mutex
	cfg        *config.Account
	events     chan<- Event
	idleConn   imapadapter.Client
	cmdConn    imapadapter.Client
	folders    []*mail.Folder                // last-known folder list; never handed out directly
	status     map[string]mail.MailboxStatus // last-known counts per folder name
	draftStore *draft.Store

	backoff func(attempt int) time.Duration
	retry   chan struct{} // buffered(1): wakes a failed engine early
	failed  atomic.Bool   // true while waiting to retry
	done    chan struct{} // closed when run returns

	serverInfoSent atomic.Bool // the server was identified this run

	probeTimeout time.Duration

	// Owned by the run goroutine.
	pass string // resolved IMAP password, kept across reconnects

	// Sending: the SMTP sender and the password found at startup.
	sender   smtpadapter.Sender
	smtpPass string

	draftMu gosync.Mutex // one draft upload pass at a time: no double uploads

	// The local copy: store holds it, mirrorConn is the connection that
	// fills it. Both set before Start, or the account is not copied.
	store          *store.Store
	mirrorConn     imapadapter.Mirror
	mirrorKick     chan struct{} // buffered(1): compare with the server now
	mirrorInterval time.Duration
	mirrorDone     chan struct{} // closed when the previous session's mirror has stopped; run goroutine only
}

// defaultProbeTimeout is how long a badge poll may take before the
// connection is treated as dead (a half-open socket after a network change).
const defaultProbeTimeout = 30 * time.Second

// NewEngine creates an Engine that dials two fresh connections for cfg.
func NewEngine(cfg *config.Account, events chan<- Event) *Engine {
	e := NewEngineWithClients(cfg, events,
		imapadapter.New(&cfg.IMAP),
		imapadapter.New(&cfg.IMAP),
	)
	e.mirrorConn = imapadapter.NewMirror(&cfg.IMAP)
	e.sender = smtpadapter.New(&cfg.SMTP)
	e.pass = ""     // only what SetPassword hands in: the lookup happens once, at startup
	e.smtpPass = "" // likewise SetSMTPPassword
	return e
}

// NewEngineWithClients creates an Engine with pre-built clients (for testing).
// It starts with the config's plain passwords as if they had been handed in,
// and with no sender (see SetSender).
func NewEngineWithClients(cfg *config.Account, events chan<- Event,
	idleConn, cmdConn imapadapter.Client) *Engine {
	return &Engine{
		cfg:      cfg,
		events:   events,
		idleConn: idleConn,
		cmdConn:  cmdConn,
		pass:     cfg.IMAP.Password,
		smtpPass: cfg.SMTP.Password,
		status:   make(map[string]mail.MailboxStatus),
		backoff:  DefaultBackoff,
		retry:    make(chan struct{}, 1),
		done:     make(chan struct{}),

		mirrorKick:     make(chan struct{}, 1),
		mirrorInterval: defaultMirrorInterval,
	}
}

// SetStore gives the engine the local mail copy: messages are read from it
// when they are there, and a third connection keeps it equal to the server.
// Call before Start.
func (e *Engine) SetStore(st *store.Store) { e.store = st }

// SetMirrorClient replaces the connection used to fill the store (for
// testing). Call before Start.
func (e *Engine) SetMirrorClient(conn imapadapter.Mirror) { e.mirrorConn = conn }

// DefaultBackoff is the reconnect delay schedule: 2s, 4s, 8s … capped at 60s.
// attempt starts at 1.
func DefaultBackoff(attempt int) time.Duration {
	const maxDelay = 60 * time.Second
	d := 2 * time.Second
	for i := 1; i < attempt && d < maxDelay; i++ {
		d *= 2
	}
	if d > maxDelay {
		d = maxDelay
	}
	return d
}

// SetBackoff overrides the retry delay schedule. Call before Start.
func (e *Engine) SetBackoff(fn func(attempt int) time.Duration) {
	e.backoff = fn
}

// Retry asks a failed engine to reconnect now instead of waiting out its
// delay. It never blocks and is a no-op while the engine is connected.
func (e *Engine) Retry() {
	if !e.failed.Load() {
		return
	}
	select {
	case e.retry <- struct{}{}:
	default:
	}
}

// SetProbeTimeout bounds how long a badge poll may take before the
// connection is considered dead. Call before Start.
func (e *Engine) SetProbeTimeout(d time.Duration) {
	e.probeTimeout = d
}

// Done is closed when the engine's background goroutine has exited.
func (e *Engine) Done() <-chan struct{} { return e.done }

// SetDraftStore wires a draft store for IMAP sync. Call before Start.
func (e *Engine) SetDraftStore(ds *draft.Store) {
	e.draftStore = ds
}

// Start launches the engine's background goroutine. Non-blocking.
func (e *Engine) Start(ctx context.Context) {
	go e.run(ctx)
}

// ErrNoPassword is wrapped by the status error when the engine was given no
// IMAP password.
var ErrNoPassword = errors.New("no password")

// ErrPasswordRejected is wrapped by the status error when the server refuses
// the password. Passwords are looked up once, at startup, so a corrected one
// is only picked up by starting m9s again.
var ErrPasswordRejected = errors.New("password rejected — fix it and restart m9s")

// SetPassword hands the engine its IMAP password. Call before Start.
func (e *Engine) SetPassword(pass string) { e.pass = pass }

// ErrNoSMTPPassword is wrapped by Send's error when the engine was given no
// SMTP password.
var ErrNoSMTPPassword = errors.New("no SMTP password")

// SetSMTPPassword hands the engine the password Send uses. Passwords are
// looked up once, at startup; nothing here looks one up. Call before Start.
func (e *Engine) SetSMTPPassword(pass string) { e.smtpPass = pass }

// SetSender replaces the SMTP sender (for testing). Call before Start.
func (e *Engine) SetSender(s smtpadapter.Sender) { e.sender = s }

// Send delivers d over SMTP with the password handed in at startup and
// returns the message exactly as it was sent.
func (e *Engine) Send(_ context.Context, d smtpadapter.Draft) ([]byte, error) {
	if e.sender == nil {
		return nil, fmt.Errorf("no sender for account %s", e.cfg.Name)
	}
	if e.smtpPass == "" {
		return nil, fmt.Errorf("%w for %s", ErrNoSMTPPassword, e.cfg.Name)
	}
	return e.sender.Send(e.smtpPass, d)
}

// emit sends ev unless ctx is cancelled first. It reports whether ev was sent.
func (e *Engine) emit(ctx context.Context, ev Event) bool {
	select {
	case e.events <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

func (e *Engine) emitStatus(ctx context.Context, st AccountStatus) bool {
	st.AccountName = e.cfg.Name
	return e.emit(ctx, st)
}

// errConnLost ends a session whose connection dropped after it was established.
var errConnLost = errors.New("connection lost")

// run is the engine's connect / serve / back-off loop. It returns only when
// ctx is cancelled.
func (e *Engine) run(ctx context.Context) {
	defer close(e.done)
	attempt := 0 // consecutive failures so far
	for {
		if !e.emitStatus(ctx, AccountStatus{State: StateConnecting}) {
			return
		}
		connected, err := e.session(ctx, attempt > 0)
		if ctx.Err() != nil {
			return
		}
		if connected {
			attempt = 0
		}
		attempt++

		// Bad credentials cannot fix themselves and repeated logins can lock
		// a mailbox, so those wait for an explicit Retry.
		retryIn := e.backoff(attempt)
		if errors.Is(err, imapadapter.ErrAuth) || errors.Is(err, ErrNoPassword) {
			retryIn = 0
		}

		select { // discard a Retry left over from an earlier wait
		case <-e.retry:
		default:
		}
		e.failed.Store(true)
		if !e.emitStatus(ctx, AccountStatus{State: StateFailed, Err: err, Attempt: attempt, RetryIn: retryIn}) {
			return
		}
		if !e.waitRetry(ctx, retryIn) {
			return
		}
		e.failed.Store(false)
	}
}

// waitRetry blocks until the delay elapses (never, when delay is 0), Retry is
// called, or ctx is cancelled. It reports false on cancellation.
func (e *Engine) waitRetry(ctx context.Context, delay time.Duration) bool {
	var elapsed <-chan time.Time
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		elapsed = timer.C
	}
	select {
	case <-ctx.Done():
		return false
	case <-e.retry:
		return true
	case <-elapsed:
		return true
	}
}

// session connects, announces the account as connected and serves it until
// ctx is cancelled or either connection is lost. connected reports whether
// the session got as far as StateConnected; err is what ended it.
func (e *Engine) session(ctx context.Context, recovered bool) (connected bool, err error) {
	// The password was looked up at startup and handed in. It is never
	// looked up again: a rejected one is kept, and the error says to restart.
	if e.pass == "" {
		return false, fmt.Errorf("%w for %q", ErrNoPassword, e.cfg.Name)
	}
	pass := e.pass
	defer func() {
		if errors.Is(err, imapadapter.ErrAuth) {
			err = fmt.Errorf("%w (%w)", ErrPasswordRejected, err)
		}
	}()

	// Everything started for this session stops when it ends.
	sctx, cancel := context.WithCancel(ctx)
	var wg gosync.WaitGroup
	defer func() {
		cancel()
		if err != nil && ctx.Err() == nil {
			// The session failed: a polite LOGOUT or IDLE DONE on a
			// half-open socket would hang, so drop both sockets first.
			e.idleConn.Abort()
			e.cmdConn.Abort()
		}
		_ = e.idleConn.Close()
		_ = e.cmdConn.Close()
		if e.mirrorConn != nil {
			// Never waited for: a mirror stuck in a dial must not delay the
			// reconnect. It notices the cancelled session and stops.
			e.mirrorConn.Abort()
		}
		wg.Wait()
	}()

	// Connect command connection first — needed for all operations.
	if err := e.cmdConn.Connect(sctx, pass); err != nil {
		return false, err
	}

	// Discover folders and emit FoldersUpdated.
	allFolders, err := e.cmdConn.Folders(sctx)
	if err != nil {
		return false, err
	}
	applied := e.applyFolderConfig(allFolders)
	e.mu.Lock()
	e.folders = applied
	e.mu.Unlock()
	if !e.emit(sctx, FoldersUpdated{AccountName: e.cfg.Name, Folders: e.handOut(applied)}) {
		return false, sctx.Err()
	}

	// Connect idle connection and start IDLE on INBOX.
	if err := e.idleConn.Connect(sctx, pass); err != nil {
		return false, fmt.Errorf("idle connection: %w", err)
	}
	notify := make(chan imapadapter.IDLENotification, 4)
	if err := e.idleConn.StartIDLE(sctx, "INBOX", notify); err != nil {
		return false, fmt.Errorf("idle: %w", err)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		e.handleIDLE(sctx, notify)
	}()

	if !e.emitStatus(sctx, AccountStatus{State: StateConnected, Recovered: recovered}) {
		return false, sctx.Err()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		e.sendServerInfo(sctx)
	}()

	e.startMirror(sctx, pass)

	// Upload drafts kept while disconnected. Safe on every reconnect: an
	// uploaded draft is no longer dirty, even on servers without UIDPLUS.
	wg.Add(1)
	go func() {
		defer wg.Done()
		e.syncDirtyDrafts(sctx)
	}()

	// Poll badges until the session ends; a poll that hangs means the
	// connection is dead even though no socket error has arrived.
	unresponsive := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		e.pollBadges(sctx, unresponsive)
	}()

	select {
	case <-ctx.Done():
		return true, ctx.Err()
	case err := <-unresponsive:
		return true, err
	case <-e.cmdConn.Closed():
		return true, errConnLost
	case <-e.idleConn.Closed():
		return true, fmt.Errorf("idle %w", errConnLost)
	}
}

func (e *Engine) handleIDLE(ctx context.Context, notify <-chan imapadapter.IDLENotification) {
	for {
		select {
		case <-ctx.Done():
			return
		case n, ok := <-notify:
			if !ok {
				return
			}
			if !e.emit(ctx, NewMail{AccountName: e.cfg.Name, FolderName: n.Folder}) {
				return
			}
			// The pushed folder's count changed too; do not wait for the poll.
			e.refreshBadge(ctx, n.Folder)
			select { // and the local copy should not wait for its next round
			case e.mirrorKick <- struct{}{}:
			default:
			}
		}
	}
}

func (e *Engine) pollBadges(ctx context.Context, unresponsive chan<- error) {
	interval := e.cfg.Folders.BadgePoll.D()
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// First pass right away, so counts do not wait a whole interval.
	if !e.probe(ctx, unresponsive) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !e.probe(ctx, unresponsive) {
				return
			}
		}
	}
}

// probe runs one badge pass and doubles as a liveness check. If the pass
// does not finish within the probe timeout it reports the connection as
// unresponsive and returns false once the stuck pass has been unblocked
// (the session aborts the sockets).
func (e *Engine) probe(ctx context.Context, unresponsive chan<- error) bool {
	timeout := e.probeTimeout
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.SyncAll(ctx)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		<-done
		return false
	case <-timer.C:
		unresponsive <- fmt.Errorf("server not responding for %s: %w", timeout, errConnLost)
		<-done
		return false
	}
}

// handOut returns copies of folders with the last-known counts attached.
// Callers (the UI) may mutate what they receive; the engine's own folders
// and counts are never shared.
func (e *Engine) handOut(folders []*mail.Folder) []*mail.Folder {
	if folders == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*mail.Folder, len(folders))
	for i, f := range folders {
		cp := *f
		cp.Status = nil
		if st, ok := e.status[f.Name]; ok {
			cp.Status = &st
		}
		out[i] = &cp
	}
	return out
}

// RefreshBadge fetches the counts for folder now and emits BadgeUpdated.
// Safe to call from any goroutine.
func (e *Engine) RefreshBadge(ctx context.Context, folder string) {
	e.refreshBadge(ctx, folder)
}

func (e *Engine) refreshBadge(ctx context.Context, folderName string) {
	status, err := e.cmdConn.Status(ctx, folderName)
	if err != nil {
		return
	}
	e.mu.Lock()
	e.status[folderName] = *status
	e.mu.Unlock()
	select {
	case e.events <- BadgeUpdated{
		AccountName: e.cfg.Name,
		FolderName:  folderName,
		Unseen:      status.Unseen,
		Total:       status.Messages,
	}:
	case <-ctx.Done():
	}
}

// SyncAll refreshes badge counts for all known folders immediately.
// Called by the TUI on the 's' keybinding.
func (e *Engine) SyncAll(ctx context.Context) {
	e.mu.Lock()
	folders := make([]*mail.Folder, len(e.folders))
	copy(folders, e.folders)
	e.mu.Unlock()
	for _, f := range folders {
		e.refreshBadge(ctx, f.Name)
	}
}

// applyFolderConfig filters allFolders according to the account's folder config.
func (e *Engine) applyFolderConfig(allFolders []*mail.Folder) []*mail.Folder {
	return applyFolderConfig(&e.cfg.Folders, allFolders)
}

// applyFolderConfig filters allFolders by a folder config: auto-detected
// folders, plus includes, minus excludes.
func applyFolderConfig(cfg *config.FolderConfig, allFolders []*mail.Folder) []*mail.Folder {
	var base []*mail.Folder
	if cfg.ShouldAutoDetect() {
		base = AutoDetect(allFolders)
	}

	// Merge explicit includes (skip duplicates already in base).
	for _, inc := range cfg.Include {
		for _, f := range allFolders {
			if f.Name == inc.Name || f.DisplayName() == inc.Name {
				alreadyIn := false
				for _, b := range base {
					if b == f {
						alreadyIn = true
						break
					}
				}
				if !alreadyIn {
					base = append(base, f)
				}
				break
			}
		}
	}

	// Apply excludes.
	excluded := make(map[string]bool, len(cfg.Exclude))
	for _, ex := range cfg.Exclude {
		excluded[ex] = true
	}
	result := base[:0:len(base)]
	for _, f := range base {
		if !excluded[f.Name] && !excluded[f.DisplayName()] {
			result = append(result, f)
		}
	}
	return result
}

// --- On-demand methods (called by TUI) ---

// FetchHeaders delegates to the command connection.
func (e *Engine) FetchHeaders(ctx context.Context, folder string, page, pageSize int) ([]*mail.Envelope, error) {
	return e.cmdConn.FetchHeaders(ctx, folder, page, pageSize)
}

// List pages a folder. The unfiltered first page comes from the server, as
// FetchHeaders does, so new mail shows at once; filtered pages, later pages
// and "all" come from the local copy (the mirror). Without a local copy the
// folder is not listable.
func (e *Engine) List(ctx context.Context, folder string, q store.ListQuery) ([]*mail.Envelope, int, error) {
	if e.store == nil {
		return nil, 0, ErrNotListable
	}
	q.Conversations = false
	if q.Filter == "" && q.Offset == 0 && q.Limit > 0 {
		rows, err := e.cmdConn.FetchHeaders(ctx, folder, 1, max(q.Limit, store.ListSize))
		if err != nil {
			return nil, 0, err
		}
		_, total, _ := e.store.Page(e.cfg.Name, folder, store.ListQuery{Limit: 1})
		return rows, max(total, len(rows)), nil
	}
	return e.store.Page(e.cfg.Name, folder, q)
}

// parseUID turns a message id back into the IMAP UID it was made from.
func parseUID(id string) (uint32, error) {
	uid, err := strconv.ParseUint(id, 10, 32)
	if err != nil || uid == 0 {
		return 0, fmt.Errorf("not an IMAP message id: %q", id)
	}
	return uint32(uid), nil
}

// FetchBody returns a message from the local copy when it has been
// downloaded — no network, so it also works offline — and from the server
// otherwise.
func (e *Engine) FetchBody(ctx context.Context, folder, id string) (*mail.Message, error) {
	if raw, ok := e.store.Body(e.cfg.Name, folder, id); ok {
		if env, ok := e.store.Envelope(e.cfg.Name, folder, id); ok {
			return mail.ParseMessage(env, raw), nil
		}
	}
	uid, err := parseUID(id)
	if err != nil {
		return nil, err
	}
	return e.cmdConn.FetchBody(ctx, folder, uid)
}

// SetFlags adds or removes flags on a message.
func (e *Engine) SetFlags(ctx context.Context, folder, id string, flags []string, add bool) error {
	uid, err := parseUID(id)
	if err != nil {
		return err
	}
	return e.cmdConn.Store(ctx, folder, uid, flags, add)
}

// Search runs query as an IMAP SEARCH (every word must appear in the
// message text) in folder and returns the newest store.ListSize matches,
// newest first.
func (e *Engine) Search(ctx context.Context, folder, query string) ([]*mail.Envelope, error) {
	res, err := e.SearchPage(ctx, folder, query, "")
	return res.Rows, err
}

// SearchPage is one page of Search: cursor is how many newer matches come
// before it ("" for the first page). IMAP lists every match, so the total
// is exact.
func (e *Engine) SearchPage(ctx context.Context, folder, query, cursor string) (SearchResult, error) {
	offset, _ := strconv.Atoi(cursor)
	uids, err := e.cmdConn.Search(ctx, folder, imapadapter.BuildSearchCriteria(query))
	if err != nil {
		return SearchResult{}, err
	}
	res := SearchResult{Total: len(uids)}
	if offset >= len(uids) {
		return res, nil
	}
	uids = slices.Clone(uids)
	slices.SortFunc(uids, func(a, b uint32) int { return cmpDesc(a, b) })
	end := min(offset+store.ListSize, len(uids))
	if end < len(uids) {
		res.Next = strconv.Itoa(end)
	}
	envs, err := e.cmdConn.FetchEnvelopes(ctx, folder, uids[offset:end])
	if err != nil {
		return SearchResult{}, err
	}
	slices.SortFunc(envs, func(a, b *mail.Envelope) int { return cmpDesc(a.UID, b.UID) })
	res.Rows = envs
	return res, nil
}

func cmpDesc(a, b uint32) int {
	switch {
	case a > b:
		return -1
	case a < b:
		return 1
	}
	return 0
}

// Folders returns the engine-resolved folder list, refreshing from server if connected.
func (e *Engine) Folders(ctx context.Context) ([]*mail.Folder, error) {
	allFolders, err := e.cmdConn.Folders(ctx)
	if err != nil {
		e.mu.Lock()
		cached := e.folders
		e.mu.Unlock()
		return e.handOut(cached), nil // return cached on error
	}
	applied := e.applyFolderConfig(allFolders)
	e.mu.Lock()
	e.folders = applied
	e.mu.Unlock()
	return e.handOut(applied), nil
}

// draftsFolderName is the server folder drafts are uploaded to: the one with
// the \Drafts attribute, else one called Drafts.
func (e *Engine) draftsFolderName() string {
	e.mu.Lock()
	folders := e.folders
	e.mu.Unlock()
	name := "Drafts"
	for _, f := range folders {
		for _, attr := range f.Attributes {
			if attr == `\Drafts` {
				return f.Name
			}
		}
		if f.DisplayName() == "Drafts" {
			name = f.Name
		}
	}
	return name
}

// SaveDraft uploads the EML bytes to the IMAP Drafts folder and returns the
// new copy's id — "" when the server does not support UIDPLUS/IMAP4rev2 and
// the copy cannot be tracked. IMAP cannot replace a message, so replaces is
// ignored: the caller removes the old copy with DeleteDraftCopy.
func (e *Engine) SaveDraft(ctx context.Context, raw []byte, _ string) (string, error) {
	uid, err := e.cmdConn.Append(ctx, e.draftsFolderName(), raw, []imap.Flag{imap.FlagDraft})
	if err != nil || uid == 0 {
		return "", err
	}
	return uidID(uid), nil
}

// DraftsFolderName is the server folder local drafts are uploaded to.
func (e *Engine) DraftsFolderName() string { return e.draftsFolderName() }

// TrashDraftCopy moves the server copy of a deleted local draft to the
// trash folder, so it can be recovered.
func (e *Engine) TrashDraftCopy(ctx context.Context, id string) error {
	uid, err := parseUID(id)
	if err != nil {
		return err
	}
	trash := e.TrashFolderName()
	if trash == "" {
		return fmt.Errorf("trash folder not found")
	}
	return e.cmdConn.Move(ctx, e.draftsFolderName(), uid, trash)
}

// DeleteDraftCopy permanently removes the server copy of a local draft that
// was replaced by a newer version or sent.
func (e *Engine) DeleteDraftCopy(ctx context.Context, id string) error {
	uid, err := parseUID(id)
	if err != nil {
		return err
	}
	return e.cmdConn.Expunge(ctx, e.draftsFolderName(), uid)
}

// TrashFolderName returns the name of the trash folder from the cached
// folder list, or "" if not found.
func (e *Engine) TrashFolderName() string {
	e.mu.Lock()
	folders := e.folders
	e.mu.Unlock()
	if f := TrashFolder(folders); f != nil {
		return f.Name
	}
	return ""
}

// SentFolderName returns the name of the folder sent mail is kept in, from
// the cached folder list, or "" if not found.
func (e *Engine) SentFolderName() string {
	e.mu.Lock()
	folders := e.folders
	e.mu.Unlock()
	if f := SentFolder(folders); f != nil {
		return f.Name
	}
	return ""
}

// ServerSavesSent reports whether the mail server keeps its own copy of
// every message sent through its SMTP (Gmail does); uploading one more
// would show the message twice.
func (e *Engine) ServerSavesSent() bool {
	host := strings.ToLower(e.cfg.IMAP.Host)
	return host == "imap.gmail.com" || host == "imap.googlemail.com"
}

// SaveSentCopy uploads msg, marked read, to the Sent folder and returns
// that folder's name. It does nothing and returns "" when the server keeps
// its own copy.
func (e *Engine) SaveSentCopy(ctx context.Context, msg []byte) (string, error) {
	if e.ServerSavesSent() {
		return "", nil
	}
	sent := e.SentFolderName()
	if sent == "" {
		return "", fmt.Errorf("sent folder not found")
	}
	if _, err := e.cmdConn.Append(ctx, sent, msg, []imap.Flag{imap.FlagSeen}); err != nil {
		return sent, err
	}
	return sent, nil
}

// MoveToTrash moves a message from folder to the trash folder.
// Returns an error if the trash folder cannot be determined.
func (e *Engine) MoveToTrash(ctx context.Context, folder, id string) error {
	uid, err := parseUID(id)
	if err != nil {
		return err
	}
	trash := e.TrashFolderName()
	if trash == "" {
		return fmt.Errorf("trash folder not found")
	}
	return e.cmdConn.Move(ctx, folder, uid, trash)
}

var _ Mover = (*Engine)(nil)

// Move moves a message from folder to dest with IMAP MOVE.
func (e *Engine) Move(ctx context.Context, folder, id, dest string) error {
	uid, err := parseUID(id)
	if err != nil {
		return err
	}
	if dest == "" || dest == folder {
		return fmt.Errorf("already in %s", folder)
	}
	return e.cmdConn.Move(ctx, folder, uid, dest)
}

// ArchiveFolderName is the folder Archive moves mail to: folders.archive
// from the config, else the one the server marks \Archive, else one called
// Archive; "" when there is none.
func (e *Engine) ArchiveFolderName() string {
	if name := strings.TrimSpace(e.cfg.Folders.Archive); name != "" {
		return name
	}
	e.mu.Lock()
	folders := e.folders
	e.mu.Unlock()
	if f := ArchiveFolder(folders); f != nil {
		return f.Name
	}
	return ""
}

// Archive moves a message from folder to the archive folder.
func (e *Engine) Archive(ctx context.Context, folder, id string) error {
	dest := e.ArchiveFolderName()
	if dest == "" {
		return ErrNoArchiveFolder
	}
	return e.Move(ctx, folder, id, dest)
}

// MoveTargets lists the shown folders a message in folder can be moved to:
// not folder itself, not Drafts, not a folder that holds no messages.
func (e *Engine) MoveTargets(folder string) []string {
	e.mu.Lock()
	folders := e.folders
	e.mu.Unlock()
	drafts := e.draftsFolderName()
	var out []string
	for _, f := range folders {
		if f.Name != folder && f.Name != drafts && !f.IsNoSelect() {
			out = append(out, f.Name)
		}
	}
	return out
}

// EmptyTrash permanently deletes every message in the trash folder and
// returns how many were removed. It refuses when no trash folder is known,
// so it can never empty another folder.
func (e *Engine) EmptyTrash(ctx context.Context) (int, error) {
	trash := e.TrashFolderName()
	if trash == "" {
		return 0, fmt.Errorf("trash folder not found")
	}
	return e.cmdConn.ExpungeAll(ctx, trash)
}

// PermanentDelete marks a message \Deleted in folder and expunges it.
func (e *Engine) PermanentDelete(ctx context.Context, folder, id string) error {
	uid, err := parseUID(id)
	if err != nil {
		return err
	}
	return e.cmdConn.Expunge(ctx, folder, uid)
}

// SyncDrafts uploads local drafts that are new or changed since their last
// upload. The UI calls it after a draft is kept, so it reaches the server's
// Drafts folder without waiting for the next start.
func (e *Engine) SyncDrafts(ctx context.Context) error {
	return e.syncDirtyDrafts(ctx)
}

// syncDirtyDrafts uploads pending drafts. A draft that fails stays pending,
// is reported with DraftSyncFailed, and the first failure is returned.
func (e *Engine) syncDirtyDrafts(ctx context.Context) error {
	e.draftMu.Lock()
	defer e.draftMu.Unlock()
	return uploadDrafts(ctx, e.cfg.Name, e.draftStore, e.SaveDraft, e.emit)
}

// uploadDrafts uploads the pending local drafts of account with save. A
// draft that fails stays pending, is reported with DraftSyncFailed, and the
// first failure is returned. The caller holds its draft lock.
func uploadDrafts(ctx context.Context, account string, ds *draft.Store,
	save func(ctx context.Context, raw []byte, replaces string) (string, error),
	emit func(ctx context.Context, ev Event) bool) error {
	if ds == nil {
		return nil
	}
	metas, err := ds.List(account)
	if err != nil {
		return err
	}
	var first error
	fail := func(m draft.Meta, err error) {
		if first == nil {
			first = err
		}
		emit(ctx, DraftSyncFailed{AccountName: account, DraftID: m.ID, Subject: m.Subject, Err: err})
	}
	for _, m := range metas {
		if !m.Dirty || m.LocalOnly {
			continue
		}
		d, err := ds.Load(account, m.ID)
		if err != nil {
			fail(m, err)
			continue
		}
		eml, err := smtpadapter.FormatDraftForUpload(d)
		if err != nil {
			fail(m, err)
			continue
		}
		syncedID, err := save(ctx, eml, m.SyncedID)
		if err != nil {
			fail(m, err)
			continue
		}
		// An empty id means the server does not support UIDPLUS/IMAP4rev2:
		// the copy is uploaded but cannot be tracked. Still clear the dirty
		// flag, or every pass would upload it again.
		_ = ds.MarkSynced(account, m.ID, syncedID)
		if !emit(ctx, DraftSynced{AccountName: account, DraftID: m.ID}) {
			return ctx.Err()
		}
	}
	return first
}

// --- Local copy ---

// startMirror runs the mirror for this session. It waits for the previous
// session's mirror to let go of the shared connection first.
func (e *Engine) startMirror(ctx context.Context, pass string) {
	if e.store == nil || e.mirrorConn == nil {
		return
	}
	prev, done := e.mirrorDone, make(chan struct{})
	e.mirrorDone = done
	go func() {
		defer close(done)
		if prev != nil {
			<-prev
		}
		e.runMirror(ctx, pass)
	}()
}

// runMirror keeps the local copy up to date until ctx ends. Trouble with the
// copy is reported and retried; it never takes the account offline.
func (e *Engine) runMirror(ctx context.Context, pass string) {
	for ctx.Err() == nil {
		err := e.mirrorSession(ctx, pass)
		if ctx.Err() != nil {
			return
		}
		if !e.emit(ctx, MirrorFailed{AccountName: e.cfg.Name, Err: err}) {
			return
		}
		timer := time.NewTimer(mirrorRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// mirrorSession connects the mirror connection and runs passes over it until
// one fails or ctx ends.
func (e *Engine) mirrorSession(ctx context.Context, pass string) error {
	if err := e.mirrorConn.Connect(ctx, pass); err != nil {
		return err
	}
	defer e.mirrorConn.Close()
	m := NewMirror(e.cfg.Name, e.mirrorConn, e.store)
	m.OnProgress(func(p MirrorProgress) { e.emit(ctx, p) })
	for {
		if err := m.Pass(ctx, e.mirrorFolders()); err != nil {
			return err
		}
		timer := time.NewTimer(e.mirrorInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-e.mirrorConn.Closed():
			timer.Stop()
			return errConnLost
		case <-e.mirrorKick:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// mirrorFolders lists the folders to copy: every folder that can hold mail,
// INBOX first.
func (e *Engine) mirrorFolders() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	names := make([]string, 0, len(e.folders))
	for _, f := range e.folders {
		if f.IsNoSelect() {
			continue
		}
		if f.Name == "INBOX" {
			names = append([]string{f.Name}, names...)
		} else {
			names = append(names, f.Name)
		}
	}
	return names
}
