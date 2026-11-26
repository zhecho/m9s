package view

import (
	"context"
	"fmt"
	"slices"
	"strings"
	gosync "sync"
	"testing"
	"time"

	tcell "github.com/derailed/tcell/v2"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/store"
	msync "github.com/zhecho/m9s/internal/sync"
)

// listerStub is an IMAP-like account (not a store keeper) whose folder has
// many messages and which lists them page by page, filtered over all.
type listerStub struct {
	moverStub
	all    []*mail.Envelope // newest first
	qmu    gosync.Mutex
	asked  []store.ListQuery
	queued chan func()
}

func (s *listerStub) List(_ context.Context, _ string, q store.ListQuery) ([]*mail.Envelope, int, error) {
	s.qmu.Lock()
	s.asked = append(s.asked, q)
	s.qmu.Unlock()
	var match []*mail.Envelope
	for _, e := range s.all {
		if q.Filter == "" || strings.Contains(strings.ToLower(e.Subject), strings.ToLower(q.Filter)) {
			cp := *e
			match = append(match, &cp)
		}
	}
	rows := match[min(q.Offset, len(match)):]
	if q.Limit > 0 && len(rows) > q.Limit {
		rows = rows[:q.Limit]
	}
	return rows, len(match), nil
}

func (s *listerStub) queries() []store.ListQuery {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	return slices.Clone(s.asked)
}

