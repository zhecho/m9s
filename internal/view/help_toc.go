package view

import (
	"fmt"
	"strings"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
)

// helpTocEntry is one row of the help window's contents pane: a title, its
// heading level (0 for Shortcuts and README) and the body line it starts on.
type helpTocEntry struct {
	title       string
	level, line int
}

// Width limits of the contents pane, and the narrowest help window that
// still shows it.
const (
	helpTocMin    = 12
	helpTocMax    = 28
	helpTocNeedsW = 70
	helpTocGap    = 2 // the │ separator and a blank column before the text
)

// tocIndent is how far an entry is drawn in: README's sections under it.
func tocIndent(level int) string {
	switch {
	case level >= 3:
		return "    "
	case level == 2:
		return "  "
	}
	return ""
}

// helpTocWidth is the pane width that fits every entry, clamped.
func helpTocWidth(entries []helpTocEntry) int {
	w := 0
	for _, e := range entries {
		w = max(w, runewidth.StringWidth(tocIndent(e.level)+e.title)+1)
	}
	return min(max(w, helpTocMin), helpTocMax)
}

// dropContentsSection removes the README's "## Contents" section: in the
// help window the contents pane takes its place, and its links do not
// click in a terminal.
func dropContentsSection(md string) string {
	lines := strings.Split(md, "\n")
	var out []string
	skipping := false
	for _, l := range lines {
		if m := mdHeading.FindStringSubmatch(l); m != nil && len(m[1]) <= 2 {
			skipping = len(m[1]) == 2 && strings.EqualFold(strings.TrimSpace(m[2]), "contents")
		}
		if !skipping {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// newHelpToc builds the contents list in the skin's colours.
func (hv *HelpView) newHelpToc() {
	hv.toc = tview.NewList()
	hv.toc.ShowSecondaryText(false)
	hv.toc.SetHighlightFullLine(true)
	hv.toc.SetWrapAround(false)
	hv.toc.SetBackgroundColor(hv.skin.Color("background"))
	hv.toc.SetMainTextColor(hv.skin.Color("foreground"))
	hv.toc.SetSelectedBackgroundColor(hv.skin.Color("selected_bg"))
	hv.toc.SetSelectedTextColor(hv.skin.Color("selected_fg"))
	hv.toc.SetChangedFunc(func(i int, _, _ string, _ rune) {
		if !hv.syncing {
			hv.jumpTo(i)
		}
	})
	hv.toc.SetInputCapture(hv.tocKey)
	border := hv.skin.Color("border")
	bg := hv.skin.Color("background")
	hv.tocSep = tview.NewBox()
	hv.tocSep.SetBackgroundColor(bg)
	hv.tocSep.SetDrawFunc(func(screen tcell.Screen, x, y, w, h int) (int, int, int, int) {
		st := tcell.StyleDefault.Foreground(border).Background(bg)
		for r := y; r < y+h; r++ {
			screen.SetContent(x, r, '│', nil, st)
		}
		return x, y, w, h
	})
}

// setEntries replaces the contents entries; the list is rebuilt only when
// the titles change, so the selection survives a re-layout.
func (hv *HelpView) setEntries(entries []helpTocEntry) {
	same := len(entries) == len(hv.entries)
	for i := 0; same && i < len(entries); i++ {
		same = entries[i].title == hv.entries[i].title && entries[i].level == hv.entries[i].level
	}
	hv.entries = entries
	if same && hv.toc.GetItemCount() == len(entries) {
		return
	}
	cur := hv.toc.GetCurrentItem()
	w := helpTocWidth(entries)
	hv.syncing = true
	hv.toc.Clear()
	for _, e := range entries {
		hv.toc.AddItem(tview.Escape(runewidth.Truncate(tocIndent(e.level)+e.title, w-1, "…")), "", 0, nil)
	}
	hv.toc.SetCurrentItem(min(cur, len(entries)-1))
	hv.syncing = false
}

// jumpTo scrolls the body so entry i's line is at the top.
func (hv *HelpView) jumpTo(i int) {
	if i >= 0 && i < len(hv.entries) {
		hv.body.ScrollTo(hv.entries[i].line, 0)
		hv.textLeads, hv.matchLeads = false, false
	}
}

// tocKey is the contents pane's keys: j/k/g/G move (and so jump the text);
// everything else is the help window's, so a search typed here keeps every
// letter and q or esc closes help.
func (hv *HelpView) tocKey(event *tcell.EventKey) *tcell.EventKey {
	if !hv.typing && event.Key() == tcell.KeyRune {
		cur, last := hv.toc.GetCurrentItem(), hv.toc.GetItemCount()-1
		switch event.Rune() {
		case 'j':
			hv.toc.SetCurrentItem(min(cur+1, last))
			return nil
		case 'k':
			hv.toc.SetCurrentItem(max(cur-1, 0))
			return nil
		case 'g':
			hv.toc.SetCurrentItem(0)
			return nil
		case 'G':
			hv.toc.SetCurrentItem(last)
			return nil
		}
	}
	return hv.handleKey(event)
}

// InputHandler moves between the panes: tab switches, enter on the
// contents goes to the text, and keys reach the text when the pane is
// hidden. Everything else goes to the pane that has focus.
func (hv *HelpView) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return hv.WrapInputHandler(func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
		if !hv.tocShown && hv.toc.HasFocus() {
			setFocus(hv.body)
		}
		if !hv.typing && hv.tocShown {
			switch {
			case event.Key() == tcell.KeyTab && hv.toc.HasFocus():
				setFocus(hv.body)
				return
			case event.Key() == tcell.KeyTab:
				setFocus(hv.toc)
				return
			case event.Key() == tcell.KeyEnter && hv.toc.HasFocus():
				setFocus(hv.body)
				return
			}
		}
		if handler := hv.Flex.InputHandler(); handler != nil {
			handler(event, setFocus)
		}
	})
}

// followText moves the contents highlight to the section the text's top
// line is in, and reports whether it moved. Only while the text has focus
// or a search moved it: otherwise the selection leads (the last headings
// cannot reach the top of the text).
func (hv *HelpView) followText() bool {
	if !hv.body.HasFocus() && !hv.textLeads {
		return false
	}
	row, _ := hv.body.GetScrollOffset()
	if hv.matchLeads && hv.current < len(hv.matches) {
		// The match, not the context above it — while it is on screen;
		// once the text is scrolled away from it, the top line again.
		_, _, _, h := hv.body.GetInnerRect()
		if m := hv.matches[hv.current].line; m >= row && m < row+h {
			row = m
		}
	}
	at := 0
	for i, e := range hv.entries {
		if e.line <= row {
			at = i
		}
	}
	if at == hv.toc.GetCurrentItem() {
		return false
	}
	hv.syncing = true
	hv.toc.SetCurrentItem(at)
	hv.syncing = false
	return true
}

// captionActive is the colour of the focused pane's caption: the yellow of
// a focused FOLDERS title.
var captionActive = tcell.NewRGBColor(255, 255, 0)

func (hv *HelpView) newCaption() *tview.TextView {
	c := tview.NewTextView()
	c.SetDynamicColors(true)
	c.SetWrap(false)
	c.SetBackgroundColor(hv.skin.Color("background"))
	return c
}

// setCaption shows text in yellow bold when its pane is active, else in
// the header colour.
func (hv *HelpView) setCaption(c *tview.TextView, text string, active bool) {
	tag := fmt.Sprintf("[%s::-]", hv.skin.ColorName("header_fg"))
	if active {
		tag = fmt.Sprintf("[#%06x::b]", captionActive.Hex())
	}
	c.SetText(tag + tview.Escape(text))
}

// sectionPath names entry i for the text's caption: SHORTCUTS, or
// README › <section> › <sub-section>.
func (hv *HelpView) sectionPath(i int) string {
	return strings.Join(hv.sectionParts(i), captionSep)
}

const captionSep = " › "

func (hv *HelpView) sectionParts(i int) []string {
	if i <= 0 || i >= len(hv.entries) {
		return []string{"SHORTCUTS"}
	}
	e := hv.entries[i]
	if e.level == 0 {
		return []string{e.title}
	}
	parts := []string{"README"}
	if e.level == 3 {
		for j := i - 1; j > 0; j-- {
			if hv.entries[j].level == 2 {
				parts = append(parts, hv.entries[j].title)
				break
			}
		}
	}
	return append(parts, e.title)
}

// fitPath joins parts into width columns. Too wide, the section in the
// middle is cut first: the sub-section is what tells the account types'
// "Config" or "Credentials" apart.
func fitPath(parts []string, width int) string {
	path := strings.Join(parts, captionSep)
	if width <= 0 || runewidth.StringWidth(path) <= width {
		return path
	}
	if len(parts) == 3 {
		rest := runewidth.StringWidth(parts[0] + captionSep + captionSep + parts[2])
		if avail := width - rest; avail >= 2 {
			return parts[0] + captionSep + runewidth.Truncate(parts[1], avail, "…") + captionSep + parts[2]
		}
	}
	return runewidth.Truncate(path, width, "…")
}

// MouseHandler lets the wheel over the text lead the contents highlight,
// whichever pane has focus; then the panes handle the event as usual.
func (hv *HelpView) MouseHandler() func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (bool, tview.Primitive) {
	return hv.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (bool, tview.Primitive) {
		if (action == tview.MouseScrollUp || action == tview.MouseScrollDown) && hv.body.InRect(event.Position()) {
			hv.textLeads, hv.matchLeads = true, false
		}
		return hv.Flex.MouseHandler()(action, event, setFocus)
	})
}
