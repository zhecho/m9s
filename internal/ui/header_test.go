package ui_test

import (
	"fmt"
	"strings"
	"testing"

	tcell "github.com/derailed/tcell/v2"
	"github.com/zhecho/m9s/internal/ui"
)

func newTestHeader() *ui.Header {
	skin, _ := ui.LoadSkin("")
	return ui.NewHeader(skin)
}

// infoRows is how many rows the info block has: Account, User, Folder,
// Unread, Status, Provider, RSRV. The header may be taller; the rest of its rows are blank
// on the info side.
const infoRows = 7

func infoLines(t *testing.T, h *ui.Header) []string {
	t.Helper()
	lines := strings.Split(h.InfoText(), "\n")
	if len(lines) != infoRows {
		t.Fatalf("InfoText has %d lines, want %d:\n%s", len(lines), infoRows, h.InfoText())
	}
	if infoRows > ui.HeaderHeight {
		t.Fatalf("the info block (%d rows) does not fit the header (%d rows)", infoRows, ui.HeaderHeight)
	}
	return lines
}

func TestHeader_infoRows(t *testing.T) {
	h := newTestHeader()
	h.SetInfo(ui.HeaderInfo{
		Account: "gmail", User: "me@x.io", Folder: "INBOX",
		Unseen: 3, Total: 128, HasCounts: true,
		Status: "● connected", StatusRole: "flash_ok",
		Provider: "gmail", Version: "v0.0.1",
		Server: "Gmail API / v1 (rev 20261106)",
	})
	lines := infoLines(t, h)
	want := [][]string{
		{"Account:", "gmail"},
		{"User:", "me@x.io"},
		{"Folder:", "INBOX"},
		{"Unread:", "3 of 128"},
		{"Status:", "● connected"},
		{"Provider:", "gmail / v0.0.1"},
		{"RSRV:", "Gmail API / v1 (rev 20261106)"},
	}
	for i, parts := range want {
		for _, p := range parts {
			if !strings.Contains(lines[i], p) {
				t.Errorf("info line %d = %q, want it to contain %q", i, lines[i], p)
			}
		}
	}
}

func TestHeader_noFolderNoCounts(t *testing.T) {
	h := newTestHeader()
	h.SetInfo(ui.HeaderInfo{Account: "gmail"})
	lines := infoLines(t, h)
	for _, i := range []int{2, 3} { // Folder, Unread
		if !strings.HasSuffix(strings.TrimRight(lines[i], " "), " -") {
			t.Errorf("info line %d = %q, want it to end with \"-\"", i, lines[i])
		}
	}
}

func TestHeader_zeroUnreadIsShownWhenCountsKnown(t *testing.T) {
	h := newTestHeader()
	h.SetInfo(ui.HeaderInfo{Account: "gmail", Folder: "Sent", HasCounts: true, Total: 7})
	if lines := infoLines(t, h); !strings.Contains(lines[3], "0 of 7") {
		t.Errorf("unread line = %q, want \"0 of 7\"", lines[3])
	}
}

func TestHeader_valuesWithBracketsSurvive(t *testing.T) {
	h := newTestHeader()
	h.SetInfo(ui.HeaderInfo{Account: "a", Folder: "[Gmail]/All Mail"})
	if lines := infoLines(t, h); !strings.Contains(lines[2], "[Gmail]/All Mail") {
		t.Errorf("folder line = %q; brackets must not be eaten as colour tags", lines[2])
	}
}

func makeHints(prefix string, n int) []ui.KeyHint {
	hints := make([]ui.KeyHint, n)
	for i := range hints {
		hints[i] = ui.KeyHint{Key: fmt.Sprintf("%s%d", prefix, i+1), Desc: fmt.Sprintf("%sDesc%d", prefix, i+1)}
	}
	return hints
}

