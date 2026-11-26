package view

import (
	"errors"
	"github.com/zhecho/m9s/internal/version"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/mail"
	msync "github.com/zhecho/m9s/internal/sync"
	"github.com/zhecho/m9s/internal/ui"
)

// newWiredRootApp builds a full RootApp through NewRootApp, with no engines:
// every network path returns early, so tests drive it with events and keys.
func newWiredRootApp(t *testing.T, accountNames ...string) *RootApp {
	t.Helper()
	skin, _ := ui.LoadSkin("")
	km, _ := ui.LoadKeyMap("")
	uiApp := ui.NewApp(skin, km)
	var accounts []*mail.Account
	for _, name := range accountNames {
		accounts = append(accounts, &mail.Account{Name: name, Config: &config.Account{
			Name: name,
			IMAP: config.IMAPConfig{User: name + "@x.io"},
		}})
	}
	return NewRootApp(uiApp, noLogPane(), mail.NewManager(accounts),
		map[string]msync.Account{}, nil, make(chan msync.Event))
}

// noLogPane is a config without the log pane: the tests of the other panes
// lay out the screen as before it existed (logpane_test.go tests the pane).
func noLogPane() *config.Config {
	off := false
	cfg := &config.Config{}
	cfg.UI.LogPane = &off
	return cfg
}

func testFolders(names ...string) []*mail.Folder {
	var out []*mail.Folder
	for _, n := range names {
		out = append(out, &mail.Folder{Name: n, Delimiter: "/"})
	}
	return out
}

func infoLine(ra *RootApp, i int) string {
	return strings.Split(ra.uiApp.Header.InfoText(), "\n")[i]
}

