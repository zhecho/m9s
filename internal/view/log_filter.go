package view

import (
	"fmt"
	"strings"

	"github.com/derailed/tview"

	"github.com/zhecho/m9s/internal/ui"
)

const logTitle = " LOGS — j/k scroll, / filter, q to close "

// logMatcher reports whether a log line has text in it: lower case matches
// any case, a capital letter makes it match case exactly (as the help
// search does).
func logMatcher(text string) func(string) bool {
	if text == "" {
		return func(string) bool { return true }
	}
	if strings.ToLower(text) == text {
		return func(line string) bool { return strings.Contains(strings.ToLower(line), text) }
	}
	return func(line string) bool { return strings.Contains(line, text) }
}

// logLine is an entry as the log page shows it, without colours: what the
// filter looks at.
func logLine(e ui.Entry) string {
	return e.Time.Format("2006-01-02 15:04:05") + " " + e.Level.String() + " " + e.Msg
}

// renderLogFiltered redraws tv with the entries that match filter, newest
// last, and returns how many it shows of how many there are.
func renderLogFiltered(tv *tview.TextView, skin *ui.Skin, filter string) (shown, total int) {
	tv.Clear()
	match := logMatcher(filter)
	entries := ui.AppLog.Entries()
	for _, e := range entries {
		if match(logLine(e)) {
			fmt.Fprintln(tv, ui.FormatEntry(e, skin))
			shown++
		}
	}
	tv.ScrollToEnd()
	return shown, len(entries)
}

// renderLogView redraws the log page with its filter and names the filter
// in the title. UI goroutine only.
func (ra *RootApp) renderLogView() {
	shown, total := renderLogFiltered(ra.logView, ra.uiApp.Skin, ra.logFilter)
	ra.logRendered, ra.logShown, ra.logRenderedFilter = total, shown, ra.logFilter
	ra.setLogTitle()
}

// appendLogView adds the entries logged since the log page was last drawn,
// those that match its filter; a changed filter or a shorter log redraws it
// all. UI goroutine only.
func (ra *RootApp) appendLogView() {
	entries := ui.AppLog.Entries()
	if ra.logFilter != ra.logRenderedFilter || len(entries) < ra.logRendered {
		ra.renderLogView()
		return
	}
	match := logMatcher(ra.logFilter)
	for _, e := range entries[ra.logRendered:] {
		if match(logLine(e)) {
			fmt.Fprintln(ra.logView, ui.FormatEntry(e, ra.uiApp.Skin))
			ra.logShown++
		}
	}
	ra.logRendered = len(entries)
	ra.logView.ScrollToEnd()
	ra.setLogTitle()
}

// setLogTitle names the log page's filter, and how many lines it shows.
func (ra *RootApp) setLogTitle() {
	if ra.logFilter == "" {
		ra.logView.SetTitle(logTitle)
		return
	}
	ra.logView.SetTitle(fmt.Sprintf(" LOGS — /%s (%d of %d) — esc clears, q to close ", tview.Escape(ra.logFilter), ra.logShown, ra.logRendered))
}

// setLogFilter shows only the log lines with text ("" shows them all).
func (ra *RootApp) setLogFilter(text string) {
	ra.logFilter = text
	ra.renderLogView()
}

// startLogFilter opens the / prompt row at the top of the log page: the
// log narrows as the text is typed, Enter keeps the filter, Esc drops it.
func (ra *RootApp) startLogFilter() {
	ra.openPrompt(false, ui.PromptConfig{
		Label:    "filter",
		Prefix:   "/",
		OnChange: ra.setLogFilter,
		OnDone: func(text string, accepted bool) {
			if !accepted {
				ra.setLogFilter("")
			}
			ra.closePrompt(ra.logView)
		},
	})
}
