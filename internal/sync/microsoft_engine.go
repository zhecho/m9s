package sync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/mail/graph"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	"github.com/zhecho/m9s/internal/store"
)

// Folder names m9s gives the special Outlook folders, whatever the mailbox
// calls them in its language.
const (
	msInbox   = "INBOX"
	msSent    = "Sent"
	msDrafts  = "Drafts"
	msTrash   = "Trash"
	msSpam    = "Spam"
	msArchive = "Archive"
)

// msWellKnown maps Graph's well-known folder names to m9s names and the
// IMAP attribute the rest of m9s looks for, in the order they are listed.
var msWellKnown = []struct{ wk, name, attr string }{
	{"inbox", msInbox, ""},
	{"sentitems", msSent, `\Sent`},
	{"drafts", msDrafts, `\Drafts`},
	{"archive", msArchive, `\Archive`},
	{"junkemail", msSpam, `\Junk`},
	{"deleteditems", msTrash, `\Trash`},
}

// msSlash stands in for a "/" inside a folder's display name, so that a
// folder called "Projects/2026" is not read as 2026 inside Projects.
const msSlash = "∕" // U+2215 DIVISION SLASH

// fullRecheck is how often every synced folder is listed again from
// scratch, for the rare changes Graph's delta feed misses.
const fullRecheck = 24 * time.Hour

// MicrosoftEngine is an Account backed by Microsoft Graph. Every synced
// folder is mirrored into the store, one shared body per message (an id
// never changes when the message moves); changes are found by polling each
// folder's delta link. The polling itself is pollCore's.
type MicrosoftEngine struct {
	*pollCore
	api *graph.Client

	// Guarded by pollCore.mu.
	idOf   map[string]string // folder name -> Graph folder id, every folder
	nameOf map[string]string // Graph folder id -> folder name
	cats   []string          // the account's category names

	lastFull time.Time // when every folder was last listed (guarded by syncMu)
}

var (
	_ Account      = (*MicrosoftEngine)(nil)
	_ Labels       = (*MicrosoftEngine)(nil)
	_ Mover        = (*MicrosoftEngine)(nil)
	_ ServerSearch = (*MicrosoftEngine)(nil)
	_ StoreKeeper  = (*MicrosoftEngine)(nil)
	_ polledAPI    = (*MicrosoftEngine)(nil)
)

// NewMicrosoftEngine creates the engine of a Microsoft account. api must
// already carry the account's sign-in.
func NewMicrosoftEngine(cfg *config.Account, events chan<- Event, st *store.Store, ds *draft.Store, api *graph.Client) *MicrosoftEngine {
	e := &MicrosoftEngine{pollCore: newPollCore(cfg, events, st, ds), api: api,
		idOf: map[string]string{}, nameOf: map[string]string{}}
	e.pollCore.api = e
	return e
}

// KeepsStore is true: every synced folder is mirrored into the store by the
// engine, which is the only writer of its rows.
func (e *MicrosoftEngine) KeepsStore() bool { return true }

// --- the Microsoft part pollCore drives ---

// connect checks the sign-in. The cursors are per folder, in the store.
func (e *MicrosoftEngine) connect(ctx context.Context) (string, error) {
	_, err := e.api.Me(ctx)
	return "", err
}

func (e *MicrosoftEngine) bodyFolders() []string { return folderNames(e.syncedFolders()) }

func (e *MicrosoftEngine) getRaw(ctx context.Context, id string) ([]byte, error) {
	return e.api.Raw(ctx, id)
}

func (e *MicrosoftEngine) notFound(err error) bool { return errors.Is(err, graph.ErrNotFound) }

// background leaves ctx as it is: Graph's limiter has no priorities.
func (e *MicrosoftEngine) background(ctx context.Context) context.Context { return ctx }

func (e *MicrosoftEngine) signInAgain(err error) (error, bool) {
	return graph.ErrSignInAgain, errors.Is(err, graph.ErrSignInAgain)
}

func (e *MicrosoftEngine) serverInfo(ctx context.Context) (string, error) { return e.api.APIInfo(ctx) }

// --- folders ---

