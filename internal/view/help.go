package view

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/ui"
)

const pageHelp = "help"

// helpEntry is one shortcut: the key and what it does.
type helpEntry struct{ Key, Desc string }

// helpSection is a titled group of shortcuts.
type helpSection struct {
	Title   string
	Entries []helpEntry
}

// helpGutter is the blank space between two columns of sections.
const helpGutter = 4

// helpSections lists every shortcut m9s has. Keys of rebindable actions come
// from km; one that is unbound is left out.
func helpSections(km ui.KeyMap) []helpSection {
	k := func(a ui.Action) string {
		if key := km.KeyFor(a); key != " " {
			return key
		}
		return "space"
	}
	cmd := k(ui.ActionCommandBar)
	if cmd == "" {
		cmd = ":"
	}
	sections := []helpSection{
		{"GENERAL", []helpEntry{
			{"j/k ↓/↑", "Navigate"},
			{"g/G", "Top/Bottom"},
			{"tab", "Next pane (log pane too)"},
			{k(ui.ActionSwitchAccountPrev), "Previous account"},
			{k(ui.ActionSwitchAccountNext), "Next account"},
			{k(ui.ActionCommandBar), "Command"},
			{k(ui.ActionSearchLocal), "Filter (local copy)"},
			{k(ui.ActionSearchServer), "Search the server"},
			{k(ui.ActionCompose), "Compose"},
			{k(ui.ActionSync), "Sync / reconnect"},
			{k(ui.ActionLog), "Logs"},
			{k(ui.ActionHelp), "Help"},
			{k(ui.ActionBack), "Quit"},
			{"ctrl-c", "Quit"},
		}},
		{"FOLDERS", []helpEntry{
			{"enter", "Open folder"},
			{k(ui.ActionEdit), "Edit draft"},
			{k(ui.ActionDelete), "Delete draft"},
		}},
		{"MESSAGES", []helpEntry{
			{"enter", "Open in reader"},
			{k(ui.ActionReply), "Reply"},
			{k(ui.ActionReplyAll), "Reply all"},
			{k(ui.ActionForward), "Forward"},
			{k(ui.ActionDelete), "Delete"},
			{k(ui.ActionMove), "Move to folder"},
			{k(ui.ActionMarkUnread), "Mark unread"},
			{k(ui.ActionEdit), "Edit draft"},
			{k(ui.ActionOpenBrowser), "Open in browser"},
			{k(ui.ActionSaveAttachments), "Save attachments"},
			{k(ui.ActionOpenAttachment), "Open attachment"},
			{k(ui.ActionStar), "Star"},
			{k(ui.ActionArchive), "Archive"},
			{k(ui.ActionLabels), "Labels (Gmail)"},
		}},
		{"SELECTION", []helpEntry{
			{k(ui.ActionMark), "Select / unselect"},
			{"shift-click", "Select / unselect"},
			{k(ui.ActionSelectAll), "Select all shown / none"},
			{k(ui.ActionMove) + " " + k(ui.ActionDelete), "Move / delete selected"},
			{k(ui.ActionArchive) + " " + k(ui.ActionMarkUnread) + " " + k(ui.ActionStar), "Archive / unread / star"},
			{"esc", "Clear selection"},
		}},
		{"DELETED ITEMS", []helpEntry{
			{k(ui.ActionDelete), "Delete forever / all"},
		}},
		{"READER", []helpEntry{
			{"tab", "Next message"},
			{"shift-tab", "Previous message"},
			{"enter", "Expand / collapse"},
			{"q/esc", "Close"},
		}},
		{"LABELS", []helpEntry{
			{"space", "Toggle label"},
			{"enter", "Apply"},
			{"esc", "Cancel"},
		}},
		{"FILTER", []helpEntry{
			{"type", "Filter the whole folder"},
			{"enter", "Keep filter"},
			{"esc", "Clear filter / search"},
		}},
		{"PROMPT", []helpEntry{
			{"enter", "Accept"},
			{"esc", "Cancel"},
			{"tab", "Complete"},
		}},
		{"PICKERS", []helpEntry{
			{"↓/↑", "Choose"},
			{"enter", "Move / save / open"},
			{"esc", "Cancel"},
		}},
		{"DIALOGS", []helpEntry{
			{"tab ←/→", "Choose button"},
			{"enter", "Confirm"},
			{"esc", "Cancel"},
		}},
		{"LOG VIEW", []helpEntry{
			{"j/k g/G", "Scroll"},
			{k(ui.ActionSearchLocal), "Filter"},
			{"esc", "Clear filter"},
			{logClose(k(ui.ActionLog)), "Close"},
		}},
		{"HELP", []helpEntry{
			{"tab", "Contents / text"},
			{"j/k", "Choose section"},
			{"j/k g/G", "Scroll"},
			{"←/→", "Scroll wide lines"},
			{"/", "Search"},
			{"n/N", "Next / previous match"},
			{"esc", "Clear search"},
			{"q/esc", "Close"},
		}},
		{"COMMANDS", []helpEntry{
			{cmd + "<account>", "Switch account"},
			{cmd + "<folder>", "Open folder"},
			{cmd + "help", "This help"},
			{cmd + configCommand, "Edit config"},
			{cmd + "q " + cmd + "quit", "Quit"},
		}},
	}
	for i := range sections {
		var bound []helpEntry
		for _, e := range sections[i].Entries {
			if e.Key != "" {
				bound = append(bound, e)
			}
		}
		sections[i].Entries = bound
	}
	return sections
}

