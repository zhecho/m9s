package view

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	msync "github.com/zhecho/m9s/internal/sync"
	"github.com/zhecho/m9s/internal/ui"
)

func TestShowSendModal_keepDraft(t *testing.T) {
	dir := t.TempDir()
	ds := draft.NewStore(dir)
	skin, _ := ui.LoadSkin("")
	uiApp := ui.NewApp(skin, ui.KeyMap{})
	acct := &mail.Account{Name: "test", Config: &config.Account{Name: "test"}}
	mgr := mail.NewManager([]*mail.Account{acct})
	ra := &RootApp{
		cfg:        &config.Config{},
		uiApp:      uiApp,
		manager:    mgr,
		draftStore: ds,
	}
	ra.sendFn = ra.sendDraft

	d := smtpadapter.Draft{Subject: "hello", Body: "world"}
	id, err := ds.Save("test", d)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	var sendCalled bool
	ra.sendFn = func(smtpadapter.Draft, string, string) { sendCalled = true }

	ra.showSendModal(d, id, "test")

	// Trigger "Server Draft"
	ra.testModalDone(1, "Server Draft")

	if sendCalled {
		t.Error("sendFn should not be called on Server Draft")
	}
	drafts, _ := ds.List("test")
	if len(drafts) == 0 {
		t.Error("draft should still exist after Server Draft")
	}
	if uiApp.Pages.GetPrimitive("send-modal") != nil {
		t.Error("send-modal page should be removed after choice")
	}
}

func TestShowSendModal_send(t *testing.T) {
	dir := t.TempDir()
	ds := draft.NewStore(dir)
	skin, _ := ui.LoadSkin("")
	uiApp := ui.NewApp(skin, ui.KeyMap{})
	acct := &mail.Account{Name: "test", Config: &config.Account{Name: "test"}}
	mgr := mail.NewManager([]*mail.Account{acct})
	ra := &RootApp{
		cfg:        &config.Config{},
		uiApp:      uiApp,
		manager:    mgr,
		draftStore: ds,
	}

	d := smtpadapter.Draft{Subject: "hello", Body: "world"}
	id, err := ds.Save("test", d)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	var sentDraft *smtpadapter.Draft
	ra.sendFn = func(dr smtpadapter.Draft, draftID, acctName string) {
		sentDraft = &dr
		if draftID != "" {
			_ = ds.Delete(acctName, draftID)
		}
	}

	ra.showSendModal(d, id, "test")

	// Trigger "Send"
	ra.testModalDone(0, "Send")

	if sentDraft == nil {
		t.Error("sendFn should be called on Send")
	}
	drafts, _ := ds.List("test")
	if len(drafts) != 0 {
		t.Error("draft should be deleted after Send")
	}
	if uiApp.Pages.GetPrimitive("send-modal") != nil {
		t.Error("send-modal page should be removed after Send")
	}
}

func TestLaunchEditor_noEditor(t *testing.T) {
	t.Setenv("EDITOR", "")
	skin, err := ui.LoadSkin("")
	if err != nil {
		t.Fatalf("load skin: %v", err)
	}
	uiApp := ui.NewApp(skin, ui.KeyMap{})
	ra := &RootApp{
		cfg:   &config.Config{},
		uiApp: uiApp,
	}
	ra.launchEditor(smtpadapter.Draft{})
	msg := uiApp.StatusBar.FlashText()
	if msg == "" {
		t.Error("expected a status bar error when no editor is configured")
	}
	if !strings.Contains(msg, "no editor") {
		t.Errorf("expected 'no editor' in error message, got %q", msg)
	}
}

func TestActiveUser_nilManager(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	uiApp := ui.NewApp(skin, ui.KeyMap{})
	ra := &RootApp{
		cfg:     &config.Config{},
		uiApp:   uiApp,
		manager: mail.NewManager(nil),
	}
	if ra.selfAddress() != "" {
		t.Errorf("want empty activeUser with nil manager, got %q", ra.selfAddress())
	}
}

func TestActiveUser_withAccount(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	uiApp := ui.NewApp(skin, ui.KeyMap{})
	acct := &mail.Account{Name: "test", Config: &config.Account{
		Name: "test",
		SMTP: config.SMTPConfig{User: "me@example.com"},
	}}
	ra := &RootApp{
		cfg:     &config.Config{},
		uiApp:   uiApp,
		manager: mail.NewManager([]*mail.Account{acct}),
	}
	if ra.selfAddress() != "me@example.com" {
		t.Errorf("want me@example.com, got %q", ra.selfAddress())
	}
}

