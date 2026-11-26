//go:build unix

// Package instance keeps m9s to one running copy per user: the mail app
// holds an exclusive lock on a file in its state directory while it runs.
package instance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// RunningError means another m9s holds the lock. PID is its process id, 0
// when it could not be read.
type RunningError struct{ PID int }

func (e *RunningError) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("m9s is already running (pid %d); this one quits in 3 seconds.", e.PID)
	}
	return "m9s is already running; this one quits in 3 seconds."
}

// StateDir is where m9s keeps its state: $XDG_STATE_HOME/m9s, else
// ~/.local/state/m9s.
func StateDir() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "m9s")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "m9s")
}

// Acquire takes the lock on dir/m9s.lock (creating dir, 0700) and writes
// this process's pid into it. The OS frees the lock when the process ends,
// however it ends, so a lock is never stale; release frees it sooner. The
// file is opened close-on-exec (Go's default), so a restart that execs m9s
// takes the lock anew. Another m9s holding it is a *RunningError.
func Acquire(dir string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "m9s.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &RunningError{PID: readPID(path)}
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return func() { f.Close() }, nil
}

// readPID reads the pid the lock holder wrote; 0 when there is none.
func readPID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}
