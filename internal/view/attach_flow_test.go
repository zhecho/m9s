package view

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/store"
)

func attachApp(t *testing.T, files ...*mail.Attachment) (*RootApp, *[]string) {
	t.Helper()
	ra, opened := browserApp(t)
	ra.uiApp.SetFocus(ra.maillist)
	if files != nil {
		ra.currentMsg = &mail.Message{Envelope: ra.selectedEnv, Attachments: files}
	}
	return ra, opened
}

func dirKey(ra *RootApp, k tcell.Key) {
	ra.attachDir.InputHandler()(tcell.NewEventKey(k, 0, tcell.ModNone), func(tview.Primitive) {})
}

func flash(ra *RootApp) string { return ra.uiApp.StatusBar.FlashText() }

func TestSave_toConfiguredDir(t *testing.T) {
	ra, _ := attachApp(t, file("a.pdf", "A"), file("b.jpg", "B"))
	dir := t.TempDir()
	ra.cfg.UI.AttachmentDir = dir
	if ev := sendRune(ra, 's'); ev != nil {
		t.Error("s must be consumed in the mail list")
	}
	if !ra.uiApp.Pages.HasPage(pageAttachPicker) {
		t.Fatal("two files and no picker")
	}
	pickerKeys(ra.attachPicker, tcell.KeyEnter) // All files
	for _, n := range []string{"a.pdf", "b.jpg"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s not saved: %v", n, err)
		}
	}
	if got := flash(ra); got != "Saved 2 files to "+dir {
		t.Errorf("status = %q", got)
	}
	if ra.uiApp.Pages.HasPage(pageAttachPicker) || ra.uiApp.Pages.HasPage(pageAttachDir) {
		t.Error("an overlay is still open")
	}
}

func TestSave_asksForFolderPrefilled(t *testing.T) {
	ra, _ := attachApp(t, file("a.pdf", "A"))
	sendRune(ra, 's')
	if ra.uiApp.Pages.HasPage(pageAttachPicker) {
		t.Error("one file needs no picker")
	}
	if !ra.uiApp.Pages.HasPage(pageAttachDir) || ra.attachDir.GetText() != "~/Downloads" {
		t.Fatalf("folder question missing or not pre-filled (%q)", ra.attachDir.GetText())
	}
	dir := t.TempDir()
	ra.attachDir.SetText(dir)
	dirKey(ra, tcell.KeyEnter)
	if _, err := os.Stat(filepath.Join(dir, "a.pdf")); err != nil {
		t.Errorf("not saved: %v", err)
	}
	if got := flash(ra); got != "Saved a.pdf to "+dir {
		t.Errorf("status = %q", got)
	}
	sendRune(ra, 's')
	if ra.attachDir.GetText() != dir {
		t.Errorf("second save pre-fills %q, want the folder used last", ra.attachDir.GetText())
	}
	dirKey(ra, tcell.KeyEscape)
	if ra.uiApp.Pages.HasPage(pageAttachDir) {
		t.Error("Esc left the folder question open")
	}
}

func TestSave_noAttachments(t *testing.T) {
	ra, _ := attachApp(t)
	ra.currentMsg = &mail.Message{Envelope: ra.selectedEnv, TextBody: "x"}
	sendRune(ra, 's')
	if flash(ra) != "No attachments" || ra.uiApp.Pages.HasPage(pageAttachDir) {
		t.Errorf("status %q, folder question open %v", flash(ra), ra.uiApp.Pages.HasPage(pageAttachDir))
	}
}

func TestSave_waitsForBody(t *testing.T) {
	ra, _ := attachApp(t)
	loads := recordBodyLoads(ra)
	ra.cfg.UI.AttachmentDir = t.TempDir()
	sendRune(ra, 's')
	if len(*loads) != 1 || ra.uiApp.Pages.HasPage(pageAttachPicker) {
		t.Fatalf("loads %v, picker %v; want the body asked for first", *loads, ra.uiApp.Pages.HasPage(pageAttachPicker))
	}
	env := ra.selectedEnv
	ra.bodyArrived(ra.manager.Active(), ra.currentFolder, env,
		&mail.Message{Envelope: env, Attachments: []*mail.Attachment{file("a.pdf", "A"), file("b.pdf", "B")}})
	if !ra.uiApp.Pages.HasPage(pageAttachPicker) {
		t.Error("the picker did not follow the body")
	}
}

func TestSave_bodyNotDownloaded(t *testing.T) {
	ra, _ := attachApp(t)
	sendRune(ra, 's')
	ra.bodyFailed(ra.manager.Active(), ra.selectedEnv, errors.New("dial tcp: no route"))
	if got := flash(ra); got != notDownloadedAttach {
		t.Errorf("status = %q, want %q", got, notDownloadedAttach)
	}
}

