package sync

import (
	"context"
	"errors"

	"github.com/zhecho/m9s/internal/mail"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	"github.com/zhecho/m9s/internal/store"
)

// Account is what the UI needs from one mail account, whatever the
// provider. A message is addressed by (folder, id): the id is opaque text —
// for IMAP the UID in decimal — and is never parsed by the caller.
type Account interface {
	Start(ctx context.Context)
	Done() <-chan struct{}
	Retry()

	Folders(ctx context.Context) ([]*mail.Folder, error)
	RefreshBadge(ctx context.Context, folder string)
	SyncAll(ctx context.Context)

	FetchHeaders(ctx context.Context, folder string, page, pageSize int) ([]*mail.Envelope, error)
	FetchBody(ctx context.Context, folder, id string) (*mail.Message, error)
	SetFlags(ctx context.Context, folder, id string, flags []string, add bool) error
	MoveToTrash(ctx context.Context, folder, id string) error
	PermanentDelete(ctx context.Context, folder, id string) error
	EmptyTrash(ctx context.Context) (int, error)

	DraftsFolderName() string
	TrashFolderName() string
	SentFolderName() string

	// SaveDraft uploads raw as a server draft and returns its id;
	// replaces is the id of the copy it supersedes ("" for a new draft).
	SaveDraft(ctx context.Context, raw []byte, replaces string) (id string, err error)
	TrashDraftCopy(ctx context.Context, id string) error
	DeleteDraftCopy(ctx context.Context, id string) error
	SyncDrafts(ctx context.Context) error

	// Send delivers d and returns the message as it was sent. The account
	// owns the means of sending and its secret.
	Send(ctx context.Context, d smtpadapter.Draft) ([]byte, error)
	ServerSavesSent() bool
	SaveSentCopy(ctx context.Context, msg []byte) (string, error)
}

// BulkDeleter is offered by accounts that delete many messages in the
// trash for good in a few requests. It keeps PermanentDelete's guard: only
// what is still in the trash on the server is deleted, one restored
// meanwhile fails with ErrNotInTrash, one already gone is done. progress is
// told how many more ids were dealt with, done or failed. failed holds the
// ids not deleted, with why.
type BulkDeleter interface {
	PermanentDeleteMany(ctx context.Context, folder string, ids []string, progress func(n int)) (failed map[string]error)
}

// Labels is offered by accounts whose messages carry labels.
type Labels interface {
	AccountLabels() []*mail.Folder // user + system labels that can be applied
	ModifyLabels(ctx context.Context, folder, id string, add, remove []string) error
	// CurrentLabels names the labels (as AccountLabels names them) carried by
	// message id; with a threadID, by any message of that conversation.
	CurrentLabels(threadID, id string) []string
}

// Mover is offered by accounts that move a message to another folder and
// archive it.
type Mover interface {
	// Move moves message id from folder to dest, a folder name as the
	// account lists it.
	Move(ctx context.Context, folder, id, dest string) error
	// Archive takes message id out of folder: IMAP moves it to the archive
	// folder, Gmail takes it out of the Inbox.
	Archive(ctx context.Context, folder, id string) error
	// MoveTargets names the folders a message in folder can be moved to.
	MoveTargets(folder string) []string
}

// ErrNoArchiveFolder means an IMAP account has no folder to archive to.
var ErrNoArchiveFolder = errors.New("no archive folder — set folders.archive in the account config")

// Threads is offered by accounts that group messages into conversations.
type Threads interface {
	Thread(ctx context.Context, threadID string) ([]*mail.Message, error) // oldest first
	// ModifyThread adds and removes labels (ids or names; \Seen and
	// \Flagged are mapped) on every message of the conversation.
	ModifyThread(ctx context.Context, threadID string, add, remove []string) error
	TrashThread(ctx context.Context, threadID string) error
}

// ShowsConversations reports whether a's list of folder is one row per
// conversation: on accounts with threads, except in Trash and Drafts, where
// every message stands alone (a permanent delete must name one message).
func ShowsConversations(a Account, folder string) bool {
	if _, ok := a.(Threads); !ok {
		return false
	}
	return folder != a.TrashFolderName() && folder != a.DraftsFolderName()
}

// StoreKeeper is offered by accounts that keep the local store themselves
// (Gmail mirrors every synced label): what they list comes from the store
// and must not be written back over newer rows.
type StoreKeeper interface {
	KeepsStore() bool
}

// KeepsStore reports whether a keeps the local store itself.
func KeepsStore(a Account) bool {
	k, ok := a.(StoreKeeper)
	return ok && k.KeepsStore()
}

// DraftCopies is offered by accounts whose Drafts rows are not named by the
// id SaveDraft returns (Gmail: a row is a message, SaveDraft names the
// draft holding it).
type DraftCopies interface {
	// DraftCopyOf returns the SaveDraft id of the server draft shown as
	// Drafts row envID, "" when no draft holds it.
	DraftCopyOf(ctx context.Context, envID string) (string, error)
}

// ServerSearch is offered by accounts that can search on the server, in the
// provider's own query syntax.
type ServerSearch interface {
	Search(ctx context.Context, folder, query string) ([]*mail.Envelope, error)
}

// SearchResult is one page of a server search.
type SearchResult struct {
	Rows []*mail.Envelope
	// Total is how many match in all; 0 when the server does not say
	// (Outlook). Estimate marks it as the server's estimate (Gmail).
	Total    int
	Estimate bool
	// Next is the cursor of the next page; "" when there is none.
	Next string
}

// PagedSearch is a ServerSearch that answers a page at a time and says how
// many match.
type PagedSearch interface {
	SearchPage(ctx context.Context, folder, query, cursor string) (SearchResult, error)
}

var (
	_ Account      = (*Engine)(nil)
	_ ServerSearch = (*Engine)(nil)
)

// ErrNotListable means a folder is not listed from the local copy (Gmail's
// All Mail, an account without one): the caller lists it as before.
var ErrNotListable = errors.New("folder is not listed from the local copy")

// Lister pages a folder's list and filters it over the whole folder. Rows
// come newest first, with how many match in all.
type Lister interface {
	List(ctx context.Context, folder string, q store.ListQuery) ([]*mail.Envelope, int, error)
}