// loadFolders reads the folder tree, names the folders, keeps those the
// config selects and remembers every folder's id and counts. The category
// list is read along with it.
func (e *MicrosoftEngine) loadFolders(ctx context.Context) ([]*mail.Folder, error) {
	remote, err := e.api.Folders(ctx)
	if err != nil {
		return nil, err
	}
	rank := func(wk string) int {
		for i, w := range msWellKnown {
			if w.wk == wk {
				return i
			}
		}
		return len(msWellKnown)
	}
	nameOf := map[string]string{}
	idOf := map[string]string{}
	status := map[string]mail.MailboxStatus{}
	var all []*mail.Folder
	ranks := map[*mail.Folder]int{}
	for _, rf := range remote { // parents come before their children
		f := &mail.Folder{Delimiter: "/"}
		name := strings.ReplaceAll(rf.DisplayName, "/", msSlash)
		for _, w := range msWellKnown {
			if w.wk == rf.WellKnown {
				name = w.name
				if w.attr != "" {
					f.Attributes = []string{w.attr}
				}
			}
		}
		if rf.WellKnown == "" {
			if parent, ok := nameOf[rf.ParentID]; ok {
				name = parent + "/" + name
			}
		}
		base := name
		for n := 2; idOf[name] != ""; n++ { // two folders, one name: keep both
			name = fmt.Sprintf("%s (%d)", base, n)
		}
		f.Name = name
		nameOf[rf.ID], idOf[name] = name, rf.ID
		status[name] = mail.MailboxStatus{Messages: uint32(rf.Total), Unseen: uint32(rf.Unread)}
		ranks[f] = rank(rf.WellKnown)
		all = append(all, f)
	}
	slices.SortStableFunc(all, func(a, b *mail.Folder) int { return ranks[a] - ranks[b] })
	applied := applyFolderConfig(&e.cfg.Folders, all)
	cats, catErr := e.api.MasterCategories(ctx)

	e.mu.Lock()
	e.folders, e.idOf, e.nameOf = applied, idOf, nameOf
	for name, st := range status {
		e.status[name] = st
	}
	if catErr == nil {
		e.cats = cats
	}
	e.mu.Unlock()
	return applied, nil
}

// folderID is the Graph id of a folder name, "" when unknown.
func (e *MicrosoftEngine) folderID(name string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.idOf[name]
}

// Folders re-reads the folders; on failure it returns the last known ones.
func (e *MicrosoftEngine) Folders(ctx context.Context) ([]*mail.Folder, error) {
	folders, err := e.loadFolders(ctx)
	if err != nil {
		e.mu.Lock()
		folders = e.folders
		e.mu.Unlock()
	}
	return e.handOut(folders), nil
}

// RefreshBadge reads the counts of folder and emits BadgeUpdated.
func (e *MicrosoftEngine) RefreshBadge(ctx context.Context, folder string) {
	id := e.folderID(folder)
	if id == "" {
		return
	}
	f, err := e.api.Folder(ctx, id)
	if err != nil {
		return
	}
	st := mail.MailboxStatus{Messages: uint32(f.Total), Unseen: uint32(f.Unread)}
	e.mu.Lock()
	e.status[folder] = st
	e.mu.Unlock()
	e.emit(ctx, BadgeUpdated{AccountName: e.cfg.Name, FolderName: folder, Unseen: st.Unseen, Total: st.Messages})
}

func (e *MicrosoftEngine) refreshBadges(ctx context.Context) {
	for _, f := range e.syncedFolders() {
		e.RefreshBadge(ctx, f.name)
	}
}

func (e *MicrosoftEngine) DraftsFolderName() string { return msDrafts }
func (e *MicrosoftEngine) TrashFolderName() string  { return msTrash }
func (e *MicrosoftEngine) SentFolderName() string   { return msSent }

// --- reading ---

// FetchHeaders serves the newest messages of folder from the store, where
// every synced folder is mirrored. Only page 1 exists.
func (e *MicrosoftEngine) FetchHeaders(_ context.Context, folder string, page, pageSize int) ([]*mail.Envelope, error) {
	if page > 1 {
		return nil, nil
	}
	envs, _ := e.store.Envelopes(e.cfg.Name, folder)
	if pageSize > 0 && len(envs) > pageSize {
		envs = envs[len(envs)-pageSize:] // oldest first: keep the newest
	}
	return envs, nil
}

// List pages a folder from the store, filtered over the whole folder.
func (e *MicrosoftEngine) List(_ context.Context, folder string, q store.ListQuery) ([]*mail.Envelope, int, error) {
	q.Conversations = false
	return e.store.Page(e.cfg.Name, folder, q)
}

