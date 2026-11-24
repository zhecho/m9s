package imap_test

import (
	"context"
	"io"
	"log"
	"net"
	"sync"
	"testing"

	imap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/zhecho/m9s/internal/config"
	imapadapter "github.com/zhecho/m9s/internal/mail/imap"
)

// startMemServer runs an in-memory IMAP server with folders A and B and
// returns a connected adapter for it.
func startMemServer(t *testing.T) imapadapter.Client {
	t.Helper()
	c, _ := startMemServerPair(t)
	return c
}

// startMemServerPair returns two adapters connected to the same in-memory
// server and user, like m9s's command and idle connections.
func startMemServerPair(t *testing.T) (imapadapter.Client, imapadapter.Client) {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("u", "p")
	for _, name := range []string{"INBOX", "A", "B"} {
		if err := user.Create(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	mem.AddUser(user)

	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapUIDPlus: {}, imap.CapMove: {}, imap.CapIdle: {}},
		InsecureAuth: true,
		Logger:       log.New(io.Discard, "", 0),
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { _ = srv.Close() })

	port := ln.Addr().(*net.TCPAddr).Port
	connect := func() imapadapter.Client {
		c := imapadapter.New(&config.IMAPConfig{Host: "127.0.0.1", Port: port, User: "u"})
		if err := c.Connect(context.Background(), "p"); err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	return connect(), connect()
}

const testMsg = "From: a@example.com\r\nTo: b@example.com\r\nSubject: hi\r\n\r\nbody\r\n"

// TestClient_mailboxOperationsDoNotInterleave reproduces the shared-connection
// race: an action on folder A (SELECT A, then STORE uid 1) must never land on
// folder B because a concurrent load of B sent its SELECT in between. Both
// folders hold a message with UID 1, so an interleaving flags B's message.
func TestClient_mailboxOperationsDoNotInterleave(t *testing.T) {
	c := startMemServer(t)
	ctx := context.Background()
	for _, name := range []string{"A", "B"} {
		if _, err := c.Append(ctx, name, []byte(testMsg), nil); err != nil {
			t.Fatalf("append to %s: %v", name, err)
		}
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() { // the user hovering over folder B
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = c.FetchHeaders(ctx, "B", 1, 50)
			}
		}
	}()
	for i := 0; i < 300; i++ { // acting on the message in folder A
		if err := c.Store(ctx, "A", 1, []string{`\Flagged`}, true); err != nil {
			t.Fatalf("store: %v", err)
		}
	}
	close(stop)
	wg.Wait()

	envs, err := c.FetchHeaders(ctx, "B", 1, 50)
	if err != nil || len(envs) != 1 {
		t.Fatalf("FetchHeaders(B) = %d envelopes, %v", len(envs), err)
	}
	if envs[0].IsFlagged() {
		t.Fatal("an action meant for folder A changed the message in folder B")
	}
}

// TestClient_deleteNeverHitsAnotherFolder is the destructive form: deleting
// UID 1 in A while B is being loaded must leave B's message in place.
func TestClient_deleteNeverHitsAnotherFolder(t *testing.T) {
	c := startMemServer(t)
	ctx := context.Background()
	for i := 0; i < 60; i++ {
		if _, err := c.Append(ctx, "A", []byte(testMsg), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Append(ctx, "B", []byte(testMsg), nil); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = c.FetchHeaders(ctx, "B", 1, 50)
			}
		}
	}()
	for uid := uint32(1); uid <= 60; uid++ {
		_ = c.Expunge(ctx, "A", uid)
	}
	close(stop)
	wg.Wait()

	envs, err := c.FetchHeaders(ctx, "B", 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 1 {
		t.Fatalf("folder B has %d messages, want 1: a delete in A removed mail from B", len(envs))
	}
}

func TestClient_expungeAllEmptiesOnlyThatFolder(t *testing.T) {
	c := startMemServer(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := c.Append(ctx, "A", []byte(testMsg), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Append(ctx, "B", []byte(testMsg), nil); err != nil {
		t.Fatal(err)
	}

	n, err := c.ExpungeAll(ctx, "A")
	if err != nil {
		t.Fatalf("ExpungeAll: %v", err)
	}
	if n != 3 {
		t.Errorf("ExpungeAll reported %d messages, want 3", n)
	}
	if envs, _ := c.FetchHeaders(ctx, "A", 1, 50); len(envs) != 0 {
		t.Errorf("folder A still has %d messages", len(envs))
	}
	if envs, _ := c.FetchHeaders(ctx, "B", 1, 50); len(envs) != 1 {
		t.Errorf("folder B has %d messages, want its 1 untouched", len(envs))
	}
}

func TestClient_expungeAllOnEmptyFolder(t *testing.T) {
	c := startMemServer(t)
	n, err := c.ExpungeAll(context.Background(), "A")
	if err != nil || n != 0 {
		t.Errorf("ExpungeAll on an empty folder = %d, %v; want 0, nil", n, err)
	}
}
