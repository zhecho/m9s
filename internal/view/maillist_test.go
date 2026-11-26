package view

import (
	"slices"
	"testing"
	"time"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/ui"
)

func testSkin() *ui.Skin {
	return &ui.Skin{Colors: map[string]string{}}
}

func testEnvelopes() []*mail.Envelope {
	return []*mail.Envelope{
		{Subject: "Hello World", From: []mail.Address{{Email: "a@example.com"}}, Date: time.Now()},
		{Subject: "Meeting Notes", From: []mail.Address{{Email: "b@example.com"}}, Date: time.Now()},
		{Subject: "Invoice #123", From: []mail.Address{{Email: "c@example.com"}}, Date: time.Now()},
	}
}

// selections records every envelope handed to onSelect.
type selections struct{ got []*mail.Envelope }

func (s *selections) record(e *mail.Envelope) { s.got = append(s.got, e) }
func (s *selections) last() *mail.Envelope {
	if len(s.got) == 0 {
		return nil
	}
	return s.got[len(s.got)-1]
}

// peopleEnvelopes are listed as the mail list shows them: newest (Alice) first.
func peopleEnvelopes() []*mail.Envelope {
	day := func(d int) time.Time { return time.Date(2026, 11, d, 9, 0, 0, 0, time.UTC) }
	return []*mail.Envelope{
		{ID: "1", UID: 1, Subject: "Lunch", From: []mail.Address{{Name: "Alice", Email: "alice@example.com"}}, Date: day(3)},
		{ID: "2", UID: 2, Subject: "Re: meeting", From: []mail.Address{{Name: "Bob", Email: "robert@corp.io"}}, Date: day(2)},
		{ID: "3", UID: 3, Subject: "Invoice", From: []mail.Address{{Name: "Carol", Email: "carol@example.com"}}, Date: day(1)},
	}
}

func TestMailList_filterSelectsVisibleEnvelope(t *testing.T) {
	var sel selections
	mlv := NewMailListView(testSkin(), "Jan 02 15:04", sel.record)
	envs := peopleEnvelopes()
	mlv.SetEnvelopes(envs)

	mlv.SetFilter("carol")

	if mlv.VisibleCount() != 1 || mlv.GetRowCount() != 1 {
		t.Fatalf("VisibleCount = %d, rows = %d; want 1 and 1", mlv.VisibleCount(), mlv.GetRowCount())
	}
	if sel.last() != envs[2] {
		t.Errorf("selected %+v, want Carol's message (the only visible row)", sel.last())
	}
}

func TestMailList_filterMatchesAddress(t *testing.T) {
	mlv := NewMailListView(testSkin(), "Jan 02 15:04", nil)
	mlv.SetEnvelopes(peopleEnvelopes())
	mlv.SetFilter("corp.io")
	if mlv.VisibleCount() != 1 {
		t.Errorf("VisibleCount = %d, want 1: the address must match even when a display name is set", mlv.VisibleCount())
	}
}

func TestMailList_filterCaseInsensitive(t *testing.T) {
	mlv := NewMailListView(testSkin(), "Jan 02 15:04", nil)
	mlv.SetEnvelopes(peopleEnvelopes())
	mlv.SetFilter("MEETING")
	if mlv.VisibleCount() != 1 {
		t.Errorf("VisibleCount = %d, want 1", mlv.VisibleCount())
	}
}

func TestMailList_filterNoMatchSelectsNil(t *testing.T) {
	var sel selections
	mlv := NewMailListView(testSkin(), "Jan 02 15:04", sel.record)
	mlv.SetEnvelopes(peopleEnvelopes())
	if sel.last() == nil {
		t.Fatal("precondition: loading a non-empty list selects its first message")
	}

	mlv.SetFilter("zzz")

	if mlv.VisibleCount() != 0 || mlv.GetRowCount() != 0 {
		t.Errorf("VisibleCount = %d, rows = %d; want 0 and 0", mlv.VisibleCount(), mlv.GetRowCount())
	}
	if sel.last() != nil {
		t.Errorf("with nothing visible the selection must be nil, got %+v", sel.last())
	}
}

