package view

import (
	"strings"
	"testing"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	msync "github.com/zhecho/m9s/internal/sync"
)

// promptApp returns an app with INBOX open and three messages listed.
func promptApp(t *testing.T, accounts ...string) *RootApp {
	t.Helper()
	if len(accounts) == 0 {
		accounts = []string{"test"}
	}
	ra := newWiredRootApp(t, accounts...)
	ra.handleEvent(msync.FoldersUpdated{AccountName: accounts[0], Folders: testFolders("INBOX", "Sent", "Drafts")})
	ra.maillist.SetEnvelopes(peopleEnvelopes())
	return ra
}

func typeInPrompt(ra *RootApp, text string) {
	for _, r := range text {
		ra.prompt.InputHandler()(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone), func(tview.Primitive) {})
	}
}

func promptKey(ra *RootApp, k tcell.Key) {
	ra.prompt.InputHandler()(tcell.NewEventKey(k, 0, tcell.ModNone), func(tview.Primitive) {})
}

func TestFilter_slashOpensPromptFromAnyPane(t *testing.T) {
	ra := promptApp(t)
	ra.uiApp.SetFocus(ra.folders)
	if ev := sendRune(ra, '/'); ev != nil {
		t.Error("/ was not consumed")
	}
	if !ra.promptOpen {
		t.Fatal("/ did not open the prompt")
	}
	if ra.uiApp.GetFocus() != tview.Primitive(ra.prompt) {
		t.Error("the prompt must take focus")
	}
}

func TestFilter_liveNarrowing(t *testing.T) {
	ra := promptApp(t)
	ra.startFilter()
	typeInPrompt(ra, "bob")
	if ra.maillist.VisibleCount() != 1 {
		t.Errorf("VisibleCount = %d while typing bob, want 1", ra.maillist.VisibleCount())
	}
	if got := ra.uiApp.StatusBar.CrumbsText(); !strings.HasSuffix(got, "</bob>") {
		t.Errorf("crumbs = %q, want them to end with </bob>", got)
	}
}

func TestFilter_enterKeepsAndFocusesList(t *testing.T) {
	ra := promptApp(t)
	ra.uiApp.SetFocus(ra.folders)
	ra.startFilter()
	typeInPrompt(ra, "bob")
	promptKey(ra, tcell.KeyEnter)

	if ra.promptOpen {
		t.Error("prompt still open after Enter")
	}
	if ra.maillist.FilterQuery() != "bob" {
		t.Errorf("filter = %q, want bob kept", ra.maillist.FilterQuery())
	}
	if ra.uiApp.GetFocus() != tview.Primitive(ra.maillist) {
		t.Error("Enter must move focus to the mail list")
	}
	if title := ra.maillist.GetTitle(); !strings.Contains(title, "</bob>") {
		t.Errorf("mail list title = %q, want the filter in it", title)
	}
}

func TestFilter_escClearsAndRestoresFocus(t *testing.T) {
	ra := promptApp(t)
	ra.uiApp.SetFocus(ra.folders)
	ra.startFilter()
	typeInPrompt(ra, "bob")
	promptKey(ra, tcell.KeyEscape)

	if ra.promptOpen || ra.maillist.FilterQuery() != "" || ra.maillist.VisibleCount() != 3 {
		t.Errorf("after Esc: open %v, filter %q, rows %d; want closed, empty, 3",
			ra.promptOpen, ra.maillist.FilterQuery(), ra.maillist.VisibleCount())
	}
	if ra.uiApp.GetFocus() != tview.Primitive(ra.folders) {
		t.Error("Esc must return focus to the pane that had it")
	}
	if title := ra.maillist.GetTitle(); strings.Contains(title, "</") {
		t.Errorf("mail list title = %q still shows a filter", title)
	}
}

func TestFilter_escInMailListClears(t *testing.T) {
	ra := promptApp(t)
	ra.startFilter()
	typeInPrompt(ra, "bob")
	promptKey(ra, tcell.KeyEnter) // filter kept, focus in the mail list

	if ev := sendKey(ra, tcell.KeyEscape); ev != nil {
		t.Error("Esc with an active filter must be consumed")
	}
	if ra.maillist.FilterQuery() != "" {
		t.Errorf("filter = %q after Esc in the mail list, want it cleared", ra.maillist.FilterQuery())
	}
	if got := ra.uiApp.StatusBar.CrumbsText(); strings.Contains(got, "</") {
		t.Errorf("crumbs = %q still show a filter", got)
	}
}

