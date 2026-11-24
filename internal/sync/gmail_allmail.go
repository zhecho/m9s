package sync

import (
	"context"
	"slices"

	"github.com/zhecho/m9s/internal/mail"
)

// GmailAllMail is the folder that lists a Gmail account's whole mailbox, as
// All Mail does on Gmail web: archived mail included, Spam and Trash left
// out. It is no label: it is listed live from Gmail (the newest
// conversations, like a search), never stored, and has no counts.
const GmailAllMail = "All Mail"

// allMailState is All Mail's last listing and the Gmail history cursor it
// was made at (guarded by the engine's mu).
type allMailState struct {
	rows   []*mail.Envelope
	cursor string
	listed bool  // a listing has arrived
	busy   bool  // a listing is on its way
	wanted bool  // the view has asked for All Mail: tell it when the mailbox changes
	err    error // the last listing's failure, nil after a success
}

// Start runs the engine; the context also bounds All Mail's listings.
func (e *GmailEngine) Start(ctx context.Context) {
	e.mu.Lock()
	e.runCtx = ctx
	e.mu.Unlock()
	e.pollCore.Start(ctx)
}

// withAllMail adds All Mail to the shown folders, after Drafts (Gmail web's
// order), else before Spam and Trash, else last — unless a shown label is
// already called that, which then stays the only All Mail.
func withAllMail(folders []*mail.Folder) []*mail.Folder {
	names := make([]string, len(folders))
	for i, f := range folders {
		names[i] = f.Name
	}
	if slices.Contains(names, GmailAllMail) {
		return folders
	}
	at := len(folders)
	if i := slices.Index(names, gmailDrafts); i >= 0 {
		at = i + 1
	} else if i := slices.IndexFunc(names, func(n string) bool { return n == gmailSpam || n == gmailTrash }); i >= 0 {
		at = i
	}
	return slices.Insert(slices.Clone(folders), at, &mail.Folder{Name: GmailAllMail, Delimiter: "/"})
}

// isAllMail reports whether folder is the listed All Mail, not a label of
// that name.
func (e *GmailEngine) isAllMail(folder string) bool {
	if folder != GmailAllMail {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, label := e.labelOf[folder]
	return !label
}

// allMailRows serves All Mail's last listing at once — the caller may be
// the UI goroutine — and starts a new listing in the background when there
// is none yet or the mailbox changed since. When it arrives NewMail for
// All Mail is sent, so an open list reloads. With no rows yet, the last
// listing's failure is returned, so the view can say so. The rows are
// copies: the view changes their flags.
func (e *GmailEngine) allMailRows() ([]*mail.Envelope, error) {
	cursor := e.store.Cursor(e.cfg.Name)
	e.mu.Lock()
	e.allMail.wanted = true
	rows := make([]*mail.Envelope, len(e.allMail.rows))
	for i, r := range e.allMail.rows {
		rows[i] = copyEnvelope(r)
	}
	var err error
	if len(rows) == 0 {
		err = e.allMail.err
	}
	stale := !e.allMail.listed || e.allMail.cursor != cursor
	ctx := e.runCtx
	start := stale && !e.allMail.busy && ctx != nil
	if start {
		e.allMail.busy = true
	}
	e.mu.Unlock()
	if start {
		go e.listAllMail(ctx, cursor)
	}
	return rows, err
}

func copyEnvelope(env *mail.Envelope) *mail.Envelope {
	c := *env
	c.Flags = slices.Clone(env.Flags)
	return &c
}

// listAllMail lists the newest conversations of the whole mailbox. A
// failure keeps the last rows; the next allMailRows tries again.
func (e *GmailEngine) listAllMail(ctx context.Context, cursor string) {
	ids, _, err := e.api.ListThreads(ctx, "", "")
	var rows []*mail.Envelope
	if err == nil {
		rows, err = e.threadRows(ctx, ids)
	}
	e.mu.Lock()
	e.allMail.busy, e.allMail.err = false, err
	if err == nil {
		e.allMail.rows, e.allMail.cursor, e.allMail.listed = rows, cursor, true
	}
	e.mu.Unlock()
	if err == nil {
		e.emit(ctx, NewMail{AccountName: e.cfg.Name, FolderName: GmailAllMail})
	}
}

// allMailChanged tells an open All Mail list that the mailbox changed, once
// the view has asked for All Mail (also when that listing failed):
// reloading it lists it again.
func (e *GmailEngine) allMailChanged(ctx context.Context) bool {
	e.mu.Lock()
	wanted := e.allMail.wanted
	e.mu.Unlock()
	return !wanted || e.emit(ctx, NewMail{AccountName: e.cfg.Name, FolderName: GmailAllMail})
}

// patchAllMail shows a change to a conversation in All Mail's rows at once
// (on and off are Gmail label ids), before the next listing confirms it.
func (e *GmailEngine) patchAllMail(threadID string, on, off []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, r := range e.allMail.rows {
		if r.ThreadID != threadID {
			continue
		}
		c := copyEnvelope(r)
		set := func(flag string, want bool) {
			c.Flags = slices.DeleteFunc(c.Flags, func(f string) bool { return f == flag })
			if want {
				c.Flags = append(c.Flags, flag)
			}
		}
		if slices.Contains(on, "UNREAD") {
			set(`\Seen`, false)
		}
		if slices.Contains(off, "UNREAD") {
			set(`\Seen`, true)
		}
		if slices.Contains(on, "STARRED") {
			set(`\Flagged`, true)
		}
		if slices.Contains(off, "STARRED") {
			set(`\Flagged`, false)
		}
		e.allMail.rows[i] = c
	}
}

// dropAllMail takes a conversation out of All Mail's rows at once (it went
// to the Trash, which All Mail leaves out).
func (e *GmailEngine) dropAllMail(threadID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.allMail.rows = slices.DeleteFunc(slices.Clone(e.allMail.rows), func(r *mail.Envelope) bool { return r.ThreadID == threadID })
}

// labelsOf turns shown names into label ids for a change, leaving out All
// Mail, which no label stands for: a move out of it only adds.
func (e *GmailEngine) labelsOf(names []string) []string {
	var out []string
	for _, n := range names {
		if e.isAllMail(n) {
			continue
		}
		out = append(out, e.labelID(n))
	}
	return out
}
