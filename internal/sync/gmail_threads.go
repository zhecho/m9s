package sync

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	gosync "sync"

	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/mail/gmail"
)

// describeThread gives a conversation row the count and senders of the
// whole conversation as stored — my replies in Sent included — leaving out
// copies that are only in Trash or Spam, as Gmail web does.
func (e *GmailEngine) describeThread(row *mail.Envelope) {
	if row.ThreadID == "" {
		return
	}
	n := 0
	seen := map[string]bool{}
	for _, env := range e.store.ThreadEnvelopes(e.cfg.Name, row.ThreadID) {
		if e.onlyBinned(env.ID) {
			continue
		}
		n++
		for _, a := range env.From {
			key := strings.ToLower(a.Email)
			if !seen[key] {
				seen[key] = true
				row.Participants = append(row.Participants, a)
			}
		}
	}
	row.ThreadCount = max(row.ThreadCount, n)
}

// onlyBinned reports whether the stored message id is in Trash or Spam:
// Gmail keeps such a message out of every other label.
func (e *GmailEngine) onlyBinned(id string) bool {
	for _, f := range []string{gmailTrash, gmailSpam} {
		if _, ok := e.store.Envelope(e.cfg.Name, f, id); ok {
			return true
		}
	}
	return false
}

// Thread reads a whole conversation, oldest first — messages under labels
// that are not synced included. Gmail is asked only which messages it holds
// and their labels; content is downloaded (and kept) only for messages
// whose body is not stored yet.
func (e *GmailEngine) Thread(ctx context.Context, threadID string) ([]*mail.Message, error) {
	metas, err := e.api.ThreadMessages(ctx, threadID)
	if err != nil {
		return nil, err
	}
	out := make([]*mail.Message, 0, len(metas))
	for _, m := range metas {
		if raw, ok := e.store.Body(e.cfg.Name, "", m.ID); ok {
			m.Raw = raw
		} else {
			full, err := e.api.GetRaw(ctx, m.ID)
			if errors.Is(err, gmail.ErrNotFound) {
				continue // deleted meanwhile
			}
			if err != nil {
				return nil, err
			}
			m = full
			_ = e.store.PutBody(e.cfg.Name, "", m.ID, m.Raw) // a cache: reading works without it
		}
		env := envelopeOf(withHeadersOf(m)) // labels from Gmail now: current flags
		out = append(out, mail.ParseMessage(env, m.Raw))
	}
	return out, nil
}

// ModifyThread adds and removes labels on every message of a conversation.
// \Seen and \Flagged mean "not UNREAD" and STARRED; a folder name means its
// label; anything else is taken as a label id.
func (e *GmailEngine) ModifyThread(ctx context.Context, threadID string, add, remove []string) error {
	var on, off []string
	put := func(names []string, adding bool) {
		for _, n := range names {
			if e.isAllMail(n) {
				continue // no label: a move out of All Mail only adds
			}
			id, set := e.labelID(n), adding
			switch n {
			case `\Seen`:
				id, set = "UNREAD", !adding // seen = not UNREAD
			case `\Flagged`:
				id = "STARRED"
			}
			if set {
				on = append(on, id)
			} else {
				off = append(off, id)
			}
		}
	}
	put(add, true)
	put(remove, false)
	if len(on)+len(off) == 0 {
		return nil
	}
	if err := e.api.ModifyThread(ctx, threadID, on, off); err != nil {
		return err
	}
	e.patchAllMail(threadID, on, off)
	if e.labelledLocally(e.store.ThreadEnvelopes(e.cfg.Name, threadID), on, off) {
		kick(e.pollKick)
		return nil
	}
	e.refreshThread(ctx, threadID)
	return nil
}

// TrashThread moves every message of a conversation to the Trash.
func (e *GmailEngine) TrashThread(ctx context.Context, threadID string) error {
	if err := e.api.TrashThread(ctx, threadID); err != nil {
		return err
	}
	e.dropAllMail(threadID)
	defer kick(e.pollKick)
	return e.trashedLocally(e.store.ThreadEnvelopes(e.cfg.Name, threadID))
}

