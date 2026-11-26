package view

import (
	"strings"
	"testing"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/ui"
)

func freshLog(t *testing.T) {
	t.Helper()
	saved := ui.AppLog
	ui.AppLog = &ui.Logger{}
	t.Cleanup(func() { ui.AppLog = saved })
}

func drawTV(tv *tview.TextView, w, h int) {
	screen := tcell.NewSimulationScreen("UTF-8")
	_ = screen.Init()
	defer screen.Fini()
	screen.SetSize(w, h)
	tv.SetRect(0, 0, w, h)
	tv.Draw(screen)
}

// A panel shows the log, narrows it to a filter, and adds new entries
// under the same filter.
func TestLogPanel_appendsAndFilters(t *testing.T) {
	freshLog(t)
	ui.AppLog.Info("alpha")
	ui.AppLog.Info("beta")
	ui.AppLog.Info("alphabet")
	p := newLogPanel(tview.NewTextView(), testSkin())
	p.render()
	if n := len(strings.Split(strings.TrimRight(p.tv.GetText(true), "\n"), "\n")); n != 3 {
		t.Fatalf("shows %d lines, want 3", n)
	}
	if strings.HasSuffix(p.tv.GetText(true), "\n\n") {
		t.Error("the view ends in an empty line")
	}
	p.setFilter("alpha")
	if text := p.tv.GetText(true); strings.Contains(text, "beta\n") || strings.Count(text, "alpha") != 2 {
		t.Errorf("filtered:\n%s", text)
	}
	ui.AppLog.Info("alpha again")
	ui.AppLog.Info("gamma")
	p.appendNew()
	if text := p.tv.GetText(true); !strings.Contains(text, "alpha again") || strings.Contains(text, "gamma") {
		t.Errorf("after appending:\n%s", text)
	}
	if f, shown, total := p.counts(); f != "alpha" || shown != 3 || total != 5 {
		t.Errorf("counts = %q %d of %d", f, shown, total)
	}
}

// Scrolled up to read, new lines do not move the view.
func TestLogPanel_scrolledUpStaysPut(t *testing.T) {
	freshLog(t)
	for i := range 100 {
		ui.AppLog.Info("line %d", i)
	}
	p := newLogPanel(tview.NewTextView().SetScrollable(true), testSkin())
	p.render()
	drawTV(p.tv, 80, 10)
	p.tv.ScrollTo(20, 0)
	drawTV(p.tv, 80, 10)
	for i := range 5 {
		ui.AppLog.Info("new %d", i)
	}
	p.appendNew()
	drawTV(p.tv, 80, 10)
	if row, _ := p.tv.GetScrollOffset(); row != 20 {
		t.Errorf("view moved to row %d, want it to stay at 20", row)
	}
}
