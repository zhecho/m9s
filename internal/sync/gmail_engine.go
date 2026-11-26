package sync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/mail/gmail"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	"github.com/zhecho/m9s/internal/store"
)

// Folder names of the Gmail system labels m9s acts on.
const (
	gmailInbox  = "INBOX"
	gmailSent   = "Sent"
	gmailDrafts = "Drafts"
	gmailTrash  = "Trash"
	gmailSpam   = "Spam"
)

// GmailEngine is an Account backed by the Gmail REST API. Every synced label
// is mirrored into the store (one row per label, one shared body per
// message); changes are found by polling Gmail's history. The polling
// itself is pollCore's; GmailEngine is its Gmail part.
type GmailEngine struct {
	*pollCore
	api *gmail.Client

	labelOf map[string]string // folder name -> label id, for every shown folder (guarded by mu)

	runCtx  context.Context // the context Start was given (guarded by mu)
	allMail allMailState    // All Mail's last listing (guarded by mu)
}

var (
	_ Account     = (*GmailEngine)(nil)
	_ Threads     = (*GmailEngine)(nil)
	_ Labels      = (*GmailEngine)(nil)
	_ Mover       = (*GmailEngine)(nil)
	_ DraftCopies = (*GmailEngine)(nil)
	_ StoreKeeper = (*GmailEngine)(nil)
)

// KeepsStore is true: every synced label is mirrored into the store by the
// engine, which is the only writer of its rows.
func (e *GmailEngine) KeepsStore() bool { return true }

// NewGmailEngine creates the engine of a Gmail account. api must already
// carry the account's sign-in.
func NewGmailEngine(cfg *config.Account, events chan<- Event, st *store.Store, ds *draft.Store, api *gmail.Client) *GmailEngine {
	e := &GmailEngine{pollCore: newPollCore(cfg, events, st, ds), api: api, labelOf: map[string]string{}}
	e.pollCore.api = e
	return e
}

// --- the Gmail part pollCore drives ---

var _ polledAPI = (*GmailEngine)(nil)

// connect reads the profile: its history id is the cursor.
func (e *GmailEngine) connect(ctx context.Context) (string, error) {
	_, historyID, err := e.api.Profile(ctx)
	return historyID, err
}

func (e *GmailEngine) bodyFolders() []string { return folderNames(e.syncedFolders()) }

func (e *GmailEngine) getRaw(ctx context.Context, id string) ([]byte, error) {
	m, err := e.api.GetRaw(ctx, id)
	if err != nil {
		return nil, err
	}
	return m.Raw, nil
}

func (e *GmailEngine) notFound(err error) bool { return errors.Is(err, gmail.ErrNotFound) }

// background lets the sync's requests wait behind the user's within
// Gmail's quota.
func (e *GmailEngine) background(ctx context.Context) context.Context { return gmail.Background(ctx) }

func (e *GmailEngine) signInAgain(err error) (error, bool) {
	return gmail.ErrSignInAgain, errors.Is(err, gmail.ErrSignInAgain)
}

// serverInfo reads the Gmail API's description.
func (e *GmailEngine) serverInfo(ctx context.Context) (string, error) { return e.api.APIInfo(ctx) }

// --- folders ---

// Folders re-reads the labels; on failure it returns the last known folders.
func (e *GmailEngine) Folders(ctx context.Context) ([]*mail.Folder, error) {
	folders, err := e.loadFolders(ctx)
	if err != nil {
		e.mu.Lock()
		folders = withAllMail(e.folders)
		e.mu.Unlock()
	}
	return e.handOut(folders), nil
}