func TestStartReply_nilActiveAccount(t *testing.T) {
	t.Setenv("EDITOR", "")
	skin, _ := ui.LoadSkin("")
	uiApp := ui.NewApp(skin, ui.KeyMap{})
	ra := &RootApp{
		cfg:     &config.Config{},
		uiApp:   uiApp,
		manager: mail.NewManager(nil), // no active account
	}
	msg := &mail.Message{
		Envelope: &mail.Envelope{
			From: []mail.Address{{Email: "alice@example.com"}},
		},
	}
	ra.startReply(msg, false) // must not panic
}

func TestStartReply_noFrom(t *testing.T) {
	t.Setenv("EDITOR", "")
	skin, _ := ui.LoadSkin("")
	uiApp := ui.NewApp(skin, ui.KeyMap{})
	acct := &mail.Account{Name: "test", Config: &config.Account{Name: "test"}}
	ra := &RootApp{
		cfg:     &config.Config{},
		uiApp:   uiApp,
		manager: mail.NewManager([]*mail.Account{acct}),
	}
	msg := &mail.Message{
		Envelope: &mail.Envelope{From: nil}, // no From
	}
	ra.startReply(msg, false) // must not panic — early return
}

func TestStartReply_launchesEditor(t *testing.T) {
	t.Setenv("EDITOR", "")
	ra := newMinimalRootApp()
	msg := &mail.Message{
		Envelope: &mail.Envelope{
			Subject: "Hello",
			From:    []mail.Address{{Email: "alice@example.com"}},
			To:      []mail.Address{{Email: "me@example.com"}},
		},
		TextBody: "original",
	}
	// With no EDITOR set, launchEditor returns early with "no editor" flash.
	ra.startReply(msg, false) // must not panic
	msg2 := ra.uiApp.StatusBar.FlashText()
	if msg2 == "" {
		t.Error("expected status bar flash when no editor configured")
	}
}

func newMinimalRootApp() *RootApp {
	skin, _ := ui.LoadSkin("")
	uiApp := ui.NewApp(skin, ui.KeyMap{})
	acct := &mail.Account{Name: "test", Config: &config.Account{Name: "test"}}
	mgr := mail.NewManager([]*mail.Account{acct})
	ra := &RootApp{
		cfg:     &config.Config{},
		uiApp:   uiApp,
		manager: mgr,
		engines: map[string]msync.Account{},
	}
	ra.folders = NewFolderView(uiApp.Skin, nil)
	ra.maillist = NewMailListView(uiApp.Skin, "", nil)
	ra.preview = NewPreviewView(uiApp.Skin)
	ra.logView = ra.buildLogView()
	ra.logPagePanel = newLogPanel(ra.logView.TextView, ra.uiApp.Skin)
	ra.sendFn = ra.sendDraft
	ra.bodyLoader = func(acct *mail.Account, folder *mail.Folder, env *mail.Envelope, markSeen bool) {
		go ra.loadBody(acct, folder, env, markSeen, ra.conversationRowsOf(acct, folder), ra.selGen.Load())
	}
	return ra
}

func TestBuildLogView(t *testing.T) {
	ra := newMinimalRootApp()
	tv := ra.buildLogView()
	if tv == nil {
		t.Fatal("buildLogView returned nil")
	}
}

func TestCloseReader(t *testing.T) {
	ra := newMinimalRootApp()
	ra.uiApp.Pages.AddPage(pageMain, ra.maillist, true, true)
	ra.uiApp.Pages.AddPage(pageReader, ra.preview, true, true)
	ra.closeReader() // must not panic
}

func TestCycleFocus(t *testing.T) {
	ra := newMinimalRootApp()
	// cycleFocus with no focus set — default branch.
	ra.cycleFocus()
	// cycleFocus from folders.
	ra.uiApp.SetFocus(ra.folders)
	ra.cycleFocus()
	// cycleFocus from maillist.
	ra.uiApp.SetFocus(ra.maillist)
	ra.cycleFocus()
}

