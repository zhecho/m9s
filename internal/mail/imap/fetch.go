package imap

import (
	"context"
	"fmt"
	"strconv"

	imap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	mail "github.com/zhecho/m9s/internal/mail"
)

// FetchHeaders fetches envelope headers for a page of messages in a folder.
// page is 1-based.
func (ic *imapClient) FetchHeaders(_ context.Context, folder string, page, pageSize int) ([]*mail.Envelope, error) {
	ic.opMu.Lock() // SELECT and the command that follows must not interleave
	defer ic.opMu.Unlock()
	ic.mu.Lock()
	c := ic.c
	ic.mu.Unlock()
	if c == nil {
		return nil, fmt.Errorf("imap: not connected")
	}

	selData, err := c.Select(folder, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("imap SELECT %s: %w", folder, err)
	}

	total := selData.NumMessages
	if total == 0 {
		return nil, nil
	}

	// Compute sequence range for this page (newest first: highest seq nums).
	if page < 1 {
		page = 1
	}
	start := int(total) - (page-1)*pageSize
	if start <= 0 {
		return nil, nil
	}
	end := start - pageSize + 1
	if end < 1 {
		end = 1
	}

	var seqSet imap.SeqSet
	seqSet.AddRange(uint32(end), uint32(start))

	fetchCmd := c.Fetch(seqSet, &imap.FetchOptions{
		Envelope:      true,
		Flags:         true,
		RFC822Size:    true,
		InternalDate:  true,
		UID:           true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	})
	defer fetchCmd.Close()

	var envelopes []*mail.Envelope
	for {
		msg := fetchCmd.Next()
		if msg == nil {
			break
		}
		buf, err := msg.Collect()
		if err != nil {
			continue
		}
		envelopes = append(envelopes, bufToEnvelope(buf))
	}
	if err := fetchCmd.Close(); err != nil {
		return envelopes, fmt.Errorf("imap FETCH headers: %w", err)
	}

	return envelopes, nil
}

// FetchBody fetches the full body of a single message by UID.
func (ic *imapClient) FetchBody(_ context.Context, folder string, uid uint32) (*mail.Message, error) {
	ic.opMu.Lock() // SELECT and the command that follows must not interleave
	defer ic.opMu.Unlock()
	ic.mu.Lock()
	c := ic.c
	ic.mu.Unlock()
	if c == nil {
		return nil, fmt.Errorf("imap: not connected")
	}

	if _, err := c.Select(folder, nil).Wait(); err != nil {
		return nil, fmt.Errorf("imap SELECT %s: %w", folder, err)
	}

	uidSet := imap.UIDSetNum(imap.UID(uid))
	bodySec := &imap.FetchItemBodySection{Peek: true}
	fetchCmd := c.Fetch(uidSet, &imap.FetchOptions{
		Envelope:    true,
		Flags:       true,
		RFC822Size:  true,
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{bodySec},
	})
	defer fetchCmd.Close()

	msg := fetchCmd.Next()
	if msg == nil {
		return nil, fmt.Errorf("imap: message uid=%d not found in %s", uid, folder)
	}
	buf, err := msg.Collect()
	if err != nil {
		return nil, fmt.Errorf("imap FETCH body: %w", err)
	}

	env := bufToEnvelope(buf)
	// No BODYSTRUCTURE here: the body itself is parsed below.

	// Parse body from the fetched body section bytes.
	var rawBody []byte
	for _, bs := range buf.BodySection {
		rawBody = bs.Bytes
		break
	}

	return mail.ParseMessage(env, rawBody), nil
}

// bufToEnvelope converts an imapclient FetchMessageBuffer to our domain Envelope.
func bufToEnvelope(buf *imapclient.FetchMessageBuffer) *mail.Envelope {
	env := &mail.Envelope{
		ID:      strconv.FormatUint(uint64(buf.UID), 10),
		UID:     uint32(buf.UID),
		SortKey: int64(buf.UID),
		SeqNum:  buf.SeqNum,
		Size:    uint32(buf.RFC822Size),
	}
	env.HasAttach = hasAttachment(buf.BodyStructure)

	for _, f := range buf.Flags {
		env.Flags = append(env.Flags, string(f))
	}

	if buf.Envelope != nil {
		e := buf.Envelope
		env.Subject = e.Subject
		env.Date = e.Date
		env.MessageID = e.MessageID
		env.InReplyTo = e.InReplyTo
		for _, a := range e.From {
			env.From = append(env.From, mail.Address{Name: a.Name, Email: addrMailbox(a)})
		}
		for _, a := range e.To {
			env.To = append(env.To, mail.Address{Name: a.Name, Email: addrMailbox(a)})
		}
		for _, a := range e.Cc {
			env.CC = append(env.CC, mail.Address{Name: a.Name, Email: addrMailbox(a)})
		}
	}
	return env
}

// addrMailbox returns the email address string from an imap.Address.
func addrMailbox(a imap.Address) string {
	if a.Host == "" {
		return a.Mailbox
	}
	return a.Mailbox + "@" + a.Host
}

// hasAttachment reports whether a message structure has a file the owner
// would save, by mail.IsFile — the rule mail.Message.Files follows, so the
// sign shown before the body is downloaded matches what the reader lists.
func hasAttachment(bs imap.BodyStructure) bool {
	if bs == nil {
		return false
	}
	found := false
	bs.Walk(func(_ []int, part imap.BodyStructure) bool {
		sp, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return !found // a multipart: look inside
		}
		disp := ""
		if d := sp.Disposition(); d != nil {
			disp = d.Value
		}
		if mail.IsFile(disp, sp.MediaType(), sp.ID != "") {
			found = true
		}
		return !found
	})
	return found
}
