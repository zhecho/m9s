package ui_test

import (
	"strings"
	"testing"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/ui"
)

type promptRec struct {
	changes []string
	done    []string
	accepts []bool
}

func openPrompt(suggest func(string) string) (*ui.Prompt, *promptRec) {
	skin, _ := ui.LoadSkin("")
	p := ui.NewPrompt(skin)
	rec := &promptRec{}
	p.Open(ui.PromptConfig{
		Label:    "command",
		Prefix:   ":",
		Suggest:  suggest,
		OnChange: func(text string) { rec.changes = append(rec.changes, text) },
		OnDone: func(text string, accepted bool) {
			rec.done = append(rec.done, text)
			rec.accepts = append(rec.accepts, accepted)
		},
	})
	return p, rec
}

func pressKey(p *ui.Prompt, key tcell.Key) {
	p.InputHandler()(tcell.NewEventKey(key, 0, tcell.ModNone), func(tview.Primitive) {})
}

func typeText(p *ui.Prompt, text string) {
	for _, r := range text {
		p.InputHandler()(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone), func(tview.Primitive) {})
	}
}

func inboxSuggest(text string) string {
	if text == "" {
		return ""
	}
	return "INBOX"
}

func TestPrompt_typingAndChange(t *testing.T) {
	p, rec := openPrompt(nil)
	typeText(p, "in")
	if p.Text() != "in" {
		t.Errorf("Text() = %q, want in", p.Text())
	}
	if strings.Join(rec.changes, ",") != "i,in" {
		t.Errorf("OnChange calls = %v, want [i in]", rec.changes)
	}
}

func TestPrompt_ghostAndTab(t *testing.T) {
	p, rec := openPrompt(inboxSuggest)
	typeText(p, "in")
	if p.Ghost() != "BOX" {
		t.Errorf("Ghost() = %q, want BOX", p.Ghost())
	}
	pressKey(p, tcell.KeyTab)
	if p.Text() != "INBOX" {
		t.Errorf("Text() after Tab = %q, want INBOX", p.Text())
	}
	if p.Ghost() != "" {
		t.Errorf("Ghost() after Tab = %q, want empty", p.Ghost())
	}
	if last := rec.changes[len(rec.changes)-1]; last != "INBOX" {
		t.Errorf("last OnChange = %q, want INBOX", last)
	}
}

func TestPrompt_noGhostWhenSuggestionDoesNotExtendText(t *testing.T) {
	p, _ := openPrompt(func(string) string { return "Sent" })
	typeText(p, "in")
	if p.Ghost() != "" {
		t.Errorf("Ghost() = %q, want empty: Sent does not start with in", p.Ghost())
	}
}

func TestPrompt_enterAccepts(t *testing.T) {
	p, rec := openPrompt(nil)
	typeText(p, "in")
	pressKey(p, tcell.KeyEnter)
	if len(rec.done) != 1 || rec.done[0] != "in" || !rec.accepts[0] {
		t.Errorf("OnDone = %v %v, want [in] [true]", rec.done, rec.accepts)
	}
}

func TestPrompt_escCancels(t *testing.T) {
	p, rec := openPrompt(nil)
	pressKey(p, tcell.KeyEscape)
	if len(rec.done) != 1 || rec.done[0] != "" || rec.accepts[0] {
		t.Errorf("OnDone = %v %v, want [\"\"] [false]", rec.done, rec.accepts)
	}
}

func TestPrompt_openClears(t *testing.T) {
	p, _ := openPrompt(nil)
	typeText(p, "abc")
	p.Open(ui.PromptConfig{Label: "filter", Prefix: "/"})
	if p.Text() != "" {
		t.Errorf("Text() after reopening = %q, want empty", p.Text())
	}
	typeText(p, "x")          // nil callbacks must be tolerated
	pressKey(p, tcell.KeyTab) // nil Suggest
	pressKey(p, tcell.KeyEnter)
}

func TestPrompt_editingEdgeCases(t *testing.T) {
	p, rec := openPrompt(nil)
	pressKey(p, tcell.KeyBackspace2) // empty: nothing to delete
	pressKey(p, tcell.KeyBackspace)
	if len(rec.changes) != 0 {
		t.Errorf("Backspace on empty text fired OnChange: %v", rec.changes)
	}

	typeText(p, "ö")
	pressKey(p, tcell.KeyBackspace2)
	if p.Text() != "" {
		t.Errorf("Backspace after ö left %q; a whole rune must go", p.Text())
	}

	typeText(p, "ab")
	pressKey(p, tcell.KeyTab) // nil Suggest
	if p.Text() != "ab" {
		t.Errorf("Tab with no suggester changed the text to %q", p.Text())
	}

	p2, _ := openPrompt(func(string) string { return "" })
	typeText(p2, "ab")
	pressKey(p2, tcell.KeyTab)
	if p2.Text() != "ab" {
		t.Errorf("Tab with an empty suggestion changed the text to %q", p2.Text())
	}
}

func TestPrompt_swallowsOtherKeys(t *testing.T) {
	p, rec := openPrompt(nil)
	typeText(p, "q") // a global shortcut elsewhere; here it is just a letter
	for _, k := range []tcell.Key{tcell.KeyLeft, tcell.KeyRight, tcell.KeyUp, tcell.KeyDown, tcell.KeyF1, tcell.KeyCtrlC} {
		pressKey(p, k)
	}
	if p.Text() != "q" {
		t.Errorf("Text() = %q, want q", p.Text())
	}
	if len(rec.done) != 0 {
		t.Errorf("non-terminating keys ended the prompt: %v", rec.done)
	}
}

func TestPrompt_drawShowsPrefixTextAndGhost(t *testing.T) {
	p, _ := openPrompt(inboxSuggest)
	typeText(p, "in")
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(40, ui.PromptHeight)
	p.SetRect(0, 0, 40, ui.PromptHeight)
	p.Draw(screen)
	screen.Show()
	out := screenText(screen)
	if !strings.Contains(out, ":inBOX") {
		t.Errorf("prompt should draw prefix, typed text and ghost as \":inBOX\":\n%s", out)
	}
	if !strings.Contains(out, "command") {
		t.Errorf("prompt box should be titled with its label:\n%s", out)
	}
}
