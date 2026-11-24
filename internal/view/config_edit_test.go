package view

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tcell "github.com/derailed/tcell/v2"
	"github.com/zhecho/m9s/internal/ui"
)

const validConfig = "accounts:\n  - name: work\n    imap:\n      host: imap.example.com\n      user: me\n"

// configApp is a RootApp whose config file is a temp copy of body; edits
// returns the "editor" that writes each of its texts in turn, one per run.
func configApp(t *testing.T, body string) (ra *RootApp, path string) {
	t.Helper()
	ra = commandApp(t)
	path = filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ra.SetConfigPath(path)
	ra.cfg.UI.Editor = "true" // the editor is faked below; this only must be set
	return ra, path
}

// fakeEdits makes each editor run replace the file with the next text; it
// returns the edited paths, one per run.
func fakeEdits(ra *RootApp, texts ...string) *[]string {
	var runs []string
	ra.editFile = func(path string) error {
		runs = append(runs, path)
		if i := len(runs) - 1; i < len(texts) {
			return os.WriteFile(path, []byte(texts[i]), 0o600)
		}
		return nil
	}
	return &runs
}

func runColon(ra *RootApp, text string) {
	ra.startCommand()
	typeInPrompt(ra, text)
	promptKey(ra, tcell.KeyEnter)
}

func TestQuestionMarkOpensHelp(t *testing.T) {
	ra := commandApp(t)
	ra.uiApp.SetFocus(ra.maillist)
	if ev := sendRune(ra, '?'); ev != nil {
		t.Error("? was not consumed")
	}
	if page, _ := ra.uiApp.Pages.GetFrontPage(); page != pageHelp {
		t.Fatalf("front page = %q, want help", page)
	}
	ra.closeHelp()
	if ra.uiApp.GetFocus() != ra.maillist {
		t.Error("focus must return to the mail list")
	}
}

func TestQuestionMarkIsRebindable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keybindings.yaml")
	if err := os.WriteFile(path, []byte("help: H\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	km, _ := ui.LoadKeyMap(path)
	if km['H'] != ui.ActionHelp || km['?'] == ui.ActionHelp {
		t.Errorf("help not moved to H: %v / %v", km['H'], km['?'])
	}
}

func TestResolveCommand_config(t *testing.T) {
	if got, err := resolveCommand("config", cmdAccounts, cmdFolders()); err != nil || got.Kind != cmdConfig {
		t.Errorf("config = %+v, %v", got, err)
	}
	if got := suggestCommand("con", cmdAccounts, cmdFolders()); got != "config" {
		t.Errorf("suggest(con) = %q, want config", got)
	}
}

func TestConfigCommand_validEditSavesAndRestarts(t *testing.T) {
	ra, path := configApp(t, validConfig)
	edited := validConfig + "ui:\n  date_format: \"02 Jan\"\n"
	runs := fakeEdits(ra, edited)

	runColon(ra, "config")

	if len(*runs) != 1 || (*runs)[0] == path {
		t.Fatalf("editor runs = %v: must edit a copy, not the live file", *runs)
	}
	if got, _ := os.ReadFile(path); string(got) != edited {
		t.Errorf("config on disk = %q", got)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 kept", info.Mode().Perm())
	}
	if !ra.RestartRequested() {
		t.Error("no restart after a valid edit")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".m9s-config-*")); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
}

func TestConfigCommand_unchangedDoesNotRestart(t *testing.T) {
	ra, path := configApp(t, validConfig)
	fakeEdits(ra) // saves nothing new
	runColon(ra, "config")
	if ra.RestartRequested() {
		t.Error("restart although nothing changed")
	}
	if !strings.Contains(ra.uiApp.StatusBar.FlashText(), "unchanged") {
		t.Errorf("status = %q", ra.uiApp.StatusBar.FlashText())
	}
	if got, _ := os.ReadFile(path); string(got) != validConfig {
		t.Errorf("config on disk changed: %q", got)
	}
}

// A bad edit never reaches the live file: m9s says why and offers to edit
// the same text again; the second, valid edit is saved.
func TestConfigCommand_invalidEditOffersEditAgain(t *testing.T) {
	ra, path := configApp(t, validConfig)
	broken := "accounts: [oops\n"
	runs := fakeEdits(ra, broken, validConfig+"# fixed\n")

	runColon(ra, "config")

	if ra.RestartRequested() {
		t.Fatal("restart with a broken config")
	}
	if got, _ := os.ReadFile(path); string(got) != validConfig {
		t.Fatalf("live config overwritten with a broken one: %q", got)
	}
	if !strings.Contains(ra.testDeleteModalText, "parsing config") {
		t.Errorf("dialog text = %q", ra.testDeleteModalText)
	}
	ra.testDeleteModalDone(0, "Edit again")

	if len(*runs) != 2 || (*runs)[1] != (*runs)[0] {
		t.Fatalf("editor runs = %v, want the same copy twice", *runs)
	}
	if got, _ := os.ReadFile(path); string(got) != validConfig+"# fixed\n" {
		t.Errorf("config on disk = %q", got)
	}
	if !ra.RestartRequested() {
		t.Error("no restart after the fix")
	}
}

func TestConfigCommand_invalidEditDiscard(t *testing.T) {
	ra, path := configApp(t, validConfig)
	fakeEdits(ra, "accounts: []\n") // valid YAML, but nothing m9s can start with

	runColon(ra, "config")
	if !strings.Contains(ra.testDeleteModalText, "no accounts") {
		t.Errorf("dialog text = %q", ra.testDeleteModalText)
	}
	ra.testDeleteModalDone(1, "Discard")

	if ra.RestartRequested() {
		t.Error("restart after discard")
	}
	if got, _ := os.ReadFile(path); string(got) != validConfig {
		t.Errorf("config on disk = %q", got)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".m9s-config-*")); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
}

// The edit goes to the file a symlinked config points at; the link stays.
func TestConfigCommand_followsSymlink(t *testing.T) {
	ra, real := configApp(t, validConfig)
	link := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	ra.SetConfigPath(link)
	fakeEdits(ra, validConfig+"# via link\n")

	runColon(ra, "config")

	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link replaced by a file: %v %v", fi, err)
	}
	if got, _ := os.ReadFile(real); string(got) != validConfig+"# via link\n" {
		t.Errorf("target = %q", got)
	}
}

func TestConfigCommand_editorFailureKeepsConfig(t *testing.T) {
	ra, path := configApp(t, validConfig)
	ra.editFile = func(p string) error {
		_ = os.WriteFile(p, []byte("half: written"), 0o600)
		return os.ErrInvalid // the editor exited with an error (vim :cq)
	}
	runColon(ra, "config")
	if ra.RestartRequested() {
		t.Error("restart after the editor failed")
	}
	if got, _ := os.ReadFile(path); string(got) != validConfig {
		t.Errorf("config on disk = %q", got)
	}
	if !strings.Contains(ra.uiApp.StatusBar.FlashText(), "not changed") {
		t.Errorf("status = %q", ra.uiApp.StatusBar.FlashText())
	}
}
