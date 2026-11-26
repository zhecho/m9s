package sync_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/mail/gmail"
	"github.com/zhecho/m9s/internal/mail/gmail/gmailtest"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	"github.com/zhecho/m9s/internal/store"
	msync "github.com/zhecho/m9s/internal/sync"
)

const gAcct = "gmail-test"

func TestGmailEngineImplementsAccount(t *testing.T) {
	var _ msync.Account = (*msync.GmailEngine)(nil)
}

func gRaw(subject, msgID string) []byte {
	return []byte(fmt.Sprintf("From: Bob <bob@example.com>\r\nTo: me@example.com\r\nSubject: %s\r\n"+
		"Date: Mon, 02 Jan 2026 15:04:05 +0000\r\nMessage-ID: <%s>\r\n\r\nbody of %s\r\n", subject, msgID, subject))
}

type gHarness struct {
	t   *testing.T
	f   *gmailtest.Fake
	st  *store.Store
	ds  *draft.Store
	eng *msync.GmailEngine

	setup func(*msync.GmailEngine) // runs on every new engine before Start
	quota gmail.Option             // the client's quota; nil: no limit to speak of

	mu     gosync.Mutex
	events []msync.Event
	stop   func()
}

// newGmail starts a fake with a "Work" label (synced through the folder
// include list); seed fills the fake before the engine starts.
func newGmail(t *testing.T, seed func(f *gmailtest.Fake)) *gHarness {
	t.Helper()
	return newGmailSetup(t, seed, nil)
}

// newGmailSetup is newGmail with setup run on the engine before it starts.
func newGmailSetup(t *testing.T, seed func(f *gmailtest.Fake), setup func(*msync.GmailEngine)) *gHarness {
	t.Helper()
	f := gmailtest.New()
	t.Cleanup(f.Close)
	f.AddLabel("Label_1", "Work")
	if seed != nil {
		seed(f)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := &gHarness{t: t, f: f, st: st, ds: draft.NewStore(t.TempDir()), setup: setup}
	h.start("Work")
	t.Cleanup(func() { h.stop() })
	return h
}

// start runs a new engine on the harness's fake and store, syncing the
// auto-detected folders plus include.
func (h *gHarness) start(include ...string) {
	cfg := &config.Account{Name: gAcct, Provider: "gmail", Gmail: config.GmailConfig{User: "me@example.com"},
		Folders: config.FolderConfig{
			BadgePoll:     config.Duration(100 * time.Millisecond),
			BodyCachePoll: config.Duration(100 * time.Millisecond),
		}}
	for _, name := range include {
		cfg.Folders.Include = append(cfg.Folders.Include, config.FolderEntry{Name: name})
	}
	events := make(chan msync.Event)
	quota := h.quota
	if quota == nil {
		quota = gmail.WithQuota(1e6, 1e6, 0) // tests run many quick requests
	}
	h.eng = msync.NewGmailEngine(cfg, events, h.st, h.ds, gmail.New(h.f.Server.Client(), gmail.WithBaseURL(h.f.URL()), quota))
	ctx, cancel := context.WithCancel(context.Background())
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case ev := <-events:
				h.mu.Lock()
				h.events = append(h.events, ev)
				h.mu.Unlock()
			case <-ctx.Done():
				return
			}
		}
	}()
	if h.setup != nil {
		h.setup(h.eng)
	}
	h.eng.Start(ctx)
	eng := h.eng
	h.stop = func() { cancel(); <-eng.Done(); <-drained }
}

func (h *gHarness) eventually(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *gHarness) snapshot() []msync.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.events)
}

// synced waits for the initial sync: the cursor is stored when it is done.
func (h *gHarness) synced() {
	h.t.Helper()
	h.eventually("initial sync", func() bool { return h.st.Cursor(gAcct) != "" })
}

func (h *gHarness) ids(folder string) []string {
	envs, _ := h.st.Envelopes(gAcct, folder)
	var ids []string
	for _, e := range envs {
		ids = append(ids, e.ID)
	}
	slices.Sort(ids)
	return ids
}

func (h *gHarness) env(folder, id string) *mail.Envelope {
	env, _ := h.st.Envelope(gAcct, folder, id)
	return env
}

func rawFetches(f *gmailtest.Fake, id string) int {
	n := 0
	for _, r := range f.Requests() {
		if r.Path == "/gmail/v1/users/me/messages/"+id && strings.Contains(r.Query, "format=raw") {
			n++
		}
	}
	return n
}

