package view

import (
	"strings"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/ui"
)

// logApp is a wired app with a log of its own (the log is global), whose
// UI updates are queued for the test to run.
func logApp(t *testing.T) (*RootApp, chan func()) {
	t.Helper()
	saved := ui.AppLog
	ui.AppLog = &ui.Logger{}
	t.Cleanup(func() { ui.AppLog = saved })
	queued := make(chan func(), 256)
	ra := newWiredRootApp(t, "a")
	ra.logDeliver = func(fn func()) { queued <- fn }
	return ra, queued
}

func settle(queued chan func()) int {
	time.Sleep(50 * time.Millisecond) // the refreshes run in their own goroutines
	return len(queued)
}

// runOne runs one queued UI update, failing instead of hanging.
func runOne(t *testing.T, queued chan func()) {
	t.Helper()
	select {
	case fn := <-queued:
		fn()
	case <-time.After(time.Second):
		t.Fatal("no log redraw was queued")
	}
}

// A burst of log lines queues one redraw of the log, not one per line: the
// log starts with up to 2,000 lines of earlier runs, and a redraw per line
// would stall the screen.
func TestLogView_burstIsOneRedraw(t *testing.T) {
	ra, queued := logApp(t)
	for i := range 100 {
		ui.AppLog.Info("line %d", i)
	}
	if n := settle(queued); n != 1 {
		t.Fatalf("%d redraws queued for 100 lines, want 1", n)
	}
	runOne(t, queued)
	if got := strings.Count(ra.logView.GetText(true), "line "); got != 100 {
		t.Errorf("log shows %d lines, want 100", got)
	}
}

// New lines are added below the ones shown; none is shown twice.
func TestLogView_appendsNewLinesOnly(t *testing.T) {
	ra, queued := logApp(t)
	ui.AppLog.Info("first")
	settle(queued)
	runOne(t, queued)
	for i := range 3 {
		ui.AppLog.Info("more %d", i)
	}
	settle(queued)
	runOne(t, queued)
	text := ra.logView.GetText(true)
	if strings.Count(text, "first") != 1 || strings.Count(text, "more ") != 3 {
		t.Errorf("log =\n%s", text)
	}
	ra.setLogFilter("more 1") // a filter redraws what matches
	if text := ra.logView.GetText(true); strings.Contains(text, "first") || !strings.Contains(text, "more 1") {
		t.Errorf("filtered log =\n%s", text)
	}
	ui.AppLog.Info("more 1 again")
	settle(queued)
	runOne(t, queued)
	if text := ra.logView.GetText(true); strings.Count(text, "more 1") != 2 || strings.Contains(text, "first") {
		t.Errorf("filtered log after a new line =\n%s", text)
	}
}
