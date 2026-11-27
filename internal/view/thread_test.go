package view

import (
	"context"
	"errors"
	"slices"
	"strings"
	gosync "sync"
	"testing"
	"time"

	tcell "github.com/derailed/tcell/v2"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/store"
	msync "github.com/zhecho/m9s/internal/sync"
)

// stubAccount is an Account that answers what the view asks of a list;
// anything else it is asked panics (the embedded interface is nil).
type stubAccount struct {
	msync.Account
	mu    gosync.Mutex
	calls []string
	rows  []*mail.Envelope
}

func (s *stubAccount) record(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

func (s *stubAccount) called() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// waitCall waits for a call to be recorded.
func (s *stubAccount) waitCall(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !slices.Contains(s.called(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("calls = %q, want %q", s.called(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *stubAccount) TrashFolderName() string              { return "Trash" }
func (s *stubAccount) DraftsFolderName() string             { return "Drafts" }
func (s *stubAccount) RefreshBadge(context.Context, string) {}
func (s *stubAccount) FetchHeaders(context.Context, string, int, int) ([]*mail.Envelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows, nil
}

// onScreen copies rows for the mail list: the UI marks the envelopes it
// shows read, while a background fetch of the stub reads the originals.
func onScreen(rows []*mail.Envelope) []*mail.Envelope {
	out := make([]*mail.Envelope, len(rows))
	for i, e := range rows {
		cp := *e
		cp.Flags = slices.Clone(e.Flags)
		out[i] = &cp
	}
	return out
}
func (s *stubAccount) MoveToTrash(_ context.Context, folder, id string) error {
	s.record("MoveToTrash " + folder + " " + id)
	return nil
}
func (s *stubAccount) SetFlags(_ context.Context, _ string, id string, flags []string, add bool) error {
	s.record("SetFlags " + id)
	return nil
}

// stubThreads is a Gmail-like account: it has conversations.
type stubThreads struct {
	stubAccount
	thread    []*mail.Message
	threadErr error
}

func (s *stubThreads) Thread(_ context.Context, id string) ([]*mail.Message, error) {
	s.record("Thread " + id)
	return s.thread, s.threadErr
}
func (s *stubThreads) ModifyThread(_ context.Context, id string, add, remove []string) error {
	s.record("ModifyThread " + id + " +" + strings.Join(add, ",") + " -" + strings.Join(remove, ","))
	return nil
}
func (s *stubThreads) TrashThread(_ context.Context, id string) error {
	s.record("TrashThread " + id)
	return nil
}

func convMessages() []*mail.Message {
	day := func(d int) time.Time { return time.Date(2026, 11, d, 9, 0, 0, 0, time.UTC) }
	m := func(id, name, email, body string, d int) *mail.Message {
		return &mail.Message{Envelope: &mail.Envelope{ID: id, ThreadID: "T", Subject: "Lunch",
			From: []mail.Address{{Name: name, Email: email}}, To: []mail.Address{{Email: "me@example.com"}},
			MessageID: id + "@x", Date: day(d), Flags: []string{`\Seen`}}, TextBody: body}
	}
	return []*mail.Message{
		m("a", "Bob Stone", "bob@example.com", "first body", 1),
		m("b", "Me", "me@example.com", "second body", 2),
		m("c", "Ann Lee", "ann@example.com", "third body", 3),
	}
}

func convRow() *mail.Envelope {
	env := *convMessages()[2].Envelope
	env.Flags = nil // unread
	env.ThreadCount = 3
	env.Participants = []mail.Address{{Name: "Bob Stone", Email: "bob@example.com"},
		{Name: "Me", Email: "me@example.com"}, {Name: "Ann Lee", Email: "ann@example.com"}}
	return &env
}

// gmailApp is a wired app whose "test" account has conversations, showing
// one conversation row in INBOX, selected, with the mail list focused.
func gmailApp(t *testing.T) (*RootApp, *stubThreads) {
	t.Helper()
	ra := newWiredRootApp(t, "test")
	stub := &stubThreads{thread: convMessages()}
	stub.rows = []*mail.Envelope{convRow()}
	ra.engines["test"] = stub
	recordBodyLoads(ra)
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX", "Sent", "Trash")})
	ra.uiApp.SetFocus(ra.maillist)
	ra.showEnvelopes(onScreen(stub.rows))
	ra.maillist.Select(0, 0)
	return ra, stub
}

func TestMailListShowsThreadCount(t *testing.T) {
	mlv := NewMailListView(testSkin(), "Jan 02 15:04", nil)
	mlv.SetSelf("me@example.com")
	alone := &mail.Envelope{ID: "z", ThreadID: "Z", ThreadCount: 1, Subject: "Alone",
		From: []mail.Address{{Name: "Cy Young", Email: "cy@example.com"}}}
	mlv.SetEnvelopes([]*mail.Envelope{convRow(), alone})
	from, subj := mlv.GetCell(0, 0).Text, mlv.GetCell(0, 1).Text
	if !strings.Contains(from, "Bob, me, Ann") {
		t.Errorf("sender cell = %q, want participants Bob, me, Ann", from)
	}
	if subj != "Lunch (3)" {
		t.Errorf("subject cell = %q, want Lunch (3)", subj)
	}
	// A conversation of one shows its sender and subject as a message would.
	if from, subj := mlv.GetCell(1, 0).Text, mlv.GetCell(1, 1).Text; !strings.Contains(from, "Cy Young") || subj != "Alone" {
		t.Errorf("single row = %q / %q", from, subj)
	}
}

func TestReaderStacksConversationNewestExpanded(t *testing.T) {
	rv := NewReaderView(testSkin(), nil)
	c := newConversation(convMessages(), false)
	rv.ShowConversation(c, false)
	text := rv.GetText(true)
	if !strings.Contains(text, "third body") {
		t.Errorf("newest message not expanded:\n%s", text)
	}
	for _, hidden := range []string{"first body", "second body"} {
		if strings.Contains(text, hidden) {
			t.Errorf("older message shown expanded (%q):\n%s", hidden, text)
		}
	}
	if !(strings.Index(text, "Bob Stone") < strings.Index(text, "Me") && strings.Index(text, "Me") < strings.Index(text, "third body")) {
		t.Errorf("messages not stacked oldest to newest:\n%s", text)
	}
	if n := strings.Count(text, "\n"); n < 3 {
		t.Errorf("text = %q", text)
	}

	// In the app: Enter opens the reader; Shift-Tab moves to the previous
	// header line, Enter expands it.
	ra, _ := gmailApp(t)
	ra.conversationArrived(ra.manager.Active(), ra.currentFolder, ra.selectedEnv, newConversation(convMessages(), false))
	sendKey(ra, tcell.KeyEnter)
	if page, _ := ra.uiApp.Pages.GetFrontPage(); page != pageReader {
		t.Fatalf("Enter in the mail list shows %q, want the reader", page)
	}
	sendKey(ra, tcell.KeyBacktab)
	sendKey(ra, tcell.KeyEnter)
	if text := ra.reader.GetText(true); !strings.Contains(text, "second body") || !strings.Contains(text, "third body") {
		t.Errorf("Enter on the second header did not expand it:\n%s", text)
	}
	sendKey(ra, tcell.KeyEnter)
	if text := ra.reader.GetText(true); strings.Contains(text, "second body") {
		t.Errorf("Enter again did not collapse it:\n%s", text)
	}
	sendKey(ra, tcell.KeyTab) // back to the newest
	sendKey(ra, tcell.KeyEnter)
	if text := ra.reader.GetText(true); strings.Contains(text, "third body") {
		t.Errorf("Tab did not move to the newest header:\n%s", text)
	}
}

func TestReplyUsesExpandedMessage(t *testing.T) {
	ra, _ := gmailApp(t)
	ra.conversationArrived(ra.manager.Active(), ra.currentFolder, ra.selectedEnv, newConversation(convMessages(), false))
	if ra.currentMsg == nil || ra.currentMsg.Envelope.ID != "c" {
		t.Fatalf("reply target = %v, want the newest message", ra.currentMsg)
	}
	sendKey(ra, tcell.KeyEnter) // open the reader
	sendKey(ra, tcell.KeyBacktab)
	sendKey(ra, tcell.KeyBacktab) // the oldest
	sendKey(ra, tcell.KeyEnter)   // expand it
	if ra.currentMsg.Envelope.ID != "a" {
		t.Fatalf("reply target = %s, want the expanded oldest message a", ra.currentMsg.Envelope.ID)
	}
	// r replies to it.
	t.Setenv("EDITOR", "")
	if ev := sendRune(ra, 'r'); ev != nil {
		t.Error("r not consumed")
	}
	// With both expanded, moving the cursor to the newest moves the reply too.
	sendKey(ra, tcell.KeyBacktab) // wraps to the newest, c (expanded)
	if ra.currentMsg.Envelope.ID != "c" {
		t.Fatalf("reply target with the cursor on expanded c = %s, want c", ra.currentMsg.Envelope.ID)
	}
	sendKey(ra, tcell.KeyTab) // back to a
	if ra.currentMsg.Envelope.ID != "a" {
		t.Fatalf("reply target with the cursor on expanded a = %s, want a", ra.currentMsg.Envelope.ID)
	}
	// Collapsing it hands the reply back to the newest expanded message.
	sendKey(ra, tcell.KeyEnter)
	if ra.currentMsg.Envelope.ID != "c" {
		t.Fatalf("reply target after collapsing = %s, want c", ra.currentMsg.Envelope.ID)
	}
}

func TestEnterOnIMAPListDoesNotOpenReader(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	stub := &stubAccount{rows: peopleEnvelopes()}
	ra.engines["test"] = stub
	recordBodyLoads(ra)
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX")})
	ra.uiApp.SetFocus(ra.maillist)
	ra.showEnvelopes(onScreen(stub.rows))
	ra.maillist.Select(0, 0)
	ra.currentMsg = &mail.Message{Envelope: ra.selectedEnv, TextBody: "hi"}
	sendKey(ra, tcell.KeyEnter)
	if page, _ := ra.uiApp.Pages.GetFrontPage(); page != pageMain {
		t.Fatalf("Enter on an IMAP message shows %q; it must do nothing, as before", page)
	}
}

func TestDeleteOnConversationTrashesThread(t *testing.T) {
	ra, stub := gmailApp(t)
	// The body has not even arrived: the row alone is enough.
	if ev := sendRune(ra, 'd'); ev != nil {
		t.Fatal("d not consumed")
	}
	if !strings.Contains(ra.testDeleteModalText, "conversation") || !strings.Contains(ra.testDeleteModalText, "3 messages") {
		t.Errorf("dialog = %q", ra.testDeleteModalText)
	}
	ra.testDeleteModalDone(0, "Delete")
	stub.waitCall(t, "TrashThread T")
	for _, c := range stub.called() {
		if strings.HasPrefix(c, "MoveToTrash") {
			t.Errorf("a single message was moved: %q", c)
		}
	}
}

// bulkThreads trashes conversations in bulk; "T2" was deleted meanwhile.
type bulkThreads struct{ stubThreads }

func (s *bulkThreads) TrashThreadsMany(_ context.Context, threadIDs []string, progress func(int)) map[string]error {
	s.record("TrashThreadsMany " + strings.Join(threadIDs, ","))
	progress(len(threadIDs))
	return map[string]error{"T2": errors.New("not found")}
}

// Marked conversation rows go to the trash as wholes in one bulk call; a
// row with no conversation goes alone, and the failed row stays marked.
func TestDelete_markedConversationsInBulk(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	stub := &bulkThreads{}
	stub.rows = []*mail.Envelope{
		{ID: "a", ThreadID: "T1", Subject: "one"},
		{ID: "b", ThreadID: "T2", Subject: "two"},
		{ID: "c", Subject: "alone"},
	}
	ra.engines["test"] = stub
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX", "Sent", "Trash")})
	ra.uiApp.SetFocus(ra.maillist)
	ra.showEnvelopes(onScreen(stub.rows))
	queued := make(chan func(), 16)
	ra.deliver = func(fn func()) { queued <- fn }
	for i := range 3 {
		ra.maillist.ToggleMark(i)
	}
	sendRune(ra, 'd')
	ra.testDeleteModalDone(0, "Delete")

	want := "Moved 2 conversations to Trash; 1 failed"
	deadline := time.After(2 * time.Second)
	for !strings.Contains(ra.uiApp.StatusBar.FlashText(), want) {
		select {
		case fn := <-queued:
			fn()
		case <-deadline:
			t.Fatalf("status = %q, want %q", ra.uiApp.StatusBar.FlashText(), want)
		}
	}
	if calls := stub.called(); !slices.Contains(calls, "TrashThreadsMany T1,T2") || !slices.Contains(calls, "MoveToTrash INBOX c") ||
		slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, "TrashThread ") }) {
		t.Errorf("calls = %q, want one bulk call and c alone", calls)
	}
	if got := markedIDs(ra.maillist); !slices.Equal(got, []string{"b"}) {
		t.Errorf("still marked = %v, want the failed one", got)
	}
}

func TestConversationRowActionsApplyToThread(t *testing.T) {
	ra, stub := gmailApp(t)
	sendRune(ra, 'u')
	stub.waitCall(t, `ModifyThread T + -\Seen`)
	sendRune(ra, '*') // the row is not starred: star it
	stub.waitCall(t, `ModifyThread T +\Flagged -`)
	sendRune(ra, 'a')
	stub.waitCall(t, `ModifyThread T + -INBOX`)
}

func TestOpeningConversationMarksThreadRead(t *testing.T) {
	ra, stub := gmailApp(t)
	ra.markThreadRead(stub, "INBOX", ra.selectedEnv)
	stub.waitCall(t, `ModifyThread T +\Seen -`)
	// Focusing the mail list marks the selected conversation read too.
	ra.uiApp.SetFocus(ra.folders)
	ra.uiApp.SetFocus(ra.maillist)
	deadline := time.Now().Add(2 * time.Second)
	for n := 0; n < 2; {
		n = 0
		for _, c := range stub.called() {
			if c == `ModifyThread T +\Seen -` {
				n++
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("calls = %q", stub.called())
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, c := range stub.called() {
		if strings.HasPrefix(c, "SetFlags") {
			t.Errorf("one message marked instead of the thread: %q", c)
		}
	}
}

func TestOfflineConversationFallsBackToStore(t *testing.T) {
	ra, stub := gmailApp(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ra.store = st
	msgs := convMessages()
	if err := st.AddMessages("test", "INBOX", []*mail.Envelope{msgs[0].Envelope, msgs[2].Envelope}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddMessages("test", "Sent", []*mail.Envelope{msgs[1].Envelope}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutBody("test", "", "a", []byte("Subject: Lunch\r\n\r\nstored first body\r\n")); err != nil {
		t.Fatal(err)
	}
	stub.threadErr = errors.New("dial tcp: no network")

	c := ra.fetchConversation("test", stub, ra.selectedEnv)
	if !c.offline || len(c.msgs) != 3 {
		t.Fatalf("offline = %v, %d messages; want the 3 stored ones", c.offline, len(c.msgs))
	}
	if got := c.msgs[0].PlainText(); !strings.Contains(got, "stored first body") {
		t.Errorf("stored body not used: %q", got)
	}
	ra.conversationArrived(ra.manager.Active(), ra.currentFolder, ra.selectedEnv, c)
	if got := ra.uiApp.StatusBar.FlashText(); !strings.Contains(got, "offline — conversation may be incomplete") {
		t.Errorf("status = %q", got)
	}
	if !strings.Contains(ra.preview.GetText(true), "Lunch") {
		t.Errorf("preview = %q", ra.preview.GetText(true))
	}
}

func TestIMAPFolderStillOneRowPerMessage(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	stub := &stubAccount{rows: peopleEnvelopes()}
	ra.engines["test"] = stub
	recordBodyLoads(ra)
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX")})
	if msync.ShowsConversations(stub, "INBOX") || ra.conversations("test", "INBOX") {
		t.Fatal("an account without threads lists conversations")
	}
	ra.showEnvelopes(onScreen(stub.rows))
	if n := ra.maillist.GetRowCount(); n != 3 {
		t.Fatalf("rows = %d, want one per message", n)
	}
	if subj := ra.maillist.GetCell(0, 1).Text; subj != "Lunch" {
		t.Errorf("subject = %q", subj)
	}
	// Archive and star on IMAP messages are covered in move_test.go.
}

// TestOfflineConversationNoReplyToUndownloaded: offline, a message whose
// body was never downloaded shows a placeholder; r and R refuse to quote it
// and say why.
func TestOfflineConversationNoReplyToUndownloaded(t *testing.T) {
	t.Setenv("EDITOR", "")
	ra, stub := gmailApp(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ra.store = st
	msgs := convMessages()
	if err := st.AddMessages("test", "INBOX", []*mail.Envelope{msgs[0].Envelope, msgs[2].Envelope}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutBody("test", "", "a", []byte("Subject: Lunch\r\n\r\nstored first body\r\n")); err != nil {
		t.Fatal(err)
	}
	stub.threadErr = errors.New("dial tcp: no network")
	c := ra.fetchConversation("test", stub, ra.selectedEnv)
	ra.conversationArrived(ra.manager.Active(), ra.currentFolder, ra.selectedEnv, c)
	if ra.currentMsg == nil || ra.currentMsg.Envelope.ID != "c" {
		t.Fatalf("reply target = %+v, want the newest (not downloaded) message", ra.currentMsg)
	}
	for _, key := range []rune{'r', 'R'} {
		ra.uiApp.StatusBar.Info("")
		sendRune(ra, key)
		got := ra.uiApp.StatusBar.FlashText()
		if !strings.Contains(got, "not downloaded") || strings.Contains(got, "editor") {
			t.Errorf("%c on an undownloaded message: status = %q, want a refusal", key, got)
		}
	}
	// A downloaded message of the same conversation can be replied to.
	ra.conv.cursor = 0
	ra.conv.expanded[0] = true
	ra.currentMsg = ra.conv.target()
	ra.uiApp.StatusBar.Info("")
	sendRune(ra, 'r')
	if got := ra.uiApp.StatusBar.FlashText(); strings.Contains(got, "not downloaded") {
		t.Errorf("reply to a downloaded message refused: %q", got)
	}
}

func TestConversation_hasFiles(t *testing.T) {
	file := &mail.Message{Envelope: &mail.Envelope{ID: "1"}, Attachments: []*mail.Attachment{{Filename: "a.pdf"}}}
	plain := &mail.Message{Envelope: &mail.Envelope{ID: "2"}, TextBody: "x"}
	if has, known := newConversation([]*mail.Message{plain, file}, false).hasFiles(); !has || !known {
		t.Errorf("with a file: has %v known %v, want true true", has, known)
	}
	if has, known := newConversation([]*mail.Message{plain}, false).hasFiles(); has || !known {
		t.Errorf("all downloaded, no files: has %v known %v, want false true", has, known)
	}
	off := newConversation([]*mail.Message{plain}, true)
	off.missing = map[*mail.Message]bool{plain: true}
	if _, known := off.hasFiles(); known {
		t.Error("a missing body was taken as a message without files")
	}
}

// unreadIMAPApp is a wired IMAP app showing peopleEnvelopes (all unread)
// with the mail list focused.
func unreadIMAPApp(t *testing.T) (*RootApp, *stubAccount) {
	t.Helper()
	ra := newWiredRootApp(t, "test")
	stub := &stubAccount{rows: peopleEnvelopes()}
	ra.engines["test"] = stub
	recordBodyLoads(ra)
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX")})
	ra.uiApp.SetFocus(ra.maillist)
	ra.showEnvelopes(onScreen(stub.rows))
	return ra, stub
}

func TestMovingOntoMessageDropsNewMailSignAtOnce(t *testing.T) {
	ra, _ := unreadIMAPApp(t)
	ra.maillist.Select(1, 0)
	if ra.selectedEnv.IsUnread() {
		t.Fatal("the message moved onto is still unread locally")
	}
	if cell := ra.maillist.GetCell(1, 0).Text; strings.HasPrefix(cell, "●") {
		t.Errorf("row 1 = %q, the new-mail sign must go at once", cell)
	}
	if cell := ra.maillist.GetCell(2, 0).Text; !strings.HasPrefix(cell, "●") {
		t.Errorf("row 2 = %q, a message not moved onto keeps its sign", cell)
	}
}

func TestUMarksMessageUnread(t *testing.T) {
	ra, stub := unreadIMAPApp(t)
	ra.maillist.Select(1, 0) // read now
	if ev := sendRune(ra, 'u'); ev != nil {
		t.Fatal("u not consumed on a message row")
	}
	if !ra.selectedEnv.IsUnread() {
		t.Error("u left the message read")
	}
	if cell := ra.maillist.GetCell(1, 0).Text; !strings.HasPrefix(cell, "●") {
		t.Errorf("row 1 = %q, u must bring the new-mail sign back", cell)
	}
	stub.waitCall(t, "SetFlags 2")
}