func sendRune(ra *RootApp, r rune) *tcell.EventKey {
	return ra.uiApp.GetInputCapture()(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
}

func sendKey(ra *RootApp, k tcell.Key) *tcell.EventKey {
	return ra.uiApp.GetInputCapture()(tcell.NewEventKey(k, 0, tcell.ModNone))
}

var errRefusedDial = errors.New("imap dial h:993: dial tcp 1.2.3.4:993: connect: connection refused")

func TestStartup_headerNeverBlank(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	if got := infoLine(ra, 0); !strings.Contains(got, "test") {
		t.Errorf("account line = %q", got)
	}
	if got := infoLine(ra, 1); !strings.Contains(got, "test@x.io") {
		t.Errorf("user line = %q", got)
	}
	if got := infoLine(ra, 4); !strings.Contains(got, "… connecting") {
		t.Errorf("status line before any event = %q, want connecting", got)
	}
	if got := ra.uiApp.StatusBar.CrumbsText(); got != "<test>" {
		t.Errorf("crumbs = %q, want <test>", got)
	}
	if !strings.Contains(ra.uiApp.Header.HintsText(), "Compose") {
		t.Errorf("hints missing at startup:\n%s", ra.uiApp.Header.HintsText())
	}
}

func TestFoldersUpdated_opensInboxAndShowsCounts(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX", "Sent")})
	ra.handleEvent(msync.BadgeUpdated{AccountName: "test", FolderName: "INBOX", Unseen: 3, Total: 128})

	if ra.currentFolder == nil || ra.currentFolder.Name != "INBOX" {
		t.Fatalf("currentFolder = %+v, want INBOX opened at startup", ra.currentFolder)
	}
	if got := infoLine(ra, 2); !strings.Contains(got, "INBOX") {
		t.Errorf("folder line = %q", got)
	}
	if got := infoLine(ra, 3); !strings.Contains(got, "3 of 128") {
		t.Errorf("unread line = %q, want 3 of 128", got)
	}
	if got := ra.uiApp.StatusBar.CrumbsText(); got != "<test> <INBOX>" {
		t.Errorf("crumbs = %q", got)
	}
}

func TestBadgeForOtherFolderDoesNotChangeHeader(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX", "Sent")})
	ra.handleEvent(msync.BadgeUpdated{AccountName: "test", FolderName: "INBOX", Unseen: 3, Total: 128})
	ra.handleEvent(msync.BadgeUpdated{AccountName: "test", FolderName: "Sent", Unseen: 9, Total: 9})
	if got := infoLine(ra, 3); !strings.Contains(got, "3 of 128") {
		t.Errorf("unread line = %q; a badge for Sent must not replace INBOX's count", got)
	}
}

func TestRefreshHeader_noFolder(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	for _, i := range []int{2, 3} {
		if got := strings.TrimRight(infoLine(ra, i), " "); !strings.HasSuffix(got, " -") {
			t.Errorf("info line %d = %q, want it to end in -", i, got)
		}
	}
}

func TestHandleStatus_firstFailureFlashesAndShows(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.handleEvent(msync.AccountStatus{AccountName: "test", State: msync.StateFailed,
		Err: errRefusedDial, Attempt: 1, RetryIn: 2 * time.Second})

	flash := ra.uiApp.StatusBar.FlashText()
	if !strings.Contains(flash, "test:") || !strings.Contains(flash, "connection refused") {
		t.Errorf("flash = %q, want the account name and the full error", flash)
	}
	if got := infoLine(ra, 4); !strings.Contains(got, "✗ connection refused · retry in 2s") {
		t.Errorf("status line = %q", got)
	}
}

func TestHandleStatus_laterFailuresDoNotFlash(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.handleEvent(msync.AccountStatus{AccountName: "test", State: msync.StateFailed,
		Err: errRefusedDial, Attempt: 2, RetryIn: 4 * time.Second})
	if flash := ra.uiApp.StatusBar.FlashText(); flash != "" {
		t.Errorf("flash on a repeat failure = %q, want none", flash)
	}
	if got := infoLine(ra, 4); !strings.Contains(got, "retry in 4s") {
		t.Errorf("status line = %q; the header must still update", got)
	}
}

func TestHandleStatus_connectedAndRecovered(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.handleEvent(msync.AccountStatus{AccountName: "test", State: msync.StateConnected})
	if flash := ra.uiApp.StatusBar.FlashText(); flash != "" {
		t.Errorf("first connect flashed %q, want nothing", flash)
	}
	if got := infoLine(ra, 4); !strings.Contains(got, "● connected") {
		t.Errorf("status line = %q", got)
	}

	ra.handleEvent(msync.AccountStatus{AccountName: "test", State: msync.StateConnected, Recovered: true})
	if flash := ra.uiApp.StatusBar.FlashText(); !strings.Contains(flash, "test: reconnected") {
		t.Errorf("flash after recovery = %q", flash)
	}
}

func TestHandleStatus_inactiveAccount(t *testing.T) {
	ra := newWiredRootApp(t, "a", "b")
	before := ra.uiApp.Header.InfoText()
	ra.handleEvent(msync.AccountStatus{AccountName: "b", State: msync.StateFailed,
		Err: errRefusedDial, Attempt: 1, RetryIn: 2 * time.Second})

	if ra.uiApp.Header.InfoText() != before {
		t.Errorf("header changed for an inactive account's status:\n%s", ra.uiApp.Header.InfoText())
	}
	if flash := ra.uiApp.StatusBar.FlashText(); !strings.Contains(flash, "b:") {
		t.Errorf("flash = %q; every account's first failure is reported", flash)
	}
}

func TestHandleStatus_everyEventIsLogged(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	before := len(ui.AppLog.Entries())
	ra.handleEvent(msync.AccountStatus{AccountName: "test", State: msync.StateFailed,
		Err: errRefusedDial, Attempt: 5, RetryIn: time.Minute})
	entries := ui.AppLog.Entries()
	if len(entries) <= before {
		t.Fatal("a repeat failure must still be logged")
	}
	if lv := entries[len(entries)-1].Level; lv != ui.LevelError {
		t.Errorf("a connection failure logged at %v, want ERROR", lv)
	}
	last := entries[len(entries)-1].Msg
	if !strings.Contains(last, "test") || !strings.Contains(last, "connection refused") || !strings.Contains(last, "5") {
		t.Errorf("log entry = %q, want account, attempt and error", last)
	}
}

func TestSwitchAccount_clearsOldAccountState(t *testing.T) {
	ra := newWiredRootApp(t, "a", "b")
	ra.handleEvent(msync.FoldersUpdated{AccountName: "a", Folders: testFolders("INBOX", "Sent")})
	ra.handleEvent(msync.FoldersUpdated{AccountName: "b", Folders: testFolders("INBOX", "Work")})
	ra.openFolderByName("Sent")
	ra.maillist.SetEnvelopes(peopleEnvelopes())
	ra.maillist.SetFilter("bob")
	ra.currentMsg = &mail.Message{}

	ra.switchAccount("b")

	if ra.manager.Active().Name != "b" || ra.folders.ActiveAccountName() != "b" {
		t.Fatalf("active: manager %q, folder pane %q; want b and b", ra.manager.Active().Name, ra.folders.ActiveAccountName())
	}
	if ra.currentMsg != nil || ra.selectedEnv != nil {
		t.Error("a message of the old account is still selected")
	}
	if ra.maillist.VisibleCount() != 0 || ra.maillist.FilterQuery() != "" {
		t.Errorf("old account's list still shown: %d rows, filter %q", ra.maillist.VisibleCount(), ra.maillist.FilterQuery())
	}
	if ra.currentFolder == nil || ra.currentFolder.Name != "INBOX" {
		t.Errorf("currentFolder = %+v, want b's INBOX", ra.currentFolder)
	}
	if got := infoLine(ra, 0); !strings.Contains(got, "b") {
		t.Errorf("account line = %q", got)
	}
	if got := ra.uiApp.StatusBar.CrumbsText(); got != "<b> <INBOX>" {
		t.Errorf("crumbs = %q", got)
	}
}

func TestSwitchAccount_sameAccountIsNoop(t *testing.T) {
	ra := newWiredRootApp(t, "a", "b")
	ra.handleEvent(msync.FoldersUpdated{AccountName: "a", Folders: testFolders("INBOX", "Sent")})
	ra.openFolderByName("Sent")
	ra.switchAccount("a")
	if ra.currentFolder == nil || ra.currentFolder.Name != "Sent" {
		t.Errorf("switching to the active account reset the open folder: %+v", ra.currentFolder)
	}
}

func TestSwitchAccount_toFailedAccountExplains(t *testing.T) {
	ra := newWiredRootApp(t, "a", "b")
	ra.handleEvent(msync.FoldersUpdated{AccountName: "a", Folders: testFolders("INBOX")})
	ra.handleEvent(msync.AccountStatus{AccountName: "b", State: msync.StateFailed,
		Err: errRefusedDial, Attempt: 1, RetryIn: 2 * time.Second})

	ra.switchAccount("b")

	if ra.currentFolder != nil {
		t.Errorf("currentFolder = %+v, want none: b has no folders", ra.currentFolder)
	}
	if got := strings.TrimRight(infoLine(ra, 2), " "); !strings.HasSuffix(got, " -") {
		t.Errorf("folder line = %q, want -", got)
	}
	if got := infoLine(ra, 4); !strings.Contains(got, "✗ connection refused") {
		t.Errorf("status line = %q; the screen must say why it is empty", got)
	}
}

func TestFoldersUpdated_forInactiveAccountDoesNotOpenAnything(t *testing.T) {
	ra := newWiredRootApp(t, "a", "b")
	ra.handleEvent(msync.FoldersUpdated{AccountName: "b", Folders: testFolders("INBOX")})
	if ra.currentFolder != nil {
		t.Errorf("a folder of an inactive account was opened: %+v", ra.currentFolder)
	}
}

func TestFoldersUpdated_openFolderGone(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX", "Old")})
	ra.openFolderByName("Old")

	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX")})

	if ra.currentFolder == nil || ra.currentFolder.Name != "INBOX" {
		t.Errorf("currentFolder = %+v, want a fall back to INBOX", ra.currentFolder)
	}
}

func TestFoldersUpdated_allFoldersGone(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX")})
	ra.maillist.SetEnvelopes(peopleEnvelopes())

	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: nil})

	if ra.currentFolder != nil || ra.selectedEnv != nil || ra.maillist.VisibleCount() != 0 {
		t.Errorf("stale state after every folder vanished: folder %+v, selected %+v, rows %d",
			ra.currentFolder, ra.selectedEnv, ra.maillist.VisibleCount())
	}
}

