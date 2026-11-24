package sync_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/mail/graph"
	"github.com/zhecho/m9s/internal/mail/graph/graphtest"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	"github.com/zhecho/m9s/internal/store"
	msync "github.com/zhecho/m9s/internal/sync"
)

const mAcct = "ms-test"

func mRaw(subject string) []byte {
	return gRaw(subject, subject+"@example.com")
}

// mRawWithFile is a message carrying one attached file.
func mRawWithFile(subject string) []byte {
	return []byte("From: Bob <bob@example.com>\r\nTo: me@outlook.com\r\nSubject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b1\r\n\r\n" +
		"--b1\r\nContent-Type: text/plain\r\n\r\nsee file\r\n" +
		"--b1\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"r.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\nJVBERi0=\r\n--b1--\r\n")
}

type mHarness struct {
	t   *testing.T
	f   *graphtest.Fake
	st  *store.Store
	ds  *draft.Store
	eng *msync.MicrosoftEngine

	mu     gosync.Mutex
	events []msync.Event
	stop   func()
}

// newMicrosoft starts an engine on a fresh fake; seed fills the fake
// first. include names folders synced beyond the auto-detected ones.
func newMicrosoft(t *testing.T, seed func(f *graphtest.Fake), include ...string) *mHarness {
	t.Helper()
	f := graphtest.New()
	t.Cleanup(f.Close)
	if seed != nil {
		seed(f)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := &mHarness{t: t, f: f, st: st, ds: draft.NewStore(t.TempDir())}
	cfg := &config.Account{Name: mAcct, Provider: config.ProviderMicrosoft, KeePassXC: true,
		Microsoft: config.MicrosoftConfig{User: "me@outlook.com", ClientID: "cid"},
		Folders: config.FolderConfig{
			BadgePoll:     config.Duration(100 * time.Millisecond),
			BodyCachePoll: config.Duration(100 * time.Millisecond),
		}}
	for _, name := range include {
		cfg.Folders.Include = append(cfg.Folders.Include, config.FolderEntry{Name: name})
	}
	events := make(chan msync.Event)
	api := graph.New(f.Server.Client(), graph.WithBaseURL(f.URL()), graph.WithRateLimit(10000, 10000))
	h.eng = msync.NewMicrosoftEngine(cfg, events, st, h.ds, api)
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
	h.eng.SetBackoff(func(int) time.Duration { return time.Hour })
	h.eng.Start(ctx)
	h.stop = func() { cancel(); <-h.eng.Done(); <-drained }
	t.Cleanup(h.stop)
	return h
}

func (h *mHarness) eventually(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *mHarness) snapshot() []msync.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.events)
}

// synced waits until every folder named has its delta link stored.
func (h *mHarness) synced(folders ...string) {
	h.t.Helper()
	if len(folders) == 0 {
		folders = []string{"INBOX", "Sent", "Drafts", "Trash", "Spam", "Archive"}
	}
	h.eventually("initial sync", func() bool {
		for _, f := range folders {
			if h.st.FolderCursor(mAcct, f) == "" {
				return false
			}
		}
		return true
	})
}

func (h *mHarness) ids(folder string) []string {
	envs, _ := h.st.Envelopes(mAcct, folder)
	ids := []string{}
	for _, e := range envs {
		ids = append(ids, e.ID)
	}
	slices.Sort(ids)
	return ids
}

func (h *mHarness) env(folder, id string) *mail.Envelope {
	env, _ := h.st.Envelope(mAcct, folder, id)
	return env
}