func TestGmailEngine_InitialSyncFillsStore(t *testing.T) {
	var both, inboxOnly string
	h := newGmail(t, func(f *gmailtest.Fake) {
		both = f.AddMessage(gmailtest.Message{ThreadID: "T1", LabelIDs: []string{"INBOX", "Label_1", "UNREAD"},
			InternalDate: 1_700_000_001_000, Raw: gRaw("both", "both@x")})
		inboxOnly = f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX", "STARRED"}, Raw: gRaw("inbox", "inbox@x")})
	})
	h.synced()
	if got := h.ids("INBOX"); !slices.Equal(got, slices.Sorted(slices.Values([]string{both, inboxOnly}))) {
		t.Fatalf("INBOX = %v", got)
	}
	if got := h.ids("Work"); !slices.Equal(got, []string{both}) {
		t.Fatalf("Work = %v", got)
	}
	env := h.env("Work", both)
	if env.ThreadID != "T1" || env.SortKey != 1_700_000_001_000 || env.Subject != "both" ||
		env.MessageID != "both@x" || len(env.From) != 1 || env.From[0].Email != "bob@example.com" || !env.IsUnread() {
		t.Fatalf("envelope = %+v", env)
	}
	if e := h.env("INBOX", inboxOnly); e.IsUnread() || !e.IsFlagged() {
		t.Fatalf("flags = %v", e.Flags)
	}
	h.eventually("bodies", func() bool {
		_, a := h.st.Body(gAcct, "", both)
		_, b := h.st.Body(gAcct, "", inboxOnly)
		return a && b
	})
	if n := rawFetches(h.f, both); n != 1 {
		t.Fatalf("message in two labels downloaded %d times", n)
	}
	msg, err := h.eng.FetchBody(context.Background(), "Work", both)
	if err != nil || !strings.Contains(msg.PlainText(), "body of both") {
		t.Fatalf("FetchBody: %v %v", msg, err)
	}
	if rawFetches(h.f, both) != 1 {
		t.Fatal("FetchBody went to the server for a stored body")
	}
	if got := h.st.Cursor(gAcct); got != h.f.HistoryID() {
		t.Fatalf("cursor = %q, want %q", got, h.f.HistoryID())
	}

	// The run loop announced itself: connecting, folders, connected.
	var states []msync.ConnState
	var folders []string
	for _, ev := range h.snapshot() {
		switch ev := ev.(type) {
		case msync.AccountStatus:
			states = append(states, ev.State)
		case msync.FoldersUpdated:
			for _, f := range ev.Folders {
				folders = append(folders, f.Name)
			}
		}
	}
	if len(states) < 2 || states[0] != msync.StateConnecting || states[1] != msync.StateConnected {
		t.Fatalf("states = %v", states)
	}
	for _, want := range []string{"INBOX", "Sent", "Drafts", "Trash", "Spam", "Work"} {
		if !slices.Contains(folders, want) {
			t.Fatalf("folders = %v, missing %s", folders, want)
		}
	}
	for _, hidden := range []string{"UNREAD", "IMPORTANT", "CATEGORY_PERSONAL", "CHAT"} {
		if slices.Contains(folders, hidden) {
			t.Fatalf("folders = %v show %s", folders, hidden)
		}
	}
	envs, err := h.eng.FetchHeaders(context.Background(), "INBOX", 1, store.ListSize)
	if err != nil || len(envs) != 2 {
		t.Fatalf("FetchHeaders = %d, %v", len(envs), err)
	}
}

func TestGmailEngine_PollAddsNewInboxMail(t *testing.T) {
	h := newGmail(t, nil)
	h.synced()
	id := h.f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX", "UNREAD"}, Raw: gRaw("new", "new@x")})
	h.eventually("NewMail", func() bool {
		for _, ev := range h.snapshot() {
			if nm, ok := ev.(msync.NewMail); ok && nm.FolderName == "INBOX" && nm.AccountName == gAcct {
				return true
			}
		}
		return false
	})
	if got := h.ids("INBOX"); !slices.Equal(got, []string{id}) {
		t.Fatalf("INBOX = %v", got)
	}
	h.eventually("INBOX badge", func() bool {
		for _, ev := range h.snapshot() {
			if b, ok := ev.(msync.BadgeUpdated); ok && b.FolderName == "INBOX" && b.Unseen == 1 && b.Total == 1 {
				return true
			}
		}
		return false
	})
	// Mail filed under a synced label without INBOX is stored, but is not new mail.
	h.mu.Lock()
	h.events = nil
	h.mu.Unlock()
	work := h.f.AddMessage(gmailtest.Message{LabelIDs: []string{"Label_1"}, Raw: gRaw("w", "w@x")})
	h.eventually("Work row", func() bool { return slices.Contains(h.ids("Work"), work) })
	for _, ev := range h.snapshot() {
		if _, ok := ev.(msync.NewMail); ok {
			t.Fatal("NewMail for a message outside INBOX")
		}
	}
}