func TestFilter_escWithoutFilterIsNotConsumed(t *testing.T) {
	ra := promptApp(t)
	ra.uiApp.SetFocus(ra.maillist)
	if ev := sendKey(ra, tcell.KeyEscape); ev == nil {
		t.Error("Esc with no filter must pass through untouched")
	}
}

func TestFilter_noMatchDisablesActions(t *testing.T) {
	ra := promptApp(t)
	ra.startFilter()
	typeInPrompt(ra, "zzz")
	promptKey(ra, tcell.KeyEnter)

	if ra.selectedEnv != nil || ra.currentMsg != nil {
		t.Fatal("a filter matching nothing must leave nothing selected")
	}
	ra.deleteMessage()
	if ra.uiApp.Pages.HasPage("delete-modal") {
		t.Error("delete offered with nothing on screen")
	}
}

func TestPrompt_globalKeysInert(t *testing.T) {
	ra := promptApp(t)
	ra.startFilter()
	for _, r := range []rune{'q', 'c', 'd', 's', 'l', '[', ']', '/', ':'} {
		if ev := sendRune(ra, r); ev == nil {
			t.Errorf("global shortcut %q fired while the prompt was open", r)
		}
	}
	if ev := sendKey(ra, tcell.KeyTab); ev == nil {
		t.Error("Tab cycled panes while the prompt was open")
	}
	if page, _ := ra.uiApp.Pages.GetFrontPage(); page != pageMain {
		t.Errorf("front page = %q, want main", page)
	}
	if !ra.promptOpen {
		t.Error("the prompt was closed by a global key")
	}
}

func TestPrompt_hintsSwap(t *testing.T) {
	ra := promptApp(t)
	ra.startFilter()
	hints := ra.uiApp.Header.HintsText()
	if !strings.Contains(hints, "Accept") || !strings.Contains(hints, "Cancel") || strings.Contains(hints, "Compose") {
		t.Errorf("hints while the prompt is open:\n%s", hints)
	}
	promptKey(ra, tcell.KeyEscape)
	if hints = ra.uiApp.Header.HintsText(); !strings.Contains(hints, "Compose") || strings.Contains(hints, "Accept") {
		t.Errorf("hints after the prompt closed:\n%s", hints)
	}
}

// Over the log page, / opens the prompt on the log page itself (it filters
// the log), never behind it on the main page; : does not open there.
func TestPrompt_notOpenedBehindLogPage(t *testing.T) {
	ra := promptApp(t)
	ra.toggleLog()
	ra.startCommand()
	if ra.promptOpen {
		t.Error("the : prompt opened over the log page")
	}
	sendRune(ra, '/')
	if !ra.promptOpen || ra.promptHost != ra.logPage {
		t.Error("/ did not open the prompt on the log page")
	}
}

func TestPrompt_rowAppearsAndDisappears(t *testing.T) {
	ra := promptApp(t)
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(120, 30)
	draw := func() string {
		ra.uiApp.Pages.SetRect(0, 0, 120, 30)
		ra.uiApp.Pages.Draw(screen)
		screen.Show()
		cells, w, h := screen.GetContents()
		var b strings.Builder
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if r := cells[y*w+x].Runes; len(r) > 0 {
					b.WriteRune(r[0])
				}
			}
			b.WriteRune('\n')
		}
		return b.String()
	}

	if out := draw(); strings.Contains(out, " filter ") {
		t.Errorf("prompt box visible before it was opened:\n%s", out)
	}
	ra.startFilter()
	typeInPrompt(ra, "bob")
	out := draw()
	if !strings.Contains(out, " filter ") || !strings.Contains(out, "/bob") {
		t.Errorf("open prompt not drawn:\n%s", out)
	}
	if !strings.Contains(out, "Account:") || !strings.Contains(out, "FOLDERS") {
		t.Errorf("header and panes must stay visible under the prompt:\n%s", out)
	}
	promptKey(ra, tcell.KeyEscape)
	if out := draw(); strings.Contains(out, " filter ") {
		t.Errorf("prompt box still drawn after closing:\n%s", out)
	}
}
