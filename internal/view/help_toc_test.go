package view

import (
	"strings"
	"testing"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
	"github.com/zhecho/m9s/internal/ui"
)

const tocReadme = "# m9s\n\nIntro.\n\n## Contents\n\n- [Animals](#animals)\n\n## Animals\n\nx\n\n### Zebras\n\ny\n\n#### Deep\n\nz\n"

func tocHelp(t *testing.T, readme string) *HelpView {
	t.Helper()
	skin, _ := ui.LoadSkin("")
	km, _ := ui.LoadKeyMap("")
	hv := NewHelpView(skin, func() {})
	if readme != "" {
		hv.SetReadme(readme)
	}
	hv.SetKeys(km)
	hv.render(60)
	return hv
}

func tocTitles(hv *HelpView) []string {
	var out []string
	for _, e := range hv.entries {
		out = append(out, e.title)
	}
	return out
}

func TestHelpToc_entriesFromReadme(t *testing.T) {
	hv := tocHelp(t, tocReadme)
	if got, want := strings.Join(tocTitles(hv), ","), "Shortcuts,README,Animals,Zebras"; got != want {
		t.Fatalf("entries = %s, want %s", got, want)
	}
	if hv.entries[0].line != 0 {
		t.Errorf("Shortcuts at line %d, want 0", hv.entries[0].line)
	}
	if l := hv.plain[hv.entries[1].line]; l != "README" {
		t.Errorf("README entry points at %q", l)
	}
	if l := hv.plain[hv.entries[2].line]; !strings.Contains(l, "Animals") {
		t.Errorf("Animals entry points at %q", l)
	}
	for _, l := range hv.plain {
		if l == "Contents" {
			t.Error("the README's Contents section is shown in help")
		}
	}
	if hv.toc.GetItemCount() != 4 {
		t.Errorf("list has %d items, want 4", hv.toc.GetItemCount())
	}
}

func TestHelpToc_noReadme(t *testing.T) {
	hv := tocHelp(t, "")
	if got := strings.Join(tocTitles(hv), ","); got != "Shortcuts" {
		t.Errorf("entries = %s, want Shortcuts", got)
	}
	if hv.toc.GetItemCount() != 1 {
		t.Errorf("list has %d items, want 1", hv.toc.GetItemCount())
	}
}

func TestHelpToc_relayoutKeepsSelection(t *testing.T) {
	hv := tocHelp(t, tocReadme)
	hv.toc.SetCurrentItem(3)
	hv.render(40)
	hv.render(100)
	if c := hv.toc.GetCurrentItem(); c != 3 {
		t.Fatalf("current item = %d after re-layout, want 3", c)
	}
	if l := hv.plain[hv.entries[3].line]; !strings.Contains(l, "Zebras") {
		t.Errorf("Zebras entry points at %q after re-layout", l)
	}
}

func TestHelpToc_widthClamped(t *testing.T) {
	if w := helpTocWidth([]helpTocEntry{{title: "A"}}); w != 12 {
		t.Errorf("short width = %d, want 12", w)
	}
	long := []helpTocEntry{{title: strings.Repeat("x", 60), level: 2}}
	if w := helpTocWidth(long); w != 28 {
		t.Errorf("long width = %d, want 28", w)
	}
	hv := tocHelp(t, "# m9s\n\n## "+strings.Repeat("Long", 15)+"\n\nx\n")
	if main, _ := hv.toc.GetItemText(2); !strings.HasSuffix(main, "…") {
		t.Errorf("long title not cut: %q", main)
	}
}

func TestHelpView_drawsContentsPane(t *testing.T) {
	ra := commandApp(t)
	ra.showHelp()
	lines := drawMainPage(t, ra, 120, 40)
	found := false
	for _, l := range lines {
		if i := strings.Index(l, "Shortcuts"); i >= 0 {
			rest := l[i:]
			found = strings.Contains(rest, "│") && strings.Contains(rest, "GENERAL")
			break
		}
	}
	if !found {
		t.Errorf("no row with the contents pane beside the shortcuts:\n%s", strings.Join(lines, "\n"))
	}
	if !ra.help.tocShown {
		t.Error("contents pane not shown at width 120")
	}
}

