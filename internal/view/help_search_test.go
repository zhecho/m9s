package view

import (
	"strings"
	"testing"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"

	"github.com/zhecho/m9s/internal/ui"
)

const searchReadme = "# m9s\n\n## Animals\n\nZebra mail is fast.\n\nSome more text here.\n\nAnd zebra again, at the end.\n\n" +
	"```text\n" + "                                                                                                          far zebra\n" + "```\n"

func searchHelp(t *testing.T) (*HelpView, *bool) {
	t.Helper()
	skin, _ := ui.LoadSkin("")
	km, _ := ui.LoadKeyMap("")
	closed := false
	hv := NewHelpView(skin, func() { closed = true })
	hv.SetKeys(km)
	hv.SetReadme(searchReadme)
	hv.render(60)
	return hv, &closed
}

func helpRune(hv *HelpView, r rune) {
	hv.body.InputHandler()(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone), func(tview.Primitive) {})
}

func helpKey(hv *HelpView, k tcell.Key) {
	hv.body.InputHandler()(tcell.NewEventKey(k, 0, tcell.ModNone), func(tview.Primitive) {})
}

func helpType(hv *HelpView, s string) {
	for _, r := range s {
		helpRune(hv, r)
	}
}

// lineOf is the index of the first plain line containing text.
func lineOf(hv *HelpView, text string, from int) int {
	for i, l := range hv.plain {
		if i >= from && strings.Contains(l, text) {
			return i
		}
	}
	return -1
}

func TestHelpSearch_typingFindsAndHighlights(t *testing.T) {
	hv, _ := searchHelp(t)
	helpRune(hv, '/')
	if hv.SearchStatus() != "/" {
		t.Fatalf("status after / = %q", hv.SearchStatus())
	}
	helpType(hv, "zebra")
	if s := hv.SearchStatus(); !strings.HasPrefix(s, "/zebra") || !strings.Contains(s, "1/3") {
		t.Fatalf("status = %q, want /zebra and 1/3", s)
	}
	body := hv.body.GetText(false)
	if !strings.Contains(body, hv.currentMatchTag()+"Zebra") {
		t.Errorf("first match not marked as current:\n%s", body)
	}
	if !strings.Contains(body, "[::r]zebra") {
		t.Errorf("other matches not highlighted:\n%s", body)
	}
	first := lineOf(hv, "Zebra mail", 0)
	if row, _ := hv.body.GetScrollOffset(); row > first || first-row > 5 {
		t.Errorf("scrolled to row %d, match on line %d", row, first)
	}
}

func TestHelpSearch_nextPreviousAndWrap(t *testing.T) {
	hv, _ := searchHelp(t)
	helpRune(hv, '/')
	helpType(hv, "zebra")
	helpKey(hv, tcell.KeyEnter)
	if s := hv.SearchStatus(); !strings.Contains(s, "1/3") {
		t.Fatalf("after enter: %q", s)
	}
	helpRune(hv, 'n')
	if s := hv.SearchStatus(); !strings.Contains(s, "2/3") {
		t.Fatalf("after n: %q", s)
	}
	second := lineOf(hv, "And zebra", 0)
	if row, _ := hv.body.GetScrollOffset(); row > second {
		t.Errorf("row %d past the match on line %d", row, second)
	}
	helpRune(hv, 'n')
	// The third match is far right in a code line: the view scrolls sideways.
	if _, col := hv.body.GetScrollOffset(); col == 0 {
		t.Errorf("not scrolled sideways to the match in the wide line")
	}
	helpRune(hv, 'n')
	if s := hv.SearchStatus(); !strings.Contains(s, "1/3") {
		t.Fatalf("n does not wrap around: %q", s)
	}
	helpRune(hv, 'N')
	if s := hv.SearchStatus(); !strings.Contains(s, "3/3") {
		t.Fatalf("N does not wrap back: %q", s)
	}
}

func TestHelpSearch_smartCase(t *testing.T) {
	hv, _ := searchHelp(t)
	helpRune(hv, '/')
	helpType(hv, "Zebra")
	if s := hv.SearchStatus(); !strings.Contains(s, "1/1") {
		t.Fatalf("a capital letter must match case: %q", s)
	}
}