func sorted(ids ...string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

func (h *mHarness) failed() bool {
	for _, ev := range h.snapshot() {
		if st, ok := ev.(msync.AccountStatus); ok && st.State == msync.StateFailed {
			return true
		}
	}
	return false
}

func TestMicrosoftEngineImplements(t *testing.T) {
	var e any = (*msync.MicrosoftEngine)(nil)
	if _, ok := e.(msync.Account); !ok {
		t.Error("not an Account")
	}
	if _, ok := e.(msync.Labels); !ok {
		t.Error("not Labels")
	}
	if _, ok := e.(msync.ServerSearch); !ok {
		t.Error("not ServerSearch")
	}
	if _, ok := e.(msync.StoreKeeper); !ok {
		t.Error("not a StoreKeeper")
	}
	if _, ok := e.(msync.Mover); !ok {
		t.Error("not a Mover")
	}
	if _, ok := e.(msync.Threads); ok {
		t.Error("offers Threads: Microsoft rows are single messages")
	}
	if _, ok := e.(msync.DraftCopies); ok {
		t.Error("offers DraftCopies: a Microsoft draft is named by its message id")
	}
}

// Outlook names its folders in the mailbox's language; the special ones
// are known by Graph's well-known names whatever they are called. A "/" in
// a display name is not nesting.
func TestMicrosoftFolders_wellKnownByID(t *testing.T) {
	h := newMicrosoft(t, func(f *graphtest.Fake) {
		inbox := f.FolderID("inbox")
		f.SetFolderName(inbox, "Входящи")
		f.SetFolderName(f.FolderID("sentitems"), "Изпратени")
		p := f.AddFolder("", "Projects", "")
		f.AddFolder(p, "2026", "")
		f.AddFolder(inbox, "Projects/2026", "")
	}, "Projects/2026", "INBOX/Projects∕2026")
	h.synced()
	fs, err := h.eng.Folders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"INBOX", "Sent", "Drafts", "Trash", "Spam", "Archive", "Projects/2026", "INBOX/Projects∕2026"} {
		if folderNamed(fs, name) == nil {
			var got []string
			for _, f := range fs {
				got = append(got, f.Name)
			}
			t.Fatalf("no folder %q in %q", name, got)
		}
	}
	if f := folderNamed(fs, "Sent"); !slices.Contains(f.Attributes, `\Sent`) {
		t.Errorf("Sent attributes = %v", f.Attributes)
	}
	if f := folderNamed(fs, "Projects/2026"); f.DisplayName() != "2026" || f.Depth() != 1 {
		t.Errorf("nested folder: display %q depth %d", f.DisplayName(), f.Depth())
	}
	if f := folderNamed(fs, "INBOX/Projects∕2026"); f.DisplayName() != "Projects∕2026" || f.Depth() != 1 {
		t.Errorf("slash in a name: display %q depth %d", f.DisplayName(), f.Depth())
	}
}

func TestMicrosoftEngine_initialSyncFillsStore(t *testing.T) {
	var a, b, c string
	h := newMicrosoft(t, func(f *graphtest.Fake) {
		a = f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "a", Flagged: true, HasAttachments: true,
			Categories: []string{"Work"}, InternetMessageID: "<a@x>", Raw: mRawWithFile("a")})
		b = f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "b", IsRead: true, Raw: mRaw("b")})
		c = f.AddMessage(f.FolderID("archive"), graphtest.Message{Subject: "c", Raw: mRaw("c")})
	})
	h.synced()
	if got := h.ids("INBOX"); !slices.Equal(got, sorted(a, b)) {
		t.Fatalf("INBOX = %v", got)
	}
	if got := h.ids("Archive"); !slices.Equal(got, []string{c}) {
		t.Fatalf("Archive = %v", got)
	}
	ea, eb := h.env("INBOX", a), h.env("INBOX", b)
	if !ea.IsUnread() || !ea.IsFlagged() || !ea.HasAttach || !slices.Equal(ea.Categories, []string{"Work"}) ||
		ea.MessageID != "a@x" || ea.ThreadID != "" || ea.Subject != "a" || ea.SortKey == 0 {
		t.Errorf("a = %+v", ea)
	}
	if eb.IsUnread() || eb.IsFlagged() {
		t.Errorf("b flags = %v", eb.Flags)
	}
	h.eventually("bodies", func() bool {
		for _, id := range []string{a, b, c} {
			if _, ok := h.st.Body(mAcct, "", id); !ok {
				return false
			}
		}
		return true
	})
}

func TestMicrosoftEngine_pollAppliesDelta(t *testing.T) {
	var a, b string
	h := newMicrosoft(t, func(f *graphtest.Fake) {
		a = f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "a"})
		b = f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "b"})
	})
	h.synced()
	n := h.f.AddMessage(h.f.FolderID("inbox"), graphtest.Message{Subject: "new"})
	h.f.Delete(a)
	h.f.Update(b, func(m *graphtest.Message) { m.IsRead = true })
	h.eng.SyncAll(context.Background())
	h.eventually("delta applied", func() bool {
		e := h.env("INBOX", b)
		return slices.Equal(h.ids("INBOX"), sorted(b, n)) && e != nil && !e.IsUnread()
	})
	h.eventually("new mail event", func() bool {
		for _, ev := range h.snapshot() {
			if nm, ok := ev.(msync.NewMail); ok && nm.FolderName == "INBOX" && nm.AccountName == mAcct {
				return true
			}
		}
		return false
	})
}

