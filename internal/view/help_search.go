package view

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
)

// helpMatch is one hit of the help search: rune offsets in a plain line.
type helpMatch struct{ line, start, end int }

// handleKey is the help window's keys: / searches (as you type; Enter keeps
// the search, Esc drops it), n and N go to the next and previous match, Esc
// clears a search and then closes, q closes. While a search is typed every
// letter, q included, goes into it.
func (hv *HelpView) handleKey(event *tcell.EventKey) *tcell.EventKey {
	if hv.typing {
		switch event.Key() {
		case tcell.KeyEscape:
			hv.cancelSearch()
		case tcell.KeyEnter:
			hv.typing = false
			hv.showStatus()
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			if r := []rune(hv.query); len(r) > 0 {
				hv.query = string(r[:len(r)-1])
				hv.search()
			}
		case tcell.KeyRune:
			hv.query += string(event.Rune())
			hv.search()
		default:
			return event // arrows and page keys still scroll
		}
		return nil
	}
	switch {
	case event.Key() == tcell.KeyRune && event.Rune() == '/':
		hv.originRow, hv.originCol = hv.body.GetScrollOffset()
		hv.typing, hv.query, hv.matches = true, "", nil
		hv.highlight()
		hv.showStatus()
		return nil
	case event.Key() == tcell.KeyRune && (event.Rune() == 'n' || event.Rune() == 'N') && len(hv.matches) > 0:
		step := 1
		if event.Rune() == 'N' {
			step = len(hv.matches) - 1
		}
		hv.current = (hv.current + step) % len(hv.matches)
		hv.highlight()
		hv.scrollToMatch()
		hv.showStatus()
		return nil
	case event.Key() == tcell.KeyEscape && hv.query != "":
		hv.clearSearch()
		return nil
	case event.Key() == tcell.KeyEscape || (event.Key() == tcell.KeyRune && event.Rune() == 'q'):
		if hv.onClose != nil {
			hv.onClose()
		}
		return nil
	}
	return event
}

// search finds the query in every line and goes to the first match at or
// below where the search started.
func (hv *HelpView) search() {
	hv.findMatches()
	hv.current = 0
	for i, m := range hv.matches {
		if m.line >= hv.originRow {
			hv.current = i
			break
		}
	}
	hv.highlight()
	if len(hv.matches) > 0 {
		hv.scrollToMatch()
	} else {
		hv.body.ScrollTo(hv.originRow, hv.originCol)
	}
	hv.showStatus()
}

// findMatches lists every hit of the query. Lower case matches any case;
// a capital letter in the query makes it match case exactly.
func (hv *HelpView) findMatches() {
	hv.matches = nil
	q := []rune(hv.query)
	if len(q) == 0 {
		return
	}
	fold := strings.ToLower(hv.query) == hv.query
	lower := func(rs []rune) []rune {
		if !fold {
			return rs
		}
		out := make([]rune, len(rs))
		for i, r := range rs {
			out[i] = unicode.ToLower(r)
		}
		return out
	}
	for li, line := range hv.plain {
		rs := lower([]rune(line))
		for i := 0; i+len(q) <= len(rs); {
			if string(rs[i:i+len(q)]) == string(q) {
				hv.matches = append(hv.matches, helpMatch{li, i, i + len(q)})
				i += len(q)
				continue
			}
			i++
		}
	}
}

// currentMatchTag draws the match the search is on.
func (hv *HelpView) currentMatchTag() string {
	return fmt.Sprintf("[%s:%s:b]", hv.skin.ColorName("background"), hv.skin.ColorName("accent"))
}

// highlight shows the body with every match marked. A line with a match is
// drawn from its plain text, so the highlight is exact.
func (hv *HelpView) highlight() {
	row, col := hv.body.GetScrollOffset()
	lines := append([]string(nil), hv.tagged...)
	fg := hv.skin.ColorName("foreground")
	for i := 0; i < len(hv.matches); {
		li := hv.matches[i].line
		rs := []rune(hv.plain[li])
		var b strings.Builder
		at := 0
		for ; i < len(hv.matches) && hv.matches[i].line == li; i++ {
			m := hv.matches[i]
			tag := "[::r]"
			if i == hv.current {
				tag = hv.currentMatchTag()
			}
			fmt.Fprintf(&b, "[%s:-:-]%s%s%s[-:-:-]", fg, tview.Escape(string(rs[at:m.start])), tag, tview.Escape(string(rs[m.start:m.end])))
			at = m.end
		}
		fmt.Fprintf(&b, "[%s:-:-]%s[-:-:-]", fg, tview.Escape(string(rs[at:])))
		lines[li] = b.String()
	}
	hv.body.SetText(strings.Join(lines, "\n"))
	hv.body.ScrollTo(row, col)
}

// scrollToMatch brings the current match into view, a few lines from the
// top, and sideways when it is beyond the window's right edge.
func (hv *HelpView) scrollToMatch() {
	m := hv.matches[hv.current]
	rs := []rune(hv.plain[m.line])
	col := 0
	if end := runewidth.StringWidth(string(rs[:m.end])); hv.width > 0 && end > hv.width {
		col = max(runewidth.StringWidth(string(rs[:m.start]))-hv.width/3, 0)
	}
	hv.body.ScrollTo(max(m.line-3, 0), col)
	hv.textLeads, hv.matchLeads = true, true
}

// cancelSearch drops the search being typed and goes back to where it began.
func (hv *HelpView) cancelSearch() {
	hv.clearSearch()
	hv.body.ScrollTo(hv.originRow, hv.originCol)
	hv.textLeads, hv.matchLeads = true, false
}

// clearSearch removes the search and its highlights.
func (hv *HelpView) clearSearch() {
	hv.typing, hv.query, hv.matches, hv.current = false, "", nil, 0
	hv.highlight()
	hv.showStatus()
}

// showStatus puts the search line under the body while there is a search.
func (hv *HelpView) showStatus() {
	if !hv.typing && hv.query == "" {
		if hv.statusShown {
			hv.Flex.RemoveItem(hv.status)
			hv.statusShown = false
		}
		hv.status.SetText("")
		return
	}
	if !hv.statusShown {
		hv.Flex.AddItem(hv.status, 1, 0, false)
		hv.statusShown = true
	}
	text := "/" + tview.Escape(hv.query)
	switch {
	case hv.query == "":
	case len(hv.matches) == 0:
		text += fmt.Sprintf("  [%s::]no match[-:-:-]", hv.skin.ColorName("flash_err"))
	default:
		text += fmt.Sprintf("  [%s::]%d/%d[-:-:-]", hv.skin.ColorName("dim"), hv.current+1, len(hv.matches))
		if !hv.typing {
			text += fmt.Sprintf("  [%s::]n/N next/previous, esc clear[-:-:-]", hv.skin.ColorName("dim"))
		}
	}
	hv.status.SetText(text)
}

// SearchStatus is the search line as plain text, "" when there is no search
// (for tests).
func (hv *HelpView) SearchStatus() string {
	if !hv.statusShown {
		return ""
	}
	return hv.status.GetText(true)
}