func TestHelpView_narrowHidesContents(t *testing.T) {
	ra := commandApp(t)
	ra.showHelp()
	lines := drawMainPage(t, ra, 60, 40)
	if strings.Contains(strings.Join(lines, "\n"), "Shortcuts") {
		t.Errorf("contents pane drawn at width 60:\n%s", strings.Join(lines, "\n"))
	}
	if ra.help.tocShown {
		t.Error("tocShown at width 60")
	}
}

// tocRig is a help window drawn on a screen, with focus handled as
// tview's application does.
type tocRig struct {
	hv      *HelpView
	screen  tcell.SimulationScreen
	focused tview.Primitive
	closed  bool
}

func (r *tocRig) setFocus(p tview.Primitive) {
	if r.focused != nil {
		r.focused.Blur()
	}
	r.focused = p
	p.Focus(r.setFocus)
}

func (r *tocRig) draw() { r.hv.Draw(r.screen); r.screen.Show() }

func (r *tocRig) key(k tcell.Key, ch rune) {
	r.hv.InputHandler()(tcell.NewEventKey(k, ch, tcell.ModNone), r.setFocus)
}

func (r *tocRig) runes(s string) {
	for _, ch := range s {
		r.key(tcell.KeyRune, ch)
	}
}

func (r *tocRig) top() int { row, _ := r.hv.body.GetScrollOffset(); return row }

// longReadme has sections long enough that each heading can reach the top.
func longReadme() string {
	filler := strings.Repeat("Some zebra text.\n\n", 30)
	return "# m9s\n\n" + filler + "## Animals\n\n" + filler + "### Zebras\n\n" + filler + "## Plants\n\n" + filler
}

func newTocRig(t *testing.T, w, h int) *tocRig {
	t.Helper()
	return newTocRigWith(t, w, h, longReadme())
}

func newTocRigWith(t *testing.T, w, h int, readme string) *tocRig {
	t.Helper()
	skin, _ := ui.LoadSkin("")
	km, _ := ui.LoadKeyMap("")
	r := &tocRig{screen: tcell.NewSimulationScreen("UTF-8")}
	if err := r.screen.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.screen.Fini)
	r.screen.SetSize(w, h)
	r.hv = NewHelpView(skin, func() { r.closed = true })
	r.hv.SetReadme(readme)
	r.hv.SetKeys(km)
	r.hv.SetRect(0, 0, w, h)
	r.setFocus(r.hv)
	r.draw()
	return r
}

func TestHelpToc_jMovesAndJumps(t *testing.T) {
	r := newTocRig(t, 120, 30)
	r.runes("j")
	if c := r.hv.toc.GetCurrentItem(); c != 1 || r.top() != r.hv.entries[1].line {
		t.Fatalf("after j: item %d, top %d; want 1, %d", c, r.top(), r.hv.entries[1].line)
	}
	r.runes("j")
	if c := r.hv.toc.GetCurrentItem(); c != 2 || r.top() != r.hv.entries[2].line {
		t.Fatalf("after jj: item %d, top %d; want 2, %d", c, r.top(), r.hv.entries[2].line)
	}
	r.runes("k")
	if c := r.hv.toc.GetCurrentItem(); c != 1 {
		t.Errorf("after k: item %d, want 1", c)
	}
	r.runes("G")
	if c := r.hv.toc.GetCurrentItem(); c != len(r.hv.entries)-1 {
		t.Errorf("after G: item %d, want last", c)
	}
	r.runes("g")
	if c := r.hv.toc.GetCurrentItem(); c != 0 || r.top() != 0 {
		t.Errorf("after g: item %d, top %d; want 0, 0", c, r.top())
	}
}

func TestHelpToc_lastEntryDoesNotSnapBack(t *testing.T) {
	r := newTocRig(t, 120, 30)
	r.runes("G")
	r.draw()
	r.draw()
	if c := r.hv.toc.GetCurrentItem(); c != len(r.hv.entries)-1 {
		t.Errorf("after G and a draw: item %d, want the last", c)
	}
}

