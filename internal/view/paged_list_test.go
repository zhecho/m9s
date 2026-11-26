package view

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/mail"
)

func pagedEnvs(from, to int) []*mail.Envelope {
	var out []*mail.Envelope
	for i := from; i <= to; i++ {
		out = append(out, &mail.Envelope{ID: fmt.Sprint(i), SortKey: int64(i), Subject: fmt.Sprint("msg ", i),
			Date: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute), Flags: []string{`\Seen`}})
	}
	return out
}

func listedView(t *testing.T) *MailListView {
	t.Helper()
	mlv := NewMailListView(testSkin(), "Jan 02 15:04", nil)
	mlv.SetEnvelopes(pagedEnvs(2382, 2431)) // the newest 50
	mlv.SetListed(true)
	mlv.SetTotals(2431, 131)
	return mlv
}

// The title says how much of the folder is loaded, the unread count and the
// selection.
func TestPagedTitle_unfiltered(t *testing.T) {
	mlv := listedView(t)
	if got := mlv.TitleText(); got != "MAIL LIST [50 of 2,431 messages · 131 unread]" {
		t.Errorf("title = %q", got)
	}
	mlv.SetConversations(true)
	mlv.ToggleMark(0)
	if got := mlv.TitleText(); got != "MAIL LIST [50 of 2,431 conversations · 131 unread · 1 selected]" {
		t.Errorf("title = %q", got)
	}
	mlv.SetTotals(50, 0) // all loaded, none unread
	if got := mlv.TitleText(); got != "MAIL LIST [50 conversations · 1 selected]" {
		t.Errorf("title = %q", got)
	}
}

func TestPagedTitle_filtered(t *testing.T) {
	mlv := listedView(t)
	mlv.SetFilter("msg")
	mlv.SetTotals(412, 131)
	if got := mlv.TitleText(); got != "MAIL LIST: filter /msg [50 of 412]" {
		t.Errorf("title = %q", got)
	}
	mlv.ReplaceEnvelopes(nil, "")
	mlv.SetTotals(0, 131)
	if got := mlv.TitleText(); got != "MAIL LIST: filter /msg [0]" {
		t.Errorf("no-match title = %q", got)
	}
}

// Rows from a Lister are what the folder filter matched: a conversation
// that matched through an older message stays, though its row does not
// contain the text.
func TestPagedList_noLocalRefilter(t *testing.T) {
	mlv := listedView(t)
	mlv.SetFilter("quote")
	mlv.SetRowsFor("quote") // as showListed does: these rows answer "quote"
	mlv.ReplaceEnvelopes([]*mail.Envelope{{ID: "c1", ThreadID: "T1", Subject: "Re: thanks", SortKey: 9}}, "")
	if len(mlv.visible) != 1 {
		t.Errorf("visible = %d, want the matched conversation shown", len(mlv.visible))
	}
}

// More rows go below, keeping the cursor and the marks.
func TestPagedList_append(t *testing.T) {
	mlv := listedView(t)
	mlv.Select(49, 0)
	mlv.ToggleMark(3)
	mlv.AppendEnvelopes(pagedEnvs(2332, 2381))
	if mlv.LoadedCount() != 100 || len(mlv.visible) != 100 {
		t.Fatalf("loaded %d, visible %d, want 100", mlv.LoadedCount(), len(mlv.visible))
	}
	if row, _ := mlv.GetSelection(); row != 49 {
		t.Errorf("cursor on row %d, want it to stay on 49", row)
	}
	if mlv.visible[50].ID != "2381" || !mlv.marked[mlv.visible[3].ID] {
		t.Error("new rows not below, or a mark lost")
	}
	mlv.AppendEnvelopes(pagedEnvs(2381, 2381)) // already loaded
	if mlv.LoadedCount() != 100 {
		t.Errorf("a row loaded twice: %d", mlv.LoadedCount())
	}
}

// MarkAll selects rows that are not loaded; they count and are acted on,
// and they are dropped when the filter changes.
func TestPagedList_markAllIncludesUnloaded(t *testing.T) {
	mlv := listedView(t)
	mlv.SetFilter("msg")
	all := pagedEnvs(2001, 2431)
	mlv.MarkAll(all)
	if n := len(mlv.Marked()); n != len(all) {
		t.Fatalf("marked %d, want %d", n, len(all))
	}
	if ids := markedIDs(mlv); ids[0] != "2431" || !slices.Contains(ids, "2001") {
		t.Errorf("marked = %v…, want the loaded ones first, then the rest", ids[:3])
	}
	mlv.AppendEnvelopes(pagedEnvs(2332, 2381)) // a loaded one stays marked once
	if n := len(mlv.Marked()); n != len(all) {
		t.Errorf("after loading more: %d marked, want %d", n, len(all))
	}
	mlv.ReplaceEnvelopes(pagedEnvs(2382, 2431), "") // a refresh keeps them
	if n := len(mlv.Marked()); n != len(all) {
		t.Errorf("after a refresh: %d marked, want %d", n, len(all))
	}
	mlv.SetFilter("msg 24") // narrowed at once to the loaded rows that match
	if n, shown := len(mlv.Marked()), len(mlv.visible); n != shown || shown == 50 {
		t.Errorf("after the filter changed: %d marked, want only the %d shown", n, shown)
	}
	mlv.Unmark("2431")
	mlv.ClearMarks()
	if n := len(mlv.Marked()); n != 0 {
		t.Errorf("after ClearMarks: %d marked", n)
	}
}

// Reaching the last row asks for more.
func TestPagedList_nearEnd(t *testing.T) {
	mlv := listedView(t)
	asked := 0
	mlv.SetNearEndFunc(func() { asked++ })
	mlv.Select(10, 0)
	if asked != 0 {
		t.Fatal("asked for more far from the end")
	}
	mlv.Select(49, 0)
	if asked != 1 {
		t.Errorf("asked %d times on the last row, want 1", asked)
	}
}

func TestThousands(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 2431: "2,431", 1234567: "1,234,567"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %q, want %q", n, got, want)
		}
	}
}

// A page whose rows sort above the cursor (by date) does not move the
// cursor to another message.
func TestPagedList_appendKeepsTheSelectedMessage(t *testing.T) {
	mlv := listedView(t)
	mlv.Select(49, 0)
	at := mlv.visible[49].ID
	newer := pagedEnvs(3000, 3000) // dated after everything loaded
	mlv.AppendEnvelopes(newer)
	if row, _ := mlv.GetSelection(); mlv.visible[row].ID != at {
		t.Errorf("cursor on %s after loading more, want it to stay on %s", mlv.visible[row].ID, at)
	}
}