func TestFoldersUpdated_keepsOpenFolderCursorAndFilter(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX", "Sent")})
	ra.openFolderByName("Sent")
	ra.maillist.SetEnvelopes(peopleEnvelopes())
	ra.maillist.SetFilter("bob")

	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX", "Sent")})

	if ra.currentFolder == nil || ra.currentFolder.Name != "Sent" {
		t.Errorf("currentFolder = %+v, want Sent kept across a reconnect", ra.currentFolder)
	}
	if ra.maillist.FilterQuery() != "bob" {
		t.Errorf("filter = %q, want bob kept", ra.maillist.FilterQuery())
	}
	if node := ra.folders.GetCurrentNode(); node == nil || node != ra.folders.NodeForFolder("Sent") {
		t.Error("folder cursor moved off Sent")
	}
}

func TestOpeningAnotherFolderDropsFilter(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX", "Sent")})
	ra.maillist.SetEnvelopes(peopleEnvelopes())
	ra.maillist.SetFilter("bob")

	ra.openFolderByName("Sent")

	if ra.maillist.FilterQuery() != "" {
		t.Errorf("filter = %q after opening another folder, want it dropped", ra.maillist.FilterQuery())
	}
	if got := ra.uiApp.StatusBar.CrumbsText(); got != "<test> <Sent>" {
		t.Errorf("crumbs = %q", got)
	}
}