func TestGmailEngine_HistoryNotFoundResyncs(t *testing.T) {
	var keep, gone string
	h := newGmail(t, func(f *gmailtest.Fake) {
		keep = f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX"}, Raw: gRaw("keep", "keep@x")})
		gone = f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX"}, Raw: gRaw("gone", "gone@x")})
	})
	h.synced()
	before := h.st.Cursor(gAcct)
	h.f.RemoveMessage(gone)
	h.f.ModifyLabels(keep, []string{"STARRED"}, nil)
	h.f.ExpireHistory()
	added := h.f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX"}, Raw: gRaw("added", "added@x")})

	want := slices.Sorted(slices.Values([]string{keep, added}))
	h.eventually("resync", func() bool { return slices.Equal(h.ids("INBOX"), want) })
	if e := h.env("INBOX", keep); !e.IsFlagged() {
		t.Fatalf("flags of kept message = %v", e.Flags)
	}
	h.eventually("cursor", func() bool { return h.st.Cursor(gAcct) == h.f.HistoryID() })
	if h.st.Cursor(gAcct) == before {
		t.Fatal("cursor not moved")
	}
	envs, _ := h.st.Envelopes(gAcct, "INBOX")
	if len(envs) != 2 {
		t.Fatalf("rows = %d, want 2 (no duplicates)", len(envs))
	}
}

func TestGmailEngine_InvalidGrantStopsWithoutRetry(t *testing.T) {
	h := newGmail(t, func(f *gmailtest.Fake) {
		f.Queue("/gmail/v1/users/me/profile", 401, `{"error":{"code":401,"status":"UNAUTHENTICATED"}}`)
	})
	var failed msync.AccountStatus
	h.eventually("failure", func() bool {
		for _, ev := range h.snapshot() {
			if st, ok := ev.(msync.AccountStatus); ok && st.State == msync.StateFailed {
				failed = st
				return true
			}
		}
		return false
	})
	if failed.RetryIn != 0 || failed.AccountName != gAcct {
		t.Fatalf("status = %+v", failed)
	}
	if !errors.Is(failed.Err, gmail.ErrSignInAgain) || !strings.Contains(failed.Err.Error(), "m9s login gmail-test, then restart m9s") {
		t.Fatalf("err = %v", failed.Err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := h.f.Count("/gmail/v1/users/me/profile"); n != 1 {
		t.Fatalf("profile asked %d times: retried by itself", n)
	}
	h.eng.Retry() // a passing 401: Retry connects again with the same token
	h.synced()
}

// failures returns every StateFailed status so far.
func (h *gHarness) failures() []msync.AccountStatus {
	var out []msync.AccountStatus
	for _, ev := range h.snapshot() {
		if st, ok := ev.(msync.AccountStatus); ok && st.State == msync.StateFailed {
			out = append(out, st)
		}
	}
	return out
}

const gProfile = "/gmail/v1/users/me/profile"

// With a way to read the sign-in again, Retry after "sign in again" reads
// it before connecting, so a new m9s login needs no restart.
func TestGmailEngine_RetryAfterSignInAgainReadsSignInAgain(t *testing.T) {
	var mu gosync.Mutex
	reads := 0
	h := newGmailSetup(t, func(f *gmailtest.Fake) {
		f.Queue(gProfile, 401, `{"error":{"code":401,"status":"UNAUTHENTICATED"}}`)
	}, func(e *msync.GmailEngine) {
		e.SetReauth(func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			reads++
			return nil
		})
	})
	h.eventually("failure", func() bool { return len(h.failures()) == 1 })
	failed := h.failures()[0]
	if !errors.Is(failed.Err, gmail.ErrSignInAgain) || !strings.Contains(failed.Err.Error(), "m9s login gmail-test, then retry (S)") {
		t.Fatalf("err = %v", failed.Err)
	}
	mu.Lock()
	if reads != 0 {
		t.Fatalf("sign-in read %d times before any Retry", reads)
	}
	mu.Unlock()

	h.eng.Retry()
	h.synced()
	mu.Lock()
	defer mu.Unlock()
	if reads != 1 {
		t.Fatalf("sign-in read %d times, want 1", reads)
	}
}

// When reading the sign-in again fails (KeePassXC locked), the engine says
// why, does not call Gmail, and waits for the next Retry, which reads again.
func TestGmailEngine_ReauthFailureWaitsForRetry(t *testing.T) {
	locked := errors.New("KeePassXC is locked")
	var mu gosync.Mutex
	reads, fail := 0, true
	h := newGmailSetup(t, func(f *gmailtest.Fake) {
		f.Queue(gProfile, 401, `{"error":{"code":401,"status":"UNAUTHENTICATED"}}`)
	}, func(e *msync.GmailEngine) {
		e.SetReauth(func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			reads++
			if fail {
				return locked
			}
			return nil
		})
	})
	h.eventually("first failure", func() bool { return len(h.failures()) == 1 })
	h.eng.Retry()
	h.eventually("second failure", func() bool { return len(h.failures()) == 2 })
	second := h.failures()[1]
	if !errors.Is(second.Err, locked) || second.RetryIn != 0 {
		t.Fatalf("status = %+v", second)
	}
	if n := h.f.Count(gProfile); n != 1 {
		t.Fatalf("profile asked %d times: Gmail called without a sign-in", n)
	}

	mu.Lock()
	fail = false
	mu.Unlock()
	h.eng.Retry()
	h.synced()
	mu.Lock()
	defer mu.Unlock()
	if reads != 2 {
		t.Fatalf("sign-in read %d times, want 2", reads)
	}
}

