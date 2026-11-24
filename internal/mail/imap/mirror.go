package imap

import (
	"context"
	"fmt"

	imap "github.com/emersion/go-imap/v2"

	"github.com/zhecho/m9s/internal/config"
	mail "github.com/zhecho/m9s/internal/mail"
)

// Mirror is what keeping a full local copy of an account needs from the
// server. It runs on a connection of its own, so downloading a mailbox never
// makes the user's own commands wait, and it only ever reads: folders are
// opened read-only and nothing is marked as seen.
type Mirror interface {
	Connect(ctx context.Context, password string) error
	Close() error
	Closed() <-chan struct{}
	Abort()
	// Snapshot returns the folder's UIDVALIDITY and every message's UID
	// with its flags.
	Snapshot(ctx context.Context, folder string) (uidValidity uint32, flags map[uint32][]string, err error)
	// FetchEnvelopes returns the headers of the given messages.
	FetchEnvelopes(ctx context.Context, folder string, uids []uint32) ([]*mail.Envelope, error)
	// FetchRaw hands each of the given messages to each, exactly as the
	// server holds it (RFC 5322), in one command. Messages that no longer
	// exist are skipped. An error from each ends the fetch and is returned.
	FetchRaw(ctx context.Context, folder string, uids []uint32, each func(uid uint32, raw []byte) error) error
}

// NewMirror creates a connection for mirroring (not yet connected).
func NewMirror(cfg *config.IMAPConfig) Mirror {
	return &imapClient{cfg: cfg}
}

// examine opens folder read-only and returns the connection to use. The
// caller must hold opMu.
func (ic *imapClient) examine(folder string) (*imap.SelectData, error) {
	ic.mu.Lock()
	c := ic.c
	ic.mu.Unlock()
	if c == nil {
		return nil, fmt.Errorf("imap: not connected")
	}
	data, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, fmt.Errorf("imap EXAMINE %s: %w", folder, err)
	}
	return data, nil
}

func (ic *imapClient) Snapshot(_ context.Context, folder string) (uint32, map[uint32][]string, error) {
	ic.opMu.Lock()
	defer ic.opMu.Unlock()
	data, err := ic.examine(folder)
	if err != nil {
		return 0, nil, err
	}
	flags := make(map[uint32][]string, data.NumMessages)
	if data.NumMessages == 0 {
		return data.UIDValidity, flags, nil
	}
	var all imap.SeqSet
	all.AddRange(1, 0) // 1:*
	cmd := ic.c.Fetch(all, &imap.FetchOptions{UID: true, Flags: true})
	defer cmd.Close()
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		buf, err := msg.Collect()
		if err != nil {
			continue
		}
		var f []string
		for _, flag := range buf.Flags {
			f = append(f, string(flag))
		}
		flags[uint32(buf.UID)] = f
	}
	if err := cmd.Close(); err != nil {
		return 0, nil, fmt.Errorf("imap FETCH flags: %w", err)
	}
	return data.UIDValidity, flags, nil
}

func (ic *imapClient) FetchEnvelopes(_ context.Context, folder string, uids []uint32) ([]*mail.Envelope, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	ic.opMu.Lock()
	defer ic.opMu.Unlock()
	if _, err := ic.examine(folder); err != nil {
		return nil, err
	}
	var set imap.UIDSet
	for _, uid := range uids {
		set.AddNum(imap.UID(uid))
	}
	cmd := ic.c.Fetch(set, &imap.FetchOptions{
		Envelope: true, Flags: true, RFC822Size: true, InternalDate: true, UID: true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	})
	defer cmd.Close()
	var envelopes []*mail.Envelope
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		buf, err := msg.Collect()
		if err != nil {
			continue
		}
		envelopes = append(envelopes, bufToEnvelope(buf))
	}
	if err := cmd.Close(); err != nil {
		return envelopes, fmt.Errorf("imap FETCH headers: %w", err)
	}
	return envelopes, nil
}

func (ic *imapClient) FetchRaw(_ context.Context, folder string, uids []uint32, each func(uid uint32, raw []byte) error) error {
	if len(uids) == 0 {
		return nil
	}
	ic.opMu.Lock()
	defer ic.opMu.Unlock()
	if _, err := ic.examine(folder); err != nil {
		return err
	}
	var set imap.UIDSet
	for _, uid := range uids {
		set.AddNum(imap.UID(uid))
	}
	cmd := ic.c.Fetch(set, &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{{Peek: true}},
	})
	defer cmd.Close()
	var eachErr error
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		buf, err := msg.Collect() // always read, so the command can finish
		if err != nil || eachErr != nil {
			continue
		}
		for _, section := range buf.BodySection {
			eachErr = each(uint32(buf.UID), section.Bytes)
			break
		}
	}
	if err := cmd.Close(); err != nil {
		return fmt.Errorf("imap FETCH body: %w", err)
	}
	return eachErr
}