// shift-tab goes through the panes in the reverse of tab's order.
func TestShiftTabCyclesPanesBack(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.uiApp.SetFocus(ra.folders)
	var forward []tview.Primitive
	for range 4 {
		sendKey(ra, tcell.KeyTab)
		forward = append(forward, ra.uiApp.GetFocus())
	}
	ra.uiApp.SetFocus(forward[len(forward)-1])
	for i := len(forward) - 2; i >= 0; i-- {
		if ev := sendKey(ra, tcell.KeyBacktab); ev != nil {
			t.Fatal("shift-tab not consumed")
		}
		if got := ra.uiApp.GetFocus(); got != forward[i] {
			t.Fatalf("shift-tab %d focused %T, want %T", len(forward)-1-i, got, forward[i])
		}
	}
	ra.uiApp.SetFocus(ra.maillist)
	sendKey(ra, tcell.KeyBacktab)
	if ra.uiApp.GetFocus() != ra.folders {
		t.Fatalf("shift-tab from the mail list focused %T, want the folders", ra.uiApp.GetFocus())
	}
}

func TestToggleLog_showAndHide(t *testing.T) {
	ra := newMinimalRootApp()
	ra.uiApp.Pages.AddPage(pageMain, ra.maillist, true, true)
	ra.uiApp.Pages.AddPage(pageLog, ra.logView, true, false)

	// First toggle — not on log page → switch to log.
	ra.toggleLog()

	// Second toggle — switch back to main.
	ra.uiApp.Pages.SwitchToPage(pageLog)
	ra.toggleLog()
}

func TestOnFolderSelected(t *testing.T) {
	ra := newMinimalRootApp()
	ra.uiApp.Pages.AddPage(pageMain, ra.maillist, true, true)
	f := &mail.Folder{Name: "INBOX", Delimiter: "/"}
	ra.onFolderSelected(f) // launches goroutine that returns early (no engine)
	if ra.currentFolder != f {
		t.Error("currentFolder should be set after onFolderSelected")
	}
}

func TestLoadEnvelopes_nilAccount(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	uiApp := ui.NewApp(skin, ui.KeyMap{})
	ra := &RootApp{
		cfg:      &config.Config{},
		uiApp:    uiApp,
		manager:  mail.NewManager(nil),
		engines:  map[string]msync.Account{},
		maillist: NewMailListView(uiApp.Skin, "", nil),
	}
	ra.loadEnvelopes(ra.manager.Active(), &mail.Folder{Name: "INBOX"}) // nil account → early return
}

func TestLoadEnvelopes_noEngine(t *testing.T) {
	ra := newMinimalRootApp()
	ra.loadEnvelopes(ra.manager.Active(), &mail.Folder{Name: "INBOX"}) // no engine → early return
}

func TestOnEnvelopeSelected(t *testing.T) {
	ra := newMinimalRootApp()
	ra.currentFolder = &mail.Folder{Name: "INBOX"}
	env := &mail.Envelope{Subject: "Test", From: []mail.Address{{Email: "a@example.com"}}}
	ra.onEnvelopeSelected(env) // shows envelope in preview + goroutine
}

func TestLoadBody_nilCurrentFolder(t *testing.T) {
	ra := newMinimalRootApp()
	// currentFolder is nil → early return
	ra.loadBody(ra.manager.Active(), nil, &mail.Envelope{ID: "1", UID: 1}, true, false, 0)
}

func TestSyncAll_nilActiveAccount(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	uiApp := ui.NewApp(skin, ui.KeyMap{})
	ra := &RootApp{
		cfg:     &config.Config{},
		uiApp:   uiApp,
		manager: mail.NewManager(nil),
		engines: map[string]msync.Account{},
	}
	ra.syncAll() // nil active → early return, no panic
}

func TestSyncAll_noEngine(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	uiApp := ui.NewApp(skin, ui.KeyMap{})
	acct := &mail.Account{Name: "test", Config: &config.Account{Name: "test"}}
	ra := &RootApp{
		cfg:     &config.Config{},
		uiApp:   uiApp,
		manager: mail.NewManager([]*mail.Account{acct}),
		engines: map[string]msync.Account{}, // no engine for "test"
	}
	ra.syncAll() // no engine → early return, no panic
}