// FetchBody reads the stored body, or downloads it (and keeps it).
func (e *MicrosoftEngine) FetchBody(ctx context.Context, folder, id string) (*mail.Message, error) {
	env, known := e.store.Envelope(e.cfg.Name, folder, id)
	if known {
		if raw, ok := e.store.Body(e.cfg.Name, "", id); ok {
			return mail.ParseMessage(env, raw), nil
		}
	}
	raw, err := e.api.Raw(ctx, id)
	if err != nil {
		return nil, err
	}
	if known {
		_ = e.store.PutBody(e.cfg.Name, "", id, raw)
	} else {
		m, err := e.api.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		env = e.envelopeOf(m)
	}
	return mail.ParseMessage(env, raw), nil
}

// --- changing messages ---

// SetFlags maps \Seen to read and \Flagged to Outlook's flag; other flags
// have no Outlook meaning and are ignored.
func (e *MicrosoftEngine) SetFlags(ctx context.Context, folder, id string, flags []string, add bool) error {
	changed := false
	for _, f := range flags {
		var err error
		switch f {
		case `\Seen`:
			err = e.api.SetRead(ctx, id, add)
		case `\Flagged`:
			err = e.api.SetFlagged(ctx, id, add)
		default:
			continue
		}
		if err != nil {
			return err
		}
		changed = true
	}
	if !changed {
		return nil
	}
	defer kick(e.pollKick)
	env, ok := e.store.Envelope(e.cfg.Name, folder, id)
	if !ok {
		return nil
	}
	next := slices.Clone(env.Flags)
	for _, f := range flags {
		if f != `\Seen` && f != `\Flagged` {
			continue
		}
		next = slices.DeleteFunc(next, func(x string) bool { return x == f })
		if add {
			next = append(next, f)
		}
	}
	e.applyMu.Lock()
	defer e.applyMu.Unlock()
	return e.store.SetFlagsEverywhere(e.cfg.Name, map[string][]string{id: next})
}

// Move moves a message from folder to dest, a folder name as listed. The
// id stays the same, and so does its stored body.
func (e *MicrosoftEngine) Move(ctx context.Context, folder, id, dest string) error {
	if dest == "" || dest == folder {
		return fmt.Errorf("already in %s", folder)
	}
	destID := e.folderID(dest)
	if destID == "" {
		return fmt.Errorf("no folder %q on this account", dest)
	}
	if _, err := e.api.Move(ctx, id, destID); err != nil {
		return err
	}
	e.moveRows(folder, []string{id}, dest)
	return nil
}

// moveRows shows a move at once: the rows leave folder and, when dest is
// synced, appear there. The next poll confirms it.
func (e *MicrosoftEngine) moveRows(folder string, ids []string, dest string) {
	defer kick(e.pollKick)
	var envs []*mail.Envelope
	for _, id := range ids {
		if env, ok := e.store.Envelope(e.cfg.Name, folder, id); ok {
			env.Flags = slices.DeleteFunc(env.Flags, func(x string) bool { return x == `\Draft` })
			envs = append(envs, env)
		}
	}
	e.applyMu.Lock()
	defer e.applyMu.Unlock()
	_ = e.store.RemoveMessages(e.cfg.Name, folder, ids)
	if len(envs) == 0 {
		return
	}
	for _, f := range e.syncedFolders() {
		if f.name == dest {
			_ = e.store.AddMessages(e.cfg.Name, dest, envs)
		}
	}
}

// MoveToTrash moves a message to Deleted Items.
func (e *MicrosoftEngine) MoveToTrash(ctx context.Context, folder, id string) error {
	return e.Move(ctx, folder, id, msTrash)
}

// MoveToTrashMany moves ids to Deleted Items in $batch requests of
// graph.BatchMax moves (BulkTrasher).
func (e *MicrosoftEngine) MoveToTrashMany(ctx context.Context, folder string, ids []string, progress func(int)) map[string]error {
	failed := make(map[string]error)
	if folder == msTrash {
		for _, id := range ids {
			failed[id] = fmt.Errorf("already in %s", folder)
		}
		progress(len(ids))
		return failed
	}
	trash := e.folderID(msTrash)
	if trash == "" {
		err := fmt.Errorf("no folder %q on this account", msTrash)
		for _, id := range ids {
			failed[id] = err
		}
		progress(len(ids))
		return failed
	}
	for part := range slices.Chunk(ids, graph.BatchMax) {
		var moved []string
		for i, err := range e.api.MoveAll(ctx, part, trash) {
			if err != nil {
				failed[part[i]] = err
			} else {
				moved = append(moved, part[i])
			}
		}
		if len(moved) > 0 {
			e.moveRows(folder, moved, msTrash)
		}
		progress(len(part))
	}
	return failed
}

