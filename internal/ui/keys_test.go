package ui_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/ui"
)

func TestSearchServerBindingRemoved(t *testing.T) {
	km, _ := ui.LoadKeyMap("")
	if a := km['S']; a != ui.ActionSync {
		t.Errorf("S = %q, want sync (the old server-search binding is gone)", a)
	}
	if a := km['L']; a != ui.ActionLabels {
		t.Errorf("L = %q, want labels", a)
	}
}

func TestDefaultKeys_attachmentsAndSync(t *testing.T) {
	km, _ := ui.LoadKeyMap("")
	for a, want := range map[ui.Action]string{ui.ActionSync: "S", ui.ActionSaveAttachments: "s", ui.ActionOpenAttachment: "o"} {
		if got := km.KeyFor(a); got != want {
			t.Errorf("%s on %q, want %q", a, got, want)
		}
	}
}

func keyFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "keys.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func loggedWarn(want string) bool {
	for _, e := range ui.AppLog.Entries() {
		if e.Level == ui.LevelWarn && strings.Contains(e.Msg, want) {
			return true
		}
	}
	return false
}

func TestLoadKeyMap_overrideDisplacesDefault(t *testing.T) {
	km, _ := ui.LoadKeyMap(keyFile(t, "sync: s\n"))
	if km.KeyFor(ui.ActionSync) != "s" || km.KeyFor(ui.ActionSaveAttachments) != "" {
		t.Errorf("sync on %q, save on %q; want sync on s and save unbound",
			km.KeyFor(ui.ActionSync), km.KeyFor(ui.ActionSaveAttachments))
	}
	if !loggedWarn("key s: sync replaces save_attachments") {
		t.Error("the displaced binding is not in the log")
	}
}

func TestLoadKeyMap_unknownActionIgnored(t *testing.T) {
	km, _ := ui.LoadKeyMap(keyFile(t, "folder_toggle: o\n"))
	if km.KeyFor(ui.ActionOpenAttachment) != "o" {
		t.Errorf("open on %q, want o: an unknown action must not take a key", km.KeyFor(ui.ActionOpenAttachment))
	}
	if !loggedWarn(`key file: unknown action "folder_toggle"`) {
		t.Error("the unknown action is not in the log")
	}
}
