package view

import (
	"context"
	"fmt"
	"strings"

	"github.com/zhecho/m9s/internal/mail"
	msync "github.com/zhecho/m9s/internal/sync"
	"github.com/zhecho/m9s/internal/ui"
)

// searchState is a server search whose results are on screen. The rows are
// the provider's answer and never reach the store; what the folder showed
// before is kept here to come back to.
type searchState struct {
	account string
	folder  string
	query   string
	saved   []*mail.Envelope // the folder's list before the search
	keepID  string           // the row that was selected
	// The server's count (total; estimate: Gmail's guess), the cursor of
	// the next page ("" on the last) and whether it is being loaded.
	total    int
	estimate bool
	next     string
	loading  bool
}

// searchKindOf names an account's server search for the title.
func searchKindOf(acct *mail.Account) string {
	switch {
	case acct == nil || acct.Config == nil:
		return "server"
	case acct.Config.IsGmail():
		return "Gmail"
	case acct.Config.IsMicrosoft():
		return "Outlook"
	}
	return "IMAP"
}

// searchWait is how long a page of an account's server search takes, when
// that is worth saying: Gmail reads each conversation it finds — about
// 2,000–3,000 quota units a page, at 85 a second. "" elsewhere.
func searchWait(acct *mail.Account) string {
	if acct != nil && acct.Config != nil && acct.Config.IsGmail() {
		return "~20–30 s"
	}
	return ""
}

// withWait is text with the wait after it, when there is one.
func withWait(text, wait string) string {
	if wait == "" {
		return text
	}
	return text + " " + wait
}

// conversationRows reports whether the rows of folder in account are
// conversations: the folder's list is, or search results on an account with
// conversations are (whatever folder they were asked in).
func (ra *RootApp) conversationRows(account, folder string) bool {
	if s := ra.search; s != nil && s.account == account && s.folder == folder {
		_, ok := ra.engines[account].(msync.Threads)
		return ok
	}
	return ra.conversations(account, folder)
}

