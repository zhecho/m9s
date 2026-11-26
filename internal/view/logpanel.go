package view

import (
	"github.com/derailed/tview"

	"github.com/zhecho/m9s/internal/ui"
)

// logPanel draws the log into a TextView — the log page's, or the log
// pane's — under a filter, and keeps up with it by adding only what is new.
// New lines never move a view that was scrolled up: the TextView follows
// the end by itself while it is at the end.
type logPanel struct {
	tv          *tview.TextView
	skin        *ui.Skin
	filter      string // lines shown contain this ("" all)
	drawnFilter string // the filter the view was drawn with
	drawn       int    // log entries looked at so far
	shown       int    // of those, how many matched
}

func newLogPanel(tv *tview.TextView, skin *ui.Skin) *logPanel {
	return &logPanel{tv: tv, skin: skin}
}

// render draws the whole log under the filter, scrolled to its end.
func (p *logPanel) render() {
	shown, total := renderLogFiltered(p.tv, p.skin, p.filter)
	p.drawn, p.shown, p.drawnFilter = total, shown, p.filter
}

// appendNew adds the entries logged since the last draw that match the
// filter; a changed filter, or a log shorter than drawn, draws it all.
func (p *logPanel) appendNew() {
	entries := ui.AppLog.Entries()
	if p.filter != p.drawnFilter || len(entries) < p.drawn {
		p.render()
		return
	}
	match := logMatcher(p.filter)
	for _, e := range entries[p.drawn:] {
		if match(logLine(e)) {
			writeLogLine(p.tv, ui.FormatEntry(e, p.skin), p.shown == 0)
			p.shown++
		}
	}
	p.drawn = len(entries)
}

// setFilter shows only the lines with text ("" shows them all).
func (p *logPanel) setFilter(text string) {
	p.filter = text
	p.render()
}

// counts is the filter and how many lines it shows of how many there are.
func (p *logPanel) counts() (filter string, shown, total int) {
	return p.filter, p.shown, p.drawn
}
