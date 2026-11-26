//go:build unix

package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/instance"
	"github.com/zhecho/m9s/internal/ui"
)

// A second m9s says one is already running, waits 3 seconds and gives up.
func TestLockInstance_secondWaitsAndRefuses(t *testing.T) {
	dir := t.TempDir()
	first, err := instance.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	var out bytes.Buffer
	var waited time.Duration
	_, ok := lockInstance(dir, &out, func(d time.Duration) { waited = d })
	if ok {
		t.Fatal("the second m9s was let in")
	}
	if !strings.Contains(out.String(), "m9s is already running (pid ") || !strings.HasSuffix(out.String(), "this one quits in 3 seconds.\n") {
		t.Errorf("printed %q", out.String())
	}
	if waited != 3*time.Second {
		t.Errorf("waited %v, want 3s", waited)
	}
}

// The first m9s gets in, and lets the next one in once it releases.
func TestLockInstance_firstGetsIn(t *testing.T) {
	dir := t.TempDir()
	release, ok := lockInstance(dir, &bytes.Buffer{}, func(time.Duration) { t.Fatal("waited") })
	if !ok {
		t.Fatal("the first m9s was refused")
	}
	release()
	if release2, ok := lockInstance(dir, &bytes.Buffer{}, func(time.Duration) {}); !ok {
		t.Error("refused after release")
	} else {
		release2()
	}
}

// Earlier runs show first in the log, then this run's marker; what is
// logged from now on is appended to the file.
func TestStartLogFile_loadsAndAppends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m9s.log")
	old := "2026-11-25 10:00:00 INFO  [a] connected\n2026-11-25 10:00:05 ERROR [a] boom\n"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	saved := ui.AppLog
	ui.AppLog = &ui.Logger{}
	t.Cleanup(func() { ui.AppLog = saved })

	lf, err := startLogFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	ui.AppLog.Info("x")
	lf.Close()

	entries := ui.AppLog.Entries()
	if len(entries) < 4 || entries[0].Msg != "[a] connected" || entries[1].Msg != "[a] boom" ||
		!strings.HasPrefix(entries[2].Msg, "── m9s ") || !strings.Contains(entries[2].Msg, "started (pid ") {
		t.Fatalf("entries = %+v", entries)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), old) || !strings.Contains(string(data), "INFO  ── m9s ") || !strings.HasSuffix(string(data), "INFO  x\n") {
		t.Errorf("file =\n%s", data)
	}
}
