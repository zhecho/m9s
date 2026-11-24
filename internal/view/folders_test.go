// internal/view/folders_test.go
package view_test

import (
	"strings"
	"testing"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	"github.com/zhecho/m9s/internal/ui"
	"github.com/zhecho/m9s/internal/view"
)

func newTestSkin() *ui.Skin {
	skin, _ := ui.LoadSkin("")
	return skin
}

func TestFolderView_updateBadge(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)
	folders := []*mail.Folder{
		{Name: "INBOX", Delimiter: "/", Status: &mail.MailboxStatus{Unseen: 0}},
	}
	fv.SetFolders("acct", folders, nil)

	// No panic, label should update
	fv.UpdateBadge("acct", "INBOX", 5, 20)

	node := fv.NodeForFolder("INBOX")
	if node == nil {
		t.Fatal("INBOX node not found after SetFolders")
	}
	if node.GetText() == "INBOX" {
		t.Error("expected badge label, got plain 'INBOX'")
	}
}

func TestFolderView_updateBadge_unknownFolder(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)
	fv.SetFolders("acct", nil, nil)
	// Must not panic
	fv.UpdateBadge("acct", "nonexistent", 1, 5)
}

func TestFolderView_localDraftsNode(t *testing.T) {
	dir := t.TempDir()
	ds := draft.NewStore(dir)
	// Save a draft so the count is non-zero.
	_, err := ds.Save("acct", smtpadapter.Draft{Subject: "hello"})
	if err != nil {
		t.Fatalf("save draft: %v", err)
	}

	fv := view.NewFolderView(newTestSkin(), nil)
	fv.SetFolders("acct", nil, ds)

	root := fv.GetRoot()
	if root == nil {
		t.Fatal("root is nil")
	}
	children := root.GetChildren()
	if len(children) == 0 {
		t.Fatal("expected Local Drafts child node")
	}
	// The last child should be the Local Drafts parent.
	draftNode := children[len(children)-1]
	text := draftNode.GetText()
	if text == "Local Drafts" {
		t.Errorf("expected badge count in label, got plain %q", text)
	}
}

func TestFolderView_updateDraftBadge(t *testing.T) {
	dir := t.TempDir()
	ds := draft.NewStore(dir)

	fv := view.NewFolderView(newTestSkin(), nil)
	fv.SetFolders("acct", nil, ds)

	// No drafts initially — label should be plain.
	fv.UpdateDraftBadge("acct", ds)
	root := fv.GetRoot()
	children := root.GetChildren()
	if len(children) == 0 {
		t.Fatal("expected draft parent node")
	}
	draftNode := children[len(children)-1]
	if draftNode.GetText() != "Local Drafts" {
		t.Errorf("expected 'Local Drafts', got %q", draftNode.GetText())
	}

	// Add a draft, then update badge.
	if _, err := ds.Save("acct", smtpadapter.Draft{Subject: "test"}); err != nil {
		t.Fatalf("save draft: %v", err)
	}
	fv.UpdateDraftBadge("acct", ds)
	if draftNode.GetText() == "Local Drafts" {
		t.Error("expected badge count after adding a draft")
	}
}

func TestFolderView_updateDraftBadge_noDraftStore(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)
	fv.SetFolders("acct", nil, nil)
	// Must not panic with nil draftStore or no draftParent.
	fv.UpdateDraftBadge("acct", nil)
}

func TestFolderView_setFolders_upsert(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)

	folders1 := []*mail.Folder{{Name: "INBOX", Delimiter: "/"}}
	folders2 := []*mail.Folder{{Name: "INBOX", Delimiter: "/"}, {Name: "Sent", Delimiter: "/"}}

	fv.SetFolders("acct1", folders1, nil)
	fv.SetFolders("acct2", folders2, nil)

	if fv.AccountCount() != 2 {
		t.Errorf("want 2 accounts, got %d", fv.AccountCount())
	}
	if fv.ActiveAccountName() != "acct1" {
		t.Errorf("want active=acct1, got %q", fv.ActiveAccountName())
	}
}

