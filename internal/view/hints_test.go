package view

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zhecho/m9s/internal/ui"
)

func hasHint(hints []ui.KeyHint, key, desc string) bool {
	for _, h := range hints {
		if h.Key == key && h.Desc == desc {
			return true
		}
	}
	return false
}

func TestGeneralHints_defaultKeymap(t *testing.T) {
	km, _ := ui.LoadKeyMap("")
	hints := generalHints(km)
	want := []ui.KeyHint{
		{Key: "j/k", Desc: "Navigate"}, {Key: "g/G", Desc: "Top/Bottom"}, {Key: "tab", Desc: "Next pane"},
		{Key: "[ ]", Desc: "Account"}, {Key: ":", Desc: "Command"}, {Key: "c", Desc: "Compose"}, {Key: "S", Desc: "Sync"},
		{Key: "l", Desc: "Logs"}, {Key: "/", Desc: "Filter"}, {Key: "?", Desc: "Help"}, {Key: "q", Desc: "Quit"},
	}
	if len(hints) != len(want) {
		t.Fatalf("generalHints has %d entries, want %d: %+v", len(hints), len(want), hints)
	}
	for i := range want {
		if hints[i] != want[i] {
			t.Errorf("generalHints[%d] = %+v, want %+v", i, hints[i], want[i])
		}
	}
}

func TestGeneralHints_followKeymap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keybindings.yaml")
	if err := os.WriteFile(path, []byte("compose: n\nswitch_account_next: \">\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	km, _ := ui.LoadKeyMap(path)
	hints := generalHints(km)
	if !hasHint(hints, "n", "Compose") || hasHint(hints, "c", "Compose") {
		t.Errorf("rebound compose must show as <n>: %+v", hints)
	}
	if !hasHint(hints, "[ >", "Account") {
		t.Errorf("rebound next-account must show in the pair: %+v", hints)
	}
}

func TestGeneralHints_unboundActionHasEmptyKey(t *testing.T) {
	hints := generalHints(ui.KeyMap{}) // nothing bound
	if !hasHint(hints, "", "Compose") || !hasHint(hints, "", "Account") {
		t.Errorf("unbound actions must carry an empty key so the header skips them: %+v", hints)
	}
	if !hasHint(hints, "j/k", "Navigate") {
		t.Errorf("literal keys do not depend on the keymap: %+v", hints)
	}
}

func TestPaneHints(t *testing.T) {
	km, _ := ui.LoadKeyMap("")

	folders := paneHints(km, paneFolders, false)
	if len(folders) != 1 || !hasHint(folders, "enter", "Open") {
		t.Errorf("folder pane hints = %+v, want exactly enter/Open", folders)
	}

	for _, pane := range []paneKind{paneMailList, panePreview} {
		hints := paneHints(km, pane, false)
		if len(hints) != 7 || !hasHint(hints, "r", "Reply") || !hasHint(hints, "R", "Reply all") ||
			!hasHint(hints, "d", "Delete") || !hasHint(hints, "u", "Unread") || !hasHint(hints, "b", "Browser") {
			t.Errorf("pane %d hints = %+v, want r, R, d, u, b, s, o", pane, hints)
		}
	}
}

func TestHints_neverAdvertiseDeadKeys(t *testing.T) {
	km, _ := ui.LoadKeyMap("")
	all := generalHints(km)
	for _, pane := range []paneKind{paneFolders, paneMailList, panePreview} {
		all = append(all, paneHints(km, pane, false)...)
	}
	for _, h := range all {
		switch h.Desc {
		case "Scroll", "Search":
			t.Errorf("hint %+v advertises a key that does nothing", h)
		}
	}
}

func TestPromptHints(t *testing.T) {
	cmd := promptHints(true)
	if len(cmd) != 3 || !hasHint(cmd, "enter", "Accept") || !hasHint(cmd, "esc", "Cancel") || !hasHint(cmd, "tab", "Complete") {
		t.Errorf("command prompt hints = %+v", cmd)
	}
	filter := promptHints(false)
	if len(filter) != 2 || hasHint(filter, "tab", "Complete") {
		t.Errorf("filter prompt hints = %+v, want only Accept and Cancel", filter)
	}
}

func TestPaneHints_saveAndOpen(t *testing.T) {
	km, _ := ui.LoadKeyMap("")
	for _, pane := range []paneKind{paneMailList, panePreview} {
		hints := paneHints(km, pane, false)
		if !hasHint(hints, "s", "Save") || !hasHint(hints, "o", "Open") {
			t.Errorf("pane %d hints lack s Save / o Open: %+v", pane, hints)
		}
	}
	if hints := paneHints(km, paneFolders, false); hasHint(hints, "s", "Save") {
		t.Error("the folder pane offers to save attachments")
	}
}
