package view

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"

	"github.com/zhecho/m9s/internal/mail"
	msync "github.com/zhecho/m9s/internal/sync"
)

// stubSearch is an account that can search the server.
type stubSearch struct {
	stubAccount
	found  []*mail.Envelope
	err    error
	queued chan func() // answers waiting for the UI loop
}

func (s *stubSearch) Search(_ context.Context, folder, query string) ([]*mail.Envelope, error) {
	s.record("Search " + folder + " " + query)
	return s.found, s.err
}

// searchApp is promptApp with a searching account on INBOX; the answer to a
// search is delivered by hand (the app's update queue is not running).
func searchApp(t *testing.T) (*RootApp, *stubSearch) {
	t.Helper()
	ra := newWiredRootApp(t, "test")
	recordBodyLoads(ra)
	stub := &stubSearch{found: []*mail.Envelope{
		{ID: "s1", UID: 11, Subject: "Invoice March", From: []mail.Address{{Name: "Dan", Email: "dan@x.io"}}},
		{ID: "s2", UID: 12, Subject: "Invoice April", From: []mail.Address{{Name: "Dan", Email: "dan@x.io"}}},
	}}
	stub.queued = make(chan func(), 4)
	ra.deliver = func(fn func()) { stub.queued <- fn }
	stub.rows = peopleEnvelopes()
	ra.engines["test"] = stub
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX", "Sent", "Drafts")})
	ra.currentFolder = &mail.Folder{Name: "INBOX"}
	ra.maillist.SetEnvelopes(peopleEnvelopes())
	return ra, stub
}

// enterSearch types text in the \ prompt, presses Enter and delivers the
// stub's answer.
func enterSearch(t *testing.T, ra *RootApp, stub *stubSearch, text string) {
	t.Helper()
	ra.startServerSearch()
	typeInPrompt(ra, text)
	promptKey(ra, tcell.KeyEnter) // tcell.KeyEnter
	stub.waitCall(t, "Search INBOX "+text)
	stub.runQueued(t)
}

