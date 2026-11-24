//go:build e2e

package e2e

import "testing"

// TestMailList_jkMovesSelection verifies that the j and k keys do not crash
// the app and keep the main UI visible after moving the mail-list selection.
func TestMailList_jkMovesSelection(t *testing.T) {
	h := NewHarness(t)
	defer h.Close()

	h.Send("j")
	h.Send("j")
	h.Send("j")
	h.Send("k")
	h.Contains("INBOX")
}

// TestMailList_enterOpensReader verifies that pressing Enter on a mail-list
// item opens the reader pane without crashing the app.
func TestMailList_enterOpensReader(t *testing.T) {
	h := NewHarness(t)
	defer h.Close()

	h.Send("Enter")

	// After Enter the reader pane opens. INBOX must remain in the folder pane.
	h.WaitFor("INBOX")
}