// RefreshBadge fetches the conversation counts of folder and emits BadgeUpdated.
func (e *GmailEngine) RefreshBadge(ctx context.Context, folder string) {
	e.mu.Lock()
	label, ok := e.labelOf[folder]
	e.mu.Unlock()
	if !ok {
		return
	}
	l, err := e.api.Label(ctx, label)
	if err != nil {
		return
	}
	st := mail.MailboxStatus{Messages: uint32(l.ThreadsTotal), Unseen: uint32(l.ThreadsUnread)}
	e.mu.Lock()
	e.status[folder] = st
	e.mu.Unlock()
	e.emit(ctx, BadgeUpdated{AccountName: e.cfg.Name, FolderName: folder, Unseen: st.Unseen, Total: st.Messages})
}

func (e *GmailEngine) refreshBadges(ctx context.Context) {
	for _, f := range e.syncedFolders() {
		e.RefreshBadge(ctx, f.name)
	}
}

func (e *GmailEngine) DraftsFolderName() string { return gmailDrafts }
func (e *GmailEngine) TrashFolderName() string  { return gmailTrash }
func (e *GmailEngine) SentFolderName() string   { return gmailSent }

// --- reading ---

// FetchHeaders serves the newest conversations of folder from the store
// (every synced label is mirrored there) — or messages, in Trash and
// Drafts. Only page 1 exists.
func (e *GmailEngine) FetchHeaders(_ context.Context, folder string, page, pageSize int) ([]*mail.Envelope, error) {
	if page > 1 {
		return nil, nil
	}
	if e.isAllMail(folder) {
		return e.allMailRows()
	}
	var envs []*mail.Envelope
	if ShowsConversations(e, folder) {
		envs = e.store.Threads(e.cfg.Name, folder)
		for _, env := range envs {
			e.describeThread(env)
		}
	} else {
		envs, _ = e.store.Envelopes(e.cfg.Name, folder)
	}
	if pageSize > 0 && len(envs) > pageSize {
		envs = envs[len(envs)-pageSize:] // oldest first: keep the newest
	}
	return envs, nil
}

// FetchBody reads the stored body, or downloads it (and keeps it).
func (e *GmailEngine) FetchBody(ctx context.Context, folder, id string) (*mail.Message, error) {
	env, known := e.store.Envelope(e.cfg.Name, folder, id)
	if known {
		if raw, ok := e.store.Body(e.cfg.Name, "", id); ok {
			return mail.ParseMessage(env, raw), nil
		}
	}
	m, err := e.api.GetRaw(ctx, id)
	if err != nil {
		return nil, err
	}
	if known {
		_ = e.store.PutBody(e.cfg.Name, "", id, m.Raw)
	} else {
		env = envelopeOf(withHeadersOf(m))
	}
	return mail.ParseMessage(env, m.Raw), nil
}

// --- changing messages ---

// SetFlags maps \Seen and \Flagged to the UNREAD and STARRED labels; other
// flags have no Gmail meaning and are ignored.
func (e *GmailEngine) SetFlags(ctx context.Context, _ string, id string, flags []string, add bool) error {
	var on, off []string
	for _, f := range flags {
		switch f {
		case `\Seen`:
			off = append(off, "UNREAD") // seen = not UNREAD
		case `\Flagged`:
			on = append(on, "STARRED")
		}
	}
	if !add {
		on, off = off, on
	}
	if len(on)+len(off) == 0 {
		return nil
	}
	if err := e.api.Modify(ctx, id, on, off); err != nil {
		return err
	}
	e.refreshMessage(ctx, id)
	return nil
}

// MoveToTrash moves a message (from every label) to the Trash.
func (e *GmailEngine) MoveToTrash(ctx context.Context, _ string, id string) error {
	if err := e.api.Trash(ctx, id); err != nil {
		return err
	}
	e.refreshMessage(ctx, id)
	return nil
}

// ErrNotInTrash refuses a permanent delete of a message that has left the
// Trash since the list was shown (restored on another device).
var ErrNotInTrash = errors.New("message is no longer in Trash")