// waitQuery waits for a List call matching q.
func (s *listerStub) waitQuery(t *testing.T, q store.ListQuery) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !slices.Contains(s.queries(), q) {
		if time.Now().After(deadline) {
			t.Fatalf("List calls = %+v, want %+v", s.queries(), q)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// run delivers one queued UI update, as the UI loop would.
func (s *listerStub) run(t *testing.T) {
	t.Helper()
	select {
	case fn := <-s.queued:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("nothing delivered to the UI")
	}
}

// pagedApp opens INBOX of a folder with 120 messages, 70 of them invoices,
// and shows the first page.
func pagedApp(t *testing.T) (*RootApp, *listerStub) {
	t.Helper()
	stub := &listerStub{queued: make(chan func(), 16)}
	for i := 120; i >= 1; i-- {
		subject := fmt.Sprint("note ", i)
		if i <= 70 {
			subject = fmt.Sprint("Invoice ", i)
		}
		stub.all = append(stub.all, &mail.Envelope{ID: fmt.Sprint(i), UID: uint32(i), SortKey: int64(i), Subject: subject,
			Date: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute), Flags: []string{`\Seen`}})
	}
	ra := newWiredRootApp(t, "test")
	recordBodyLoads(ra)
	ra.deliver = func(fn func()) { stub.queued <- fn }
	ra.debounce = func(_ time.Duration, fn func()) { fn() }
	ra.engines["test"] = stub
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX", "Archive")})
	stub.waitQuery(t, store.ListQuery{Limit: 50})
	stub.run(t)
	ra.uiApp.SetFocus(ra.maillist)
	return ra, stub
}

func TestPaged_firstPageAndTitle(t *testing.T) {
	ra, _ := pagedApp(t)
	if n := ra.maillist.LoadedCount(); n != 50 {
		t.Fatalf("loaded %d, want 50", n)
	}
	if got := ra.maillist.TitleText(); got != "MAIL LIST [50 of 120 messages]" {
		t.Errorf("title = %q", got)
	}
}

// Reaching the last row loads the next page; a refresh keeps them all.
func TestPaged_scrollLoadsMoreAndRefreshKeepsThem(t *testing.T) {
	ra, stub := pagedApp(t)
	ra.maillist.Select(49, 0)
	stub.waitQuery(t, store.ListQuery{Offset: 50, Limit: 50})
	stub.run(t)
	if n := ra.maillist.LoadedCount(); n != 100 {
		t.Fatalf("loaded %d after scrolling, want 100", n)
	}
	if got := ra.maillist.TitleText(); got != "MAIL LIST [100 of 120 messages]" {
		t.Errorf("title = %q", got)
	}
	go ra.loadEnvelopes(ra.manager.Active(), ra.currentFolder) // new mail
	stub.waitQuery(t, store.ListQuery{Limit: 100})
	stub.run(t)
	if n := ra.maillist.LoadedCount(); n != 100 {
		t.Errorf("a refresh left %d rows, want the 100 loaded", n)
	}
}

// The filter searches the whole folder and the title counts the matches.
func TestPaged_filterSearchesTheWholeFolder(t *testing.T) {
	ra, stub := pagedApp(t)
	ra.startFilter()
	typeInPrompt(ra, "invoice")
	stub.waitQuery(t, store.ListQuery{Filter: "invoice", Limit: 50})
	for len(stub.queued) > 0 { // answers to the shorter texts typed on the way
		stub.run(t)
	}
	if got := ra.maillist.TitleText(); got != "MAIL LIST: filter /invoice [50 of 70]" {
		t.Errorf("title = %q", got)
	}
	if ra.maillist.LoadedCount() != 50 {
		t.Errorf("loaded %d, want the first 50 matches", ra.maillist.LoadedCount())
	}
}

// An answer to an older filter text never replaces a newer one.
func TestPaged_staleFilterAnswerDropped(t *testing.T) {
	ra, stub := pagedApp(t)
	ra.startFilter()
	typeInPrompt(ra, "note")
	stub.waitQuery(t, store.ListQuery{Filter: "note", Limit: 50})
	older := <-stub.queued // the "n", "no", "not", "note" answers…
	for len(stub.queued) > 1 {
		<-stub.queued
	}
	newest := <-stub.queued
	newest()
	older() // arrives late
	if got := ra.maillist.TitleText(); got != "MAIL LIST: filter /note [50]" {
		t.Errorf("title = %q, want the answer to the newest text", got)
	}
}

// A selects every match, loaded or not; a asks before touching rows that
// are not loaded, and Cancel leaves them alone.
func TestPaged_selectAllMatchesAndConfirm(t *testing.T) {
	ra, stub := pagedApp(t)
	ra.startFilter()
	typeInPrompt(ra, "invoice")
	stub.waitQuery(t, store.ListQuery{Filter: "invoice", Limit: 50})
	for len(stub.queued) > 0 {
		stub.run(t)
	}
	promptKey(ra, tcell.KeyEnter) // no server search here: the filter stays, the list gets the keys

	sendRune(ra, 'A')
	stub.waitQuery(t, store.ListQuery{Filter: "invoice"})
	stub.run(t)
	if n := len(ra.maillist.Marked()); n != 70 {
		t.Fatalf("A selected %d, want all 70 matches", n)
	}
	sendRune(ra, 'a')
	if !strings.Contains(ra.testDeleteModalText, "Archive 70 messages?") {
		t.Fatalf("prompt = %q, want it to ask before archiving 70", ra.testDeleteModalText)
	}
	ra.testDeleteModalDone(1, "Cancel")
	time.Sleep(50 * time.Millisecond)
	if slices.ContainsFunc(stub.called(), func(c string) bool { return strings.HasPrefix(c, "Archive") }) {
		t.Fatal("Cancel archived anyway")
	}
	sendRune(ra, 'a')
	ra.testDeleteModalDone(0, "Archive")
	stub.waitCall(t, "Archive INBOX 1") // an invoice never loaded
	stub.waitCall(t, "Archive INBOX 70")
}

// Only an unfiltered list from the server goes back to the local copy: a
// filtered page would make the store forget every message between matches.
func TestPaged_filteredRowsNeverReplaceTheLocalCopy(t *testing.T) {
	ra, stub := pagedApp(t)
	st := store.NewMemory()
	if err := st.AddMessages("test", "INBOX", stub.all); err != nil {
		t.Fatal(err)
	}
	ra.store = st
	ra.startFilter()
	typeInPrompt(ra, "invoice")
	stub.waitQuery(t, store.ListQuery{Filter: "invoice", Limit: 50})
	time.Sleep(50 * time.Millisecond)
	if _, n, _ := st.Page("test", "INBOX", store.ListQuery{}); n != 120 {
		t.Errorf("the local copy holds %d messages after filtering, want all 120", n)
	}
}

// pagedAppUnloaded is pagedApp before the first listing has been put on
// screen: its answer is still queued.
func pagedAppUnloaded(t *testing.T) (*RootApp, *listerStub) {
	t.Helper()
	stub := &listerStub{queued: make(chan func(), 32)}
	for i := 120; i >= 1; i-- {
		subject := fmt.Sprint("note ", i)
		if i <= 70 {
			subject = fmt.Sprint("Invoice ", i)
		}
		stub.all = append(stub.all, &mail.Envelope{ID: fmt.Sprint(i), UID: uint32(i), SortKey: int64(i), Subject: subject,
			Date: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute), Flags: []string{`\Seen`}})
	}
	ra := newWiredRootApp(t, "test")
	recordBodyLoads(ra)
	ra.deliver = func(fn func()) { stub.queued <- fn }
	ra.debounce = func(_ time.Duration, fn func()) { fn() }
	ra.engines["test"] = stub
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX", "Archive")})
	stub.waitQuery(t, store.ListQuery{Limit: 50})
	return ra, stub
}