func TestFolderView_switchAccount(t *testing.T) {
	var switched string
	fv := view.NewFolderView(newTestSkin(), nil)
	fv.SetOnAccountSwitch(func(name string) { switched = name })

	fv.SetFolders("acct1", []*mail.Folder{{Name: "INBOX", Delimiter: "/"}}, nil)
	fv.SetFolders("acct2", []*mail.Folder{{Name: "INBOX", Delimiter: "/"}}, nil)

	fv.SwitchAccount(1)

	if fv.ActiveAccountName() != "acct2" {
		t.Errorf("want active=acct2, got %q", fv.ActiveAccountName())
	}
	if switched != "acct2" {
		t.Errorf("want callback called with acct2, got %q", switched)
	}
}

func TestFolderView_switchAccount_noCallback(t *testing.T) {
	// SwitchAccount when onAccountSwitch is nil — must not panic.
	fv := view.NewFolderView(newTestSkin(), nil)
	fv.SetFolders("acct1", []*mail.Folder{{Name: "INBOX", Delimiter: "/"}}, nil)
	fv.SetFolders("acct2", []*mail.Folder{{Name: "INBOX", Delimiter: "/"}}, nil)
	// No SetOnAccountSwitch — covers the `if fv.onAccountSwitch != nil` else path.
	fv.SwitchAccount(1)
}

func TestFolderView_switchAccount_wraps(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)
	fv.SetFolders("acct1", []*mail.Folder{{Name: "INBOX", Delimiter: "/"}}, nil)
	fv.SetFolders("acct2", []*mail.Folder{{Name: "INBOX", Delimiter: "/"}}, nil)

	fv.SwitchAccount(-1)
	if fv.ActiveAccountName() != "acct2" {
		t.Errorf("want wrap to acct2, got %q", fv.ActiveAccountName())
	}
}

func TestFolderView_updateBadge_nilStatus(t *testing.T) {
	// Folder has no Status set — UpdateBadge should create it.
	fv := view.NewFolderView(newTestSkin(), nil)
	folders := []*mail.Folder{
		{Name: "INBOX", Delimiter: "/", Status: nil},
	}
	fv.SetFolders("acct", folders, nil)
	fv.UpdateBadge("acct", "INBOX", 3, 10) // nil Status path
	node := fv.NodeForFolder("INBOX")
	if node == nil {
		t.Fatal("INBOX node not found")
	}
	if node.GetText() == "INBOX" {
		t.Error("expected badge in label after UpdateBadge with unseen>0")
	}
}

func TestFolderView_updateBadge_zeroUnseen(t *testing.T) {
	// unseen=0 hits the else branch that restores foreground color.
	fv := view.NewFolderView(newTestSkin(), nil)
	folders := []*mail.Folder{
		{Name: "INBOX", Delimiter: "/", Status: &mail.MailboxStatus{Unseen: 5}},
	}
	fv.SetFolders("acct", folders, nil)
	fv.UpdateBadge("acct", "INBOX", 0, 10) // unseen=0 → foreground color path
	node := fv.NodeForFolder("INBOX")
	if node == nil {
		t.Fatal("INBOX node not found")
	}
	if node.GetText() != "INBOX" {
		t.Errorf("zero unseen badge should be plain name, got %q", node.GetText())
	}
}

func TestFolderView_activeAccountName_empty(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)
	if fv.ActiveAccountName() != "" {
		t.Errorf("want empty name when no accounts, got %q", fv.ActiveAccountName())
	}
}

func TestFolderView_setFolders_update(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)

	folders1 := []*mail.Folder{{Name: "INBOX", Delimiter: "/"}}
	folders2 := []*mail.Folder{{Name: "INBOX", Delimiter: "/"}, {Name: "Sent", Delimiter: "/"}}

	// First call — creates entry.
	fv.SetFolders("acct1", folders1, nil)
	if fv.AccountCount() != 1 {
		t.Fatalf("want 1 account, got %d", fv.AccountCount())
	}

	// Second call with same name — upserts.
	fv.SetFolders("acct1", folders2, nil)
	if fv.AccountCount() != 1 {
		t.Errorf("want still 1 account after upsert, got %d", fv.AccountCount())
	}
}

