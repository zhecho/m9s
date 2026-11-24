package view

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/ui"
)

// SetConfigPath names the config file :config edits.
func (ra *RootApp) SetConfigPath(path string) { ra.configPath = path }

// SetReadme gives the help window the README to show below the shortcuts.
func (ra *RootApp) SetReadme(text string) { ra.help.SetReadme(text) }

// RestartRequested reports whether :config saved a new config: once the app
// has stopped, m9s starts again with it.
func (ra *RootApp) RestartRequested() bool { return ra.restart }

// editorCommand is ui.editor, else $EDITOR, or "" when neither is set.
func (ra *RootApp) editorCommand() string {
	if editor := os.ExpandEnv(ra.cfg.UI.Editor); editor != "" {
		return editor
	}
	return os.Getenv("EDITOR")
}

// editInTerminal runs the editor on path with the screen handed over to it.
// An editor that exits with an error (vim :cq) is an error.
func (ra *RootApp) editInTerminal(path string) error {
	parts := strings.Fields(ra.editorCommand())
	if len(parts) == 0 {
		return errors.New("no editor — set $EDITOR or ui.editor in config.yaml")
	}
	var runErr error
	if !ra.uiApp.Suspend(func() {
		cmd := exec.Command(parts[0], append(parts[1:], path)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		runErr = cmd.Run()
	}) {
		return errors.New("the screen could not be handed to the editor")
	}
	return runErr
}

// configEdit is one :config session: the editor works on a copy next to the
// config file, which is only replaced by a copy m9s can start with.
type configEdit struct {
	target string      // the config file, symlinks resolved
	copy   string      // what the editor edits
	orig   []byte      // the config as it was
	mode   fs.FileMode // the config file's permissions, kept
}

// editConfig opens the config file in the editor. A changed, valid config
// replaces the file and m9s restarts with it; an invalid one never reaches
// the file, and the user is offered to edit it again or to discard it.
func (ra *RootApp) editConfig() {
	if ra.configPath == "" {
		ra.uiApp.StatusBar.Error("no config file to edit")
		return
	}
	if ra.editorCommand() == "" {
		ra.uiApp.StatusBar.Error("no editor — set $EDITOR or ui.editor in config.yaml")
		return
	}
	ce, err := newConfigEdit(ra.configPath)
	if err != nil {
		ra.uiApp.StatusBar.Error("config: " + err.Error())
		return
	}
	ra.continueConfigEdit(ce)
}

func newConfigEdit(path string) (*configEdit, error) {
	target, err := filepath.EvalSymlinks(path) // a linked config stays linked
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	orig, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	// Next to the file, so the swap is one rename; .yaml for the editor.
	f, err := os.CreateTemp(filepath.Dir(target), ".m9s-config-*.yaml")
	if err != nil {
		return nil, err
	}
	ce := &configEdit{target: target, copy: f.Name(), orig: orig, mode: info.Mode().Perm()}
	_, err = f.Write(orig)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(ce.copy, ce.mode)
	}
	if err != nil {
		os.Remove(ce.copy)
		return nil, err
	}
	return ce, nil
}

// continueConfigEdit runs the editor on ce's copy and acts on the result.
func (ra *RootApp) continueConfigEdit(ce *configEdit) {
	if err := ra.editFile(ce.copy); err != nil {
		os.Remove(ce.copy)
		ra.uiApp.StatusBar.Error("config not changed: editor: " + err.Error())
		return
	}
	data, err := os.ReadFile(ce.copy)
	if err != nil {
		os.Remove(ce.copy)
		ra.uiApp.StatusBar.Error("config not changed: " + err.Error())
		return
	}
	if bytes.Equal(data, ce.orig) {
		os.Remove(ce.copy)
		ra.uiApp.StatusBar.Info("config unchanged")
		return
	}
	if err := checkConfig(data); err != nil {
		text := fmt.Sprintf("Config not saved:\n\n%v\n\nm9s keeps running with the old config. Esc discards the edit.", err)
		ra.showDeleteDialog(text, []ui.DialogButton{{Label: "Edit again"}, {Label: "Discard", Danger: true}}, 0,
			func(label string) {
				if label == "Edit again" {
					ra.continueConfigEdit(ce)
					return
				}
				os.Remove(ce.copy)
				ra.uiApp.StatusBar.Info("config edit discarded")
			})
		return
	}
	if err := os.Rename(ce.copy, ce.target); err != nil {
		os.Remove(ce.copy)
		ra.uiApp.StatusBar.Error("config not saved: " + err.Error())
		return
	}
	ui.AppLog.Info("config saved to %s: restarting m9s", ce.target)
	ra.restart = true
	ra.uiApp.Stop() // after the editor: Stop while suspended would panic
}

// checkConfig reports why m9s could not start with data.
func checkConfig(data []byte) error {
	cfg, err := config.LoadFromBytes(data)
	if err != nil {
		return err
	}
	if len(cfg.Accounts) == 0 {
		return errors.New("no accounts in config")
	}
	return nil
}
