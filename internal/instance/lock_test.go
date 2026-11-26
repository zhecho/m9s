//go:build unix

package instance

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A second m9s is refused while the first holds the lock, and learns its
// pid. A flock belongs to an open file, so this holds within one process
// too.
func TestAcquire_secondIsRefused(t *testing.T) {
	dir := t.TempDir()
	release, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = Acquire(dir)
	var running *RunningError
	if !errors.As(err, &running) || running.PID != os.Getpid() {
		t.Fatalf("second Acquire = %v, want *RunningError with pid %d", err, os.Getpid())
	}
}

// Once the first lets go (it quit, crashed, or :config restarts it), the
// next one gets the lock.
func TestAcquire_againAfterRelease(t *testing.T) {
	dir := t.TempDir()
	release, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	release()
	release2, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	release2()
}

// The lock file is private, in a private directory made when missing.
func TestAcquire_makesThePrivateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "m9s")
	release, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, %v; want 0700", st.Mode().Perm(), err)
	}
	if st, err := os.Stat(filepath.Join(dir, "m9s.lock")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("lock file mode = %v, %v; want 0600", st.Mode().Perm(), err)
	}
}

func TestRunningError_message(t *testing.T) {
	if got := (&RunningError{PID: 1234}).Error(); got != "m9s is already running (pid 1234); this one quits in 3 seconds." {
		t.Errorf("with pid: %q", got)
	}
	if got := (&RunningError{}).Error(); got != "m9s is already running; this one quits in 3 seconds." {
		t.Errorf("without pid: %q", got)
	}
}

func TestStateDir_followsXDG(t *testing.T) {
	d := t.TempDir()
	t.Setenv("XDG_STATE_HOME", d)
	if got := StateDir(); got != filepath.Join(d, "m9s") {
		t.Errorf("StateDir = %q", got)
	}
	t.Setenv("XDG_STATE_HOME", "")
	home, _ := os.UserHomeDir()
	if got := StateDir(); got != filepath.Join(home, ".local", "state", "m9s") {
		t.Errorf("StateDir without XDG = %q", got)
	}
}
