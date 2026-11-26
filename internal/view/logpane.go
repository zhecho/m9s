package view

import (
	"fmt"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"

	"github.com/zhecho/m9s/internal/ui"
)

const logPaneTitle = " LOG "

// logPaneView is a log view — the log pane, or the log page's view: a
// TextView that tells when it gains or loses the focus, as the other panes
// do, takes the focus when clicked, and follows new lines again once
// scrolled back to the end.
type logPaneView struct {
	*tview.TextView
	focusFunc, blurFunc func()
	nudge               bool // scrolled down: see whether that reached the end
}

func newLogView(tv *tview.TextView) *logPaneView { return &logPaneView{TextView: tv} }

// InputHandler notes a scroll down. tview follows the end again only when
// scrolled past it; arriving exactly on the last line with j leaves it
// stopped, so Draw checks.
func (v *logPaneView) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	inner := v.TextView.InputHandler()
	return func(event *tcell.EventKey, setFocus func(tview.Primitive)) {
		inner(event, setFocus)
		switch event.Key() {
		case tcell.KeyDown, tcell.KeyCtrlE, tcell.KeyPgDn, tcell.KeyCtrlF:
			v.nudge = true
		case tcell.KeyRune:
			if event.Rune() == 'j' || event.Rune() == ' ' {
				v.nudge = true
			}
		}
	}
}

// Draw, after a scroll down, tries one line further: past the end, tview
// clamps it back and follows the end again; short of it, the step is undone.
func (v *logPaneView) Draw(screen tcell.Screen) {
	if !v.nudge {
		v.TextView.Draw(screen)
		return
	}
	v.nudge = false
	row, col := v.GetScrollOffset()
	v.ScrollTo(row+1, col)
	v.TextView.Draw(screen)
	if r, _ := v.GetScrollOffset(); r == row+1 { // not at the end
		v.ScrollTo(row, col)
		v.TextView.Draw(screen)
	}
}

// Focus fires the focus callback.
func (v *logPaneView) Focus(delegate func(tview.Primitive)) {
	if v.focusFunc != nil {
		v.focusFunc()
	}
	v.TextView.Focus(delegate)
}

// Blur fires the blur callback.
func (v *logPaneView) Blur() {
	if v.blurFunc != nil {
		v.blurFunc()
	}
	v.TextView.Blur()
}

// MouseHandler focuses the pane itself, not the TextView inside it, so the
// focus callbacks and the key handling see it.
func (v *logPaneView) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	inner := v.TextView.MouseHandler()
	return func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		if action == tview.MouseScrollDown {
			v.nudge = true
		}
		return inner(action, event, func(p tview.Primitive) {
			if p == tview.Primitive(v.TextView) {
				p = v
			}
			setFocus(p)
		})
	}
}

// newLogPaneView is the log pane's view: bordered like the other panes,
// scrollable, following new lines while scrolled to the end. Esc clears
// its filter, then leaves it for the folders.
func (ra *RootApp) newLogPaneView() *logPaneView {
	skin := ra.uiApp.Skin
	tv := tview.NewTextView()
	v := newLogView(tv)
	tv.SetBorder(true)
	tv.SetTitle(logPaneTitle)
	tv.SetTitleColor(skin.Color("header_fg"))
	tv.SetBorderColor(skin.Color("border"))
	tv.SetBackgroundColor(skin.Color("background"))
	tv.SetTextColor(skin.Color("foreground"))
	tv.SetDynamicColors(true)
	tv.SetScrollable(true)
	tv.SetWordWrap(true)
	tv.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() != tcell.KeyEscape {
			return event
		}
		if ra.logPanePanel.filter != "" {
			ra.setLogPaneFilter("")
			return nil
		}
		ra.uiApp.SetFocus(ra.folders)
		return nil
	})
	v.focusFunc = func() {
		ra.setLogPaneTitle(true)
		tv.SetTitleColor(tcell.NewRGBColor(255, 255, 0))
		ra.setPane(paneLog)
	}
	v.blurFunc = func() {
		ra.setLogPaneTitle(false)
		tv.SetTitleColor(skin.Color("header_fg"))
	}
	return v
}

// logPaneFocused reports whether the log pane has the focus.
func (ra *RootApp) logPaneFocused() bool {
	return ra.logPane != nil && ra.uiApp.GetFocus() == tview.Primitive(ra.logPane)
}

// setLogPaneTitle names the pane, bold while focused, with its filter and
// how many lines it shows.
// focused may be given while the focus is changing (the callbacks run
// before tview records it).
func (ra *RootApp) setLogPaneTitle(focused ...bool) {
	if ra.logPane == nil {
		return
	}
	isFocused := ra.logPane.HasFocus()
	if len(focused) > 0 {
		isFocused = focused[0]
	}
	text := "LOG"
	if filter, shown, total := ra.logPanePanel.counts(); filter != "" {
		text = fmt.Sprintf("LOG — /%s (%d of %d)", tview.Escape(filter), shown, total)
	}
	if isFocused {
		text = "[::b]" + text + "[::-]"
	}
	ra.logPane.SetTitle(" " + text + " ")
}

// setLogPaneFilter shows only the pane's lines with text ("" all of them).
func (ra *RootApp) setLogPaneFilter(text string) {
	ra.logPanePanel.setFilter(text)
	ra.setLogPaneTitle()
}

// startLogPaneFilter opens the / prompt for the log pane: it narrows as the
// text is typed, Enter keeps the filter, Esc drops it.
func (ra *RootApp) startLogPaneFilter() {
	ra.openPrompt(false, ui.PromptConfig{
		Label:    "filter",
		Prefix:   "/",
		OnChange: ra.setLogPaneFilter,
		OnDone: func(text string, accepted bool) {
			if !accepted {
				ra.setLogPaneFilter("")
			}
			ra.closePrompt(ra.logPane)
		},
	})
}