func TestHelpToc_tabAndEnterMoveFocus(t *testing.T) {
	r := newTocRig(t, 120, 30)
	if !r.hv.toc.HasFocus() {
		t.Fatal("help does not open on the contents pane")
	}
	r.key(tcell.KeyTab, 0)
	if !r.hv.body.HasFocus() {
		t.Fatal("tab did not move to the text")
	}
	r.key(tcell.KeyTab, 0)
	if !r.hv.toc.HasFocus() {
		t.Fatal("tab did not move back to the contents")
	}
	r.key(tcell.KeyEnter, 0)
	if !r.hv.body.HasFocus() {
		t.Error("enter on the contents did not move to the text")
	}
}

func TestHelpToc_followsTextWhenBodyFocused(t *testing.T) {
	r := newTocRig(t, 120, 30)
	r.key(tcell.KeyTab, 0)
	r.hv.body.ScrollTo(r.hv.entries[2].line+1, 0)
	r.draw()
	if c := r.hv.toc.GetCurrentItem(); c != 2 {
		t.Errorf("text scrolled into entry 2, highlight on %d", c)
	}
	if r.top() != r.hv.entries[2].line+1 {
		t.Errorf("following the text moved the text to %d", r.top())
	}
}

func TestHelpToc_searchFromContentsPane(t *testing.T) {
	r := newTocRig(t, 120, 30)
	r.runes("/zeb qj")
	if s := r.hv.SearchStatus(); !strings.HasPrefix(s, "/zeb qj") {
		t.Errorf("status = %q, want the typed letters", s)
	}
	if r.closed {
		t.Fatal("q typed in a search closed help")
	}
	if c := r.hv.toc.GetCurrentItem(); c != 0 {
		t.Errorf("j typed in a search moved the contents to %d", c)
	}
	r.key(tcell.KeyEscape, 0)
	if r.hv.SearchStatus() != "" {
		t.Errorf("esc left the search open: %q", r.hv.SearchStatus())
	}
	r.runes("q")
	if !r.closed {
		t.Error("q on the contents pane did not close help")
	}
}

func TestHelpToc_clickSelects(t *testing.T) {
	r := newTocRig(t, 120, 30)
	x, y, _, _ := r.hv.toc.GetInnerRect()
	ev := tcell.NewEventMouse(x+1, y+2, tcell.ButtonPrimary, tcell.ModNone)
	r.hv.MouseHandler()(tview.MouseLeftClick, ev, r.setFocus)
	if c := r.hv.toc.GetCurrentItem(); c != 2 || r.top() != r.hv.entries[2].line {
		t.Errorf("click: item %d, top %d; want 2, %d", c, r.top(), r.hv.entries[2].line)
	}
}

func TestHelpToc_narrowSendsKeysToText(t *testing.T) {
	r := newTocRig(t, 60, 30)
	r.runes("j")
	if !r.hv.body.HasFocus() {
		t.Fatal("hidden contents pane kept the keys")
	}
	if r.top() != 1 {
		t.Errorf("j scrolled the text to %d, want 1", r.top())
	}
}

func TestHelpToc_resizeKeepsTheSection(t *testing.T) {
	r := newTocRig(t, 120, 30)
	r.runes("jj")
	r.hv.SetRect(0, 0, 100, 30)
	r.draw()
	if c := r.hv.toc.GetCurrentItem(); c != 2 || r.top() != r.hv.entries[2].line {
		t.Errorf("after resize: item %d, top %d; want 2, %d", c, r.top(), r.hv.entries[2].line)
	}
}

func TestHelpToc_reopenStartsAtTheTop(t *testing.T) {
	r := newTocRig(t, 120, 30)
	r.runes("jjj")
	km, _ := ui.LoadKeyMap("")
	r.hv.SetKeys(km) // what showHelp does on every open
	r.setFocus(r.hv)
	r.draw()
	if c := r.hv.toc.GetCurrentItem(); c != 0 || r.top() != 0 {
		t.Errorf("reopened on item %d, top %d; want 0, 0", c, r.top())
	}
}

