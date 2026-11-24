package sync_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	imapadapter "github.com/zhecho/m9s/internal/mail/imap"
	msync "github.com/zhecho/m9s/internal/sync"
)

var errRefused = errors.New("dial tcp: connection refused")

func TestDefaultBackoff(t *testing.T) {
	want := []time.Duration{2, 4, 8, 16, 32, 60, 60}
	for i, w := range want {
		if got := msync.DefaultBackoff(i + 1); got != w*time.Second {
			t.Errorf("DefaultBackoff(%d) = %v, want %v", i+1, got, w*time.Second)
		}
	}
	if got := msync.DefaultBackoff(500); got != 60*time.Second {
		t.Errorf("DefaultBackoff(500) = %v, want 60s (no overflow)", got)
	}
}

func TestEngine_retriesUntilConnected(t *testing.T) {
	mc := newMock(inboxOnly())
	mc.connectErrs = []error{errRefused, errRefused}
	_, events := startEngine(t, mc, mc)

	if st := nextStatus(t, events, msync.StateFailed); st.Attempt != 1 {
		t.Errorf("first failure Attempt = %d, want 1", st.Attempt)
	}
	if st := nextStatus(t, events, msync.StateFailed); st.Attempt != 2 {
		t.Errorf("second failure Attempt = %d, want 2", st.Attempt)
	}
	nextEvent[msync.FoldersUpdated](t, events)
	if st := nextStatus(t, events, msync.StateConnected); !st.Recovered {
		t.Error("Connected after failures must be flagged Recovered")
	}
}

func TestEngine_failedCarriesRetryIn(t *testing.T) {
	mc := newMock(inboxOnly())
	mc.connectErr = errRefused
	_, events := startEngineBackoff(t, mc, mc, func(attempt int) time.Duration {
		if attempt == 1 {
			return 8 * time.Second
		}
		return time.Hour
	})
	if st := nextStatus(t, events, msync.StateFailed); st.RetryIn != 8*time.Second {
		t.Errorf("RetryIn = %v, want 8s", st.RetryIn)
	}
}

// expectNoStatus fails if an AccountStatus in state arrives within d.
func expectNoStatus(t *testing.T, events <-chan msync.Event, state msync.ConnState, d time.Duration) {
	t.Helper()
	timeout := time.After(d)
	for {
		select {
		case ev := <-events:
			if st, ok := ev.(msync.AccountStatus); ok && st.State == state {
				t.Fatalf("unexpected AccountStatus state %d", state)
			}
		case <-timeout:
			return
		}
	}
}

func TestEngine_authFailureDoesNotAutoRetry(t *testing.T) {
	mc := newMock(inboxOnly())
	mc.connectErrs = []error{fmt.Errorf("%w: bad credentials", imapadapter.ErrAuth)}
	eng, events := startEngine(t, mc, mc)

	st := nextStatus(t, events, msync.StateFailed)
	if st.RetryIn != 0 {
		t.Errorf("auth failure RetryIn = %v, want 0", st.RetryIn)
	}
	expectNoStatus(t, events, msync.StateConnecting, 50*time.Millisecond)

	eng.Retry()
	nextStatus(t, events, msync.StateConnecting)
	nextStatus(t, events, msync.StateConnected)
}

func TestEngine_noPasswordDoesNotAutoRetry(t *testing.T) {
	mc := newMock(inboxOnly())
	acct := newTestAccount("test")
	acct.IMAP.Password = ""
	events := make(chan msync.Event, 64)
	eng := msync.NewEngineWithClients(acct, events, mc, mc)
	eng.SetBackoff(func(int) time.Duration { return time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	if st := nextStatus(t, events, msync.StateFailed); st.RetryIn != 0 {
		t.Errorf("no-password RetryIn = %v, want 0", st.RetryIn)
	}
	expectNoStatus(t, events, msync.StateConnecting, 50*time.Millisecond)
}

func TestEngine_retryForcesImmediateAttempt(t *testing.T) {
	mc := newMock(inboxOnly())
	mc.connectErrs = []error{errRefused}
	eng, events := startEngineBackoff(t, mc, mc, func(int) time.Duration { return time.Hour })

	nextStatus(t, events, msync.StateFailed)
	eng.Retry()
	nextStatus(t, events, msync.StateConnecting)
	nextStatus(t, events, msync.StateConnected)
}

func TestEngine_reconnectsAfterMidSessionLoss(t *testing.T) {
	mc := newMock(inboxOnly())
	_, events := startEngine(t, mc, mc)
	nextStatus(t, events, msync.StateConnected)

	mc.kill()

	if st := nextStatus(t, events, msync.StateFailed); st.Attempt != 1 || st.Err == nil {
		t.Errorf("after loss: Attempt = %d Err = %v, want 1 and non-nil", st.Attempt, st.Err)
	}
	nextStatus(t, events, msync.StateConnecting)
	nextEvent[msync.FoldersUpdated](t, events)
	if st := nextStatus(t, events, msync.StateConnected); !st.Recovered {
		t.Error("reconnect must be flagged Recovered")
	}
}

func TestEngine_reconnectsWhenIdleConnDies(t *testing.T) {
	cmd := newMock(inboxOnly())
	idle := newMock(nil)
	_, events := startEngine(t, cmd, idle)
	nextStatus(t, events, msync.StateConnected)

	idle.kill()

	nextStatus(t, events, msync.StateFailed)
	if st := nextStatus(t, events, msync.StateConnected); !st.Recovered {
		t.Error("reconnect must be flagged Recovered")
	}
}

func TestEngine_attemptResetsAfterSuccess(t *testing.T) {
	mc := newMock(inboxOnly())
	mc.connectErrs = []error{errRefused, errRefused}
	_, events := startEngine(t, mc, mc)
	nextStatus(t, events, msync.StateConnected)

	mc.kill()

	if st := nextStatus(t, events, msync.StateFailed); st.Attempt != 1 {
		t.Errorf("Attempt after a successful session = %d, want 1", st.Attempt)
	}
}

func TestEngine_retryWhileConnectedIsNoop(t *testing.T) {
	mc := newMock(inboxOnly())
	eng, events := startEngine(t, mc, mc)
	nextStatus(t, events, msync.StateConnected)

	eng.Retry()
	eng.Retry() // must never block

	expectNoStatus(t, events, msync.StateConnecting, 50*time.Millisecond)
}

func TestEngine_stopsOnCancelWhileBackingOff(t *testing.T) {
	mc := newMock(inboxOnly())
	mc.connectErr = errRefused
	events := make(chan msync.Event) // unbuffered: nobody reads after the first failure
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	eng.SetBackoff(func(int) time.Duration { return time.Hour })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	nextStatus(t, events, msync.StateFailed)
	cancel()

	select {
	case <-eng.Done():
	case <-time.After(time.Second):
		t.Fatal("engine did not stop within 1s of cancellation")
	}
}

func TestEngine_stopsOnCancelWhileBlockedOnSend(t *testing.T) {
	mc := newMock(inboxOnly())
	events := make(chan msync.Event) // never read
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	ctx, cancel := context.WithCancel(context.Background())
	eng.Start(ctx)
	cancel()

	select {
	case <-eng.Done():
	case <-time.After(time.Second):
		t.Fatal("engine blocked on an unread event after cancellation")
	}
}