func TestHelpSearch_backspaceAndNoMatch(t *testing.T) {
	hv, _ := searchHelp(t)
	helpRune(hv, '/')
	helpType(hv, "zebrax")
	if s := hv.SearchStatus(); !strings.Contains(s, "no match") {
		t.Fatalf("status = %q, want no match", s)
	}
	helpKey(hv, tcell.KeyBackspace2)
	if s := hv.SearchStatus(); !strings.HasPrefix(s, "/zebra ") || !strings.Contains(s, "1/3") {
		t.Fatalf("after backspace: %q", s)
	}
}

func TestHelpSearch_qTypesWhileSearching(t *testing.T) {
	hv, closed := searchHelp(t)
	helpRune(hv, '/')
	helpRune(hv, 'q')
	if *closed || !strings.HasPrefix(hv.SearchStatus(), "/q") {
		t.Fatalf("q closed help instead of being typed (status %q)", hv.SearchStatus())
	}
}

func TestHelpSearch_escCancelsThenCloses(t *testing.T) {
	hv, closed := searchHelp(t)
	helpRune(hv, 'j')
	helpRune(hv, 'j')
	start, _ := hv.body.GetScrollOffset()

	// Esc while typing: the search is cancelled and the view goes back.
	helpRune(hv, '/')
	helpType(hv, "zebra")
	helpKey(hv, tcell.KeyEscape)
	if hv.SearchStatus() != "" || strings.Contains(hv.body.GetText(false), "[::r]") {
		t.Fatalf("not cancelled: status %q", hv.SearchStatus())
	}
	if row, _ := hv.body.GetScrollOffset(); row != start {
		t.Errorf("row %d after cancel, want %d", row, start)
	}
	if *closed {
		t.Fatal("Esc while typing closed help")
	}

	// After Enter, the first Esc clears the search, the second closes.
	helpRune(hv, '/')
	helpType(hv, "zebra")
	helpKey(hv, tcell.KeyEnter)
	helpKey(hv, tcell.KeyEscape)
	if hv.SearchStatus() != "" || *closed {
		t.Fatalf("first Esc: status %q, closed %v", hv.SearchStatus(), *closed)
	}
	helpKey(hv, tcell.KeyEscape)
	if !*closed {
		t.Fatal("second Esc did not close help")
	}
}

func TestHelpSearch_keptAcrossRelayout(t *testing.T) {
	hv, _ := searchHelp(t)
	helpRune(hv, '/')
	helpType(hv, "zebra")
	helpKey(hv, tcell.KeyEnter)
	helpRune(hv, 'n')
	hv.render(120)
	if s := hv.SearchStatus(); !strings.Contains(s, "2/3") {
		t.Fatalf("after relayout: %q", s)
	}
	if !strings.Contains(hv.body.GetText(false), "[::r]") {
		t.Error("highlights lost on relayout")
	}
}

func TestHelpSections_listHelpSearch(t *testing.T) {
	km, _ := ui.LoadKeyMap("")
	var help []string
	for _, s := range helpSections(km) {
		if s.Title == "HELP" {
			for _, e := range s.Entries {
				help = append(help, "<"+e.Key+"> "+e.Desc)
			}
		}
	}
	text := strings.Join(help, "\n")
	for _, want := range []string{"</> Search", "<n/N> Next / previous match", "<←/→> Scroll wide lines"} {
		if !strings.Contains(text, want) {
			t.Errorf("HELP section lacks %q:\n%s", want, text)
		}
	}
}

// In the running app: ? then /compose shows the search line and jumps to
// the shortcut.
func TestHelpSearch_inTheApp(t *testing.T) {
	ra := commandApp(t)
	ra.showHelp()
	for _, r := range "/compose" {
		ra.help.body.InputHandler()(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone), func(tview.Primitive) {})
	}
	screen := strings.Join(drawMainPage(t, ra, 120, 40), "\n")
	if !strings.Contains(screen, "/compose  1/") {
		t.Errorf("search line not drawn:\n%s", screen)
	}
	if !strings.Contains(screen, "Compose") {
		t.Errorf("the match is not on screen:\n%s", screen)
	}
}
