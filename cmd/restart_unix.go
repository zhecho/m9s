//go:build unix

package cmd

import (
	"fmt"
	"os"
	"syscall"
)

// restartSelf replaces this process with a new m9s: the same binary, flags
// and environment, so it starts with the config :config just saved.
func restartSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("restart m9s: %w", err)
	}
	return fmt.Errorf("restart m9s: %w", syscall.Exec(exe, os.Args, os.Environ()))
}
