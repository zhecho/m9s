package sync_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	gosync "sync"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/mail"
	imapadapter "github.com/zhecho/m9s/internal/mail/imap"
	"github.com/zhecho/m9s/internal/store"
	msync "github.com/zhecho/m9s/internal/sync"
)

// fakeServer is an in-memory mailbox implementing imapadapter.Mirror.
type fakeServer struct {
	mu         gosync.Mutex
	validity   map[string]uint32
	mail       map[string]map[uint32][]string // folder → uid → flags
	rawFetched []string                       // "folder/uid" of every FetchRaw
	vanish     map[string]bool                // "folder/uid": listed, but gone when fetched
	closed     chan struct{}
	failAll    error
	failRaw    error // every FetchRaw fails with this
}

func newFakeServer() *fakeServer {
	return &fakeServer{
		validity: map[string]uint32{},
		mail:     map[string]map[uint32][]string{},
		vanish:   map[string]bool{},
		closed:   make(chan struct{}),
	}
}

func (f *fakeServer) add(folder string, uid uint32, flags ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mail[folder] == nil {
		f.mail[folder] = map[uint32][]string{}
		if f.validity[folder] == 0 {
			f.validity[folder] = 1
		}
	}
	f.mail[folder][uid] = flags
}

func (f *fakeServer) remove(folder string, uid uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.mail[folder], uid)
}

func (f *fakeServer) fetchedRaw() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.rawFetched...)
}

func (f *fakeServer) Connect(context.Context, string) error { return nil }
func (f *fakeServer) Close() error                          { return nil }
func (f *fakeServer) Closed() <-chan struct{}               { return f.closed }
func (f *fakeServer) Abort()                                {}

func (f *fakeServer) Snapshot(_ context.Context, folder string) (uint32, map[uint32][]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAll != nil {
		return 0, nil, f.failAll
	}
	out := map[uint32][]string{}
	for uid, flags := range f.mail[folder] {
		out[uid] = flags
	}
	return f.validity[folder], out, nil
}

func (f *fakeServer) FetchEnvelopes(_ context.Context, folder string, uids []uint32) ([]*mail.Envelope, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*mail.Envelope
	for _, uid := range uids {
		if flags, ok := f.mail[folder][uid]; ok {
			out = append(out, &mail.Envelope{UID: uid, Subject: fmt.Sprintf("%s %d", folder, uid), Flags: flags})
		}
	}
	return out, nil
}

func (f *fakeServer) FetchRaw(_ context.Context, folder string, uids []uint32, each func(uint32, []byte) error) error {
	for _, uid := range uids {
		f.mu.Lock()
		key := fmt.Sprintf("%s/%d", folder, uid)
		_, exists := f.mail[folder][uid]
		if exists && !f.vanish[key] {
			f.rawFetched = append(f.rawFetched, key)
		}
		fail := f.failRaw
		f.mu.Unlock()
		if fail != nil {
			return fail
		}
		if !exists || f.vanish[key] {
			continue // gone since it was listed: the server just leaves it out
		}
		if err := each(uid, []byte("Subject: "+key+"\r\n\r\nbody of "+key)); err != nil {
			return err
		}
	}
	return nil
}

func mirrorFixture(t *testing.T) (*msync.Mirror, *fakeServer, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "mail"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := newFakeServer()
	srv.add("INBOX", 1)
	srv.add("INBOX", 2, `\Seen`)
	srv.add("Sent", 1, `\Seen`)
	return msync.NewMirror("acct", srv, st), srv, st
}

