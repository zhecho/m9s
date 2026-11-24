package sync_test

import (
	"context"
	"fmt"
	"net/http"
	gosync "sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/zhecho/m9s/internal/mail/gmail/gmailtest"
	msync "github.com/zhecho/m9s/internal/sync"
)

// idMock is a mockClient whose server answers IMAP ID.
type idMock struct {
	*mockClient
	idMu  gosync.Mutex
	id    *imap.IDData
	calls int
}

func (m *idMock) ServerID(context.Context) (*imap.IDData, error) {
	m.idMu.Lock()
	defer m.idMu.Unlock()
	m.calls++
	return m.id, nil
}

func (m *idMock) idCalls() int {
	m.idMu.Lock()
	defer m.idMu.Unlock()
	return m.calls
}

func TestEngine_serverInfoOncePerRun(t *testing.T) {
	cmd := &idMock{mockClient: newMock(inboxOnly()), id: &imap.IDData{Name: "Dovecot", Version: "2.3.21"}}
	idle := newMock(nil)
	events := make(chan msync.Event, 64)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, idle, cmd)
	eng.SetBackoff(func(int) time.Duration { return time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng.Start(ctx)

	si := nextEvent[msync.ServerInfo](t, events)
	if si.AccountName != "test" || si.Text != "Dovecot / 2.3.21" {
		t.Errorf("ServerInfo = %+v, want test / \"Dovecot / 2.3.21\"", si)
	}

	idle.kill()
	nextStatus(t, events, msync.StateFailed)
	nextStatus(t, events, msync.StateConnected)
	time.Sleep(50 * time.Millisecond)
	if n := cmd.idCalls(); n != 1 {
		t.Errorf("ID asked %d times, want once per run", n)
	}
}

func TestGmailEngine_serverInfoFromDiscovery(t *testing.T) {
	h := newGmail(t, func(f *gmailtest.Fake) {
		f.Handle("/$discovery/rest", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"name":"gmail","version":"v1","revision":"20261106","title":"Gmail API"}`)
		})
	})
	h.eventually("ServerInfo", func() bool {
		for _, ev := range h.snapshot() {
			if si, ok := ev.(msync.ServerInfo); ok {
				if si.Text != "Gmail API / v1 (rev 20261106)" {
					t.Fatalf("ServerInfo.Text = %q", si.Text)
				}
				return true
			}
		}
		return false
	})
	h.synced()
	time.Sleep(300 * time.Millisecond) // a few polls go by
	if n := h.f.Count("/$discovery/rest"); n != 1 {
		t.Errorf("discovery fetched %d times, want once per run", n)
	}
}