func TestOpenFolderByName_unknown(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX")})
	if ra.openFolderByName("nope") {
		t.Error("openFolderByName(nope) = true")
	}
	if ra.currentFolder.Name != "INBOX" {
		t.Errorf("a failed open changed the folder to %q", ra.currentFolder.Name)
	}
}

func TestBracketKeysSwitchAccount(t *testing.T) {
	ra := newWiredRootApp(t, "a", "b")
	for _, pane := range []struct {
		name  string
		focus func()
	}{
		{"folders", func() { ra.uiApp.SetFocus(ra.folders) }},
		{"mail list", func() { ra.uiApp.SetFocus(ra.maillist) }},
		{"preview", func() { ra.uiApp.SetFocus(ra.preview) }},
	} {
		ra.switchAccount("a")
		pane.focus()
		if ev := sendRune(ra, ']'); ev != nil {
			t.Errorf("%s: ] was not consumed", pane.name)
		}
		if ra.manager.Active().Name != "b" {
			t.Errorf("%s: ] did not switch to the next account", pane.name)
		}
		sendRune(ra, '[')
		if ra.manager.Active().Name != "a" {
			t.Errorf("%s: [ did not switch back", pane.name)
		}
	}
}

func TestFocusChangesPaneHints(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.uiApp.SetFocus(ra.folders)
	hints := ra.uiApp.Header.HintsText()
	if !strings.Contains(hints, "<enter>") || strings.Contains(hints, "Reply") {
		t.Errorf("folder pane hints wrong:\n%s", hints)
	}
	ra.uiApp.SetFocus(ra.maillist)
	hints = ra.uiApp.Header.HintsText()
	if !strings.Contains(hints, "Reply") || !strings.Contains(hints, "Delete") || strings.Contains(hints, "<enter>") {
		t.Errorf("mail list hints wrong:\n%s", hints)
	}
}

func TestSyncOnFailedAccountDoesNotPretendToSync(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.handleEvent(msync.AccountStatus{AccountName: "test", State: msync.StateFailed, Err: errRefusedDial, Attempt: 2})
	sendRune(ra, 'S') // no engine in this test: must not panic and must not claim a sync
	if flash := ra.uiApp.StatusBar.FlashText(); strings.Contains(flash, "Sync") {
		t.Errorf("flash = %q on a failed account", flash)
	}
}

func drawMainPage(t *testing.T, ra *RootApp, w, h int) []string {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(w, h)
	ra.uiApp.Pages.SetRect(0, 0, w, h)
	ra.uiApp.Pages.Draw(screen)
	screen.Show()
	cells, cw, ch := screen.GetContents()
	lines := make([]string, ch)
	for y := 0; y < ch; y++ {
		var b strings.Builder
		for x := 0; x < cw; x++ {
			if r := cells[y*cw+x].Runes; len(r) > 0 {
				b.WriteRune(r[0])
			} else {
				b.WriteRune(' ')
			}
		}
		lines[y] = b.String()
	}
	return lines
}