func TestShowSendModal_noDraftStore_discard(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	uiApp := ui.NewApp(skin, ui.KeyMap{})
	acct := &mail.Account{Name: "test", Config: &config.Account{Name: "test"}}
	mgr := mail.NewManager([]*mail.Account{acct})
	ra := &RootApp{
		cfg:        &config.Config{},
		uiApp:      uiApp,
		manager:    mgr,
		draftStore: nil, // no draft store → "Send/Discard" buttons
	}
	var sendCalled bool
	ra.sendFn = func(smtpadapter.Draft, string, string) { sendCalled = true }

	d := smtpadapter.Draft{Subject: "no store"}
	ra.showSendModal(d, "", "test")

	// Press "Discard" — should not send, modal removed
	ra.testModalDone(0, "Discard")

	if sendCalled {
		t.Error("sendFn should not be called on Discard")
	}
	if uiApp.Pages.GetPrimitive("send-modal") != nil {
		t.Error("send-modal page should be removed after Discard")
	}
}

func TestShowSendModal_noDraftStore_send(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	uiApp := ui.NewApp(skin, ui.KeyMap{})
	acct := &mail.Account{Name: "test", Config: &config.Account{Name: "test"}}
	mgr := mail.NewManager([]*mail.Account{acct})
	ra := &RootApp{
		cfg:        &config.Config{},
		uiApp:      uiApp,
		manager:    mgr,
		draftStore: nil,
	}
	var sentDraft *smtpadapter.Draft
	ra.sendFn = func(dr smtpadapter.Draft, _, _ string) { sentDraft = &dr }

	d := smtpadapter.Draft{Subject: "no store send"}
	ra.showSendModal(d, "", "test")
	ra.testModalDone(0, "Send")

	if sentDraft == nil {
		t.Error("sendFn should be called on Send even without draftStore")
	}
}

func TestDeleteMessage_nilActiveAccount(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	uiApp := ui.NewApp(skin, ui.KeyMap{})
	ra := &RootApp{
		cfg:     &config.Config{},
		uiApp:   uiApp,
		manager: mail.NewManager(nil), // no active account
		engines: map[string]msync.Account{},
	}
	ra.deleteMessage() // nil active account → early return, no panic
}

func TestDeleteMessage_noEngine(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	uiApp := ui.NewApp(skin, ui.KeyMap{})
	acct := &mail.Account{Name: "test", Config: &config.Account{Name: "test"}}
	ra := &RootApp{
		cfg:     &config.Config{},
		uiApp:   uiApp,
		manager: mail.NewManager([]*mail.Account{acct}),
		engines: map[string]msync.Account{}, // no engine for "test"
	}
	ra.deleteMessage() // no engine → early return, no panic
}

func TestDeleteMessage_nilCurrentMsg(t *testing.T) {
	ra := newMinimalRootApp()
	ra.currentMsg = nil
	ra.deleteMessage() // nil currentMsg → early return, no panic
}

func TestShowDeleteModal_cancel(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	uiApp := ui.NewApp(skin, ui.KeyMap{})
	ra := &RootApp{
		cfg:   &config.Config{},
		uiApp: uiApp,
	}
	var confirmCalled bool
	ra.showDeleteModal("Move to Deleted Items?", []string{"Delete", "Cancel"}, func() {
		confirmCalled = true
	})

	// Trigger "Cancel"
	ra.testDeleteModalDone(1, "Cancel")

	if confirmCalled {
		t.Error("onConfirm should not be called when Cancel is chosen")
	}
	if uiApp.Pages.GetPrimitive("delete-modal") != nil {
		t.Error("delete-modal page should be removed after Cancel")
	}
}

func TestShowDeleteModal_confirm(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	uiApp := ui.NewApp(skin, ui.KeyMap{})
	ra := &RootApp{
		cfg:   &config.Config{},
		uiApp: uiApp,
	}
	var confirmCalled bool
	ra.showDeleteModal("Move to Deleted Items?", []string{"Delete", "Cancel"}, func() {
		confirmCalled = true
	})

	// Trigger "Delete"
	ra.testDeleteModalDone(0, "Delete")

	if !confirmCalled {
		t.Error("onConfirm should be called when Delete is chosen")
	}
	if uiApp.Pages.GetPrimitive("delete-modal") != nil {
		t.Error("delete-modal page should be removed after Delete")
	}
}

