package view

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tcell "github.com/derailed/tcell/v2"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/mail/gmail"
	"github.com/zhecho/m9s/internal/mail/gmail/gmailtest"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	"github.com/zhecho/m9s/internal/store"
	msync "github.com/zhecho/m9s/internal/sync"
	"github.com/zhecho/m9s/internal/ui"
)

// gmailFlow runs the real m9s UI on a simulated screen against the real
// Gmail engine talking to an in-memory fake Gmail. The account has no smtp
// block: everything it needs is under gmail.
type gmailFlow struct {
	*flowHarness
	fake *gmailtest.Fake
	st   *store.Store
	eng  *msync.GmailEngine
}

const gmailFlowUser = "me@gmail.com"

func newGmailFlow(t *testing.T, seed func(f *gmailtest.Fake)) *gmailFlow {
	t.Helper()
	f := gmailtest.New()
	t.Cleanup(f.Close)
	f.Email = gmailFlowUser
	if seed != nil {
		seed(f)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	acctCfg := &config.Account{Name: "flow", Provider: "gmail", Gmail: config.GmailConfig{User: gmailFlowUser},
		Folders: config.FolderConfig{
			BadgePoll:     config.Duration(100 * time.Millisecond),
			BodyCachePoll: config.Duration(100 * time.Millisecond),
		}}
	capture := filepath.Join(t.TempDir(), "capture")
	ds := draft.NewStore(t.TempDir())
	events := make(chan msync.Event, 64)
	eng := msync.NewGmailEngine(acctCfg, events, st, ds, gmail.New(f.Server.Client(), gmail.WithBaseURL(f.URL())))
	ctx, cancel := context.WithCancel(context.Background())

	skin, _ := ui.LoadSkin("")
	km, _ := ui.LoadKeyMap("")
	uiApp := ui.NewApp(skin, km)
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	screen.SetSize(140, 40)
	uiApp.SetScreen(screen)

	cfg := &config.Config{}
	cfg.UI.Editor = "sh " + fakeEditor(t, capture) + " " + capture
	ra := NewRootApp(uiApp, cfg, mail.NewManager([]*mail.Account{{Name: "flow", Config: acctCfg}}),
		map[string]msync.Account{"flow": eng}, ds, events)
	ra.SetStore(st)

	done := make(chan struct{})
	go func() { _ = uiApp.Run(); close(done) }()
	eng.Start(ctx)
	t.Cleanup(func() {
		uiApp.Stop()
		<-done
		cancel()
		<-eng.Done()
		st.Close()
	})
	h := &flowHarness{t: t, ra: ra, screen: screen, drafts: ds, capture: capture}
	return &gmailFlow{flowHarness: h, fake: f, st: st, eng: eng}
}

// serverDraftBodies lists the raw messages of every draft on the fake.
func (g *gmailFlow) serverDraftBodies() []string {
	var out []string
	for _, mid := range g.fake.Drafts() {
		m, _ := g.fake.Message(mid)
		out = append(out, string(m.Raw))
	}
	return out
}

// TestGmailFlow_editDraftsRowEditsTheLocalDraft: e on a Gmail Drafts row
// that m9s uploaded opens the local draft it came from; after the edit
// exactly one draft exists, locally and on Gmail.
func TestGmailFlow_editDraftsRowEditsTheLocalDraft(t *testing.T) {
	h := newGmailFlow(t, nil)
	h.eventually("connected with INBOX open", func() bool {
		return h.ra.currentFolder != nil && h.ra.currentFolder.Name == "INBOX"
	})

	h.onUI(func() { h.ra.uiApp.SetFocus(h.ra.maillist) })
	h.key('c')
	h.eventually("send prompt after composing", func() bool { return h.ra.uiApp.Pages.HasPage("send-modal") })
	h.onUI(func() { h.ra.testModalDone(1, "Server Draft") })
	h.eventually("draft uploaded to Gmail", func() bool {
		d := h.localDrafts()
		return len(d) == 1 && d[0].SyncedID != "" && !d[0].Dirty
	})

	h.eventually("the poll stored the Drafts row", func() bool {
		envs, _ := h.st.Envelopes("flow", "Drafts")
		return len(envs) == 1
	})
	h.onUI(func() { h.ra.openFolderByName("Drafts") })
	h.eventually("Gmail Drafts row shown", func() bool {
		return h.ra.currentMsg != nil && strings.Contains(h.ra.preview.GetText(true), "version 0")
	})
	h.onUI(func() { h.ra.uiApp.SetFocus(h.ra.maillist) })
	h.key('e')
	h.eventually("send prompt after editing the Drafts row", func() bool { return h.ra.uiApp.Pages.HasPage("send-modal") })
	if in := h.editorInput(1); !strings.Contains(in, "version 0") {
		t.Fatalf("editor was not handed the draft; got:\n%s", in)
	}
	h.onUI(func() { h.ra.testModalDone(1, "Server Draft") })

	h.eventually("one local draft, uploaded again", func() bool {
		d := h.localDrafts()
		return len(d) == 1 && d[0].SyncedID != "" && !d[0].Dirty
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b := h.serverDraftBodies(); len(b) == 1 && strings.Contains(b[0], "version 1") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond) // a stray upload or delete has had time
	if n := len(h.localDrafts()); n != 1 {
		t.Fatalf("%d local drafts after editing a Drafts row, want 1", n)
	}
	if b := h.serverDraftBodies(); len(b) != 1 || !strings.Contains(b[0], "version 1") {
		t.Fatalf("Gmail drafts after the edit = %d, want exactly the edited one: %q", len(b), b)
	}
}

// TestGmailFlow_listFetchWritesNoStoreRows: the Gmail engine keeps the
// store itself, so a Trash (or Drafts) list the view fetched from it is not
// written back — a poll's change made meanwhile survives.
func TestGmailFlow_listFetchWritesNoStoreRows(t *testing.T) {
	var id string
	h := newGmailFlow(t, func(f *gmailtest.Fake) {
		id = f.AddMessage(gmailtest.Message{LabelIDs: []string{"TRASH", "UNREAD"},
			Raw: []byte("From: bob@example.com\r\nSubject: binned\r\nMessage-ID: <b@x>\r\n\r\nbody\r\n")})
	})
	h.eventually("Trash row stored", func() bool {
		_, ok := h.st.Envelope("flow", "Trash", id)
		return ok
	})
	trash := &mail.Folder{Name: "Trash"}
	stale, err := h.eng.FetchHeaders(context.Background(), "Trash", 1, store.ListSize)
	if err != nil || len(stale) != 1 {
		t.Fatalf("FetchHeaders(Trash) = %d rows, %v", len(stale), err)
	}
	// A poll marks it read while the view's fetch is in flight.
	if err := h.st.SetFlags("flow", "Trash", map[string][]string{id: {`\Seen`}}); err != nil {
		t.Fatal(err)
	}
	h.ra.storeFetched(h.ra.manager.Active(), trash, stale)
	env, ok := h.st.Envelope("flow", "Trash", id)
	if !ok || env.IsUnread() {
		t.Fatalf("the poll's flag change was reverted by the view: %+v", env)
	}
}

// TestGmailFlow_selfAddressIsTheGmailUser: a Gmail account with no smtp
// block composes and replies from gmail.user, leaves itself out of a
// reply-all's Cc, and its Message-IDs are on gmail.com.
func TestGmailFlow_selfAddressIsTheGmailUser(t *testing.T) {
	h := newGmailFlow(t, nil)
	h.eventually("connected with INBOX open", func() bool {
		return h.ra.currentFolder != nil && h.ra.currentFolder.Name == "INBOX"
	})

	h.onUI(func() { h.ra.uiApp.SetFocus(h.ra.maillist) })
	h.key('c')
	h.eventually("send prompt after composing", func() bool { return h.ra.uiApp.Pages.HasPage("send-modal") })
	compose, err := smtpadapter.ParseDraft([]byte(h.editorInput(0)))
	if err != nil {
		t.Fatal(err)
	}
	if compose.From != gmailFlowUser {
		t.Fatalf("compose From = %q, want %q", compose.From, gmailFlowUser)
	}
	raw, err := smtpadapter.BuildMessage(smtpadapter.Draft{From: compose.From, To: []string{"x@example.com"}, Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "@gmail.com>") {
		t.Fatalf("Message-ID is not on gmail.com:\n%s", raw)
	}
	h.onUI(func() { h.ra.uiApp.Pages.RemovePage("send-modal") })

	msg := &mail.Message{Envelope: &mail.Envelope{ID: "m1", MessageID: "orig@example.com", Subject: "Hi",
		From: []mail.Address{{Email: "bob@example.com"}},
		To:   []mail.Address{{Email: gmailFlowUser}, {Email: "carol@example.com"}}}, TextBody: "hello"}
	h.onUI(func() { h.ra.startReply(msg, true) })
	h.eventually("send prompt after replying", func() bool { return h.ra.uiApp.Pages.HasPage("send-modal") })
	reply, err := smtpadapter.ParseDraft([]byte(h.editorInput(1)))
	if err != nil {
		t.Fatal(err)
	}
	if reply.From != gmailFlowUser {
		t.Fatalf("reply From = %q, want %q", reply.From, gmailFlowUser)
	}
	if len(reply.CC) != 1 || reply.CC[0] != "carol@example.com" {
		t.Fatalf("reply-all Cc = %v, want only carol (not self)", reply.CC)
	}
}

// TestGmailFlow_sendingASyncedDraftDeletesItsGmailDraft: once a draft that
// was uploaded to Gmail is sent, its Gmail draft goes too.
func TestGmailFlow_sendingASyncedDraftDeletesItsGmailDraft(t *testing.T) {
	h := newGmailFlow(t, nil)
	h.eventually("connected with INBOX open", func() bool {
		return h.ra.currentFolder != nil && h.ra.currentFolder.Name == "INBOX"
	})
	h.onUI(func() { h.ra.uiApp.SetFocus(h.ra.maillist) })
	h.key('c')
	h.eventually("send prompt", func() bool { return h.ra.uiApp.Pages.HasPage("send-modal") })
	h.onUI(func() { h.ra.testModalDone(1, "Server Draft") })
	h.eventually("draft uploaded to Gmail", func() bool {
		d := h.localDrafts()
		return len(d) == 1 && d[0].SyncedID != "" && !d[0].Dirty
	})
	if n := len(h.fake.Drafts()); n != 1 {
		t.Fatalf("%d Gmail drafts before sending, want 1", n)
	}
	local := h.localDrafts()[0]
	d, err := h.drafts.Load("flow", local.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.onUI(func() { h.ra.sendFn(d, local.ID, "flow") })
	h.eventually("sent", func() bool { return strings.Contains(h.ra.uiApp.StatusBar.FlashText(), "Message sent") })
	if n := len(h.fake.MessageIDs("SENT")); n != 1 {
		t.Fatalf("%d messages in SENT, want 1", n)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(h.fake.Drafts()) != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := len(h.fake.Drafts()); n != 0 {
		t.Fatalf("%d Gmail drafts left after sending the draft, want 0", n)
	}
	if n := len(h.localDrafts()); n != 0 {
		t.Fatalf("%d local drafts left after sending", n)
	}
}

// TestGmailFlow_allMailShowsArchivedAndMovesBackToInbox: an archived
// conversation is in All Mail, and moving it to INBOX from there adds the
// Inbox label without touching any other.
func TestGmailFlow_allMailShowsArchivedAndMovesBackToInbox(t *testing.T) {
	var archived string
	h := newGmailFlow(t, func(f *gmailtest.Fake) {
		f.AddMessage(gmailtest.Message{ThreadID: "A", LabelIDs: []string{"INBOX"}, InternalDate: 2_000,
			Raw: []byte("From: ann@example.com\r\nSubject: in the inbox\r\nMessage-ID: <a@x>\r\n\r\nhi\r\n")})
		archived = f.AddMessage(gmailtest.Message{ThreadID: "B", LabelIDs: []string{"STARRED"}, InternalDate: 1_000,
			Raw: []byte("From: bob@example.com\r\nSubject: archived one\r\nMessage-ID: <b@x>\r\n\r\nhi\r\n")})
	})
	// eventually runs its condition on the UI goroutine.
	h.eventually("All Mail opened", func() bool { return h.ra.openFolderByName(msync.GmailAllMail) })
	var row *mail.Envelope
	h.eventually("archived row in All Mail", func() bool {
		for _, e := range h.ra.maillist.Envelopes() {
			if e.ThreadID == "B" {
				row = e
				return true
			}
		}
		return false
	})
	h.onUI(func() {
		eng := h.ra.engines["flow"]
		h.ra.moveTo(h.ra.manager.Active(), eng, eng.(msync.Mover), h.ra.currentFolder, row, "INBOX")
	})
	h.eventually("INBOX label added", func() bool {
		m, _ := h.fake.Message(archived)
		return slices.Contains(m.LabelIDs, "INBOX") && slices.Contains(m.LabelIDs, "STARRED")
	})
}
