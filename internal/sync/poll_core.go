package sync

import (
	"context"
	"fmt"
	gosync "sync"
	"sync/atomic"
	"time"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/store"
)

// defaultAPIPoll is how often an API account is asked what changed when
// the config gives no badge_poll.
const defaultAPIPoll = 30 * time.Second

// apiBodyWorkers is how many bodies are downloaded at once: few, so the
// list stays responsive while the copy fills.
const apiBodyWorkers = 4

// polledAPI is the provider part of an API account (Gmail, Microsoft):
// what differs between them. pollCore drives it: connecting and backing
// off, the poll timer, body downloads, draft upload and the events.
type polledAPI interface {
	// connect signs in and returns the change cursor, taken before any
	// listing so nothing that happens during the first sync is missed.
	connect(ctx context.Context) (cursor string, err error)
	// loadFolders reads the folders, applies the folder config and
	// remembers them: the shown and synced folders.
	loadFolders(ctx context.Context) ([]*mail.Folder, error)
	initialSync(ctx context.Context, cursor string) error
	// poll applies what changed since the stored cursor(s).
	poll(ctx context.Context) error
	refreshBadges(ctx context.Context)
	// bodyFolders lists the synced folders, INBOX first: the order bodies
	// are downloaded in.
	bodyFolders() []string
	getRaw(ctx context.Context, id string) ([]byte, error)
	// notFound tells an error meaning the message is gone.
	notFound(err error) bool
	// signInAgain tells an error meaning the sign-in was refused, and
	// returns the provider's sentinel the reported error wraps.
	signInAgain(err error) (sentinel error, ok bool)
	// serverInfo describes the API, for the server info line.
	serverInfo(ctx context.Context) (string, error)
	SaveDraft(ctx context.Context, raw []byte, replaces string) (string, error)
	// background marks ctx as the engine's own work (syncing, downloading
	// bodies), which the provider may let wait behind what the user does.
	background(ctx context.Context) context.Context
}

// pollCore is the provider-neutral half of an API account engine. Every
// synced folder is mirrored into the store (one body per message for the
// whole account); changes are found by polling.
type pollCore struct {
	cfg    *config.Account
	events chan<- Event
	store  *store.Store
	drafts *draft.Store
	api    polledAPI

	mu      gosync.Mutex
	folders []*mail.Folder                // shown and synced, after the folder config
	status  map[string]mail.MailboxStatus // last-known counts per folder name

	backoff func(attempt int) time.Duration
	reauth  func(ctx context.Context) error // reads the sign-in again; nil: only a restart does
	retry   chan struct{}                   // buffered(1): wakes a failed engine early
	failed  atomic.Bool
	done    chan struct{}

	serverInfoSent atomic.Bool // the API was described this run

	pollKick chan struct{} // buffered(1): ask what changed now
	bodyKick chan struct{} // buffered(1): download missing bodies now

	syncMu  gosync.Mutex // one poll or snapshot at a time
	applyMu gosync.Mutex // the store rows of one message change together
	draftMu gosync.Mutex // one draft upload pass at a time
}

// newPollCore makes the core of an API engine; the engine sets api to its
// provider part before Start.
func newPollCore(cfg *config.Account, events chan<- Event, st *store.Store, ds *draft.Store) *pollCore {
	return &pollCore{
		cfg:      cfg,
		events:   events,
		store:    st,
		drafts:   ds,
		status:   map[string]mail.MailboxStatus{},
		backoff:  DefaultBackoff,
		retry:    make(chan struct{}, 1),
		done:     make(chan struct{}),
		pollKick: make(chan struct{}, 1),
		bodyKick: make(chan struct{}, 1),
	}
}

// SetBackoff overrides the retry delay schedule. Call before Start.
func (c *pollCore) SetBackoff(fn func(attempt int) time.Duration) { c.backoff = fn }

// SetReauth gives the engine a way to read the account's sign-in again
// (the client secret and refresh token, wherever they are kept) and hand it
// to the API client. After "sign in again", the next Retry calls it before
// connecting, so a new m9s login takes effect without a restart. Call
// before Start.
func (c *pollCore) SetReauth(fn func(ctx context.Context) error) { c.reauth = fn }

