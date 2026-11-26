package sync

import (
	"bytes"
	"context"
	"errors"
	"net/textproto"
	"slices"
	"sort"
	"strings"
	gosync "sync"
	"time"

	gomail "github.com/emersion/go-message/mail"

	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/mail/gmail"
)

// gmailWorkers is how many messages are fetched from Gmail at once.
const gmailWorkers = 8

// gmailFetchChunk is how many new messages are stored per transaction.
const gmailFetchChunk = 100

// specialUse gives Gmail's system folders the IMAP attributes the rest of
// m9s looks for (auto-detection, Trash, Sent).
var specialUse = map[string]string{
	"SENT": `\Sent`, "DRAFT": `\Drafts`, "TRASH": `\Trash`, "SPAM": `\Junk`, "STARRED": `\Flagged`,
}

// systemOrder is where system folders appear; user labels follow by name.
var systemOrder = []string{"INBOX", "STARRED", "SENT", "DRAFT", "SPAM", "TRASH"}

type syncedFolder struct{ name, label string }

// loadFolders reads the labels, keeps the folders the config selects and
// remembers which label each one is.
func (e *GmailEngine) loadFolders(ctx context.Context) ([]*mail.Folder, error) {
	labels, err := e.api.Labels(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(labels, func(i, j int) bool {
		ri, rj := slices.Index(systemOrder, labels[i].ID), slices.Index(systemOrder, labels[j].ID)
		if ri < 0 {
			ri = len(systemOrder)
		}
		if rj < 0 {
			rj = len(systemOrder)
		}
		if ri != rj {
			return ri < rj
		}
		return labels[i].Name < labels[j].Name
	})
	var all []*mail.Folder
	ids := map[string]string{}
	for _, l := range labels {
		name, ok := gmail.FolderName(l)
		if !ok {
			continue
		}
		f := &mail.Folder{Name: name, Delimiter: "/"}
		if attr, ok := specialUse[l.ID]; ok {
			f.Attributes = []string{attr}
		}
		all = append(all, f)
		ids[name] = l.ID
	}
	applied := applyFolderConfig(&e.cfg.Folders, all)
	e.mu.Lock()
	e.folders = applied
	e.labelOf = map[string]string{}
	for _, f := range applied {
		e.labelOf[f.Name] = ids[f.Name]
	}
	e.mu.Unlock()
	return withAllMail(applied), nil // All Mail is shown, not synced: e.folders stays the labels
}

// syncedFolders lists the mirrored folders, INBOX first.
func (e *GmailEngine) syncedFolders() []syncedFolder {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]syncedFolder, 0, len(e.folders))
	for _, f := range e.folders {
		sf := syncedFolder{f.Name, e.labelOf[f.Name]}
		if f.Name == gmailInbox {
			out = append([]syncedFolder{sf}, out...)
		} else {
			out = append(out, sf)
		}
	}
	return out
}

func folderNames(fs []syncedFolder) []string {
	names := make([]string, len(fs))
	for i, f := range fs {
		names[i] = f.name
	}
	return names
}

// initialSync brings the store up to date when a session starts: a full
// listing the first time, else the history since the stored cursor plus a
// listing of folders the store has never held (newly included labels).
func (e *GmailEngine) initialSync(ctx context.Context, historyID string) error {
	folders := e.syncedFolders()
	if err := e.store.PruneFolders(e.cfg.Name, folderNames(folders)); err != nil {
		return err
	}
	if err := e.syncStore(ctx, folders, historyID); err != nil {
		return err
	}
	e.refreshBadges(ctx)
	return nil
}

func (e *GmailEngine) syncStore(ctx context.Context, folders []syncedFolder, historyID string) error {
	if e.store.Cursor(e.cfg.Name) == "" {
		e.syncMu.Lock()
		defer e.syncMu.Unlock()
		return e.snapshot(ctx, folders, historyID)
	}
	// Decided before the history is applied: applying it may already store
	// a row in a new folder, which must not pass for a listed folder.
	var fresh []syncedFolder
	for _, f := range folders {
		if _, known := e.store.Envelopes(e.cfg.Name, f.name); !known {
			fresh = append(fresh, f)
		}
	}
	if err := e.poll(ctx); err != nil {
		return err
	}
	if len(fresh) == 0 {
		return nil
	}
	e.syncMu.Lock()
	defer e.syncMu.Unlock()
	return e.snapshot(ctx, fresh, "")
}

