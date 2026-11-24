package view

import (
	"errors"
	"strings"
	"testing"

	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	msync "github.com/zhecho/m9s/internal/sync"
	"github.com/zhecho/m9s/internal/ui"
)

// draftsApp has INBOX open with a previewed message, and one local draft.
func draftsApp(t *testing.T) (*RootApp, *draft.Store) {
	t.Helper()
	ds := draft.NewStore(t.TempDir())
	if _, err := ds.Save("test", smtpadapter.Draft{Subject: "unsent reply", Body: "x"}); err != nil {
		t.Fatal(err)
	}
	skin, _ := ui.LoadSkin("")
	km, _ := ui.LoadKeyMap("")
	acct := &mail.Account{Name: "test", Config: &config.Account{Name: "test"}}
	// An engine that is never started: deleteMessage reaches its modal
	// without any network, exactly as it does for a connected account.
	engines := map[string]msync.Account{"test": msync.NewEngine(acct.Config, make(chan msync.Event, 8))}
	ra := NewRootApp(ui.NewApp(skin, km), &config.Config{}, mail.NewManager([]*mail.Account{acct}),
		engines, ds, make(chan msync.Event))
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX")})
	ra.maillist.SetEnvelopes(peopleEnvelopes())
	ra.currentMsg = &mail.Message{Envelope: peopleEnvelopes()[0]} // "Lunch" is previewed
	return ra, ds
}

// selectDraftNode puts the folder cursor on the first local draft entry.
func selectDraftNode(t *testing.T, ra *RootApp) {
	t.Helper()
	var found *tview.TreeNode
	ra.folders.GetRoot().Walk(func(node, _ *tview.TreeNode) bool {
		if _, ok := node.GetReference().(*draft.Meta); ok && found == nil {
			found = node
		}
		return true
	})
	if found == nil {
		t.Fatal("no local draft node in the tree")
	}
	ra.folders.SetCurrentNode(found)
	ra.uiApp.SetFocus(ra.folders)
}

func TestDeleteDraft_askesAboutTheDraftNotThePreviewedMessage(t *testing.T) {
	ra, _ := draftsApp(t)
	selectDraftNode(t, ra)

	sendRune(ra, 'd')

	if !ra.uiApp.Pages.HasPage("delete-modal") {
		t.Fatal("d on a local draft must ask to delete it")
	}
	if !strings.Contains(ra.testDeleteModalText, "unsent reply") {
		t.Errorf("modal = %q, want it to name the draft", ra.testDeleteModalText)
	}
	if strings.Contains(ra.testDeleteModalText, "Lunch") || strings.Contains(ra.testDeleteModalText, "Deleted Items") {
		t.Errorf("modal = %q; it is about the previewed INBOX message, not the draft", ra.testDeleteModalText)
	}
}

func TestDeleteDraft_confirmRemovesItAndRefreshesTree(t *testing.T) {
	ra, ds := draftsApp(t)
	selectDraftNode(t, ra)
	sendRune(ra, 'd')

	ra.testDeleteModalDone(0, "Delete")

	if drafts, _ := ds.List("test"); len(drafts) != 0 {
		t.Errorf("%d local drafts left, want 0", len(drafts))
	}
	ra.folders.GetRoot().Walk(func(node, _ *tview.TreeNode) bool {
		if _, ok := node.GetReference().(*draft.Meta); ok {
			t.Errorf("deleted draft %q still in the tree", node.GetText())
		}
		return true
	})
	if flash := ra.uiApp.StatusBar.FlashText(); !strings.Contains(flash, "Draft deleted") {
		t.Errorf("flash = %q", flash)
	}
}

func TestDeleteDraft_cancelKeepsIt(t *testing.T) {
	ra, ds := draftsApp(t)
	selectDraftNode(t, ra)
	sendRune(ra, 'd')
	ra.testDeleteModalDone(1, "Cancel")
	if drafts, _ := ds.List("test"); len(drafts) != 1 {
		t.Errorf("%d drafts after Cancel, want 1", len(drafts))
	}
}

func TestDelete_inFolderPaneOnAFolderDoesNothing(t *testing.T) {
	ra, _ := draftsApp(t)
	ra.folders.SelectFolder("INBOX")
	ra.uiApp.SetFocus(ra.folders)

	sendRune(ra, 'd')

	if ra.uiApp.Pages.HasPage("delete-modal") {
		t.Errorf("d in the folder pane offered to delete %q; Delete is not a folder-pane key", ra.testDeleteModalText)
	}
}

