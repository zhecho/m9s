package view

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	gosync "sync"
	"testing"
	"time"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	imap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail"
	imapadapter "github.com/zhecho/m9s/internal/mail/imap"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	msync "github.com/zhecho/m9s/internal/sync"
	"github.com/zhecho/m9s/internal/ui"
)

// flowHarness runs the real m9s UI on a simulated screen against an
// in-memory IMAP server. No real mail account is involved.
type flowHarness struct {
	t       *testing.T
	ra      *RootApp
	screen  tcell.SimulationScreen
	drafts  *draft.Store
	server  imapadapter.Client // independent client to inspect the mailbox
	capture string             // the fake editor saves what it was given to capture.N
	sender  *recordingSender
}

// recordingSender stands in for SMTP and records what was sent.
type recordingSender struct {
	mu   gosync.Mutex
	sent []smtpadapter.Draft
}

func (s *recordingSender) Send(_ string, d smtpadapter.Draft) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, d)
	return smtpadapter.BuildMessage(d)
}

func (s *recordingSender) Sent() []smtpadapter.Draft {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]smtpadapter.Draft(nil), s.sent...)
}

func startFlowServer(t *testing.T, extraFolders ...string) int {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("flow", "secret")
	for _, name := range append([]string{"INBOX", "Drafts", "Sent Items", "Deleted Items"}, extraFolders...) {
		if err := user.Create(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	mem.AddUser(user)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapUIDPlus: {}, imap.CapMove: {}, imap.CapIdle: {}},
		InsecureAuth: true,
		Logger:       log.New(io.Discard, "", 0),
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// fakeEditor writes a script that records the draft it is handed and then
// replaces it with "Hello from the flow test (version N)", N = run number.
func fakeEditor(t *testing.T, capture string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "editor.sh")
	body := `#!/bin/sh
cap="$1"; f="$2"
n=$(ls "$cap".* 2>/dev/null | wc -l | tr -d ' ')
cp "$f" "$cap.$n"
if [ -n "$FLOW_RAW_FILE" ]; then
  cp "$FLOW_RAW_FILE" "$f" # exact bytes, as a real editor saved them
  exit 0
fi
cat > "$f" <<EOF
From: flow@example.com
To: friend@example.com
Subject: Flow draft

Hello from the flow test (version $n)
EOF
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return script
}

// newFlowHarness runs the app against an in-memory IMAP server with INBOX,
// Drafts, Sent Items, Deleted Items and extraFolders.
func newFlowHarness(t *testing.T, extraFolders ...string) *flowHarness {
	t.Helper()
	port := startFlowServer(t, extraFolders...)
	imapCfg := config.IMAPConfig{Host: "127.0.0.1", Port: port, User: "flow", Password: "secret"}
	acctCfg := &config.Account{Name: "flow", IMAP: imapCfg,
		SMTP: config.SMTPConfig{Host: "127.0.0.1", Port: 1, User: "flow", Password: "secret"}}
	capture := filepath.Join(t.TempDir(), "capture")

	ds := draft.NewStore(t.TempDir())
	events := make(chan msync.Event, 64)
	eng := msync.NewEngine(acctCfg, events)
	eng.SetPassword("secret") // as startup hands it over
	eng.SetDraftStore(ds)
	sender := &recordingSender{}
	eng.SetSender(sender)
	eng.SetSMTPPassword("secret") // as startup hands it over
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

	cfg := noLogPane()
	cfg.UI.Editor = "sh " + fakeEditor(t, capture) + " " + capture
	ra := NewRootApp(uiApp, cfg, mail.NewManager([]*mail.Account{{Name: "flow", Config: acctCfg}}),
		map[string]msync.Account{"flow": eng}, ds, events)

	done := make(chan struct{})
	go func() { _ = uiApp.Run(); close(done) }()
	eng.Start(ctx)
	t.Cleanup(func() {
		uiApp.Stop()
		<-done
		cancel()
		<-eng.Done()
	})

	inspector := imapadapter.New(&imapCfg)
	if err := inspector.Connect(context.Background(), "secret"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inspector.Close() })

	return &flowHarness{t: t, ra: ra, screen: screen, drafts: ds, server: inspector, capture: capture, sender: sender}
}

// onUI runs f on the UI goroutine and waits for it.
func (h *flowHarness) onUI(f func()) { h.ra.uiApp.QueueUpdateDraw(f) }

// eventually polls cond on the UI goroutine until it holds or 5s pass.
func (h *flowHarness) eventually(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var ok bool
		h.onUI(func() { ok = cond() })
		if ok {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (h *flowHarness) key(r rune) { h.screen.InjectKey(tcell.KeyRune, r, tcell.ModNone) }

// serverSubjects lists the subjects and bodies in a server folder.
func (h *flowHarness) serverFolder(folder string) []*mail.Message {
	h.t.Helper()
	envs, err := h.server.FetchHeaders(context.Background(), folder, 1, 50)
	if err != nil {
		h.t.Fatalf("FetchHeaders(%s): %v", folder, err)
	}
	var msgs []*mail.Message
	for _, env := range envs {
		msg, err := h.server.FetchBody(context.Background(), folder, env.UID)
		if err != nil {
			h.t.Fatalf("FetchBody(%s, %d): %v", folder, env.UID, err)
		}
		msgs = append(msgs, msg)
	}
	return msgs
}

func (h *flowHarness) localDrafts() []draft.Meta {
	metas, _ := h.drafts.List("flow")
	return metas
}

// editorInput returns what the fake editor was handed on run n (0-based).
func (h *flowHarness) editorInput(n int) string {
	h.t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("%s.%d", h.capture, n))
	if err != nil {
		h.t.Fatalf("editor run %d did not happen: %v", n, err)
	}
	return string(data)
}

// putCursorOnLocalDraft focuses the folder pane with the cursor on the
// (single) local draft, as moving onto it with j/k would.
func (h *flowHarness) putCursorOnLocalDraft() {
	h.t.Helper()
	h.onUI(func() {
		var node *tview.TreeNode
		h.ra.folders.GetRoot().Walk(func(n, _ *tview.TreeNode) bool {
			if _, ok := n.GetReference().(*draft.Meta); ok && node == nil {
				node = n
			}
			return true
		})
		if node == nil {
			return
		}
		h.ra.uiApp.SetFocus(h.ra.folders)
		h.ra.folders.moveCursorTo(node) // fires the same hover as j/k
	})
}

func onlyBody(msgs []*mail.Message) string {
	if len(msgs) != 1 {
		return fmt.Sprintf("<%d messages>", len(msgs))
	}
	return strings.TrimSpace(msgs[0].PlainText())
}

// TestDraftFlow walks a draft through its whole life against a fake server:
// compose and keep it, see it in the preview, edit it with e, edit it again
// from the server's Drafts folder, then delete it and find it in Deleted Items.
func TestDraftFlow(t *testing.T) {
	h := newFlowHarness(t)
	h.eventually("connected with INBOX open", func() bool {
		return h.ra.currentFolder != nil && h.ra.currentFolder.Name == "INBOX"
	})

	// 1. Compose a new draft and keep it.
	h.onUI(func() { h.ra.uiApp.SetFocus(h.ra.maillist) })
	h.key('c')
	h.eventually("send / keep prompt after composing", func() bool { return h.ra.uiApp.Pages.HasPage("send-modal") })
	h.onUI(func() { h.ra.testModalDone(1, "Server Draft") })

	h.eventually("draft kept locally and uploaded to the server's Drafts", func() bool {
		d := h.localDrafts()
		return len(d) == 1 && d[0].SyncedID != "" && !d[0].Dirty
	})
	if got := onlyBody(h.serverFolder("Drafts")); got != "Hello from the flow test (version 0)" {
		t.Fatalf("server Drafts = %q", got)
	}

	// 2. Moving onto the local draft shows its contents in the preview.
	h.putCursorOnLocalDraft()
	h.eventually("local draft shown in the preview", func() bool {
		return strings.Contains(h.ra.preview.GetText(true), "Hello from the flow test (version 0)")
	})

	// 3. e opens the editor with the draft's contents; keep the edit.
	h.key('e')
	h.eventually("send / keep prompt after editing", func() bool { return h.ra.uiApp.Pages.HasPage("send-modal") })
	if in := h.editorInput(1); !strings.Contains(in, "Hello from the flow test (version 0)") || !strings.Contains(in, "Subject: Flow draft") {
		t.Fatalf("editor was not handed the draft; got:\n%s", in)
	}
	h.onUI(func() { h.ra.testModalDone(1, "Server Draft") })
	h.eventually("edited draft replaces the old one, locally and on the server", func() bool {
		d := h.localDrafts()
		return len(d) == 1 && d[0].SyncedID != "" && !d[0].Dirty
	})
	if got := onlyBody(h.serverFolder("Drafts")); got != "Hello from the flow test (version 1)" {
		t.Fatalf("server Drafts after edit = %q, want only version 1", got)
	}
	if n := len(h.serverFolder("Deleted Items")); n != 0 {
		t.Fatalf("Deleted Items has %d messages after an edit; replaced versions are not deletions", n)
	}

	// 4. In the server's Drafts folder the draft shows on the right, and e edits it.
	h.onUI(func() { h.ra.openFolderByName("Drafts") })
	h.eventually("server draft shown in the preview", func() bool {
		return h.ra.currentMsg != nil && strings.Contains(h.ra.preview.GetText(true), "Hello from the flow test (version 1)")
	})
	h.onUI(func() { h.ra.uiApp.SetFocus(h.ra.maillist) })
	h.key('e')
	h.eventually("send / keep prompt after editing the server draft", func() bool { return h.ra.uiApp.Pages.HasPage("send-modal") })
	if in := h.editorInput(2); !strings.Contains(in, "Hello from the flow test (version 1)") {
		t.Fatalf("editor was not handed the server draft; got:\n%s", in)
	}
	h.onUI(func() { h.ra.testModalDone(1, "Server Draft") })
	h.eventually("still one draft, now version 2, locally and on the server", func() bool {
		d := h.localDrafts()
		return len(d) == 1 && d[0].SyncedID != "" && !d[0].Dirty
	})
	if got := onlyBody(h.serverFolder("Drafts")); got != "Hello from the flow test (version 2)" {
		t.Fatalf("server Drafts after editing from the Drafts folder = %q", got)
	}

	// 5. Delete it: it ends up in Deleted Items, not lost.
	h.putCursorOnLocalDraft()
	h.key('d')
	h.eventually("delete confirmation for the draft", func() bool { return h.ra.uiApp.Pages.HasPage("delete-modal") })
	if !strings.Contains(h.ra.testDeleteModalText, "Flow draft") {
		t.Fatalf("delete prompt = %q, want it about the draft", h.ra.testDeleteModalText)
	}
	h.onUI(func() { h.ra.testDeleteModalDone(0, "Delete") })

	h.eventually("local draft gone", func() bool { return len(h.localDrafts()) == 0 })
	deadline := time.Now().Add(5 * time.Second)
	for len(h.serverFolder("Drafts")) != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := len(h.serverFolder("Drafts")); n != 0 {
		t.Fatalf("server Drafts still has %d messages after delete", n)
	}
	if got := onlyBody(h.serverFolder("Deleted Items")); got != "Hello from the flow test (version 2)" {
		t.Fatalf("Deleted Items = %q, want the deleted draft", got)
	}
}

// TestDraftFlow_localDraftStaysOnThisMachine: Local Draft is never uploaded,
// until an edit is kept as a Server Draft.
func TestDraftFlow_localDraftStaysOnThisMachine(t *testing.T) {
	h := newFlowHarness(t)
	h.eventually("connected with INBOX open", func() bool {
		return h.ra.currentFolder != nil && h.ra.currentFolder.Name == "INBOX"
	})

	h.onUI(func() { h.ra.uiApp.SetFocus(h.ra.maillist) })
	h.key('c')
	h.eventually("send prompt", func() bool { return h.ra.uiApp.Pages.HasPage("send-modal") })
	h.onUI(func() { h.ra.testModalDone(2, "Local Draft") })

	h.eventually("kept locally only", func() bool {
		d := h.localDrafts()
		return len(d) == 1 && d[0].LocalOnly
	})
	time.Sleep(300 * time.Millisecond) // an upload, if one were started, has had time
	if n := len(h.serverFolder("Drafts")); n != 0 {
		t.Fatalf("a Local Draft reached the server: %d messages in Drafts", n)
	}

	h.putCursorOnLocalDraft()
	h.key('e')
	h.eventually("send prompt after editing", func() bool { return h.ra.uiApp.Pages.HasPage("send-modal") })
	h.onUI(func() { h.ra.testModalDone(1, "Server Draft") })
	h.eventually("uploaded once kept as a Server Draft", func() bool {
		d := h.localDrafts()
		return len(d) == 1 && !d[0].LocalOnly && d[0].SyncedID != ""
	})
	if got := onlyBody(h.serverFolder("Drafts")); got != "Hello from the flow test (version 1)" {
		t.Fatalf("server Drafts = %q", got)
	}
}

// TestDraftFlow_keepingADraftSyncs: choosing Server Draft or Local Draft runs
// a sync by itself, as if s had been pressed.
func TestDraftFlow_keepingADraftSyncs(t *testing.T) {
	h := newFlowHarness(t)
	h.eventually("connected with INBOX open", func() bool {
		return h.ra.currentFolder != nil && h.ra.currentFolder.Name == "INBOX"
	})
	h.eventually("Server Drafts count known and empty", func() bool {
		_, total, ok := h.ra.folders.Counts("flow", "Drafts")
		return ok && total == 0
	})

	h.onUI(func() { h.ra.uiApp.SetFocus(h.ra.maillist) })
	h.key('c')
	h.eventually("send prompt", func() bool { return h.ra.uiApp.Pages.HasPage("send-modal") })
	h.onUI(func() { h.ra.testModalDone(1, "Server Draft") })

	h.eventually("Server Drafts count updated without pressing s", func() bool {
		_, total, ok := h.ra.folders.Counts("flow", "Drafts")
		return ok && total == 1
	})
	h.eventually("bottom row says it synced", func() bool {
		return strings.Contains(h.ra.uiApp.StatusBar.FlashText(), "synced")
	})

	h.key('c')
	h.eventually("send prompt again", func() bool { return h.ra.uiApp.Pages.HasPage("send-modal") })
	h.onUI(func() {
		h.ra.uiApp.StatusBar.Info("") // forget the previous message
		h.ra.testModalDone(2, "Local Draft")
	})
	h.eventually("a Local Draft syncs too", func() bool {
		return strings.Contains(h.ra.uiApp.StatusBar.FlashText(), "synced")
	})
	var total uint32
	h.onUI(func() { _, total, _ = h.ra.folders.Counts("flow", "Drafts") })
	if total != 1 {
		t.Errorf("Server Drafts count = %d after a Local Draft, want still 1", total)
	}
}

// TestDraftFlow_openServerDraftsListUpdates: with Server Drafts open, keeping
// a Server Draft shows it in the list without reopening the folder.
func TestDraftFlow_openServerDraftsListUpdates(t *testing.T) {
	h := newFlowHarness(t)
	h.eventually("connected with INBOX open", func() bool {
		return h.ra.currentFolder != nil && h.ra.currentFolder.Name == "INBOX"
	})
	h.onUI(func() {
		h.ra.openFolderByName("Drafts")
		h.ra.uiApp.SetFocus(h.ra.maillist)
	})
	h.key('c')
	h.eventually("send prompt", func() bool { return h.ra.uiApp.Pages.HasPage("send-modal") })
	h.onUI(func() { h.ra.testModalDone(1, "Server Draft") })

	h.eventually("new draft listed in the open Server Drafts", func() bool {
		return h.ra.maillist.VisibleCount() == 1
	})
}

const bulgarianSubject = "Здравей Яна, как си днес? Това е по-дълъг български предмет за тест"

// TestDraftFlow_bulgarianSubjectEditedToServerDraft reproduces editing a
// local draft's subject to Bulgarian in vim and keeping it as a Server Draft.
// The editor saves CRLF line endings with stray CRs and wraps the long
// subject onto a continuation line, as vim (filetype=mail) does.
func TestDraftFlow_bulgarianSubjectEditedToServerDraft(t *testing.T) {
	h := newFlowHarness(t)
	h.eventually("connected with INBOX open", func() bool {
		return h.ra.currentFolder != nil && h.ra.currentFolder.Name == "INBOX"
	})
	h.onUI(func() { h.ra.uiApp.SetFocus(h.ra.maillist) })
	h.key('c')
	h.eventually("send prompt", func() bool { return h.ra.uiApp.Pages.HasPage("send-modal") })
	h.onUI(func() { h.ra.testModalDone(2, "Local Draft") })
	h.eventually("a local draft", func() bool { return len(h.localDrafts()) == 1 })

	raw := filepath.Join(t.TempDir(), "edited.eml")
	edited := "From: flow@example.com\r\r\n" +
		"To: Яна <friend@example.com>\r\r\n" +
		"Subject: Здравей Яна, как си днес? Това е по-дълъг\r\r\n" +
		" български предмет за тест\r\r\n" +
		"\r\r\n" +
		"Здравей,\r\r\nкак си?\r\r\n"
	if err := os.WriteFile(raw, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLOW_RAW_FILE", raw)

	h.putCursorOnLocalDraft()
	h.key('e')
	h.eventually("send prompt after the edit", func() bool { return h.ra.uiApp.Pages.HasPage("send-modal") })
	h.onUI(func() { h.ra.testModalDone(1, "Server Draft") })

	h.eventually("uploaded as a Server Draft", func() bool {
		d := h.localDrafts()
		return len(d) == 1 && !d[0].LocalOnly && d[0].SyncedID != "" && !d[0].Dirty
	})
	meta := h.localDrafts()[0]
	if meta.Subject != bulgarianSubject {
		t.Errorf("local draft subject = %q", meta.Subject)
	}
	d, err := h.drafts.Load("flow", meta.ID)
	if err != nil {
		t.Fatalf("local draft unreadable after the edit: %v", err)
	}
	if d.Subject != bulgarianSubject || strings.TrimSpace(d.Body) != "Здравей,\nкак си?" || len(d.To) != 1 {
		t.Errorf("local draft = %+v", d)
	}
	server := h.serverFolder("Drafts")
	if len(server) != 1 || server[0].Envelope.Subject != bulgarianSubject {
		t.Fatalf("server Drafts = %v, want one draft with the Bulgarian subject", subjects(server))
	}
	if body := strings.TrimSpace(server[0].PlainText()); !strings.Contains(body, "как си?") {
		t.Errorf("server draft body = %q", body)
	}
	h.eventually("the bottom row reports the sync", func() bool {
		return strings.Contains(h.ra.uiApp.StatusBar.FlashText(), "synced")
	})
	h.putCursorOnLocalDraft()
	h.eventually("preview shows the Bulgarian subject", func() bool {
		return strings.Contains(h.ra.preview.GetText(true), bulgarianSubject)
	})
}

func subjects(msgs []*mail.Message) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Envelope.Subject)
	}
	return out
}

// TestDraftFlow_sentDraftLeavesTheTree: after Send the draft is gone from
// Local Drafts on screen too, not only on disk.
func TestDraftFlow_sentDraftLeavesTheTree(t *testing.T) {
	h := newFlowHarness(t)
	h.eventually("connected with INBOX open", func() bool {
		return h.ra.currentFolder != nil && h.ra.currentFolder.Name == "INBOX"
	})
	h.onUI(func() { h.ra.uiApp.SetFocus(h.ra.maillist) })
	h.key('c')
	h.eventually("send prompt", func() bool { return h.ra.uiApp.Pages.HasPage("send-modal") })
	h.onUI(func() { h.ra.testModalDone(0, "Send") })

	h.eventually("message sent", func() bool { return len(h.sender.Sent()) == 1 })
	h.eventually("local draft removed from disk and from the tree", func() bool {
		if len(h.localDrafts()) != 0 {
			return false
		}
		found := false
		h.ra.folders.GetRoot().Walk(func(n, _ *tview.TreeNode) bool {
			if _, ok := n.GetReference().(*draft.Meta); ok {
				found = true
			}
			return true
		})
		return !found
	})
}

func TestSendFlow_copyKeptInSentItems(t *testing.T) {
	h := newFlowHarness(t)
	h.eventually("connected with INBOX open", func() bool {
		return h.ra.currentFolder != nil && h.ra.currentFolder.Name == "INBOX"
	})
	h.onUI(func() { h.ra.uiApp.SetFocus(h.ra.maillist) })
	h.key('c')
	h.eventually("send prompt", func() bool { return h.ra.uiApp.Pages.HasPage("send-modal") })
	h.onUI(func() { h.ra.testModalDone(0, "Send") })

	h.eventually("message sent", func() bool { return len(h.sender.Sent()) == 1 })
	deadline := time.Now().Add(5 * time.Second)
	for {
		msgs := h.serverFolder("Sent Items")
		if len(msgs) == 1 {
			if msgs[0].Envelope.Subject != "Flow draft" {
				t.Fatalf("Sent Items subject = %q, want %q", msgs[0].Envelope.Subject, "Flow draft")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Sent Items has %d messages, want 1", len(msgs))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// trashHarness has 3 messages in Deleted Items and 1 in INBOX, with
// Deleted Items open and its first message previewed.
func trashHarness(t *testing.T) *flowHarness {
	t.Helper()
	h := newFlowHarness(t)
	ctx := context.Background()
	msg := "From: a@example.com\r\nTo: b@example.com\r\nSubject: old\r\n\r\nold mail\r\n"
	for i := 0; i < 3; i++ {
		if _, err := h.server.Append(ctx, "Deleted Items", []byte(msg), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.server.Append(ctx, "INBOX", []byte(msg), nil); err != nil {
		t.Fatal(err)
	}
	h.eventually("connected with INBOX open", func() bool {
		return h.ra.currentFolder != nil && h.ra.currentFolder.Name == "INBOX"
	})
	h.onUI(func() {
		h.ra.openFolderByName("Deleted Items")
		h.ra.uiApp.SetFocus(h.ra.maillist)
	})
	h.eventually("a message in Deleted Items previewed", func() bool {
		return h.ra.currentMsg != nil && h.ra.maillist.VisibleCount() == 3
	})
	return h
}

func TestTrash_deleteAllForever(t *testing.T) {
	h := trashHarness(t)

	h.key('d')
	h.eventually("delete dialog", func() bool { return h.ra.uiApp.Pages.HasPage("delete-modal") })
	var first []ui.DialogButton
	h.onUI(func() { first = h.ra.testDeleteButtons })
	if len(first) != 3 || first[0].Label != "Delete Forever" || first[1].Label != "Delete All Forever" || first[2].Label != "Cancel" {
		t.Fatalf("buttons in Deleted Items = %+v", first)
	}
	if !first[1].Danger || first[0].Danger {
		t.Errorf("only Delete All Forever is drawn in the danger colour: %+v", first)
	}

	h.onUI(func() { h.ra.testDeleteModalDone(1, "Delete All Forever") })
	h.eventually("second confirmation", func() bool {
		return h.ra.uiApp.Pages.HasPage("delete-modal") && strings.Contains(h.ra.testDeleteModalText, "ALL 3")
	})
	var second []ui.DialogButton
	var focus int
	h.onUI(func() { second, focus = h.ra.testDeleteButtons, h.ra.testDeleteFocus })
	if len(second) != 2 || second[0].Label != "Delete All Forever" || !second[0].Danger || second[1].Label != "Cancel" {
		t.Fatalf("confirmation buttons = %+v", second)
	}
	if focus != 1 {
		t.Errorf("confirmation starts on button %d, want Cancel (1): a stray Enter must not empty the folder", focus)
	}

	h.onUI(func() { h.ra.testDeleteModalDone(0, "Delete All Forever") })
	h.eventually("Deleted Items emptied on screen", func() bool {
		return h.ra.maillist.VisibleCount() == 0 && strings.Contains(h.ra.uiApp.StatusBar.FlashText(), "3")
	})
	if n := len(h.serverFolder("Deleted Items")); n != 0 {
		t.Errorf("server Deleted Items still has %d messages", n)
	}
	if n := len(h.serverFolder("INBOX")); n != 1 {
		t.Errorf("INBOX has %d messages, want its 1 untouched", n)
	}
	h.eventually("Deleted Items count shows 0", func() bool {
		_, total, ok := h.ra.folders.Counts("flow", "Deleted Items")
		return ok && total == 0
	})
}

func TestTrash_deleteAllCancelKeepsEverything(t *testing.T) {
	h := trashHarness(t)
	h.key('d')
	h.eventually("delete dialog", func() bool { return h.ra.uiApp.Pages.HasPage("delete-modal") })
	h.onUI(func() { h.ra.testDeleteModalDone(1, "Delete All Forever") })
	h.eventually("second confirmation", func() bool { return strings.Contains(h.ra.testDeleteModalText, "ALL") })
	h.onUI(func() { h.ra.testDeleteModalDone(1, "Cancel") })

	time.Sleep(200 * time.Millisecond)
	if n := len(h.serverFolder("Deleted Items")); n != 3 {
		t.Errorf("Deleted Items has %d messages after Cancel, want 3", n)
	}
}

func TestTrash_singleDeleteForeverStillWorks(t *testing.T) {
	h := trashHarness(t)
	h.key('d')
	h.eventually("delete dialog", func() bool { return h.ra.uiApp.Pages.HasPage("delete-modal") })
	h.onUI(func() { h.ra.testDeleteModalDone(0, "Delete Forever") })
	h.eventually("one message removed", func() bool { return len(h.serverFolder("Deleted Items")) == 2 })
}

func TestStartup_foldersPaneFocused(t *testing.T) {
	h := newFlowHarness(t)
	h.eventually("connected with INBOX open", func() bool {
		return h.ra.currentFolder != nil && h.ra.currentFolder.Name == "INBOX"
	})
	time.Sleep(200 * time.Millisecond) // let any late focus change land
	var focused tview.Primitive
	h.onUI(func() { focused = h.ra.uiApp.GetFocus() })
	if focused != h.ra.folders {
		t.Fatalf("focus on start = %T, want the Folders pane", focused)
	}
	var title string
	var pane paneKind
	h.onUI(func() { title, pane = h.ra.folders.GetTitle(), h.ra.pane })
	if !strings.Contains(title, "[::b]") || pane != paneFolders {
		t.Fatalf("Folders pane focused but not shown as focused: title %q, pane %v", title, pane)
	}
}

func TestSelection_dragInPreviewCopiesItsText(t *testing.T) {
	h := newFlowHarness(t)
	ctx := context.Background()
	msg := "From: a@example.com\r\nTo: b@example.com\r\nSubject: pick me\r\n\r\nselect this body text\r\n"
	if _, err := h.server.Append(ctx, "INBOX", []byte(msg), nil); err != nil {
		t.Fatal(err)
	}
	copied := make(chan string, 1)
	h.onUI(func() {
		h.ra.selector.Copy = func(text string) error { copied <- text; return nil }
	})
	h.eventually("message previewed", func() bool {
		return h.ra.currentMsg != nil && strings.Contains(h.ra.preview.GetText(true), "select this body text")
	})
	var px, py, pw, ph int
	h.onUI(func() { px, py, pw, ph = h.ra.preview.GetInnerRect() })
	// The text is set before the screen is drawn: wait for the drawn line.
	row := -1
	h.eventually("body line on screen", func() bool {
		for y := py; y < py+ph; y++ {
			var line strings.Builder
			for x := px; x < px+pw; x++ {
				c, _, _, _ := h.screen.GetContent(x, y)
				line.WriteRune(c)
			}
			if strings.Contains(line.String(), "select this body text") {
				row = y
				return true
			}
		}
		return false
	})
	// Start left of the preview's text column (inside the pane), drag over "select this".
	h.screen.InjectMouse(px, row, tcell.ButtonPrimary, tcell.ModNone)
	h.screen.InjectMouse(px+5, row, tcell.ButtonPrimary, tcell.ModNone)
	h.screen.InjectMouse(px+10, row, tcell.ButtonPrimary, tcell.ModNone)
	h.screen.InjectMouse(px+10, row, tcell.ButtonNone, tcell.ModNone)
	select {
	case got := <-copied:
		if got != "select this" {
			t.Fatalf("copied %q, want %q", got, "select this")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing copied")
	}
	h.eventually("status says copied", func() bool {
		return strings.Contains(h.ra.uiApp.StatusBar.FlashText(), "Copied 11 characters")
	})
}
