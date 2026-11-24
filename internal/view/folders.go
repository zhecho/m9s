package view

import (
	"fmt"
	"strings"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/ui"
)

// accountTab holds one account's folder list for the tab switcher.
type accountTab struct {
	name       string
	folders    []*mail.Folder
	draftStore *draft.Store
	counts     map[string]folderCounts // last-known counts per folder name
}

type folderCounts struct{ unseen, total uint32 }

// FolderView is the left-pane folder tree with a tab bar above it.
type FolderView struct {
	*tview.TreeView
	skin            *ui.Skin
	onSelect        func(*mail.Folder)
	onHover         func(*mail.Folder)
	onDraftHover    func(draft.Meta)
	serverDrafts    map[string]string // account → the server's drafts folder, labelled "Server Drafts"
	onAccountSwitch func(string)

	accounts  []accountTab
	activeIdx int
	rendered  string // account whose tree is currently shown

	// nodeMap and folderMap are for the currently-rendered (active) account.
	nodeMap     map[string]*tview.TreeNode
	folderMap   map[string]*mail.Folder
	draftParent *tview.TreeNode

	focusFunc func()
	blurFunc  func()
}

// NewFolderView creates the folder tree widget.
func NewFolderView(skin *ui.Skin, onSelect func(*mail.Folder)) *FolderView {
	tv := tview.NewTreeView()
	tv.SetBorder(true)
	tv.SetTitle(" FOLDERS ")
	tv.SetTitleColor(skin.Color("header_fg"))
	tv.SetBorderColor(skin.Color("border"))
	tv.SetBackgroundColor(skin.Color("background"))
	tv.SetGraphicsColor(skin.Color("dim"))

	fv := &FolderView{
		TreeView:  tv,
		skin:      skin,
		onSelect:  onSelect,
		nodeMap:   make(map[string]*tview.TreeNode),
		folderMap: make(map[string]*mail.Folder),
	}

	// Moving the cursor onto a folder previews it without needing Enter.
	tv.SetChangedFunc(fv.cursorMoved)

	tv.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyLeft, tcell.KeyRight:
			// Not an account switch ([ ] and : do that) and not the tree's
			// default up/down either: the arrows do nothing here.
			return nil
		}
		return event
	})

	return fv
}

// cursorMoved reports the node the cursor moved onto: a folder or a draft.
func (fv *FolderView) cursorMoved(node *tview.TreeNode) {
	switch ref := node.GetReference().(type) {
	case *mail.Folder:
		if fv.onHover != nil {
			fv.onHover(ref)
		}
	case *draft.Meta:
		if fv.onDraftHover != nil {
			fv.onDraftHover(*ref)
		}
	}
}

// moveCursorTo puts the cursor on node exactly as moving there with j/k does,
// including the hover callback.
func (fv *FolderView) moveCursorTo(node *tview.TreeNode) {
	if node == nil {
		return
	}
	fv.SetCurrentNode(node)
	fv.cursorMoved(node)
}

// ServerDraftsLabel is how the server's drafts folder is shown, next to Local Drafts.
const ServerDraftsLabel = "Server Drafts"

// SetServerDraftsFolder names the folder of account to label ServerDraftsLabel.
func (fv *FolderView) SetServerDraftsFolder(accountName, folderName string) {
	if fv.serverDrafts == nil {
		fv.serverDrafts = make(map[string]string)
	}
	fv.serverDrafts[accountName] = folderName
}

// DisplayName is how folder of account is shown on screen.
func (fv *FolderView) DisplayName(accountName string, f *mail.Folder) string {
	if f != nil && fv.serverDrafts[accountName] == f.Name {
		return ServerDraftsLabel
	}
	if f == nil {
		return ""
	}
	return f.DisplayName()
}

// SetOnDraftHover registers a callback fired when the cursor moves onto a local draft.
func (fv *FolderView) SetOnDraftHover(fn func(draft.Meta)) { fv.onDraftHover = fn }

// SetOnHover registers a callback fired when the cursor moves onto a folder.
func (fv *FolderView) SetOnHover(fn func(*mail.Folder)) { fv.onHover = fn }

// SetOnAccountSwitch registers a callback fired when the active account changes.
func (fv *FolderView) SetOnAccountSwitch(fn func(string)) { fv.onAccountSwitch = fn }

// SetFocusFunc registers a callback invoked when this view gains focus.
func (fv *FolderView) SetFocusFunc(fn func()) { fv.focusFunc = fn }

// SetBlurFunc registers a callback invoked when this view loses focus.
func (fv *FolderView) SetBlurFunc(fn func()) { fv.blurFunc = fn }

// Focus overrides Box.Focus to fire the registered focusFunc.
func (fv *FolderView) Focus(delegate func(tview.Primitive)) {
	if fv.focusFunc != nil {
		fv.focusFunc()
	}
	fv.TreeView.Focus(delegate)
}

// Blur overrides Box.Blur to fire the registered blurFunc.
func (fv *FolderView) Blur() {
	if fv.blurFunc != nil {
		fv.blurFunc()
	}
	fv.TreeView.Blur()
}