func TestHeader_hintsColumns(t *testing.T) {
	h := newTestHeader()
	general := ui.HeaderHeight + 2 // one full column and two in the next
	h.SetHints(makeHints("g", general), makeHints("p", 2))
	text := h.HintsText()
	for _, hint := range append(makeHints("g", general), makeHints("p", 2)...) {
		want := "<" + hint.Key + "> " + hint.Desc
		if !strings.Contains(strings.Join(strings.Fields(text), " "), want) {
			t.Errorf("hints text lacks %q:\n%s", want, text)
		}
	}
	rows := strings.Split(text, "\n")
	if len(rows) != ui.HeaderHeight {
		t.Fatalf("HintsText has %d rows, want %d", len(rows), ui.HeaderHeight)
	}
	last := fmt.Sprintf("<g%d>", ui.HeaderHeight)   // bottom of the first column
	next := fmt.Sprintf("<g%d>", ui.HeaderHeight+1) // top of the second
	g1, gNext, p1 := strings.Index(rows[0], "<g1>"), strings.Index(rows[0], next), strings.Index(rows[0], "<p1>")
	if g1 < 0 || gNext < 0 || p1 < 0 {
		t.Fatalf("row 0 should hold g1, %s (second column) and p1 (pane column): %q", next, rows[0])
	}
	if !(g1 < gNext && gNext < p1) {
		t.Errorf("column order wrong in row 0: %q", rows[0])
	}
	bottom := rows[ui.HeaderHeight-1]
	if !strings.Contains(bottom, last) || strings.Contains(bottom, next) {
		t.Errorf("general hints fill %d rows per column; last row = %q", ui.HeaderHeight, bottom)
	}
}

func TestHeader_paneHintsAlwaysOwnColumn(t *testing.T) {
	h := newTestHeader()
	h.SetHints(makeHints("g", 2), makeHints("p", 1))
	rows := strings.Split(h.HintsText(), "\n")
	if !strings.Contains(rows[0], "<g1>") || !strings.Contains(rows[0], "<p1>") {
		t.Errorf("pane hint must start a new column on row 0 even when general has room: %q", rows[0])
	}
}

func TestHeader_skipsUnboundHints(t *testing.T) {
	h := newTestHeader()
	h.SetHints([]ui.KeyHint{{Key: "", Desc: "Ghost"}, {Key: "c", Desc: "Compose"}}, nil)
	text := h.HintsText()
	if strings.Contains(text, "Ghost") {
		t.Errorf("a hint with no key must not be shown:\n%s", text)
	}
	if !strings.Contains(text, "<c> Compose") {
		t.Errorf("bound hint missing:\n%s", text)
	}
}

func TestHeader_setHintsReplaces(t *testing.T) {
	h := newTestHeader()
	h.SetHints(makeHints("g", 3), makeHints("p", 2))
	h.SetHints(makeHints("g", 3), nil)
	if strings.Contains(h.HintsText(), "<p1>") {
		t.Error("pane hints from an earlier call must be gone")
	}
}

func screenText(screen tcell.SimulationScreen) string {
	cells, w, h := screen.GetContents()
	var b strings.Builder
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if r := cells[y*w+x].Runes; len(r) > 0 {
				b.WriteRune(r[0])
			} else {
				b.WriteRune(' ')
			}
		}
		b.WriteRune('\n')
	}
	return b.String()
}

func drawHeader(t *testing.T, h *ui.Header, w, hgt int) string {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(w, hgt)
	h.SetRect(0, 0, w, hgt)
	h.Draw(screen)
	screen.Show()
	return screenText(screen)
}

func fullHeader() *ui.Header {
	h := newTestHeader()
	h.SetInfo(ui.HeaderInfo{Account: "gmail", User: "me@x.io", Folder: "INBOX", HasCounts: true, Unseen: 3, Total: 128, Status: "● connected", StatusRole: "flash_ok"})
	h.SetHints(makeHints("g", 9), makeHints("p", 3))
	return h
}

func TestHeader_drawWide(t *testing.T) {
	out := drawHeader(t, fullHeader(), 160, ui.HeaderHeight)
	for _, want := range []string{"Account:", "gmail", "3 of 128", "<g1> gDesc1", "<p3> pDesc3", "|_|"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide header lacks %q:\n%s", want, out)
		}
	}
}

func TestHeader_drawNarrowKeepsInfo(t *testing.T) {
	out := drawHeader(t, fullHeader(), 40, ui.HeaderHeight)
	for _, want := range []string{"Account:", "me@x.io", "3 of 128", "● connected"} {
		if !strings.Contains(out, want) {
			t.Errorf("narrow header must keep the info block; lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "|_|") {
		t.Errorf("logo must be dropped when narrow:\n%s", out)
	}
}

func TestHeader_drawTinyScreen(t *testing.T) {
	for _, size := range [][2]int{{0, 0}, {20, 5}, {50, 3}, {1, 1}} {
		drawHeader(t, fullHeader(), size[0], size[1]) // must not panic
	}
}