func TestMicrosoftEngine_moveKeepsBody(t *testing.T) {
	var a string
	h := newMicrosoft(t, func(f *graphtest.Fake) {
		a = f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "a", Raw: mRaw("a")})
	})
	h.synced()
	h.eventually("body", func() bool { _, ok := h.st.Body(mAcct, "", a); return ok })
	h.f.Move(a, h.f.FolderID("archive"))
	h.eng.SyncAll(context.Background())
	h.eventually("row moved", func() bool {
		return !slices.Contains(h.ids("INBOX"), a) && slices.Contains(h.ids("Archive"), a)
	})
	time.Sleep(300 * time.Millisecond) // a few body passes
	if _, ok := h.st.Body(mAcct, "", a); !ok {
		t.Fatal("body lost in the move")
	}
	if n := h.f.Count("/v1.0/me/messages/" + a + "/$value"); n != 1 {
		t.Fatalf("body downloaded %d times", n)
	}
}

func TestMicrosoftEngine_expiredDeltaRelists(t *testing.T) {
	h := newMicrosoft(t, func(f *graphtest.Fake) {
		f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "a"})
	})
	h.synced()
	old := h.st.FolderCursor(mAcct, "INBOX")
	h.f.ExpireDelta(h.f.FolderID("inbox"))
	n := h.f.AddMessage(h.f.FolderID("inbox"), graphtest.Message{Subject: "new"})
	h.eng.SyncAll(context.Background())
	h.eventually("relisted", func() bool {
		c := h.st.FolderCursor(mAcct, "INBOX")
		return slices.Contains(h.ids("INBOX"), n) && c != "" && c != old
	})
	if h.failed() {
		t.Fatal("an expired delta link failed the account")
	}
}

func TestMicrosoftEngine_archive(t *testing.T) {
	var a string
	h := newMicrosoft(t, func(f *graphtest.Fake) {
		a = f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "a"})
	})
	h.synced()
	if n := h.eng.ArchiveFolderName(); n != "Archive" {
		t.Fatalf("ArchiveFolderName = %q", n)
	}
	if err := h.eng.Archive(context.Background(), "INBOX", a); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.ids("Archive"), a) || slices.Contains(h.ids("INBOX"), a) {
		t.Fatalf("INBOX %v, Archive %v", h.ids("INBOX"), h.ids("Archive"))
	}
	if m, _ := h.f.Message(a); m.FolderID != h.f.FolderID("archive") {
		t.Fatalf("server folder = %s", m.FolderID)
	}
}

func TestMicrosoftEngine_noArchiveFolder(t *testing.T) {
	var a string
	h := newMicrosoft(t, func(f *graphtest.Fake) {
		f.RemoveFolder(f.FolderID("archive"))
		a = f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "a"})
	})
	h.synced("INBOX", "Sent", "Drafts", "Trash", "Spam")
	if n := h.eng.ArchiveFolderName(); n != "" {
		t.Fatalf("ArchiveFolderName = %q", n)
	}
	err := h.eng.Archive(context.Background(), "INBOX", a)
	if !errors.Is(err, msync.ErrNoArchiveFolder) || err.Error() != "no Archive folder on this account" {
		t.Fatalf("err = %v", err)
	}
}

func TestMicrosoftEngine_moveToFolder(t *testing.T) {
	var a string
	h := newMicrosoft(t, func(f *graphtest.Fake) {
		f.AddFolder("", "Projects", "")
		a = f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "a"})
	}, "Projects")
	h.synced("INBOX", "Projects")
	targets := h.eng.MoveTargets("INBOX")
	if !slices.Contains(targets, "Projects") || slices.Contains(targets, "INBOX") || slices.Contains(targets, "Drafts") {
		t.Fatalf("MoveTargets = %v", targets)
	}
	if err := h.eng.Move(context.Background(), "INBOX", a, "Projects"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.ids("Projects"), a) || slices.Contains(h.ids("INBOX"), a) {
		t.Fatalf("INBOX %v, Projects %v", h.ids("INBOX"), h.ids("Projects"))
	}
}