// logClose names the keys that close the log view: q, esc and the log key.
func logClose(logKey string) string {
	if logKey == "" || logKey == "q" {
		return "q/esc"
	}
	return "q/esc/" + logKey
}

// helpCell is one line of a section as plain and as tview-tagged text.
type helpCell struct{ plain, tagged string }

// renderSection lays a section out as a title row, its entries with keys
// padded to one width, and a blank row.
func renderSection(s helpSection, skin *ui.Skin) []helpCell {
	keyW := 0
	for _, e := range s.Entries {
		keyW = max(keyW, utf8.RuneCountInString(e.Key)+2)
	}
	cells := []helpCell{{
		plain:  s.Title,
		tagged: fmt.Sprintf("[%s::b]%s[-:-:-]", skin.ColorName("accent"), s.Title),
	}}
	for _, e := range s.Entries {
		k := "<" + e.Key + ">"
		pad := strings.Repeat(" ", keyW-utf8.RuneCountInString(k))
		cells = append(cells, helpCell{
			plain: k + pad + "  " + e.Desc,
			tagged: fmt.Sprintf("[%s::b]%s[-:-:-]%s  [%s::]%s[-:-:-]",
				skin.ColorName("heading"), tview.Escape(k), pad, skin.ColorName("foreground"), tview.Escape(e.Desc)),
		})
	}
	return append(cells, helpCell{})
}

// layoutHelp flows the sections, in order, into as many columns as fit in
// width, balancing their heights. It returns the rows as plain and tagged
// text.
func layoutHelp(sections []helpSection, skin *ui.Skin, width int) (plain, tagged []string) {
	blocks := make([][]helpCell, len(sections))
	colW, total := 0, 0
	for i, s := range sections {
		blocks[i] = renderSection(s, skin)
		total += len(blocks[i])
		for _, c := range blocks[i] {
			colW = max(colW, utf8.RuneCountInString(c.plain))
		}
	}
	colW += helpGutter
	ncols := max(1, min(len(blocks), width/colW))

	// The shortest column height that fits every section, in order and
	// unsplit, into ncols columns: filling each column up to it is then the
	// best split.
	fill := func(height int) [][]helpCell {
		var cols [][]helpCell
		var cur []helpCell
		for _, b := range blocks {
			if len(cur) > 0 && len(cur)+len(b) > height {
				cols = append(cols, cur)
				cur = nil
			}
			cur = append(cur, b...)
		}
		return append(cols, cur)
	}
	height := 0
	for _, b := range blocks {
		height = max(height, len(b))
	}
	for len(fill(height)) > ncols && height < total {
		height++
	}
	cols := fill(height)

	rows := 0
	for _, c := range cols {
		rows = max(rows, len(c))
	}
	for r := range rows {
		var p, t strings.Builder
		for ci, c := range cols {
			cell := helpCell{}
			if r < len(c) {
				cell = c[r]
			}
			p.WriteString(cell.plain)
			t.WriteString(cell.tagged)
			if ci < len(cols)-1 {
				pad := strings.Repeat(" ", colW-utf8.RuneCountInString(cell.plain))
				p.WriteString(pad)
				t.WriteString(pad)
			}
		}
		plain = append(plain, strings.TrimRight(p.String(), " "))
		tagged = append(tagged, t.String())
	}
	return plain, tagged
}