// poll applies what changed since the stored cursor. A cursor Gmail no
// longer remembers means a full listing.
func (e *GmailEngine) poll(ctx context.Context) error {
	e.syncMu.Lock()
	defer e.syncMu.Unlock()
	cursor := e.store.Cursor(e.cfg.Name)
	if cursor == "" {
		return e.resync(ctx)
	}
	ch, newID, err := e.api.History(ctx, cursor)
	if errors.Is(err, gmail.ErrNotFound) {
		return e.resync(ctx)
	}
	if err != nil {
		return err
	}
	// Any failure leaves the cursor where it was: the next poll applies the
	// same changes again rather than losing them.
	if err := e.forget(ch.Deleted); err != nil {
		return err
	}
	changed := slices.Concat(ch.Added, ch.LabelsChanged)
	metas, gone, err := e.fetchMetas(ctx, changed)
	if err != nil {
		return err
	}
	if err := e.forget(gone); err != nil {
		return err
	}
	newMail, isNew := false, setOf(ch.Added)
	for _, m := range metas {
		if err := e.applyMeta(m); err != nil {
			return err
		}
		if isNew[m.ID] && member(m.LabelIDs, "INBOX") {
			newMail = true
		}
	}
	if err := e.store.SetCursor(e.cfg.Name, newID); err != nil {
		return err
	}
	if newMail && !e.emit(ctx, NewMail{AccountName: e.cfg.Name, FolderName: gmailInbox}) {
		return ctx.Err()
	}
	if len(changed)+len(ch.Deleted) > 0 {
		e.refreshBadges(ctx)
		kick(e.bodyKick)
		if !e.allMailChanged(ctx) {
			return ctx.Err()
		}
	}
	return nil
}

// resync lists every synced folder again. The caller holds syncMu.
func (e *GmailEngine) resync(ctx context.Context) error {
	_, historyID, err := e.api.Profile(ctx) // before listing: nothing is missed
	if err != nil {
		return err
	}
	if err := e.snapshot(ctx, e.syncedFolders(), historyID); err != nil {
		return err
	}
	e.refreshBadges(ctx)
	return nil
}

// snapshot makes the stored rows of folders equal to Gmail's listing and,
// when historyID is set, stores it as the cursor. The caller holds syncMu.
func (e *GmailEngine) snapshot(ctx context.Context, folders []syncedFolder, historyID string) error {
	for _, f := range folders {
		if err := e.reconcile(ctx, f); err != nil {
			return err
		}
	}
	_ = e.store.PruneSharedBodies(e.cfg.Name)
	kick(e.bodyKick)
	if historyID == "" {
		return nil
	}
	return e.store.SetCursor(e.cfg.Name, historyID)
}

// reconcile lists one label: stored rows Gmail no longer lists go, new ones
// are fetched, and the flags of the others are brought up to date from the
// label's unread and starred listings.
func (e *GmailEngine) reconcile(ctx context.Context, f syncedFolder) error {
	remote, err := e.listAll(ctx, f.label, "")
	if err != nil {
		return err
	}
	unread, err := e.listAll(ctx, f.label, "is:unread")
	if err != nil {
		return err
	}
	starred, err := e.listAll(ctx, f.label, "is:starred")
	if err != nil {
		return err
	}
	_, stored, err := e.store.FolderState(e.cfg.Name, f.name)
	if err != nil {
		return err
	}
	listed, isUnread, isStarred := setOf(remote), setOf(unread), setOf(starred)
	var gone, added []string
	changed := map[string][]string{}
	for id, flags := range stored {
		if !listed[id] {
			gone = append(gone, id)
			continue
		}
		next := listedFlags(flags, isUnread[id], isStarred[id])
		if !sameFlags(flags, next) {
			changed[id] = next
		}
	}
	for _, id := range remote { // newest first: an interrupted sync has the recent mail
		if _, have := stored[id]; !have {
			added = append(added, id)
		}
	}
	if err := e.store.RemoveMessages(e.cfg.Name, f.name, gone); err != nil {
		return err
	}
	if err := e.store.SetFlags(e.cfg.Name, f.name, changed); err != nil {
		return err
	}
	if len(added) == 0 {
		// Still record the folder as known, even when it is empty.
		return e.store.AddMessages(e.cfg.Name, f.name, nil)
	}
	have := len(remote) - len(added)
	progress := func(done bool) bool {
		return e.emit(ctx, ListingProgress{AccountName: e.cfg.Name, FolderName: f.name,
			Stored: have, Total: len(remote), Done: done})
	}
	if !progress(false) {
		return ctx.Err()
	}
	// A message already stored under another label has the same headers:
	// they are reused rather than fetched again (20 quota units each).
	reused, fetch := e.storedElsewhere(added, isUnread, isStarred)
	for chunk := range slices.Chunk(reused, gmailFetchChunk) {
		if err := e.store.AddMessages(e.cfg.Name, f.name, chunk); err != nil {
			return err
		}
		have += len(chunk)
		if have < len(remote) && !progress(false) {
			return ctx.Err()
		}
	}
	for chunk := range slices.Chunk(fetch, gmailFetchChunk) {
		metas, _, err := e.fetchMetas(ctx, chunk)
		if err != nil {
			return err
		}
		envs := make([]*mail.Envelope, len(metas))
		for i, m := range metas {
			envs[i] = envelopeOf(m)
		}
		if err := e.store.AddMessages(e.cfg.Name, f.name, envs); err != nil {
			return err
		}
		have += len(chunk) // a message deleted meanwhile is done with too
		if have < len(remote) && !progress(false) {
			return ctx.Err()
		}
	}
	if !progress(true) {
		return ctx.Err()
	}
	return nil
}

