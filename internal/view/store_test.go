package view

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/store"
	msync "github.com/zhecho/m9s/internal/sync"
	"github.com/zhecho/m9s/internal/ui"
)

// twoAccounts returns an app with accounts a (active) and b, both with folders.
func twoAccounts(t *testing.T) *RootApp {
	t.Helper()
	ra := newWiredRootApp(t, "a", "b")
	ra.handleEvent(msync.FoldersUpdated{AccountName: "a", Folders: testFolders("INBOX", "Sent")})
	ra.handleEvent(msync.FoldersUpdated{AccountName: "b", Folders: testFolders("INBOX", "Work")})
	return ra
}

func account(ra *RootApp, name string) *mail.Account {
	for _, a := range ra.manager.All() {
		if a.Name == name {
			return a
		}
	}
	return nil
}

func TestSwitchAccount_showsRememberedInboxAtOnce(t *testing.T) {
	ra := twoAccounts(t)
	recordBodyLoads(ra)
	// b's INBOX was fetched earlier (this run or a previous one).
	ra.storeFetched(account(ra, "b"), &mail.Folder{Name: "INBOX"}, peopleEnvelopes())

	ra.switchAccount("b")

	if got := ra.maillist.VisibleCount(); got != 3 {
		t.Fatalf("mail list shows %d messages right after the switch, want b's 3 without waiting for the server", got)
	}
	if ra.selectedEnv == nil || ra.selectedEnv.ID != "1" {
		t.Errorf("selected %+v, want the top message", ra.selectedEnv)
	}
}

func TestSwitchAccount_neverShowsAnotherAccountsMail(t *testing.T) {
	ra := twoAccounts(t)
	recordBodyLoads(ra)
	ra.storeFetched(account(ra, "a"), &mail.Folder{Name: "INBOX"}, peopleEnvelopes()[:1])
	ra.storeFetched(account(ra, "b"), &mail.Folder{Name: "INBOX"}, peopleEnvelopes())

	ra.switchAccount("b")
	ra.switchAccount("a")

	if got := ra.maillist.VisibleCount(); got != 1 {
		t.Errorf("back on a: %d messages, want a's 1", got)
	}
}

func TestOpenFolder_showsRememberedListAtOnce(t *testing.T) {
	ra := twoAccounts(t)
	recordBodyLoads(ra)
	ra.storeFetched(account(ra, "a"), &mail.Folder{Name: "Sent"}, peopleEnvelopes()[:2])

	ra.openFolderByName("Sent")

	if got := ra.maillist.VisibleCount(); got != 2 {
		t.Errorf("Sent shows %d messages, want its remembered 2", got)
	}
}

func TestOpenFolder_rememberedListDoesNotMarkRead(t *testing.T) {
	ra := twoAccounts(t)
	ra.uiApp.SetFocus(ra.maillist)
	loads := recordBodyLoads(ra)
	ra.storeFetched(account(ra, "a"), &mail.Folder{Name: "Sent"}, peopleEnvelopes())

	ra.openFolderByName("Sent")

	if len(*loads) == 0 {
		t.Fatal("the top message of the remembered list was not loaded into the preview")
	}
	for _, l := range *loads {
		if l.markSeen {
			t.Errorf("message %s was marked read by opening the folder; nobody chose it", l.id)
		}
	}
}

func TestFetchedList_forAFolderTheUserLeftIsStillRemembered(t *testing.T) {
	ra := twoAccounts(t)
	recordBodyLoads(ra)
	a, inbox := account(ra, "a"), ra.currentFolder
	ra.switchAccount("b") // the reply for a's INBOX arrives after the user moved on

	ra.storeFetched(a, inbox, peopleEnvelopes())
	ra.showFetched(a, inbox, peopleEnvelopes())

	if got := ra.maillist.VisibleCount(); got != 0 {
		t.Fatalf("b's INBOX shows %d messages of account a", got)
	}
	ra.switchAccount("a")
	if got := ra.maillist.VisibleCount(); got != 3 {
		t.Errorf("back on a: %d messages, want the 3 fetched meanwhile", got)
	}
}

func TestFreshList_identicalToWhatIsShownLeavesThePreviewAlone(t *testing.T) {
	ra := twoAccounts(t)
	loads := recordBodyLoads(ra)
	ra.showEnvelopes(peopleEnvelopes())
	ra.currentMsg = &mail.Message{Envelope: ra.selectedEnv, TextBody: "hello"} // its body arrived
	shown := ra.currentMsg
	*loads = nil

	ra.showEnvelopes(freshCopies()) // the server confirms the remembered list

	if len(*loads) != 0 {
		t.Errorf("the body was fetched again (%+v) although nothing changed", *loads)
	}
	if ra.currentMsg != shown {
		t.Error("the message in the preview was dropped although nothing changed")
	}
}

func TestFreshList_withChangedFlagsIsRedrawn(t *testing.T) {
	ra := twoAccounts(t)
	recordBodyLoads(ra)
	ra.showEnvelopes(peopleEnvelopes())
	ra.currentMsg = &mail.Message{Envelope: ra.selectedEnv}

	fresh := freshCopies()
	fresh[1].Flags = []string{`\Seen`} // read on another device
	ra.showEnvelopes(fresh)

	if got := ra.maillist.Envelopes()[1]; got.IsUnread() {
		t.Error("the list still shows the message as unread")
	}
}