// HelpView is the full-screen list of shortcuts opened by :help or ?: the m9s
// logo on top, then the shortcuts in as many columns as fit.
type HelpView struct {
	*tview.Flex
	skin     *ui.Skin
	logo     *tview.TextView
	body     *tview.TextView
	sections []helpSection
	readme   string // the README shown below the shortcuts, "" for none

	// The contents pane left of the body: its entries, the list showing
	// them, the separator, and the row holding the three.
	mid    *tview.Flex
	tocCol *tview.Flex // the contents caption over the list
	toc    *tview.List
	// Captions over the panes: the active one in yellow.
	tocCaption, bodyCaption *tview.TextView
	tocSep                  *tview.Box
	entries                 []helpTocEntry
	tocShown                bool
	tocW                    int
	syncing                 bool // the list is set from the code: do not scroll the body
	// textLeads: a search moved the text, so the highlight follows it
	// even while the contents pane has focus; a jump from the list ends it.
	textLeads bool
	// matchLeads: it was a search, so the section is the match's, not the
	// top line's (the match sits a few lines down).
	matchLeads bool

	width   int      // body width the text was laid out for
	plain   []string // body rows as plain text
	tagged  []string // body rows as drawn, before search highlights
	onClose func()

	// The / search: what is typed, its hits, the one shown, and where the
	// view was when the search began.
	status               *tview.TextView
	statusShown, typing  bool
	query                string
	matches              []helpMatch
	current              int
	originRow, originCol int
}

// NewHelpView builds the help window. onClose runs on q or Esc.
func NewHelpView(skin *ui.Skin, onClose func()) *HelpView {
	hv := &HelpView{
		Flex:    tview.NewFlex().SetDirection(tview.FlexRow),
		skin:    skin,
		logo:    tview.NewTextView(),
		body:    tview.NewTextView(),
		status:  tview.NewTextView(),
		onClose: onClose,
	}
	hv.SetBorder(true)
	hv.SetBorderPadding(0, 0, 1, 1)
	hv.SetTitle(" HELP — j/k move, tab contents/text, / search, ←/→ wide lines, q to close ")
	hv.SetTitleColor(skin.Color("accent"))
	hv.SetBorderColor(skin.Color("border"))
	hv.SetBackgroundColor(skin.Color("background"))

	logo := ui.Logo()
	hv.logo.SetDynamicColors(true)
	hv.logo.SetWrap(false)
	hv.logo.SetBackgroundColor(skin.Color("background"))
	hv.logo.SetText(fmt.Sprintf("[%s::b]%s", skin.ColorName("heading"), tview.Escape(strings.Join(logo, "\n"))))

	hv.body.SetDynamicColors(true)
	// Everything is laid out to the width already; code and tables wider
	// than the window scroll sideways (←/→) instead of breaking.
	hv.body.SetWrap(false)
	hv.body.SetScrollable(true)
	hv.body.SetBackgroundColor(skin.Color("background"))
	hv.body.SetTextColor(skin.Color("foreground"))

	hv.status.SetDynamicColors(true)
	hv.status.SetWrap(false)
	hv.status.SetBackgroundColor(skin.Color("background"))
	hv.status.SetTextColor(skin.Color("foreground"))

	hv.newHelpToc()
	hv.tocShown, hv.tocW = true, helpTocMin
	hv.tocCaption, hv.bodyCaption = hv.newCaption(), hv.newCaption()
	hv.tocCol = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(hv.tocCaption, 1, 0, false).
		AddItem(hv.toc, 0, 1, true)
	bodyCol := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(hv.bodyCaption, 1, 0, false).
		AddItem(hv.body, 0, 1, true)
	hv.mid = tview.NewFlex().
		AddItem(hv.tocCol, hv.tocW, 0, true).
		AddItem(hv.tocSep, helpTocGap, 0, false).
		AddItem(bodyCol, 0, 1, false)

	hv.AddItem(hv.logo, len(logo)+1, 0, false).
		AddItem(hv.mid, 0, 1, true)

	hv.body.SetInputCapture(hv.handleKey)
	return hv
}

// SetKeys lists the shortcuts of km. Help opens without a search.
func (hv *HelpView) SetKeys(km ui.KeyMap) {
	hv.typing, hv.query, hv.matches, hv.current = false, "", nil, 0
	hv.showStatus()
	hv.syncing = true // help opens on the first entry, at the top
	hv.toc.SetCurrentItem(0)
	hv.syncing = false
	hv.sections = helpSections(km)
	hv.width = -1 // lay out again on the next draw
	hv.render(80)
}

