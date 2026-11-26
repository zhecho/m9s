package view

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	gosync "sync"
	"testing"
	"time"

	tcell "github.com/derailed/tcell/v2"
	"github.com/zhecho/m9s/internal/mail"
	msync "github.com/zhecho/m9s/internal/sync"
)

// pagedSearchStub answers a server search a page of 50 at a time. total is
// what it says matches (0: does not say); estimate marks it as a guess.
type pagedSearchStub struct {
	moverStub
	results  int
	total    int
	estimate bool
	smu      gosync.Mutex
	cursors  []string
	queued   chan func()
}

func (s *pagedSearchStub) Search(ctx context.Context, folder, q string) ([]*mail.Envelope, error) {
	res, err := s.SearchPage(ctx, folder, q, "")
	return res.Rows, err
}

func (s *pagedSearchStub) SearchPage(_ context.Context, _, _ string, cursor string) (msync.SearchResult, error) {
	s.smu.Lock()
	s.cursors = append(s.cursors, cursor)
	s.smu.Unlock()
	from, _ := strconv.Atoi(cursor)
	res := msync.SearchResult{Total: s.total, Estimate: s.estimate}
	for i := from; i < min(from+50, s.results); i++ {
		n := s.results - i // newest first
		res.Rows = append(res.Rows, &mail.Envelope{ID: fmt.Sprint("r", n), UID: uint32(n), Subject: fmt.Sprint("linkedin ", n),
			Date: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Minute), Flags: []string{`\Seen`}})
	}
	if from+50 < s.results {
		res.Next = strconv.Itoa(from + 50)
	}
	return res, nil
}

func (s *pagedSearchStub) waitCursor(t *testing.T, cursor string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.smu.Lock()
		ok := slices.Contains(s.cursors, cursor)
		s.smu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no search page asked at cursor %q", cursor)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *pagedSearchStub) run(t *testing.T) {
	t.Helper()
	select {
	case fn := <-s.queued:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("nothing delivered to the UI")
	}
}

// pagedSearchApp is an account of provider (imap, gmail, microsoft) on
// INBOX whose server search pages through results.
func pagedSearchApp(t *testing.T, provider string, stub *pagedSearchStub) *RootApp {
	t.Helper()
	stub.queued = make(chan func(), 16)
	ra := newWiredRootApp(t, "test")
	ra.manager.Active().Config.Provider = provider
	recordBodyLoads(ra)
	ra.deliver = func(fn func()) { stub.queued <- fn }
	stub.rows = threeMessages()
	ra.engines["test"] = stub
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX", "Archive")})
	ra.uiApp.SetFocus(ra.maillist)
	return ra
}

func serverSearch(ra *RootApp, text string) {
	ra.startServerSearch()
	typeInPrompt(ra, text)
	promptKey(ra, tcell.KeyEnter)
}

// While the first page is on its way the title says so — on Gmail with how
// long that takes — then it counts the server's matches.
func TestServerSearch_titleWhileSearchingThenCount(t *testing.T) {
	stub := &pagedSearchStub{results: 120, total: 1240, estimate: true}
	ra := pagedSearchApp(t, "gmail", stub)
	serverSearch(ra, "linkedin")
	if got := ra.maillist.TitleText(); got != `MAIL LIST: Gmail search \linkedin [searching… ~20–30 s]` {
		t.Errorf("title while searching = %q", got)
	}
	if got := ra.uiApp.StatusBar.FlashText(); !strings.Contains(got, `searching Gmail for "linkedin"… (about 20–30 s)`) {
		t.Errorf("status = %q", got)
	}
	stub.waitCursor(t, "")
	stub.run(t)
	if got := ra.maillist.TitleText(); got != `MAIL LIST: Gmail search \linkedin [50 of ~1,240]` {
		t.Errorf("title = %q", got)
	}
}

// The last row loads the next page of results, saying so meanwhile.
func TestServerSearch_scrollLoadsMore(t *testing.T) {
	stub := &pagedSearchStub{results: 120, total: 120, estimate: true}
	ra := pagedSearchApp(t, "gmail", stub)
	serverSearch(ra, "linkedin")
	stub.waitCursor(t, "")
	stub.run(t)
	ra.maillist.Select(49, 0)
	stub.waitCursor(t, "50")
	if got := ra.maillist.TitleText(); got != `MAIL LIST: Gmail search \linkedin [50 of ~120 · loading more… ~20–30 s]` {
		t.Errorf("title while loading = %q", got)
	}
	stub.run(t)
	if got := ra.maillist.TitleText(); got != `MAIL LIST: Gmail search \linkedin [100 of ~120]` {
		t.Errorf("title = %q", got)
	}
}

// Outlook does not say how many match: "50+" while more pages follow, the
// plain count once they are all loaded. IMAP has no wait to mention.
func TestServerSearch_unknownTotal(t *testing.T) {
	stub := &pagedSearchStub{results: 70}
	ra := pagedSearchApp(t, "microsoft", stub)
	serverSearch(ra, "invoice")
	stub.waitCursor(t, "")
	stub.run(t)
	if got := ra.maillist.TitleText(); got != `MAIL LIST: Outlook search \invoice [50 of 50+]` {
		t.Errorf("title = %q", got)
	}
	ra.maillist.Select(49, 0)
	stub.waitCursor(t, "50")
	stub.run(t)
	if got := ra.maillist.TitleText(); got != `MAIL LIST: Outlook search \invoice [70]` {
		t.Errorf("title after the last page = %q", got)
	}
}

func TestServerSearch_imapSaysNoWait(t *testing.T) {
	stub := &pagedSearchStub{results: 10, total: 10}
	ra := pagedSearchApp(t, "imap", stub)
	serverSearch(ra, "x")
	if got := ra.maillist.TitleText(); got != `MAIL LIST: IMAP search \x [searching…]` {
		t.Errorf("title = %q", got)
	}
}