// ActiveAccountName returns the name of the currently displayed account.
func (fv *FolderView) ActiveAccountName() string {
	if len(fv.accounts) == 0 {
		return ""
	}
	return fv.accounts[fv.activeIdx].name
}

// AccountCount returns the number of tracked accounts (for tests).
func (fv *FolderView) AccountCount() int { return len(fv.accounts) }

// SwitchAccount moves the active tab by delta (+1 or -1), wrapping around.
func (fv *FolderView) SwitchAccount(delta int) {
	if len(fv.accounts) == 0 {
		return
	}
	fv.activeIdx = (fv.activeIdx + delta + len(fv.accounts)) % len(fv.accounts)
	fv.renderActive()
	if fv.onAccountSwitch != nil {
		fv.onAccountSwitch(fv.accounts[fv.activeIdx].name)
	}
}

// SeedAccounts creates an empty tab per name, in order, so every configured
// account is visible before (or without) its folders arriving. The first is
// active. Accounts that already have a tab are left untouched.
func (fv *FolderView) SeedAccounts(names []string) {
	for _, name := range names {
		if fv.tab(name) == nil {
			fv.accounts = append(fv.accounts, accountTab{name: name})
		}
	}
	fv.renderActive()
}

func (fv *FolderView) tab(accountName string) *accountTab {
	for i := range fv.accounts {
		if fv.accounts[i].name == accountName {
			return &fv.accounts[i]
		}
	}
	return nil
}

// SetActiveAccount switches to the named tab and fires onAccountSwitch. It
// reports false, and does nothing, if name is unknown or already active.
func (fv *FolderView) SetActiveAccount(name string) bool {
	for i := range fv.accounts {
		if fv.accounts[i].name != name {
			continue
		}
		if i == fv.activeIdx {
			return false
		}
		fv.activeIdx = i
		fv.renderActive()
		if fv.onAccountSwitch != nil {
			fv.onAccountSwitch(name)
		}
		return true
	}
	return false
}

// Folders returns the folder list held for accountName (nil if unknown).
func (fv *FolderView) Folders(accountName string) []*mail.Folder {
	if tab := fv.tab(accountName); tab != nil {
		return tab.folders
	}
	return nil
}

// Counts returns the last-known unseen and total counts for a folder.
func (fv *FolderView) Counts(accountName, folderName string) (unseen, total uint32, ok bool) {
	tab := fv.tab(accountName)
	if tab == nil {
		return 0, 0, false
	}
	c, ok := tab.counts[folderName]
	return c.unseen, c.total, ok
}

// SelectFolder moves the cursor to the named folder of the active account
// without firing onHover or onSelect.
func (fv *FolderView) SelectFolder(folderName string) bool {
	node, ok := fv.nodeMap[folderName]
	if !ok {
		return false
	}
	fv.SetCurrentNode(node)
	return true
}

// SetFolders upserts the folder list for accountName and re-renders the
// active tab. Counts already known for a folder are kept when the new list
// brings none for it.
func (fv *FolderView) SetFolders(accountName string, folders []*mail.Folder, draftStore *draft.Store) {
	tab := fv.tab(accountName)
	if tab == nil {
		fv.accounts = append(fv.accounts, accountTab{name: accountName})
		tab = &fv.accounts[len(fv.accounts)-1]
	}
	if tab.counts == nil {
		tab.counts = make(map[string]folderCounts)
	}
	for _, f := range folders {
		if f.Status != nil {
			tab.counts[f.Name] = folderCounts{f.Status.Unseen, f.Status.Messages}
		} else if c, ok := tab.counts[f.Name]; ok {
			f.Status = &mail.MailboxStatus{Unseen: c.unseen, Messages: c.total}
		}
	}
	tab.folders = folders
	tab.draftStore = draftStore
	fv.renderActive()
}

func (fv *FolderView) renderActive() {
	if len(fv.accounts) > 0 {
		active := fv.accounts[fv.activeIdx]
		// Re-rendering the same account keeps the cursor where it was; a
		// different account starts at its root.
		keepFolder, keepDraft := "", ""
		if fv.rendered == active.name {
			if node := fv.GetCurrentNode(); node != nil {
				switch ref := node.GetReference().(type) {
				case *mail.Folder:
					keepFolder = ref.Name
				case *draft.Meta:
					keepDraft = ref.ID
				}
			}
		}
		fv.renderTree(active.name, active.folders, active.draftStore)
		fv.rendered = active.name
		switch {
		case keepFolder != "":
			fv.SelectFolder(keepFolder)
		case keepDraft != "":
			fv.selectDraft(keepDraft)
		}
	}
}