func TestMicrosoftEngine_saveDraftReplaces(t *testing.T) {
	h := newMicrosoft(t, nil)
	h.synced()
	ctx := context.Background()
	id1, err := h.eng.SaveDraft(ctx, mRaw("draft 1"), "")
	if err != nil || id1 == "" {
		t.Fatalf("first save = %q %v", id1, err)
	}
	id2, err := h.eng.SaveDraft(ctx, mRaw("draft 2"), id1)
	if err != nil || id2 == "" || id2 == id1 {
		t.Fatalf("second save = %q %v", id2, err)
	}
	if m, _ := h.f.Message(id1); m.FolderID == h.f.FolderID("drafts") {
		t.Fatal("the replaced draft is still in Drafts")
	}
	if m, _ := h.f.Message(id2); m.FolderID != h.f.FolderID("drafts") || m.Subject != "draft 2" {
		t.Fatalf("new draft = %+v", m)
	}
	// Saving again over a draft already deleted for good (Purges: 403).
	id3, err := h.eng.SaveDraft(ctx, mRaw("draft 3"), id1)
	if err != nil || id3 == "" {
		t.Fatalf("save over a gone draft = %q %v", id3, err)
	}
	id4, err := h.eng.SaveDraft(ctx, mRaw("draft 4"), "never-existed")
	if err != nil || id4 == "" {
		t.Fatalf("save over an unknown draft = %q %v", id4, err)
	}
}

func TestMicrosoftEngine_draftCopies(t *testing.T) {
	h := newMicrosoft(t, nil)
	h.synced()
	ctx := context.Background()
	a, _ := h.eng.SaveDraft(ctx, mRaw("a"), "")
	b, _ := h.eng.SaveDraft(ctx, mRaw("b"), "")
	if err := h.eng.TrashDraftCopy(ctx, a); err != nil {
		t.Fatal(err)
	}
	if m, _ := h.f.Message(a); m.FolderID != h.f.FolderID("deleteditems") {
		t.Fatalf("trashed draft in %s", m.FolderID)
	}
	if err := h.eng.DeleteDraftCopy(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.DeleteDraftCopy(ctx, b); err != nil {
		t.Fatalf("deleting it again: %v", err)
	}
}

func TestMicrosoftEngine_permanentDeleteOnlyInTrash(t *testing.T) {
	var a, b string
	h := newMicrosoft(t, func(f *graphtest.Fake) {
		a = f.AddMessage(f.FolderID("deleteditems"), graphtest.Message{Subject: "a"})
		b = f.AddMessage(f.FolderID("deleteditems"), graphtest.Message{Subject: "b"})
	})
	h.synced()
	ctx := context.Background()
	h.f.Move(a, h.f.FolderID("inbox")) // restored on another device
	if err := h.eng.PermanentDelete(ctx, "Trash", a); !errors.Is(err, msync.ErrNotInTrash) {
		t.Fatalf("err = %v", err)
	}
	if m, ok := h.f.Message(a); !ok || m.FolderID != h.f.FolderID("inbox") {
		t.Fatal("a restored message was deleted")
	}
	if err := h.eng.PermanentDelete(ctx, "Trash", b); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(h.ids("Trash"), b) {
		t.Fatal("row kept after a permanent delete")
	}
	// Deleted for good elsewhere meanwhile: it sits in Purges; done, not an error.
	if err := h.eng.PermanentDelete(ctx, "Trash", b); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestMicrosoftEngine_emptyTrash(t *testing.T) {
	h := newMicrosoft(t, func(f *graphtest.Fake) {
		for range 3 {
			f.AddMessage(f.FolderID("deleteditems"), graphtest.Message{Subject: "x"})
		}
		f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "keep"})
	})
	h.synced()
	n, err := h.eng.EmptyTrash(context.Background())
	if err != nil || n != 3 {
		t.Fatalf("EmptyTrash = %d %v", n, err)
	}
	if got := h.ids("Trash"); len(got) != 0 {
		t.Fatalf("Trash = %v", got)
	}
	if got := h.ids("INBOX"); len(got) != 1 {
		t.Fatalf("INBOX = %v", got)
	}
}

func TestMicrosoftEngine_setFlags(t *testing.T) {
	var a string
	h := newMicrosoft(t, func(f *graphtest.Fake) {
		a = f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "a"})
	})
	h.synced()
	ctx := context.Background()
	if err := h.eng.SetFlags(ctx, "INBOX", a, []string{`\Seen`, `\Flagged`}, true); err != nil {
		t.Fatal(err)
	}
	if m, _ := h.f.Message(a); !m.IsRead || !m.Flagged {
		t.Fatalf("server = %+v", m)
	}
	if e := h.env("INBOX", a); e.IsUnread() || !e.IsFlagged() {
		t.Fatalf("row flags = %v", e.Flags)
	}
	if err := h.eng.SetFlags(ctx, "INBOX", a, []string{`\Seen`}, false); err != nil {
		t.Fatal(err)
	}
	if e := h.env("INBOX", a); !e.IsUnread() || !e.IsFlagged() {
		t.Fatalf("row flags after unread = %v", e.Flags)
	}
}

