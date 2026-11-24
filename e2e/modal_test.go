//go:build e2e

package e2e

import "testing"

// TestSendModal_tabCyclesButtonsWithoutEscapingModal is a regression test for
// the bug where Tab in the send-modal moved focus to other panes instead of
// cycling between the modal's buttons.
func TestSendModal_tabCyclesButtonsWithoutEscapingModal(t *testing.T) {
	h := NewHarnessWithEditor(t, FakeEditorPath(t))
	defer h.Close()

	h.Send("c")
	h.WaitFor("Send now?")

	h.Send("Tab")
	h.Send("Tab")
	h.Send("Tab")

	h.Contains("Send now?")
}

// TestSendModal_keepDraftClosesModal verifies that choosing the second button
// ("Server Draft" or "Discard") dismisses the modal and returns to the main UI.
func TestSendModal_keepDraftClosesModal(t *testing.T) {
	h := NewHarnessWithEditor(t, FakeEditorPath(t))
	defer h.Close()

	h.Send("c")
	h.WaitFor("Send now?")

	// Tab moves to the second button ("Server Draft" or "Discard" depending on config).
	h.Send("Tab")
	h.Send("Enter")

	// Wait for the modal to disappear — it takes a draw cycle after Enter.
	h.WaitForAbsence("Send now?")
}
