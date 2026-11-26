//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Harness controls a running m9s instance inside a tmux pane.
type Harness struct {
	paneID  string // tmux target: pane ID (%12) or session:window.pane
	inSplit bool   // true when a pane was split from an existing tmux session
	t       *testing.T
}

// setupPane creates the tmux pane (split or new session) and returns
// a Harness with paneID and inSplit set. It does NOT start m9s.
func setupPane(t *testing.T) *Harness {
	t.Helper()
	h := &Harness{t: t}
	if os.Getenv("TMUX") != "" {
		out, err := exec.Command("tmux", "split-window", "-h", "-P", "-F", "#{pane_id}").Output()
		if err != nil {
			t.Fatalf("tmux split-window: %v", err)
		}
		h.paneID = strings.TrimSpace(string(out))
		h.inSplit = true
	} else {
		const session = "m9s-e2e"
		exec.Command("tmux", "kill-session", "-t", session).Run()
		if err := exec.Command("tmux", "new-session", "-d", "-s", session, "-x", "220", "-y", "50").Run(); err != nil {
			t.Fatalf("tmux new-session: %v", err)
		}
		h.paneID = session + ":0.0"
	}
	return h
}

// NewHarness starts m9s. If already inside tmux, splits the current window to
// the right. Otherwise creates a detached session named "m9s-e2e".
// It waits up to 10s for "INBOX" to appear before returning.
func NewHarness(t *testing.T) *Harness {
	t.Helper()
	h := setupPane(t)
	if err := exec.Command("tmux", "send-keys", "-t", h.paneID, stateEnv(t)+"m9s", "Enter").Run(); err != nil {
		t.Fatalf("launch m9s: %v", err)
	}
	h.waitFor("INBOX", 10*time.Second)
	return h
}

// NewHarnessWithEditor is like NewHarness but sets $EDITOR to editorPath before
// launching m9s. editorPath must be an absolute path.
func NewHarnessWithEditor(t *testing.T, editorPath string) *Harness {
	t.Helper()
	h := setupPane(t)
	launch := stateEnv(t) + "EDITOR=" + editorPath + " m9s"
	if err := exec.Command("tmux", "send-keys", "-t", h.paneID, launch, "Enter").Run(); err != nil {
		t.Fatalf("launch m9s with editor: %v", err)
	}
	h.waitFor("INBOX", 10*time.Second)
	return h
}

// stateEnv points the m9s under test at a state directory of its own, so
// it neither meets the lock of an m9s you have open nor writes to your log
// and layout.
func stateEnv(t *testing.T) string {
	return "XDG_STATE_HOME=" + t.TempDir() + " "
}

// FakeEditorPath returns the absolute path to e2e/testdata/fake-editor.sh.
// Must be called from within a Go test (working dir = e2e/).
func FakeEditorPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("testdata/fake-editor.sh")
	if err != nil {
		t.Fatalf("fake editor path: %v", err)
	}
	return abs
}

// Send sends a single keystroke or key name to the m9s pane.
// Use tmux key names for special keys: "Enter", "Tab", "Escape", "Up", "Down",
// "Left", "Right", "C-c". Regular characters ("j", "k", "c") are sent as-is.
func (h *Harness) Send(key string) {
	h.t.Helper()
	if err := exec.Command("tmux", "send-keys", "-t", h.paneID, key).Run(); err != nil {
		h.t.Fatalf("send-keys %q: %v", key, err)
	}
	time.Sleep(60 * time.Millisecond)
}

// Screen returns the current full terminal snapshot as plain text (no escape codes).
func (h *Harness) Screen() string {
	out, err := exec.Command("tmux", "capture-pane", "-t", h.paneID, "-p").Output()
	if err != nil {
		h.t.Fatalf("capture-pane: %v", err)
	}
	return string(out)
}

// WaitFor polls the screen every 100ms until text appears or 3s elapses.
func (h *Harness) WaitFor(text string) {
	h.t.Helper()
	h.waitFor(text, 3*time.Second)
}

// WaitForAny waits until any of the given strings appears on screen.
func (h *Harness) WaitForAny(texts ...string) {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s := h.Screen()
		for _, text := range texts {
			if strings.Contains(s, text) {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("WaitForAny %v timed out\nscreen:\n%s", texts, h.Screen())
}

// Contains asserts text is present on the screen right now (no polling).
func (h *Harness) Contains(text string) {
	h.t.Helper()
	if !strings.Contains(h.Screen(), text) {
		h.t.Errorf("Contains %q: not found\nscreen:\n%s", text, h.Screen())
	}
}

// NotContains asserts text is absent from the screen right now.
func (h *Harness) NotContains(text string) {
	h.t.Helper()
	if strings.Contains(h.Screen(), text) {
		h.t.Errorf("NotContains %q: unexpectedly found\nscreen:\n%s", text, h.Screen())
	}
}

// WaitForAbsence polls the screen every 100ms until text is gone or 3s elapses.
// Use this instead of NotContains when an action needs time to take effect.
func (h *Harness) WaitForAbsence(text string) {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !strings.Contains(h.Screen(), text) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("WaitForAbsence %q timed out\nscreen:\n%s", text, h.Screen())
}

// Close kills the tmux pane (split mode) or the detached session.
func (h *Harness) Close() {
	if h.inSplit {
		exec.Command("tmux", "kill-pane", "-t", h.paneID).Run()
	} else {
		exec.Command("tmux", "kill-session", "-t", "m9s-e2e").Run()
	}
}

// waitFor is the internal polling helper.
func (h *Harness) waitFor(text string, timeout time.Duration) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(h.Screen(), text) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("waitFor %q timed out after %v\nscreen:\n%s", text, timeout, h.Screen())
}
