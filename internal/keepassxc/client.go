// Package keepassxc asks a running KeePassXC for logins, and stores them,
// over its browser-integration socket. KeePassXC does the unlocking (Touch ID
// included) and the approving in its own windows; this package never sees
// the database's master password.
//
// Only the address search is offered. Looking an entry up by its id
// (keepassxc://by-uuid/…) crashed KeePassXC 2.8.0 for entries without a URL,
// so that request does not exist here.
package keepassxc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"

	"golang.org/x/crypto/nacl/box"
)

var (
	ErrUnavailable = errors.New("KeePassXC is not running or its browser integration is off (Settings → Browser Integration)")
	ErrNotRunning  = errors.New("KeePassXC is not running")
	ErrLocked      = errors.New("KeePassXC database is locked")
	ErrNoLogins    = errors.New("no logins found")
	ErrClosed      = errors.New("KeePassXC closed the connection")
)

// ProtocolError is an error answer from KeePassXC other than "locked" and
// "no logins".
type ProtocolError struct{ Code, Message string }

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("KeePassXC: %s (code %s)", e.Message, e.Code)
}

// Association is the approval a user gave this program in KeePassXC: the
// name they typed and the public key it was given for.
type Association struct{ ID, Key string }

// Login is one KeePassXC entry.
type Login struct{ Name, User, Password, UUID, Group string }

var b64 = base64.StdEncoding

// Client is one connection to KeePassXC. Calls are serialised.
type Client struct {
	conn      net.Conn
	clientID  string
	pub, priv *[32]byte
	serverPub [32]byte

	call sync.Mutex          // one request at a time
	msgs chan map[string]any // everything KeePassXC sends, in order
	done chan struct{}       // closed when the connection is gone
}

// Dial connects to the socket at socketPath and exchanges keys.
func Dial(ctx context.Context, socketPath string) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w (%v)", ErrNotRunning, err)
	}
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		conn.Close()
		return nil, err
	}
	c := &Client{conn: conn, clientID: randomB64(24), pub: pub, priv: priv,
		msgs: make(chan map[string]any, 64), done: make(chan struct{})}
	go c.readLoop()

	c.call.Lock()
	defer c.call.Unlock()
	if err := c.send(map[string]any{"action": "change-public-keys",
		"publicKey": b64.EncodeToString(pub[:]), "nonce": randomB64(24), "clientID": c.clientID}); err != nil {
		c.Close()
		return nil, err
	}
	resp, err := c.next(ctx, func(m map[string]any) bool { return m["action"] == "change-public-keys" })
	if err == nil {
		err = clearError(resp)
	}
	if err == nil {
		raw, decErr := b64.DecodeString(str(resp, "publicKey"))
		if decErr != nil || len(raw) != 32 {
			err = errors.New("KeePassXC sent no usable key")
		}
		copy(c.serverPub[:], raw)
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Close ends the connection.
func (c *Client) Close() error { return c.conn.Close() }

// readLoop hands every message to msgs until the connection ends. A full
// queue drops the message: nobody is waiting, so it is a stale notice.
func (c *Client) readLoop() {
	defer close(c.done)
	dec := json.NewDecoder(c.conn)
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			return
		}
		select {
		case c.msgs <- m:
		default:
		}
	}
}

func (c *Client) send(v map[string]any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := c.conn.Write(data); err != nil {
		return ErrClosed
	}
	return nil
}