// signInHint says what the owner does after the provider refused the sign-in.
func (c *pollCore) signInHint() string {
	if c.reauth != nil {
		return fmt.Sprintf("m9s login %s, then retry (S)", c.cfg.Name)
	}
	return fmt.Sprintf("m9s login %s, then restart m9s", c.cfg.Name)
}

// Start launches the engine's background goroutine. Non-blocking.
func (c *pollCore) Start(ctx context.Context) { go c.run(ctx) }

// Done is closed when the engine's background goroutine has exited.
func (c *pollCore) Done() <-chan struct{} { return c.done }

// Retry asks a failed engine to connect now instead of waiting out its
// delay. After "sign in again" it first reads the sign-in again when
// SetReauth gave a way to; without one, a new m9s login takes effect only
// once m9s restarts. Never blocks.
func (c *pollCore) Retry() {
	if !c.failed.Load() {
		return
	}
	select {
	case c.retry <- struct{}{}:
	default:
	}
}

func kick(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (c *pollCore) emit(ctx context.Context, ev Event) bool {
	select {
	case c.events <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *pollCore) emitStatus(ctx context.Context, st AccountStatus) bool {
	st.AccountName = c.cfg.Name
	return c.emit(ctx, st)
}

// run is the connect / serve / back-off loop. It returns only when ctx is
// cancelled.
func (c *pollCore) run(ctx context.Context) {
	defer close(c.done)
	attempt := 0
	reread := false // the sign-in was refused: read it again before connecting
	for {
		if !c.emitStatus(ctx, AccountStatus{State: StateConnecting}) {
			return
		}
		var reauthErr error
		if reread && c.reauth != nil {
			reauthErr = c.reauth(ctx)
		}
		var connected bool
		var err error
		if reauthErr != nil {
			err = fmt.Errorf("reading the sign-in again: %w", reauthErr)
		} else {
			connected, err = c.session(ctx, attempt > 0)
		}
		if ctx.Err() != nil {
			return
		}
		if connected {
			attempt = 0
		}
		attempt++

		// A revoked sign-in cannot fix itself, and neither can a sign-in that
		// could not be read: wait for the owner, who signs in again (or
		// unlocks KeePassXC) and retries, or restarts.
		retryIn := c.backoff(attempt)
		sentinel, refused := c.api.signInAgain(err)
		reread = reauthErr != nil || refused
		if reread {
			retryIn = 0
		}
		if refused {
			err = fmt.Errorf("%w: %s", sentinel, c.signInHint())
		}
		select { // discard a Retry left over from an earlier wait
		case <-c.retry:
		default:
		}
		c.failed.Store(true)
		if !c.emitStatus(ctx, AccountStatus{State: StateFailed, Err: err, Attempt: attempt, RetryIn: retryIn}) {
			return
		}
		if !c.waitRetry(ctx, retryIn) {
			return
		}
		c.failed.Store(false)
	}
}

// waitRetry blocks until delay elapses (never, when 0), Retry is called,
// or ctx ends; it reports false on cancellation.
func (c *pollCore) waitRetry(ctx context.Context, delay time.Duration) bool {
	var elapsed <-chan time.Time
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		elapsed = timer.C
	}
	select {
	case <-ctx.Done():
		return false
	case <-c.retry:
		return true
	case <-elapsed:
		return true
	}
}

// session signs in, announces the folders, syncs and then polls until an
// error or ctx ends it. connected reports whether it reached StateConnected.
func (c *pollCore) session(ctx context.Context, recovered bool) (connected bool, err error) {
	sctx, cancel := context.WithCancel(c.api.background(ctx))
	var wg gosync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()

	cursor, err := c.api.connect(sctx) // taken before any listing
	if err != nil {
		return false, err
	}
	folders, err := c.api.loadFolders(sctx)
	if err != nil {
		return false, err
	}
	if !c.emit(sctx, FoldersUpdated{AccountName: c.cfg.Name, Folders: c.handOut(folders)}) {
		return false, sctx.Err()
	}
	if !c.emitStatus(sctx, AccountStatus{State: StateConnected, Recovered: recovered}) {
		return false, sctx.Err()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.sendServerInfo(sctx)
	}()
	if err := c.api.initialSync(sctx, cursor); err != nil {
		return true, err
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		c.bodyLoop(sctx)
	}()
	go func() {
		defer wg.Done()
		_ = c.SyncDrafts(sctx)
	}()
	return true, c.pollLoop(sctx)
}

// pollLoop asks what changed every badge_poll, or sooner when kicked.
func (c *pollCore) pollLoop(ctx context.Context) error {
	interval := c.cfg.Folders.BadgePoll.D()
	if interval <= 0 {
		interval = defaultAPIPoll
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-c.pollKick:
		}
		if err := c.api.poll(ctx); err != nil {
			return err
		}
	}
}