// TrashThreadsMany moves every message of each conversation to the Trash
// (BulkThreadTrasher), as TrashThread does one, keyed by thread id. A
// conversation's messages are not all stored (archived ones, unsynced
// labels), so when that saves time the mailbox is listed once — ids and
// conversations only, Spam and Trash left out — and the messages of the
// conversations get the TRASH label with batchModify, a thousand per
// request. A few conversations cost less one by one than such a listing.
func (e *GmailEngine) TrashThreadsMany(ctx context.Context, threadIDs []string, progress func(int)) map[string]error {
	defer kick(e.pollKick)
	threadIDs = slices.Compact(slices.Sorted(slices.Values(threadIDs)))
	failed := make(map[string]error)
	oneByOne := func(threads []string) {
		for _, t := range threads {
			if err := e.TrashThread(ctx, t); err != nil {
				failed[t] = err
			}
			progress(1)
		}
	}
	idsOf, err := e.threadMessageIDs(ctx, threadIDs)
	if err != nil {
		if ctx.Err() != nil {
			for _, t := range threadIDs {
				failed[t] = ctx.Err()
			}
			progress(len(threadIDs))
			return failed
		}
		oneByOne(threadIDs)
		return failed
	}
	var ids, unlisted []string
	left := make(map[string]int) // messages of a conversation not trashed yet
	for _, t := range threadIDs {
		if len(idsOf[t]) == 0 { // gone, or in Trash already: TrashThread tells
			unlisted = append(unlisted, t)
			continue
		}
		ids = append(ids, idsOf[t]...)
		left[t] = len(idsOf[t])
	}
	threadOf := make(map[string]string, len(ids))
	for t, ms := range idsOf {
		for _, id := range ms {
			threadOf[id] = t
		}
	}
	var again []string // conversations of batches Gmail refused for their ids
	settled := make(map[string]bool)
	for part := range slices.Chunk(ids, gmail.BatchModifyMax) {
		var threads []string // of this batch, each once
		for _, id := range part {
			if t := threadOf[id]; !slices.Contains(threads, t) {
				threads = append(threads, t)
			}
		}
		err := e.api.BatchModify(ctx, part, []string{"TRASH"}, nil)
		var ae *gmail.APIError
		switch {
		case err == nil:
			for _, id := range part {
				t := threadOf[id]
				if left[t]--; left[t] == 0 && !settled[t] {
					settled[t] = true
					e.dropAllMail(t)
					// Trashed on the server (its copies in Spam stay); should
					// the store fail, the poll this kicks shows it.
					_ = e.trashedLocally(slices.Collect(maps.Values(e.store.EnvelopesByID(e.cfg.Name, idsOf[t]))))
					progress(1)
				}
			}
		case ctx.Err() == nil && errors.As(err, &ae) && (ae.Code == http.StatusBadRequest || ae.Code == http.StatusNotFound):
			// One bad id (deleted meanwhile) fails a whole batch.
			for _, t := range threads {
				if !settled[t] {
					settled[t] = true
					again = append(again, t)
				}
			}
		default:
			for _, t := range threads {
				if !settled[t] {
					settled[t] = true
					failed[t] = err
					progress(1)
				}
			}
		}
	}
	oneByOne(append(unlisted, again...))
	return failed
}

// threadMessageIDs lists the mailbox once, Spam and Trash left out, and
// returns the ids of the messages of each of threadIDs — or an error when
// the listing would take longer than trashing threadIDs one by one (about
// one request either way, a listing page naming gmail.ListPageSize).
func (e *GmailEngine) threadMessageIDs(ctx context.Context, threadIDs []string) (map[string][]string, error) {
	total, err := e.api.MessagesTotal(ctx)
	if err != nil {
		return nil, err
	}
	if pages := (total + gmail.ListPageSize - 1) / gmail.ListPageSize; pages+1 >= len(threadIDs) {
		return nil, errCheaperOneByOne
	}
	want := make(map[string]bool, len(threadIDs))
	for _, t := range threadIDs {
		want[t] = true
	}
	out := make(map[string][]string, len(threadIDs))
	token := ""
	for {
		refs, next, err := e.api.ListMessageRefs(ctx, "", "", token)
		if err != nil {
			return nil, err
		}
		for _, r := range refs {
			if want[r.ThreadID] {
				out[r.ThreadID] = append(out[r.ThreadID], r.ID)
			}
		}
		if next == "" {
			return out, nil
		}
		token = next
	}
}