// noArchiveError is Archive on an account without an archive folder.
type noArchiveError struct{}

func (noArchiveError) Error() string        { return "no Archive folder on this account" }
func (noArchiveError) Is(target error) bool { return target == ErrNoArchiveFolder }

// ArchiveFolderName is the folder Archive moves mail to: folders.archive
// from the config, else Outlook's Archive; "" when there is none.
func (e *MicrosoftEngine) ArchiveFolderName() string {
	if name := strings.TrimSpace(e.cfg.Folders.Archive); name != "" {
		return name
	}
	if e.folderID(msArchive) != "" {
		return msArchive
	}
	return ""
}

// Archive moves a message from folder to the archive folder.
func (e *MicrosoftEngine) Archive(ctx context.Context, folder, id string) error {
	dest := e.ArchiveFolderName()
	if dest == "" {
		return noArchiveError{}
	}
	return e.Move(ctx, folder, id, dest)
}

// MoveTargets lists the shown folders a message in folder can be moved to:
// not folder itself and not Drafts.
func (e *MicrosoftEngine) MoveTargets(folder string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, f := range e.folders {
		if f.Name != folder && f.Name != msDrafts {
			out = append(out, f.Name)
		}
	}
	return out
}

// PermanentDelete deletes a message for good — only while Graph still has
// it in Deleted Items, so a stale row can never delete mail that was
// restored meanwhile. One already deleted for good (Exchange keeps it in a
// hidden folder for a while) is done.
func (e *MicrosoftEngine) PermanentDelete(ctx context.Context, _ string, id string) error {
	defer kick(e.pollKick)
	m, err := e.api.Get(ctx, id)
	if errors.Is(err, graph.ErrNotFound) {
		return e.forget([]string{id})
	}
	if err != nil {
		return err
	}
	if m.FolderID != e.folderID(msTrash) {
		e.mu.Lock()
		_, visible := e.nameOf[m.FolderID]
		e.mu.Unlock()
		if !visible { // in Recoverable Items: already deleted for good
			return e.forget([]string{id})
		}
		return ErrNotInTrash
	}
	if err := e.api.PermanentDelete(ctx, id); err != nil {
		return err
	}
	return e.forget([]string{id})
}

// PermanentDeleteMany deletes ids for good in $batch requests, after one
// listing of Deleted Items (BulkDeleter).
func (e *MicrosoftEngine) PermanentDeleteMany(ctx context.Context, folder string, ids []string, progress func(int)) map[string]error {
	defer kick(e.pollKick)
	return permanentDeleteMany(ctx, ids, graph.BatchMax, progress,
		func(ctx context.Context) ([]string, error) {
			trash := e.folderID(msTrash)
			if trash == "" {
				return nil, errors.New("no Deleted Items folder on this account")
			}
			msgs, err := e.api.List(ctx, trash)
			if err != nil {
				return nil, err
			}
			out := make([]string, 0, len(msgs))
			for _, m := range msgs {
				out = append(out, m.ID)
			}
			return out, nil
		},
		e.api.PermanentDeleteAll, e.forget,
		func(ctx context.Context, id string) error { return e.PermanentDelete(ctx, folder, id) })
}

// EmptyTrash deletes every message in Deleted Items for good.
func (e *MicrosoftEngine) EmptyTrash(ctx context.Context) (int, error) {
	trash := e.folderID(msTrash)
	if trash == "" {
		return 0, errors.New("no Deleted Items folder on this account")
	}
	msgs, err := e.api.List(ctx, trash)
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}
	if err := e.api.PermanentDeleteAll(ctx, ids); err != nil {
		return 0, err
	}
	kick(e.pollKick)
	return len(ids), e.forget(ids)
}

// --- drafts ---

// SaveDraft stores raw as a new draft and returns its id. Graph cannot
// change a draft made from MIME, so the draft replaces is deleted; if that
// fails, the old copy stays in Drafts rather than the new one being lost.
func (e *MicrosoftEngine) SaveDraft(ctx context.Context, raw []byte, replaces string) (string, error) {
	defer kick(e.pollKick)
	id, err := e.api.CreateDraftMIME(ctx, raw)
	if err != nil {
		return "", err
	}
	if replaces != "" && replaces != id {
		if err := e.api.PermanentDelete(ctx, replaces); err == nil || errors.Is(err, graph.ErrNotFound) {
			_ = e.forget([]string{replaces})
		}
	}
	return id, nil
}