func storedUIDs(t *testing.T, st *store.Store, folder string) []uint32 {
	t.Helper()
	_, flags, err := st.FolderState("acct", folder)
	if err != nil {
		t.Fatal(err)
	}
	uids := []uint32{}
	for id := range flags {
		uid, err := strconv.ParseUint(id, 10, 32)
		if err != nil {
			t.Fatalf("stored id %q is not a UID", id)
		}
		uids = append(uids, uint32(uid))
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	return uids
}

var bothFolders = []string{"INBOX", "Sent"}

func TestMirror_firstPassCopiesEveryMessageOfEveryFolder(t *testing.T) {
	m, _, st := mirrorFixture(t)
	if err := m.Pass(context.Background(), bothFolders); err != nil {
		t.Fatal(err)
	}
	if got := storedUIDs(t, st, "INBOX"); !reflect.DeepEqual(got, []uint32{1, 2}) {
		t.Errorf("INBOX holds %v, want [1 2]", got)
	}
	if raw, ok := st.Body("acct", "Sent", "1"); !ok || string(raw) != "Subject: Sent/1\r\n\r\nbody of Sent/1" {
		t.Errorf("Sent/1 content = %q, %v", raw, ok)
	}
	if messages, bodies := st.Counts("acct"); messages != 3 || bodies != 3 {
		t.Errorf("stored %d messages with %d downloaded, want 3 and 3", messages, bodies)
	}
	if list, ok := st.Envelopes("acct", "INBOX"); !ok || len(list) != 2 || list[1].IsUnread() {
		t.Errorf("mail list from the copy: %d messages, known=%v", len(list), ok)
	}
}

func TestMirror_emptyFolderBecomesKnown(t *testing.T) {
	m, _, st := mirrorFixture(t)
	if err := m.Pass(context.Background(), []string{"INBOX", "Archive"}); err != nil {
		t.Fatal(err)
	}
	if list, ok := st.Envelopes("acct", "Archive"); !ok || len(list) != 0 {
		t.Errorf("Archive: %d messages, known=%v; want known and empty", len(list), ok)
	}
}

func TestMirror_secondPassDownloadsOnlyWhatIsNew(t *testing.T) {
	m, srv, st := mirrorFixture(t)
	ctx := context.Background()
	if err := m.Pass(ctx, bothFolders); err != nil {
		t.Fatal(err)
	}
	before := len(srv.fetchedRaw())
	srv.add("INBOX", 3)

	if err := m.Pass(ctx, bothFolders); err != nil {
		t.Fatal(err)
	}
	if got := srv.fetchedRaw()[before:]; !reflect.DeepEqual(got, []string{"INBOX/3"}) {
		t.Errorf("second pass downloaded %v, want only the new INBOX/3", got)
	}
	if _, ok := st.Body("acct", "INBOX", "3"); !ok {
		t.Error("the new message was not stored")
	}
}

func TestMirror_newestMessagesAreDownloadedFirst(t *testing.T) {
	m, srv, _ := mirrorFixture(t)
	srv.add("INBOX", 3)
	if err := m.Pass(context.Background(), []string{"INBOX"}); err != nil {
		t.Fatal(err)
	}
	if got := srv.fetchedRaw(); !reflect.DeepEqual(got, []string{"INBOX/3", "INBOX/2", "INBOX/1"}) {
		t.Errorf("download order %v, want newest first", got)
	}
}

func TestMirror_deletionsAndFlagChangesOnTheServerAreApplied(t *testing.T) {
	m, srv, st := mirrorFixture(t)
	ctx := context.Background()
	if err := m.Pass(ctx, bothFolders); err != nil {
		t.Fatal(err)
	}
	srv.remove("INBOX", 2)       // deleted from another device
	srv.add("INBOX", 1, `\Seen`) // read on another device

	if err := m.Pass(ctx, bothFolders); err != nil {
		t.Fatal(err)
	}
	if got := storedUIDs(t, st, "INBOX"); !reflect.DeepEqual(got, []uint32{1}) {
		t.Errorf("INBOX holds %v, want [1]", got)
	}
	if _, ok := st.Body("acct", "INBOX", "2"); ok {
		t.Error("the deleted message's content is still on disk")
	}
	if env, _ := st.Envelope("acct", "INBOX", "1"); env == nil || env.IsUnread() {
		t.Error("message 1 still shows as unread")
	}
}

func TestMirror_renumberedFolderIsCopiedAgain(t *testing.T) {
	m, srv, st := mirrorFixture(t)
	ctx := context.Background()
	if err := m.Pass(ctx, bothFolders); err != nil {
		t.Fatal(err)
	}
	// The server rebuilt INBOX: UID 1 is now a different message.
	srv.mu.Lock()
	srv.validity["INBOX"] = 2
	srv.mail["INBOX"] = map[uint32][]string{1: nil}
	srv.mu.Unlock()
	before := len(srv.fetchedRaw())

	if err := m.Pass(ctx, bothFolders); err != nil {
		t.Fatal(err)
	}
	if got := storedUIDs(t, st, "INBOX"); !reflect.DeepEqual(got, []uint32{1}) {
		t.Errorf("INBOX holds %v, want [1]", got)
	}
	if got := srv.fetchedRaw()[before:]; !reflect.DeepEqual(got, []string{"INBOX/1"}) {
		t.Errorf("after renumbering downloaded %v; UID 1 must be fetched again", got)
	}
}

func TestMirror_folderGoneFromTheServerIsDropped(t *testing.T) {
	m, _, st := mirrorFixture(t)
	ctx := context.Background()
	if err := m.Pass(ctx, bothFolders); err != nil {
		t.Fatal(err)
	}
	if err := m.Pass(ctx, []string{"INBOX"}); err != nil { // Sent was deleted
		t.Fatal(err)
	}
	if _, ok := st.Envelopes("acct", "Sent"); ok {
		t.Error("Sent is still stored")
	}
}

func TestMirror_messageDeletedWhileCopyingDoesNotStopThePass(t *testing.T) {
	m, srv, st := mirrorFixture(t)
	srv.vanish["INBOX/2"] = true
	if err := m.Pass(context.Background(), bothFolders); err != nil {
		t.Fatalf("pass failed: %v", err)
	}
	if _, ok := st.Body("acct", "INBOX", "1"); !ok {
		t.Error("INBOX/1 was not downloaded after INBOX/2 vanished")
	}
	if _, ok := st.Body("acct", "Sent", "1"); !ok {
		t.Error("Sent was not copied after INBOX/2 vanished")
	}
}

func TestMirror_serverErrorEndsThePass(t *testing.T) {
	m, srv, _ := mirrorFixture(t)
	srv.failAll = errors.New("connection lost")
	if err := m.Pass(context.Background(), bothFolders); err == nil {
		t.Error("no error although the server answered nothing")
	}
}

func TestMirror_downloadErrorEndsThePassAndKeepsWhatWasSaved(t *testing.T) {
	m, srv, st := mirrorFixture(t)
	ctx := context.Background()
	srv.failRaw = errors.New("connection lost")
	if err := m.Pass(ctx, bothFolders); err == nil {
		t.Fatal("no error although no message could be downloaded")
	}
	if messages, _ := st.Counts("acct"); messages != 3 {
		t.Errorf("%d message headers stored, want all 3 kept for the next try", messages)
	}
	srv.failRaw = nil
	if err := m.Pass(ctx, bothFolders); err != nil {
		t.Fatal(err)
	}
	if _, bodies := st.Counts("acct"); bodies != 3 {
		t.Errorf("%d bodies after the retry, want 3", bodies)
	}
}

func TestMirror_cancelledPassStops(t *testing.T) {
	m, srv, _ := mirrorFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Pass(ctx, bothFolders); err == nil {
		t.Error("a cancelled pass reported success")
	}
	if got := srv.fetchedRaw(); len(got) != 0 {
		t.Errorf("a cancelled pass still downloaded %v", got)
	}
}

func TestMirror_reportsProgressAndCompletion(t *testing.T) {
	m, _, _ := mirrorFixture(t)
	var seen []msync.MirrorProgress
	m.OnProgress(func(p msync.MirrorProgress) { seen = append(seen, p) })
	if err := m.Pass(context.Background(), bothFolders); err != nil {
		t.Fatal(err)
	}
	if len(seen) < 2 {
		t.Fatalf("progress reported %d times, want at least a start and an end", len(seen))
	}
	if first := seen[0]; first.Done || first.AccountName != "acct" {
		t.Errorf("first report = %+v, want a running pass for acct", first)
	}
	if last := seen[len(seen)-1]; !last.Done || last.Messages != 3 || last.Downloaded != 3 {
		t.Errorf("last report = %+v, want done with 3 of 3", last)
	}
}

// --- the engine runs the mirror ---

// startMirroringEngine starts an engine whose account is copied from srv
// into a fresh store. Events are drained so nothing blocks.
func startMirroringEngine(t *testing.T, srv *fakeServer) (*msync.Engine, *mockClient, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "mail"))
	if err != nil {
		t.Fatal(err)
	}
	mc := newMock([]*mail.Folder{
		{Name: "Sent", Delimiter: "/"},
		{Name: "INBOX", Delimiter: "/"},
		{Name: "[Gmail]", Delimiter: "/", Attributes: []string{`\Noselect`}},
	})
	events := make(chan msync.Event, 64)
	eng := msync.NewEngineWithClients(newTestAccount("acct"), events, mc, mc)
	eng.SetBackoff(func(int) time.Duration { return time.Millisecond })
	eng.SetStore(st)
	eng.SetMirrorClient(srv)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			select {
			case <-events:
			case <-ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-eng.Done()
		st.Close()
	})
	eng.Start(ctx)
	return eng, mc, st
}