// runQueued delivers the search answers queued so far, as the UI loop would.
func (s *stubSearch) runQueued(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case fn := <-s.queued:
			fn()
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("no search answer queued")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// \ opens the server search prompt; Enter sends the text to the server.
func TestBackslashRunsServerSearch(t *testing.T) {
	ra, stub := searchApp(t)
	ra.uiApp.SetFocus(ra.maillist)
	if sendRune(ra, '\\') != nil {
		t.Fatal("\\ was not consumed")
	}
	if ra.prompt == nil || !strings.Contains(ra.prompt.GetTitle(), "search") {
		t.Fatal("\\ did not open the search prompt")
	}
	typeInPrompt(ra, "invoice")
	promptKey(ra, tcell.KeyEnter)
	stub.waitCall(t, "Search INBOX invoice")
	stub.runQueued(t)
	if ra.maillist.VisibleCount() != 2 {
		t.Fatalf("rows = %d, want the 2 results", ra.maillist.VisibleCount())
	}
	if title := ra.maillist.GetTitle(); !strings.Contains(title, `IMAP search \invoice`) {
		t.Errorf("title = %q, want the query in it", title)
	}
	if ra.selectedEnv == nil || ra.selectedEnv.ID != "s1" {
		t.Errorf("selected = %+v, want the first result", ra.selectedEnv)
	}
}

func TestEscLeavesSearchRestoresFolder(t *testing.T) {
	ra, stub := searchApp(t)
	ra.maillist.Select(1, 0) // the folder's second row; typing will move the cursor off it
	enterSearch(t, ra, stub, "invoice")
	if sendKey(ra, tcell.KeyEscape) != nil { // tcell.KeyEscape
		t.Fatal("Esc in a search must be consumed")
	}
	if ra.search != nil || ra.maillist.VisibleCount() != 3 {
		t.Fatalf("search %v, rows %d; want left, 3", ra.search, ra.maillist.VisibleCount())
	}
	if ra.selectedEnv == nil || ra.selectedEnv.ID != "2" {
		t.Errorf("selected = %+v, want row 2 again", ra.selectedEnv)
	}
	if title := ra.maillist.GetTitle(); strings.Contains(title, "search") {
		t.Errorf("title = %q still says search", title)
	}
}

func TestEscInPromptLeavesSearch(t *testing.T) {
	ra, stub := searchApp(t)
	enterSearch(t, ra, stub, "invoice")
	ra.startFilter()
	promptKey(ra, tcell.KeyEscape)
	if ra.search != nil || ra.maillist.VisibleCount() != 3 {
		t.Fatalf("search %v, rows %d; want left, 3", ra.search, ra.maillist.VisibleCount())
	}
}

// Enter in the / filter only keeps the filter: the server is not asked.
func TestFilterEnterDoesNotSearchTheServer(t *testing.T) {
	ra, stub := searchApp(t)
	ra.startFilter()
	typeInPrompt(ra, "bob")
	promptKey(ra, tcell.KeyEnter)
	time.Sleep(50 * time.Millisecond)
	if slices.ContainsFunc(stub.called(), func(c string) bool { return strings.HasPrefix(c, "Search") }) {
		t.Errorf("Enter in the filter searched the server: %q", stub.called())
	}
	if ra.maillist.FilterQuery() != "bob" || ra.uiApp.GetFocus() != tview.Primitive(ra.maillist) {
		t.Errorf("filter %q, focus %T; want the filter kept and the list focused", ra.maillist.FilterQuery(), ra.uiApp.GetFocus())
	}
}

// An account that cannot search says so instead of opening the prompt.
func TestBackslashWithoutServerSearch(t *testing.T) {
	ra := promptApp(t) // no engine for the account
	ra.uiApp.SetFocus(ra.maillist)
	sendRune(ra, '\\')
	if got := ra.uiApp.StatusBar.FlashText(); !strings.Contains(got, "server search: not available") {
		t.Errorf("status = %q", got)
	}
}

func TestTypingStillFiltersLocally(t *testing.T) {
	ra, stub := searchApp(t)
	ra.startFilter()
	typeInPrompt(ra, "bob")
	if ra.maillist.VisibleCount() != 1 {
		t.Errorf("rows = %d while typing, want 1", ra.maillist.VisibleCount())
	}
	if calls := stub.called(); len(calls) != 0 {
		t.Errorf("typing searched the server: %q", calls)
	}
}

func TestSearchErrorKeepsTheList(t *testing.T) {
	ra, stub := searchApp(t)
	stub.err = errors.New("boom")
	ra.startServerSearch()
	typeInPrompt(ra, "bob")
	promptKey(ra, tcell.KeyEnter)
	stub.waitCall(t, "Search INBOX bob")
	ra.searchArrived(ra.searchSeq, ra.manager.Active(), ra.currentFolder, "bob", "", nil, stub.err)
	if ra.search != nil || ra.maillist.VisibleCount() != 3 {
		t.Errorf("search %v, rows %d; want the folder's list kept", ra.search, ra.maillist.VisibleCount())
	}
	if got := ra.uiApp.StatusBar.FlashText(); !strings.Contains(got, "boom") {
		t.Errorf("status = %q, want the error", got)
	}
}

func TestFolderChangeLeavesSearch(t *testing.T) {
	ra, stub := searchApp(t)
	enterSearch(t, ra, stub, "invoice")
	ra.onFolderSelected(&mail.Folder{Name: "Sent"})
	if ra.search != nil || strings.Contains(ra.maillist.TitleText(), "search") {
		t.Errorf("search %v, title %q; want left", ra.search, ra.maillist.TitleText())
	}
}

func TestReloadDoesNotReplaceSearchResults(t *testing.T) {
	ra, stub := searchApp(t)
	enterSearch(t, ra, stub, "invoice")
	ra.showFetched(ra.manager.Active(), ra.currentFolder, peopleEnvelopes())
	if ra.maillist.VisibleCount() != 2 || ra.search == nil {
		t.Errorf("rows = %d; a reload replaced the search results", ra.maillist.VisibleCount())
	}
}

func TestStaleSearchAnswerIsDropped(t *testing.T) {
	ra, stub := searchApp(t)
	ra.startServerSearch()
	typeInPrompt(ra, "invoice")
	promptKey(ra, tcell.KeyEnter)
	stub.waitCall(t, "Search INBOX invoice")
	old := ra.searchSeq
	ra.dropSearch() // the user moved on
	ra.searchArrived(old, ra.manager.Active(), ra.currentFolder, "invoice", "", stub.found, nil)
	if ra.search != nil {
		t.Error("an answer nobody waits for was shown")
	}
}

func TestEnterWithoutServerSearchKeepsFilter(t *testing.T) {
	ra := promptApp(t) // no engine for the account
	ra.startFilter()
	typeInPrompt(ra, "bob")
	promptKey(ra, tcell.KeyEnter)
	if ra.search != nil || ra.maillist.FilterQuery() != "bob" {
		t.Errorf("search %v, filter %q", ra.search, ra.maillist.FilterQuery())
	}
}

// Search results of a conversation account act as conversations in any folder.
func TestSearchResultsOnGmailAreConversations(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	recordBodyLoads(ra)
	srch := &stubSearchThreads{stubThreads: &stubThreads{thread: convMessages()}, found: []*mail.Envelope{convRow()}}
	ra.engines["test"] = srch
	ra.deliver = func(func()) {}
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX", "Sent", "Trash")})
	ra.currentFolder = &mail.Folder{Name: "Trash"} // a folder listed as messages
	ra.startServerSearch()
	typeInPrompt(ra, "lunch")
	promptKey(ra, tcell.KeyEnter)
	srch.waitCall(t, "Search Trash lunch")
	ra.searchArrived(ra.searchSeq, ra.manager.Active(), ra.currentFolder, "lunch", "", srch.found, nil)
	ra.uiApp.SetFocus(ra.maillist)
	ra.maillist.Select(0, 0)
	if _, _, _, env, ok := ra.selectedConversation(); !ok || env.ThreadID != "T" {
		t.Fatal("a search result is not treated as a conversation")
	}
	var _ msync.ServerSearch = srch
}

type stubSearchThreads struct {
	*stubThreads
	found []*mail.Envelope
}

func (s *stubSearchThreads) Search(_ context.Context, folder, q string) ([]*mail.Envelope, error) {
	s.record("Search " + folder + " " + q)
	return s.found, nil
}

func TestEscInListCancelsInFlightSearch(t *testing.T) {
	ra, stub := searchApp(t)
	ra.maillist.Select(1, 0)
	ra.startServerSearch()
	typeInPrompt(ra, "invoice")
	promptKey(ra, tcell.KeyEnter)
	stub.waitCall(t, "Search INBOX invoice")
	if sendKey(ra, tcell.KeyEscape) != nil { // the search has not answered yet
		t.Fatal("Esc with a search on its way must be consumed")
	}
	after := ra.selectedEnv // what Esc left selected
	stub.runQueued(t)       // the answer arrives late
	if ra.search != nil || ra.maillist.VisibleCount() != 3 {
		t.Fatalf("search %v, rows %d; a cancelled search showed its results", ra.search, ra.maillist.VisibleCount())
	}
	if ra.selectedEnv != after {
		t.Errorf("selected = %+v, want %+v unchanged", ra.selectedEnv, after)
	}
}
