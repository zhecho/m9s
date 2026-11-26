package view

import (
	"strings"
	"testing"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/ui"
)

func TestResolveCommand_accountNamedHelpBeatsHelp(t *testing.T) {
	got, err := resolveCommand("help", []string{"help"}, nil)
	if err != nil || got.Kind != cmdAccount {
		t.Errorf("got %+v, %v; want the account", got, err)
	}
}

func TestHelpSections_listEveryBoundAction(t *testing.T) {
	km, _ := ui.LoadKeyMap("")
	var all []string
	for _, s := range helpSections(km) {
		for _, e := range s.Entries {
			all = append(all, "<"+e.Key+"> "+e.Desc)
		}
	}
	text := strings.Join(all, "\n")
	for _, want := range []string{
		"<c> Compose", "<r> Reply", "<R> Reply all", "<d> Delete", "<u> Mark unread",
		"<S> Sync", "<l> Logs", "</> Filter", "<:> Command", "<[> Previous account",
		"<]> Next account", "<b> Open in browser", "<s> Save attachments",
		"<o> Open attachment", "<e> Edit draft", "<*> Star", "<a> Archive", "<L> Labels",
		"<f> Forward", "<m> Move to folder", "<:help> This help", "<?> Help", "<:config> Edit config", "<q> Quit",
		// everything else the README documents
		"<space> Select / unselect", "<shift-click> Select / unselect", "<A> Select all shown / none",
		"<m d> Move / delete selected", "<a u *> Archive / unread / star", "<esc> Clear selection", "<d> Delete forever / all",
		"<enter> Keep filter", "<\\> Search the server", "<esc> Clear filter / search",
		"<enter> Move / save / open", "<tab ←/→> Choose button", "<q/esc/l> Close",
		"<j/k g/G> Scroll", "<:q :quit> Quit", "<shift-tab> Previous message",
		"<space> Toggle label",
		"<tab> Contents / text", "<j/k> Choose section",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("help lacks %q", want)
		}
	}
}

func TestHelpSections_followRebindingAndSkipUnbound(t *testing.T) {
	km, _ := ui.LoadKeyMap("")
	delete(km, 'c')
	km['x'] = ui.ActionReply
	delete(km, 'r')
	for _, s := range helpSections(km) {
		for _, e := range s.Entries {
			if e.Desc == "Compose" {
				t.Errorf("unbound compose still listed as <%s>", e.Key)
			}
			if e.Desc == "Reply" && e.Key != "x" {
				t.Errorf("reply listed as <%s>, want <x>", e.Key)
			}
		}
	}
}

func TestLayoutHelp_columnsFollowWidth(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	km, _ := ui.LoadKeyMap("")
	sections := helpSections(km)

	narrow, _ := layoutHelp(sections, skin, 20)
	wide, _ := layoutHelp(sections, skin, 200)
	if len(wide) >= len(narrow) {
		t.Errorf("wide layout has %d rows, narrow %d: want fewer", len(wide), len(narrow))
	}
	// One column: every title starts a line.
	for _, s := range sections {
		found := false
		for _, l := range narrow {
			if l == s.Title {
				found = true
			}
		}
		if !found {
			t.Errorf("narrow layout lacks title row %q", s.Title)
		}
	}
	// The first wide row holds the first section's title and another one.
	if !strings.HasPrefix(wide[0], "GENERAL") || len(strings.Fields(wide[0])) < 2 {
		t.Errorf("wide first row = %q, want several titles", wide[0])
	}
}

func TestLayoutHelp_keysUseHeadingColor(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	km, _ := ui.LoadKeyMap("")
	_, tagged := layoutHelp(helpSections(km), skin, 30)
	want := "[" + skin.ColorName("heading") + "::b]<j/k ↓/↑>"
	if !strings.Contains(strings.Join(tagged, "\n"), want) {
		t.Errorf("keys are not drawn in the heading colour %s", skin.ColorName("heading"))
	}
}

func TestHelpCommand_opensAndClosesHelp(t *testing.T) {
	ra := commandApp(t)
	ra.uiApp.SetFocus(ra.maillist)
	ra.startCommand()
	typeInPrompt(ra, "help")
	promptKey(ra, tcell.KeyEnter)

	if page, _ := ra.uiApp.Pages.GetFrontPage(); page != pageHelp {
		t.Fatalf("front page = %q, want help", page)
	}
	if !strings.Contains(ra.help.Text(), "Compose") {
		t.Errorf("help text lacks shortcuts:\n%s", ra.help.Text())
	}
	// Global shortcuts are off while help is open: q closes help, not m9s.
	ev := tcell.NewEventKey(tcell.KeyRune, 'q', tcell.ModNone)
	if sendRune(ra, 'q') == nil {
		t.Fatal("q was swallowed by the global keys")
	}
	ra.help.body.InputHandler()(ev, func(tview.Primitive) {})
	if page, _ := ra.uiApp.Pages.GetFrontPage(); page != pageMain {
		t.Errorf("after q front page = %q, want main", page)
	}
	if ra.uiApp.GetFocus() != tview.Primitive(ra.maillist) {
		t.Error("focus must return to the pane that had it")
	}
}

func TestHelpView_drawsLogoAndShortcuts(t *testing.T) {
	ra := commandApp(t)
	ra.showHelp()
	lines := drawMainPage(t, ra, 120, 60) // the contents pane narrows the columns
	screen := strings.Join(lines, "\n")
	for _, want := range []string{ui.Logo()[1], "HELP", "GENERAL", "MESSAGES", "<:help>", "<:config>", "<?>"} {
		if !strings.Contains(screen, want) {
			t.Errorf("help window lacks %q:\n%s", want, screen)
		}
	}
}

func TestHelpView_dragSelectStaysInThePane(t *testing.T) {
	ra := commandApp(t)
	ra.showHelp()
	drawMainPage(t, ra, 120, 40)
	regions := ra.selector.Regions()
	inner := func(x, y, w, h int) ui.Rect { return ui.Rect{X: x, Y: y, W: w, H: h} }
	want := []ui.Rect{inner(ra.help.body.GetInnerRect()), inner(ra.help.toc.GetInnerRect())}
	if len(regions) != 2 || regions[0] != want[0] || regions[1] != want[1] {
		t.Errorf("help regions = %v, want the text and the contents %v", regions, want)
	}
}