// TrashDraftCopy moves the server draft of a deleted local draft to
// Deleted Items.
func (e *MicrosoftEngine) TrashDraftCopy(ctx context.Context, id string) error {
	defer kick(e.pollKick)
	if _, err := e.api.Move(ctx, id, "deleteditems"); err != nil && !errors.Is(err, graph.ErrNotFound) {
		return err
	}
	e.moveRows(msDrafts, []string{id}, msTrash)
	return nil
}

// DeleteDraftCopy deletes a server draft for good; one already gone is done.
func (e *MicrosoftEngine) DeleteDraftCopy(ctx context.Context, id string) error {
	defer kick(e.pollKick)
	if err := e.api.PermanentDelete(ctx, id); err != nil && !errors.Is(err, graph.ErrNotFound) {
		return err
	}
	return e.forget([]string{id})
}

// --- sending ---

// Send sends d through Graph, which files it in Sent Items itself.
func (e *MicrosoftEngine) Send(ctx context.Context, d smtpadapter.Draft) ([]byte, error) {
	raw, err := smtpadapter.BuildMessage(d)
	if err != nil {
		return nil, err
	}
	if err := e.api.SendMIME(ctx, raw); err != nil {
		return nil, err
	}
	kick(e.pollKick)
	return raw, nil
}

// ServerSavesSent is true: Graph keeps every sent message in Sent Items.
func (e *MicrosoftEngine) ServerSavesSent() bool { return true }

// SaveSentCopy saves nothing — Graph already did — and names Sent.
func (e *MicrosoftEngine) SaveSentCopy(context.Context, []byte) (string, error) {
	return msSent, nil
}

// --- categories (Labels) ---

// AccountLabels lists the account's Outlook categories, for the picker.
func (e *MicrosoftEngine) AccountLabels() []*mail.Folder {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*mail.Folder, 0, len(e.cats))
	for _, c := range e.cats {
		out = append(out, &mail.Folder{Name: c})
	}
	return out
}

// CurrentLabels reads message id's categories from the store. Microsoft
// rows are single messages: threadID is not used.
func (e *MicrosoftEngine) CurrentLabels(_ string, id string) []string {
	for _, f := range e.syncedFolders() {
		if env, ok := e.store.Envelope(e.cfg.Name, f.name, id); ok {
			return slices.Clone(env.Categories)
		}
	}
	return nil
}

// ModifyLabels adds and removes categories on one message.
func (e *MicrosoftEngine) ModifyLabels(ctx context.Context, _ string, id string, add, remove []string) error {
	cur := e.CurrentLabels("", id)
	next := slices.DeleteFunc(slices.Clone(cur), func(c string) bool { return slices.Contains(remove, c) })
	for _, c := range add {
		if !slices.Contains(next, c) {
			next = append(next, c)
		}
	}
	if slices.Equal(cur, next) {
		return nil
	}
	if err := e.api.SetCategories(ctx, id, next); err != nil {
		return err
	}
	defer kick(e.pollKick)
	e.applyMu.Lock()
	defer e.applyMu.Unlock()
	for _, f := range e.syncedFolders() {
		if env, ok := e.store.Envelope(e.cfg.Name, f.name, id); ok {
			env.Categories = next
			if err := e.store.AddMessages(e.cfg.Name, f.name, []*mail.Envelope{env}); err != nil {
				return err
			}
		}
	}
	return nil
}

// --- search ---

// Search runs an Outlook search over the whole mailbox. Nothing is stored.
// SearchPage is one page of an Outlook search; cursor is the next page's
// link. Outlook does not say how many match.
func (e *MicrosoftEngine) SearchPage(ctx context.Context, _ string, query, cursor string) (SearchResult, error) {
	msgs, next, err := e.api.SearchPage(ctx, query, cursor)
	if err != nil {
		return SearchResult{}, err
	}
	rows := make([]*mail.Envelope, 0, len(msgs))
	for _, m := range msgs {
		rows = append(rows, e.envelopeOf(m))
	}
	return SearchResult{Rows: rows, Next: next}, nil
}

func (e *MicrosoftEngine) Search(ctx context.Context, _ string, query string) ([]*mail.Envelope, error) {
	msgs, err := e.api.Search(ctx, query)
	if err != nil {
		return nil, err
	}
	envs := make([]*mail.Envelope, 0, len(msgs))
	for _, m := range msgs {
		envs = append(envs, e.envelopeOf(m))
	}
	return envs, nil
}
