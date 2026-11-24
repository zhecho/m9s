package sync_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail"
	imapadapter "github.com/zhecho/m9s/internal/mail/imap"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	msync "github.com/zhecho/m9s/internal/sync"
)

func (m *mockClient) seenPasswords() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.passwords...)
}

func TestEngine_usesTheHandedPassword(t *testing.T) {
	t.Setenv("PWHAND_IMAP_PASSWORD", "from-env") // must not be consulted: the lookup happened at startup
	acct := newTestAccount("pwhand")
	acct.IMAP.Password = ""
	acct.IMAP.PasswordCmd = "echo from-cmd"
	mc := newMock(inboxOnly())
	mc.connectErrs = []error{errRefused, errRefused}
	events := make(chan msync.Event, 64)
	eng := msync.NewEngineWithClients(acct, events, mc, mc)
	eng.SetPassword(" handed pw ")
	eng.SetBackoff(func(int) time.Duration { return time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	nextStatus(t, events, msync.StateConnected)
	for _, pw := range mc.seenPasswords() {
		if pw != " handed pw " {
			t.Errorf("connected with %q, want only the handed password, unaltered", pw)
		}
	}
	if len(mc.seenPasswords()) < 3 {
		t.Errorf("connect attempts = %d, want the retries too", len(mc.seenPasswords()))
	}
}

func TestEngine_noPasswordIsErrNoPassword(t *testing.T) {
	acct := newTestAccount("test")
	acct.IMAP.Password = ""
	acct.IMAP.PasswordCmd = "echo would-work" // not the engine's business any more
	mc := newMock(inboxOnly())
	events := make(chan msync.Event, 64)
	eng := msync.NewEngineWithClients(acct, events, mc, mc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	st := nextStatus(t, events, msync.StateFailed)
	if !errors.Is(st.Err, msync.ErrNoPassword) || len(mc.seenPasswords()) != 0 {
		t.Errorf("Err = %v, connects %d; want ErrNoPassword and no login attempt", st.Err, len(mc.seenPasswords()))
	}
}

func TestEngine_rejectedPasswordIsKeptAndReported(t *testing.T) {
	acct := newTestAccount("test")
	mc := newMock(inboxOnly())
	mc.connectErrs = []error{fmt.Errorf("%w: bad", imapadapter.ErrAuth)}
	events := make(chan msync.Event, 64)
	eng := msync.NewEngineWithClients(acct, events, mc, mc)
	eng.SetPassword("typed-wrong")
	eng.SetBackoff(func(int) time.Duration { return time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	st := nextStatus(t, events, msync.StateFailed)
	if !errors.Is(st.Err, msync.ErrPasswordRejected) || !errors.Is(st.Err, imapadapter.ErrAuth) {
		t.Errorf("Err = %v, want ErrPasswordRejected wrapping the server's answer", st.Err)
	}
	if !strings.Contains(st.Err.Error(), "restart m9s") {
		t.Errorf("Err = %q; it must tell the user to restart", st.Err)
	}
	if st.RetryIn != 0 {
		t.Errorf("RetryIn = %v; a rejected password is not retried by itself", st.RetryIn)
	}
	eng.Retry() // s: tries again with what it has, without looking anything up
	nextStatus(t, events, msync.StateConnected)
	if got := mc.seenPasswords(); len(got) < 2 || got[0] != "typed-wrong" || got[1] != "typed-wrong" {
		t.Errorf("passwords used = %q, want the same one twice", got)
	}
}

// startProbeEngine starts an engine that polls quickly and gives up on a hung
// poll after probeTimeout.
func startProbeEngine(t *testing.T, cmd, idle *mockClient, probeTimeout time.Duration) chan msync.Event {
	t.Helper()
	acct := newTestAccount("test")
	acct.Folders.BadgePoll = config.Duration(10 * time.Millisecond)
	events := make(chan msync.Event, 256)
	eng := msync.NewEngineWithClients(acct, events, idle, cmd)
	eng.SetBackoff(func(int) time.Duration { return time.Hour })
	eng.SetProbeTimeout(probeTimeout)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng.Start(ctx)
	return events
}

func TestEngine_hungConnectionIsDetected(t *testing.T) {
	cmd := newMock(inboxOnly())
	cmd.hangStatus = true // the socket is half-open: requests never get an answer
	idle := newMock(nil)
	events := startProbeEngine(t, cmd, idle, 30*time.Millisecond)

	nextStatus(t, events, msync.StateConnected)
	st := nextStatus(t, events, msync.StateFailed)
	if st.Err == nil || !strings.Contains(st.Err.Error(), "not responding") {
		t.Errorf("Err = %v, want a not-responding error", st.Err)
	}
	if cmd.abortCount() == 0 {
		t.Error("the hung command connection must be aborted, not politely closed")
	}
}

func TestEngine_lostSessionDoesNotWaitOnHalfOpenIdle(t *testing.T) {
	cmd := newMock(inboxOnly())
	idle := newMock(nil)
	idle.hangClose = true // a polite Close (IDLE DONE) would never return
	events := startProbeEngine(t, cmd, idle, time.Hour)
	nextStatus(t, events, msync.StateConnected)

	cmd.kill()

	nextStatus(t, events, msync.StateFailed) // 2s timeout in nextStatus
	if idle.abortCount() == 0 {
		t.Error("after a lost session the other connection must be aborted, not closed politely")
	}
}

func TestEngine_dirtyDraftsSyncOnlyOnceAcrossReconnects(t *testing.T) {
	ds := draft.NewStore(t.TempDir())
	if _, err := ds.Save("test", smtpadapter.Draft{Subject: "unsent", Body: "x"}); err != nil {
		t.Fatal(err)
	}
	mc := newMock(inboxOnly())
	mc.noUIDPlus = true // the draft can never be marked synced
	events := make(chan msync.Event, 64)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	eng.SetDraftStore(ds)
	eng.SetBackoff(func(int) time.Duration { return time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	nextStatus(t, events, msync.StateConnected)
	waitFor(t, func() bool { return mc.appendCount() >= 1 })
	mc.kill()
	nextStatus(t, events, msync.StateConnected)
	mc.kill()
	nextStatus(t, events, msync.StateConnected)
	time.Sleep(50 * time.Millisecond) // give a wrongly started sync time to run

	if n := mc.appendCount(); n != 1 {
		t.Errorf("draft uploaded %d times over 3 sessions, want 1 (duplicates on the server)", n)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 2s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func draftsAndTrashMock() *mockClient {
	return newMock([]*mail.Folder{
		{Name: "INBOX", Delimiter: "/"},
		{Name: "Drafts", Delimiter: "/"},
		{Name: "Deleted Items", Delimiter: "/"},
	})
}

func TestEngine_syncDraftsUploadsADraftSavedMidSession(t *testing.T) {
	ds := draft.NewStore(t.TempDir())
	mc := draftsAndTrashMock()
	mc.appendUID = 9
	events := make(chan msync.Event, 64)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	eng.SetDraftStore(ds)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	nextStatus(t, events, msync.StateConnected)

	id, err := ds.Save("test", smtpadapter.Draft{Subject: "later", Body: "x"})
	if err != nil {
		t.Fatal(err)
	}
	eng.SyncDrafts(context.Background())

	metas, _ := ds.List("test")
	if len(metas) != 1 || metas[0].ID != id || metas[0].SyncedID != "9" || metas[0].Dirty {
		t.Errorf("after SyncDrafts: %+v, want uploaded with uid 9", metas)
	}
}

func TestEngine_syncDraftsWithoutUIDPlusUploadsOnce(t *testing.T) {
	ds := draft.NewStore(t.TempDir())
	if _, err := ds.Save("test", smtpadapter.Draft{Subject: "x", Body: "x"}); err != nil {
		t.Fatal(err)
	}
	mc := draftsAndTrashMock()
	mc.noUIDPlus = true
	eng := msync.NewEngineWithClients(newTestAccount("test"), make(chan msync.Event, 64), mc, mc)
	eng.SetDraftStore(ds)

	eng.SyncDrafts(context.Background())
	eng.SyncDrafts(context.Background())

	if n := mc.appendCount(); n != 1 {
		t.Errorf("uploaded %d times, want 1: a draft the server gave no UID for must not be re-sent", n)
	}
}

func TestEngine_trashDraftCopyMovesItToDeletedItems(t *testing.T) {
	mc := draftsAndTrashMock()
	eng := msync.NewEngineWithClients(newTestAccount("test"), make(chan msync.Event, 4), mc, mc)
	if _, err := eng.Folders(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := eng.DraftsFolderName(); got != "Drafts" {
		t.Errorf("DraftsFolderName = %q", got)
	}

	if err := eng.TrashDraftCopy(context.Background(), "5"); err != nil {
		t.Fatalf("TrashDraftCopy: %v", err)
	}
	if mc.moveCalledSrc != "Drafts" || mc.moveCalledUID != 5 || mc.moveCalledDst != "Deleted Items" {
		t.Errorf("moved %q uid %d to %q, want Drafts uid 5 to Deleted Items", mc.moveCalledSrc, mc.moveCalledUID, mc.moveCalledDst)
	}
}

func TestEngine_syncDraftsSkipsLocalOnlyDrafts(t *testing.T) {
	ds := draft.NewStore(t.TempDir())
	localID, _ := ds.Save("test", smtpadapter.Draft{Subject: "local", Body: "x"})
	if err := ds.SetLocalOnly("test", localID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.Save("test", smtpadapter.Draft{Subject: "server", Body: "y"}); err != nil {
		t.Fatal(err)
	}
	mc := draftsAndTrashMock()
	eng := msync.NewEngineWithClients(newTestAccount("test"), make(chan msync.Event, 64), mc, mc)
	eng.SetDraftStore(ds)

	eng.SyncDrafts(context.Background())

	if n := mc.appendCount(); n != 1 {
		t.Errorf("uploaded %d drafts, want 1: a Local Draft must never leave the machine", n)
	}
}

func TestEngine_draftSavedWhileDisconnectedUploadsOnReconnect(t *testing.T) {
	ds := draft.NewStore(t.TempDir())
	mc := draftsAndTrashMock()
	events := make(chan msync.Event, 64)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	eng.SetDraftStore(ds)
	eng.SetBackoff(func(int) time.Duration { return time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	nextStatus(t, events, msync.StateConnected)

	mc.mu.Lock()
	mc.connectErrs = []error{errRefused} // the next reconnect attempt fails once
	mc.mu.Unlock()
	mc.kill()
	nextStatus(t, events, msync.StateFailed)
	if _, err := ds.Save("test", smtpadapter.Draft{Subject: "offline", Body: "x"}); err != nil {
		t.Fatal(err)
	}

	nextStatus(t, events, msync.StateConnected)
	waitFor(t, func() bool { return mc.appendCount() == 1 })
}

func TestEngine_draftUploadIsAStandardMessage(t *testing.T) {
	ds := draft.NewStore(t.TempDir())
	if _, err := ds.Save("test", smtpadapter.Draft{From: "me@x.io", Subject: "Здравей Яна", Body: "Тяло"}); err != nil {
		t.Fatal(err)
	}
	mc := draftsAndTrashMock()
	eng := msync.NewEngineWithClients(newTestAccount("test"), make(chan msync.Event, 64), mc, mc)
	eng.SetDraftStore(ds)

	if err := eng.SyncDrafts(context.Background()); err != nil {
		t.Fatalf("SyncDrafts: %v", err)
	}
	head := strings.SplitN(string(mc.lastAppend), "\r\n\r\n", 2)[0]
	if !strings.Contains(head, "Subject: =?utf-8?") || !strings.Contains(head, "charset=utf-8") {
		t.Errorf("uploaded headers are not encoded:\n%s", head)
	}
}

func TestEngine_draftUploadFailureIsReported(t *testing.T) {
	ds := draft.NewStore(t.TempDir())
	id, _ := ds.Save("test", smtpadapter.Draft{Subject: "Здравей", Body: "x"})
	mc := draftsAndTrashMock()
	mc.appendErr = errors.New("NO [CANNOT] invalid message")
	events := make(chan msync.Event, 64)
	eng := msync.NewEngineWithClients(newTestAccount("test"), events, mc, mc)
	eng.SetDraftStore(ds)

	err := eng.SyncDrafts(context.Background())

	if err == nil || !strings.Contains(err.Error(), "invalid message") {
		t.Errorf("SyncDrafts error = %v, want the server's answer", err)
	}
	ev := nextEvent[msync.DraftSyncFailed](t, events)
	if ev.AccountName != "test" || ev.Subject != "Здравей" || ev.Err == nil {
		t.Errorf("DraftSyncFailed = %+v", ev)
	}
	metas, _ := ds.List("test")
	if len(metas) != 1 || metas[0].ID != id || !metas[0].Dirty {
		t.Errorf("a failed upload must leave the draft pending: %+v", metas)
	}
}

func TestEngine_emptyTrashExpungesTheTrashFolder(t *testing.T) {
	mc := draftsAndTrashMock()
	eng := msync.NewEngineWithClients(newTestAccount("test"), make(chan msync.Event, 8), mc, mc)
	if _, err := eng.Folders(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.EmptyTrash(context.Background()); err != nil {
		t.Fatalf("EmptyTrash: %v", err)
	}
	if mc.expungeAllFolder != "Deleted Items" {
		t.Errorf("emptied %q, want Deleted Items", mc.expungeAllFolder)
	}
}

func TestEngine_emptyTrashWithoutTrashFolderFails(t *testing.T) {
	mc := newMock(inboxOnly())
	eng := msync.NewEngineWithClients(newTestAccount("test"), make(chan msync.Event, 8), mc, mc)
	_, _ = eng.Folders(context.Background())
	if _, err := eng.EmptyTrash(context.Background()); err == nil {
		t.Error("EmptyTrash with no trash folder must fail, never touch another folder")
	}
	if mc.expungeAllFolder != "" {
		t.Errorf("emptied %q", mc.expungeAllFolder)
	}
}