func TestFolderView_focusBlur(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)
	focused := 0
	blurred := 0
	fv.SetFocusFunc(func() { focused++ })
	fv.SetBlurFunc(func() { blurred++ })

	fv.Focus(func(_ tview.Primitive) {})
	if focused != 1 {
		t.Errorf("want focusFunc called once, got %d", focused)
	}
	fv.Blur()
	if blurred != 1 {
		t.Errorf("want blurFunc called once, got %d", blurred)
	}
}

func TestFolderView_focusBlur_noCallbacks(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)
	fv.Focus(func(_ tview.Primitive) {})
	fv.Blur()
}

func TestFolderView_renderTree_noSelect(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)
	folders := []*mail.Folder{
		{Name: "INBOX", Delimiter: "/"},
		{Name: "NoSelFolder", Delimiter: "/", Attributes: []string{`\NoSelect`}},
	}
	fv.SetFolders("acct", folders, nil)
	// NoSelect folder should not appear as a selectable node.
	if fv.NodeForFolder("NoSelFolder") != nil {
		t.Error("NoSelect folder should not be in nodeMap")
	}
	if fv.NodeForFolder("INBOX") == nil {
		t.Error("INBOX should be in nodeMap")
	}
}

func TestFolderView_renderTree_nestedFolders(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)
	folders := []*mail.Folder{
		{Name: "Work", Delimiter: "/"},
		{Name: "Work/Projects", Delimiter: "/"},
		{Name: "Work/Projects/Active", Delimiter: "/"},
	}
	fv.SetFolders("acct", folders, nil)
	if fv.NodeForFolder("Work/Projects/Active") == nil {
		t.Error("nested folder node should exist")
	}
}

func TestFolderView_renderTree_unreadFolder(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)
	folders := []*mail.Folder{
		{Name: "INBOX", Delimiter: "/", Status: &mail.MailboxStatus{Unseen: 5}},
	}
	fv.SetFolders("acct", folders, nil)
	node := fv.NodeForFolder("INBOX")
	if node == nil {
		t.Fatal("INBOX node not found")
	}
	// Unread folder should have its label include the count.
	if node.GetText() == "INBOX" {
		t.Error("unread folder should have badge in label")
	}
}

// drawFolderView runs one draw pass, which is when the tree applies cursor movement.
func drawFolderView(t *testing.T, fv *view.FolderView) {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	fv.SetRect(0, 0, 40, 20)
	fv.Draw(screen)
}

func pressFolderKey(t *testing.T, fv *view.FolderView, r rune) {
	t.Helper()
	fv.InputHandler()(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone), func(tview.Primitive) {})
	drawFolderView(t, fv)
}

func TestFolderView_hoverFiresOnCursorMove(t *testing.T) {
	var selected, hovered []string
	fv := view.NewFolderView(newTestSkin(), func(f *mail.Folder) { selected = append(selected, f.Name) })
	fv.SetOnHover(func(f *mail.Folder) { hovered = append(hovered, f.Name) })
	fv.SetFolders("acct", []*mail.Folder{
		{Name: "INBOX", Delimiter: "/"},
		{Name: "Sent", Delimiter: "/"},
	}, nil)
	drawFolderView(t, fv) // cursor starts on the account root

	pressFolderKey(t, fv, 'j') // root → INBOX
	pressFolderKey(t, fv, 'j') // INBOX → Sent
	pressFolderKey(t, fv, 'k') // Sent → INBOX
	pressFolderKey(t, fv, 'k') // INBOX → root: not a folder, no hover

	want := []string{"INBOX", "Sent", "INBOX"}
	if strings.Join(hovered, ",") != strings.Join(want, ",") {
		t.Errorf("hovered = %v, want %v", hovered, want)
	}
	if len(selected) != 0 {
		t.Errorf("cursor movement must not fire onSelect, got %v", selected)
	}
}

func TestFolderView_hoverWithoutCallback(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)
	fv.SetFolders("acct", []*mail.Folder{{Name: "INBOX", Delimiter: "/"}}, nil)
	drawFolderView(t, fv)
	pressFolderKey(t, fv, 'j') // must not panic
}

func twoFolders() []*mail.Folder {
	return []*mail.Folder{
		{Name: "INBOX", Delimiter: "/"},
		{Name: "Sent", Delimiter: "/"},
	}
}