// startServerSearch handles \: a prompt whose text, on Enter, goes to the
// server as a search (Gmail search syntax, Outlook search, IMAP text search)
// of the open folder. Esc closes it and leaves any search on screen. An
// account that cannot search says so.
func (ra *RootApp) startServerSearch() {
	acct := ra.manager.Active()
	if acct == nil || ra.currentFolder == nil {
		return
	}
	if _, ok := ra.engines[acct.Name].(msync.ServerSearch); !ok {
		ra.uiApp.StatusBar.Info("server search: not available for this account")
		return
	}
	previous := ra.uiApp.GetFocus()
	keepID := "" // the row to come back to when the search is left
	if ra.selectedEnv != nil {
		keepID = ra.selectedEnv.ID
	}
	ra.openPrompt(false, ui.PromptConfig{
		Label:  "search",
		Prefix: `\`,
		OnDone: func(text string, accepted bool) {
			if !accepted || strings.TrimSpace(text) == "" {
				ra.closePrompt(previous)
				return
			}
			ra.closePrompt(ra.maillist)
			ra.refreshMailListTitle() // focus changed after the title was last set
			ra.startSearch(strings.TrimSpace(text), keepID)
		},
	})
}

// startSearch asks the server for text (keepID: the row that was selected
// before), in the open folder, on accounts that can search.
func (ra *RootApp) startSearch(text, keepID string) {
	acct, folder := ra.manager.Active(), ra.currentFolder
	if text == "" || acct == nil || folder == nil {
		return
	}
	eng, ok := ra.engines[acct.Name]
	if !ok {
		return
	}
	srv, ok := eng.(msync.ServerSearch)
	if !ok {
		return
	}
	ra.searchSeq++
	seq := ra.searchSeq
	ra.searching = true
	kind, wait := searchKindOf(acct), searchWait(acct)
	status := fmt.Sprintf("searching %s for %q…", kind, text)
	if wait != "" {
		status += " (about " + strings.TrimPrefix(wait, "~") + ")"
	}
	ra.uiApp.StatusBar.Info(status)
	// The title says so at once; the rows stay the folder's until the answer.
	ra.maillist.SetSearch(text)
	ra.maillist.SetSearchInfo(kind, 0, false, false)
	ra.maillist.SetSearchBusy(withWait("searching…", wait), true)
	ra.refreshMailListTitle()
	go func() {
		var res msync.SearchResult
		var err error
		if ps, ok := srv.(msync.PagedSearch); ok {
			res, err = ps.SearchPage(context.Background(), folder.Name, text, "")
		} else {
			res.Rows, err = srv.Search(context.Background(), folder.Name, text)
			res.Total = len(res.Rows)
		}
		ra.searchDeliver(func() { ra.searchPageArrived(seq, acct, folder, text, keepID, res, err) })
	}()
}

// searchDeliver runs fn on the UI goroutine; tests replace it.
func (ra *RootApp) searchDeliver(fn func()) { ra.onUI(fn) }

// searchArrived shows the answer to a search, unless another was asked or
// the user moved on meanwhile; a failure is said and the filter kept. UI
// goroutine only.
func (ra *RootApp) searchArrived(seq int, acct *mail.Account, folder *mail.Folder, text, keepID string, envs []*mail.Envelope, err error) {
	ra.searchPageArrived(seq, acct, folder, text, keepID, msync.SearchResult{Rows: envs, Total: len(envs)}, err)
}

// searchPageArrived shows a search's first page, with the server's count.
// UI goroutine only.
func (ra *RootApp) searchPageArrived(seq int, acct *mail.Account, folder *mail.Folder, text, keepID string, res msync.SearchResult, err error) {
	if seq != ra.searchSeq || !ra.isCurrent(acct, folder) {
		return
	}
	ra.searching = false
	ra.maillist.SetSearchBusy("", false)
	if err != nil {
		ui.AppLog.Warn("[%s] search %q: %v", acct.Name, text, err)
		ra.uiApp.StatusBar.Error("search failed: " + err.Error())
		if ra.search == nil {
			ra.maillist.SetSearch("") // the folder's list stays, under its own title
		} else {
			ra.maillist.SetSearch(ra.search.query) // the search on screen stays
		}
		ra.refreshMailListTitle()
		return
	}
	ra.showSearch(acct, folder, text, keepID, res.Rows)
	ra.search.total, ra.search.estimate, ra.search.next = res.Total, res.Estimate, res.Next
	ra.maillist.SetSearchInfo(searchKindOf(acct), res.Total, res.Estimate, res.Next != "")
	ra.refreshMailListTitle()
}

// loadMoreSearch loads the next page of the search on screen when the
// cursor reaches the last row. UI goroutine only.
func (ra *RootApp) loadMoreSearch() {
	s := ra.search
	acct, f := ra.manager.Active(), ra.currentFolder
	if s == nil || s.next == "" || s.loading || acct == nil || f == nil {
		return
	}
	ps, ok := ra.engines[acct.Name].(msync.PagedSearch)
	if !ok {
		return
	}
	s.loading = true
	kind, wait := searchKindOf(acct), searchWait(acct)
	ra.maillist.SetSearchBusy(withWait("loading more…", wait), false)
	ra.refreshMailListTitle()
	if wait != "" {
		ra.uiApp.StatusBar.Info(fmt.Sprintf("loading more %s results… (about %s)", kind, strings.TrimPrefix(wait, "~")))
	}
	seq, cursor, text := ra.searchSeq, s.next, s.query
	go func() {
		res, err := ps.SearchPage(context.Background(), f.Name, text, cursor)
		ra.onUI(func() {
			if seq != ra.searchSeq || ra.search != s {
				return
			}
			s.loading = false
			ra.maillist.SetSearchBusy("", false)
			if err != nil {
				ra.fetchFailed(acct.Name, "loading more results", err)
				ra.refreshMailListTitle()
				return
			}
			s.next = res.Next
			if res.Total > 0 {
				s.total, s.estimate = res.Total, res.Estimate
			}
			ra.reloading = true
			ra.maillist.AppendEnvelopes(res.Rows)
			ra.reloading = false
			ra.maillist.SetSearchInfo(kind, s.total, s.estimate, s.next != "")
			ra.refreshMailListTitle()
		})
	}()
}

// showSearch puts search results in the mail list. UI goroutine only.
func (ra *RootApp) showSearch(acct *mail.Account, folder *mail.Folder, text, keepID string, envs []*mail.Envelope) {
	if ra.search == nil {
		s := &searchState{account: acct.Name, folder: folder.Name,
			saved: ra.maillist.Envelopes(), keepID: keepID}
		ra.search = s
	}
	ra.search.query = text
	ra.maillist.SetSelf(ra.selfAddress())
	ra.maillist.SetSearch(text)
	ra.maillist.SetListed(false) // the server's answer: narrowed locally, counted as is
	ra.maillist.SetSearchInfo(searchKindOf(acct), len(envs), false, false) // the caller may know more
	ra.reloading = true
	ra.maillist.SetEnvelopes(envs)
	ra.reloading = false
	ra.refreshMailListTitle()
	ra.refreshHeader()
	if len(envs) == 0 {
		ra.uiApp.StatusBar.Info("no matches")
	}
}

// dropSearch forgets a search and any answer still on its way, leaving the
// list as it is. UI goroutine only.
func (ra *RootApp) dropSearch() {
	ra.searchSeq++
	ra.searching = false
	ra.search = nil
	ra.maillist.SetSearch("")
	ra.maillist.SetSearchBusy("", false)
}

// leaveSearch brings back the list the search replaced, with the same row
// selected, and reloads it. Nothing happens when no search is on screen.
func (ra *RootApp) leaveSearch() {
	s := ra.search
	ra.dropSearch()
	if s == nil {
		return
	}
	ra.maillist.SetFilter("")
	ra.list.set("")
	ra.reloading = true
	ra.maillist.ReplaceEnvelopes(s.saved, s.keepID)
	ra.reloading = false
	ra.refreshMailListTitle()
	ra.refreshHeader()
	if acct, f := ra.manager.Active(), ra.currentFolder; acct != nil && f != nil && acct.Name == s.account {
		go ra.loadEnvelopes(acct, f)
	}
}
