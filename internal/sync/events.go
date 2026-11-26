package sync

import (
	"time"

	"github.com/zhecho/m9s/internal/mail"
)

// Event is a discriminated union emitted by Engine over the events channel.
type Event interface{ sealedEvent() }

// FoldersUpdated is emitted after initial folder discovery or a re-scan.
type FoldersUpdated struct {
	AccountName string
	Folders     []*mail.Folder
}

func (FoldersUpdated) sealedEvent() {}

// BadgeUpdated is emitted when unread counts change for a folder.
type BadgeUpdated struct {
	AccountName string
	FolderName  string
	Unseen      uint32
	Total       uint32
}

func (BadgeUpdated) sealedEvent() {}

// NewMail is emitted when the IDLE connection detects new messages.
type NewMail struct {
	AccountName string
	FolderName  string
}

func (NewMail) sealedEvent() {}

// DraftSynced is emitted after a local draft is successfully APPENDed to IMAP.
type DraftSynced struct {
	AccountName string
	DraftID     string
}

func (DraftSynced) sealedEvent() {}

// ConnState is the connection state of one account.
type ConnState int

const (
	StateConnecting ConnState = iota
	StateConnected
	StateFailed
)

// AccountStatus is emitted on every connection state change of an account.
type AccountStatus struct {
	AccountName string
	State       ConnState
	Err         error         // set when State == StateFailed
	Attempt     int           // StateFailed: 1 for the first failure of a streak, then 2, 3…
	RetryIn     time.Duration // StateFailed: delay before the next automatic attempt; 0 = no auto-retry
	Recovered   bool          // StateConnected: true when this follows one or more failures
}

func (AccountStatus) sealedEvent() {}

// DraftSyncFailed is emitted when a local draft could not be uploaded to the
// server's Drafts folder. The draft stays pending and is retried later.
type DraftSyncFailed struct {
	AccountName string
	DraftID     string
	Subject     string
	Err         error
}

func (DraftSyncFailed) sealedEvent() {}

// ListingProgress reports a folder's listing being stored in the local
// copy (an API account: Gmail, Microsoft): Stored of the Total messages
// the server lists are in the store so far. Done is set on the report that
// ends the listing. The open list reloads from it, so the first sync of a
// big mailbox fills the screen while it runs.
type ListingProgress struct {
	AccountName string
	FolderName  string
	Stored      int
	Total       int
	Done        bool
}

func (ListingProgress) sealedEvent() {}

// ServerInfo names the remote server of an account, e.g. "Dovecot / 2.3.21"
// or "Gmail API / v1 (rev 20261106)". Sent at most once per engine run.
type ServerInfo struct {
	AccountName string
	Text        string
}

func (ServerInfo) sealedEvent() {}
