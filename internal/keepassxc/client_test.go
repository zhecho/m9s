package keepassxc

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mkShortDir(fallback string) (string, error) {
	dir, err := os.MkdirTemp("/tmp", "kp")
	if err != nil {
		return fallback, nil
	}
	return dir, nil
}

func ctx1s(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}

func dial(t *testing.T, s *fakeServer) *Client {
	t.Helper()
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(s.path)) })
	c, err := Dial(ctx1s(t), s.path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func associated(t *testing.T, s *fakeServer, c *Client) Association {
	t.Helper()
	a, err := c.Associate(ctx1s(t))
	if err != nil {
		t.Fatalf("Associate: %v", err)
	}
	return a
}

func TestSocketPath_noSocketIsUnavailable(t *testing.T) {
	empty := t.TempDir()
	t.Setenv("TMPDIR", empty)
	t.Setenv("XDG_RUNTIME_DIR", empty)
	if _, err := SocketPath(); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestSocketPath_findsTheSocketInTMPDIR(t *testing.T) {
	s := newFakeServer(t)
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(s.path)) })
	t.Setenv("TMPDIR", filepath.Dir(s.path))
	t.Setenv("XDG_RUNTIME_DIR", "")
	if got, err := SocketPath(); err != nil || got != s.path {
		t.Errorf("SocketPath = %q, %v; want %q", got, err, s.path)
	}
}

func TestDial_socketFileButNobodyListening(t *testing.T) {
	dir, _ := mkShortDir(t.TempDir())
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, socketName)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close() // the file stays, nobody listens: KeePassXC was killed
	if _, err := Dial(ctx1s(t), path); !errors.Is(err, ErrNotRunning) {
		t.Errorf("err = %v, want ErrNotRunning", err)
	}
}

func TestClient_isOpen(t *testing.T) {
	s := newFakeServer(t)
	c := dial(t, s)
	if err := c.IsOpen(ctx1s(t), false); err != nil {
		t.Errorf("open database: %v", err)
	}
	s.set(func() { s.locked = true })
	if err := c.IsOpen(ctx1s(t), true); !errors.Is(err, ErrLocked) {
		t.Errorf("locked database: err = %v, want ErrLocked", err)
	}
	s.set(func() {
		if len(s.unlockAsked) != 2 || s.unlockAsked[0] || !s.unlockAsked[1] {
			t.Errorf("triggerUnlock sent = %v, want [false true]", s.unlockAsked)
		}
	})
}

func TestClient_waitUnlockedReturnsOnNotice(t *testing.T) {
	s := newFakeServer(t)
	s.locked = true
	c := dial(t, s)
	go func() { time.Sleep(20 * time.Millisecond); s.unlock() }()
	if err := c.WaitUnlocked(ctx1s(t)); err != nil {
		t.Fatalf("WaitUnlocked: %v", err)
	}
	if err := c.IsOpen(ctx1s(t), false); err != nil {
		t.Errorf("after the unlock notice the connection must still work: %v", err)
	}
}

