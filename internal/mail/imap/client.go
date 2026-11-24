// Package imap provides an IMAP adapter wrapping go-imap/v2.
package imap

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"sync"
	"time"

	imap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/zhecho/m9s/internal/config"
	mail "github.com/zhecho/m9s/internal/mail"
)

// logoutTimeout bounds the LOGOUT exchange when closing a connection.
const logoutTimeout = 3 * time.Second

// ErrAuth is wrapped by Connect when the server rejects the credentials.
var ErrAuth = errors.New("imap: authentication failed")

// loginError classifies a failed LOGIN. Only a NO answer means the server
// rejected the credentials; NO [UNAVAILABLE], BAD, BYE and I/O errors (the
// socket dropping mid-login while the network changes) are transient and must
// stay eligible for automatic retry.
func loginError(err error) error {
	var imapErr *imap.Error
	if errors.As(err, &imapErr) && imapErr.Type == imap.StatusResponseTypeNo &&
		imapErr.Code != imap.ResponseCodeUnavailable {
		return fmt.Errorf("%w: %w", ErrAuth, err)
	}
	return fmt.Errorf("imap login: %w", err)
}

// IDLENotification is sent when the server pushes an unsolicited update.
type IDLENotification struct {
	Folder  string
	Exists  uint32 // new message count, 0 if not updated
	Expunge uint32 // expunged seq number, 0 if not applicable
}

// Client is the interface for IMAP operations used by the rest of m9s.
type Client interface {
	Connect(ctx context.Context, password string) error
	Close() error
	// Closed returns a channel that is closed when the current connection
	// ends for any reason, or nil if there is no connection.
	Closed() <-chan struct{}
	// Abort drops the current connection without LOGOUT or IDLE DONE, so a
	// half-open socket cannot stall the caller. Commands in flight fail.
	Abort()
	Folders(ctx context.Context) ([]*mail.Folder, error)
	FetchHeaders(ctx context.Context, folder string, page, pageSize int) ([]*mail.Envelope, error)
	FetchBody(ctx context.Context, folder string, uid uint32) (*mail.Message, error)
	Store(ctx context.Context, folder string, uid uint32, flags []string, add bool) error
	Search(ctx context.Context, folder string, criteria *imap.SearchCriteria) ([]uint32, error)
	// FetchEnvelopes returns the headers of the given messages.
	FetchEnvelopes(ctx context.Context, folder string, uids []uint32) ([]*mail.Envelope, error)
	StartIDLE(ctx context.Context, folder string, notify chan<- IDLENotification) error
	StopIDLE()
	Status(ctx context.Context, mailbox string) (*mail.MailboxStatus, error)
	Append(ctx context.Context, mailbox string, data []byte, flags []imap.Flag) (uint32, error)
	Move(ctx context.Context, srcFolder string, uid uint32, dstFolder string) error
	Expunge(ctx context.Context, folder string, uid uint32) error
	// ExpungeAll permanently deletes every message in folder and returns
	// how many were removed.
	ExpungeAll(ctx context.Context, folder string) (int, error)
}

type imapClient struct {
	cfg *config.IMAPConfig
	mu  sync.Mutex // guards c, idleCmd, idleCancel
	c   *imapclient.Client

	// opMu serialises mailbox-scoped operations. They SELECT a folder and
	// then act on UIDs in it; on one shared connection a concurrent SELECT
	// in between would make the action (a delete!) hit the other folder.
	// Close and Abort never take it, so they can interrupt a hung operation.
	opMu sync.Mutex

	idleCmd    *imapclient.IdleCommand
	idleCancel context.CancelFunc
	idleNotify chan<- IDLENotification // set while IDLE runs
	idleFolder string
}

// New creates a new IMAP Client (not yet connected).
func New(cfg *config.IMAPConfig) Client {
	return &imapClient{cfg: cfg}
}