func TestGmailEngine_FlagChangeVisibleInAllLabels(t *testing.T) {
	var id string
	h := newGmail(t, func(f *gmailtest.Fake) {
		id = f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX", "Label_1", "UNREAD"}, Raw: gRaw("s", "s@x")})
	})
	h.synced()
	ctx := context.Background()
	if err := h.eng.SetFlags(ctx, "INBOX", id, []string{`\Seen`}, true); err != nil {
		t.Fatal(err)
	}
	if m, _ := h.f.Message(id); slices.Contains(m.LabelIDs, "UNREAD") {
		t.Fatalf("server labels = %v", m.LabelIDs)
	}
	for _, folder := range []string{"INBOX", "Work"} {
		if e := h.env(folder, id); e.IsUnread() {
			t.Fatalf("%s still unread: %v", folder, e.Flags)
		}
	}
	// Starred on another device: both rows follow, and Starred is not synced
	// (not auto-detected, not included) so no row appears there.
	h.f.ModifyLabels(id, []string{"STARRED"}, nil)
	h.eventually("flagged everywhere", func() bool {
		a, b := h.env("INBOX", id), h.env("Work", id)
		return a != nil && b != nil && a.IsFlagged() && b.IsFlagged()
	})
	if err := h.eng.SetFlags(ctx, "Work", id, []string{`\Flagged`}, false); err != nil {
		t.Fatal(err)
	}
	if m, _ := h.f.Message(id); slices.Contains(m.LabelIDs, "STARRED") {
		t.Fatalf("server labels = %v", m.LabelIDs)
	}
	if e := h.env("INBOX", id); e.IsFlagged() {
		t.Fatal("INBOX row still flagged")
	}
	// A label removed elsewhere drops the row of that folder only.
	h.f.ModifyLabels(id, nil, []string{"Label_1"})
	h.eventually("Work row gone", func() bool { return len(h.ids("Work")) == 0 })
	if !slices.Equal(h.ids("INBOX"), []string{id}) {
		t.Fatal("INBOX row lost")
	}
}

