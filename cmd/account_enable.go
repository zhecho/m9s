package cmd

import (
	"fmt"
	"slices"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/ui"
)

// disabledAccount is the error for name when it is an account with
// enable: false, nil otherwise.
func disabledAccount(cfg *config.Config, name string) error {
	if slices.Contains(cfg.Disabled, name) {
		return fmt.Errorf("account %q is disabled (enable: false in the config)", name)
	}
	return nil
}

// disabledLogLines tell the log which accounts were left out.
func disabledLogLines(cfg *config.Config) []ui.Entry {
	var lines []ui.Entry
	for _, name := range cfg.Disabled {
		lines = append(lines, ui.Entry{Level: ui.LevelInfo, Msg: fmt.Sprintf("[%s] disabled (enable: false): not loaded", name)})
	}
	return lines
}