func allMatch(t *testing.T, ra *RootApp, text string) {
	t.Helper()
	for _, env := range ra.maillist.visible {
		if !strings.Contains(strings.ToLower(env.Subject), text) {
			t.Fatalf("%q is shown under the filter %q", env.Subject, text)
		}
	}
}

// A filter typed before the folder's first listing arrives is not lost: the
// unfiltered answer never shows under it.
func TestPaged_filterTypedBeforeTheFirstListing(t *testing.T) {
	ra, stub := pagedAppUnloaded(t)
	ra.startFilter()
	typeInPrompt(ra, "invoice")
	stub.waitQuery(t, store.ListQuery{Filter: "invoice", Limit: 50})
	for len(stub.queued) > 0 {
		stub.run(t)
	}
	allMatch(t, ra, "invoice")
	if got := ra.maillist.TitleText(); got != "MAIL LIST: filter /invoice [50 of 70]" {
		t.Errorf("title = %q", got)
	}
}

// Until the folder's answer to a new text arrives, the rows on screen are
// narrowed to it, so nothing that does not match can be selected.
func TestPaged_newTextNarrowsAtOnce(t *testing.T) {
	ra, _ := pagedApp(t)
	ra.debounce = func(time.Duration, func()) {} // the folder search has not run yet
	ra.startFilter()
	typeInPrompt(ra, "note 1")
	allMatch(t, ra, "note 1")
}

// A server search's title counts its results, not the folder.
func TestPaged_searchTitleCountsResults(t *testing.T) {
	ra, _ := pagedApp(t)
	found := []*mail.Envelope{{ID: "s1", Subject: "Invoice 3"}, {ID: "s2", Subject: "Invoice 4"}}
	ra.showSearch(ra.manager.Active(), ra.currentFolder, "inv", "", found)
	if got := ra.maillist.TitleText(); got != `MAIL LIST: IMAP search \inv [2]` {
		t.Errorf("title = %q", got)
	}
}

// When the server counts more than the local copy holds (a first sync), A
// selects what can be listed, and A again selects none.
func TestPaged_selectAllTogglesWhenTheCopyIsShort(t *testing.T) {
	ra, stub := pagedApp(t)
	ra.handleEvent(msync.BadgeUpdated{AccountName: "test", FolderName: "INBOX", Total: 500})
	if got := ra.maillist.TitleText(); got != "MAIL LIST [50 of 500 messages]" {
		t.Fatalf("title = %q", got)
	}
	sendRune(ra, 'A')
	stub.waitQuery(t, store.ListQuery{})
	stub.run(t)
	if n := len(ra.maillist.Marked()); n != 120 {
		t.Fatalf("A selected %d, want the 120 listed", n)
	}
	sendRune(ra, 'A')
	if n := len(ra.maillist.Marked()); n != 0 {
		t.Errorf("A again left %d selected, want none", n)
	}
}

// Scrolling past what the local copy holds stops asking for more.
func TestPaged_loadMoreStopsWhenTheCopyEnds(t *testing.T) {
	ra, stub := pagedApp(t)
	ra.handleEvent(msync.BadgeUpdated{AccountName: "test", FolderName: "INBOX", Total: 500})
	for _, offset := range []int{50, 100} {
		ra.maillist.Select(ra.maillist.LoadedCount()-1, 0)
		stub.waitQuery(t, store.ListQuery{Offset: offset, Limit: 50})
		stub.run(t)
	}
	ra.maillist.Select(ra.maillist.LoadedCount()-1, 0) // 120 loaded: the copy has no more
	stub.waitQuery(t, store.ListQuery{Offset: 120, Limit: 50})
	stub.run(t)
	asked := len(stub.queries())
	ra.maillist.Select(0, 0)
	ra.maillist.Select(ra.maillist.LoadedCount()-1, 0)
	time.Sleep(50 * time.Millisecond)
	if n := len(stub.queries()); n != asked {
		t.Errorf("asked again after the copy ran out: %+v", stub.queries()[asked:])
	}
	if got := ra.maillist.TitleText(); got != "MAIL LIST [120 messages]" {
		t.Errorf("title = %q, want the count the copy holds", got)
	}
}

// Counts in prompts and status lines group thousands, as the title does.
func TestCountText_thousands(t *testing.T) {
	ra, _ := pagedApp(t)
	if got := ra.countText(ra.manager.Active(), ra.currentFolder, 2431); got != "2,431 messages" {
		t.Errorf("countText = %q", got)
	}
}