func TestLayout_noAccountTabRowBetweenHeaderAndPanes(t *testing.T) {
	ra := newWiredRootApp(t, "alpha", "beta")
	ra.handleEvent(msync.FoldersUpdated{AccountName: "alpha", Folders: testFolders("INBOX")})
	lines := drawMainPage(t, ra, 120, 30)

	below := lines[ui.HeaderHeight]
	if !strings.Contains(below, "FOLDERS") || !strings.Contains(below, "┌") {
		t.Errorf("the line under the top bar must be the FOLDERS box border, got %q", below)
	}
	for i, l := range lines[ui.HeaderHeight:] {
		if strings.Contains(l, "beta") {
			t.Errorf("line %d lists the inactive account %q; there must be no account tab row", ui.HeaderHeight+i, l)
		}
	}
}

func mailListHeight(t *testing.T, ra *RootApp) int {
	t.Helper()
	drawMainPage(t, ra, 120, 40)
	_, _, _, h := ra.maillist.GetRect()
	return h
}

func TestLayout_mailListShowsTenRowsByDefault(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	if h := mailListHeight(t, ra); h != 12 {
		t.Errorf("mail list is %d lines tall, want 12: 10 rows plus its border", h)
	}
}

func TestLayout_configuredRows(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	km, _ := ui.LoadKeyMap("")
	cfg := &config.Config{}
	cfg.UI.MailListRows = 6
	acct := &mail.Account{Name: "test", Config: &config.Account{Name: "test"}}
	ra := NewRootApp(ui.NewApp(skin, km), cfg, mail.NewManager([]*mail.Account{acct}),
		map[string]msync.Account{}, nil, make(chan msync.Event))
	if h := mailListHeight(t, ra); h != 8 {
		t.Errorf("mail list is %d lines tall, want 8 for mail_list_rows: 6", h)
	}
}

func TestLayout_savedSizeWinsOverConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "layout.json")
	if err := config.SaveLayoutState(path, config.LayoutState{MailListRows: 4}); err != nil {
		t.Fatal(err)
	}
	ra := newWiredRootApp(t, "test")
	ra.SetLayoutStatePath(path)
	if h := mailListHeight(t, ra); h != 6 {
		t.Errorf("mail list is %d lines tall, want 6: the size left last time", h)
	}
}

func TestLayout_dragWithTheMouseIsRemembered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "layout.json")
	ra := newWiredRootApp(t, "test")
	ra.SetLayoutStatePath(path)
	drawMainPage(t, ra, 120, 40)
	x, y, _, h := ra.maillist.GetRect()
	borderY := y + h - 1 // the mail list's bottom border

	send := func(handler func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive),
		action tview.MouseAction, row int) tview.Primitive {
		_, capture := handler(action, tcell.NewEventMouse(x+10, row, tcell.ButtonNone, tcell.ModNone), func(tview.Primitive) {})
		return capture
	}
	capture := send(ra.uiApp.Pages.MouseHandler(), tview.MouseLeftDown, borderY)
	if capture == nil {
		t.Fatal("pressing on the border between mail list and preview did not start a drag")
	}
	send(capture.MouseHandler(), tview.MouseMove, borderY+5)
	send(capture.MouseHandler(), tview.MouseLeftUp, borderY+5)

	if h := mailListHeight(t, ra); h != 17 {
		t.Errorf("mail list is %d lines tall after dragging 5 lines down, want 17", h)
	}
	if st := config.LoadLayoutState(path); st.MailListRows != 15 {
		t.Errorf("saved layout = %+v, want MailListRows 15 for next time", st)
	}
}

// TestKeys_jkMoveInTheMailListThroughTheRealLayout sends keys the way tview
// does: to the root, which passes them down to the focused pane.
func TestKeys_jkMoveInTheMailListThroughTheRealLayout(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX")})
	ra.maillist.SetEnvelopes(peopleEnvelopes())
	ra.uiApp.SetFocus(ra.maillist)
	drawMainPage(t, ra, 120, 40)

	press := func(r rune) {
		ev := tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone)
		if ra.uiApp.GetInputCapture()(ev) == nil {
			return // consumed by a global shortcut
		}
		ra.uiApp.Pages.InputHandler()(ev, func(p tview.Primitive) { ra.uiApp.SetFocus(p) })
	}

	press('j')
	press('j')
	if row, _ := ra.maillist.GetSelection(); row != 2 {
		t.Errorf("after j j the mail list is on row %d, want 2", row)
	}
	press('k')
	if row, _ := ra.maillist.GetSelection(); row != 1 {
		t.Errorf("after k the mail list is on row %d, want 1", row)
	}
	if ra.selectedEnv == nil || ra.selectedEnv.ID != "2" {
		t.Errorf("selected message = %+v, want Bob's (ID 2) previewed", ra.selectedEnv)
	}
}