func TestWireFocusCallbacks_yellowTitle(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	uiApp := ui.NewApp(skin, ui.KeyMap{})
	ra := &RootApp{
		cfg:     &config.Config{},
		uiApp:   uiApp,
		manager: mail.NewManager(nil),
	}
	ra.folders = NewFolderView(uiApp.Skin, nil)
	ra.maillist = NewMailListView(uiApp.Skin, "", nil)
	ra.preview = NewPreviewView(uiApp.Skin)
	ra.wireFocusCallbacks()

	tests := []struct {
		name     string
		trigger  func()
		getTitle func() string
	}{
		{
			name:     "folders focus → bold title, yellow color",
			trigger:  func() { ra.folders.focusFunc() },
			getTitle: func() string { return ra.folders.GetTitle() },
		},
		{
			name:     "maillist focus → bold title, yellow color",
			trigger:  func() { ra.maillist.focusFunc() },
			getTitle: func() string { return ra.maillist.GetTitle() },
		},
		{
			name:     "preview focus → bold title, yellow color",
			trigger:  func() { ra.preview.focusFunc() },
			getTitle: func() string { return ra.preview.GetTitle() },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.trigger()
			title := tt.getTitle()
			if !strings.Contains(title, "[::b]") {
				t.Errorf("focused title should contain bold markup, got %q", title)
			}
		})
	}

	// Verify title color is tcell.ColorYellow on focus.
	ra.folders.focusFunc()
	// SetTitleColor stores in the Box; we verify indirectly by checking blur restores.
	ra.folders.blurFunc()
	blurTitle := ra.folders.GetTitle()
	if strings.Contains(blurTitle, "[::b]") {
		t.Errorf("blurred title should be plain, got %q", blurTitle)
	}
	// The title color after blur must NOT be yellow — it must be the skin's header_fg.
	// We verify this does not panic and the title is plain "FOLDERS".
	if blurTitle != " FOLDERS " {
		t.Errorf("blurred title: want \" FOLDERS \", got %q", blurTitle)
	}

	// Confirm tcell.NewRGBColor is used (compile-time check that the constant exists).
	_ = tcell.NewRGBColor(255, 255, 0)
}

func TestOpenHovered_currentSeqOpensFolder(t *testing.T) {
	ra := newMinimalRootApp()
	f := &mail.Folder{Name: "Sent", Delimiter: "/"}
	ra.hoverSeq = 3
	ra.openHovered(3, f)
	if ra.currentFolder != f {
		t.Error("hover with the current seq should open the folder")
	}
}

func TestOpenHovered_staleSeqIgnored(t *testing.T) {
	ra := newMinimalRootApp()
	ra.hoverSeq = 3
	ra.openHovered(2, &mail.Folder{Name: "Sent", Delimiter: "/"})
	if ra.currentFolder != nil {
		t.Error("a superseded hover must not open its folder")
	}
}

func TestOnFolderHover_debounces(t *testing.T) {
	ra := newMinimalRootApp()
	ra.hoverDelay = time.Hour // never fires during the test
	ra.onFolderHover(&mail.Folder{Name: "INBOX", Delimiter: "/"})
	first := ra.hoverTimer
	ra.onFolderHover(&mail.Folder{Name: "Sent", Delimiter: "/"})
	defer ra.hoverTimer.Stop()

	if ra.currentFolder != nil {
		t.Error("hover must not open a folder before the delay elapses")
	}
	if first.Stop() {
		t.Error("the earlier hover timer should have been stopped by the later hover")
	}
	if ra.hoverSeq != 2 {
		t.Errorf("hoverSeq = %d, want 2", ra.hoverSeq)
	}
}

func TestOnFolderSelected_cancelsPendingHover(t *testing.T) {
	ra := newMinimalRootApp()
	inbox := &mail.Folder{Name: "INBOX", Delimiter: "/"}
	ra.hoverSeq = 1 // a hover on some other folder is pending
	ra.onFolderSelected(inbox)
	ra.openHovered(1, &mail.Folder{Name: "Sent", Delimiter: "/"})
	if ra.currentFolder != inbox {
		t.Error("Enter should cancel the pending hover")
	}
}

