package sync

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	imapadapter "github.com/zhecho/m9s/internal/mail/imap"
	"github.com/zhecho/m9s/internal/store"
)

// envelopeBatch is how many messages' headers are asked for in one command.
const envelopeBatch = 200

// progressEvery is how many downloaded messages pass between progress reports.
const progressEvery = 25

// bodyBatch is how many messages are downloaded per command: enough to save
// most of the round trips, few enough that a cancelled pass stops promptly.
const bodyBatch = 20

// MirrorProgress reports how far the local copy of an account is: how many
// messages the store knows and how many of them are downloaded. Done is set
// on the report that ends a pass.
type MirrorProgress struct {
	AccountName string
	Messages    int
	Downloaded  int
	Done        bool
}

func (MirrorProgress) sealedEvent() {}

// MirrorFailed is emitted when the local copy could not be continued. The
// account itself stays connected, and the copy is resumed later.
type MirrorFailed struct {
	AccountName string
	Err         error
}

func (MirrorFailed) sealedEvent() {}

// Mirror keeps the store's copy of one account equal to the server: every
// folder, every message. It only reads from the server.
type Mirror struct {
	account    string
	conn       imapadapter.Mirror
	store      *store.Store
	onProgress func(MirrorProgress)
}

// NewMirror creates a mirror that copies account from conn into st.
func NewMirror(account string, conn imapadapter.Mirror, st *store.Store) *Mirror {
	return &Mirror{account: account, conn: conn, store: st, onProgress: func(MirrorProgress) {}}
}

// OnProgress registers fn to be told how far the copy is.
func (m *Mirror) OnProgress(fn func(MirrorProgress)) { m.onProgress = fn }

func (m *Mirror) report(done bool) {
	messages, downloaded := m.store.Counts(m.account)
	m.onProgress(MirrorProgress{AccountName: m.account, Messages: messages, Downloaded: downloaded, Done: done})
}

// Pass brings the store up to date with the server once. Headers of every
// folder come first, so every mail list is complete early; then message
// content, newest first. It stops at the first server error or when ctx is
// cancelled, and can simply be run again: what is stored is not redone.
func (m *Mirror) Pass(ctx context.Context, folders []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.store.PruneFolders(m.account, folders); err != nil {
		return err
	}
	m.report(false)
	for _, folder := range folders {
		if err := m.syncHeaders(ctx, folder); err != nil {
			return err
		}
	}
	m.report(false)
	for _, folder := range folders {
		if err := m.downloadBodies(ctx, folder); err != nil {
			return err
		}
	}
	m.report(true)
	return nil
}

// syncHeaders makes the stored message list of folder equal to the server's.
func (m *Mirror) syncHeaders(ctx context.Context, folder string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	validity, remote, err := m.conn.Snapshot(ctx, folder)
	if err != nil {
		return fmt.Errorf("mirror %s: %w", folder, err)
	}
	storedValidity, stored, err := m.store.FolderState(m.account, folder)
	if err != nil {
		return err
	}
	local := byUID(stored)
	if storedValidity != 0 && storedValidity != validity {
		// The server renumbered the folder: stored UIDs now name other mail.
		err = m.store.ResetFolder(m.account, folder, validity)
		local = nil
	} else {
		// Also the first pass over a folder the UI may already have listed.
		err = m.store.SetUIDValidity(m.account, folder, validity)
	}
	if err != nil {
		return err
	}

	var gone, added []uint32
	changed := make(map[uint32][]string)
	for uid, flags := range local {
		newFlags, still := remote[uid]
		switch {
		case !still:
			gone = append(gone, uid)
		case !sameFlags(flags, newFlags):
			changed[uid] = newFlags
		}
	}
	for uid := range remote {
		if _, have := local[uid]; !have {
			added = append(added, uid)
		}
	}
	if err := m.store.RemoveMessages(m.account, folder, uidIDs(gone)); err != nil {
		return err
	}
	if err := m.store.SetFlags(m.account, folder, byID(changed)); err != nil {
		return err
	}

	slices.Sort(added)
	slices.Reverse(added) // newest first: an interrupted pass has the recent mail
	for batch := range slices.Chunk(added, envelopeBatch) {
		if err := ctx.Err(); err != nil {
			return err
		}
		envelopes, err := m.conn.FetchEnvelopes(ctx, folder, batch)
		if err != nil {
			return fmt.Errorf("mirror %s: %w", folder, err)
		}
		if err := m.store.AddMessages(m.account, folder, envelopes); err != nil {
			return err
		}
	}
	return nil
}

// downloadBodies fetches the content of every message of folder that is not
// on disk yet, a batch per command. A message deleted since the list was
// taken is simply not returned; the next pass drops it.
func (m *Mirror) downloadBodies(ctx context.Context, folder string) error {
	missingIDs, err := m.store.MissingBodies(m.account, folder)
	if err != nil {
		return err
	}
	missing := idUIDs(missingIDs)
	saved := 0
	for batch := range slices.Chunk(missing, bodyBatch) {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := m.conn.FetchRaw(ctx, folder, batch, func(uid uint32, raw []byte) error {
			if err := m.store.PutBody(m.account, folder, uidID(uid), raw); err != nil {
				return err // most likely the disk: stop rather than fail per message
			}
			if saved++; saved%progressEvery == 0 {
				m.report(false)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("mirror %s: %w", folder, err)
		}
	}
	return nil
}

// sameFlags reports whether two flag lists hold the same flags in any order.
func sameFlags(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, f := range a {
		if !slices.Contains(b, f) {
			return false
		}
	}
	return true
}

// The store keys messages by text id; for IMAP the id is the UID in decimal.
// The mirror works in UIDs and converts at the store boundary.

func uidID(uid uint32) string { return strconv.FormatUint(uint64(uid), 10) }

func uidIDs(uids []uint32) []string {
	ids := make([]string, len(uids))
	for i, uid := range uids {
		ids[i] = uidID(uid)
	}
	return ids
}

// idUIDs converts stored ids back to UIDs, skipping any that is not one.
func idUIDs(ids []string) []uint32 {
	uids := make([]uint32, 0, len(ids))
	for _, id := range ids {
		if uid, err := strconv.ParseUint(id, 10, 32); err == nil {
			uids = append(uids, uint32(uid))
		}
	}
	return uids
}

func byUID(flags map[string][]string) map[uint32][]string {
	out := make(map[uint32][]string, len(flags))
	for id, f := range flags {
		if uid, err := strconv.ParseUint(id, 10, 32); err == nil {
			out[uint32(uid)] = f
		}
	}
	return out
}

func byID(flags map[uint32][]string) map[string][]string {
	out := make(map[string][]string, len(flags))
	for uid, f := range flags {
		out[uidID(uid)] = f
	}
	return out
}