func currentFolderName(fv *view.FolderView) string {
	node := fv.GetCurrentNode()
	if node == nil {
		return ""
	}
	if f, ok := node.GetReference().(*mail.Folder); ok {
		return f.Name
	}
	return ""
}

func TestFolderView_seedAccounts(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)
	fv.SeedAccounts([]string{"a", "b", "c"})
	if fv.AccountCount() != 3 {
		t.Fatalf("AccountCount = %d, want 3", fv.AccountCount())
	}
	if fv.ActiveAccountName() != "a" {
		t.Errorf("active = %q, want the first seeded account", fv.ActiveAccountName())
	}
	fv.SwitchAccount(1)
	if fv.ActiveAccountName() != "b" {
		t.Errorf("seeded accounts must be switchable in config order; after +1 active = %q", fv.ActiveAccountName())
	}
	fv.SwitchAccount(-1)

	fv.SetFolders("b", twoFolders(), nil)
	fv.SeedAccounts([]string{"a", "b", "c"}) // idempotent
	if fv.AccountCount() != 3 || fv.ActiveAccountName() != "a" {
		t.Errorf("after SetFolders+reseed: count %d active %q, want 3 and a", fv.AccountCount(), fv.ActiveAccountName())
	}
	if len(fv.Folders("b")) != 2 {
		t.Errorf("reseeding must not drop b's folders, got %d", len(fv.Folders("b")))
	}
}

func TestFolderView_seededEmptyAccountRenders(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)
	fv.SeedAccounts([]string{"a"})
	if fv.GetRoot() == nil {
		t.Fatal("a seeded account with no folders yet must still render its root")
	}
	if fv.Folders("a") != nil || fv.Folders("unknown") != nil {
		t.Error("Folders must be nil for an account with none and for an unknown account")
	}
}

func TestFolderView_badgeForInactiveAccountIsStored(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)
	fv.SeedAccounts([]string{"a", "b"})
	fv.SetFolders("b", twoFolders(), nil)

	fv.UpdateBadge("b", "INBOX", 4, 9)

	unseen, total, ok := fv.Counts("b", "INBOX")
	if !ok || unseen != 4 || total != 9 {
		t.Fatalf("Counts(b, INBOX) = %d, %d, %v; want 4, 9, true", unseen, total, ok)
	}
	if _, _, ok := fv.Counts("a", "INBOX"); ok {
		t.Error("the badge must not leak into another account")
	}
	if !fv.SetActiveAccount("b") {
		t.Fatal("SetActiveAccount(b) = false")
	}
	if label := fv.NodeForFolder("INBOX").GetText(); !strings.Contains(label, "(4)") {
		t.Errorf("INBOX label after switching = %q, want it to show (4)", label)
	}
}

func TestFolderView_countsUnknown(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)
	fv.SetFolders("a", twoFolders(), nil)
	if _, _, ok := fv.Counts("a", "INBOX"); ok {
		t.Error("counts must be unknown before any badge or status arrived")
	}
	if _, _, ok := fv.Counts("nope", "INBOX"); ok {
		t.Error("counts must be unknown for an unknown account")
	}
}

func TestFolderView_countsFromFolderStatus(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)
	fv.SetFolders("a", []*mail.Folder{
		{Name: "INBOX", Delimiter: "/", Status: &mail.MailboxStatus{Unseen: 2, Messages: 5}},
	}, nil)
	if unseen, total, ok := fv.Counts("a", "INBOX"); !ok || unseen != 2 || total != 5 {
		t.Errorf("Counts = %d, %d, %v; want 2, 5, true", unseen, total, ok)
	}
}

func TestFolderView_countsSurviveSetFolders(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)
	fv.SetFolders("a", twoFolders(), nil)
	fv.UpdateBadge("a", "INBOX", 4, 9)

	fv.SetFolders("a", twoFolders(), nil) // fresh objects without Status

	if unseen, total, ok := fv.Counts("a", "INBOX"); !ok || unseen != 4 || total != 9 {
		t.Errorf("Counts after SetFolders = %d, %d, %v; want 4, 9, true", unseen, total, ok)
	}
	if label := fv.NodeForFolder("INBOX").GetText(); !strings.Contains(label, "(4)") {
		t.Errorf("label after SetFolders = %q, want (4) kept", label)
	}
}

