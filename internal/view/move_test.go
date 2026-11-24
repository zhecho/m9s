package view

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tcell "github.com/derailed/tcell/v2"
	"github.com/zhecho/m9s/internal/mail"
	msync "github.com/zhecho/m9s/internal/sync"
)

// moverStub is an IMAP-like account that can move and archive.
type moverStub struct {
	stubAccount
	targets []string
}

func (s *moverStub) SetFlags(_ context.Context, folder, id string, flags []string, add bool) error {
	s.record(fmt.Sprintf("SetFlags %s %s %s %v", folder, id, strings.Join(flags, ","), add))
	return nil
}
func (s *moverStub) Move(_ context.Context, folder, id, dest string) error {
	s.record("Move " + folder + " " + id + " " + dest)
	return nil
}
func (s *moverStub) Archive(_ context.Context, folder, id string) error {
	s.record("Archive " + folder + " " + id)
	return nil
}
func (s *moverStub) MoveTargets(string) []string { return s.targets }

func oneMessage() *mail.Envelope {
	return &mail.Envelope{ID: "7", Subject: "Invoice", Date: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC),
		From: []mail.Address{{Name: "Bob", Email: "bob@example.com"}}, Flags: []string{`\Seen`}}
}

// imapApp shows one message in INBOX of account "test", selected, with the
// mail list focused; eng answers for the account.
func imapApp(t *testing.T, eng msync.Account, rows []*mail.Envelope) *RootApp {
	t.Helper()
	ra := newWiredRootApp(t, "test")
	ra.engines["test"] = eng
	recordBodyLoads(ra)
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX", "Work", "Archive")})
	ra.uiApp.SetFocus(ra.maillist)
	ra.showEnvelopes(onScreen(rows))
	ra.maillist.Select(0, 0)
	return ra
}

func eventuallyFlash(t *testing.T, ra *RootApp, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(ra.uiApp.StatusBar.FlashText(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("status = %q, want %q", ra.uiApp.StatusBar.FlashText(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStar_singleMessageToggles(t *testing.T) {
	stub := &moverStub{}
	stub.rows = []*mail.Envelope{oneMessage()}
	ra := imapApp(t, stub, stub.rows)

	if sendRune(ra, '*') != nil {
		t.Fatal("* was not consumed")
	}
	stub.waitCall(t, `SetFlags INBOX 7 \Flagged true`)
	if !ra.selectedEnv.IsFlagged() {
		t.Error("the row is not shown starred")
	}
	sendRune(ra, '*')
	stub.waitCall(t, `SetFlags INBOX 7 \Flagged false`)
	if ra.selectedEnv.IsFlagged() {
		t.Error("the row is still shown starred")
	}
}

func TestArchive_singleMessage(t *testing.T) {
	stub := &moverStub{}
	stub.rows = []*mail.Envelope{oneMessage()}
	ra := imapApp(t, stub, stub.rows)

	if sendRune(ra, 'a') != nil {
		t.Fatal("a was not consumed")
	}
	stub.waitCall(t, "Archive INBOX 7")
}

// The error an account without an archive folder gives says how to fix it.
func TestArchive_noArchiveFolderSaysHow(t *testing.T) {
	if !strings.Contains(msync.ErrNoArchiveFolder.Error(), "folders.archive") {
		t.Errorf("ErrNoArchiveFolder = %q, want it to name folders.archive", msync.ErrNoArchiveFolder)
	}
}

func TestArchiveAndMove_notOfferedWithoutMover(t *testing.T) {
	stub := &stubAccount{rows: []*mail.Envelope{oneMessage()}}
	ra := imapApp(t, stub, stub.rows)
	sendRune(ra, 'a')
	eventuallyFlash(t, ra, "not available")
	sendRune(ra, 'm')
	if ra.uiApp.Pages.HasPage(pageMovePicker) {
		t.Error("move picker opened for an account that cannot move")
	}
}

func TestMove_pickerMovesToChosenFolder(t *testing.T) {
	stub := &moverStub{targets: []string{"Work", "Archive", "archive/2025"}}
	stub.rows = []*mail.Envelope{oneMessage()}
	ra := imapApp(t, stub, stub.rows)

	if sendRune(ra, 'm') != nil {
		t.Fatal("m was not consumed")
	}
	if !ra.uiApp.Pages.HasPage(pageMovePicker) {
		t.Fatal("m did not open the folder picker")
	}
	if got, want := ra.movePicker.Items(), []string{"Archive", "archive/2025", "Work"}; !slices.Equal(got, want) {
		t.Errorf("picker items = %v, want %v (sorted, case-insensitive)", got, want)
	}
	// Keys go to the picker, not the global shortcuts.
	if sendRune(ra, 'd') == nil {
		t.Error("d was swallowed by the global keys while the picker is open")
	}
	ra.movePicker.InputHandler()(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone), nil)
	ra.movePicker.InputHandler()(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone), nil)
	ra.movePicker.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), nil)

	stub.waitCall(t, "Move INBOX 7 Work")
	if ra.uiApp.Pages.HasPage(pageMovePicker) {
		t.Error("picker still open after choosing")
	}
}

func TestMove_escCancels(t *testing.T) {
	stub := &moverStub{targets: []string{"Work"}}
	stub.rows = []*mail.Envelope{oneMessage()}
	ra := imapApp(t, stub, stub.rows)
	sendRune(ra, 'm')
	ra.movePicker.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), nil)
	if ra.uiApp.Pages.HasPage(pageMovePicker) || ra.uiApp.GetFocus() != ra.maillist {
		t.Error("Esc must close the picker and give the mail list its focus back")
	}
	if slices.ContainsFunc(stub.called(), func(c string) bool { return strings.HasPrefix(c, "Move") }) {
		t.Errorf("moved after Esc: %v", stub.called())
	}
}

