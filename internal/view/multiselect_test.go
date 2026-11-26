package view

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/mail"
)

// trashStub is a moverStub that can also delete for good.
type trashStub struct{ moverStub }

func (s *trashStub) PermanentDelete(_ context.Context, folder, id string) error {
	s.record("PermanentDelete " + folder + " " + id)
	return nil
}

func threeMessages() []*mail.Envelope {
	day := func(d int) time.Time { return time.Date(2026, 11, d, 9, 0, 0, 0, time.UTC) }
	return []*mail.Envelope{
		{ID: "1", Subject: "Lunch", Date: day(3), Flags: []string{`\Seen`}},
		{ID: "2", Subject: "Meeting", Date: day(2), Flags: []string{`\Seen`}},
		{ID: "3", Subject: "Invoice", Date: day(1), Flags: []string{`\Seen`}},
	}
}

func markedIDs(mlv *MailListView) []string {
	var ids []string
	for _, env := range mlv.Marked() {
		ids = append(ids, env.ID)
	}
	return ids
}

func TestMailList_spaceMarksAndMovesDown(t *testing.T) {
	stub := &moverStub{}
	stub.rows = threeMessages()
	ra := imapApp(t, stub, stub.rows)

	if sendRune(ra, ' ') != nil {
		t.Fatal("space was not consumed")
	}
	if row, _ := ra.maillist.GetSelection(); row != 1 {
		t.Errorf("cursor on row %d after space, want 1", row)
	}
	sendRune(ra, ' ')
	if got := markedIDs(ra.maillist); !slices.Equal(got, []string{"1", "2"}) {
		t.Errorf("marked = %v, want [1 2]", got)
	}
	if got := ra.maillist.GetCell(0, 0).Text; !strings.HasPrefix(got, markSign) {
		t.Errorf("marked row shows %q, want the %q sign", got, markSign)
	}
	if !strings.Contains(ra.maillist.TitleText(), "· 2 selected]") {
		t.Errorf("title = %q, want it to count the selection", ra.maillist.TitleText())
	}
	// space on a marked row unmarks it
	ra.maillist.Select(0, 0)
	sendRune(ra, ' ')
	if got := markedIDs(ra.maillist); !slices.Equal(got, []string{"2"}) {
		t.Errorf("marked = %v after unmarking row 0, want [2]", got)
	}
}

func TestMailList_escClearsMarks(t *testing.T) {
	stub := &moverStub{}
	stub.rows = threeMessages()
	ra := imapApp(t, stub, stub.rows)
	sendRune(ra, ' ')
	ra.uiApp.GetInputCapture()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if n := len(ra.maillist.Marked()); n != 0 {
		t.Errorf("%d still marked after esc", n)
	}
}

func TestMailList_modifierClickMarks(t *testing.T) {
	mlv := NewMailListView(testSkin(), "Jan 02 15:04", nil)
	mlv.SetRect(0, 0, 80, 10) // border: rows start at y=1
	mlv.SetEnvelopes(threeMessages())
	handler := mlv.MouseHandler()
	click := func(y int, mod tcell.ModMask) {
		ev := tcell.NewEventMouse(5, y, tcell.Button1, mod)
		handler(tview.MouseLeftClick, ev, func(tview.Primitive) {})
	}

	click(3, tcell.ModShift) // row 2
	click(2, tcell.ModCtrl)  // row 1
	if got := markedIDs(mlv); !slices.Equal(got, []string{"2", "3"}) {
		t.Errorf("marked = %v, want [2 3]", got)
	}
	if row, _ := mlv.GetSelection(); row != 0 {
		t.Errorf("a modifier click moved the cursor to row %d", row)
	}
	click(3, tcell.ModShift) // again: unmarks
	click(2, tcell.ModNone)  // a plain click only moves the cursor
	if got := markedIDs(mlv); !slices.Equal(got, []string{"2"}) {
		t.Errorf("marked = %v, want [2]", got)
	}
	if row, _ := mlv.GetSelection(); row != 1 {
		t.Errorf("plain click put the cursor on row %d, want 1", row)
	}
}

func TestMailList_marksSurviveReloadButNotNewList(t *testing.T) {
	mlv := NewMailListView(testSkin(), "Jan 02 15:04", nil)
	envs := threeMessages()
	mlv.SetEnvelopes(envs)
	mlv.ToggleMark(0)
	mlv.ToggleMark(2)

	mlv.ReplaceEnvelopes(envs[1:], "") // message 1 left the folder
	if got := markedIDs(mlv); !slices.Equal(got, []string{"3"}) {
		t.Errorf("marked = %v after reload, want [3]", got)
	}
	mlv.SetFilter("Meeting")
	if n := len(mlv.Marked()); n != 0 {
		t.Errorf("a hidden marked message is acted on: %d marked on screen", n)
	}
	mlv.SetFilter("")
	mlv.SetEnvelopes(envs)
	if n := len(mlv.Marked()); n != 0 {
		t.Errorf("%d marks kept across a new list", n)
	}
}

