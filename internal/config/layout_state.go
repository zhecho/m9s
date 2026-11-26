package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// LayoutState is UI layout the user changed at runtime (e.g. by dragging a
// border). It is remembered between runs and is not part of config.yaml.
type LayoutState struct {
	MailListRows int `json:"mail_list_rows,omitempty"`
	LogPaneRows  int `json:"log_pane_rows,omitempty"`
}

// UpdateLayoutState changes the saved layout with change and saves it,
// keeping what change does not touch (the other pane's size).
func UpdateLayoutState(path string, change func(*LayoutState)) error {
	st := LoadLayoutState(path)
	change(&st)
	return SaveLayoutState(path, st)
}

// DefaultLayoutStatePath is ~/.local/state/m9s/layout.json, or under
// $XDG_STATE_HOME when set.
func DefaultLayoutStatePath() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "m9s", "layout.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "m9s", "layout.json")
}

// LoadLayoutState reads the saved layout. A missing or unreadable file
// yields an empty state: layout is a convenience, never a reason to fail.
func LoadLayoutState(path string) LayoutState {
	var st LayoutState
	data, err := os.ReadFile(path)
	if err != nil {
		return LayoutState{}
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return LayoutState{}
	}
	return st
}

// SaveLayoutState writes the layout, creating the directory if needed.
func SaveLayoutState(path string, st LayoutState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path) // never leave a half-written file behind
}