func TestFolderView_selectFolderDoesNotFireCallbacks(t *testing.T) {
	var calls int
	fv := view.NewFolderView(newTestSkin(), func(*mail.Folder) { calls++ })
	fv.SetOnHover(func(*mail.Folder) { calls++ })
	fv.SetFolders("a", twoFolders(), nil)

	if !fv.SelectFolder("Sent") {
		t.Fatal("SelectFolder(Sent) = false")
	}
	if got := currentFolderName(fv); got != "Sent" {
		t.Errorf("cursor on %q, want Sent", got)
	}
	drawFolderView(t, fv)
	if fv.SelectFolder("nope") {
		t.Error("SelectFolder(nope) = true")
	}
	if got := currentFolderName(fv); got != "Sent" {
		t.Errorf("failed SelectFolder moved the cursor to %q", got)
	}
	if calls != 0 {
		t.Errorf("SelectFolder fired %d callbacks, want 0", calls)
	}
}

func TestFolderView_cursorKeptAcrossSetFolders(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)
	fv.SetFolders("a", twoFolders(), nil)
	fv.SelectFolder("Sent")

	fv.SetFolders("a", twoFolders(), nil)
	if got := currentFolderName(fv); got != "Sent" {
		t.Errorf("cursor after same-account SetFolders on %q, want Sent", got)
	}

	fv.SetFolders("a", []*mail.Folder{{Name: "INBOX", Delimiter: "/"}}, nil)
	if got := currentFolderName(fv); got != "" {
		t.Errorf("cursor on %q after its folder vanished, want the account root", got)
	}
}

func TestFolderView_cursorNotCarriedToOtherAccount(t *testing.T) {
	fv := view.NewFolderView(newTestSkin(), nil)
	fv.SeedAccounts([]string{"a", "b"})
	fv.SetFolders("a", twoFolders(), nil)
	fv.SetFolders("b", twoFolders(), nil)
	fv.SelectFolder("Sent")

	fv.SetActiveAccount("b")
	if got := currentFolderName(fv); got != "" {
		t.Errorf("cursor on %q after switching account, want the root: nothing of b is open", got)
	}
}

func TestFolderView_setActiveAccount(t *testing.T) {
	var switched []string
	fv := view.NewFolderView(newTestSkin(), nil)
	fv.SetOnAccountSwitch(func(name string) { switched = append(switched, name) })
	fv.SeedAccounts([]string{"a", "b"})

	if !fv.SetActiveAccount("b") {
		t.Error("SetActiveAccount(b) = false, want true")
	}
	if fv.SetActiveAccount("b") {
		t.Error("SetActiveAccount on the already-active account = true, want false")
	}
	if fv.SetActiveAccount("nope") {
		t.Error("SetActiveAccount(nope) = true, want false")
	}
	if len(switched) != 1 || switched[0] != "b" {
		t.Errorf("callback calls = %v, want exactly [b]", switched)
	}
	if fv.ActiveAccountName() != "b" {
		t.Errorf("active = %q, want b", fv.ActiveAccountName())
	}
}

func pressFolderSpecial(t *testing.T, fv *view.FolderView, k tcell.Key) {
	t.Helper()
	fv.InputHandler()(tcell.NewEventKey(k, 0, tcell.ModNone), func(tview.Primitive) {})
	drawFolderView(t, fv)
}

func TestFolderView_arrowsDoNothing(t *testing.T) {
	var switched []string
	fv := view.NewFolderView(newTestSkin(), nil)
	fv.SetOnAccountSwitch(func(name string) { switched = append(switched, name) })
	fv.SeedAccounts([]string{"a", "b"})
	fv.SetFolders("a", twoFolders(), nil)
	fv.SelectFolder("INBOX")
	drawFolderView(t, fv)

	pressFolderSpecial(t, fv, tcell.KeyRight)
	pressFolderSpecial(t, fv, tcell.KeyLeft)

	if len(switched) != 0 || fv.ActiveAccountName() != "a" {
		t.Errorf("arrows switched account: %v, active %q", switched, fv.ActiveAccountName())
	}
	if got := currentFolderName(fv); got != "INBOX" {
		t.Errorf("arrows moved the cursor to %q; they must do nothing", got)
	}
}