func TestMailList_emptyFolderSelectsNil(t *testing.T) {
	var sel selections
	mlv := NewMailListView(testSkin(), "Jan 02 15:04", sel.record)
	mlv.SetEnvelopes(peopleEnvelopes())
	mlv.SetEnvelopes(nil)
	if sel.last() != nil {
		t.Errorf("an empty folder must clear the selection, got %+v", sel.last())
	}
}

func TestMailList_clearingFilterRestoresAll(t *testing.T) {
	mlv := NewMailListView(testSkin(), "Jan 02 15:04", nil)
	mlv.SetEnvelopes(peopleEnvelopes())
	mlv.SetFilter("carol")
	mlv.SetFilter("")
	if mlv.VisibleCount() != 3 || mlv.FilterQuery() != "" {
		t.Errorf("VisibleCount = %d, FilterQuery = %q; want 3 and empty", mlv.VisibleCount(), mlv.FilterQuery())
	}
}

func TestMailList_setEnvelopesClearsFilter(t *testing.T) {
	mlv := NewMailListView(testSkin(), "Jan 02 15:04", nil)
	mlv.SetEnvelopes(peopleEnvelopes())
	mlv.SetFilter("carol")
	mlv.SetEnvelopes(peopleEnvelopes())
	if mlv.FilterQuery() != "" || mlv.VisibleCount() != 3 {
		t.Errorf("FilterQuery = %q, VisibleCount = %d; a new list starts unfiltered", mlv.FilterQuery(), mlv.VisibleCount())
	}
}

func TestMailList_titleText(t *testing.T) {
	mlv := NewMailListView(testSkin(), "Jan 02 15:04", nil)
	if got := mlv.TitleText(); got != "MAIL LIST" {
		t.Errorf("TitleText() = %q, want MAIL LIST", got)
	}
	mlv.SetEnvelopes(peopleEnvelopes())
	mlv.SetFilter("bob")
	if got := mlv.TitleText(); got != "MAIL LIST: filter /bob [1]" {
		t.Errorf("TitleText() = %q, want MAIL LIST: filter /bob [1]", got)
	}
}

func TestMailListView_unreadEnvelope(t *testing.T) {
	// Unread envelopes trigger the unread color branch in renderEnvelopes.
	mlv := NewMailListView(testSkin(), "Jan 02 15:04", nil)
	envs := []*mail.Envelope{
		{Subject: "Unread", Flags: []string{}, From: []mail.Address{{Email: "a@example.com"}}, Date: time.Now()},
		{Subject: "Read", Flags: []string{`\Seen`}, From: []mail.Address{{Email: "b@example.com"}}, Date: time.Now()},
		{Subject: "Flagged", Flags: []string{`\Flagged`}, From: []mail.Address{{Email: "c@example.com"}}, Date: time.Now()},
	}
	mlv.SetEnvelopes(envs)
	if mlv.GetRowCount() != 3 {
		t.Errorf("want 3 rows, got %d", mlv.GetRowCount())
	}
}

func TestMailListView_emptyEnvelopes(t *testing.T) {
	// SetEnvelopes with empty slice hits the len==0 branch (no Select call).
	mlv := NewMailListView(testSkin(), "Jan 02 15:04", nil)
	mlv.SetEnvelopes([]*mail.Envelope{})
	if mlv.GetRowCount() != 0 {
		t.Errorf("want 0 rows for empty envelopes, got %d", mlv.GetRowCount())
	}
}

func TestMailListView_onSelect(t *testing.T) {
	var selected *mail.Envelope
	mlv := NewMailListView(testSkin(), "Jan 02 15:04", func(e *mail.Envelope) { selected = e })
	envs := []*mail.Envelope{
		{Subject: "First", Flags: []string{}, From: []mail.Address{{Email: "a@example.com"}}, Date: time.Now()},
	}
	mlv.SetEnvelopes(envs)
	// Simulate selection change.
	mlv.Select(0, 0)
	_ = selected // callback is fired by tview internally on selection change
}

