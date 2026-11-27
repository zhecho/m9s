package ui_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/ui"
)

// ---- NewApp ----

func TestNewApp(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	app := ui.NewApp(skin, ui.KeyMap{})
	if app == nil {
		t.Fatal("NewApp returned nil")
	}
	if app.Pages == nil {
		t.Error("Pages must not be nil")
	}
	if app.StatusBar == nil {
		t.Error("StatusBar must not be nil")
	}
	if app.Header == nil {
		t.Error("Header must not be nil")
	}
}

// ---- KeyMap ----

func TestLoadKeyMap_defaults(t *testing.T) {
	km, err := ui.LoadKeyMap("")
	if err != nil {
		t.Fatalf("LoadKeyMap: %v", err)
	}
	if len(km) == 0 {
		t.Error("default KeyMap should not be empty")
	}
}

func TestLoadKeyMap_fromFile(t *testing.T) {
	f := t.TempDir() + "/keys.yaml"
	if err := os.WriteFile(f, []byte("compose: \"x\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	km, err := ui.LoadKeyMap(f)
	if err != nil {
		t.Fatalf("LoadKeyMap from file: %v", err)
	}
	if km['x'] != ui.ActionCompose {
		t.Errorf("want ActionCompose for 'x', got %v", km['x'])
	}
}

func TestKeyMap_LookupRune(t *testing.T) {
	km, _ := ui.LoadKeyMap("")
	ev := tcell.NewEventKey(tcell.KeyRune, 'j', tcell.ModNone)
	action := km.Lookup(ev)
	if action != ui.ActionNavigateDown {
		t.Errorf("want ActionNavigateDown for 'j', got %v", action)
	}
}

func TestKeyMap_LookupTab(t *testing.T) {
	km, _ := ui.LoadKeyMap("")
	ev := tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)
	if km.Lookup(ev) != ui.ActionTabCycle {
		t.Error("Tab should return ActionTabCycle")
	}
}

func TestKeyMap_LookupShiftTab(t *testing.T) {
	km, _ := ui.LoadKeyMap("")
	ev := tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModShift)
	if km.Lookup(ev) != ui.ActionTabCycleBack {
		t.Error("shift-tab should return ActionTabCycleBack")
	}
}

func TestKeyMap_LookupEnter(t *testing.T) {
	km, _ := ui.LoadKeyMap("")
	ev := tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)
	if km.Lookup(ev) != ui.ActionOpen {
		t.Error("Enter should return ActionOpen")
	}
}

func TestKeyMap_LookupUnbound(t *testing.T) {
	km, _ := ui.LoadKeyMap("")
	ev := tcell.NewEventKey(tcell.KeyRune, 'Z', tcell.ModNone)
	if km.Lookup(ev) != "" {
		t.Error("unbound key should return empty action")
	}
}

// ---- Logger ----

func TestLogger_storesLevelTimeAndMessage(t *testing.T) {
	l := &ui.Logger{}
	before := time.Now()
	l.Warn("hello %s", "world")
	es := l.Entries()
	if len(es) != 1 || es[0].Level != ui.LevelWarn || es[0].Msg != "hello world" {
		t.Fatalf("entries = %+v", es)
	}
	if es[0].Time.Before(before) || es[0].Time.After(time.Now()) {
		t.Errorf("time = %v", es[0].Time)
	}
}

func TestLogger_percentInArgIsLiteral(t *testing.T) {
	l := &ui.Logger{}
	l.Error("%s", "disk 100% full")
	if got := l.Entries()[0].Msg; got != "disk 100% full" {
		t.Errorf("msg = %q", got)
	}
}

func TestLogger_OnUpdate(t *testing.T) {
	l := &ui.Logger{}
	called := 0
	l.OnUpdate(func() { called++ })
	l.Info("test")
	if called != 1 {
		t.Errorf("want callback called once, got %d", called)
	}
	l.Info("test2")
	if called != 2 {
		t.Errorf("want callback called twice, got %d", called)
	}
}