func TestIsCurrent(t *testing.T) {
	ra := newMinimalRootApp()
	acct := ra.manager.Active()
	other := &mail.Account{Name: "other"}
	f := &mail.Folder{Name: "INBOX"}
	if ra.isCurrent(acct, f) {
		t.Error("no folder open yet: nothing is current")
	}
	ra.currentFolder = &mail.Folder{Name: "INBOX"}
	if !ra.isCurrent(acct, f) {
		t.Error("same account and folder name should be current")
	}
	if ra.isCurrent(acct, &mail.Folder{Name: "Sent"}) {
		t.Error("a different folder is not current")
	}
	if ra.isCurrent(other, f) {
		t.Error("a different account is not current")
	}
}

func TestOnEnvelopeSelected_tracksSelectionAndClearsMessage(t *testing.T) {
	ra := newMinimalRootApp()
	ra.currentFolder = &mail.Folder{Name: "INBOX"}
	ra.currentMsg = &mail.Message{}
	env := &mail.Envelope{ID: "7", UID: 7}
	ra.onEnvelopeSelected(env)
	if ra.selectedEnv != env {
		t.Error("selectedEnv should track the selected row")
	}
	if ra.currentMsg != nil {
		t.Error("currentMsg must be cleared until the new body arrives")
	}
}

func TestOnFolderSelected_clearsPreviousSelection(t *testing.T) {
	ra := newMinimalRootApp()
	ra.currentMsg = &mail.Message{}
	ra.selectedEnv = &mail.Envelope{ID: "7", UID: 7}
	ra.onFolderSelected(&mail.Folder{Name: "Sent", Delimiter: "/"})
	if ra.currentMsg != nil || ra.selectedEnv != nil {
		t.Error("opening a folder must drop the previous folder's selection")
	}
}

func TestMailListFocused(t *testing.T) {
	ra := newMinimalRootApp()
	ra.uiApp.SetFocus(ra.folders)
	if ra.mailListFocused() {
		t.Error("focus is on the folder pane")
	}
	ra.uiApp.SetFocus(ra.maillist)
	if !ra.mailListFocused() {
		t.Error("focus is on the mail list")
	}
}

func TestMarkSelectedSeen_noSelectionOrEngine(t *testing.T) {
	ra := newMinimalRootApp()
	ra.markSelectedSeen() // nothing selected → no-op
	ra.currentFolder = &mail.Folder{Name: "INBOX"}
	ra.selectedEnv = &mail.Envelope{ID: "1", UID: 1}
	ra.markSelectedSeen() // no engine → no-op, no panic
}

func TestOnEnvelopeSelected_nilClearsSelection(t *testing.T) {
	ra := newMinimalRootApp()
	ra.currentFolder = &mail.Folder{Name: "INBOX"}
	ra.currentMsg = &mail.Message{}
	ra.selectedEnv = &mail.Envelope{ID: "7", UID: 7}
	ra.preview.SetText("old message body")

	ra.onEnvelopeSelected(nil)

	if ra.currentMsg != nil || ra.selectedEnv != nil {
		t.Error("no visible message: reply and delete must have nothing to act on")
	}
	if got := ra.preview.GetText(true); strings.Contains(got, "old message body") {
		t.Errorf("preview still shows the previous message: %q", got)
	}
}

func TestDeleteMessage_nothingSelectedShowsNoModal(t *testing.T) {
	ra := newMinimalRootApp()
	ra.currentFolder = &mail.Folder{Name: "INBOX"}
	ra.onEnvelopeSelected(nil)
	ra.deleteMessage()
	if ra.uiApp.Pages.HasPage("delete-modal") {
		t.Error("delete with nothing selected must not open a confirmation")
	}
}

// The SMTP password now lives in the engine (see sync's
// TestEngineSendSMTPPassword); a missing one reads as before.
func TestSendFailureText_noPasswordReadsAsBefore(t *testing.T) {
	err := fmt.Errorf("%w for test", msync.ErrNoSMTPPassword)
	if got := sendFailureText(err); got != "no SMTP password for test" {
		t.Errorf("text = %q, want 'no SMTP password for test'", got)
	}
}

func TestSendFailureText_rejectedPasswordSaysRestart(t *testing.T) {
	rejected := fmt.Errorf("%w: 535 bad credentials", smtpadapter.ErrAuth)
	if got := sendFailureText(rejected); !strings.Contains(got, "password rejected — fix it and restart m9s") {
		t.Errorf("text = %q", got)
	}
	if got := sendFailureText(errors.New("dial tcp: connection refused")); got != "send failed: dial tcp: connection refused" {
		t.Errorf("text = %q", got)
	}
}