// PermanentDelete deletes a message for good, from every label — only while
// Gmail still has it in the Trash, so a stale row can never delete mail
// that was restored meanwhile.
func (e *GmailEngine) PermanentDelete(ctx context.Context, _ string, id string) error {
	defer kick(e.pollKick)
	m, err := e.api.GetMetadata(ctx, id)
	if errors.Is(err, gmail.ErrNotFound) {
		return e.forget([]string{id}) // already gone: done
	}
	if err != nil {
		return err
	}
	if !slices.Contains(m.LabelIDs, "TRASH") {
		_ = e.applyMeta(m) // show where it is now
		return ErrNotInTrash
	}
	if err := e.api.Delete(ctx, id); err != nil && !errors.Is(err, gmail.ErrNotFound) {
		return err
	}
	return e.forget([]string{id})
}

// EmptyTrash deletes every message in the Trash for good.
func (e *GmailEngine) EmptyTrash(ctx context.Context) (int, error) {
	ids, err := e.listAll(ctx, "TRASH", "")
	if err != nil {
		return 0, err
	}
	if err := e.api.BatchDelete(ctx, ids); err != nil {
		return 0, err
	}
	kick(e.pollKick)
	return len(ids), e.forget(ids)
}

// Move swaps the open label for dest's.
func (e *GmailEngine) Move(ctx context.Context, folder, id, dest string) error {
	if dest == "" || dest == folder {
		return fmt.Errorf("already in %s", folder)
	}
	return e.ModifyLabels(ctx, folder, id, []string{dest}, []string{folder})
}

// Archive takes a message out of the Inbox; its other labels stay.
func (e *GmailEngine) Archive(ctx context.Context, folder, id string) error {
	return e.ModifyLabels(ctx, folder, id, nil, []string{gmailInbox})
}

// MoveTargets lists the labels a message in folder can be moved to: not
// folder itself, and not Drafts, Sent or Trash, which Gmail does not let a
// label change reach (d moves to Trash).
func (e *GmailEngine) MoveTargets(folder string) []string {
	var out []string
	for _, f := range e.AccountLabels() {
		switch f.Name {
		case folder, gmailDrafts, gmailSent, gmailTrash:
			continue
		}
		out = append(out, f.Name)
	}
	return out
}

// --- drafts ---

// SaveDraft saves raw as a Gmail draft and returns the draft id: the draft
// replaces is updated in place when it still exists, else a new one is made.
func (e *GmailEngine) SaveDraft(ctx context.Context, raw []byte, replaces string) (string, error) {
	defer kick(e.pollKick)
	if replaces != "" {
		err := e.api.UpdateDraft(ctx, replaces, raw)
		if err == nil {
			return replaces, nil
		}
		if !errors.Is(err, gmail.ErrNotFound) {
			return "", err
		}
	}
	return e.api.CreateDraft(ctx, raw, e.threadOf(ctx, headerOf(raw, "In-Reply-To")))
}

// TrashDraftCopy deletes the Gmail draft of a deleted local draft (Gmail
// drafts are not moved to the Trash).
func (e *GmailEngine) TrashDraftCopy(ctx context.Context, id string) error {
	return e.DeleteDraftCopy(ctx, id)
}

// ErrNoSuchDraft means no Gmail draft is named by, or holds, the id given:
// nothing was deleted.
var ErrNoSuchDraft = errors.New("no such Gmail draft")

// DeleteDraftCopy deletes a Gmail draft, named by its draft id (what
// SaveDraft returned) or by the id of the message it holds (a Drafts row).
// An id no draft answers to is ErrNoSuchDraft: nothing was deleted.
func (e *GmailEngine) DeleteDraftCopy(ctx context.Context, id string) error {
	defer kick(e.pollKick)
	did, err := e.draftID(ctx, id)
	if err != nil {
		return err
	}
	if did == "" {
		return fmt.Errorf("%w: %s (nothing deleted)", ErrNoSuchDraft, id)
	}
	// Gone between the listing and now: deleted all the same.
	if err := e.api.DeleteDraft(ctx, did); err != nil && !errors.Is(err, gmail.ErrNotFound) {
		return err
	}
	return nil
}

