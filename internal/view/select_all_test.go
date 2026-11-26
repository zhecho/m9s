package view

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/ui"
)

// A selects every message the list shows; with a filter on, only its
// matches. A again, with all of them selected, clears them.
func TestSelectAll_selectsWhatTheFilterShows(t *testing.T) {
	stub := &moverStub{}
	stub.rows = threeMessages()
	ra := imapApp(t, stub, stub.rows)
	ra.maillist.SetFilter("i") // Meeting and Invoice; not Lunch
	shown := len(ra.maillist.visible)

	if sendRune(ra, 'A') != nil {
		t.Fatal("A was not consumed")
	}
	if got := len(markedIDs(ra.maillist)); got != shown || shown == 3 {
		t.Fatalf("marked %d of %d shown (filter must narrow the list)", got, shown)
	}
	ra.maillist.SetFilter("")
	for _, env := range ra.maillist.visible {
		if ra.maillist.marked[env.ID] && !strings.Contains(strings.ToLower(env.Subject), "i") {
			t.Errorf("%q was hidden by the filter but got selected", env.Subject)
		}
	}
}

func TestSelectAll_againClears(t *testing.T) {
	stub := &moverStub{}
	stub.rows = threeMessages()
	ra := imapApp(t, stub, stub.rows)
	sendRune(ra, 'A')
	if n := len(markedIDs(ra.maillist)); n != 3 {
		t.Fatalf("marked %d, want all 3", n)
	}
	sendRune(ra, 'A')
	if n := len(markedIDs(ra.maillist)); n != 0 {
		t.Errorf("marked %d after A again, want none", n)
	}
	// Some selected, not all: A completes the selection.
	ra.maillist.ToggleMark(1)
	sendRune(ra, 'A')
	if n := len(markedIDs(ra.maillist)); n != 3 {
		t.Errorf("marked %d, want A to select the rest", n)
	}
}

// The title counts what the list shows, and the selection.
func TestMailList_titleCountsMessages(t *testing.T) {
	mlv := NewMailListView(testSkin(), "Jan 02 15:04", nil)
	if got := mlv.TitleText(); got != "MAIL LIST" {
		t.Errorf("empty list title = %q, want MAIL LIST", got)
	}
	mlv.SetEnvelopes(threeMessages())
	if got := mlv.TitleText(); got != "MAIL LIST [3 messages]" {
		t.Errorf("title = %q, want MAIL LIST [3 messages]", got)
	}
	mlv.SetFilter("Lunch")
	if got := mlv.TitleText(); got != "MAIL LIST: filter /Lunch [1]" {
		t.Errorf("filtered title = %q", got)
	}
	mlv.SetFilter("")
	mlv.ToggleMark(0)
	mlv.ToggleMark(1)
	if got := mlv.TitleText(); got != "MAIL LIST [3 messages · 2 selected]" {
		t.Errorf("title with a selection = %q", got)
	}
	mlv.SetConversations(true)
	if got := mlv.TitleText(); got != "MAIL LIST [3 conversations · 2 selected]" {
		t.Errorf("conversation list title = %q", got)
	}
}

// The app keeps the title's count current as the list fills.
func TestMailList_titleFollowsTheList(t *testing.T) {
	stub := &moverStub{}
	stub.rows = threeMessages()
	ra := imapApp(t, stub, stub.rows)
	// tview shows "[3 messages[]" as "[3 messages]": ] is escaped.
	if got := ra.maillist.GetTitle(); !strings.Contains(got, "[3 messages[]") {
		t.Errorf("pane title = %q, want the count", got)
	}
}

// a, u and * act on the selection.
func TestArchive_marked(t *testing.T) {
	stub := &moverStub{}
	stub.rows = threeMessages()
	ra := imapApp(t, stub, stub.rows)
	ra.maillist.ToggleMark(0)
	ra.maillist.ToggleMark(2)
	sendRune(ra, 'a')
	stub.waitCall(t, "Archive INBOX 1")
	stub.waitCall(t, "Archive INBOX 3")
	if slices.Contains(stub.called(), "Archive INBOX 2") {
		t.Error("the unselected message was archived too")
	}
}