func TestDraftHover_showsDraftInPreview(t *testing.T) {
	ra, _ := draftsApp(t)
	selectDraftNode(t, ra)
	ra.folders.moveCursorTo(ra.folders.GetCurrentNode())

	text := ra.preview.GetText(true)
	if !strings.Contains(text, "unsent reply") || !strings.Contains(text, "x") {
		t.Errorf("preview = %q, want the draft's subject and body", text)
	}
	if ra.currentMsg != nil || ra.selectedEnv != nil {
		t.Error("with a draft previewed, r/R/d must not act on the folder's message")
	}
}

func TestFolderView_draftHoverFiresOnCursorMove(t *testing.T) {
	ds := draft.NewStore(t.TempDir())
	if _, err := ds.Save("a", smtpadapter.Draft{Subject: "s", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	var hovered []string
	fv := NewFolderView(testSkin(), nil)
	fv.SetOnDraftHover(func(dm draft.Meta) { hovered = append(hovered, dm.Subject) })
	fv.SetFolders("a", testFolders("INBOX"), ds)

	var draftNode *tview.TreeNode
	fv.GetRoot().Walk(func(n, _ *tview.TreeNode) bool {
		if _, ok := n.GetReference().(*draft.Meta); ok {
			draftNode = n
		}
		return true
	})
	fv.moveCursorTo(draftNode)

	if len(hovered) != 1 || hovered[0] != "s" {
		t.Errorf("draft hover calls = %v, want [s]", hovered)
	}
}

func TestDraftCursorKeptWhenDraftsRefresh(t *testing.T) {
	ra, ds := draftsApp(t)
	selectDraftNode(t, ra)
	id := ra.folders.SelectedDraft().ID
	if _, err := ds.Save("test", smtpadapter.Draft{Subject: "another", Body: "y"}); err != nil {
		t.Fatal(err)
	}

	ra.folders.RefreshDrafts()

	if dm := ra.folders.SelectedDraft(); dm == nil || dm.ID != id {
		t.Errorf("cursor left the draft after a refresh: %+v", dm)
	}
}

func TestEditHints(t *testing.T) {
	ra, _ := draftsApp(t)
	ra.folders.SelectFolder("INBOX")
	ra.uiApp.SetFocus(ra.folders)
	ra.refreshHints()
	if strings.Contains(ra.uiApp.Header.HintsText(), "Edit") {
		t.Error("<e> Edit shown on a folder, where e does nothing")
	}
	selectDraftNode(t, ra)
	ra.folders.moveCursorTo(ra.folders.GetCurrentNode())
	if !strings.Contains(strings.Join(strings.Fields(ra.uiApp.Header.HintsText()), " "), "<e> Edit") {
		t.Errorf("<e> Edit missing on a local draft:\n%s", ra.uiApp.Header.HintsText())
	}
}

func TestEditKeyIsBound(t *testing.T) {
	km, _ := ui.LoadKeyMap("")
	if km.KeyFor(ui.ActionEdit) != "e" {
		t.Errorf("edit is bound to %q, want e", km.KeyFor(ui.ActionEdit))
	}
}

func TestDraftFromMessage(t *testing.T) {
	msg := &mail.Message{
		Envelope: &mail.Envelope{
			Subject:   "Plan",
			From:      []mail.Address{{Name: "Me", Email: "me@x.io"}},
			To:        []mail.Address{{Email: "a@x.io"}, {Name: "B", Email: "b@x.io"}},
			CC:        []mail.Address{{Email: "c@x.io"}},
			InReplyTo: []string{"parent@x.io"},
		},
		TextBody: "draft body",
	}
	d := draftFromMessage(msg)
	if d.Subject != "Plan" || d.Body != "draft body" || !strings.Contains(d.From, "me@x.io") {
		t.Errorf("draft = %+v", d)
	}
	if len(d.To) != 2 || !strings.Contains(d.To[1], "b@x.io") || len(d.CC) != 1 {
		t.Errorf("recipients = %v / %v", d.To, d.CC)
	}
	if d.InReplyTo != "<parent@x.io>" {
		t.Errorf("InReplyTo = %q", d.InReplyTo)
	}
}

func TestSendModal_offersServerAndLocalDraft(t *testing.T) {
	ra, ds := draftsApp(t)
	id, _ := ds.Save("test", smtpadapter.Draft{Subject: "new", Body: "b"})

	ra.showSendModal(smtpadapter.Draft{Subject: "new", Body: "b"}, id, "test")

	want := []string{"Send", "Server Draft", "Local Draft"}
	if strings.Join(ra.testModalButtons, ",") != strings.Join(want, ",") {
		t.Errorf("buttons = %v, want %v", ra.testModalButtons, want)
	}
}

func metaByID(t *testing.T, ds *draft.Store, id string) draft.Meta {
	t.Helper()
	metas, _ := ds.List("test")
	for _, m := range metas {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("draft %s not found", id)
	return draft.Meta{}
}

func TestSendModal_localDraftStaysLocal(t *testing.T) {
	ra, ds := draftsApp(t)
	id, _ := ds.Save("test", smtpadapter.Draft{Subject: "mine", Body: "b"})
	ra.showSendModal(smtpadapter.Draft{Subject: "mine", Body: "b"}, id, "test")

	ra.testModalDone(2, "Local Draft")

	if !metaByID(t, ds, id).LocalOnly {
		t.Error("a Local Draft must be marked to never be uploaded")
	}
	if flash := ra.uiApp.StatusBar.FlashText(); !strings.Contains(flash, "local draft") {
		t.Errorf("flash = %q", flash)
	}
}

func TestSendModal_serverDraftClearsLocalOnly(t *testing.T) {
	ra, ds := draftsApp(t)
	id, _ := ds.Save("test", smtpadapter.Draft{Subject: "mine", Body: "b"})
	_ = ds.SetLocalOnly("test", id, true) // it was a Local Draft before this edit
	ra.showSendModal(smtpadapter.Draft{Subject: "mine", Body: "b"}, id, "test")

	ra.testModalDone(1, "Server Draft")

	if metaByID(t, ds, id).LocalOnly {
		t.Error("choosing Server Draft must make the draft eligible for upload")
	}
}

func TestServerDraftsFolderIsLabelled(t *testing.T) {
	ra, _ := draftsApp(t)
	ra.handleEvent(msync.FoldersUpdated{AccountName: "test", Folders: testFolders("INBOX", "Drafts")})

	if label := ra.folders.NodeForFolder("Drafts").GetText(); !strings.Contains(label, "Server Drafts") {
		t.Errorf("tree label = %q, want Server Drafts", label)
	}
	ra.openFolderByName("Drafts")
	if line := infoLine(ra, 2); !strings.Contains(line, "Server Drafts") {
		t.Errorf("header folder line = %q", line)
	}
	if got := ra.uiApp.StatusBar.CrumbsText(); got != "<test> <Server Drafts>" {
		t.Errorf("crumbs = %q", got)
	}
	if label := ra.folders.NodeForFolder("INBOX").GetText(); label != "INBOX" {
		t.Errorf("other folders keep their names, INBOX = %q", label)
	}
}

func TestDraftHover_draftGoneFromDiskRefreshesTree(t *testing.T) {
	ra, ds := draftsApp(t)
	selectDraftNode(t, ra)
	_ = ds.Delete("test", ra.folders.SelectedDraft().ID) // removed behind the screen's back

	ra.folders.moveCursorTo(ra.folders.GetCurrentNode())

	if text := ra.preview.GetText(true); strings.Contains(text, "no such file") || !strings.Contains(text, "no longer exists") {
		t.Errorf("preview = %q, want a plain explanation", text)
	}
	ra.folders.GetRoot().Walk(func(n, _ *tview.TreeNode) bool {
		if _, ok := n.GetReference().(*draft.Meta); ok {
			t.Error("the vanished draft is still listed")
		}
		return true
	})
}

func TestDraftSyncFailed_isShown(t *testing.T) {
	ra, _ := draftsApp(t)
	ra.handleEvent(msync.DraftSyncFailed{AccountName: "test", Subject: "Здравей", Err: errors.New("NO [CANNOT] invalid message")})
	flash := ra.uiApp.StatusBar.FlashText()
	if !strings.Contains(flash, "Здравей") || !strings.Contains(flash, "invalid message") {
		t.Errorf("flash = %q, want the draft and the server's answer", flash)
	}
}

func TestDeleteOutsideTrash_hasNoDeleteAll(t *testing.T) {
	ra, _ := draftsApp(t)
	ra.uiApp.SetFocus(ra.maillist)
	ra.deleteMessage()
	for _, b := range ra.testDeleteButtons {
		if strings.Contains(b.Label, "All") {
			t.Errorf("INBOX delete offers %q; Delete All Forever belongs to Deleted Items only", b.Label)
		}
	}
	if !ra.uiApp.Pages.HasPage("delete-modal") || !strings.Contains(ra.testDeleteModalText, "Deleted Items") {
		t.Errorf("move-to-trash prompt = %q", ra.testDeleteModalText)
	}
}