func (fv *FolderView) renderTree(accountName string, folders []*mail.Folder, draftStore *draft.Store) {
	fv.nodeMap = make(map[string]*tview.TreeNode)
	fv.folderMap = make(map[string]*mail.Folder)
	fv.draftParent = nil

	root := tview.NewTreeNode("[::b]" + accountName + "[::-]")
	root.SetColor(fv.skin.Color("accent"))
	fv.SetRoot(root)
	fv.SetCurrentNode(root)

	nodeMap := map[string]*tview.TreeNode{"": root}

	for _, f := range folders {
		if f.IsNoSelect() {
			continue
		}
		fv.folderMap[f.Name] = f

		parts := strings.Split(f.Name, f.Delimiter)
		parentKey := strings.Join(parts[:len(parts)-1], f.Delimiter)
		parent, ok := nodeMap[parentKey]
		if !ok {
			parent = root
		}
		label := fv.folderLabel(f)
		node := tview.NewTreeNode(label)
		node.SetReference(f)
		node.SetColor(fv.skin.Color("foreground"))
		if f.Status != nil && f.Status.Unseen > 0 {
			node.SetColor(fv.skin.Color("unread"))
		}
		node.SetSelectable(true)
		node.SetExpanded(true)
		parent.AddChild(node)
		nodeMap[f.Name] = node
		fv.nodeMap[f.Name] = node
	}

	// Local Drafts node
	if draftStore != nil {
		drafts, _ := draftStore.List(accountName)
		label := "Local Drafts"
		if len(drafts) > 0 {
			label = fmt.Sprintf("[::b]Local Drafts (%d)[::-]", len(drafts))
		}
		draftParent := tview.NewTreeNode(label)
		draftParent.SetColor(fv.skin.Color("accent"))
		draftParent.SetSelectable(false)
		draftParent.SetExpanded(true)
		root.AddChild(draftParent)
		fv.draftParent = draftParent
		for i := range drafts {
			dm := &drafts[i]
			subj := dm.Subject
			if subj == "" {
				subj = "(no subject)"
			}
			entry := tview.NewTreeNode(subj)
			entry.SetReference(dm)
			entry.SetColor(fv.skin.Color("foreground"))
			entry.SetSelectable(true)
			draftParent.AddChild(entry)
		}
	}

	fv.SetSelectedFunc(func(node *tview.TreeNode) {
		if f, ok := node.GetReference().(*mail.Folder); ok && fv.onSelect != nil {
			fv.onSelect(f)
		}
	})
}

// UpdateBadge stores the counts for a folder of accountName, whether or not
// that account is the one on screen, and relabels its node if it is visible.
func (fv *FolderView) UpdateBadge(accountName, folderName string, unseen, total uint32) {
	tab := fv.tab(accountName)
	if tab == nil {
		return
	}
	var f *mail.Folder
	for _, cand := range tab.folders {
		if cand.Name == folderName {
			f = cand
			break
		}
	}
	if f == nil {
		return
	}
	if tab.counts == nil {
		tab.counts = make(map[string]folderCounts)
	}
	tab.counts[folderName] = folderCounts{unseen, total}
	if f.Status == nil {
		f.Status = &mail.MailboxStatus{}
	}
	f.Status.Unseen = unseen
	f.Status.Messages = total

	if accountName != fv.ActiveAccountName() {
		return
	}
	node, ok := fv.nodeMap[folderName]
	if !ok {
		return
	}
	node.SetText(fv.folderLabel(f))
	if unseen > 0 {
		node.SetColor(fv.skin.Color("unread"))
	} else {
		node.SetColor(fv.skin.Color("foreground"))
	}
}

// UpdateDraftBadge refreshes the Local Drafts count label.
func (fv *FolderView) UpdateDraftBadge(accountName string, draftStore *draft.Store) {
	if fv.draftParent == nil || draftStore == nil {
		return
	}
	drafts, _ := draftStore.List(accountName)
	label := "Local Drafts"
	if len(drafts) > 0 {
		label = fmt.Sprintf("[::b]Local Drafts (%d)[::-]", len(drafts))
	}
	fv.draftParent.SetText(label)
}

// SelectedDraft returns the local draft under the cursor, or nil.
func (fv *FolderView) SelectedDraft() *draft.Meta {
	if node := fv.GetCurrentNode(); node != nil {
		if dm, ok := node.GetReference().(*draft.Meta); ok {
			return dm
		}
	}
	return nil
}

// selectDraft moves the cursor to the local draft with id, without callbacks.
func (fv *FolderView) selectDraft(id string) {
	if fv.draftParent == nil {
		return
	}
	for _, node := range fv.draftParent.GetChildren() {
		if dm, ok := node.GetReference().(*draft.Meta); ok && dm.ID == id {
			fv.SetCurrentNode(node)
			return
		}
	}
}

// RefreshDrafts re-reads the active account's local drafts into the tree.
func (fv *FolderView) RefreshDrafts() { fv.renderActive() }

// NodeForFolder returns the tree node for the given folder name (for tests).
func (fv *FolderView) NodeForFolder(folderName string) *tview.TreeNode {
	return fv.nodeMap[folderName]
}

func (fv *FolderView) folderLabel(f *mail.Folder) string {
	name := fv.DisplayName(fv.ActiveAccountName(), f)
	if f.Status != nil && f.Status.Unseen > 0 {
		return fmt.Sprintf("[::b]%s (%d)[::-]", name, f.Status.Unseen)
	}
	return name
}