func TestKeys_jkStillMoveInTheFolderPane(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX", "Sent")})
	ra.folders.SelectFolder("INBOX")
	ra.uiApp.SetFocus(ra.folders)
	drawMainPage(t, ra, 120, 40)

	ra.uiApp.Pages.InputHandler()(tcell.NewEventKey(tcell.KeyRune, 'j', tcell.ModNone), func(p tview.Primitive) {})
	drawMainPage(t, ra, 120, 40) // the tree applies the move when drawn
	if ra.folders.GetCurrentNode() != ra.folders.NodeForFolder("Sent") {
		t.Error("j in the folder pane did not move to the next folder")
	}
}

func TestHeader_showsProviderAndVersion(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.refreshHeader()
	got := infoLine(ra, 5)
	if !strings.Contains(got, "Provider:") || !strings.Contains(got, "imap / "+version.String()) {
		t.Errorf("provider row = %q, want it to contain %q", got, "imap / "+version.String())
	}
}

func TestHeader_showsRemoteServerOnceKnown(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.refreshHeader()
	if got := infoLine(ra, 6); !strings.Contains(got, "RSRV:") || !strings.Contains(got, "-") {
		t.Errorf("RSRV row before the server answered = %q, want \"RSRV: -\"", got)
	}
	ra.handleEvent(msync.ServerInfo{AccountName: "test", Text: "Dovecot / 2.3.21"})
	if got := infoLine(ra, 6); !strings.Contains(got, "Dovecot / 2.3.21") {
		t.Errorf("RSRV row = %q, want it to contain %q", got, "Dovecot / 2.3.21")
	}
	ra.handleEvent(msync.ServerInfo{AccountName: "other", Text: "GImap / -"})
	if got := infoLine(ra, 6); strings.Contains(got, "GImap") {
		t.Errorf("an inactive account's server changed the header: %q", got)
	}
}

func TestLogView_rowsAreFormattedEntries(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ui.AppLog.Warn("[test] marker %d", 42)
	es := ui.AppLog.Entries()
	ra.toggleLog()
	want := ui.FormatEntry(es[len(es)-1], ra.uiApp.Skin)
	if got := ra.logView.GetText(false); !strings.Contains(got, want) {
		t.Errorf("log view lacks %q:\n%s", want, got)
	}
}

func TestNoLevelPrefixInMessages(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.handleEvent(msync.AccountStatus{AccountName: "test", State: msync.StateConnecting})
	ra.handleEvent(msync.AccountStatus{AccountName: "test", State: msync.StateFailed, Err: errRefusedDial, Attempt: 2, RetryIn: time.Second})
	ra.uiApp.StatusBar.Info("hi")
	for _, e := range ui.AppLog.Entries() {
		for _, p := range []string{"INFO ", "WARN ", "ERROR "} {
			if strings.HasPrefix(e.Msg, p) {
				t.Errorf("message keeps its level text: %q", e.Msg)
			}
		}
	}
}