func TestMove_marked(t *testing.T) {
	stub := &moverStub{targets: []string{"Work"}}
	stub.rows = threeMessages()
	ra := imapApp(t, stub, stub.rows)
	ra.maillist.ToggleMark(0)
	ra.maillist.ToggleMark(2)

	sendRune(ra, 'm')
	if !ra.uiApp.Pages.HasPage(pageMovePicker) {
		t.Fatal("m did not open the folder picker")
	}
	ra.movePicker.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), nil)

	stub.waitCall(t, "Move INBOX 1 Work")
	stub.waitCall(t, "Move INBOX 3 Work")
	if slices.Contains(stub.called(), "Move INBOX 2 Work") {
		t.Error("the unmarked message was moved too")
	}
}

func TestDelete_markedAsksOnceAndTrashesAll(t *testing.T) {
	stub := &moverStub{}
	stub.rows = threeMessages()
	ra := imapApp(t, stub, stub.rows)
	ra.maillist.ToggleMark(1)
	ra.maillist.ToggleMark(2)

	sendRune(ra, 'd')
	if !strings.Contains(ra.testDeleteModalText, "2 messages") {
		t.Fatalf("delete prompt = %q, want it to name 2 messages", ra.testDeleteModalText)
	}
	ra.testDeleteModalDone(0, "Delete")

	stub.waitCall(t, "MoveToTrash INBOX 2")
	stub.waitCall(t, "MoveToTrash INBOX 3")
}

func TestDelete_markedInTrashStartsOnCancel(t *testing.T) {
	stub := &trashStub{}
	stub.rows = threeMessages()
	ra := imapApp(t, stub, stub.rows)
	ra.currentFolder = &mail.Folder{Name: "Trash"}
	ra.maillist.ToggleMark(0)
	ra.maillist.ToggleMark(1)

	sendRune(ra, 'd')
	if !strings.Contains(ra.testDeleteModalText, "Permanently delete 2 messages") {
		t.Fatalf("delete prompt = %q", ra.testDeleteModalText)
	}
	if ra.testDeleteButtons[ra.testDeleteFocus].Label != "Cancel" {
		t.Errorf("prompt starts on %q, want Cancel", ra.testDeleteButtons[ra.testDeleteFocus].Label)
	}
	ra.testDeleteModalDone(0, "Delete Forever")
	stub.waitCall(t, "PermanentDelete Trash 1")
	stub.waitCall(t, "PermanentDelete Trash 2")
}

// End to end on an IMAP server: two of the three messages in Deleted Items
// are marked with space and deleted for good with one d.
func TestFlow_markedDeleteForever(t *testing.T) {
	h := trashHarness(t)
	h.key(' ')
	h.key(' ')
	h.eventually("two marked", func() bool { return len(h.ra.maillist.Marked()) == 2 })

	h.key('d')
	h.eventually("delete dialog", func() bool {
		return h.ra.uiApp.Pages.HasPage("delete-modal") && strings.Contains(h.ra.testDeleteModalText, "2 messages")
	})
	h.onUI(func() { h.ra.testDeleteModalDone(0, "Delete Forever") })
	h.eventually("two removed on screen", func() bool {
		return h.ra.maillist.VisibleCount() == 1 && len(h.ra.maillist.Marked()) == 0 &&
			strings.Contains(h.ra.uiApp.StatusBar.FlashText(), "Permanently deleted 2 messages")
	})
	if n := len(h.serverFolder("Deleted Items")); n != 1 {
		t.Errorf("server Deleted Items has %d messages, want 1", n)
	}
}

// End to end: marked INBOX messages move with one m.
func TestFlow_markedMove(t *testing.T) {
	h := newFlowHarness(t, "Archive")
	ctx := context.Background()
	for _, subj := range []string{"one", "two", "three"} {
		msg := "From: a@example.com\r\nTo: b@example.com\r\nSubject: " + subj + "\r\n\r\nbody\r\n"
		if _, err := h.server.Append(ctx, "INBOX", []byte(msg), nil); err != nil {
			t.Fatal(err)
		}
	}
	h.eventually("INBOX listed", func() bool {
		return h.ra.currentFolder != nil && h.ra.currentFolder.Name == "INBOX" && h.ra.maillist.VisibleCount() == 3
	})
	h.onUI(func() {
		h.ra.uiApp.SetFocus(h.ra.maillist)
		h.ra.maillist.Select(0, 0)
	})
	h.key(' ')
	h.key(' ')
	h.eventually("two marked", func() bool { return len(h.ra.maillist.Marked()) == 2 })
	h.key('m')
	h.eventually("move picker", func() bool { return h.ra.uiApp.Pages.HasPage(pageMovePicker) })
	var items []string
	h.onUI(func() { items = h.ra.movePicker.Items() })
	archive := slices.Index(items, "Archive")
	if archive < 0 {
		t.Fatalf("picker items = %v, want Archive among them", items)
	}
	for range archive {
		h.screen.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	}
	h.screen.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)

	h.eventually("moved", func() bool {
		return h.ra.maillist.VisibleCount() == 1 &&
			strings.Contains(h.ra.uiApp.StatusBar.FlashText(), "Moved 2 messages to Archive")
	})
	if n := len(h.serverFolder("Archive")); n != 2 {
		t.Errorf("Archive has %d messages, want 2", n)
	}
	if n := len(h.serverFolder("INBOX")); n != 1 {
		t.Errorf("INBOX has %d messages, want 1", n)
	}
}