func TestHelpToc_GInTextMovesHighlightAtOnce(t *testing.T) {
	r := newTocRig(t, 120, 30)
	r.key(tcell.KeyTab, 0)
	r.runes("G")
	r.draw()
	if c := r.hv.toc.GetCurrentItem(); c != len(r.hv.entries)-1 {
		t.Errorf("after G in the text and one draw: item %d, want the last", c)
	}
}

func TestHelpToc_searchMovesHighlight(t *testing.T) {
	r := newTocRig(t, 120, 30)
	r.runes("/Plants")
	r.key(tcell.KeyEnter, 0)
	r.draw()
	if c := r.hv.toc.GetCurrentItem(); c != len(r.hv.entries)-1 {
		t.Fatalf("search went to Plants, highlight on %d", c)
	}
	r.runes("k")
	if c := r.hv.toc.GetCurrentItem(); c != len(r.hv.entries)-2 {
		t.Errorf("k after the search: item %d, want the section before Plants", c)
	}
}

// cellAt finds text on the rig's screen and returns its first cell's style.
func (r *tocRig) find(text string) (x, y int, st tcell.Style, ok bool) {
	cells, w, h := r.screen.GetContents()
	for y := 0; y < h; y++ {
		var b strings.Builder
		for x := 0; x < w; x++ {
			if rs := cells[y*w+x].Runes; len(rs) > 0 {
				b.WriteRune(rs[0])
			} else {
				b.WriteRune(' ')
			}
		}
		if i := strings.Index(b.String(), text); i >= 0 {
			col := len([]rune(b.String()[:i]))
			_, _, st, _ := r.screen.GetContent(col, y)
			return col, y, st, true
		}
	}
	return 0, 0, tcell.StyleDefault, false
}

func fgBold(st tcell.Style) (tcell.Color, bool) {
	fg, _, a := st.Decompose()
	return fg, a&tcell.AttrBold != 0
}

var captionYellow = tcell.NewRGBColor(255, 255, 0)

func TestHelpToc_captionsShowActivePane(t *testing.T) {
	r := newTocRig(t, 120, 30)
	_, y1, st, ok := r.find("CONTENTS")
	if !ok {
		t.Fatal("no CONTENTS caption")
	}
	if fg, bold := fgBold(st); fg != captionYellow || !bold {
		t.Errorf("active CONTENTS caption = %v bold %v, want yellow bold", fg, bold)
	}
	_, y2, st2, ok := r.find("SHORTCUTS")
	if !ok || y2 != y1 {
		t.Fatalf("no SHORTCUTS caption on the caption row (found %v, row %d vs %d)", ok, y2, y1)
	}
	if fg, _ := fgBold(st2); fg == captionYellow {
		t.Error("inactive text caption is yellow")
	}
	r.key(tcell.KeyTab, 0)
	r.draw()
	_, _, st, _ = r.find("CONTENTS")
	if fg, _ := fgBold(st); fg != r.hv.skin.Color("header_fg") {
		t.Errorf("inactive CONTENTS caption = %v, want header_fg", fg)
	}
	_, _, st2, _ = r.find("SHORTCUTS")
	if fg, bold := fgBold(st2); fg != captionYellow || !bold {
		t.Errorf("active text caption = %v bold %v, want yellow bold", fg, bold)
	}
}

func TestHelpToc_rightCaptionNamesSection(t *testing.T) {
	r := newTocRig(t, 120, 30)
	r.runes("jjj")
	r.draw()
	if got := strings.TrimSpace(r.hv.bodyCaption.GetText(true)); got != "README › Animals › Zebras" {
		t.Errorf("caption = %q", got)
	}
	r.runes("j")
	r.draw()
	if got := strings.TrimSpace(r.hv.bodyCaption.GetText(true)); got != "README › Plants" {
		t.Errorf("caption = %q", got)
	}
}

