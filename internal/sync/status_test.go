package sync_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/mail"
	msync "github.com/zhecho/m9s/internal/sync"
)

// nextEvent drains events until one of type T arrives, failing after 2s.
func nextEvent[T msync.Event](t *testing.T, events <-chan msync.Event) T {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if v, ok := ev.(T); ok {
				return v
			}
		case <-timeout:
			var zero T
			t.Fatalf("timeout waiting for %T", zero)
			return zero
		}
	}
}

// nextStatus drains events until an AccountStatus in the given state arrives.
func nextStatus(t *testing.T, events <-chan msync.Event, state msync.ConnState) msync.AccountStatus {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if st, ok := ev.(msync.AccountStatus); ok && st.State == state {
				return st
			}
		case <-timeout:
			t.Fatalf("timeout waiting for AccountStatus state %d", state)
			return msync.AccountStatus{}
		}
	}
}

func startEngine(t *testing.T, cmd, idle *mockClient) (*msync.Engine, chan msync.Event) {
	t.Helper()
	return startEngineBackoff(t, cmd, idle, func(int) time.Duration { return time.Millisecond })
}

func startEngineBackoff(t *testing.T, cmd, idle *mockClient, backoff func(int) time.Duration) (*msync.Engine, chan msync.Event) {
	t.Helper()
	events := make(chan msync.Event, 64)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, idle, cmd)
	eng.SetBackoff(backoff)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng.Start(ctx)
	return eng, events
}

func inboxOnly() []*mail.Folder {
	return []*mail.Folder{{Name: "INBOX", Delimiter: "/"}}
}

func TestEngine_statusConnectingThenConnected(t *testing.T) {
	mc := newMock(inboxOnly())
	_, events := startEngine(t, mc, mc)

	var order []string
	timeout := time.After(2 * time.Second)
	for len(order) < 3 {
		select {
		case ev := <-events:
			switch e := ev.(type) {
			case msync.AccountStatus:
				if e.State == msync.StateConnecting {
					order = append(order, "connecting")
				}
				if e.State == msync.StateConnected {
					if e.Recovered {
						t.Error("first connect must not be flagged Recovered")
					}
					order = append(order, "connected")
				}
			case msync.FoldersUpdated:
				order = append(order, "folders")
			}
		case <-timeout:
			t.Fatalf("timeout; got %v", order)
		}
	}
	want := []string{"connecting", "folders", "connected"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestEngine_statusFailedOnConnectError(t *testing.T) {
	refused := errors.New("dial tcp: connection refused")
	mc := newMock(inboxOnly())
	mc.connectErrs = []error{refused}
	_, events := startEngine(t, mc, mc)

	nextStatus(t, events, msync.StateConnecting)
	st := nextStatus(t, events, msync.StateFailed)
	if st.Attempt != 1 {
		t.Errorf("Attempt = %d, want 1", st.Attempt)
	}
	if !errors.Is(st.Err, refused) {
		t.Errorf("Err = %v, want it to wrap %v", st.Err, refused)
	}
	if st.AccountName != "test" {
		t.Errorf("AccountName = %q", st.AccountName)
	}
}

func TestEngine_statusFailedOnFoldersError(t *testing.T) {
	listErr := errors.New("imap LIST: boom")
	mc := newMock(nil)
	mc.foldersErr = listErr
	_, events := startEngine(t, mc, mc)

	st := nextStatus(t, events, msync.StateFailed)
	if !errors.Is(st.Err, listErr) {
		t.Errorf("Err = %v, want it to wrap %v", st.Err, listErr)
	}
}

func TestEngine_statusFailedOnIdleError(t *testing.T) {
	idleErr := errors.New("idle dial refused")
	cmd := newMock(inboxOnly())
	idle := newMock(nil)
	idle.connectErrs = []error{idleErr}
	_, events := startEngine(t, cmd, idle)

	st := nextStatus(t, events, msync.StateFailed)
	if !errors.Is(st.Err, idleErr) {
		t.Errorf("Err = %v, want it to wrap %v", st.Err, idleErr)
	}
}

func TestEngine_statusFailedNoPassword(t *testing.T) {
	mc := newMock(inboxOnly())
	acct := newTestAccount("test")
	acct.IMAP.Password = "" // no password was handed to the engine
	events := make(chan msync.Event, 64)
	eng := msync.NewEngineWithClients(acct, events, mc, mc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	st := nextStatus(t, events, msync.StateFailed)
	if !errors.Is(st.Err, msync.ErrNoPassword) {
		t.Errorf("Err = %v, want ErrNoPassword", st.Err)
	}
}