func TestFoldersUpdated_areRemembered(t *testing.T) {
	ra := twoAccounts(t)
	got := ra.store.Folders("b")
	if len(got) != 2 || got[0].Name != "INBOX" || got[1].Name != "Work" {
		t.Errorf("remembered folders of b = %+v, want INBOX and Work", got)
	}
}

func TestSetStore_startsWithLastRunsFoldersAndInbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mail")
	last, err := store.Open(path) // what the previous run left behind
	if err != nil {
		t.Fatal(err)
	}
	_ = last.PutFolders("a", testFolders("INBOX", "Sent"))
	_ = last.PutFolders("b", testFolders("INBOX", "Work"))
	_ = last.PutEnvelopes("a", "INBOX", peopleEnvelopes())
	_ = last.PutEnvelopes("b", "INBOX", peopleEnvelopes()[:2])
	last.Close()

	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ra := newWiredRootApp(t, "a", "b")
	recordBodyLoads(ra)

	ra.SetStore(st) // no server has answered yet

	if ra.currentFolder == nil || ra.currentFolder.Name != "INBOX" {
		t.Fatalf("currentFolder = %+v, want INBOX open from the start", ra.currentFolder)
	}
	if got := ra.maillist.VisibleCount(); got != 3 {
		t.Errorf("mail list shows %d messages at startup, want last run's 3", got)
	}
	ra.switchAccount("b")
	if got := ra.maillist.VisibleCount(); got != 2 {
		t.Errorf("b shows %d messages before it connected, want last run's 2", got)
	}
}

func TestSetStore_doesNotReplaceFoldersTheServerAlreadySent(t *testing.T) {
	st := store.NewMemory()
	_ = st.PutFolders("a", testFolders("INBOX", "Old"))
	ra := newWiredRootApp(t, "a")
	ra.handleEvent(msync.FoldersUpdated{AccountName: "a", Folders: testFolders("INBOX", "New")})

	ra.SetStore(st)

	names := ""
	for _, f := range ra.folders.Folders("a") {
		names += f.Name + " "
	}
	if names != "INBOX New " {
		t.Errorf("folders = %q, want the server's list kept", names)
	}
}

func TestFetchError_whileNotConnectedIsNotFlashed(t *testing.T) {
	ra := twoAccounts(t) // a has not reported "connected": it shows remembered mail
	ra.fetchFailed("a", "fetch failed", errors.New("imap: not connected"))

	if flash := ra.uiApp.StatusBar.FlashText(); flash != "" {
		t.Errorf("bottom row says %q; the header already shows the connection state", flash)
	}
}

func TestFetchError_whileConnectedIsFlashed(t *testing.T) {
	ra := twoAccounts(t)
	ra.handleEvent(msync.AccountStatus{AccountName: "a", State: msync.StateConnected})
	ra.fetchFailed("a", "fetch failed", errors.New("imap SELECT INBOX: timeout"))

	if flash := ra.uiApp.StatusBar.FlashText(); !strings.Contains(flash, "fetch failed: imap SELECT INBOX: timeout") {
		t.Errorf("bottom row says %q, want the fetch error", flash)
	}
}

func TestMirrorProgress_showsInTheHeaderWhileCopying(t *testing.T) {
	ra := twoAccounts(t)
	ra.handleEvent(msync.AccountStatus{AccountName: "a", State: msync.StateConnected})

	ra.handleEvent(msync.MirrorProgress{AccountName: "a", Messages: 53000, Downloaded: 1200})
	if got := infoLine(ra, 4); !strings.Contains(got, "saving mail 1200/53000") {
		t.Errorf("status line = %q, want the copy's progress", got)
	}

	ra.handleEvent(msync.MirrorProgress{AccountName: "a", Messages: 53000, Downloaded: 53000, Done: true})
	if got := infoLine(ra, 4); strings.Contains(got, "saving") || !strings.Contains(got, "connected") {
		t.Errorf("status line = %q, want plain connected once the copy is complete", got)
	}
}

func TestMirrorProgress_ofAnotherAccountStaysOutOfTheHeader(t *testing.T) {
	ra := twoAccounts(t)
	ra.handleEvent(msync.AccountStatus{AccountName: "a", State: msync.StateConnected})
	ra.handleEvent(msync.MirrorProgress{AccountName: "b", Messages: 10, Downloaded: 1})
	if got := infoLine(ra, 4); strings.Contains(got, "saving") {
		t.Errorf("status line = %q; that is account b's copy", got)
	}
}

func TestMirrorFailed_isLoggedNotFlashed(t *testing.T) {
	ra := twoAccounts(t)
	ra.handleEvent(msync.MirrorFailed{AccountName: "a", Err: errors.New("disk full")})

	if flash := ra.uiApp.StatusBar.FlashText(); flash != "" {
		t.Errorf("bottom row says %q; reading mail still works", flash)
	}
	var msgs []string
	for _, e := range ui.AppLog.Entries() {
		msgs = append(msgs, e.Msg)
	}
	logged := strings.Join(msgs, "\n")
	if !strings.Contains(logged, "[a] saving mail locally paused") || !strings.Contains(logged, "disk full") {
		t.Error("the reason the copy stopped is not in the log")
	}
}