// threadsMover is a Gmail-like account with conversations that can move.
type threadsMover struct {
	stubThreads
	targets []string
}

func (s *threadsMover) Move(_ context.Context, folder, id, dest string) error {
	s.record("Move " + folder + " " + id + " " + dest)
	return nil
}
func (s *threadsMover) Archive(_ context.Context, folder, id string) error {
	s.record("Archive " + folder + " " + id)
	return nil
}
func (s *threadsMover) MoveTargets(string) []string { return s.targets }

// On a conversation row, m moves the whole conversation.
func TestMove_conversationRowMovesWholeConversation(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	stub := &threadsMover{stubThreads: stubThreads{thread: convMessages()}, targets: []string{"Work"}}
	stub.rows = []*mail.Envelope{convRow()}
	ra.engines["test"] = stub
	recordBodyLoads(ra)
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX", "Work", "Trash")})
	ra.uiApp.SetFocus(ra.maillist)
	ra.showEnvelopes(onScreen(stub.rows))
	ra.maillist.Select(0, 0)

	sendRune(ra, 'm')
	ra.movePicker.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), nil)
	stub.waitCall(t, "ModifyThread T +Work -INBOX")
}

func TestBuildForwardDraft(t *testing.T) {
	acct := &mail.Account{Name: "work", Config: nil}
	msg := &mail.Message{
		Envelope: &mail.Envelope{Subject: "Invoice 42", Date: time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC),
			From: []mail.Address{{Name: "Bob", Email: "bob@example.com"}},
			To:   []mail.Address{{Email: "me@example.com"}}, CC: []mail.Address{{Email: "ann@example.com"}}},
		TextBody:    "Please pay.",
		Attachments: []*mail.Attachment{{Filename: "invoice.pdf", Data: []byte("%PDF")}, {Filename: "logo.png", Inline: true, ContentID: "logo"}},
	}
	d, dropped := buildForwardDraft(acct, msg)
	if d.Subject != "Fwd: Invoice 42" || len(d.To) != 0 || d.InReplyTo != "" {
		t.Errorf("draft = %+v", d)
	}
	for _, want := range []string{"---------- Forwarded message ---------", "From: Bob <bob@example.com>",
		"Subject: Invoice 42", "To: me@example.com", "Cc: ann@example.com", "Please pay.",
		"Attachments not included: invoice.pdf"} {
		if !strings.Contains(d.Body, want) {
			t.Errorf("body lacks %q:\n%s", want, d.Body)
		}
	}
	if strings.Contains(d.Body, "logo.png") {
		t.Error("an inline picture is not an attachment")
	}
	if !slices.Equal(dropped, []string{"invoice.pdf"}) {
		t.Errorf("dropped = %v", dropped)
	}
	for _, subj := range []string{"Fwd: x", "FW: x", "fwd: x"} {
		msg.Envelope.Subject = subj
		if d, _ := buildForwardDraft(acct, msg); d.Subject != subj {
			t.Errorf("subject %q became %q", subj, d.Subject)
		}
	}
}

