package view

import (
	"strings"
	"testing"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"

	"github.com/zhecho/m9s/internal/ui"
)

func logEsc(ra *RootApp) {
	if c := ra.logView.GetInputCapture(); c != nil {
		if c(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)) == nil {
			return
		}
	}
}

func frontPage(ra *RootApp) string {
	p, _ := ra.uiApp.Pages.GetFrontPage()
	return p
}

// / on the log page opens the same prompt row as on the main page, at the
// top of the log page; the log narrows to the lines with the text as it is
// typed, q and l type into it, Enter keeps the filter.
func TestLogFilter_slashOpensThePromptRowAndFilters(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ui.AppLog.Info("lf-1 apple pie")
	ui.AppLog.Warn("lf-1 banana split")
	ra.toggleLog()
	if frontPage(ra) != pageLog {
		t.Fatalf("front page = %q", frontPage(ra))
	}
	if sendRune(ra, '/') != nil {
		t.Fatal("/ not taken on the log page")
	}
	if !ra.promptOpen || ra.promptHost != ra.logPage {
		t.Fatalf("prompt row not on the log page (open %v)", ra.promptOpen)
	}
	screen := drawMainPage(t, ra, 100, 20)
	promptRow, logRow := -1, -1
	for i, l := range screen {
		if promptRow < 0 && strings.Contains(l, "filter") {
			promptRow = i
		}
		if logRow < 0 && strings.Contains(l, "LOGS") {
			logRow = i
		}
	}
	if promptRow < 0 || logRow < 0 || promptRow > logRow {
		t.Fatalf("want the filter row above the log box:\n%s", strings.Join(screen, "\n"))
	}
	if ra.uiApp.GetFocus() != tview.Primitive(ra.prompt) {
		t.Fatal("prompt not focused")
	}
	typeInPrompt(ra, "banana")
	text := ra.logView.GetText(true)
	if !strings.Contains(text, "lf-1 banana split") || strings.Contains(text, "apple pie") {
		t.Fatalf("not filtered:\n%s", text)
	}
	for _, r := range "ql" {
		if sendRune(ra, r) == nil || frontPage(ra) != pageLog {
			t.Fatalf("%c acted on the page while typing a filter", r)
		}
	}
	typeInPrompt(ra, "")
	promptKey(ra, tcell.KeyEnter)
	if ra.promptOpen {
		t.Fatal("Enter did not close the prompt row")
	}
	for _, l := range drawMainPage(t, ra, 100, 20)[:2] {
		if strings.Contains(l, "filter") {
			t.Fatalf("prompt row still drawn: %q", l)
		}
	}
	if ra.uiApp.GetFocus() != tview.Primitive(ra.logView) {
		t.Error("focus not back on the log")
	}
	if text := ra.logView.GetText(true); strings.Contains(text, "apple pie") {
		t.Errorf("filter dropped by Enter:\n%s", text)
	}
	if title := ra.logView.GetTitle(); !strings.Contains(title, "/banana") {
		t.Errorf("title %q does not name the filter", title)
	}

	// Esc clears the filter first, then closes the log page.
	logEsc(ra)
	if frontPage(ra) != pageLog || !strings.Contains(ra.logView.GetText(true), "apple pie") {
		t.Fatalf("first Esc: page %q, text:\n%s", frontPage(ra), ra.logView.GetText(true))
	}
	logEsc(ra)
	if frontPage(ra) != pageMain {
		t.Fatalf("second Esc: page %q", frontPage(ra))
	}
}

func TestLogFilter_escInThePromptDropsTheFilter(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ui.AppLog.Info("lf-2 apple")
	ui.AppLog.Info("lf-2 banana")
	ra.toggleLog()
	sendRune(ra, '/')
	typeInPrompt(ra, "banana")
	promptKey(ra, tcell.KeyEscape)
	if ra.promptOpen || frontPage(ra) != pageLog {
		t.Fatalf("prompt open %v, page %q", ra.promptOpen, frontPage(ra))
	}
	if !strings.Contains(ra.logView.GetText(true), "lf-2 apple") {
		t.Errorf("filter kept after Esc:\n%s", ra.logView.GetText(true))
	}
}

func TestLogFilter_newLinesFollowTheFilter(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ui.AppLog.Info("lf-3 banana")
	ra.toggleLog()
	sendRune(ra, '/')
	typeInPrompt(ra, "banana")
	promptKey(ra, tcell.KeyEnter)
	ui.AppLog.Info("lf-3 banana bread")
	ui.AppLog.Info("lf-3 cherry")
	ra.renderLogView() // what the log's update callback does
	text := ra.logView.GetText(true)
	if !strings.Contains(text, "banana bread") || strings.Contains(text, "cherry") {
		t.Errorf("new lines not filtered:\n%s", text)
	}
}

func TestLogFilter_smartCaseAndLevels(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ui.AppLog.Info("lf-4 Kiwi")
	ui.AppLog.Error("lf-4 kiwi broke")
	ra.toggleLog()
	ra.setLogFilter("Kiwi")
	if text := ra.logView.GetText(true); strings.Contains(text, "kiwi broke") || !strings.Contains(text, "lf-4 Kiwi") {
		t.Errorf("a capital letter must match case:\n%s", text)
	}
	ra.setLogFilter("error lf-4")
	if text := ra.logView.GetText(true); !strings.Contains(text, "kiwi broke") || strings.Contains(text, "lf-4 Kiwi") {
		t.Errorf("the level is part of the line:\n%s", text)
	}
}

func TestLogFilter_clearedWhenTheLogCloses(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ui.AppLog.Info("lf-5 apple")
	ra.toggleLog()
	ra.setLogFilter("nothing-matches-this")
	ra.closeLog()
	ra.toggleLog()
	if !strings.Contains(ra.logView.GetText(true), "lf-5 apple") {
		t.Errorf("the log reopened filtered:\n%s", ra.logView.GetText(true))
	}
}

func TestHelpSections_listLogFilter(t *testing.T) {
	km, _ := ui.LoadKeyMap("")
	var log []string
	for _, s := range helpSections(km) {
		if s.Title == "LOG VIEW" {
			for _, e := range s.Entries {
				log = append(log, "<"+e.Key+"> "+e.Desc)
			}
		}
	}
	text := strings.Join(log, "\n")
	for _, want := range []string{"</> Filter", "<esc> Clear filter", "<j/k g/G> Scroll"} {
		if !strings.Contains(text, want) {
			t.Errorf("LOG VIEW lacks %q:\n%s", want, text)
		}
	}
}