// SetReadme shows md, the README, below the shortcuts.
func (hv *HelpView) SetReadme(md string) {
	hv.readme = dropContentsSection(md)
	hv.width = -1 // lay out again on the next draw
}

func (hv *HelpView) render(width int) {
	if width == hv.width {
		return
	}
	hv.width = width
	plain, tagged := layoutHelp(hv.sections, hv.skin, width)
	entries := []helpTocEntry{{title: "Shortcuts"}}
	if hv.readme != "" {
		rule := strings.Repeat("─", max(width, 1))
		entries = append(entries, helpTocEntry{title: "README", line: len(plain) + 1})
		plain = append(plain, "", "README", rule)
		tagged = append(tagged, "",
			fmt.Sprintf("[%s::b]README[-:-:-]", hv.skin.ColorName("accent")),
			fmt.Sprintf("[%s::]%s[-:-:-]", hv.skin.ColorName("dim"), rule))
		for _, l := range renderMarkdown(hv.readme, hv.skin, width) {
			if l.level == 2 || l.level == 3 {
				entries = append(entries, helpTocEntry{title: l.title, level: l.level, line: len(plain)})
			}
			plain = append(plain, l.plain)
			tagged = append(tagged, l.tagged)
		}
	}
	hv.plain, hv.tagged = plain, tagged
	hv.setEntries(entries)
	if hv.query == "" {
		hv.body.SetText(strings.Join(tagged, "\n"))
		// Laid out again (opened, or the window resized): stay on the
		// section the contents pane is on.
		if cur := hv.toc.GetCurrentItem(); cur > 0 {
			hv.jumpTo(cur)
		} else {
			hv.body.ScrollToBeginning()
		}
		return
	}
	// Laid out again during a search: the same hit, at its new place.
	current := hv.current
	hv.findMatches()
	hv.current = min(current, max(len(hv.matches)-1, 0))
	hv.highlight()
	if len(hv.matches) > 0 {
		hv.scrollToMatch()
	}
	hv.showStatus()
}

// Draw lays the shortcuts out for the body's width, beside the contents
// pane when the window is wide enough for both, then draws.
func (hv *HelpView) Draw(screen tcell.Screen) {
	_, _, w, _ := hv.GetInnerRect()
	hv.tocShown = w >= helpTocNeedsW
	if !hv.tocShown {
		hv.setTocWidth(0)
		hv.render(w)
	} else {
		hv.setTocWidth(helpTocWidth(hv.entries))
		hv.render(w - hv.tocW - helpTocGap)
		if tw := helpTocWidth(hv.entries); tw != hv.tocW { // the README's titles came with this layout
			hv.setTocWidth(tw)
			hv.render(w - tw - helpTocGap)
		}
	}
	hv.Flex.Draw(screen)
	// After the text has drawn: G or a scroll past the end only settles
	// its offset there. Redraw the list when its highlight moved.
	if hv.followText() {
		hv.toc.Draw(screen)
	}
	// Captions last: they name the section the list now shows.
	hv.setCaption(hv.tocCaption, "CONTENTS", hv.toc.HasFocus())
	// With the pane hidden, keys go to the text (InputHandler): it is active.
	textActive := hv.body.HasFocus() || (!hv.tocShown && hv.toc.HasFocus())
	hv.setCaption(hv.bodyCaption, fitPath(hv.sectionParts(hv.toc.GetCurrentItem()), hv.width), textActive)
	if hv.tocShown {
		hv.tocCaption.Draw(screen)
	}
	hv.bodyCaption.Draw(screen)
}

// setTocWidth sizes the contents pane; 0 hides it and its separator.
func (hv *HelpView) setTocWidth(w int) {
	if w == hv.tocW {
		return
	}
	hv.tocW = w
	hv.mid.ResizeItem(hv.tocCol, w, 0)
	hv.mid.ResizeItem(hv.tocSep, min(w, helpTocGap), 0)
}

// Focus puts the keyboard on the contents pane, or on the text when the
// pane is hidden.
func (hv *HelpView) Focus(delegate func(p tview.Primitive)) {
	if hv.tocShown {
		delegate(hv.toc)
		return
	}
	delegate(hv.body)
}

// Text returns the shortcuts as plain text, as last laid out (for tests).
func (hv *HelpView) Text() string { return strings.Join(hv.plain, "\n") }