func TestUnread_marked(t *testing.T) {
	stub := &moverStub{}
	stub.rows = threeMessages()
	ra := imapApp(t, stub, stub.rows)
	ra.maillist.ToggleMark(1)
	ra.maillist.ToggleMark(2)
	sendRune(ra, 'u')
	stub.waitCall(t, `SetFlags INBOX 2 \Seen false`)
	stub.waitCall(t, `SetFlags INBOX 3 \Seen false`)
	if slices.Contains(stub.called(), `SetFlags INBOX 1 \Seen false`) {
		t.Error("the unselected message was marked unread too")
	}
}

// * stars every selected message unless all of them are starred already;
// then it unstars them.
func TestStar_marked(t *testing.T) {
	stub := &moverStub{}
	stub.rows = threeMessages()
	stub.rows[0].Flags = append(stub.rows[0].Flags, `\Flagged`)
	ra := imapApp(t, stub, stub.rows)
	ra.maillist.ToggleMark(0)
	ra.maillist.ToggleMark(1)
	sendRune(ra, '*')
	stub.waitCall(t, `SetFlags INBOX 1 \Flagged true`)
	stub.waitCall(t, `SetFlags INBOX 2 \Flagged true`)
}

func TestStar_markedAllStarredUnstars(t *testing.T) {
	stub := &moverStub{}
	stub.rows = threeMessages()
	for _, e := range stub.rows {
		e.Flags = append(e.Flags, `\Flagged`)
	}
	ra := imapApp(t, stub, stub.rows)
	ra.maillist.ToggleMark(0)
	ra.maillist.ToggleMark(2)
	sendRune(ra, '*')
	stub.waitCall(t, `SetFlags INBOX 1 \Flagged false`)
	stub.waitCall(t, `SetFlags INBOX 3 \Flagged false`)
}

// On Gmail, conversation rows are changed as whole conversations.
func TestArchive_markedConversations(t *testing.T) {
	ra, stub := gmailApp(t)
	second := *convRow()
	second.ID, second.ThreadID = "d", "T2"
	rows := []*mail.Envelope{convRow(), &second}
	stub.mu.Lock() // a list load from opening INBOX may be reading them
	stub.rows = rows
	stub.mu.Unlock()
	ra.showEnvelopes(onScreen(rows))
	sendRune(ra, 'A')
	sendRune(ra, 'a')
	stub.waitCall(t, "ModifyThread T + -INBOX")
	stub.waitCall(t, "ModifyThread T2 + -INBOX")
}

// A is listed in :help and can be rebound as select_all.
func TestSelectAll_isAKnownAction(t *testing.T) {
	km, _ := ui.LoadKeyMap("")
	if km['A'] != ui.ActionSelectAll {
		t.Errorf("A = %q, want select_all", km['A'])
	}
}

// gateStub blocks archiving until released, like a slow server.
type gateStub struct {
	moverStub
	gate chan struct{}
}

func (s *gateStub) Archive(ctx context.Context, folder, id string) error {
	<-s.gate
	return s.moverStub.Archive(ctx, folder, id)
}

// A bulk action says what it is doing, and a second one waits for it.
func TestMarked_bulkShowsProgressAndRefusesASecond(t *testing.T) {
	stub := &gateStub{gate: make(chan struct{})}
	stub.rows = threeMessages()
	ra := imapApp(t, stub, stub.rows)
	ra.maillist.ToggleMark(0)
	ra.maillist.ToggleMark(1)
	sendRune(ra, 'a')
	if got := ra.uiApp.StatusBar.FlashText(); !strings.Contains(got, "Archiving 2 messages…") {
		t.Errorf("status = %q, want the action in progress", got)
	}
	sendRune(ra, 'u')
	if got := ra.uiApp.StatusBar.FlashText(); !strings.Contains(got, "Still working") {
		t.Errorf("status = %q, want the second action refused", got)
	}
	close(stub.gate)
	stub.waitCall(t, "Archive INBOX 1")
	time.Sleep(50 * time.Millisecond)
	if slices.ContainsFunc(stub.called(), func(c string) bool { return strings.HasPrefix(c, "SetFlags") }) {
		t.Error("the refused action ran anyway")
	}
}