func TestGmailEngine_SendDoesNotSaveSentCopy(t *testing.T) {
	var orig string
	h := newGmail(t, func(f *gmailtest.Fake) {
		orig = f.AddMessage(gmailtest.Message{ThreadID: "T7", LabelIDs: []string{"INBOX"}, Raw: gRaw("hello", "orig@x")})
	})
	h.synced()
	ctx := context.Background()
	if !h.eng.ServerSavesSent() {
		t.Fatal("Gmail keeps its own Sent copy")
	}
	d := smtpadapter.Draft{From: "me@example.com", To: []string{"bob@example.com"}, Subject: "Re: hello",
		Body: "hi", InReplyTo: "<orig@x>", References: "<orig@x>"}
	sent, err := h.eng.Send(ctx, d)
	if err != nil || !strings.Contains(string(sent), "Subject: Re: hello") {
		t.Fatalf("Send: %v", err)
	}
	ids := h.f.MessageIDs("SENT")
	if len(ids) != 1 {
		t.Fatalf("sent messages = %v", ids)
	}
	m, _ := h.f.Message(ids[0])
	if m.ThreadID != "T7" {
		t.Fatalf("reply thread = %q, want T7 (of %s)", m.ThreadID, orig)
	}
	before := len(h.f.Requests())
	folder, err := h.eng.SaveSentCopy(ctx, sent)
	if err != nil || folder != h.eng.SentFolderName() || folder != "Sent" {
		t.Fatalf("SaveSentCopy = %q, %v", folder, err)
	}
	for _, r := range h.f.Requests()[before:] {
		if r.Method != "GET" {
			t.Fatalf("SaveSentCopy wrote to the server: %+v", r)
		}
	}
	if len(h.f.MessageIDs("SENT")) != 1 {
		t.Fatal("a second Sent copy")
	}
	// The sent message shows up in Sent through the poll.
	h.eventually("Sent row", func() bool { return slices.Equal(h.ids("Sent"), ids) })

	// A new conversation: no thread.
	if _, err := h.eng.Send(ctx, smtpadapter.Draft{From: "me@example.com", To: []string{"x@example.com"}, Subject: "new", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range h.f.MessageIDs("SENT") {
		if m, _ := h.f.Message(id); id != ids[0] && m.ThreadID == "T7" {
			t.Fatal("new conversation filed in T7")
		}
	}
}

func TestGmailEngine_TrashAndEmptyTrash(t *testing.T) {
	var a, b, old string
	h := newGmail(t, func(f *gmailtest.Fake) {
		a = f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX", "Label_1"}, Raw: gRaw("a", "a@x")})
		b = f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX"}, Raw: gRaw("b", "b@x")})
		old = f.AddMessage(gmailtest.Message{LabelIDs: []string{"TRASH"}, Raw: gRaw("old", "old@x")})
	})
	h.synced()
	ctx := context.Background()
	if h.eng.TrashFolderName() != "Trash" {
		t.Fatalf("trash = %q", h.eng.TrashFolderName())
	}
	if !slices.Equal(h.ids("Trash"), []string{old}) {
		t.Fatalf("Trash = %v", h.ids("Trash"))
	}
	if err := h.eng.MoveToTrash(ctx, "INBOX", a); err != nil {
		t.Fatal(err)
	}
	if m, _ := h.f.Message(a); !slices.Contains(m.LabelIDs, "TRASH") {
		t.Fatalf("server labels = %v", m.LabelIDs)
	}
	if slices.Contains(h.ids("INBOX"), a) || slices.Contains(h.ids("Work"), a) {
		t.Fatal("trashed message still listed under its labels")
	}
	if !slices.Contains(h.ids("Trash"), a) {
		t.Fatalf("Trash = %v", h.ids("Trash"))
	}

	n, err := h.eng.EmptyTrash(ctx)
	if err != nil || n != 2 {
		t.Fatalf("EmptyTrash = %d, %v", n, err)
	}
	if len(h.f.MessageIDs("TRASH")) != 0 || len(h.ids("Trash")) != 0 {
		t.Fatal("trash not empty")
	}
	if h.f.Count("/gmail/v1/users/me/messages/batchDelete") != 1 {
		t.Fatal("EmptyTrash should use one batchDelete")
	}

	if err := h.eng.MoveToTrash(ctx, "INBOX", b); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.PermanentDelete(ctx, "Trash", b); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.f.Message(b); ok || len(h.ids("INBOX")) != 0 || len(h.ids("Trash")) != 0 {
		t.Fatal("permanently deleted message remains")
	}
	// Already gone on the server: done, and its rows go too.
	if err := h.eng.PermanentDelete(ctx, "Trash", b); err != nil {
		t.Fatalf("delete of a gone message: %v", err)
	}
}

func deletes(f *gmailtest.Fake) int {
	n := 0
	for _, r := range f.Requests() {
		if r.Method == "DELETE" || r.Path == "/gmail/v1/users/me/messages/batchDelete" {
			n++
		}
	}
	return n
}

func TestGmailEngine_PermanentDeleteRefusesWhenNotInTrash(t *testing.T) {
	var id string
	h := newGmail(t, func(f *gmailtest.Fake) {
		id = f.AddMessage(gmailtest.Message{LabelIDs: []string{"TRASH"}, Raw: gRaw("t", "t@x")})
	})
	h.synced()
	if !slices.Equal(h.ids("Trash"), []string{id}) {
		t.Fatalf("Trash = %v", h.ids("Trash"))
	}
	// Restored on another device; the Trash row the UI shows is now stale.
	h.f.ModifyLabels(id, []string{"INBOX"}, []string{"TRASH"})
	err := h.eng.PermanentDelete(context.Background(), "Trash", id)
	if !errors.Is(err, msync.ErrNotInTrash) {
		t.Fatalf("err = %v, want ErrNotInTrash", err)
	}
	if _, ok := h.f.Message(id); !ok {
		t.Fatal("restored message was deleted")
	}
	if n := deletes(h.f); n != 0 {
		t.Fatalf("%d delete requests sent", n)
	}
	if len(h.ids("Trash")) != 0 || !slices.Equal(h.ids("INBOX"), []string{id}) {
		t.Fatalf("rows not refreshed: Trash %v INBOX %v", h.ids("Trash"), h.ids("INBOX"))
	}
}

func TestGmailEngine_NewlyIncludedLabelSnapshottedAfterRestart(t *testing.T) {
	var later []string
	h := newGmail(t, func(f *gmailtest.Fake) {
		f.AddLabel("Label_2", "Later")
		for i := 0; i < 4; i++ {
			later = append(later, f.AddMessage(gmailtest.Message{LabelIDs: []string{"Label_2"}, Raw: gRaw("l", fmt.Sprint("l", i, "@x"))}))
		}
	})
	h.synced()
	h.stop()
	// A change in the not-yet-synced label lands in the history the restart applies.
	h.f.ModifyLabels(later[0], []string{"STARRED"}, nil)
	later = append(later, h.f.AddMessage(gmailtest.Message{LabelIDs: []string{"Label_2"}, Raw: gRaw("n", "n@x")}))

	h.start("Work", "Later")
	want := slices.Sorted(slices.Values(later))
	h.eventually("every Later message", func() bool { return slices.Equal(h.ids("Later"), want) })
	if e := h.env("Later", later[0]); !e.IsFlagged() {
		t.Fatalf("flags = %v", e.Flags)
	}
}

func TestGmailEngine_StoreErrorDoesNotAdvanceCursor(t *testing.T) {
	h := newGmail(t, nil)
	h.synced()
	before := h.st.Cursor(gAcct)
	id := h.f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX"}, Raw: gRaw("x", "x@x")})
	// Gmail describes the message without an id: the store refuses the row.
	path := "/gmail/v1/users/me/messages/" + id
	h.f.Handle(path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"","threadId":"t","labelIds":["INBOX"],"internalDate":"1"}`)
	})
	h.eventually("failed poll", func() bool {
		for _, ev := range h.snapshot() {
			if st, ok := ev.(msync.AccountStatus); ok && st.State == msync.StateFailed {
				return true
			}
		}
		return false
	})
	if got := h.st.Cursor(gAcct); got != before {
		t.Fatalf("cursor moved %q -> %q past a change that was not stored", before, got)
	}
	// Once the store takes it, the same change is applied: nothing was lost.
	h.f.Handle(path, nil)
	h.eng.Retry()
	h.eventually("change applied", func() bool { return slices.Equal(h.ids("INBOX"), []string{id}) })
	h.eventually("cursor", func() bool { return h.st.Cursor(gAcct) == h.f.HistoryID() })
}

func TestGmailEngine_DraftCreateUpdateDelete(t *testing.T) {
	h := newGmail(t, nil)
	h.synced()
	ctx := context.Background()
	if h.eng.DraftsFolderName() != "Drafts" {
		t.Fatalf("drafts = %q", h.eng.DraftsFolderName())
	}
	id, err := h.eng.SaveDraft(ctx, gRaw("draft one", "d1@x"), "")
	if err != nil || id == "" {
		t.Fatalf("SaveDraft: %q %v", id, err)
	}
	same, err := h.eng.SaveDraft(ctx, gRaw("draft two", "d2@x"), id)
	if err != nil || same != id {
		t.Fatalf("update: %q %v", same, err)
	}
	if m, _ := h.f.Message(h.f.Drafts()[id]); !strings.Contains(string(m.Raw), "draft two") {
		t.Fatalf("draft not replaced: %q", m.Raw)
	}
	h.eventually("Drafts row", func() bool { return len(h.ids("Drafts")) == 1 })
	if e := h.env("Drafts", h.ids("Drafts")[0]); !slices.Contains(e.Flags, `\Draft`) {
		t.Fatalf("draft flags = %v", e.Flags)
	}
	if err := h.eng.DeleteDraftCopy(ctx, id); err != nil {
		t.Fatal(err)
	}
	if len(h.f.Drafts()) != 0 {
		t.Fatal("draft left on the server")
	}
	h.eventually("Drafts empty", func() bool { return len(h.ids("Drafts")) == 0 })

	// A superseded id that is gone: a new draft is created instead.
	id2, err := h.eng.SaveDraft(ctx, gRaw("draft three", "d3@x"), id)
	if err != nil || id2 == "" || id2 == id {
		t.Fatalf("save over gone draft: %q %v", id2, err)
	}
	if err := h.eng.TrashDraftCopy(ctx, id2); err != nil || len(h.f.Drafts()) != 0 {
		t.Fatalf("TrashDraftCopy: %v %v", err, h.f.Drafts())
	}

	// Local drafts are uploaded by SyncDrafts and remember the draft id.
	local, err := h.ds.Save(gAcct, smtpadapter.Draft{From: "me@example.com", To: []string{"x@example.com"}, Subject: "local", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.eng.SyncDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	metas, _ := h.ds.List(gAcct)
	if len(metas) != 1 || metas[0].ID != local || metas[0].Dirty || metas[0].SyncedID == "" {
		t.Fatalf("metas = %+v", metas)
	}
	if _, ok := h.f.Drafts()[metas[0].SyncedID]; !ok {
		t.Fatalf("server drafts = %v, synced id %q", h.f.Drafts(), metas[0].SyncedID)
	}
	h.eventually("DraftSynced", func() bool {
		for _, ev := range h.snapshot() {
			if ds, ok := ev.(msync.DraftSynced); ok && ds.DraftID == local {
				return true
			}
		}
		return false
	})
}

func TestGmailEngine_RestartContinuesFromHistory(t *testing.T) {
	var a, b string
	h := newGmail(t, func(f *gmailtest.Fake) {
		a = f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX"}, Raw: gRaw("a", "a@x")})
		f.AddLabel("Label_2", "Later")
		b = f.AddMessage(gmailtest.Message{LabelIDs: []string{"Label_2"}, Raw: gRaw("b", "b@x")})
	})
	h.synced()
	h.stop()
	c := h.f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX"}, Raw: gRaw("c", "c@x")})
	listedBefore := 0
	for _, r := range h.f.Requests() {
		if r.Path == "/gmail/v1/users/me/messages" && strings.Contains(r.Query, "labelIds=INBOX") {
			listedBefore++
		}
	}

	h.start("Work", "Later") // Later is newly included
	want := slices.Sorted(slices.Values([]string{a, c}))
	h.eventually("history applied", func() bool { return slices.Equal(h.ids("INBOX"), want) })
	h.eventually("new folder listed", func() bool { return slices.Equal(h.ids("Later"), []string{b}) })
	for _, r := range h.f.Requests() {
		if r.Path == "/gmail/v1/users/me/messages" && strings.Contains(r.Query, "labelIds=INBOX") {
			listedBefore--
		}
	}
	if listedBefore != 0 {
		t.Fatalf("INBOX listed again %d times: the restart should use history", -listedBefore)
	}
	h.eventually("cursor", func() bool { return h.st.Cursor(gAcct) == h.f.HistoryID() })
	h.eventually("badges after restart", func() bool {
		for _, ev := range h.snapshot() {
			if b, ok := ev.(msync.BadgeUpdated); ok && b.FolderName == "Later" {
				return true
			}
		}
		return false
	})
}

// A Drafts row is named by its MESSAGE id, while SaveDraft hands out DRAFT
// ids: the engine maps one to the other, and deleting by either removes
// the one draft. An id no draft holds is an error, not a silent success.
func TestGmailEngine_DraftCopyByMessageID(t *testing.T) {
	h := newGmail(t, nil)
	h.synced()
	ctx := context.Background()
	did, err := h.eng.SaveDraft(ctx, gRaw("draft", "dm@x"), "")
	if err != nil {
		t.Fatal(err)
	}
	mid := h.f.Drafts()[did]
	if mid == "" || mid == did {
		t.Fatalf("draft %q holds message %q", did, mid)
	}
	var dc msync.DraftCopies = h.eng
	if got, err := dc.DraftCopyOf(ctx, mid); err != nil || got != did {
		t.Fatalf("DraftCopyOf(%s) = %q, %v; want %q", mid, got, err, did)
	}
	if got, err := dc.DraftCopyOf(ctx, "m-unknown"); err != nil || got != "" {
		t.Fatalf("DraftCopyOf(unknown) = %q, %v", got, err)
	}
	if err := h.eng.DeleteDraftCopy(ctx, mid); err != nil {
		t.Fatalf("DeleteDraftCopy(message id): %v", err)
	}
	if n := len(h.f.Drafts()); n != 0 {
		t.Fatalf("%d drafts left after deleting by message id", n)
	}
	if err := h.eng.DeleteDraftCopy(ctx, mid); err == nil {
		t.Fatal("deleting a draft no one holds reported success")
	}
	if err := h.eng.TrashDraftCopy(ctx, "m-unknown"); err == nil {
		t.Fatal("TrashDraftCopy of an unknown id reported success")
	}
}

// Move swaps the open label for the target; Archive takes the message out of
// the Inbox and leaves its other labels.
func TestGmailEngine_MoveAndArchive(t *testing.T) {
	var moved, archived string
	h := newGmail(t, func(f *gmailtest.Fake) {
		moved = f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX"}, Raw: gRaw("m", "m@x")})
		archived = f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX", "Label_1"}, Raw: gRaw("a", "a@x")})
	})
	h.synced()
	ctx := context.Background()

	if err := h.eng.Move(ctx, "INBOX", moved, "Work"); err != nil {
		t.Fatal(err)
	}
	if m, _ := h.f.Message(moved); slices.Contains(m.LabelIDs, "INBOX") || !slices.Contains(m.LabelIDs, "Label_1") {
		t.Errorf("labels after move = %v, want Label_1 without INBOX", m.LabelIDs)
	}
	h.eventually("moved row", func() bool { return h.env("Work", moved) != nil && h.env("INBOX", moved) == nil })

	if err := h.eng.Archive(ctx, "INBOX", archived); err != nil {
		t.Fatal(err)
	}
	if m, _ := h.f.Message(archived); slices.Contains(m.LabelIDs, "INBOX") || !slices.Contains(m.LabelIDs, "Label_1") {
		t.Errorf("labels after archive = %v, want Label_1 kept and INBOX gone", m.LabelIDs)
	}
	if err := h.eng.Move(ctx, "INBOX", moved, "INBOX"); err == nil {
		t.Error("a move onto the same label must be refused")
	}
}

// Drafts, Sent and Trash are not move targets: Gmail refuses those labels
// (Trash has d), and the open folder is not offered either.
func TestGmailEngine_MoveTargets(t *testing.T) {
	h := newGmail(t, nil)
	h.synced()
	got := h.eng.MoveTargets("INBOX")
	if !slices.Contains(got, "Work") {
		t.Errorf("MoveTargets = %v, want Work", got)
	}
	for _, no := range []string{"INBOX", "Drafts", "Sent", "Trash"} {
		if slices.Contains(got, no) {
			t.Errorf("MoveTargets = %v offers %s", got, no)
		}
	}
}

// The sync's requests are background work: they leave the quota's reserve
// to the user. Here the reserve is the whole bucket, so the sync stalls
// after its first request while opening a message still works.
func TestGmailEngine_SyncLeavesTheQuotaReserveToTheUser(t *testing.T) {
	f := gmailtest.New()
	t.Cleanup(f.Close)
	id := f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX"}, Raw: gRaw("hello", "hello@x")})
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := &gHarness{t: t, f: f, st: st, ds: draft.NewStore(t.TempDir()), quota: gmail.WithQuota(1e-6, 1000, 1000)}
	h.start()
	t.Cleanup(func() { h.stop() })

	msg, err := h.eng.FetchBody(context.Background(), "INBOX", id)
	if err != nil || !strings.Contains(msg.PlainText(), "body of hello") {
		t.Fatalf("FetchBody = %v, %v", msg, err)
	}
	for deadline := time.Now().Add(500 * time.Millisecond); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if c := h.st.Cursor(gAcct); c != "" {
			t.Fatalf("the sync went into the reserve: cursor %q stored", c)
		}
	}
}

func metadataFetches(f *gmailtest.Fake, id string) int {
	n := 0
	for _, r := range f.Requests() {
		if r.Path == "/gmail/v1/users/me/messages/"+id && strings.Contains(r.Query, "format=metadata") {
			n++
		}
	}
	return n
}

// A message under two synced labels has its headers fetched once (each
// fetch costs 20 quota units); the second label reuses them, with its own
// listing's flags.
func TestGmailEngine_MessageInTwoLabelsFetchedOnce(t *testing.T) {
	var both string
	h := newGmail(t, func(f *gmailtest.Fake) {
		both = f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX", "Label_1", "UNREAD", "STARRED"}, Raw: gRaw("both", "both@x")})
	})
	h.synced()
	if n := metadataFetches(h.f, both); n != 1 {
		t.Fatalf("headers fetched %d times, want 1", n)
	}
	for _, folder := range []string{"INBOX", "Work"} {
		e := h.env(folder, both)
		if e == nil || e.Subject != "both" || !e.IsUnread() || !e.IsFlagged() {
			t.Fatalf("%s: %+v", folder, e)
		}
	}
}

// A selection in Trash is deleted with one batchDelete, not a request or
// two per message, and keeps the guard: one restored meanwhile is refused,
// one already gone is done.
func TestGmailEngine_PermanentDeleteManyBatchesInTrashOnly(t *testing.T) {
	var trashed []string
	var restored, gone string
	h := newGmail(t, func(f *gmailtest.Fake) {
		for i := range 3 {
			trashed = append(trashed, f.AddMessage(gmailtest.Message{LabelIDs: []string{"TRASH"}, Raw: gRaw("t", fmt.Sprint("t", i, "@x"))}))
		}
		restored = f.AddMessage(gmailtest.Message{LabelIDs: []string{"TRASH"}, Raw: gRaw("r", "r@x")})
		gone = f.AddMessage(gmailtest.Message{LabelIDs: []string{"TRASH"}, Raw: gRaw("g", "g@x")})
	})
	h.synced()
	h.f.ModifyLabels(restored, []string{"INBOX"}, []string{"TRASH"}) // on another device
	h.f.RemoveMessage(gone)                                          // deleted elsewhere

	bulk, ok := msync.Account(h.eng).(msync.BulkDeleter)
	if !ok {
		t.Fatal("the Gmail engine does not delete in bulk")
	}
	reached := 0
	failed := bulk.PermanentDeleteMany(context.Background(), "Trash",
		append(slices.Clone(trashed), restored, gone), func(n int) { reached += n })

	if len(failed) != 1 || !errors.Is(failed[restored], msync.ErrNotInTrash) {
		t.Fatalf("failed = %v, want only the restored one, ErrNotInTrash", failed)
	}
	for _, id := range trashed {
		if _, ok := h.f.Message(id); ok {
			t.Errorf("%s is still on the server", id)
		}
	}
	if _, ok := h.f.Message(restored); !ok {
		t.Fatal("the restored message was deleted")
	}
	if n := h.f.Count("/gmail/v1/users/me/messages/batchDelete"); n != 1 || deletes(h.f) != 1 {
		t.Errorf("%d batchDelete, %d delete requests; want one batchDelete", n, deletes(h.f))
	}
	if reached != 5 {
		t.Errorf("progress reached %d, want 5", reached)
	}
	if got := h.ids("Trash"); len(got) != 0 {
		t.Errorf("Trash rows = %v", got)
	}
}