// next returns the first message want accepts, skipping the others.
func (c *Client) next(ctx context.Context, want func(map[string]any) bool) (map[string]any, error) {
	for {
		select {
		case m := <-c.msgs:
			if want(m) {
				return m, nil
			}
		case <-c.done:
			// Drain what arrived before the connection ended.
			select {
			case m := <-c.msgs:
				if want(m) {
					return m, nil
				}
				continue
			default:
			}
			return nil, ErrClosed
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// request sends one encrypted request and returns the decrypted answer.
// The answer is recognised by its nonce (the request's plus one), so a late
// answer to an abandoned request, or a notice, is never mistaken for it.
func (c *Client) request(ctx context.Context, action string, body map[string]any, triggerUnlock bool) (map[string]any, error) {
	c.call.Lock()
	defer c.call.Unlock()

	body["action"] = action
	plain, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	req := map[string]any{
		"action":   action,
		"message":  b64.EncodeToString(box.Seal(nil, plain, &nonce, &c.serverPub, c.priv)),
		"nonce":    b64.EncodeToString(nonce[:]),
		"clientID": c.clientID,
	}
	if triggerUnlock {
		req["triggerUnlock"] = "true"
	}
	if err := c.send(req); err != nil {
		return nil, err
	}

	replyNonce := incremented(nonce)
	wantNonce := b64.EncodeToString(replyNonce[:])
	resp, err := c.next(ctx, func(m map[string]any) bool {
		if m["action"] != action {
			return false // a notice
		}
		// Errors come back in clear, without a nonce.
		return str(m, "error") != "" || str(m, "nonce") == wantNonce
	})
	if err != nil {
		if ctx.Err() != nil {
			// The answer may still come. An error answer carries no nonce,
			// so it could be taken for the next request's: end the
			// connection rather than risk that.
			c.Close()
		}
		return nil, err
	}
	if err := clearError(resp); err != nil {
		return nil, err
	}
	sealed, err := b64.DecodeString(str(resp, "message"))
	if err != nil {
		return nil, errors.New("KeePassXC sent an unreadable answer")
	}
	out, ok := box.Open(nil, sealed, &replyNonce, &c.serverPub, c.priv)
	if !ok {
		return nil, errors.New("KeePassXC sent an answer that could not be decrypted")
	}
	var answer map[string]any
	if err := json.Unmarshal(out, &answer); err != nil {
		return nil, errors.New("KeePassXC sent an unreadable answer")
	}
	return answer, nil
}

// IsOpen reports whether the database is unlocked (nil) or not (ErrLocked).
// With triggerUnlock KeePassXC is asked to show its unlock window.
func (c *Client) IsOpen(ctx context.Context, triggerUnlock bool) error {
	_, err := c.request(ctx, "get-databasehash", map[string]any{}, triggerUnlock)
	return err
}

// WaitUnlocked blocks until KeePassXC announces that the database was
// unlocked, or ctx ends.
func (c *Client) WaitUnlocked(ctx context.Context) error {
	c.call.Lock()
	defer c.call.Unlock()
	_, err := c.next(ctx, func(m map[string]any) bool { return m["action"] == "database-unlocked" })
	return err
}

// Associate asks the user, in KeePassXC, to allow this program and name the
// connection. The result is what later calls identify themselves with.
func (c *Client) Associate(ctx context.Context) (Association, error) {
	idPub, _, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return Association{}, err
	}
	a := Association{Key: b64.EncodeToString(idPub[:])}
	resp, err := c.request(ctx, "associate", map[string]any{
		"key": b64.EncodeToString(c.pub[:]), "idKey": a.Key}, false)
	if err != nil {
		return Association{}, err
	}
	a.ID = str(resp, "id")
	if a.ID == "" {
		return Association{}, errors.New("KeePassXC did not name the connection")
	}
	return a, nil
}

// TestAssociate reports whether KeePassXC still accepts a.
func (c *Client) TestAssociate(ctx context.Context, a Association) error {
	_, err := c.request(ctx, "test-associate", map[string]any{"id": a.ID, "key": a.Key}, false)
	return err
}

// Logins returns the entries whose URL field matches url, which callers
// give as "https://<host>". ErrNoLogins if there are none.
func (c *Client) Logins(ctx context.Context, url string, a Association) ([]Login, error) {
	resp, err := c.request(ctx, "get-logins", map[string]any{
		"url": url, "keys": []map[string]any{{"id": a.ID, "key": a.Key}}}, false)
	if err != nil {
		return nil, err
	}
	entries, _ := resp["entries"].([]any)
	out := make([]Login, 0, len(entries))
	for _, e := range entries {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, Login{Name: str(m, "name"), User: str(m, "login"),
			Password: str(m, "password"), UUID: str(m, "uuid"), Group: str(m, "group")})
	}
	if len(out) == 0 {
		return nil, ErrNoLogins
	}
	return out, nil
}

// Group is one group of the database, with the groups inside it.
type Group struct {
	Name     string  `json:"name"`
	UUID     string  `json:"uuid"`
	Children []Group `json:"children,omitempty"`
}

// Groups returns the database's group tree, the root group first. KeePassXC
// answers only on a connection it has approved (Associate or TestAssociate
// first).
func (c *Client) Groups(ctx context.Context) ([]Group, error) {
	resp, err := c.request(ctx, "get-database-groups", map[string]any{}, false)
	if err != nil {
		return nil, err
	}
	// KeePassXC nests the list: {"groups": {"groups": [root]}}; a flat
	// {"groups": [root]} is read too.
	raw := resp["groups"]
	if m, ok := raw.(map[string]any); ok {
		raw = m["groups"]
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var groups []Group
	if err := json.Unmarshal(data, &groups); err != nil {
		return nil, errors.New("KeePassXC sent an unreadable group list")
	}
	return groups, nil
}

// SetLogin stores password for login in the entry whose URL field is url:
// it updates the entry with id uuid, or creates a new one when uuid is "".
// KeePassXC may ask the user to confirm, and puts new entries in its
// default group.
func (c *Client) SetLogin(ctx context.Context, url, login, password, uuid string, a Association) error {
	resp, err := c.request(ctx, "set-login", map[string]any{
		"url": url, "submitUrl": url, "id": a.ID, "nonce": randomB64(24),
		"login": login, "password": password,
		"group": "", "groupUuid": "", "uuid": uuid, "downloadFavicon": "false"}, false)
	if err != nil {
		return err
	}
	if str(resp, "success") != "true" {
		return errors.New("KeePassXC did not store the entry")
	}
	return nil
}

// clearError turns an error answer into an error.
func clearError(m map[string]any) error {
	msg := str(m, "error")
	if msg == "" {
		return nil
	}
	switch code := str(m, "errorCode"); code {
	case "1":
		return ErrLocked
	case "15":
		return ErrNoLogins
	default:
		return &ProtocolError{Code: code, Message: msg}
	}
}

func str(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

// incremented returns n plus one, as libsodium counts (little-endian).
func incremented(n [24]byte) [24]byte {
	for i := range n {
		n[i]++
		if n[i] != 0 {
			break
		}
	}
	return n
}

func randomB64(size int) string {
	buf := make([]byte, size)
	rand.Read(buf)
	return b64.EncodeToString(buf)
}
