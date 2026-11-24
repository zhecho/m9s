package imap_test

import (
	"context"
	"testing"
	"time"

	imap "github.com/emersion/go-imap/v2"

	imapadapter "github.com/zhecho/m9s/internal/mail/imap"
)

func awaitNotification(t *testing.T, notify <-chan imapadapter.IDLENotification, match func(imapadapter.IDLENotification) bool) imapadapter.IDLENotification {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case n := <-notify:
			if match(n) {
				return n
			}
		case <-timeout:
			t.Fatal("no IDLE notification within 3s")
			return imapadapter.IDLENotification{}
		}
	}
}

// TestClient_idlePushesNewMail: a message arriving in INBOX while the idle
// connection waits is pushed to the notify channel.
func TestClient_idlePushesNewMail(t *testing.T) {
	idle, other := startMemServerPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notify := make(chan imapadapter.IDLENotification, 8)
	if err := idle.StartIDLE(ctx, "INBOX", notify); err != nil {
		t.Fatalf("StartIDLE: %v", err)
	}

	if _, err := other.Append(ctx, "INBOX", []byte(testMsg), nil); err != nil {
		t.Fatal(err)
	}

	n := awaitNotification(t, notify, func(n imapadapter.IDLENotification) bool { return n.Exists > 0 })
	if n.Folder != "INBOX" || n.Exists != 1 {
		t.Errorf("notification = %+v, want INBOX with 1 message", n)
	}
}

// TestClient_idlePushesExpunge: a message removed by another client is pushed too.
func TestClient_idlePushesExpunge(t *testing.T) {
	idle, other := startMemServerPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := other.Append(ctx, "INBOX", []byte(testMsg), nil); err != nil {
		t.Fatal(err)
	}
	notify := make(chan imapadapter.IDLENotification, 8)
	if err := idle.StartIDLE(ctx, "INBOX", notify); err != nil {
		t.Fatalf("StartIDLE: %v", err)
	}

	if err := other.Expunge(ctx, "INBOX", 1); err != nil {
		t.Fatal(err)
	}

	n := awaitNotification(t, notify, func(n imapadapter.IDLENotification) bool { return n.Expunge > 0 })
	if n.Folder != "INBOX" {
		t.Errorf("notification = %+v, want INBOX", n)
	}
}

// TestClient_noPushAfterStop: once IDLE is stopped nothing is sent any more.
func TestClient_noPushAfterStop(t *testing.T) {
	idle, other := startMemServerPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	notify := make(chan imapadapter.IDLENotification, 8)
	if err := idle.StartIDLE(ctx, "INBOX", notify); err != nil {
		t.Fatal(err)
	}
	cancel()
	time.Sleep(100 * time.Millisecond) // let the stop goroutine run
	if _, err := other.Append(context.Background(), "INBOX", []byte(testMsg), nil); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-notify:
		t.Errorf("notification after IDLE stopped: %+v", n)
	case <-time.After(300 * time.Millisecond):
	}
}

var _ = imap.CapIdle

// TestClient_closeDuringNewMailFloodIsPrompt guards against Close holding the
// client lock while the reader goroutine delivers IDLE updates.
func TestClient_closeDuringNewMailFloodIsPrompt(t *testing.T) {
	idle, other := startMemServerPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notify := make(chan imapadapter.IDLENotification) // unbuffered and never read
	if err := idle.StartIDLE(ctx, "INBOX", notify); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = other.Append(context.Background(), "INBOX", []byte(testMsg), nil)
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)

	start := time.Now()
	_ = idle.Close()
	if d := time.Since(start); d > time.Second {
		t.Errorf("Close took %s during a flood of new mail, want well under 1s", d)
	}
}