// f reaches the editor with the previewed message (here: no editor is set,
// which the status bar reports).
func TestForwardKeyOpensEditor(t *testing.T) {
	t.Setenv("EDITOR", "")
	stub := &moverStub{}
	stub.rows = []*mail.Envelope{oneMessage()}
	ra := imapApp(t, stub, stub.rows)
	ra.currentMsg = &mail.Message{Envelope: ra.selectedEnv, TextBody: "hi"}
	if sendRune(ra, 'f') != nil {
		t.Fatal("f was not consumed")
	}
	if !strings.Contains(ra.uiApp.StatusBar.FlashText(), "no editor") {
		t.Errorf("status = %q, want the editor to be asked for", ra.uiApp.StatusBar.FlashText())
	}
}

// Through the real IMAP engine and an IMAP server: * stars the selected
// message, a archives it to the Archive folder, m moves the other one to the
// folder picked in the list.
func TestFlow_starArchiveMoveOnIMAP(t *testing.T) {
	h := newFlowHarness(t, "Archive")
	ctx := context.Background()
	for _, subj := range []string{"older", "newer"} {
		msg := "From: a@example.com\r\nTo: flow@example.com\r\nSubject: " + subj + "\r\n\r\nbody\r\n"
		if _, err := h.server.Append(ctx, "INBOX", []byte(msg), nil); err != nil {
			t.Fatal(err)
		}
	}
	count := func(folder string) int {
		envs, err := h.server.FetchHeaders(ctx, folder, 1, 50)
		if err != nil {
			t.Fatalf("FetchHeaders(%s): %v", folder, err)
		}
		return len(envs)
	}
	h.eventually("both listed", func() bool { return h.ra.maillist.VisibleCount() == 2 && h.ra.selectedEnv != nil })
	h.screen.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	h.eventually("mail list focused", h.ra.mailListFocused)

	h.key('*')
	deadline := time.Now().Add(5 * time.Second)
	for {
		envs, _ := h.server.FetchHeaders(ctx, "INBOX", 1, 50)
		if slices.ContainsFunc(envs, (*mail.Envelope).IsFlagged) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no message starred on the server")
		}
		time.Sleep(20 * time.Millisecond)
	}

	h.key('a')
	h.eventually("archived", func() bool { return count("Archive") == 1 && count("INBOX") == 1 })
	if got := h.serverFolder("Archive"); got[0].Envelope.Subject != "newer" || !got[0].Envelope.IsFlagged() {
		t.Errorf("archived %q (flagged %v), want the starred newer message", got[0].Envelope.Subject, got[0].Envelope.IsFlagged())
	}

	h.eventually("the other one listed", func() bool {
		return h.ra.maillist.VisibleCount() == 1 && h.ra.selectedEnv != nil && h.ra.selectedEnv.Subject == "older"
	})
	h.key('m')
	h.eventually("picker open", func() bool { return h.ra.movePicker != nil })
	var items []string
	h.onUI(func() { items = h.ra.movePicker.Items() })
	if want := []string{"Archive", "Deleted Items", "Sent Items"}; !slices.Equal(items, want) {
		t.Fatalf("picker = %v, want %v", items, want)
	}
	h.screen.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	h.screen.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	h.screen.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	h.eventually("moved", func() bool { return count("Sent Items") == 1 && count("INBOX") == 0 })
}
