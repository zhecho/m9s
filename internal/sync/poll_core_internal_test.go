package sync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/store"
)

var errStubSignIn = errors.New("stub: sign in again")

// stubAPI is a provider part that only counts what pollCore asks of it.
type stubAPI struct {
	st   *store.Store
	acct string

	mu         gosync.Mutex
	connectErr error
	cursors    []string // initialSync's cursor, per call
	polls      int
	raws       int
}

func (s *stubAPI) connect(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return "c1", s.connectErr
}

func (s *stubAPI) loadFolders(context.Context) ([]*mail.Folder, error) {
	return []*mail.Folder{{Name: "INBOX", Delimiter: "/"}}, nil
}

func (s *stubAPI) initialSync(_ context.Context, cursor string) error {
	s.mu.Lock()
	s.cursors = append(s.cursors, cursor)
	s.mu.Unlock()
	return s.st.AddMessages(s.acct, "INBOX", []*mail.Envelope{{ID: "m1", SortKey: 1}, {ID: "m2", SortKey: 2}})
}

func (s *stubAPI) poll(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.polls++
	return nil
}

func (s *stubAPI) refreshBadges(context.Context) {}
func (s *stubAPI) bodyFolders() []string         { return []string{"INBOX"} }

func (s *stubAPI) getRaw(_ context.Context, id string) ([]byte, error) {
	s.mu.Lock()
	s.raws++
	s.mu.Unlock()
	return []byte("Subject: " + id + "\r\n\r\nbody\r\n"), nil
}

func (s *stubAPI) notFound(error) bool { return false }

func (s *stubAPI) signInAgain(err error) (error, bool) {
	return errStubSignIn, errors.Is(err, errStubSignIn)
}

func (s *stubAPI) serverInfo(context.Context) (string, error) { return "Stub / 1", nil }

func (s *stubAPI) SaveDraft(context.Context, []byte, string) (string, error) { return "d1", nil }

func (s *stubAPI) background(ctx context.Context) context.Context { return ctx }

type coreHarness struct {
	t    *testing.T
	core *pollCore
	api  *stubAPI
	mu   gosync.Mutex
	evs  []Event
	stop func()
}

func newCoreHarness(t *testing.T, connectErr error) *coreHarness {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Account{Name: "acct", Folders: config.FolderConfig{
		BadgePoll: config.Duration(time.Hour), BodyCachePoll: config.Duration(time.Hour),
	}}
	events := make(chan Event)
	h := &coreHarness{t: t, api: &stubAPI{st: st, acct: "acct", connectErr: connectErr}}
	h.core = newPollCore(cfg, events, st, draft.NewStore(t.TempDir()))
	h.core.api = h.api
	h.core.SetBackoff(func(int) time.Duration { return time.Hour })
	ctx, cancel := context.WithCancel(context.Background())
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case ev := <-events:
				h.mu.Lock()
				h.evs = append(h.evs, ev)
				h.mu.Unlock()
			case <-ctx.Done():
				return
			}
		}
	}()
	h.core.Start(ctx)
	h.stop = func() { cancel(); <-h.core.Done(); <-drained }
	t.Cleanup(h.stop)
	return h
}

func (h *coreHarness) eventually(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *coreHarness) events() []Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.evs)
}

func stateLabel(s ConnState) string { return fmt.Sprintf("state %d", s) }

func TestPollCore_drivesAnyProvider(t *testing.T) {
	h := newCoreHarness(t, nil)
	st := h.core.store
	h.eventually("both bodies", func() bool {
		_, ok1 := st.Body("acct", "", "m1")
		_, ok2 := st.Body("acct", "", "m2")
		return ok1 && ok2
	})

	// Connecting, then the folders, then connected — in that order.
	var order []string
	for _, ev := range h.events() {
		switch e := ev.(type) {
		case AccountStatus:
			if e.AccountName != "acct" {
				t.Errorf("status of %q", e.AccountName)
			}
			order = append(order, stateLabel(e.State))
		case FoldersUpdated:
			if len(e.Folders) != 1 || e.Folders[0].Name != "INBOX" {
				t.Errorf("folders = %+v", e.Folders)
			}
			order = append(order, "folders")
		}
	}
	want := []string{stateLabel(StateConnecting), "folders", stateLabel(StateConnected)}
	if len(order) < 3 || !slices.Equal(order[:3], want) {
		t.Fatalf("order = %v, want %v first", order, want)
	}
	h.api.mu.Lock()
	cursors, polls := slices.Clone(h.api.cursors), h.api.polls
	h.api.mu.Unlock()
	if !slices.Equal(cursors, []string{"c1"}) {
		t.Fatalf("initialSync cursors = %v", cursors)
	}

	h.core.SyncAll(context.Background())
	h.eventually("a poll after SyncAll", func() bool {
		h.api.mu.Lock()
		defer h.api.mu.Unlock()
		return h.api.polls > polls
	})
	h.eventually("server info", func() bool {
		for _, ev := range h.events() {
			if si, ok := ev.(ServerInfo); ok && si.Text == "Stub / 1" {
				return true
			}
		}
		return false
	})
}

func TestPollCore_signInAgainWaitsForTheOwner(t *testing.T) {
	h := newCoreHarness(t, errStubSignIn)
	var failed AccountStatus
	h.eventually("failure", func() bool {
		for _, ev := range h.events() {
			if st, ok := ev.(AccountStatus); ok && st.State == StateFailed {
				failed = st
				return true
			}
		}
		return false
	})
	if failed.RetryIn != 0 {
		t.Fatalf("RetryIn = %v: a refused sign-in cannot fix itself", failed.RetryIn)
	}
	if !errors.Is(failed.Err, errStubSignIn) || !strings.HasSuffix(failed.Err.Error(), "m9s login acct, then restart m9s") {
		t.Fatalf("err = %v", failed.Err)
	}
}
