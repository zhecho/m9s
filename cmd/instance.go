package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/zhecho/m9s/internal/instance"
	"github.com/zhecho/m9s/internal/ui"
	"github.com/zhecho/m9s/internal/version"
)

const (
	logFileName  = "m9s.log"
	logFileMax   = 5 << 20 // past this the log moves to m9s.log.1
	logTailLines = 2000    // lines of earlier runs the log view starts with
	refuseWait   = 3 * time.Second
)

// lockInstance keeps the mail app to one running copy: it takes the lock in
// dir, or, when another m9s holds it, says so on stderr, waits refuseWait
// (so a window opened just for it shows the message) and reports ok=false.
// A lock that cannot be made at all never stops m9s; it is logged.
func lockInstance(dir string, stderr io.Writer, wait func(time.Duration)) (release func(), ok bool) {
	release, err := instance.Acquire(dir)
	var running *instance.RunningError
	switch {
	case errors.As(err, &running):
		fmt.Fprintln(stderr, running.Error())
		wait(refuseWait)
		return nil, false
	case err != nil:
		ui.AppLog.Warn("one-m9s lock not taken: %v", err)
		return func() {}, true
	}
	return release, true
}

// startLogFile keeps the log in dir/m9s.log: the log view starts with the
// last lines of earlier runs, this run is marked, and every entry from now
// on is appended. The caller closes the file when m9s ends.
func startLogFile(dir string) (*ui.LogFile, error) {
	path := filepath.Join(dir, logFileName)
	lf, err := ui.OpenLogFile(path, logFileMax)
	if err != nil {
		return nil, err
	}
	ui.AppLog.Load(ui.TailEntries(path, logTailLines))
	lf.OnError(func(err error) { // once a run: a full disk must not flood the log
		ui.AppLog.Warn("log file %s not written: %v (the log goes on here)", path, err)
	})
	ui.AppLog.SetSink(lf.Write)
	ui.AppLog.Info("── m9s %s started (pid %d) ──", version.String(), os.Getpid())
	return lf, nil
}