// Connect dials the server and authenticates with LOGIN.
func (ic *imapClient) Connect(_ context.Context, password string) error {
	addr := fmt.Sprintf("%s:%d", ic.cfg.Host, ic.cfg.Port)

	opts := &imapclient.Options{
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(data *imapclient.UnilateralDataMailbox) {
				if data.NumMessages != nil {
					ic.pushIdle(IDLENotification{Exists: *data.NumMessages})
				}
			},
			Expunge: func(seqNum uint32) {
				ic.pushIdle(IDLENotification{Expunge: seqNum})
			},
		},
	}
	if ic.cfg.TLSSkipVerify {
		opts.TLSConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}

	var (
		c   *imapclient.Client
		err error
	)
	if ic.cfg.TLS {
		c, err = imapclient.DialTLS(addr, opts)
	} else {
		c, err = imapclient.DialInsecure(addr, opts)
	}
	if err != nil {
		return fmt.Errorf("imap dial %s: %w", addr, err)
	}

	if err := c.Login(ic.cfg.User, password).Wait(); err != nil {
		_ = c.Close()
		return loginError(err)
	}

	ic.mu.Lock()
	ic.c = c
	ic.mu.Unlock()
	return nil
}

// ServerIdentifier is a Client that can ask the server who it is.
type ServerIdentifier interface {
	// ServerID returns the server's IMAP ID answer (RFC 2971); nil with no
	// error when the server does not support ID.
	ServerID(ctx context.Context) (*imap.IDData, error)
}

var _ ServerIdentifier = (*imapClient)(nil)

// ServerID sends IMAP ID with no client details and returns the answer.
func (ic *imapClient) ServerID(_ context.Context) (*imap.IDData, error) {
	ic.mu.Lock()
	c := ic.c
	ic.mu.Unlock()
	if c == nil {
		return nil, fmt.Errorf("imap: not connected")
	}
	if !c.Caps().Has(imap.CapID) {
		return nil, nil
	}
	data, err := c.ID(nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("imap ID: %w", err)
	}
	return data, nil
}

// Close logs out and closes the connection.
func (ic *imapClient) Close() error {
	ic.StopIDLE()
	// Take the client out under the lock, but do not hold the lock while
	// waiting for LOGOUT: go-imap's reader goroutine calls pushIdle, which
	// needs the lock, and must keep reading for the LOGOUT reply to arrive.
	ic.mu.Lock()
	c := ic.c
	ic.c = nil
	ic.mu.Unlock()
	if c == nil {
		return nil
	}
	var err error
	select {
	case <-c.Closed():
		// Already dead: a LOGOUT would only wait on a broken socket.
	default:
		// Bound the polite logout so a half-open connection cannot stall a reconnect.
		done := make(chan error, 1)
		go func() { done <- c.Logout().Wait() }()
		select {
		case err = <-done:
		case <-time.After(logoutTimeout):
			err = fmt.Errorf("imap logout: timed out after %s", logoutTimeout)
		}
	}
	_ = c.Close()
	return err
}

