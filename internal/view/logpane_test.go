package view

import (
	"path/filepath"
	"strings"
	"testing"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/mail"
	msync "github.com/zhecho/m9s/internal/sync"
	"github.com/zhecho/m9s/internal/ui"
)

// paneApp is a wired app whose log redraws are queued for the test.
func paneApp(t *testing.T, cfg *config.Config) (*RootApp, chan func()) {
	t.Helper()
	freshLog(t)
	skin, _ := ui.LoadSkin("")
	km, _ := ui.LoadKeyMap("")
	accounts := []*mail.Account{{Name: "a", Config: &config.Account{Name: "a", IMAP: config.IMAPConfig{User: "a@x.io"}}}}
	ra := NewRootApp(ui.NewApp(skin, km), cfg, mail.NewManager(accounts), map[string]msync.Account{}, nil, make(chan msync.Event))
	queued := make(chan func(), 64)
	ra.logDeliver = func(fn func()) { queued <- fn }
	return ra, queued
}

func TestLogPane_onByDefault(t *testing.T) {
	ra, queued := paneApp(t, &config.Config{})
	if ra.logPane == nil || ra.outer == nil || ra.outer.BottomRows() != 12 {
		t.Fatalf("log pane %v, outer %v; want a 12-row pane", ra.logPane != nil, ra.outer)
	}
	ui.AppLog.Info("hello pane")
	settle(queued)
	runOne(t, queued)
	if !strings.Contains(ra.logPane.GetText(true), "hello pane") {
		t.Errorf("pane shows:\n%s", ra.logPane.GetText(true))
	}
}

func TestLogPane_off(t *testing.T) {
	off := false
	cfg := &config.Config{}
	cfg.UI.LogPane = &off
	ra, _ := paneApp(t, cfg)
	if ra.logPane != nil || ra.outer != nil {
		t.Fatal("a log pane with log_pane: false")
	}
	ra.uiApp.SetFocus(ra.preview)
	ra.cycleFocus()
	if ra.uiApp.GetFocus() != tview.Primitive(ra.folders) {
		t.Error("tab from the preview did not go to the folders")
	}
}

func TestLogPane_tabReachesIt(t *testing.T) {
	ra, _ := paneApp(t, &config.Config{})
	ra.uiApp.SetFocus(ra.preview)
	ra.cycleFocus()
	if ra.uiApp.GetFocus() != tview.Primitive(ra.logPane) {
		t.Fatal("tab from the preview did not reach the log pane")
	}
	if !strings.Contains(ra.logPane.GetTitle(), "[::b]LOG") {
		t.Errorf("focused title = %q", ra.logPane.GetTitle())
	}
	ra.cycleFocus()
	if ra.uiApp.GetFocus() != tview.Primitive(ra.folders) {
		t.Error("tab from the log pane did not go to the folders")
	}
}

// / in the focused pane filters the pane, not the mail list; Esc clears
// the filter, then leaves the pane.
func TestLogPane_slashFiltersThePane(t *testing.T) {
	ra, _ := paneApp(t, &config.Config{})
	ra.uiApp.SetFocus(ra.logPane)
	sendRune(ra, '/')
	typeInPrompt(ra, "zz")
	promptKey(ra, tcell.KeyEnter)
	if ra.logPanePanel.filter != "zz" || ra.maillist.FilterQuery() != "" {
		t.Fatalf("pane filter %q, mail filter %q", ra.logPanePanel.filter, ra.maillist.FilterQuery())
	}
	if !strings.Contains(ra.logPane.GetTitle(), "/zz") {
		t.Errorf("title = %q, want the filter", ra.logPane.GetTitle())
	}
	esc := func() { // as tview delivers it: the app's capture, then down from the root
		ev := tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)
		if out := ra.uiApp.GetInputCapture()(ev); out != nil {
			ra.uiApp.Pages.InputHandler()(out, func(p tview.Primitive) { ra.uiApp.SetFocus(p) })
		}
	}
	esc()
	if ra.logPanePanel.filter != "" || ra.uiApp.GetFocus() != tview.Primitive(ra.logPane) {
		t.Errorf("first Esc: filter %q, focus %T; want the filter gone and the pane kept", ra.logPanePanel.filter, ra.uiApp.GetFocus())
	}
	esc()
	if ra.uiApp.GetFocus() == tview.Primitive(ra.logPane) {
		t.Error("a second Esc did not leave the pane")
	}
}

// Message keys do nothing in the log pane: d deletes no message.
func TestLogPane_messageKeysStayOut(t *testing.T) {
	ra, _ := paneApp(t, &config.Config{})
	ra.uiApp.SetFocus(ra.logPane)
	if ev := sendRune(ra, 'd'); ev == nil {
		t.Error("d was taken as a message action in the log pane")
	}
}

func TestLogPane_lStillOpensThePage(t *testing.T) {
	ra, _ := paneApp(t, &config.Config{})
	ra.uiApp.SetFocus(ra.logPane)
	sendRune(ra, 'l')
	if page, _ := ra.uiApp.Pages.GetFrontPage(); page != pageLog {
		t.Errorf("front page = %q, want the log page", page)
	}
}