// sectionAt is the entry whose section holds line.
func sectionAt(hv *HelpView, line int) int {
	at := 0
	for i, e := range hv.entries {
		if e.line <= line {
			at = i
		}
	}
	return at
}

func (r *tocRig) wheel(p tview.Primitive, n int) {
	x, y, _, _ := p.GetRect()
	for range n {
		ev := tcell.NewEventMouse(x+1, y+1, tcell.WheelDown, tcell.ModNone)
		r.hv.MouseHandler()(tview.MouseScrollDown, ev, r.setFocus)
	}
}

func TestHelpToc_wheelOverTextMovesHighlight(t *testing.T) {
	r := newTocRig(t, 120, 30)
	r.wheel(r.hv.body, 150)
	r.draw()
	if r.top() <= r.hv.entries[2].line {
		t.Fatalf("wheel did not scroll past Animals: top %d", r.top())
	}
	if c, want := r.hv.toc.GetCurrentItem(), sectionAt(r.hv, r.top()); c != want {
		t.Errorf("after the wheel: item %d, want %d", c, want)
	}
	if !r.hv.toc.HasFocus() {
		t.Error("the wheel moved focus")
	}
}

func TestHelpToc_wheelOverListDoesNotLead(t *testing.T) {
	r := newTocRig(t, 120, 30)
	r.wheel(r.hv.toc, 3)
	if r.hv.textLeads {
		t.Error("wheel over the contents made the text lead")
	}
}

func TestHelpToc_narrowToWideKeepsSection(t *testing.T) {
	r := newTocRig(t, 60, 30)
	for range 250 {
		r.runes("j")
	}
	r.draw()
	want := sectionAt(r.hv, r.top())
	if want < 2 {
		t.Fatalf("j did not scroll into a README section: top %d", r.top())
	}
	r.hv.SetRect(0, 0, 120, 30)
	r.draw()
	if c := r.hv.toc.GetCurrentItem(); c != want || r.top() != r.hv.entries[want].line {
		t.Errorf("after widening: item %d, top %d; want %d, %d", c, r.top(), want, r.hv.entries[want].line)
	}
}

func TestHelpToc_keysAfterSearchFollowTheTop(t *testing.T) {
	r := newTocRig(t, 120, 30)
	r.runes("/Zebras")
	r.key(tcell.KeyEnter, 0)
	r.draw()
	r.key(tcell.KeyTab, 0)
	for range 200 {
		r.runes("j")
	}
	r.draw()
	if c, want := r.hv.toc.GetCurrentItem(), sectionAt(r.hv, r.top()); c != want {
		t.Errorf("after a search and j in the text: item %d, want %d (top %d)", c, want, r.top())
	}
}

func TestHelpToc_longCaptionKeepsTheSubsection(t *testing.T) {
	filler := strings.Repeat("Some text.\n\n", 30)
	md := "# m9s\n\n" + filler + "## Microsoft 365 / Outlook.com (Graph API)\n\n" + filler + "### Setup (one time)\n\n" + filler
	r := newTocRigWith(t, 80, 30, md)
	r.runes("G")
	r.draw()
	got := strings.TrimSpace(r.hv.bodyCaption.GetText(true))
	if !strings.HasSuffix(got, "› Setup (one time)") || !strings.Contains(got, "…") {
		t.Errorf("caption = %q, want the ## part cut and the ### part kept", got)
	}
	if w := runewidth.StringWidth(got); w > r.hv.width {
		t.Errorf("caption is %d wide, the text %d", w, r.hv.width)
	}
}

func TestHelpToc_narrowCaption(t *testing.T) {
	r := newTocRig(t, 60, 30)
	if _, _, _, ok := r.find("CONTENTS"); ok {
		t.Error("CONTENTS caption drawn while the pane is hidden")
	}
	_, _, st, ok := r.find("SHORTCUTS")
	if !ok {
		t.Fatal("no text caption at width 60")
	}
	if fg, bold := fgBold(st); fg != captionYellow || !bold {
		t.Errorf("text caption at width 60 = %v bold %v, want yellow bold (keys go to the text)", fg, bold)
	}
}