// Abort closes the socket of the current connection without any goodbye.
func (ic *imapClient) Abort() {
	ic.mu.Lock()
	c := ic.c
	ic.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// Closed returns a channel closed when the current connection ends, or nil
// when not connected.
func (ic *imapClient) Closed() <-chan struct{} {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if ic.c == nil {
		return nil
	}
	return ic.c.Closed()
}

// Folders lists all mailboxes.
func (ic *imapClient) Folders(_ context.Context) ([]*mail.Folder, error) {
	ic.mu.Lock()
	c := ic.c
	ic.mu.Unlock()
	if c == nil {
		return nil, fmt.Errorf("imap: not connected")
	}

	listCmd := c.List("", "*", &imap.ListOptions{})
	items, err := listCmd.Collect()
	if err != nil {
		return nil, fmt.Errorf("imap LIST: %w", err)
	}

	folders := make([]*mail.Folder, 0, len(items))
	for _, item := range items {
		delim := ""
		if item.Delim != 0 {
			delim = string(item.Delim)
		}
		attrs := make([]string, 0, len(item.Attrs))
		for _, a := range item.Attrs {
			attrs = append(attrs, string(a))
		}
		folders = append(folders, &mail.Folder{
			Name:       item.Mailbox,
			Delimiter:  delim,
			Attributes: attrs,
		})
	}
	return folders, nil
}

// Store adds or removes flags on the given UID.
func (ic *imapClient) Store(_ context.Context, folder string, uid uint32, flags []string, add bool) error {
	ic.opMu.Lock() // SELECT and the command that follows must not interleave
	defer ic.opMu.Unlock()
	ic.mu.Lock()
	c := ic.c
	ic.mu.Unlock()
	if c == nil {
		return fmt.Errorf("imap: not connected")
	}

	if _, err := c.Select(folder, nil).Wait(); err != nil {
		return fmt.Errorf("imap SELECT %s: %w", folder, err)
	}

	op := imap.StoreFlagsDel
	if add {
		op = imap.StoreFlagsAdd
	}

	imapFlags := make([]imap.Flag, len(flags))
	for i, f := range flags {
		imapFlags[i] = imap.Flag(f)
	}

	uidSet := imap.UIDSetNum(imap.UID(uid))
	storeCmd := c.Store(uidSet, &imap.StoreFlags{
		Op:     op,
		Flags:  imapFlags,
		Silent: true,
	}, nil)
	return storeCmd.Close()
}

// StartIDLE enters IDLE on folder and sends notifications to ch.
func (ic *imapClient) StartIDLE(ctx context.Context, folder string, notify chan<- IDLENotification) error {
	ic.opMu.Lock() // SELECT and the command that follows must not interleave
	defer ic.opMu.Unlock()
	ic.mu.Lock()
	c := ic.c
	ic.mu.Unlock()
	if c == nil {
		return fmt.Errorf("imap: not connected")
	}

	if _, err := c.Select(folder, nil).Wait(); err != nil {
		return fmt.Errorf("imap SELECT %s for IDLE: %w", folder, err)
	}

	// From here on the unilateral handler installed in Connect forwards
	// EXISTS / EXPUNGE for this folder to notify. go-imap re-issues IDLE
	// every 28 minutes by itself, before servers drop an idle client.
	ic.mu.Lock()
	ic.idleNotify, ic.idleFolder = notify, folder
	ic.mu.Unlock()

	idleCmd, err := c.Idle()
	if err != nil {
		ic.mu.Lock()
		ic.idleNotify, ic.idleFolder = nil, ""
		ic.mu.Unlock()
		return fmt.Errorf("imap IDLE: %w", err)
	}

	ic.mu.Lock()
	ic.idleCmd = idleCmd
	ic.mu.Unlock()

	go func() {
		<-ctx.Done()
		ic.stopIdle(idleCmd) // only this IDLE: a later StartIDLE owns its own
	}()

	return nil
}

// StopIDLE ends the current IDLE, if any, and stops forwarding updates.
func (ic *imapClient) StopIDLE() {
	ic.mu.Lock()
	cmd := ic.idleCmd
	ic.mu.Unlock()
	ic.stopIdle(cmd)
}

func (ic *imapClient) stopIdle(cmd *imapclient.IdleCommand) {
	if cmd == nil {
		return
	}
	ic.mu.Lock()
	current := ic.idleCmd == cmd
	if current {
		ic.idleCmd = nil
		ic.idleNotify, ic.idleFolder = nil, ""
	}
	ic.mu.Unlock()
	if current {
		_ = cmd.Close()
	}
}

// pushIdle forwards one server update while IDLE is running. It never
// blocks: it runs on go-imap's reader goroutine, and the engine reloads the
// whole folder anyway, so a dropped duplicate loses nothing.
func (ic *imapClient) pushIdle(n IDLENotification) {
	ic.mu.Lock()
	notify, folder := ic.idleNotify, ic.idleFolder
	ic.mu.Unlock()
	if notify == nil {
		return
	}
	n.Folder = folder
	select {
	case notify <- n:
	default:
	}
}

// Status retrieves mailbox status information (message count, unseen count).
func (ic *imapClient) Status(_ context.Context, mailbox string) (*mail.MailboxStatus, error) {
	ic.mu.Lock()
	c := ic.c
	ic.mu.Unlock()
	if c == nil {
		return nil, fmt.Errorf("imap: not connected")
	}
	opts := &imap.StatusOptions{NumMessages: true, NumUnseen: true}
	data, err := c.Status(mailbox, opts).Wait()
	if err != nil {
		return nil, fmt.Errorf("imap STATUS %s: %w", mailbox, err)
	}
	ms := &mail.MailboxStatus{}
	if data.NumMessages != nil {
		ms.Messages = *data.NumMessages
	}
	if data.NumUnseen != nil {
		ms.Unseen = *data.NumUnseen
	}
	return ms, nil
}

// Append appends a message to a mailbox and returns the assigned UID.
// flags is optional. The AppendData from go-imap/v2 requires UIDPLUS or IMAP4rev2.
func (ic *imapClient) Append(_ context.Context, mailbox string, data []byte, flags []imap.Flag) (uint32, error) {
	ic.mu.Lock()
	c := ic.c
	ic.mu.Unlock()
	if c == nil {
		return 0, fmt.Errorf("imap: not connected")
	}

	opts := &imap.AppendOptions{}
	if len(flags) > 0 {
		opts.Flags = flags
	}

	appendCmd := c.Append(mailbox, int64(len(data)), opts)
	if _, err := appendCmd.Write(data); err != nil {
		_ = appendCmd.Close()
		return 0, fmt.Errorf("imap APPEND write: %w", err)
	}
	if err := appendCmd.Close(); err != nil {
		return 0, fmt.Errorf("imap APPEND close: %w", err)
	}

	result, err := appendCmd.Wait()
	if err != nil {
		return 0, fmt.Errorf("imap APPEND %s: %w", mailbox, err)
	}

	return uint32(result.UID), nil
}

// Move moves uid from srcFolder to dstFolder using IMAP MOVE (RFC 6851).
// The go-imap/v2 library transparently falls back to COPY + STORE \Deleted +
// EXPUNGE when the server lacks the MOVE capability.
func (ic *imapClient) Move(_ context.Context, srcFolder string, uid uint32, dstFolder string) error {
	ic.opMu.Lock() // SELECT and the command that follows must not interleave
	defer ic.opMu.Unlock()
	ic.mu.Lock()
	c := ic.c
	ic.mu.Unlock()
	if c == nil {
		return fmt.Errorf("imap: not connected")
	}

	if _, err := c.Select(srcFolder, nil).Wait(); err != nil {
		return fmt.Errorf("imap SELECT %s: %w", srcFolder, err)
	}

	uidSet := imap.UIDSetNum(imap.UID(uid))
	if _, err := c.Move(uidSet, dstFolder).Wait(); err != nil {
		return fmt.Errorf("imap MOVE %d → %s: %w", uid, dstFolder, err)
	}
	return nil
}

// Expunge marks uid \Deleted in folder and expunges it.
// Uses UID EXPUNGE (UIDPLUS) for precision; falls back to EXPUNGE if
// UIDPLUS is unavailable.
func (ic *imapClient) Expunge(_ context.Context, folder string, uid uint32) error {
	ic.opMu.Lock() // SELECT and the command that follows must not interleave
	defer ic.opMu.Unlock()
	ic.mu.Lock()
	c := ic.c
	ic.mu.Unlock()
	if c == nil {
		return fmt.Errorf("imap: not connected")
	}

	if _, err := c.Select(folder, nil).Wait(); err != nil {
		return fmt.Errorf("imap SELECT %s: %w", folder, err)
	}

	uidSet := imap.UIDSetNum(imap.UID(uid))
	storeCmd := c.Store(uidSet, &imap.StoreFlags{
		Op:     imap.StoreFlagsAdd,
		Flags:  []imap.Flag{imap.FlagDeleted},
		Silent: true,
	}, nil)
	if err := storeCmd.Close(); err != nil {
		return fmt.Errorf("imap STORE \\Deleted on uid %d: %w", uid, err)
	}

	if err := c.UIDExpunge(uidSet).Close(); err != nil {
		return fmt.Errorf("imap UID EXPUNGE %d: %w", uid, err)
	}
	return nil
}

// ExpungeAll marks every message in folder \Deleted and expunges them.
func (ic *imapClient) ExpungeAll(_ context.Context, folder string) (int, error) {
	ic.opMu.Lock() // SELECT and the command that follows must not interleave
	defer ic.opMu.Unlock()
	ic.mu.Lock()
	c := ic.c
	ic.mu.Unlock()
	if c == nil {
		return 0, fmt.Errorf("imap: not connected")
	}

	sel, err := c.Select(folder, nil).Wait()
	if err != nil {
		return 0, fmt.Errorf("imap SELECT %s: %w", folder, err)
	}
	if sel.NumMessages == 0 {
		return 0, nil
	}
	var all imap.SeqSet
	all.AddRange(1, 0) // 1:* — every message in the folder
	storeCmd := c.Store(all, &imap.StoreFlags{
		Op:     imap.StoreFlagsAdd,
		Flags:  []imap.Flag{imap.FlagDeleted},
		Silent: true,
	}, nil)
	if err := storeCmd.Close(); err != nil {
		return 0, fmt.Errorf("imap STORE \\Deleted on all of %s: %w", folder, err)
	}
	expunged, err := c.Expunge().Collect()
	if err != nil {
		return 0, fmt.Errorf("imap EXPUNGE %s: %w", folder, err)
	}
	return len(expunged), nil
}