// storedElsewhere splits ids, in order, into the envelopes the store holds
// under another label — given the flags of this label's listing — and the
// ids whose headers must be fetched.
func (e *GmailEngine) storedElsewhere(ids []string, isUnread, isStarred map[string]bool) (reused []*mail.Envelope, fetch []string) {
	known := e.store.EnvelopesByID(e.cfg.Name, ids)
	for _, id := range ids {
		env, ok := known[id]
		if !ok {
			fetch = append(fetch, id)
			continue
		}
		env.Flags = listedFlags(env.Flags, isUnread[id], isStarred[id])
		reused = append(reused, env)
	}
	return reused, fetch
}

// listedFlags is flags with \Seen and \Flagged as a label's unread and
// starred listings say; other flags (\Draft) stay.
func listedFlags(flags []string, unread, starred bool) []string {
	next := slices.DeleteFunc(slices.Clone(flags), func(fl string) bool { return fl == `\Seen` || fl == `\Flagged` })
	if !unread {
		next = append(next, `\Seen`)
	}
	if starred {
		next = append(next, `\Flagged`)
	}
	return next
}

func setOf(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// listAll pages through every message id of label matching query.
func (e *GmailEngine) listAll(ctx context.Context, label, query string) ([]string, error) {
	var all []string
	token := ""
	for {
		ids, next, err := e.api.ListMessageIDs(ctx, label, query, token)
		if err != nil {
			return nil, err
		}
		all = append(all, ids...)
		if next == "" {
			return all, nil
		}
		token = next
	}
}

// fetchMetas gets the metadata of ids, gmailWorkers at a time, in order.
// Messages deleted meanwhile are returned in gone.
func (e *GmailEngine) fetchMetas(ctx context.Context, ids []string) (metas []*gmail.MsgMeta, gone []string, err error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	results := make([]*gmail.MsgMeta, len(ids))
	errs := make([]error, len(ids))
	work := make(chan int)
	var wg gosync.WaitGroup
	for range min(gmailWorkers, len(ids)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				results[i], errs[i] = e.api.GetMetadata(ctx, ids[i])
			}
		}()
	}
	for i := range ids {
		work <- i
	}
	close(work)
	wg.Wait()
	for i, m := range results {
		switch {
		case errors.Is(errs[i], gmail.ErrNotFound):
			gone = append(gone, ids[i])
		case errs[i] != nil:
			return nil, nil, errs[i]
		default:
			metas = append(metas, m)
		}
	}
	return metas, gone, nil
}

// member reports whether a message with labels is listed under label:
// Gmail leaves mail in Trash or Spam out of every other label.
func member(labels []string, label string) bool {
	if !slices.Contains(labels, label) {
		return false
	}
	if label == "TRASH" || label == "SPAM" {
		return true
	}
	return !slices.Contains(labels, "TRASH") && !slices.Contains(labels, "SPAM")
}

// flagsOf maps labels to the IMAP-style flags the UI understands.
func flagsOf(labels []string) []string {
	var flags []string
	if !slices.Contains(labels, "UNREAD") {
		flags = append(flags, `\Seen`)
	}
	if slices.Contains(labels, "STARRED") {
		flags = append(flags, `\Flagged`)
	}
	if slices.Contains(labels, "DRAFT") {
		flags = append(flags, `\Draft`)
	}
	return flags
}