func TestMicrosoftEngine_categories(t *testing.T) {
	var a string
	h := newMicrosoft(t, func(f *graphtest.Fake) {
		f.SetMasterCategories("Work", "Travel")
		a = f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "a", Categories: []string{"Travel"}})
	})
	h.synced()
	var names []string
	for _, l := range h.eng.AccountLabels() {
		names = append(names, l.Name)
	}
	if !slices.Equal(names, []string{"Work", "Travel"}) {
		t.Fatalf("AccountLabels = %v", names)
	}
	if got := h.eng.CurrentLabels("", a); !slices.Equal(got, []string{"Travel"}) {
		t.Fatalf("CurrentLabels = %v", got)
	}
	if err := h.eng.ModifyLabels(context.Background(), "INBOX", a, []string{"Work"}, []string{"Travel"}); err != nil {
		t.Fatal(err)
	}
	var patch string
	for _, r := range h.f.Requests() {
		if r.Method == "PATCH" && r.Path == "/v1.0/me/messages/"+a {
			patch = r.Body
		}
	}
	if patch != `{"categories":["Work"]}` {
		t.Fatalf("PATCH body = %s", patch)
	}
	if got := h.eng.CurrentLabels("", a); !slices.Equal(got, []string{"Work"}) {
		t.Fatalf("CurrentLabels after = %v", got)
	}
}

func TestMicrosoftEngine_sendSavesNoCopy(t *testing.T) {
	h := newMicrosoft(t, nil)
	h.synced()
	if !h.eng.ServerSavesSent() {
		t.Fatal("Graph files sent mail in Sent Items itself")
	}
	d := smtpadapter.Draft{From: "me@outlook.com", To: []string{"bob@example.com"}, Subject: "hello", Body: "hi"}
	raw, err := h.eng.Send(context.Background(), d)
	if err != nil || !strings.Contains(string(raw), "Subject: hello") {
		t.Fatalf("Send: %v", err)
	}
	if n := h.f.Count("/v1.0/me/sendMail"); n != 1 {
		t.Fatalf("sendMail requests = %d", n)
	}
	if folder, err := h.eng.SaveSentCopy(context.Background(), raw); err != nil || folder != "Sent" {
		t.Fatalf("SaveSentCopy = %q %v", folder, err)
	}
	if n := h.f.Count("/v1.0/me/messages"); n != 0 {
		t.Fatalf("a Sent copy was uploaded (%d)", n)
	}
}

func TestMicrosoftEngine_searchStoresNothing(t *testing.T) {
	var a string
	h := newMicrosoft(t, func(f *graphtest.Fake) {
		a = f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "invoice 42"})
	})
	h.synced()
	envs, err := h.eng.Search(context.Background(), "INBOX", "invoice")
	if err != nil || len(envs) != 1 || envs[0].ID != a || envs[0].Subject != "invoice 42" {
		t.Fatalf("Search = %+v %v", envs, err)
	}
}

func TestMicrosoftEngine_fetchBody(t *testing.T) {
	var a string
	h := newMicrosoft(t, func(f *graphtest.Fake) {
		a = f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "a", Raw: mRaw("a")})
	})
	h.synced()
	m, err := h.eng.FetchBody(context.Background(), "INBOX", a)
	if err != nil || !strings.Contains(m.PlainText(), "body of a") || m.Envelope.ID != a {
		t.Fatalf("FetchBody = %+v %v", m, err)
	}
}

func TestMicrosoftEngine_signInAgain(t *testing.T) {
	h := newMicrosoft(t, func(f *graphtest.Fake) {
		f.Queue("/v1.0/me", 401, `{"error":{"code":"InvalidAuthenticationToken"}}`, nil)
	})
	h.eventually("failure", h.failed)
	for _, ev := range h.snapshot() {
		if st, ok := ev.(msync.AccountStatus); ok && st.State == msync.StateFailed {
			if !errors.Is(st.Err, graph.ErrSignInAgain) || st.RetryIn != 0 ||
				!strings.Contains(st.Err.Error(), "m9s login ms-test") {
				t.Fatalf("status = %+v", st)
			}
		}
	}
}
