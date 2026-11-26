//go:build !unix

// Package instance keeps m9s to one running copy per user. Off Unix there
// is no lock: m9s is built for macOS and Linux.
package instance

import (
	"os"
	"path/filepath"
)

// RunningError means another m9s holds the lock (never off Unix).
type RunningError struct{ PID int }

func (e *RunningError) Error() string { return "m9s is already running" }

// StateDir is where m9s keeps its state.
func StateDir() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "m9s")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "m9s")
}

// Acquire takes no lock off Unix.
func Acquire(dir string) (release func(), err error) {
	return func() {}, os.MkdirAll(dir, 0o700)
}