func TestFormatEntry_dateLevelAndColor(t *testing.T) {
	s, _ := ui.LoadSkin("") // solarized-dark
	at := time.Date(2026, 11, 15, 15, 4, 5, 0, time.Local)
	cases := []struct {
		lv   ui.Level
		want string
	}{
		{ui.LevelInfo, "[#586e75::]2026-11-15 15:04:05[-:-:-] [#839496::]INFO  connected[-:-:-]"},
		{ui.LevelWarn, "[#586e75::]2026-11-15 15:04:05[-:-:-] [#b58900::]WARN  connected[-:-:-]"},
		{ui.LevelError, "[#586e75::]2026-11-15 15:04:05[-:-:-] [#dc322f::]ERROR connected[-:-:-]"},
	}
	for _, c := range cases {
		if got := ui.FormatEntry(ui.Entry{Time: at, Level: c.lv, Msg: "connected"}, s); got != c.want {
			t.Errorf("%v: got %q, want %q", c.lv, got, c.want)
		}
	}
}

func TestFormatEntry_escapesBrackets(t *testing.T) {
	s, _ := ui.LoadSkin("")
	row := ui.FormatEntry(ui.Entry{Time: time.Now(), Level: ui.LevelError, Msg: "[work] dial tcp [::]:993 [-:-:-] [AUTH] 100%"}, s)
	tv := tview.NewTextView().SetDynamicColors(true)
	tv.SetText(row)
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	screen.SetSize(120, 1)
	tv.SetRect(0, 0, 120, 1)
	tv.Draw(screen)
	screen.Show()
	if got := screenText(screen); !strings.Contains(got, "ERROR [work] dial tcp [::]:993 [-:-:-] [AUTH] 100%") {
		t.Errorf("rendered %q", got)
	}
}

func TestFormatEntry_warnFallsBackToForeground(t *testing.T) {
	s := &ui.Skin{Colors: map[string]string{"foreground": "#111111", "dim": "#222222"}}
	row := ui.FormatEntry(ui.Entry{Time: time.Now(), Level: ui.LevelWarn, Msg: "x"}, s)
	if !strings.Contains(row, "[#111111::]WARN  x") {
		t.Errorf("row = %q", row)
	}
}

func TestBuiltinThemesHaveWarn(t *testing.T) {
	for _, name := range ui.BuiltinThemes() {
		s, warns := ui.ResolveTheme(name, "", nil)
		raw, _ := ui.BuiltinThemeYAML(name)
		if len(warns) > 0 || !strings.Contains(string(raw), "warn:") || s.ColorName("warn") == "default" {
			t.Errorf("%s: no warn role", name)
		}
	}
}

func TestKeyMap_KeyFor(t *testing.T) {
	km, _ := ui.LoadKeyMap("")
	if got := km.KeyFor(ui.ActionCompose); got != "c" {
		t.Errorf("KeyFor(compose) = %q, want c", got)
	}
	if got := km.KeyFor(ui.ActionCommandBar); got != ":" {
		t.Errorf("KeyFor(command_bar) = %q, want :", got)
	}
	if got := km.KeyFor(ui.Action("nope")); got != "" {
		t.Errorf("KeyFor(unbound) = %q, want empty", got)
	}
}

func TestKeyMap_KeyFor_rebound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keybindings.yaml")
	if err := os.WriteFile(path, []byte("compose: n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	km, _ := ui.LoadKeyMap(path)
	if got := km.KeyFor(ui.ActionCompose); got != "n" {
		t.Errorf("KeyFor(compose) after rebind = %q, want n", got)
	}
}

func TestLoadKeyMap_bOpensTheBrowserAndCanBeRebound(t *testing.T) {
	km, _ := ui.LoadKeyMap("")
	if got := km.Lookup(tcell.NewEventKey(tcell.KeyRune, 'b', tcell.ModNone)); got != ui.ActionOpenBrowser {
		t.Errorf("b = %q, want %q", got, ui.ActionOpenBrowser)
	}
	path := filepath.Join(t.TempDir(), "keybindings.yaml")
	if err := os.WriteFile(path, []byte("open_browser: v\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	km, _ = ui.LoadKeyMap(path)
	if km.KeyFor(ui.ActionOpenBrowser) != "v" {
		t.Errorf("rebound key = %q, want v", km.KeyFor(ui.ActionOpenBrowser))
	}
}
