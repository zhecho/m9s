//go:build e2e

package e2e

import "testing"

// TestMailList_dKeyShowsMoveToTrashModal verifies that pressing 'd' in INBOX
// shows the move-to-trash confirmation modal.
func TestMailList_dKeyShowsMoveToTrashModal(t *testing.T) {
	h := NewHarness(t)
	defer h.Close()

	h.WaitFor("INBOX")
	h.Send("Tab") // d deletes in the mail list; in the folder pane it only deletes local drafts
	h.Send("d")
	// The modal contains a "Cancel" button — unique to modal context.
	h.WaitFor("Cancel")
}

// TestMailList_dKeyMovesToDeletedItems verifies that confirming the
// move-to-trash modal dismisses it and shows "Moved to Deleted Items".
func TestMailList_dKeyMovesToDeletedItems(t *testing.T) {
	h := NewHarness(t)
	defer h.Close()

	h.WaitFor("INBOX")
	h.Send("Tab") // focus the mail list
	h.Send("d")
	h.WaitFor("Cancel") // modal appeared
	h.Send("Enter")
	h.WaitForAbsence("Cancel") // modal dismissed
	h.WaitFor("Moved to Deleted Items")
}

// navigateToTrash focuses the folder pane and navigates to the Deleted Items /
// Trash folder. The folder pane is a TreeView that uses Up/Down arrow keys.
// We press Up several times to reach the top of the list, then Enter.
// Callers should WaitFor a folder-specific string after this returns.
func navigateToTrash(h *Harness) {
	h.Send("Tab") // focus folder pane
	// Press Up enough times to ensure we reach the top of the folder list.
	for i := 0; i < 8; i++ {
		h.Send("Up")
	}
	// The first folder alphabetically on this server is Deleted Items.
	h.Send("Enter")
}

// TestMailList_permanentDeleteShowsConfirmModal verifies that pressing 'd'
// while in the Deleted Items folder shows the permanent-delete modal.
func TestMailList_permanentDeleteShowsConfirmModal(t *testing.T) {
	h := NewHarness(t)
	defer h.Close()

	h.WaitFor("INBOX")
	navigateToTrash(h)
	// Header now shows the trash folder; Tab back to mail list.
	h.WaitForAny("Deleted Items", "Trash")
	h.Send("Tab") // focus mail list
	h.Send("d")
	// Permanent-delete modal has "Delete Forever" button — unique to this modal.
	h.WaitFor("Forever")
}

// TestMailList_permanentDeleteConfirm verifies that confirming permanent
// deletion in the Deleted Items folder shows "Permanently deleted".
func TestMailList_permanentDeleteConfirm(t *testing.T) {
	h := NewHarness(t)
	defer h.Close()

	h.WaitFor("INBOX")
	navigateToTrash(h)
	h.WaitForAny("Deleted Items", "Trash")
	h.Send("Tab") // focus mail list
	h.Send("d")
	h.WaitFor("Forever") // permanent-delete modal
	h.Send("Enter")
	h.WaitForAbsence("Forever") // modal dismissed
	h.WaitFor("Permanently deleted")
}