func TestLogPane_sizeRemembered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "layout.json")
	if err := config.SaveLayoutState(path, config.LayoutState{MailListRows: 7, LogPaneRows: 5}); err != nil {
		t.Fatal(err)
	}
	ra, _ := paneApp(t, &config.Config{})
	ra.SetLayoutStatePath(path)
	if ra.outer.BottomRows() != 5 || ra.split.TopRows() != 7 {
		t.Errorf("log %d, mail list %d; want 5 and 7", ra.outer.BottomRows(), ra.split.TopRows())
	}
}

// A click on the pane focuses it, as on the other panes.
func TestLogPane_clickFocusesIt(t *testing.T) {
	ra, _ := paneApp(t, &config.Config{})
	drawMainPage(t, ra, 120, 50)
	click(ra, ra.logPane)
	if ra.uiApp.GetFocus() != tview.Primitive(ra.logPane) || !strings.Contains(ra.logPane.GetTitle(), "[::b]") {
		t.Errorf("focus %T, title %q; want the pane, highlighted", ra.uiApp.GetFocus(), ra.logPane.GetTitle())
	}
}

// fireMouse sends one mouse event the way tview's application does: first
// to the mouse capture (the text selector), then to the primitive that
// captured the mouse, else to the root. It returns the new capture.
func fireMouse(ra *RootApp, captured tview.Primitive, action tview.MouseAction, x, y int) tview.Primitive {
	ev := tcell.NewEventMouse(x, y, tcell.Button1, tcell.ModNone)
	if mc := ra.uiApp.GetMouseCapture(); mc != nil {
		if ev, action = mc(ev, action); ev == nil {
			return captured
		}
	}
	target := captured
	if target == nil {
		target = ra.uiApp.Pages
	}
	_, capture := target.MouseHandler()(action, ev, func(p tview.Primitive) { ra.uiApp.SetFocus(p) })
	return capture
}

// Dragging the log pane's border with the mouse resizes it — through the
// text selector, which must not take the drag for a selection — and the
// next click is an ordinary click.
func TestLogPane_dragResizesThroughTheRealMouse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "layout.json")
	ra, _ := paneApp(t, &config.Config{})
	ra.SetLayoutStatePath(path)
	drawMainPage(t, ra, 120, 50)
	_, py, _, _ := ra.logPane.GetRect()
	var cap tview.Primitive
	cap = fireMouse(ra, cap, tview.MouseLeftDown, 30, py)
	cap = fireMouse(ra, cap, tview.MouseMove, 30, py-3)
	cap = fireMouse(ra, cap, tview.MouseLeftUp, 30, py-3)
	if ra.outer.BottomRows() != 15 || ra.outer.Dragging() {
		t.Fatalf("after the drag: %d rows, dragging %v; want 15 and done", ra.outer.BottomRows(), ra.outer.Dragging())
	}
	if st := config.LoadLayoutState(path); st.LogPaneRows != 15 {
		t.Errorf("saved %+v, want log_pane_rows 15", st)
	}
	_, my, _, _ := ra.maillist.GetRect()
	cap = fireMouse(ra, cap, tview.MouseLeftDown, 40, my+2)
	fireMouse(ra, cap, tview.MouseLeftUp, 40, my+2)
	if ra.outer.BottomRows() != 15 {
		t.Errorf("a click on the mail list resized the log to %d", ra.outer.BottomRows())
	}
}

// Back at the end with j, the pane follows new lines again.
func TestLogPane_jBackToTheEndFollows(t *testing.T) {
	ra, queued := paneApp(t, &config.Config{})
	for i := range 60 {
		ui.AppLog.Info("line %d", i)
	}
	settle(queued)
	runOne(t, queued)
	drawMainPage(t, ra, 120, 50)
	press := func(r rune) {
		ra.logPane.InputHandler()(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone), func(tview.Primitive) {})
		drawMainPage(t, ra, 120, 50)
	}
	end, _ := ra.logPane.GetScrollOffset()
	press('k')
	press('k')
	press('j')
	press('j')
	if row, _ := ra.logPane.GetScrollOffset(); row != end {
		t.Fatalf("back at row %d, want the end %d", row, end)
	}
	ui.AppLog.Info("brand new")
	settle(queued)
	runOne(t, queued)
	drawMainPage(t, ra, 120, 50)
	if row, _ := ra.logPane.GetScrollOffset(); row != end+1 {
		t.Errorf("at row %d after a new line, want %d: following the end", row, end+1)
	}
}

// The log page, too, follows new lines again once j brings it back to the end.
func TestLogPage_jBackToTheEndFollows(t *testing.T) {
	ra, queued := paneApp(t, &config.Config{})
	for i := range 80 {
		ui.AppLog.Info("line %d", i)
	}
	settle(queued)
	runOne(t, queued)
	ra.toggleLog()
	draw := func() {
		screen := tcell.NewSimulationScreen("UTF-8")
		_ = screen.Init()
		defer screen.Fini()
		screen.SetSize(120, 30)
		ra.logView.SetRect(0, 0, 120, 30)
		ra.logView.Draw(screen)
	}
	draw()
	press := func(r rune) {
		ra.logView.InputHandler()(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone), func(tview.Primitive) {})
		draw()
	}
	end, _ := ra.logView.GetScrollOffset()
	press('k')
	press('j')
	ui.AppLog.Info("brand new")
	settle(queued)
	runOne(t, queued)
	draw()
	if row, _ := ra.logView.GetScrollOffset(); row != end+1 {
		t.Errorf("page at row %d after a new line, want %d", row, end+1)
	}
}
