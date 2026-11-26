package sync

import (
	"context"
	"errors"
	"time"

	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/mail/graph"
)

// syncedFolders lists the mirrored folders with their Graph ids, INBOX
// first.
func (e *MicrosoftEngine) syncedFolders() []syncedFolder {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]syncedFolder, 0, len(e.folders))
	for _, f := range e.folders {
		sf := syncedFolder{f.Name, e.idOf[f.Name]}
		if f.Name == msInbox {
			out = append([]syncedFolder{sf}, out...)
		} else {
			out = append(out, sf)
		}
	}
	return out
}

// initialSync brings the store up to date when a session starts: folders
// without a delta link are listed, the others catch up from their link.
func (e *MicrosoftEngine) initialSync(ctx context.Context, _ string) error {
	folders := e.syncedFolders()
	if err := e.store.PruneFolders(e.cfg.Name, folderNames(folders)); err != nil {
		return err
	}
	e.syncMu.Lock()
	e.lastFull = time.Now()
	err := e.syncFolders(ctx, folders, false)
	e.syncMu.Unlock()
	if err != nil {
		return err
	}
	e.refreshBadges(ctx)
	kick(e.bodyKick)
	return nil
}

// poll applies what changed in every synced folder since its delta link,
// and lists every folder again once a day.
func (e *MicrosoftEngine) poll(ctx context.Context) error {
	e.syncMu.Lock()
	full := time.Since(e.lastFull) >= fullRecheck
	if full {
		e.lastFull = time.Now()
	}
	err := e.syncFolders(ctx, e.syncedFolders(), full)
	e.syncMu.Unlock()
	return err
}

// syncFolders brings folders up to date: listing them when full or when
// they have no (valid) delta link, else applying their delta. Bodies that
// no folder holds any more are pruned only at the end, so a message that
// moved between two synced folders keeps its body. The caller holds syncMu.
func (e *MicrosoftEngine) syncFolders(ctx context.Context, folders []syncedFolder, full bool) error {
	changed, removed, newMail := false, false, false
	for _, f := range folders {
		if f.label == "" {
			continue
		}
		var r folderSync
		var err error
		if link := e.store.FolderCursor(e.cfg.Name, f.name); full || link == "" {
			r, err = e.relist(ctx, f)
		} else {
			r, err = e.applyDelta(ctx, f, link)
			if errors.Is(err, graph.ErrSyncReset) {
				r, err = e.relist(ctx, f)
			}
		}
		if err != nil {
			return err
		}
		changed = changed || r.changed
		removed = removed || r.removed
		newMail = newMail || (r.added && f.name == msInbox)
	}
	if removed {
		_ = e.store.PruneSharedBodies(e.cfg.Name) // tidying only
	}
	if newMail && !e.emit(ctx, NewMail{AccountName: e.cfg.Name, FolderName: msInbox}) {
		return ctx.Err()
	}
	if changed {
		e.refreshBadges(ctx)
		kick(e.bodyKick)
	}
	return nil
}

// folderSync is what one folder's sync did.
type folderSync struct {
	changed bool // any row added, changed or removed
	removed bool // a row left the folder
	added   bool // a message the folder did not hold arrived (not on a listing)
}

// applyDelta applies one delta round of folder f. Any failure leaves the
// link where it was: the next poll applies the same changes again.
func (e *MicrosoftEngine) applyDelta(ctx context.Context, f syncedFolder, link string) (folderSync, error) {
	msgs, gone, next, err := e.api.Delta(ctx, f.label, link)
	if err != nil {
		return folderSync{}, err
	}
	var r folderSync
	envs := make([]*mail.Envelope, 0, len(msgs))
	e.applyMu.Lock()
	for _, m := range msgs {
		if _, have := e.store.Envelope(e.cfg.Name, f.name, m.ID); !have {
			r.added = true
		}
		envs = append(envs, e.envelopeOf(m))
	}
	err = e.store.RemoveMessages(e.cfg.Name, f.name, gone)
	if err == nil {
		err = e.store.AddMessages(e.cfg.Name, f.name, envs)
	}
	e.applyMu.Unlock()
	if err != nil {
		return folderSync{}, err
	}
	r.changed, r.removed = len(msgs)+len(gone) > 0, len(gone) > 0
	return r, e.store.SetFolderCursor(e.cfg.Name, f.name, next)
}

// relist makes the stored rows of folder f equal to a full listing (a
// first delta round) and keeps that round's link.
func (e *MicrosoftEngine) relist(ctx context.Context, f syncedFolder) (folderSync, error) {
	msgs, _, next, err := e.api.Delta(ctx, f.label, "")
	if err != nil {
		return folderSync{}, err
	}
	listed := make(map[string]bool, len(msgs))
	envs := make([]*mail.Envelope, 0, len(msgs))
	for _, m := range msgs {
		listed[m.ID] = true
		envs = append(envs, e.envelopeOf(m))
	}
	e.applyMu.Lock()
	_, stored, err := e.store.FolderState(e.cfg.Name, f.name)
	var gone []string
	for id := range stored {
		if !listed[id] {
			gone = append(gone, id)
		}
	}
	if err == nil {
		err = e.store.RemoveMessages(e.cfg.Name, f.name, gone)
	}
	if err == nil {
		// Also records the folder as known when it is empty.
		err = e.store.AddMessages(e.cfg.Name, f.name, envs)
	}
	e.applyMu.Unlock()
	if err != nil {
		return folderSync{}, err
	}
	r := folderSync{changed: true, removed: len(gone) > 0}
	if err := e.store.SetFolderCursor(e.cfg.Name, f.name, next); err != nil {
		return r, err
	}
	if !e.emit(ctx, ListingProgress{AccountName: e.cfg.Name, FolderName: f.name,
		Stored: len(envs), Total: len(envs), Done: true}) {
		return r, ctx.Err()
	}
	return r, nil
}

// forget removes messages deleted on the server from every folder.
func (e *MicrosoftEngine) forget(ids []string) error {
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
	_ = e.store.PruneSharedBodies(e.cfg.Name) // tidying only
	return nil
}

// envelopeOf builds the stored envelope of a message. Graph has no
// In-Reply-To property; it is in the body.
func (e *MicrosoftEngine) envelopeOf(m graph.Msg) *mail.Envelope {
	env := &mail.Envelope{
		ID: m.ID, SortKey: m.Received.UnixMilli(), MessageID: m.InternetMessageID,
		Subject: m.Subject, From: m.From, To: m.To, CC: m.CC, Date: m.Received,
		HasAttach: m.HasAttachments, Categories: m.Categories,
	}
	if m.IsRead {
		env.Flags = append(env.Flags, `\Seen`)
	}
	if m.Flagged {
		env.Flags = append(env.Flags, `\Flagged`)
	}
	if drafts := e.folderID(msDrafts); drafts != "" && m.FolderID == drafts {
		env.Flags = append(env.Flags, `\Draft`)
	}
	return env
}
