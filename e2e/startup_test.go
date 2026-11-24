//go:build e2e

package e2e

import "testing"

// TestApp_showsFolderPaneOnStart verifies that m9s renders the folder pane
// containing INBOX within 10s of launch.
func TestApp_showsFolderPaneOnStart(t *testing.T) {
	h := NewHarness(t)
	defer h.Close()

	h.Contains("INBOX")
}