func TestMailListView_focusBlur(t *testing.T) {
	mlv := NewMailListView(testSkin(), "Jan 02 15:04", nil)
	focused := 0
	blurred := 0
	mlv.SetFocusFunc(func() { focused++ })
	mlv.SetBlurFunc(func() { blurred++ })

	mlv.Focus(func(p tview.Primitive) {})
	if focused != 1 {
		t.Errorf("want focusFunc called once, got %d", focused)
	}
	mlv.Blur()
	if blurred != 1 {
		t.Errorf("want blurFunc called once, got %d", blurred)
	}
}

func TestMailListView_focusBlur_noCallbacks(t *testing.T) {
	mlv := NewMailListView(testSkin(), "Jan 02 15:04", nil)
	mlv.Focus(func(p tview.Primitive) {})
	mlv.Blur()
}

func datedEnvelopes() []*mail.Envelope {
	day := func(d int) time.Time { return time.Date(2026, 11, d, 9, 0, 0, 0, time.UTC) }
	return []*mail.Envelope{
		{ID: "1", UID: 1, Subject: "oldest", Date: day(1)},
		{ID: "2", UID: 2, Subject: "newest", Date: day(3)},
		{ID: "3", UID: 3, Subject: "middle", Date: day(2)},
	}
}

func rowSubjects(mlv *MailListView) []string {
	var out []string
	for row := 0; row < mlv.GetRowCount(); row++ {
		out = append(out, mlv.GetCell(row, 1).Text)
	}
	return out
}

func TestMailList_newestOnTop(t *testing.T) {
	mlv := NewMailListView(testSkin(), "Jan 02", nil)
	mlv.SetEnvelopes(datedEnvelopes())
	want := []string{"newest", "middle", "oldest"}
	if got := rowSubjects(mlv); !slices.Equal(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
}

func TestMailList_newestOnTopAfterReloadAndFilter(t *testing.T) {
	sel := &selections{}
	mlv := NewMailListView(testSkin(), "Jan 02", sel.record)
	mlv.ReplaceEnvelopes(datedEnvelopes(), "3")
	if got, want := rowSubjects(mlv), []string{"newest", "middle", "oldest"}; !slices.Equal(got, want) {
		t.Errorf("rows after reload = %v, want %v", got, want)
	}
	if sel.last() == nil || sel.last().ID != "3" {
		t.Errorf("selected = %+v, want ID 3 kept", sel.last())
	}
	mlv.SetFilter("e") // matches all three
	if got, want := rowSubjects(mlv), []string{"newest", "middle", "oldest"}; !slices.Equal(got, want) {
		t.Errorf("rows while filtering = %v, want %v", got, want)
	}
}

func TestMailList_gAndGJumpToTopAndBottom(t *testing.T) {
	sel := &selections{}
	mlv := NewMailListView(testSkin(), "Jan 02", sel.record)
	mlv.SetEnvelopes(datedEnvelopes())
	press := func(r rune) {
		mlv.InputHandler()(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone), func(tview.Primitive) {})
	}
	press('G')
	if sel.last().Subject != "oldest" {
		t.Errorf("G selected %q, want the bottom row", sel.last().Subject)
	}
	press('g')
	if sel.last().Subject != "newest" {
		t.Errorf("g selected %q, want the top row", sel.last().Subject)
	}
}

func TestMailList_showsAttachSign(t *testing.T) {
	mlv := NewMailListView(testSkin(), "Jan 02", nil)
	es := testEnvelopes()
	es[1].HasAttach = true
	mlv.SetEnvelopes(es)
	for row, env := range mlv.visible {
		sign := mlv.GetCell(row, 2).Text
		if want := map[bool]string{true: attachSign, false: ""}[env.HasAttach]; sign != want {
			t.Errorf("row %d (%s): sign %q, want %q", row, env.Subject, sign, want)
		}
		if date := mlv.GetCell(row, 3).Text; date != env.FormattedDate("Jan 02") {
			t.Errorf("row %d: column 3 = %q, want the date", row, date)
		}
	}
}