// applyMeta makes the store hold m in exactly the synced folders whose label
// it carries, with its current flags everywhere.
func (e *GmailEngine) applyMeta(m *gmail.MsgMeta) error {
	env := envelopeOf(m)
	e.applyMu.Lock()
	defer e.applyMu.Unlock()
	for _, f := range e.syncedFolders() {
		var err error
		if member(m.LabelIDs, f.label) {
			err = e.store.AddMessages(e.cfg.Name, f.name, []*mail.Envelope{env})
		} else {
			err = e.store.RemoveMessages(e.cfg.Name, f.name, []string{m.ID})
		}
		if err != nil {
			return err
		}
	}
	return e.store.SetFlagsEverywhere(e.cfg.Name, map[string][]string{m.ID: env.Flags})
}

// refreshMessage re-reads one message after the UI changed it, so the store
// shows the change at once. A failure here is mended by the poll that
// follows: its history still holds the change.
func (e *GmailEngine) refreshMessage(ctx context.Context, id string) {
	defer kick(e.pollKick)
	m, err := e.api.GetMetadata(ctx, id)
	if errors.Is(err, gmail.ErrNotFound) {
		_ = e.forget([]string{id})
		return
	}
	if err == nil {
		_ = e.applyMeta(m)
	}
}

// forget removes messages deleted on the server from every folder.
func (e *GmailEngine) forget(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	e.applyMu.Lock()
	for _, f := range e.syncedFolders() {
		if err := e.store.RemoveMessages(e.cfg.Name, f.name, ids); err != nil {
			e.applyMu.Unlock()
			return err
		}
	}
	e.applyMu.Unlock()
	_ = e.store.PruneSharedBodies(e.cfg.Name) // tidying only: nothing is lost if it fails
	return nil
}

// --- envelopes ---

// envelopeOf builds the stored envelope of a message from its metadata.
func envelopeOf(m *gmail.MsgMeta) *mail.Envelope {
	var th textproto.MIMEHeader = make(map[string][]string, len(m.Headers))
	for k, v := range m.Headers {
		th.Set(k, v)
	}
	h := gomail.HeaderFromMap(th)
	env := &mail.Envelope{ID: m.ID, ThreadID: m.ThreadID, SortKey: m.InternalDate, Flags: flagsOf(m.LabelIDs)}
	if s, err := h.Subject(); err == nil {
		env.Subject = s
	} else {
		env.Subject = m.Headers["Subject"]
	}
	env.From = addresses(h, "From")
	env.To = addresses(h, "To")
	env.CC = addresses(h, "Cc")
	if d, err := h.Date(); err == nil && !d.IsZero() {
		env.Date = d
	} else if m.InternalDate > 0 {
		env.Date = time.UnixMilli(m.InternalDate)
	}
	if id, err := h.MessageID(); err == nil {
		env.MessageID = id
	}
	if ids, err := h.MsgIDList("In-Reply-To"); err == nil {
		env.InReplyTo = ids
	}
	// A guess until the body is stored: mixed is how mail carries files.
	if ct, _, err := h.ContentType(); err == nil && ct == "multipart/mixed" {
		env.HasAttach = true
	}
	return env
}

func addresses(h gomail.Header, key string) []mail.Address {
	list, err := h.AddressList(key)
	if err != nil {
		if v := h.Get(key); v != "" {
			return []mail.Address{{Email: v}}
		}
		return nil
	}
	out := make([]mail.Address, 0, len(list))
	for _, a := range list {
		out = append(out, mail.Address{Name: a.Name, Email: a.Address})
	}
	return out
}

// withHeadersOf fills m.Headers from its raw content.
func withHeadersOf(m *gmail.MsgMeta) *gmail.MsgMeta {
	m.Headers = map[string]string{}
	for _, name := range []string{"From", "To", "Cc", "Subject", "Date", "Message-ID", "In-Reply-To"} {
		if v := headerOf(m.Raw, name); v != "" {
			m.Headers[name] = v
		}
	}
	return m
}

// headerOf returns one header of a raw message ("" if absent or unreadable).
func headerOf(raw []byte, name string) string {
	r, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	defer r.Close()
	return strings.TrimSpace(r.Header.Get(name))
}