// errCheaperOneByOne says a few conversations are trashed one by one.
var errCheaperOneByOne = errors.New("cheaper one by one")

// labelID returns the label of a shown folder, or name itself (a label id).
func (e *GmailEngine) labelID(name string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if id, ok := e.labelOf[name]; ok {
		return id
	}
	return name
}

// refreshThread re-reads the stored messages of a conversation after the
// UI changed it, so the store shows the change at once; the poll that
// follows mends anything missed.
func (e *GmailEngine) refreshThread(ctx context.Context, threadID string) {
	for _, env := range e.store.ThreadEnvelopes(e.cfg.Name, threadID) {
		e.refreshMessage(ctx, env.ID)
	}
	kick(e.pollKick)
}

var _ ServerSearch = (*GmailEngine)(nil)

// Search asks Gmail for the newest conversations matching query — Gmail
// search syntax, passed on as typed, over the whole mailbox: folder is not
// a restriction. Rows look like the list's: the newest message stands for
// the conversation, flags and count from the whole of it. Nothing is
// written to the store.
func (e *GmailEngine) Search(ctx context.Context, folder string, query string) ([]*mail.Envelope, error) {
	res, err := e.SearchPage(ctx, folder, query, "")
	return res.Rows, err
}

// SearchPage is one page of Search (cursor: Gmail's page token), with
// Gmail's estimate of how many conversations match.
func (e *GmailEngine) SearchPage(ctx context.Context, _ string, query, cursor string) (SearchResult, error) {
	ids, next, estimate, err := e.api.SearchThreads(ctx, query, cursor)
	if err != nil {
		return SearchResult{}, err
	}
	rows, err := e.threadRows(ctx, ids)
	if err != nil {
		return SearchResult{}, err
	}
	return SearchResult{Rows: rows, Total: estimate, Estimate: true, Next: next}, nil
}

// threadRows builds a list row for each conversation in ids, as Search and
// All Mail show them; one deleted meanwhile is left out.
func (e *GmailEngine) threadRows(ctx context.Context, ids []string) ([]*mail.Envelope, error) {
	rows := make([]*mail.Envelope, len(ids))
	errs := make([]error, len(ids))
	sem := make(chan struct{}, 8)
	var wg gosync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows[i], errs[i] = e.searchRow(ctx, id)
		}()
	}
	wg.Wait()
	out := make([]*mail.Envelope, 0, len(rows))
	for i, row := range rows {
		if errors.Is(errs[i], gmail.ErrNotFound) {
			continue // deleted meanwhile
		}
		if errs[i] != nil {
			return nil, errs[i]
		}
		out = append(out, row)
	}
	return out, nil
}

// searchRow builds the row of one conversation: the newest message's
// envelope (from the store when it is there, else from Gmail's metadata),
// unread or starred when any message is, counting every message.
func (e *GmailEngine) searchRow(ctx context.Context, threadID string) (*mail.Envelope, error) {
	metas, err := e.api.ThreadMessages(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if len(metas) == 0 {
		return nil, gmail.ErrNotFound
	}
	latest := metas[0]
	unread, starred := false, false
	for _, m := range metas {
		if m.InternalDate >= latest.InternalDate {
			latest = m
		}
		unread = unread || slices.Contains(m.LabelIDs, "UNREAD")
		starred = starred || slices.Contains(m.LabelIDs, "STARRED")
	}
	var row *mail.Envelope
	for _, env := range e.store.ThreadEnvelopes(e.cfg.Name, threadID) {
		if env.ID == latest.ID {
			c := *env
			row = &c
		}
	}
	if row == nil {
		full, err := e.api.GetMetadata(ctx, latest.ID)
		if err != nil {
			return nil, err
		}
		row = envelopeOf(full)
	}
	row.ThreadID = threadID
	row.Flags = flagsOf(slices.Concat(latest.LabelIDs, boolIf(unread, "UNREAD"), boolIf(starred, "STARRED")))
	row.ThreadCount = len(metas)
	row.Participants = nil
	e.describeThread(row)
	if len(row.Participants) == 0 {
		row.Participants = row.From
	}
	return row, nil
}

func boolIf(ok bool, label string) []string {
	if ok {
		return []string{label}
	}
	return nil
}