// DraftCopyOf names the Gmail draft that holds message id — a Drafts row
// — by the id SaveDraft returned for it; "" when no draft holds it.
func (e *GmailEngine) DraftCopyOf(ctx context.Context, messageID string) (string, error) {
	drafts, err := e.api.ListDrafts(ctx)
	if err != nil {
		return "", err
	}
	for _, d := range drafts {
		if d.MessageID == messageID {
			return d.ID, nil
		}
	}
	return "", nil
}

// draftID resolves id, a draft id or the id of a draft's message, to the
// draft id; "" when no draft answers to it.
func (e *GmailEngine) draftID(ctx context.Context, id string) (string, error) {
	drafts, err := e.api.ListDrafts(ctx)
	if err != nil {
		return "", err
	}
	for _, d := range drafts {
		if d.ID == id || d.MessageID == id {
			return d.ID, nil
		}
	}
	return "", nil
}

// --- sending ---

// Send sends d through Gmail; a reply stays in its conversation. Gmail
// files the message in Sent itself.
func (e *GmailEngine) Send(ctx context.Context, d smtpadapter.Draft) ([]byte, error) {
	raw, err := smtpadapter.BuildMessage(d)
	if err != nil {
		return nil, err
	}
	if _, err := e.api.Send(ctx, raw, e.threadOf(ctx, d.InReplyTo)); err != nil {
		return nil, err
	}
	kick(e.pollKick)
	return raw, nil
}

// ServerSavesSent is true: Gmail keeps every sent message in Sent.
func (e *GmailEngine) ServerSavesSent() bool { return true }

// SaveSentCopy saves nothing — Gmail already did — and names Sent.
func (e *GmailEngine) SaveSentCopy(context.Context, []byte) (string, error) {
	return e.SentFolderName(), nil
}

// threadOf finds the conversation of the message whose Message-ID is
// inReplyTo: in the store first, else by asking Gmail. "" when unknown.
func (e *GmailEngine) threadOf(ctx context.Context, inReplyTo string) string {
	fields := strings.Fields(inReplyTo)
	if len(fields) == 0 {
		return ""
	}
	msgID := strings.Trim(fields[0], "<>")
	if msgID == "" {
		return ""
	}
	if t := e.store.ThreadOfMessageID(e.cfg.Name, msgID); t != "" {
		return t
	}
	ids, _, err := e.api.ListMessageIDs(ctx, "", "rfc822msgid:"+msgID, "")
	if err != nil || len(ids) == 0 {
		return ""
	}
	m, err := e.api.GetMetadata(ctx, ids[0])
	if err != nil {
		return ""
	}
	return m.ThreadID
}

// AccountLabels lists the labels the account shows (the folder config
// applies), for the label picker.
func (e *GmailEngine) AccountLabels() []*mail.Folder {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.folders)
}

// CurrentLabels reads which shown labels the message — or, given a
// threadID, any message of the conversation — carries, from the store: a
// message has a row under every synced label it carries.
func (e *GmailEngine) CurrentLabels(threadID, id string) []string {
	ids := []string{id}
	if threadID != "" {
		ids = ids[:0]
		for _, env := range e.store.ThreadEnvelopes(e.cfg.Name, threadID) {
			ids = append(ids, env.ID)
		}
	}
	var out []string
	for _, f := range e.AccountLabels() {
		for _, m := range ids {
			if _, ok := e.store.Envelope(e.cfg.Name, f.Name, m); ok {
				out = append(out, f.Name)
				break
			}
		}
	}
	return out
}

// ModifyLabels adds and removes labels, given by their shown names, on one
// message.
func (e *GmailEngine) ModifyLabels(ctx context.Context, _ string, id string, add, remove []string) error {
	on, off := e.labelsOf(add), e.labelsOf(remove)
	if len(on)+len(off) == 0 {
		return nil
	}
	if err := e.api.Modify(ctx, id, on, off); err != nil {
		return err
	}
	e.refreshMessage(ctx, id)
	return nil
}