func TestOpen_refusesRunnable(t *testing.T) {
	ra, opened := attachApp(t, file("run.command", "#!/bin/sh"))
	sendRune(ra, 'o')
	if len(*opened) != 0 {
		t.Errorf("opened %v", *opened)
	}
	if got := flash(ra); !strings.Contains(got, "run.command can run programs: save it with s instead") {
		t.Errorf("status = %q", got)
	}
}

func TestOpen_writesAndOpens(t *testing.T) {
	ra, opened := attachApp(t, file("a.pdf", "PDF"))
	sendRune(ra, 'o')
	if len(*opened) != 1 {
		t.Fatalf("opened %v, want one file", *opened)
	}
	p := (*opened)[0]
	if b, _ := os.ReadFile(p); string(b) != "PDF" || filepath.Base(p) != "a.pdf" {
		t.Errorf("opened %s holding %q", p, b)
	}
	if got := flash(ra); got != "Opened a.pdf" {
		t.Errorf("status = %q", got)
	}
	ra.browser.cleanup() // what Stop does on quit
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("the opened file outlived m9s")
	}
}

func TestAttachKeysIgnoredInFolders(t *testing.T) {
	ra, opened := attachApp(t, file("a.pdf", "A"))
	ra.uiApp.SetFocus(ra.folders)
	sendRune(ra, 's')
	sendRune(ra, 'o')
	if ra.uiApp.Pages.HasPage(pageAttachDir) || len(*opened) != 0 {
		t.Error("s/o acted in the folder pane")
	}
}

func TestSave_conversationRowTakesEveryMessage(t *testing.T) {
	ra, _ := gmailApp(t)
	ra.cfg.UI.AttachmentDir = t.TempDir()
	a := &mail.Message{Envelope: &mail.Envelope{ID: "1"}, Attachments: []*mail.Attachment{file("a.pdf", "A")}}
	b := &mail.Message{Envelope: &mail.Envelope{ID: "2"}, Attachments: []*mail.Attachment{file("b.pdf", "B")}}
	ra.conv = newConversation([]*mail.Message{a, b}, false)
	ra.currentMsg = ra.conv.target()
	sendRune(ra, 's')
	if ra.attachPicker == nil || len(ra.attachPicker.files) != 2 {
		t.Fatal("a conversation row must offer the files of all its messages")
	}
}

const pdfBody = "From: a@example.com\r\nMIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=MIX\r\n\r\n" +
	"--MIX\r\nContent-Type: application/pdf; name=bill.pdf\r\n\r\nJVBERg==\r\n--MIX--\r\n"

func TestStoreFetched_keepsSignLearnedFromBody(t *testing.T) {
	ra, _ := attachApp(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ra.store = st
	acct, folder := ra.manager.Active(), ra.currentFolder
	first := []*mail.Envelope{{ID: "1", SortKey: 1, Subject: "bill"}}
	ra.storeFetched(acct, folder, first)
	if err := st.PutBody(acct.Name, folder.Name, "1", []byte(pdfBody)); err != nil {
		t.Fatal(err)
	}
	again := []*mail.Envelope{{ID: "1", SortKey: 1, Subject: "bill"}} // server guess: no file
	ra.storeFetched(acct, folder, again)
	if !again[0].HasAttach {
		t.Error("a refetched list dropped the sign the body taught")
	}
}

func TestSameList_seesSignChanges(t *testing.T) {
	a := []*mail.Envelope{{ID: "1"}}
	b := []*mail.Envelope{{ID: "1", HasAttach: true}}
	if sameList(a, b) {
		t.Error("a sign that changed counts as the same list, so it is never drawn")
	}
}

func TestPending_droppedWhenSelectionMoves(t *testing.T) {
	ra, opened := attachApp(t)
	first := ra.selectedEnv
	sendRune(ra, 'o') // body not here yet: pending
	ra.maillist.Select(1, 0)
	ra.maillist.Select(0, 0) // back, minutes later
	ra.bodyArrived(ra.manager.Active(), ra.currentFolder, first,
		&mail.Message{Envelope: first, Attachments: []*mail.Attachment{file("a.pdf", "A")}})
	if len(*opened) != 0 {
		t.Errorf("a file opened that the owner stopped asking for: %v", *opened)
	}
}

func TestPending_survivesListRefreshFailure(t *testing.T) {
	ra, _ := attachApp(t)
	ra.cfg.UI.AttachmentDir = t.TempDir()
	env := ra.selectedEnv
	sendRune(ra, 's')
	ra.fetchFailed("test", "fetch failed", errors.New("list refresh timed out"))
	if flash(ra) == notDownloadedAttach {
		t.Error("a failed list refresh was taken for the body failing")
	}
	ra.bodyArrived(ra.manager.Active(), ra.currentFolder, env,
		&mail.Message{Envelope: env, Attachments: []*mail.Attachment{file("a.pdf", "A"), file("b.pdf", "B")}})
	if !ra.uiApp.Pages.HasPage(pageAttachPicker) {
		t.Error("the save was lost")
	}
}
