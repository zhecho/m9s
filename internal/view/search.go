package view

import (
	"context"

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

// startSearch asks the server for text (keepID: the row that was selected
// before the filter was typed), in the open folder, on accounts
// that can search; the local filter stays until the answer arrives. Off
// that, Enter only keeps the filter, as before.
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
	ra.uiApp.StatusBar.Info("searching…")
	go func() {
		envs, err := srv.Search(context.Background(), folder.Name, text)
		ra.searchDeliver(func() { ra.searchArrived(seq, acct, folder, text, keepID, envs, err) })
	}()
}

// searchDeliver runs fn on the UI goroutine; tests replace it.
func (ra *RootApp) searchDeliver(fn func()) {
	if ra.deliver != nil {
		ra.deliver(fn)
		return
	}
	ra.uiApp.QueueUpdateDraw(fn)
}

// searchArrived shows the answer to a search, unless another was asked or
// the user moved on meanwhile; a failure is said and the filter kept. UI
// goroutine only.
func (ra *RootApp) searchArrived(seq int, acct *mail.Account, folder *mail.Folder, text, keepID string, envs []*mail.Envelope, err error) {
	if seq != ra.searchSeq || !ra.isCurrent(acct, folder) {
		return
	}
	if err != nil {
		ui.AppLog.Warn("[%s] search %q: %v", acct.Name, text, err)
		ra.uiApp.StatusBar.Error("search failed: " + err.Error())
		return
	}
	ra.showSearch(acct, folder, text, keepID, envs)
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
	ra.search = nil
	ra.maillist.SetSearch("")
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
	ra.reloading = true
	ra.maillist.ReplaceEnvelopes(s.saved, s.keepID)
	ra.reloading = false
	ra.refreshMailListTitle()
	ra.refreshHeader()
	if acct, f := ra.manager.Active(), ra.currentFolder; acct != nil && f != nil && acct.Name == s.account {
		go ra.loadEnvelopes(acct, f)
	}
}
