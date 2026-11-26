package view

import (
	"context"
	gosync "sync"
	"time"

	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/store"
	msync "github.com/zhecho/m9s/internal/sync"
)

// filterDelay is how long the filter waits after the last keystroke before
// it searches the folder.
const filterDelay = 150 * time.Millisecond

// listState is what the open list asks its engine's Lister for: the filter,
// how many rows are loaded, and a generation that every filter or folder
// change bumps, so answers to an older one are dropped. loadEnvelopes reads
// it off the UI goroutine, hence the lock.
type listState struct {
	mu     gosync.Mutex
	filter string
	loaded int  // rows the list holds; a reload asks for as many
	total  int  // the folder's count from the last unfiltered listing
	gen    int  // bumped on every filter or folder change
	more   bool // a next page is on its way
	// exhausted: a next page came back empty — the local copy holds fewer
	// rows than the server counts (a first sync still running).
	exhausted bool
}

// set starts a new listing: filter (or none), from the first page.
func (l *listState) set(filter string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.filter, l.loaded, l.more, l.exhausted = filter, store.ListSize, false, false
	l.total = 0
	l.gen++
}

// snapshot is the filter, how many rows to ask for, and the generation.
func (l *listState) snapshot() (filter string, limit, gen int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.filter, max(l.loaded, store.ListSize), l.gen
}

func (l *listState) current(gen int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.gen == gen
}

func (l *listState) grow(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.loaded = max(l.loaded, n)
}

func (l *listState) setTotal(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total = n
}

func (l *listState) folderTotal() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total
}

func (l *listState) setExhausted() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.exhausted = true
}

func (l *listState) isExhausted() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.exhausted
}

// startMore claims the next-page load; false when one is on its way.
func (l *listState) startMore() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.more {
		return false
	}
	l.more = true
	return true
}

func (l *listState) endMore() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.more = false
}

// lister is the open account's Lister, if it has one.
func (ra *RootApp) lister(acct *mail.Account) (msync.Lister, bool) {
	if acct == nil {
		return nil, false
	}
	l, ok := ra.engines[acct.Name].(msync.Lister)
	return l, ok
}

// showListed puts a page from the Lister on screen, unless the filter or
// folder changed meanwhile. UI goroutine only.
func (ra *RootApp) showListed(acct *mail.Account, f *mail.Folder, rows []*mail.Envelope, total int, filter string, gen int) {
	if !ra.list.current(gen) || !ra.isCurrent(acct, f) || ra.search != nil || filter != ra.maillist.FilterQuery() {
		return
	}
	ra.maillist.SetListed(true)
	ra.maillist.SetRowsFor(filter)
	if filter == "" {
		ra.list.setTotal(total)
	} else {
		ra.maillist.SetTotals(total, 0)
	}
	ra.showEnvelopes(rows)
	ra.list.grow(ra.maillist.LoadedCount())
	ra.refreshMailListTitle()
}

// listTotals gives the title its counts while no filter is on: the folder's
// count and unread count when known, else the Lister's count.
func (ra *RootApp) listTotals() {
	if !ra.maillist.Listed() || ra.maillist.FilterQuery() != "" || ra.search != nil {
		return
	}
	if ra.list.isExhausted() { // the local copy holds no more than is loaded
		ra.maillist.SetTotals(ra.maillist.LoadedCount(), ra.maillist.unread)
		return
	}
	total, unread := ra.list.folderTotal(), 0
	if acct, f := ra.manager.Active(), ra.currentFolder; acct != nil && f != nil {
		if unseen, n, ok := ra.folders.Counts(acct.Name, f.Name); ok {
			unread = int(unseen)
			if n > 0 {
				total = int(n)
			}
		}
	}
	ra.maillist.SetTotals(max(total, ra.maillist.LoadedCount()), unread)
}

// loadMore fetches the next page when the cursor reaches the last row and
// the folder (or the filter) has more. UI goroutine only.
func (ra *RootApp) loadMore() {
	if ra.reloading {
		return
	}
	if ra.search != nil {
		ra.loadMoreSearch()
		return
	}
	if !ra.maillist.Listed() {
		return
	}
	loaded := ra.maillist.LoadedCount()
	if loaded >= ra.maillist.Total() {
		return
	}
	acct, f := ra.manager.Active(), ra.currentFolder
	lister, ok := ra.lister(acct)
	if !ok || f == nil || !ra.list.startMore() {
		return
	}
	filter, _, gen := ra.list.snapshot()
	go func() {
		rows, _, err := lister.List(context.Background(), f.Name, store.ListQuery{Filter: filter, Offset: loaded, Limit: store.ListSize})
		ra.onUI(func() {
			ra.list.endMore()
			if !ra.list.current(gen) || !ra.isCurrent(acct, f) {
				return
			}
			if err != nil {
				ra.fetchFailed(acct.Name, "loading more", err)
				return
			}
			if len(rows) == 0 {
				ra.list.setExhausted()
			}
			ra.reloading = true
			ra.maillist.AppendEnvelopes(rows)
			ra.reloading = false
			ra.list.grow(ra.maillist.LoadedCount())
			ra.refreshMailListTitle()
		})
	}()
}

// selectAll handles A. With every row loaded it toggles them; otherwise it
// asks the Lister for every row the list would show (all filter matches,
// or the whole folder) and selects them. Once all are selected, A selects
// none. UI goroutine only.
func (ra *RootApp) selectAll() {
	if !ra.maillist.Listed() || ra.search != nil || ra.maillist.LoadedCount() >= ra.maillist.Total() {
		ra.maillist.ToggleMarkAll()
		return
	}
	if ra.maillist.AllMarked() || len(ra.maillist.Marked()) >= ra.maillist.Total() {
		ra.maillist.ClearMarks()
		return
	}
	acct, f := ra.manager.Active(), ra.currentFolder
	lister, ok := ra.lister(acct)
	if !ok || f == nil {
		return
	}
	filter, _, gen := ra.list.snapshot()
	ra.uiApp.StatusBar.Info("selecting…")
	go func() {
		rows, _, err := lister.List(context.Background(), f.Name, store.ListQuery{Filter: filter})
		ra.onUI(func() {
			if !ra.list.current(gen) || !ra.isCurrent(acct, f) {
				return
			}
			if err != nil {
				ra.fetchFailed(acct.Name, "select all", err)
				return
			}
			ra.maillist.MarkAll(rows)
			ra.uiApp.StatusBar.Info("Selected " + ra.countText(acct, f, len(rows)))
		})
	}()
}

// onUI runs fn on the UI goroutine; tests replace it (deliver).
func (ra *RootApp) onUI(fn func()) {
	if ra.deliver != nil {
		ra.deliver(fn)
		return
	}
	ra.uiApp.QueueUpdateDraw(fn)
}