// handOut returns copies of folders with the last-known counts attached.
func (c *pollCore) handOut(folders []*mail.Folder) []*mail.Folder {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*mail.Folder, len(folders))
	for i, f := range folders {
		cp := *f
		cp.Status = nil
		if st, ok := c.status[f.Name]; ok {
			cp.Status = &st
		}
		out[i] = &cp
	}
	return out
}

// SyncAll asks what changed now; counts follow when anything did.
func (c *pollCore) SyncAll(context.Context) { kick(c.pollKick) }

// SyncDrafts uploads local drafts that are new or changed.
func (c *pollCore) SyncDrafts(ctx context.Context) error {
	c.draftMu.Lock()
	defer c.draftMu.Unlock()
	return uploadDrafts(ctx, c.cfg.Name, c.drafts, c.api.SaveDraft, c.emit)
}

// sendServerInfo describes the API, once per engine run.
func (c *pollCore) sendServerInfo(ctx context.Context) {
	if c.serverInfoSent.Load() {
		return
	}
	text, err := c.api.serverInfo(ctx)
	if err != nil {
		return
	}
	c.serverInfoSent.Store(true)
	c.emit(ctx, ServerInfo{AccountName: c.cfg.Name, Text: text})
}

// --- bodies ---

// bodyLoop downloads missing bodies after each sync and every
// body_cache_poll, until ctx ends. Trouble is reported, never fatal.
func (c *pollCore) bodyLoop(ctx context.Context) {
	interval := c.cfg.Folders.BodyCachePoll.D()
	if interval <= 0 {
		interval = defaultMirrorInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := c.downloadBodies(ctx); err != nil && ctx.Err() == nil {
			c.emit(ctx, MirrorFailed{AccountName: c.cfg.Name, Err: err})
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-c.bodyKick:
		}
	}
}

// downloadBodies fetches every body not on disk yet, INBOX first, newest
// first, each message once however many folders hold it.
func (c *pollCore) downloadBodies(ctx context.Context) error {
	seen := map[string]bool{}
	var ids []string
	for _, folder := range c.api.bodyFolders() {
		missing, err := c.store.MissingBodies(c.cfg.Name, folder)
		if err != nil {
			return err
		}
		for _, id := range missing {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	report := func(done bool) {
		messages, downloaded := c.store.Counts(c.cfg.Name)
		c.emit(ctx, MirrorProgress{AccountName: c.cfg.Name, Messages: messages, Downloaded: downloaded, Done: done})
	}
	report(false)
	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu gosync.Mutex
	var first error
	saved := 0
	work := make(chan string)
	var wg gosync.WaitGroup
	for range min(apiBodyWorkers, len(ids)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range work {
				raw, err := c.api.getRaw(dctx, id)
				if err == nil {
					err = c.store.PutBody(c.cfg.Name, "", id, raw)
				}
				progress := false
				mu.Lock()
				switch {
				case err == nil:
					saved++
					progress = saved%progressEvery == 0
				case c.api.notFound(err): // deleted meanwhile
				case first == nil:
					first = err
					cancel()
				}
				mu.Unlock()
				if progress {
					report(false)
				}
			}
		}()
	}
feed:
	for _, id := range ids {
		select {
		case work <- id:
		case <-dctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	if first != nil {
		return first
	}
	report(true)
	return ctx.Err()
}
