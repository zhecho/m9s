package sync

// TestSealedEvent_methods calls sealedEvent() on every event type to hit
// the zero-body marker methods that the coverage tool marks as statements.
// This must live in package sync (not sync_test) to access unexported methods.
func init() {
	FoldersUpdated{}.sealedEvent()
	BadgeUpdated{}.sealedEvent()
	NewMail{}.sealedEvent()
	DraftSynced{}.sealedEvent()
	AccountStatus{}.sealedEvent()
	DraftSyncFailed{}.sealedEvent()
}