func TestClient_waitUnlockedHonoursContext(t *testing.T) {
	s := newFakeServer(t)
	s.locked = true
	c := dial(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := c.WaitUnlocked(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > time.Second {
		t.Error("WaitUnlocked did not stop with its context")
	}
	// Giving up a wait must not break the connection.
	s.set(func() { s.locked = false })
	if err := c.IsOpen(ctx1s(t), false); err != nil {
		t.Errorf("connection unusable after a wait timed out: %v", err)
	}
}

func TestClient_associateThenTestAssociate(t *testing.T) {
	s := newFakeServer(t)
	c := dial(t, s)
	a := associated(t, s, c)
	if a.ID != "m9s" || a.Key == "" {
		t.Fatalf("association = %+v, want the id KeePassXC chose and a key", a)
	}
	if err := c.TestAssociate(ctx1s(t), a); err != nil {
		t.Errorf("TestAssociate: %v", err)
	}
	var pe *ProtocolError
	if err := c.TestAssociate(ctx1s(t), Association{ID: "stranger", Key: "x"}); !errors.As(err, &pe) {
		t.Errorf("unknown association: err = %v, want *ProtocolError", err)
	}
}

func TestClient_loginsReturnsPasswordsVerbatim(t *testing.T) {
	s := newFakeServer(t)
	pw := " p\"ä ss\n "
	s.logins["https://mail.x.io"] = []Login{{Name: "work", User: "a@x.io", Password: pw, UUID: "u1", Group: "Mail"}}
	c := dial(t, s)
	got, err := c.Logins(ctx1s(t), "https://mail.x.io", associated(t, s, c))
	if err != nil {
		t.Fatal(err)
	}
	want := Login{Name: "work", User: "a@x.io", Password: pw, UUID: "u1", Group: "Mail"}
	if len(got) != 1 || got[0] != want {
		t.Errorf("logins = %+v, want exactly %+v", got, want)
	}
}

func TestClient_loginsNoneIsErrNoLogins(t *testing.T) {
	s := newFakeServer(t)
	c := dial(t, s)
	if _, err := c.Logins(ctx1s(t), "https://nothing.x.io", associated(t, s, c)); !errors.Is(err, ErrNoLogins) {
		t.Errorf("err = %v, want ErrNoLogins", err)
	}
}

func TestClient_loginsOnLockedDatabaseIsErrLocked(t *testing.T) {
	s := newFakeServer(t)
	c := dial(t, s)
	a := associated(t, s, c)
	s.set(func() { s.locked = true })
	if _, err := c.Logins(ctx1s(t), "https://mail.x.io", a); !errors.Is(err, ErrLocked) {
		t.Errorf("err = %v, want ErrLocked", err)
	}
}

func TestClient_skipsNoticesWhileWaitingForReply(t *testing.T) {
	s := newFakeServer(t)
	s.logins["https://mail.x.io"] = []Login{{User: "a@x.io", Password: "pw"}}
	c := dial(t, s)
	a := associated(t, s, c)
	s.set(func() { s.noticesBeforeReply = []string{"database-locked", "database-unlocked"} })
	got, err := c.Logins(ctx1s(t), "https://mail.x.io", a)
	if err != nil || len(got) != 1 || got[0].Password != "pw" {
		t.Errorf("logins = %+v, %v; notices must not be mistaken for the reply", got, err)
	}
}

func TestClient_serverClosesMidRequest(t *testing.T) {
	s := newFakeServer(t)
	c := dial(t, s)
	a := associated(t, s, c)
	s.set(func() { s.closeOn = "get-logins" })
	start := time.Now()
	_, err := c.Logins(ctx1s(t), "https://mail.x.io", a)
	if !errors.Is(err, ErrClosed) {
		t.Errorf("err = %v, want ErrClosed", err)
	}
	if time.Since(start) > 900*time.Millisecond {
		t.Error("a dropped connection must be noticed at once, not at the deadline")
	}
	if err := c.IsOpen(ctx1s(t), false); !errors.Is(err, ErrClosed) {
		t.Errorf("later calls: err = %v, want ErrClosed", err)
	}
}

func TestClient_neverAsksByUUID(t *testing.T) {
	s := newFakeServer(t)
	s.logins["https://mail.x.io"] = []Login{{User: "a@x.io", Password: "pw", UUID: "6768fa923bdd4bca9f360635d0d7509d"}}
	c := dial(t, s)
	a := associated(t, s, c)
	c.Logins(ctx1s(t), "https://mail.x.io", a)
	c.Logins(ctx1s(t), "https://other.x.io", a)
	s.set(func() {
		for _, url := range s.seen {
			if strings.HasPrefix(url, "keepassxc://") {
				t.Errorf("asked KeePassXC for %q; by-uuid lookups crash KeePassXC 2.8.0", url)
			}
		}
		if len(s.seen) != 2 {
			t.Errorf("seen = %v", s.seen)
		}
	})
}

func TestProtocolError_saysWhatKeePassXCSaid(t *testing.T) {
	err := &ProtocolError{Code: "8", Message: "KeePassXC association failed, try again"}
	if !strings.Contains(err.Error(), "association failed") {
		t.Errorf("Error() = %q", err.Error())
	}
}

func TestClient_abandonedRequestEndsTheConnection(t *testing.T) {
	s := newFakeServer(t)
	c := dial(t, s)
	a := associated(t, s, c)
	s.set(func() { s.silentOn = "get-logins" })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Logins(ctx, "https://mail.x.io", a); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	// Its answer may still come; it must never be taken for another request's.
	s.set(func() { s.silentOn = "" })
	if err := c.IsOpen(ctx1s(t), false); !errors.Is(err, ErrClosed) {
		t.Errorf("after an abandoned request: err = %v, want ErrClosed", err)
	}
}

func TestSetLoginCreatesAndUpdates(t *testing.T) {
	s := newFakeServer(t)
	c := dial(t, s)
	a := associated(t, s, c)
	const url = "https://oauth2.googleapis.com"

	if err := c.SetLogin(ctx1s(t), url, "me@x.io", "tok-1", "", a); err != nil {
		t.Fatalf("SetLogin (create): %v", err)
	}
	got, err := c.Logins(ctx1s(t), url, a)
	if err != nil || len(got) != 1 || got[0].Password != "tok-1" || got[0].UUID == "" {
		t.Fatalf("after create: %+v, %v", got, err)
	}
	if err := c.SetLogin(ctx1s(t), url, "me@x.io", "tok-2", got[0].UUID, a); err != nil {
		t.Fatalf("SetLogin (update): %v", err)
	}
	again, err := c.Logins(ctx1s(t), url, a)
	if err != nil || len(again) != 1 || again[0].Password != "tok-2" {
		t.Fatalf("after update: %+v, %v (want one entry, updated)", again, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.setLogins) != 2 {
		t.Fatalf("set-login requests = %d, want 2", len(s.setLogins))
	}
	for i, wantUUID := range []string{"", got[0].UUID} {
		b := s.setLogins[i]
		for k, want := range map[string]string{"url": url, "submitUrl": url, "id": a.ID, "login": "me@x.io",
			"uuid": wantUUID, "group": "", "groupUuid": "", "downloadFavicon": "false"} {
			if v, ok := b[k]; !ok || str(b, k) != want {
				t.Errorf("request %d: %s = %v (present %v), want %q", i, k, v, ok, want)
			}
		}
		if str(b, "nonce") == "" {
			t.Errorf("request %d: no nonce", i)
		}
	}
}

func TestSetLoginErrors(t *testing.T) {
	s := newFakeServer(t)
	c := dial(t, s)
	a := associated(t, s, c)
	s.set(func() { s.locked = true })
	if err := c.SetLogin(ctx1s(t), "https://x", "u", "p", "", a); !errors.Is(err, ErrLocked) {
		t.Errorf("locked: err = %v, want ErrLocked", err)
	}
	s.set(func() { s.locked = false })
	if err := c.SetLogin(ctx1s(t), "https://x", "u", "p", "", Association{ID: "nope"}); err == nil {
		t.Error("unknown association: want an error")
	}
}