// TestLogView_wrapKeepsBrackets draws a long row at every width so a wrap
// lands inside each bracket at least once; the text must survive intact.
// Brackets shaped like a real colour tag ([::b]) are left out: tview's word
// wrap garbles their escape, a tview bug m9s does not work around.
func TestLogView_wrapKeepsBrackets(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	msg := `[my-account] [delete] "[PATCH v2] fix" dial tcp [::1]:993 [a.b.c] [AUTH] refused`
	ui.AppLog.Error("%s", msg)
	renderLog(ra.logView.TextView, ra.uiApp.Skin)
	squash := func(s string) string { return strings.Join(strings.Fields(s), "") }
	for w := 30; w <= 110; w++ {
		screen := tcell.NewSimulationScreen("UTF-8")
		if err := screen.Init(); err != nil {
			t.Fatal(err)
		}
		screen.SetSize(w, 40)
		ra.logView.SetRect(0, 0, w, 40)
		ra.logView.ScrollToEnd()
		ra.logView.Draw(screen)
		screen.Show()
		cells, sw, sh := screen.GetContents()
		var b strings.Builder
		for y := 1; y < sh-1; y++ { // inside the border
			for x := 1; x < sw-1; x++ {
				if r := cells[y*sw+x].Runes; len(r) > 0 {
					b.WriteRune(r[0])
				} else {
					b.WriteRune(' ')
				}
			}
		}
		if !strings.Contains(squash(b.String()), squash(msg)) {
			t.Errorf("width %d: drawn text lost %q:\n%s", w, msg, b.String())
			return
		}
	}
}

// click sends a left click at the centre of p through the main page, as
// tview does: down, up, click, focusing whatever the panes ask for.
func click(ra *RootApp, p tview.Primitive) {
	x, y, w, h := p.GetRect()
	ev := tcell.NewEventMouse(x+w/2, y+h/2, tcell.ButtonPrimary, tcell.ModNone)
	handler := ra.uiApp.Pages.MouseHandler()
	for _, action := range []tview.MouseAction{tview.MouseLeftDown, tview.MouseLeftUp, tview.MouseLeftClick} {
		handler(action, ev, func(p tview.Primitive) { ra.uiApp.SetFocus(p) })
	}
}

// A click focuses the pane itself, not the tview widget inside it: its
// title turns yellow and bold, and Tab goes on from it to the next pane.
func TestClickFocusesPane(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX", "Work")})
	drawMainPage(t, ra, 120, 40)

	panes := []struct {
		name  string
		p     tview.Primitive
		title func() string
		next  tview.Primitive
	}{
		{"maillist", ra.maillist, ra.maillist.GetTitle, ra.preview},
		{"preview", ra.preview, ra.preview.GetTitle, ra.folders},
		{"folders", ra.folders, ra.folders.GetTitle, ra.maillist},
	}
	for _, pane := range panes {
		click(ra, pane.p)
		if got := ra.uiApp.GetFocus(); got != pane.p {
			t.Fatalf("click on %s focused %T, want the pane", pane.name, got)
		}
		if !strings.Contains(pane.title(), "[::b]") {
			t.Errorf("%s title after click = %q, want it highlighted", pane.name, pane.title())
		}
		sendKey(ra, tcell.KeyTab)
		if got := ra.uiApp.GetFocus(); got != pane.next {
			t.Errorf("Tab after clicking %s focused %T", pane.name, got)
		}
	}
}

// q on the log page closes the log, like the title says, instead of quitting
// m9s; focus goes back to the pane that had it. Keys meant for the main page
// (d deletes) do nothing behind the log; j/k still reach it to scroll.
func TestLogView_qClosesLogNotM9s(t *testing.T) {
	ra := newWiredRootApp(t, "test")
	ra.uiApp.SetFocus(ra.folders)
	if sendRune(ra, 'l') != nil {
		t.Fatal("l was not consumed")
	}
	if page, _ := ra.uiApp.Pages.GetFrontPage(); page != pageLog {
		t.Fatalf("front page = %q, want log", page)
	}
	if sendRune(ra, 'd') == nil || ra.uiApp.Pages.HasPage("delete-modal") {
		t.Error("d acted on the main page behind the log")
	}
	if sendRune(ra, 'j') == nil {
		t.Error("j must reach the log view to scroll it")
	}
	if sendRune(ra, 'q') != nil {
		t.Error("q was not consumed")
	}
	if page, _ := ra.uiApp.Pages.GetFrontPage(); page != pageMain {
		t.Fatalf("after q front page = %q, want main (q must not quit m9s)", page)
	}
	if ra.uiApp.GetFocus() != ra.folders {
		t.Errorf("focus = %T, want the folders pane back", ra.uiApp.GetFocus())
	}
	sendRune(ra, 'l')
	sendRune(ra, 'l') // l toggles the log closed too
	if page, _ := ra.uiApp.Pages.GetFrontPage(); page != pageMain {
		t.Errorf("after l l front page = %q, want main", page)
	}
}