func TestEngine_copiesTheAccountInTheBackground(t *testing.T) {
	srv := newFakeServer()
	srv.add("INBOX", 1)
	srv.add("Sent", 1, `\Seen`)
	_, _, st := startMirroringEngine(t, srv)

	waitFor(t, func() bool {
		messages, bodies := st.Counts("acct")
		return messages == 2 && bodies == 2
	})
	if got := srv.fetchedRaw(); len(got) != 2 || got[0] != "INBOX/1" {
		t.Errorf("download order %v, want INBOX before the other folders", got)
	}
}

func TestEngine_newMailIsCopiedWithoutWaitingForTheNextRound(t *testing.T) {
	srv := newFakeServer()
	srv.add("INBOX", 1)
	_, mc, st := startMirroringEngine(t, srv)
	waitFor(t, func() bool { _, ok := st.Body("acct", "INBOX", "1"); return ok })

	srv.add("INBOX", 2)
	mc.push(t, imapadapter.IDLENotification{Folder: "INBOX", Exists: 2})

	waitFor(t, func() bool { _, ok := st.Body("acct", "INBOX", "2"); return ok })
}

func TestEngine_fetchBodyUsesTheLocalCopy(t *testing.T) {
	srv := newFakeServer()
	srv.add("INBOX", 1)
	eng, _, st := startMirroringEngine(t, srv)
	waitFor(t, func() bool { _, ok := st.Body("acct", "INBOX", "1"); return ok })

	msg, err := eng.FetchBody(context.Background(), "INBOX", "1")
	if err != nil {
		t.Fatal(err)
	}
	// The mock server answers FetchBody with nothing; only the copy has it.
	if msg == nil || msg.Envelope == nil || msg.Envelope.UID != 1 || msg.TextBody != "body of INBOX/1" {
		t.Errorf("FetchBody = %+v, want the stored message", msg)
	}
}

func TestEngine_mirrorTroubleDoesNotDisconnectTheAccount(t *testing.T) {
	srv := newFakeServer()
	srv.failAll = errors.New("mirror connection refused")
	st, err := store.Open(filepath.Join(t.TempDir(), "mail"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mc := newMock(inboxOnly())
	events := make(chan msync.Event, 64)
	eng := msync.NewEngineWithClients(newTestAccount("acct"), events, mc, mc)
	eng.SetStore(st)
	eng.SetMirrorClient(srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	var failed *msync.MirrorFailed
	deadline := time.After(2 * time.Second)
	for failed == nil {
		select {
		case ev := <-events:
			switch e := ev.(type) {
			case msync.MirrorFailed:
				failed = &e
			case msync.AccountStatus:
				if e.State == msync.StateFailed {
					t.Fatalf("the account was reported as failed: %v", e.Err)
				}
			}
		case <-deadline:
			t.Fatal("no MirrorFailed event; the user would never learn why nothing is copied")
		}
	}
	if failed.AccountName != "acct" || failed.Err == nil {
		t.Errorf("MirrorFailed = %+v", *failed)
	}
}
