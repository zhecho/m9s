package view

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	"github.com/zhecho/m9s/internal/store"
	msync "github.com/zhecho/m9s/internal/sync"
	"github.com/zhecho/m9s/internal/ui"
	"github.com/zhecho/m9s/internal/version"
)

const pageMain = "main"
const pageReader = "reader"
const pageLog = "log"

// folderHoverDelay is how long the folder cursor must rest on a folder before
// its mail list is fetched, so holding j/k does not fetch every folder passed.
const folderHoverDelay = 150 * time.Millisecond

// RootApp wires all views and owns application state.
type RootApp struct {
	uiApp      *ui.App
	cfg        *config.Config
	manager    *mail.Manager
	engines    map[string]msync.Account
	draftStore *draft.Store
	sendFn     func(smtpadapter.Draft, string, string)

	// store is the local mail copy: a folder's list is painted from it at
	// once and refreshed when the server answers. mirror is how far each
	// account's background copy has come.
	store  *store.Store
	mirror map[string]msync.MirrorProgress
	// listing is the last listing report of each account's folders (API
	// accounts); listReload is when the open list was last reloaded for one.
	listing    map[string]map[string]msync.ListingProgress
	listReload time.Time
	// started is where each running listing or copy began, for the time
	// left; now is the clock (tests set it).
	started map[string]progressStart
	now     func() time.Time

	// testModalDone is set by showSendModal for use in tests.
	testModalDone func(int, string)
	// testDeleteModalDone and testDeleteModalText are set by showDeleteModal for use in tests.
	testDeleteModalDone func(int, string)
	testDeleteModalText string
	testDeleteButtons   []ui.DialogButton
	testDeleteFocus     int
	// testModalButtons is set by showSendModal for use in tests.
	testModalButtons []string

	labelPicker *LabelPicker // the open L picker, if any

	folders    *FolderView
	maillist   *MailListView
	preview    *PreviewView
	reader     *ReaderView
	logView    *tview.TextView
	logPage    *tview.Flex // the log page: the / prompt row on demand, then logView
	logFilter  string      // the log page shows only lines with this text
	help       *HelpView
	helpReturn tview.Primitive // focus to give back when help closes
	logReturn  tview.Primitive // focus to give back when the log closes
	movePicker *FolderPicker   // the m folder picker while it is open

	configPath string             // the config file :config edits; "" disables it
	editFile   func(string) error // runs the editor on a file (tests replace it)
	restart    bool               // :config saved a new config: m9s restarts on exit
	selector   *ui.Selector

	currentFolder *mail.Folder
	currentMsg    *mail.Message
	selectedEnv   *mail.Envelope // row selected in the mail list

	// conv is the selected row's conversation once it has arrived; then
	// currentMsg is its message that reply and forward act on.
	conv *conversation

	// status is the last connection state reported per account; pane is the
	// focused pane, which decides the header's last hint column.
	status map[string]msync.AccountStatus
	pane   paneKind

	// server is each account's remote server, as its engine reported it.
	server map[string]string

	// The : and / prompt: a row between header and panes, shown on demand.
	root          *tview.Flex
	promptHost    *tview.Flex // the page the prompt row is on: root or logPage
	prompt        *ui.Prompt
	promptOpen    bool
	promptCommand bool // true for the : prompt, false for the / filter

	split      *ui.SplitPane
	layoutPath string // where a dragged layout is remembered; "" = not remembered

	// bodyLoader fetches a message body in the background; tests replace it.
	reloading  bool         // the mail list is being refilled by showEnvelopes
	search     *searchState // server search results on screen, or nil
	deliver    func(func()) // tests: where search answers are queued
	searchSeq  int          // identifies the latest search asked for
	bodyLoader func(acct *mail.Account, folder *mail.Folder, env *mail.Envelope, markSeen bool)

	// browser holds the pages handed to the web browser; openBrowser shows
	// one (tests replace it). browserPending is the message the user asked to
	// see there while its body was still downloading.
	browser        browserFiles
	openBrowser    func(path string) error
	browserPending *mail.Envelope

	// attachPicker and attachDir are the open attachment overlays, if any;
	// attachPending is a save or open waiting for its message's body.
	attachPicker  *AttachmentPicker
	attachDir     *tview.InputField
	attachPending *attachRequest
	lastSaveDir   string // the folder typed for the last save this session

	// hoverSeq and hoverTimer debounce folder-cursor movement; both are only
	// touched from the UI goroutine.
	hoverSeq   int
	hoverTimer *time.Timer
	hoverDelay time.Duration
}

// NewRootApp builds the three-pane layout and wires all views.
func NewRootApp(uiApp *ui.App, cfg *config.Config, manager *mail.Manager,
	engines map[string]msync.Account, draftStore *draft.Store, events <-chan msync.Event) *RootApp {

	ra := &RootApp{
		uiApp:      uiApp,
		cfg:        cfg,
		manager:    manager,
		engines:    engines,
		draftStore: draftStore,
		store:      store.NewMemory(),
		now:        time.Now,
	}
	ra.sendFn = ra.sendDraft
	ra.openBrowser = openInDefaultBrowser
	ra.bodyLoader = func(acct *mail.Account, folder *mail.Folder, env *mail.Envelope, markSeen bool) {
		go ra.loadBody(acct, folder, env, markSeen, ra.conversationRowsOf(acct, folder))
	}

	ra.hoverDelay = folderHoverDelay
	ra.folders = NewFolderView(uiApp.Skin, ra.onFolderSelected)
	ra.folders.SetOnHover(ra.onFolderHover)
	ra.folders.SetOnDraftHover(ra.onDraftHover)
	ra.maillist = NewMailListView(uiApp.Skin, cfg.UI.DateFormat, ra.onEnvelopeSelected)
	ra.maillist.SetMarksChangedFunc(ra.refreshMailListTitle)
	ra.preview = NewPreviewView(uiApp.Skin)
	ra.reader = NewReaderView(uiApp.Skin, ra.closeReader)
	ra.logView = ra.buildLogView()
	ra.help = NewHelpView(uiApp.Skin, ra.closeHelp)
	ra.editFile = ra.editInTerminal

	// Mail list on top showing mail_list_rows messages, preview below it with
	// the rest; the border between them can be dragged with the mouse.
	rows := cfg.UI.MailListRows
	if rows <= 0 {
		rows = 10
	}
	ra.split = ui.NewSplitPane(ra.maillist, ra.preview, rows)
	rightPane := ra.split

	// No account tab row: the active account is in the header and at the
	// root of the folder tree; [ ] and : switch accounts.
	mainArea := tview.NewFlex().
		AddItem(ra.folders, 22, 0, true).
		AddItem(rightPane, 0, 1, false)

	ra.prompt = ui.NewPrompt(uiApp.Skin)
	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(uiApp.Header, ui.HeaderHeight, 0, false).
		AddItem(mainArea, 0, 1, true).
		AddItem(uiApp.StatusBar, 1, 0, false)
	ra.root = root

	uiApp.Pages.AddPage(pageMain, root, true, true)
	uiApp.Pages.AddPage(pageReader, ra.reader, true, false)
	ra.logPage = tview.NewFlex().SetDirection(tview.FlexRow).AddItem(ra.logView, 0, 1, true)
	uiApp.Pages.AddPage(pageLog, ra.logPage, true, false)
	uiApp.Pages.AddPage(pageHelp, ra.help, true, false)
	uiApp.SetRoot(uiApp.Pages, true)

	names := make([]string, 0, len(manager.All()))
	for _, a := range manager.All() {
		names = append(names, a.Name)
	}
	ra.folders.SeedAccounts(names)

	ra.folders.SetOnAccountSwitch(ra.switchAccount)

	ra.wireFocusCallbacks()
	// SetRoot already focused the Folders pane, before its focus callback
	// existed: focus it again so it opens highlighted, with its own hints.
	uiApp.SetFocus(ra.folders)
	ra.wireKeys()
	ra.wireSelection()
	ra.refreshHints()
	ra.refreshHeader()
	go ra.dispatchEvents(events)
	return ra
}

// wireSelection lets the mouse select text anywhere on screen; what is
// selected is copied to the clipboard. Called after wireKeys: it wraps the
// app's key capture.
func (ra *RootApp) wireSelection() {
	inner := func(b interface{ GetInnerRect() (int, int, int, int) }) ui.Rect {
		x, y, w, h := b.GetInnerRect()
		return ui.Rect{X: x, Y: y, W: w, H: h}
	}
	sel := &ui.Selector{
		Regions: func() []ui.Rect {
			switch page, _ := ra.uiApp.Pages.GetFrontPage(); page {
			case pageMain:
				return []ui.Rect{inner(ra.folders), inner(ra.maillist), inner(ra.preview),
					inner(ra.uiApp.Header), inner(ra.uiApp.StatusBar)}
			case pageReader:
				return []ui.Rect{inner(ra.reader)}
			case pageLog:
				return []ui.Rect{inner(ra.logView)}
			case pageHelp:
				return []ui.Rect{inner(ra.help.body), inner(ra.help.toc)}
			}
			return nil // a dialog: the whole screen
		},
		Busy: ra.split.Dragging,
	}
	sel.OnCopied = func(text string, err error) {
		if err != nil {
			ui.AppLog.Warn("copy to clipboard: %v", err)
			ra.uiApp.StatusBar.Error("Copy failed: " + err.Error())
			return
		}
		ra.uiApp.StatusBar.Info(fmt.Sprintf("Copied %d characters", len([]rune(text))))
	}
	sel.Install(ra.uiApp.Application)
	ra.selector = sel
}

func (ra *RootApp) wireFocusCallbacks() {
	inactiveTitleColor := ra.uiApp.Skin.Color("header_fg")

	ra.folders.SetFocusFunc(func() {
		ra.folders.SetTitle(" [::b]FOLDERS[::-] ")
		ra.folders.SetTitleColor(tcell.NewRGBColor(255, 255, 0))
		ra.setPane(paneFolders)
	})
	ra.folders.SetBlurFunc(func() {
		ra.folders.SetTitle(" FOLDERS ")
		ra.folders.SetTitleColor(inactiveTitleColor)
	})

	ra.maillist.SetFocusFunc(func() {
		ra.maillist.SetTitle(" [::b]" + tview.Escape(ra.maillist.TitleText()) + "[::-] ")
		ra.maillist.SetTitleColor(tcell.NewRGBColor(255, 255, 0))
		ra.markSelectedSeen()
		ra.setPane(paneMailList)
	})
	ra.maillist.SetBlurFunc(func() {
		ra.maillist.SetTitle(" " + tview.Escape(ra.maillist.TitleText()) + " ")
		ra.maillist.SetTitleColor(inactiveTitleColor)
	})

	ra.preview.SetFocusFunc(func() {
		ra.preview.SetTitle(" [::b]PREVIEW[::-] ")
		ra.preview.SetTitleColor(tcell.NewRGBColor(255, 255, 0))
		ra.setPane(panePreview)
	})
	ra.preview.SetBlurFunc(func() {
		ra.preview.SetTitle(" PREVIEW ")
		ra.preview.SetTitleColor(inactiveTitleColor)
	})
}

func (ra *RootApp) setPane(pane paneKind) {
	ra.pane = pane
	ra.refreshHints()
}

// refreshHints shows the general keys plus the focused pane's keys, or only
// the prompt's own keys while it is open.
func (ra *RootApp) refreshHints() {
	if ra.promptOpen {
		ra.uiApp.Header.SetHints(promptHints(ra.promptCommand), nil)
		return
	}
	km := ra.uiApp.KeyMap
	ra.uiApp.Header.SetHints(generalHints(km), paneHints(km, ra.pane, ra.canEdit()))
}

// headerUser is the address the top bar shows: gmail.user or
// microsoft.user for an API account (which has no imap block), else
// imap.user.
func headerUser(cfg *config.Account) string {
	switch {
	case cfg.IsMicrosoft():
		return cfg.Microsoft.User
	case cfg.IsGmail():
		return cfg.Gmail.User
	}
	return cfg.IMAP.User
}

// refreshHeader recomputes the header's info block and the breadcrumbs from
// the current state. UI goroutine only.
func (ra *RootApp) refreshHeader() {
	acct := ra.manager.Active()
	if acct == nil {
		ra.uiApp.Header.SetInfo(ui.HeaderInfo{Status: "no accounts configured", StatusRole: "flash_err"})
		ra.uiApp.StatusBar.SetCrumbs()
		return
	}
	info := ui.HeaderInfo{Account: acct.Name, Version: version.String()}
	if acct.Config != nil {
		info.User = headerUser(acct.Config)
		info.Provider = acct.Config.ProviderName()
	}
	if ra.currentFolder != nil {
		info.Folder = ra.currentFolder.Name
		if label := ra.folders.DisplayName(acct.Name, ra.currentFolder); label == ServerDraftsLabel {
			info.Folder = label
		}
		info.Unseen, info.Total, info.HasCounts = ra.folders.Counts(acct.Name, ra.currentFolder.Name)
	}
	info.Server = ra.server[acct.Name]
	st, known := ra.status[acct.Name]
	info.Status, info.StatusRole = statusLine(st, known)
	if known && st.State == msync.StateConnected {
		if p, ok := ra.listingShown(acct.Name); ok {
			info.Status += fmt.Sprintf(" · syncing %s %d/%d (%d%%)", p.FolderName, p.Stored, p.Total, percent(p.Stored, p.Total)) +
				ra.timeLeft(listingKey(p), p.Stored, p.Total)
		}
		if p, copying := ra.mirror[acct.Name]; copying && !p.Done {
			info.Status += fmt.Sprintf(" · saving mail %d/%d (%d%%)", p.Downloaded, p.Messages, percent(p.Downloaded, p.Messages)) +
				ra.timeLeft(mirrorKey(p.AccountName), p.Downloaded, p.Messages)
		}
	}
	ra.uiApp.Header.SetInfo(info)

	filter := ""
	if q := ra.maillist.FilterQuery(); q != "" {
		filter = "/" + q
	}
	ra.uiApp.StatusBar.SetCrumbs(acct.Name, info.Folder, filter)
}

// switchAccount is the single path for every account switch ([ ], :name).
// It drops everything belonging to the previous account and opens the new
// account's INBOX, as at startup. A remembered INBOX is on screen at once.
func (ra *RootApp) switchAccount(name string) {
	if active := ra.manager.Active(); active == nil || active.Name == name {
		return
	}
	if ra.folders.ActiveAccountName() != name {
		// Bring the folder pane along first; it calls back into switchAccount.
		ra.folders.SetActiveAccount(name)
		return
	}
	ra.hoverSeq++ // a pending hover belongs to the previous account
	ra.manager.SetActive(name)
	ra.clearOpenFolder()
	ra.openFolderByName("INBOX")
	ra.refreshHeader()
}

// clearOpenFolder leaves no folder open: empty list, empty preview, nothing
// selected for reply or delete.
func (ra *RootApp) clearOpenFolder() {
	ra.currentFolder = nil
	ra.currentMsg = nil
	ra.conv = nil
	ra.selectedEnv = nil
	ra.dropSearch()
	ra.maillist.SetEnvelopes(nil)
	ra.refreshMailListTitle()
}

// refreshMailListTitle re-renders the mail list title, which carries the filter.
func (ra *RootApp) refreshMailListTitle() {
	title := tview.Escape(ra.maillist.TitleText())
	if ra.mailListFocused() {
		ra.maillist.SetTitle(" [::b]" + title + "[::-] ")
	} else {
		ra.maillist.SetTitle(" " + title + " ")
	}
}

// openFolderByName opens the named folder of the active account and moves the
// folder cursor onto it. It reports false if there is no such folder.
func (ra *RootApp) openFolderByName(name string) bool {
	acct := ra.manager.Active()
	if acct == nil {
		return false
	}
	for _, f := range ra.folders.Folders(acct.Name) {
		if f.Name == name && !f.IsNoSelect() {
			if !ra.folders.SelectFolder(name) {
				return false
			}
			ra.onFolderSelected(f)
			return true
		}
	}
	return false
}

// openPrompt shows the prompt row under the header (on the log page: at its
// top) and gives it focus. While it is open every global shortcut is
// suspended (see wireKeys).
func (ra *RootApp) openPrompt(command bool, cfg ui.PromptConfig) {
	if ra.promptOpen {
		return
	}
	switch page, _ := ra.uiApp.Pages.GetFrontPage(); {
	case page == pageMain:
		ra.promptHost = ra.root
		ra.root.AddItemAtIndex(1, ra.prompt, ui.PromptHeight, 0, false)
	case page == pageLog && ra.logPage != nil && !command:
		ra.promptHost = ra.logPage
		ra.logPage.AddItemAtIndex(0, ra.prompt, ui.PromptHeight, 0, false)
	default:
		return // other pages have no prompt
	}
	ra.promptOpen = true
	ra.promptCommand = command
	ra.prompt.Open(cfg)
	ra.refreshHints()
	ra.uiApp.SetFocus(ra.prompt)
}

// closePrompt hides the prompt row and hands focus to the given pane.
func (ra *RootApp) closePrompt(focus tview.Primitive) {
	if !ra.promptOpen {
		return
	}
	ra.promptOpen = false
	ra.promptHost.RemoveItem(ra.prompt)
	ra.refreshHints()
	if focus == nil {
		focus = ra.folders
	}
	ra.uiApp.SetFocus(focus)
}

// startFilter opens the / prompt. The mail list narrows as the user types;
// Enter keeps the filter, moves to the list and, on accounts that can, asks
// the server to search for the same text; Esc clears it.
func (ra *RootApp) startFilter() {
	previous := ra.uiApp.GetFocus()
	keepID := "" // typing narrows the list and moves the selection: note it now
	if ra.selectedEnv != nil {
		keepID = ra.selectedEnv.ID
	}
	ra.openPrompt(false, ui.PromptConfig{
		Label:  "filter",
		Prefix: "/",
		OnChange: func(text string) {
			ra.maillist.SetFilter(text)
			ra.refreshMailListTitle()
			ra.refreshHeader()
		},
		OnDone: func(text string, accepted bool) {
			if accepted {
				ra.closePrompt(ra.maillist)
				ra.refreshMailListTitle() // focus changed after the title was last set
				ra.startSearch(text, keepID)
				return
			}
			ra.leaveSearch()
			ra.clearFilter()
			ra.closePrompt(previous)
		},
	})
}

// startCommand opens the : prompt, which switches to an account or a folder
// of the active account by name.
func (ra *RootApp) startCommand() {
	previous := ra.uiApp.GetFocus()
	accounts := make([]string, 0, len(ra.manager.All()))
	for _, a := range ra.manager.All() {
		accounts = append(accounts, a.Name)
	}
	folders := ra.folders.Folders(ra.folders.ActiveAccountName())

	ra.openPrompt(true, ui.PromptConfig{
		Label:  "command",
		Prefix: ":",
		Suggest: func(text string) string {
			return suggestCommand(text, accounts, folders)
		},
		OnDone: func(text string, accepted bool) {
			ra.closePrompt(previous)
			if accepted {
				ra.runCommand(text, accounts, folders)
			}
		},
	})
}

func (ra *RootApp) runCommand(text string, accounts []string, folders []*mail.Folder) {
	target, err := resolveCommand(text, accounts, folders)
	if err != nil {
		ra.uiApp.StatusBar.Error(err.Error())
		return
	}
	switch target.Kind {
	case cmdAccount:
		ra.switchAccount(target.Name)
	case cmdFolder:
		ra.openFolderByName(target.Name)
	case cmdQuit:
		ra.uiApp.Stop()
	case cmdHelp:
		ra.showHelp()
	case cmdConfig:
		ra.editConfig()
	}
}

// showHelp opens the full-screen list of shortcuts over the main page.
func (ra *RootApp) showHelp() {
	ra.helpReturn = ra.uiApp.GetFocus()
	ra.help.SetKeys(ra.uiApp.KeyMap)
	ra.uiApp.Pages.SwitchToPage(pageHelp)
	ra.uiApp.SetFocus(ra.help)
}

// closeHelp goes back to the main page and the pane that had focus.
func (ra *RootApp) closeHelp() {
	ra.uiApp.Pages.SwitchToPage(pageMain)
	focus := ra.helpReturn
	if focus == nil {
		focus = ra.folders
	}
	ra.helpReturn = nil
	ra.uiApp.SetFocus(focus)
}

// clearFilter shows the whole mail list again.
func (ra *RootApp) clearFilter() {
	ra.searchSeq++ // a search still running was cancelled
	ra.maillist.SetFilter("")
	ra.refreshMailListTitle()
	ra.refreshHeader()
}

func (ra *RootApp) wireKeys() {
	ra.uiApp.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		// When a modal is present, pass all events through to it.
		// This prevents Tab/keys from escaping to other panes.
		if ra.uiApp.Pages.HasPage("send-modal") || ra.uiApp.Pages.HasPage("delete-modal") || ra.uiApp.Pages.HasPage("label-picker") ||
			ra.uiApp.Pages.HasPage(pageAttachPicker) || ra.uiApp.Pages.HasPage(pageAttachDir) ||
			ra.uiApp.Pages.HasPage(pageMovePicker) {
			return event
		}
		// Likewise while the : or / prompt is open: typing q types a q.
		if ra.promptOpen {
			return event
		}
		// The help window handles its own keys: q and Esc close it.
		if page, _ := ra.uiApp.Pages.GetFrontPage(); page == pageHelp {
			return event
		}
		// So does the log page (j/k scroll, q and Esc close it): keys meant
		// for the main page must not act behind it, and q must not quit.
		if page, _ := ra.uiApp.Pages.GetFrontPage(); page == pageLog {
			switch ra.uiApp.KeyMap.Lookup(event) {
			case ui.ActionSearchLocal:
				ra.startLogFilter()
				return nil
			case ui.ActionLog, ui.ActionBack:
				ra.closeLog()
				return nil
			}
			return event
		}
		// Esc in the mail list unmarks, leaves a search, or drops an active
		// filter, in that order.
		if event.Key() == tcell.KeyEscape && ra.mailListFocused() && len(ra.maillist.Marked()) > 0 {
			ra.maillist.ClearMarks()
			return nil
		}
		if event.Key() == tcell.KeyEscape && ra.mailListFocused() && ra.search != nil {
			ra.leaveSearch()
			return nil
		}
		if event.Key() == tcell.KeyEscape && ra.mailListFocused() && ra.maillist.FilterQuery() != "" {
			ra.clearFilter()
			return nil
		}
		if page, _ := ra.uiApp.Pages.GetFrontPage(); page == pageReader && ra.conv != nil {
			switch event.Key() {
			case tcell.KeyTab:
				ra.conv.move(1)
				ra.currentMsg = ra.conv.target()
				ra.reader.ShowConversation(ra.conv, true)
				return nil
			case tcell.KeyBacktab:
				ra.conv.move(-1)
				ra.currentMsg = ra.conv.target()
				ra.reader.ShowConversation(ra.conv, true)
				return nil
			case tcell.KeyEnter:
				ra.conv.toggle()
				ra.currentMsg = ra.conv.target()
				ra.reader.ShowConversation(ra.conv, true)
				return nil
			}
		}
		action := ra.uiApp.KeyMap.Lookup(event)
		switch action {
		case ui.ActionOpen:
			if ra.pane == paneFolders {
				return event // opens a folder
			}
			if page, _ := ra.uiApp.Pages.GetFrontPage(); page == pageMain && ra.openReader() {
				return nil
			}
			return event
		case ui.ActionMarkUnread, ui.ActionStar, ui.ActionArchive:
			if ra.conversationAction(action) {
				return nil
			}
			switch {
			case action == ui.ActionMarkUnread && ra.markSelectedUnread(),
				action == ui.ActionStar && ra.starSelected(),
				action == ui.ActionArchive && ra.archiveSelected():
				return nil
			}
			return event
		case ui.ActionMove:
			if ra.pane == paneFolders {
				return event // a folder is not a message
			}
			ra.openMovePicker()
			return nil
		case ui.ActionMark:
			if !ra.mailListFocused() {
				return event
			}
			ra.maillist.MarkCursor()
			return nil
		case ui.ActionForward:
			if ra.currentMsg != nil {
				ra.startForward(ra.currentMsg)
			}
			return nil
		case ui.ActionLabels:
			ra.openLabelPicker()
			return nil
		case ui.ActionSearchLocal:
			ra.startFilter()
			return nil
		case ui.ActionCommandBar:
			ra.startCommand()
			return nil
		case ui.ActionHelp:
			if page, _ := ra.uiApp.Pages.GetFrontPage(); page != pageMain {
				return event // help opens over the main page only, as :help
			}
			ra.showHelp()
			return nil
		case ui.ActionBack:
			page, _ := ra.uiApp.Pages.GetFrontPage()
			if page == pageReader {
				ra.closeReader()
				return nil
			}
			ra.uiApp.Stop()
			return nil
		case ui.ActionSync:
			ra.syncAll()
			return nil
		case ui.ActionSwitchAccountPrev:
			ra.folders.SwitchAccount(-1)
			return nil
		case ui.ActionSwitchAccountNext:
			ra.folders.SwitchAccount(1)
			return nil
		case ui.ActionEdit:
			ra.editSelected()
			return nil
		case ui.ActionOpenBrowser:
			if ra.pane == paneFolders {
				return event // a folder is not a message
			}
			ra.openSelectedInBrowser()
			return nil
		case ui.ActionSaveAttachments, ui.ActionOpenAttachment:
			if ra.pane == paneFolders {
				return event // a folder has no attachments
			}
			ra.attachKey(action)
			return nil
		case ui.ActionCompose:
			ra.launchEditor(smtpadapter.Draft{From: ra.selfAddress()})
			return nil
		case ui.ActionReply:
			if ra.currentMsg != nil {
				ra.startReply(ra.currentMsg, false)
			}
			return nil
		case ui.ActionReplyAll:
			if ra.currentMsg != nil {
				ra.startReply(ra.currentMsg, true)
			}
			return nil
		case ui.ActionTabCycle:
			ra.cycleFocus()
			return nil
		case ui.ActionLog:
			ra.toggleLog()
			return nil
		case ui.ActionDelete:
			ra.deleteMessage()
			return nil
		}
		return event
	})
}

// syncAll handles the sync key: on a healthy account it refreshes all folder
// counts; on a failed one it retries the connection now.
func (ra *RootApp) syncAll() {
	acct := ra.manager.Active()
	if acct == nil {
		return
	}
	eng, hasEngine := ra.engines[acct.Name]
	if st, known := ra.status[acct.Name]; known && st.State == msync.StateFailed {
		if hasEngine {
			eng.Retry()
			ra.uiApp.StatusBar.Info("Reconnecting…")
		}
		return
	}
	if !hasEngine {
		return
	}
	ra.uiApp.StatusBar.Info("Syncing...")
	go func() {
		eng.SyncAll(context.Background())
		ra.uiApp.QueueUpdateDraw(func() {
			ra.uiApp.StatusBar.Info("Sync complete")
		})
	}()
}

func (ra *RootApp) cycleFocus() {
	focused := ra.uiApp.GetFocus()
	switch focused {
	case ra.folders:
		ra.uiApp.SetFocus(ra.maillist)
	case ra.maillist:
		ra.uiApp.SetFocus(ra.preview)
	default:
		ra.uiApp.SetFocus(ra.folders)
	}
}

func (ra *RootApp) closeReader() {
	ra.uiApp.Pages.SwitchToPage(pageMain)
	ra.uiApp.SetFocus(ra.maillist)
}

func (ra *RootApp) buildLogView() *tview.TextView {
	tv := tview.NewTextView()
	tv.SetBorder(true)
	tv.SetTitle(logTitle)
	tv.SetTitleColor(ra.uiApp.Skin.Color("accent"))
	tv.SetBorderColor(ra.uiApp.Skin.Color("border"))
	tv.SetBackgroundColor(ra.uiApp.Skin.Color("background"))
	tv.SetTextColor(ra.uiApp.Skin.Color("foreground"))
	tv.SetDynamicColors(true)
	tv.SetScrollable(true)
	tv.SetWordWrap(true)

	refresh := func() {
		go ra.uiApp.QueueUpdateDraw(func() {
			if ra.logView == tv {
				ra.renderLogView()
			} else {
				renderLog(tv, ra.uiApp.Skin)
			}
		})
	}
	ui.AppLog.OnUpdate(refresh)

	// Esc clears a filter first, then closes; q closes.
	tv.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape && ra.logFilter != "" {
			ra.setLogFilter("")
			return nil
		}
		if event.Key() == tcell.KeyEscape ||
			(event.Key() == tcell.KeyRune && event.Rune() == 'q') {
			ra.closeLog()
			return nil
		}
		return event
	})
	return tv
}

// renderLog redraws tv with every log entry, newest last. UI goroutine only.
func renderLog(tv *tview.TextView, skin *ui.Skin) { renderLogFiltered(tv, skin, "") }

func (ra *RootApp) toggleLog() {
	if page, _ := ra.uiApp.Pages.GetFrontPage(); page == pageLog {
		ra.closeLog()
		return
	}
	ra.logReturn = ra.uiApp.GetFocus()
	ra.renderLogView() // refresh before showing
	ra.uiApp.Pages.SwitchToPage(pageLog)
	ra.uiApp.SetFocus(ra.logView)
}

// closeLog goes back to the main page and the pane that had focus. The log
// opens again without a filter.
func (ra *RootApp) closeLog() {
	ra.closePrompt(ra.logView)
	ra.logFilter = ""
	ra.uiApp.Pages.SwitchToPage(pageMain)
	focus := ra.logReturn
	if focus == nil || focus == tview.Primitive(ra.logView) {
		focus = ra.maillist
	}
	ra.logReturn = nil
	ra.uiApp.SetFocus(focus)
}

// onFolderHover is called when the folder cursor moves onto f. It opens the
// folder once the cursor has rested there for hoverDelay.
func (ra *RootApp) onFolderHover(f *mail.Folder) {
	defer ra.refreshHints() // e Edit applies to drafts only
	ra.hoverSeq++
	seq := ra.hoverSeq
	if ra.hoverTimer != nil {
		ra.hoverTimer.Stop()
	}
	ra.hoverTimer = time.AfterFunc(ra.hoverDelay, func() {
		ra.uiApp.QueueUpdateDraw(func() { ra.openHovered(seq, f) })
	})
}

// openHovered opens f unless the cursor has moved (or Enter was pressed)
// since the hover identified by seq was scheduled.
func (ra *RootApp) openHovered(seq int, f *mail.Folder) {
	if seq != ra.hoverSeq {
		return
	}
	ra.onFolderSelected(f)
}

func (ra *RootApp) onFolderSelected(f *mail.Folder) {
	ra.hoverSeq++ // cancel any pending hover
	if ra.currentFolder == nil || ra.currentFolder.Name != f.Name {
		ra.dropSearch() // search results belong to the folder they were asked in
		ra.maillist.ClearMarks()
		// A filter belongs to the folder it was typed for.
		if ra.maillist.FilterQuery() != "" {
			ra.maillist.SetFilter("")
			ra.refreshMailListTitle()
		}
	}
	ra.currentFolder = f
	// The previous folder's selection must not be acted on (reply, delete)
	// while this folder loads, or at all if it turns out to be empty.
	ra.currentMsg = nil
	ra.conv = nil
	ra.selectedEnv = nil
	ra.refreshHeader()
	if acct := ra.manager.Active(); acct != nil {
		// What we last saw of this folder goes on screen now; the fetch
		// below replaces it when the server answers.
		if remembered, ok := ra.remembered(acct.Name, f.Name); ok {
			ra.showEnvelopes(remembered)
		}
		if eng, ok := ra.engines[acct.Name]; ok {
			// Counts must be right when a folder opens, not a poll later.
			go eng.RefreshBadge(context.Background(), f.Name)
		}
	}
	go ra.loadEnvelopes(ra.manager.Active(), f)
}

// loadEnvelopes fetches f's message list for acct. It runs off the UI
// goroutine, so the account is captured by the caller rather than read here.
func (ra *RootApp) loadEnvelopes(acct *mail.Account, f *mail.Folder) {
	if acct == nil {
		return
	}
	eng, ok := ra.engines[acct.Name]
	if !ok {
		return
	}
	envelopes, err := eng.FetchHeaders(context.Background(), f.Name, 1, store.ListSize)
	if err != nil {
		ra.uiApp.QueueUpdateDraw(func() { ra.fetchFailed(acct.Name, "fetch failed", err) })
		return
	}
	ra.storeFetched(acct, f, envelopes)
	ra.uiApp.QueueUpdateDraw(func() { ra.showFetched(acct, f, envelopes) })
}

// storeFetched remembers a folder's freshly fetched list — also when the
// user has moved on meanwhile, so it is there when they come back. Safe off
// the UI goroutine.
func (ra *RootApp) storeFetched(acct *mail.Account, f *mail.Folder, envelopes []*mail.Envelope) {
	if ra.conversations(acct.Name, f.Name) {
		return // conversation rows are not messages
	}
	if eng, ok := ra.engines[acct.Name]; ok && msync.KeepsStore(eng) {
		return // the list came from the store, which the engine alone keeps
	}
	if err := ra.store.PutEnvelopes(acct.Name, f.Name, envelopes); err != nil {
		ui.AppLog.Warn("[%s] %s not remembered for next time: %v", acct.Name, f.Name, err)
	}
	// The server only guesses the attachment sign; a stored body knows.
	known := ra.store.KnownAttachSigns(acct.Name, f.Name)
	for _, e := range envelopes {
		if has, ok := known[e.ID]; ok {
			e.HasAttach = has
		}
	}
}

// conversations reports whether folder of account is listed one row per
// conversation.
func (ra *RootApp) conversations(account, folder string) bool {
	eng, ok := ra.engines[account]
	return ok && msync.ShowsConversations(eng, folder)
}

// remembered is what the store last held of a folder, as the list shows it.
func (ra *RootApp) remembered(account, folder string) ([]*mail.Envelope, bool) {
	if ra.conversations(account, folder) {
		// The engine lists conversations from the store: no network.
		threads, err := ra.engines[account].FetchHeaders(context.Background(), folder, 1, store.ListSize)
		return threads, err == nil && len(threads) > 0
	}
	return ra.store.Envelopes(account, folder)
}

// showFetched puts a fetched list on screen, unless the user moved to another
// folder or account while the fetch was in flight. UI goroutine only.
func (ra *RootApp) showFetched(acct *mail.Account, f *mail.Folder, envelopes []*mail.Envelope) {
	if !ra.isCurrent(acct, f) {
		return
	}
	ra.showEnvelopes(envelopes)
}

// fetchFailed reports a failed fetch. While the account is not connected the
// header already says so (and a remembered list may be on screen), so the
// error only goes to the log. UI goroutine only.
func (ra *RootApp) fetchFailed(acctName, what string, err error) {
	if st, known := ra.status[acctName]; !known || st.State != msync.StateConnected {
		ui.AppLog.Warn("[%s] %s: %v", acctName, what, err)
		return
	}
	ra.uiApp.StatusBar.Error(what + ": " + err.Error())
}

// showEnvelopes replaces the mail list's content for the open folder. The
// filter and the selected message survive: this is either the first load of
// a folder (opening a different folder has already dropped both) or a reload
// of the same one (new mail, a delete, a reconnect). Neither is the user
// choosing a message, so nothing is marked as read on the way.
//
// When the list is what is already on screen and the selected message's body
// is showing — the server confirming a remembered list — nothing is redrawn,
// so the preview does not blink.
func (ra *RootApp) showEnvelopes(envelopes []*mail.Envelope) {
	if ra.search != nil {
		return // search results stay until Esc or another folder
	}
	envelopes = mail.NewestFirst(envelopes) // the order the list shows them in
	if ra.currentMsg != nil && ra.selectedEnv != nil && sameList(ra.maillist.Envelopes(), envelopes) {
		ra.refreshHeader()
		return
	}
	var keep string
	if ra.selectedEnv != nil {
		keep = ra.selectedEnv.ID
	}
	ra.maillist.SetSelf(ra.selfAddress())
	ra.reloading = true
	ra.maillist.ReplaceEnvelopes(envelopes, keep)
	ra.reloading = false
	ra.refreshMailListTitle()
	ra.refreshHeader()
}

// sameList reports whether two message lists would look the same on screen:
// the same messages in the same order with the same flags. A message's
// content never changes on the server, so nothing else needs comparing.
func sameList(a, b []*mail.Envelope) bool {
	return slices.EqualFunc(a, b, func(x, y *mail.Envelope) bool {
		return x.ID == y.ID && x.ThreadCount == y.ThreadCount && x.HasAttach == y.HasAttach && slices.Equal(x.Flags, y.Flags)
	})
}

// isCurrent reports whether f in acct is still the folder being shown.
func (ra *RootApp) isCurrent(acct *mail.Account, f *mail.Folder) bool {
	return ra.manager.Active() == acct && ra.currentFolder != nil && ra.currentFolder.Name == f.Name
}

func (ra *RootApp) onEnvelopeSelected(env *mail.Envelope) {
	ra.selectedEnv = env
	ra.currentMsg = nil // until the body for env arrives
	ra.conv = nil
	ra.browserPending = nil
	if p := ra.attachPending; p != nil && (env == nil || env.ID != p.env.ID) {
		ra.attachPending = nil // the owner moved on: never open it unasked later
	}
	if env == nil { // empty folder, or a filter that matches nothing
		ra.preview.Clear()
		return
	}
	ra.preview.ShowEnvelope(env)
	markSeen := ra.mailListFocused() && !ra.reloading && env.IsUnread() // read already: no call for every row passed
	if markSeen {
		ra.setRead(env, true) // the new-mail sign goes now, not on the next sync
	}
	ra.bodyLoader(ra.manager.Active(), ra.currentFolder, env, markSeen)
}

// setRead shows env as read or unread in the mail list at once; the server
// is told separately. UI goroutine only.
func (ra *RootApp) setRead(env *mail.Envelope, read bool) {
	flags := slices.DeleteFunc(slices.Clone(env.Flags), func(f string) bool { return f == `\Seen` })
	if read {
		flags = append(flags, `\Seen`)
	}
	env.Flags = flags
	ra.maillist.Redraw()
}

// mailListFocused reports whether the user is working in the mail list. A
// message selected while focus is elsewhere (e.g. the first message of a
// folder that was just opened from the folder pane) was not chosen by the
// user and must not be marked as read.
func (ra *RootApp) mailListFocused() bool {
	return ra.maillist != nil && ra.uiApp.GetFocus() == tview.Primitive(ra.maillist)
}

// conversationRowsOf is conversationRows for loadBody, asked on the UI
// goroutine: it reads the search state, which only that goroutine changes.
func (ra *RootApp) conversationRowsOf(acct *mail.Account, folder *mail.Folder) bool {
	return acct != nil && folder != nil && ra.conversationRows(acct.Name, folder.Name)
}

// loadBody fetches env's body from folder and shows it in the preview. The
// message is marked as read on the server only when markSeen is set. conv
// says the row is a conversation, decided on the UI goroutine, as loadBody
// runs off it.
func (ra *RootApp) loadBody(acct *mail.Account, folder *mail.Folder, env *mail.Envelope, markSeen, conv bool) {
	if acct == nil || folder == nil {
		return
	}
	eng, ok := ra.engines[acct.Name]
	if !ok {
		return
	}
	if th, ok := eng.(msync.Threads); ok && conv && env.ThreadID != "" {
		c := ra.fetchConversation(acct.Name, eng, env)
		ra.uiApp.QueueUpdateDraw(func() { ra.conversationArrived(acct, folder, env, c) })
		if markSeen {
			ra.markThreadRead(th, folder.Name, env)
		}
		return
	}
	msg, err := eng.FetchBody(context.Background(), folder.Name, env.ID)
	if err != nil {
		ra.uiApp.QueueUpdateDraw(func() { ra.bodyFailed(acct, env, err) })
		return
	}
	ra.uiApp.QueueUpdateDraw(func() { ra.bodyArrived(acct, folder, env, msg) })
	if markSeen {
		ra.markSeen(eng, folder.Name, env.ID)
	}
}

// bodyArrived shows a downloaded body, unless the user moved to another
// message, folder or account meanwhile. If they pressed the browser key while
// it was downloading, it goes to the browser now. UI goroutine only.
func (ra *RootApp) bodyArrived(acct *mail.Account, folder *mail.Folder, env *mail.Envelope, msg *mail.Message) {
	if ra.selectedEnv != env || !ra.isCurrent(acct, folder) {
		return
	}
	ra.currentMsg = msg
	if has := len(msg.Files()) > 0; has != env.HasAttach { // the body knows best
		env.HasAttach = has
		ra.maillist.Redraw()
	}
	ra.preview.ShowMessage(msg)
	ra.resumeAttach(env)
	if ra.browserPending == env {
		ra.browserPending = nil
		ra.showInBrowser(msg)
	}
}

// openSelectedInBrowser shows the selected message in the web browser, where
// HTML mail and pictures look as their sender meant. A body that is not here
// yet is fetched first.
func (ra *RootApp) openSelectedInBrowser() {
	env := ra.selectedEnv
	if env == nil {
		return
	}
	if ra.currentMsg != nil {
		ra.showInBrowser(ra.currentMsg)
		return
	}
	ra.browserPending = env
	ra.uiApp.StatusBar.Info("Loading message for the browser…")
	ra.bodyLoader(ra.manager.Active(), ra.currentFolder, env, false)
}

// showInBrowser writes msg as a page and hands it to the browser.
func (ra *RootApp) showInBrowser(msg *mail.Message) {
	path, err := ra.browser.write(msg)
	if err == nil {
		err = ra.openBrowser(path)
	}
	if err != nil {
		ra.uiApp.StatusBar.Error("open in browser: " + err.Error())
		return
	}
	ra.uiApp.StatusBar.Info("Opened in browser")
}

// markSelectedSeen marks the selected message as read. It runs when the mail
// list gains focus, since that message was previewed without being marked.
func (ra *RootApp) markSelectedSeen() {
	env, folder, acct := ra.selectedEnv, ra.currentFolder, ra.manager.Active()
	if env == nil || folder == nil || acct == nil || !env.IsUnread() {
		return
	}
	eng, ok := ra.engines[acct.Name]
	if !ok {
		return
	}
	ra.setRead(env, true)
	if th, ok := eng.(msync.Threads); ok && ra.conversationRows(acct.Name, folder.Name) && env.ThreadID != "" {
		go ra.markThreadRead(th, folder.Name, env)
		return
	}
	go ra.markSeen(eng, folder.Name, env.ID)
}

// markSelectedUnread marks the selected message unread (u on a message
// row): the new-mail sign comes back at once, and goes again if the server
// refuses. It reports false when no message is selected.
func (ra *RootApp) markSelectedUnread() bool {
	env, folder, acct := ra.selectedEnv, ra.currentFolder, ra.manager.Active()
	if env == nil || folder == nil || acct == nil || ra.pane == paneFolders {
		return false
	}
	eng, ok := ra.engines[acct.Name]
	if !ok {
		return false
	}
	if env.IsUnread() {
		ra.uiApp.StatusBar.Info("Already unread")
		return true
	}
	ra.setRead(env, false)
	id, name := env.ID, folder.Name
	go func() {
		err := eng.SetFlags(context.Background(), name, id, []string{`\Seen`}, false)
		if err == nil {
			eng.RefreshBadge(context.Background(), name)
		}
		ra.uiApp.QueueUpdateDraw(func() {
			if err != nil {
				ui.AppLog.Error("[%s] mark unread failed: %v", acct.Name, err)
				ra.uiApp.StatusBar.Error("Failed: " + err.Error())
				ra.setRead(env, true)
				return
			}
			ra.uiApp.StatusBar.Info("Marked unread")
		})
	}()
	return true
}

// markSeen marks a message read on the server, then refreshes the folder's
// counts so the header does not wait for the next poll.
func (ra *RootApp) markSeen(eng msync.Account, folder, id string) {
	if err := eng.SetFlags(context.Background(), folder, id, []string{`\Seen`}, true); err == nil {
		eng.RefreshBadge(context.Background(), folder)
	}
}

func (ra *RootApp) startReply(msg *mail.Message, replyAll bool) {
	acct := ra.manager.Active()
	if acct == nil || len(msg.Envelope.From) == 0 {
		return
	}
	if ra.conv != nil && !ra.conv.downloaded(msg) {
		ra.uiApp.StatusBar.Info(replyRefused) // never quote the placeholder
		return
	}
	ra.launchEditor(buildReplyDraft(acct, msg, replyAll))
}

func (ra *RootApp) launchEditor(d smtpadapter.Draft) {
	ra.runEditor(d, "", "")
}

// runEditor opens $EDITOR on d. With localID the result replaces that local
// draft instead of creating a new one. A server copy the result replaces —
// supersede, or the edited local draft's previous upload — is removed once
// the new version is safely saved locally: it is not a deletion, so it does
// not go to Deleted Items.
func (ra *RootApp) runEditor(d smtpadapter.Draft, localID, supersede string) {
	eml, err := smtpadapter.FormatDraft(d)
	if err != nil {
		ra.uiApp.StatusBar.Error("format draft: " + err.Error())
		return
	}
	tmpFile, err := os.CreateTemp("", "m9s-*.eml")
	if err != nil {
		ra.uiApp.StatusBar.Error("tmp file: " + err.Error())
		return
	}
	tmpName := tmpFile.Name()
	defer os.Remove(tmpName)
	if _, err := tmpFile.Write(eml); err != nil {
		tmpFile.Close()
		ra.uiApp.StatusBar.Error("write draft: " + err.Error())
		return
	}
	tmpFile.Close()

	editor := ra.editorCommand()
	if editor == "" {
		ra.uiApp.StatusBar.Error("no editor — set $EDITOR or ui.editor in config.yaml")
		return
	}

	var (
		parsed     smtpadapter.Draft
		savedID    string
		suspendErr string
		acctName   string
	)
	ra.uiApp.Suspend(func() {
		parts := strings.Fields(editor)
		args := append(parts[1:], tmpName)
		cmd := exec.Command(parts[0], args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run() // exit code no longer determines send vs draft

		data, readErr := os.ReadFile(tmpName)
		if readErr != nil {
			suspendErr = "read draft: " + readErr.Error()
			return
		}
		p, parseErr := smtpadapter.ParseDraft(data)
		if parseErr != nil {
			suspendErr = "parse draft: " + parseErr.Error()
			return
		}
		if strings.TrimSpace(p.Body) == "" {
			return // empty body — discard silently
		}
		parsed = p

		acct := ra.manager.Active()
		if acct == nil {
			suspendErr = "no active account"
			return
		}
		acctName = acct.Name // captured unconditionally so sendDraft uses the right account

		if ra.draftStore != nil {
			if localID != "" {
				prev, updErr := ra.draftStore.Update(acctName, localID, parsed)
				if updErr != nil {
					suspendErr = "draft save failed: " + updErr.Error()
					return
				}
				savedID = localID
				if supersede == "" {
					supersede = prev
				}
				return
			}
			id, saveErr := ra.draftStore.Save(acctName, parsed)
			if saveErr != nil {
				suspendErr = "draft save failed: " + saveErr.Error()
				return
			}
			savedID = id
		}
	})

	if suspendErr != "" {
		ra.uiApp.StatusBar.Error(suspendErr)
		return
	}
	if strings.TrimSpace(parsed.Body) == "" {
		return // empty — nothing to do
	}
	ra.folders.RefreshDrafts()
	if supersede != "" && savedID != "" {
		if eng, ok := ra.engines[acctName]; ok {
			go func() {
				if err := eng.DeleteDraftCopy(context.Background(), supersede); err != nil {
					ra.uiApp.QueueUpdateDraw(func() { // also logs
						ra.uiApp.StatusBar.Error(fmt.Sprintf("draft: the replaced server copy (id %s) was not removed: %v", supersede, err))
					})
				}
			}()
		}
	}
	ra.showSendModal(parsed, savedID, acctName)
}

// editSelected handles e: edit the local draft under the folder cursor, or
// the message open in the server's Drafts folder.
func (ra *RootApp) editSelected() {
	acct := ra.manager.Active()
	if acct == nil || ra.draftStore == nil {
		return
	}
	if ra.uiApp.GetFocus() == tview.Primitive(ra.folders) {
		if dm := ra.folders.SelectedDraft(); dm != nil {
			ra.editLocalDraft(acct, dm.ID)
		}
		return
	}
	if !ra.inDraftsFolder() || ra.currentMsg == nil {
		return
	}
	msg := ra.currentMsg
	envID := msg.Envelope.ID
	dc, named := ra.engines[acct.Name].(msync.DraftCopies)
	if !named {
		ra.editServerDraft(acct, msg, envID)
		return
	}
	// The row is a message; the draft holding it has an id of its own (the
	// one our local drafts remember). Ask which, off the UI goroutine.
	ra.uiApp.StatusBar.Info("Opening draft…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		copyID, err := dc.DraftCopyOf(ctx, envID)
		ra.uiApp.QueueUpdateDraw(func() {
			if err != nil {
				ui.AppLog.Warn("[draft] which draft holds message %s: %v", envID, err)
				ra.uiApp.StatusBar.Error("edit draft: " + err.Error())
				return
			}
			if copyID == "" {
				ra.uiApp.StatusBar.Error("edit draft: no longer a draft in Gmail")
				return
			}
			ra.uiApp.StatusBar.Info("")
			ra.editServerDraft(acct, msg, copyID)
		})
	}()
}

// editServerDraft edits a server draft whose copy id (what SaveDraft
// returned for it) is copyID: the upload of one of our local drafts edits
// that draft, so there are never two of the same; any other is turned into
// a local draft that replaces it.
func (ra *RootApp) editServerDraft(acct *mail.Account, msg *mail.Message, copyID string) {
	if metas, _ := ra.draftStore.List(acct.Name); metas != nil {
		for _, m := range metas {
			if m.SyncedID != "" && m.SyncedID == copyID {
				ra.editLocalDraft(acct, m.ID)
				return
			}
		}
	}
	ra.runEditor(draftFromMessage(msg), "", copyID)
}

func (ra *RootApp) editLocalDraft(acct *mail.Account, id string) {
	d, err := ra.draftStore.Load(acct.Name, id)
	if err != nil {
		ra.uiApp.StatusBar.Error("open draft: " + err.Error())
		return
	}
	ra.runEditor(d, id, "")
}

// inDraftsFolder reports whether the open folder is the server's Drafts folder.
func (ra *RootApp) inDraftsFolder() bool {
	acct := ra.manager.Active()
	if acct == nil || ra.currentFolder == nil {
		return false
	}
	eng, ok := ra.engines[acct.Name]
	return ok && ra.currentFolder.Name == eng.DraftsFolderName()
}

// canEdit reports whether e does something in the focused pane right now.
func (ra *RootApp) canEdit() bool {
	if ra.draftStore == nil {
		return false
	}
	if ra.pane == paneFolders {
		return ra.folders.SelectedDraft() != nil
	}
	return ra.inDraftsFolder()
}

// onDraftHover shows a local draft in the preview when the folder cursor
// moves onto it. The folder's message is no longer what r/R/d act on.
func (ra *RootApp) onDraftHover(dm draft.Meta) {
	ra.hoverSeq++ // a pending folder hover is superseded
	ra.currentMsg = nil
	ra.conv = nil
	ra.selectedEnv = nil
	ra.refreshHints()
	acct := ra.manager.Active()
	if acct == nil || ra.draftStore == nil {
		return
	}
	d, err := ra.draftStore.Load(acct.Name, dm.ID)
	if err != nil {
		// Sent or deleted meanwhile (or removed outside m9s): say so plainly
		// and drop it from the tree instead of showing a file error.
		ui.AppLog.Warn("[draft] %q unreadable: %v", dm.Subject, err)
		ra.preview.SetText(tview.Escape("This draft no longer exists (it was sent or deleted)."))
		ra.folders.RefreshDrafts()
		return
	}
	ra.preview.ShowDraft(d)
}

// keepDraft runs after "Server Draft" or "Local Draft". A Server Draft is
// uploaded now; a Local Draft is marked so it is never uploaded.
func (ra *RootApp) keepDraft(acctName, draftID string, onServer bool) {
	if ra.draftStore != nil && draftID != "" {
		if err := ra.draftStore.SetLocalOnly(acctName, draftID, !onServer); err != nil {
			ra.uiApp.StatusBar.Error("save draft: " + err.Error())
			return
		}
	}
	if ra.folders != nil {
		ra.folders.RefreshDrafts()
	}
	done := "Kept as a local draft (not uploaded)"
	if onServer {
		done = "Draft saved to Server Drafts"
	}
	ra.uiApp.StatusBar.Info(done)
	ra.syncAfterKeep(acctName, onServer, done)
}

// syncAfterKeep does what s does, right after a draft was kept: upload it
// first if it is a Server Draft, then refresh every folder's counts and
// reload the open folder's list.
func (ra *RootApp) syncAfterKeep(acctName string, upload bool, done string) {
	eng, ok := ra.engines[acctName]
	if !ok {
		return
	}
	if st, known := ra.status[acctName]; known && st.State == msync.StateFailed {
		ra.uiApp.StatusBar.Info(done + " · offline, syncs on reconnect")
		return
	}
	acct, folder := ra.manager.Active(), ra.currentFolder
	go func() {
		ctx := context.Background()
		var uploadErr error
		if upload {
			uploadErr = eng.SyncDrafts(ctx)
		}
		eng.SyncAll(ctx)
		if acct != nil && folder != nil && acct.Name == acctName {
			ra.loadEnvelopes(acct, folder) // dropped if the user moved on meanwhile
		}
		if uploadErr != nil {
			return // DraftSyncFailed already put the reason on the bottom row
		}
		ra.uiApp.QueueUpdateDraw(func() {
			ra.uiApp.StatusBar.Info(done + " · synced")
		})
	}()
}

// showSendModal displays a tview Modal asking the user whether to send the
// just-saved draft or keep it locally. acctName is used for draft deletion.
func (ra *RootApp) showSendModal(d smtpadapter.Draft, draftID, acctName string) {
	buttons := []string{"Send", "Server Draft", "Local Draft"}
	modalText := "Send now?\n\nOr keep it as a draft: on the server, or only on this machine."
	if ra.draftStore == nil {
		buttons = []string{"Send", "Discard"}
		modalText = "Send now?"
	}
	doneFn := func(_ int, label string) {
		ra.uiApp.Pages.RemovePage("send-modal")
		switch label {
		case "Send":
			ra.sendFn(d, draftID, acctName)
		case "Server Draft":
			ra.keepDraft(acctName, draftID, true)
		case "Local Draft":
			ra.keepDraft(acctName, draftID, false)
			// "Discard" — do nothing, no flash
		}
	}
	ra.testModalDone = doneFn
	ra.testModalButtons = buttons
	modal := tview.NewModal().
		SetText(modalText).
		AddButtons(buttons).
		SetDoneFunc(doneFn)
	ra.uiApp.Pages.AddPage("send-modal", modal, true, true)
	ra.uiApp.SetFocus(modal)
}

// showDeleteModal asks to confirm a deletion; onConfirm runs only when the
// first button is chosen.
func (ra *RootApp) showDeleteModal(modalText string, buttons []string, onConfirm func()) {
	dbs := make([]ui.DialogButton, len(buttons))
	for i, l := range buttons {
		dbs[i] = ui.DialogButton{Label: l}
	}
	ra.showDeleteDialog(modalText, dbs, 0, func(label string) {
		if label == buttons[0] {
			onConfirm()
		}
	})
}

// showDeleteDialog shows a confirmation dialog whose buttons may be marked
// Danger (drawn in the error colour). onChoice receives the chosen label.
func (ra *RootApp) showDeleteDialog(text string, buttons []ui.DialogButton, focus int, onChoice func(label string)) {
	previous := ra.uiApp.GetFocus()
	doneFn := func(_ int, label string) {
		ra.uiApp.Pages.RemovePage("delete-modal")
		if previous != nil {
			ra.uiApp.SetFocus(previous)
		}
		onChoice(label)
	}
	ra.testDeleteModalDone = doneFn
	ra.testDeleteModalText = text
	ra.testDeleteButtons = buttons
	ra.testDeleteFocus = focus
	dialog := ui.NewDialog(ra.uiApp.Skin, text, buttons, focus, doneFn)
	ra.uiApp.Pages.AddPage("delete-modal", dialog, true, true)
	ra.uiApp.SetFocus(dialog)
}

// confirmEmptyTrash asks a second time — starting on Cancel — before
// permanently deleting every message in the trash folder.
func (ra *RootApp) confirmEmptyTrash(acct *mail.Account, eng msync.Account, folder *mail.Folder) {
	n := ra.maillist.VisibleCount()
	if _, total, ok := ra.folders.Counts(acct.Name, folder.Name); ok {
		n = int(total)
	}
	name := ra.folders.DisplayName(acct.Name, folder)
	ra.showDeleteDialog(
		fmt.Sprintf("Permanently delete ALL %d messages in %q?\nThis cannot be undone.", n, name),
		[]ui.DialogButton{{Label: "Delete All Forever", Danger: true}, {Label: "Cancel"}},
		1, // Cancel: a stray Enter must not empty the folder
		func(label string) {
			if label != "Delete All Forever" {
				return
			}
			go func() {
				removed, err := eng.EmptyTrash(context.Background())
				ra.uiApp.QueueUpdateDraw(func() {
					if err != nil {
						ui.AppLog.Error(`[delete] error emptying %q: %v`, name, err)
						ra.uiApp.StatusBar.Error("Empty " + name + " failed: " + err.Error())
						return
					}
					ui.AppLog.Info(`[delete] %q emptied: %d messages permanently deleted`, name, removed)
					ra.uiApp.StatusBar.Info(fmt.Sprintf("%s emptied: %d messages permanently deleted", name, removed))
				})
				if err == nil {
					eng.RefreshBadge(context.Background(), folder.Name)
					ra.loadEnvelopes(acct, folder)
				}
			}()
		})
}

// deleteMessage handles the 'd' keybinding in the mail list pane.
// When in the trash folder it shows a permanent-delete modal; otherwise
// it shows a move-to-trash modal.
func (ra *RootApp) deleteMessage() {
	acct := ra.manager.Active()
	if acct == nil {
		return
	}
	// In the folder pane d acts on what the cursor is on: a local draft is
	// deleted; on a folder there is nothing to delete (and the previewed
	// message is not what the user is pointing at).
	if ra.uiApp.GetFocus() == tview.Primitive(ra.folders) {
		if dm := ra.folders.SelectedDraft(); dm != nil {
			ra.deleteDraft(acct, *dm)
		}
		return
	}
	eng, ok := ra.engines[acct.Name]
	if !ok {
		return
	}
	if ra.currentFolder == nil {
		return
	}
	if marked := ra.markedMessages(); len(marked) > 0 {
		ra.deleteMarked(acct, eng, ra.currentFolder, marked)
		return
	}
	if env := ra.selectedEnv; env != nil && env.ThreadID != "" && ra.conversationRows(acct.Name, ra.currentFolder.Name) {
		ra.trashConversation(acct, eng, ra.currentFolder, env)
		return
	}
	if ra.currentMsg == nil {
		return
	}

	subject := ra.currentMsg.Envelope.Subject
	id := ra.currentMsg.Envelope.ID
	folder := ra.currentFolder

	if eng.TrashFolderName() == folder.Name {
		// Already in trash — offer permanent deletion, of this one or of all.
		ra.showDeleteDialog(
			fmt.Sprintf("Permanently delete %q?\nThis cannot be undone.", subject),
			[]ui.DialogButton{{Label: "Delete Forever"}, {Label: "Delete All Forever", Danger: true}, {Label: "Cancel"}},
			0,
			func(label string) {
				switch label {
				case "Delete All Forever":
					ra.confirmEmptyTrash(acct, eng, folder)
					return
				case "Delete Forever":
				default:
					return
				}
				go func() {
					err := eng.PermanentDelete(context.Background(), folder.Name, id)
					ra.uiApp.QueueUpdateDraw(func() {
						if err != nil {
							ui.AppLog.Error(`[delete] error permanently deleting %q: %v`, subject, err)
							ra.uiApp.StatusBar.Error(permanentDeleteFailure(err))
						} else {
							ui.AppLog.Info(`[delete] %q permanently deleted`, subject)
							ra.uiApp.StatusBar.Info("Permanently deleted")
						}
					})
					// Not in Trash: the list shows where the message is now.
					if err == nil || errors.Is(err, msync.ErrNotInTrash) {
						go eng.RefreshBadge(context.Background(), folder.Name)
						go ra.loadEnvelopes(acct, folder)
					}
				}()
			},
		)
	} else {
		ra.showDeleteModal(
			fmt.Sprintf("Move %q to Deleted Items?", subject),
			[]string{"Delete", "Cancel"},
			func() {
				go func() {
					err := eng.MoveToTrash(context.Background(), folder.Name, id)
					ra.uiApp.QueueUpdateDraw(func() {
						if err != nil {
							ui.AppLog.Error(`[delete] error moving %q: %v`, subject, err)
							ra.uiApp.StatusBar.Error("Move failed: " + err.Error())
						} else {
							ui.AppLog.Info(`[delete] %q moved to Deleted Items`, subject)
							ra.uiApp.StatusBar.Info("Moved to Deleted Items")
						}
					})
					if err == nil {
						go eng.RefreshBadge(context.Background(), folder.Name)
						go ra.loadEnvelopes(acct, folder)
					}
				}()
			},
		)
	}
}

// deleteDraft asks, then removes a local draft and, if it was synced, its
// copy in the server's Drafts folder.
func (ra *RootApp) deleteDraft(acct *mail.Account, dm draft.Meta) {
	if ra.draftStore == nil {
		return
	}
	// The tree's copy of the meta may predate the upload; read it fresh.
	if metas, _ := ra.draftStore.List(acct.Name); metas != nil {
		for _, m := range metas {
			if m.ID == dm.ID {
				dm = m
			}
		}
	}
	subject := dm.Subject
	if subject == "" {
		subject = "(no subject)"
	}
	text := fmt.Sprintf("Delete local draft %q?", subject)
	eng, hasEngine := ra.engines[acct.Name]
	copyConfirm, copyDone := draftCopyWords(acct)
	if dm.SyncedID != "" && hasEngine {
		text += "\n" + copyConfirm
	}
	ra.showDeleteModal(text, []string{"Delete", "Cancel"}, func() {
		if err := ra.draftStore.Delete(acct.Name, dm.ID); err != nil {
			ui.AppLog.Error(`[delete] error deleting local draft %q: %v`, subject, err)
			ra.uiApp.StatusBar.Error("Delete draft failed: " + err.Error())
			return
		}
		ui.AppLog.Info(`[delete] local draft %q deleted`, subject)
		ra.folders.RefreshDrafts()
		ra.uiApp.SetFocus(ra.folders)
		ra.uiApp.StatusBar.Info("Draft deleted")
		if copyID := dm.SyncedID; copyID != "" && hasEngine {
			go func() {
				err := eng.TrashDraftCopy(context.Background(), copyID)
				ra.uiApp.QueueUpdateDraw(func() {
					if err != nil {
						ui.AppLog.Error(`[delete] error removing server copy of draft %q: %v`, subject, err)
						ra.uiApp.StatusBar.Error("Draft deleted locally; server copy: " + err.Error())
						return
					}
					ui.AppLog.Info(`[delete] draft %q: %s`, subject, strings.ToLower(copyDone))
					ra.uiApp.StatusBar.Info(copyDone)
					if ra.inDraftsFolder() {
						go ra.loadEnvelopes(ra.manager.Active(), ra.currentFolder)
					}
				})
			}()
		}
	})
}

// draftCopyWords says what deleting a draft's server copy does on acct's
// provider: Gmail and Microsoft delete the draft for good, IMAP moves it to
// the trash.
func draftCopyWords(acct *mail.Account) (confirm, done string) {
	if acct != nil && acct.Config != nil && acct.Config.IsGmail() {
		return "Its copy in Gmail's Drafts is deleted for good.", "Server draft deleted"
	}
	if acct != nil && acct.Config != nil && acct.Config.IsMicrosoft() {
		return "Its copy in Outlook's Drafts is deleted for good.", "Server draft deleted"
	}
	return "Its copy in the server's Drafts folder is moved to Deleted Items.", "Draft moved to Deleted Items"
}

// permanentDeleteFailure is the status line of a failed permanent delete.
func permanentDeleteFailure(err error) string {
	if errors.Is(err, msync.ErrNotInTrash) {
		return "Not deleted: the message is no longer in Trash"
	}
	return "Delete failed: " + err.Error()
}

func (ra *RootApp) sendDraft(draft smtpadapter.Draft, draftID, acctName string) {
	// Resolve account name: use the passed name when set, fall back to active.
	resolvedAcctName := acctName
	if resolvedAcctName == "" {
		active := ra.manager.Active()
		if active == nil {
			ra.uiApp.QueueUpdateDraw(func() { ra.uiApp.StatusBar.Error("no active account") })
			return
		}
		resolvedAcctName = active.Name
	}

	// Find account config by name (not Active() — user may have switched accounts).
	var acct *mail.Account
	for _, a := range ra.manager.All() {
		if a.Name == resolvedAcctName {
			acct = a
			break
		}
	}
	if acct == nil {
		ra.uiApp.QueueUpdateDraw(func() {
			ra.uiApp.StatusBar.Error("account not found: " + resolvedAcctName)
		})
		return
	}

	eng, ok := ra.engines[resolvedAcctName]
	if !ok {
		ra.uiApp.QueueUpdateDraw(func() {
			ra.uiApp.StatusBar.Error("no sender for account " + resolvedAcctName)
		})
		return
	}
	go func() {
		msg, err := eng.Send(context.Background(), draft)
		if err != nil {
			ra.uiApp.QueueUpdateDraw(func() { ra.uiApp.StatusBar.Error(sendFailureText(err)) })
			return
		}
		if ra.draftStore != nil && draftID != "" {
			copyID := ra.syncedCopy(resolvedAcctName, draftID)
			_ = ra.draftStore.Delete(resolvedAcctName, draftID)
			// Gmail and Graph send the message, not the draft, and keep the
			// draft: delete it. (IMAP drafts are left as they always were.)
			if copyID != "" && acct.Config != nil && (acct.Config.IsGmail() || acct.Config.IsMicrosoft()) {
				if err := eng.DeleteDraftCopy(context.Background(), copyID); err != nil {
					ui.AppLog.Warn("[send] %s: sent, but its server draft (id %s) was not deleted: %v", resolvedAcctName, copyID, err)
				}
			}
		}
		sentFolder, saveErr := ra.saveSentCopy(resolvedAcctName, smtpadapter.SentCopy(msg, draft))
		ra.uiApp.QueueUpdateDraw(func() {
			if ra.folders != nil {
				ra.folders.RefreshDrafts() // the sent draft must leave Local Drafts on screen too
			}
			if saveErr != nil {
				ui.AppLog.Warn(`[send] %s: sent, but saving a copy to Sent Items failed: %v`, resolvedAcctName, saveErr)
				ra.uiApp.StatusBar.Error("Sent, but couldn't save copy to Sent Items: " + saveErr.Error())
				return
			}
			if sentFolder != "" {
				ui.AppLog.Info(`[send] %s: copy saved to %s`, resolvedAcctName, sentFolder)
				if a := ra.manager.Active(); a != nil && a.Name == resolvedAcctName &&
					ra.currentFolder != nil && ra.currentFolder.Name == sentFolder {
					go ra.loadEnvelopes(a, ra.currentFolder)
				}
			}
			ra.uiApp.StatusBar.Info("Message sent.")
		})
	}()
}

// syncedCopy is the id of the server copy of local draft id, "" if none.
func (ra *RootApp) syncedCopy(account, id string) string {
	metas, _ := ra.draftStore.List(account)
	for _, m := range metas {
		if m.ID == id {
			return m.SyncedID
		}
	}
	return ""
}

// saveSentCopy keeps msg in the account's Sent folder and returns that
// folder's name ("" when the server keeps its own copy). Call it off the
// UI goroutine: it talks to the server.
func (ra *RootApp) saveSentCopy(account string, msg []byte) (string, error) {
	eng, ok := ra.engines[account]
	if !ok {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	folder, err := eng.SaveSentCopy(ctx, msg)
	if err == nil && folder != "" {
		eng.RefreshBadge(ctx, folder)
	}
	return folder, err
}

// sendFailureText words a failed send for the status row. A refused
// password cannot be fixed from inside m9s: it was looked up at startup.
func sendFailureText(err error) string {
	if errors.Is(err, msync.ErrNoSMTPPassword) {
		return err.Error() // nothing was tried: not a failure of the send itself
	}
	if errors.Is(err, smtpadapter.ErrAuth) {
		return "send failed: password rejected — fix it and restart m9s (" + err.Error() + ")"
	}
	return "send failed: " + err.Error()
}

func (ra *RootApp) dispatchEvents(events <-chan msync.Event) {
	for ev := range events {
		ra.uiApp.QueueUpdateDraw(func() { ra.handleEvent(ev) })
	}
}

// handleEvent applies one sync-engine event to the UI. UI goroutine only.
func (ra *RootApp) handleEvent(ev msync.Event) {
	switch e := ev.(type) {
	case msync.AccountStatus:
		ra.handleStatus(e)
	case msync.ServerInfo:
		if ra.server == nil {
			ra.server = make(map[string]string)
		}
		ra.server[e.AccountName] = e.Text
		if ra.isActiveAccount(e.AccountName) {
			ra.refreshHeader()
		}
	case msync.FoldersUpdated:
		ra.handleFolders(e)
	case msync.BadgeUpdated:
		ra.folders.UpdateBadge(e.AccountName, e.FolderName, e.Unseen, e.Total)
		if ra.isActiveAccount(e.AccountName) && ra.currentFolder != nil && ra.currentFolder.Name == e.FolderName {
			ra.refreshHeader()
		}
	case msync.NewMail:
		if ra.isActiveAccount(e.AccountName) && ra.currentFolder != nil && ra.currentFolder.Name == e.FolderName {
			go ra.loadEnvelopes(ra.manager.Active(), ra.currentFolder)
		}
	case msync.MirrorProgress:
		ra.handleMirrorProgress(e)
	case msync.ListingProgress:
		ra.handleListingProgress(e)
	case msync.MirrorFailed:
		// The account still works; only the background copy is paused.
		ui.AppLog.Warn("[%s] saving mail locally paused, resumed shortly: %v", e.AccountName, e.Err)
	case msync.DraftSyncFailed:
		subject := e.Subject
		if subject == "" {
			subject = "(no subject)"
		}
		ra.uiApp.StatusBar.Error(fmt.Sprintf("%s: draft %q not uploaded to Server Drafts: %v (kept locally, retried on next sync)",
			e.AccountName, subject, e.Err)) // also logs
	case msync.DraftSynced:
		if e.AccountName == ra.folders.ActiveAccountName() {
			ra.folders.RefreshDrafts() // the entries carry the new server id
			if ra.inDraftsFolder() {
				go ra.loadEnvelopes(ra.manager.Active(), ra.currentFolder)
			}
		}
	}
}

// handleMirrorProgress records how far an account's local copy is. The
// header shows it while it runs; the log gets one line when a round found
// and saved something.
func (ra *RootApp) handleMirrorProgress(p msync.MirrorProgress) {
	if ra.mirror == nil {
		ra.mirror = make(map[string]msync.MirrorProgress)
	}
	prev, seen := ra.mirror[p.AccountName]
	ra.mirror[p.AccountName] = p
	ra.trackStart(mirrorKey(p.AccountName), p.Downloaded, p.Done)
	if p.Done && (!seen || prev.Downloaded != p.Downloaded || prev.Messages != p.Messages) {
		ui.AppLog.Info("[%s] local copy up to date: %d messages", p.AccountName, p.Messages)
	}
	if ra.isActiveAccount(p.AccountName) {
		ra.refreshHeader()
	}
}

// listReloadEvery is how often at most the open list is reloaded while its
// listing is stored; the end of the listing always reloads it.
const listReloadEvery = time.Second

// handleListingProgress records how far a folder's listing is stored. The
// header shows it while it runs, the log gets its start and end, and the
// open folder reloads so its mail appears while the first sync runs.
func (ra *RootApp) handleListingProgress(p msync.ListingProgress) {
	if ra.listing == nil {
		ra.listing = make(map[string]map[string]msync.ListingProgress)
	}
	folders := ra.listing[p.AccountName]
	if folders == nil {
		folders = make(map[string]msync.ListingProgress)
		ra.listing[p.AccountName] = folders
	}
	prev, seen := folders[p.FolderName]
	folders[p.FolderName] = p
	ra.trackStart(listingKey(p), p.Stored, p.Done)
	running := seen && !prev.Done
	switch {
	case !p.Done && !running:
		ui.AppLog.Info("[%s] syncing %s: %d messages, %d already stored", p.AccountName, p.FolderName, p.Total, p.Stored)
	case p.Done && (running || !seen):
		ui.AppLog.Info("[%s] %s synced: %d messages", p.AccountName, p.FolderName, p.Total)
	}
	if !ra.isActiveAccount(p.AccountName) {
		return
	}
	ra.refreshHeader()
	if ra.currentFolder == nil || ra.currentFolder.Name != p.FolderName {
		return
	}
	if now := ra.now(); p.Done || now.Sub(ra.listReload) >= listReloadEvery {
		ra.listReload = now
		go ra.loadEnvelopes(ra.manager.Active(), ra.currentFolder)
	}
}

// listingShown is the listing the header shows for account: the open
// folder's while it runs, else any running one (by name, so it is stable).
func (ra *RootApp) listingShown(account string) (msync.ListingProgress, bool) {
	folders := ra.listing[account]
	if ra.currentFolder != nil {
		if p, ok := folders[ra.currentFolder.Name]; ok && !p.Done {
			return p, true
		}
	}
	var out msync.ListingProgress
	found := false
	for name, p := range folders {
		if !p.Done && (!found || name < out.FolderName) {
			out, found = p, true
		}
	}
	return out, found
}

// percent is part of total in whole percent (0 for an empty total).
func percent(part, total int) int {
	if total <= 0 {
		return 0
	}
	return part * 100 / total
}

func (ra *RootApp) isActiveAccount(name string) bool {
	acct := ra.manager.Active()
	return acct != nil && acct.Name == name
}

// handleStatus records an account's connection state. Every event is logged;
// the bottom row only reports the first failure of a streak and the recovery,
// so a server that stays down does not spam it.
func (ra *RootApp) handleStatus(st msync.AccountStatus) {
	if ra.status == nil {
		ra.status = make(map[string]msync.AccountStatus)
	}
	ra.status[st.AccountName] = st

	switch st.State {
	case msync.StateConnecting:
		ui.AppLog.Info("[%s] connecting", st.AccountName)
	case msync.StateConnected:
		if st.Recovered {
			ra.uiApp.StatusBar.Info(st.AccountName + ": reconnected") // also logs
		} else {
			ui.AppLog.Info("[%s] connected", st.AccountName)
		}
	case msync.StateFailed:
		if st.Attempt <= 1 {
			ra.uiApp.StatusBar.Error(fmt.Sprintf("%s: %v", st.AccountName, st.Err)) // also logs
		}
		ui.AppLog.Error("[%s] connection failed (attempt %d, %s): %v",
			st.AccountName, st.Attempt, retryInText(st.RetryIn), st.Err)
	}
	if ra.isActiveAccount(st.AccountName) {
		ra.refreshHeader()
	}
}

// handleFolders takes a (re)discovered folder list. For the active account it
// opens INBOX when nothing is open, falls back to INBOX when the open folder
// no longer exists, and otherwise reloads the open folder in place — which is
// what restores the screen after a reconnect.
func (ra *RootApp) handleFolders(e msync.FoldersUpdated) {
	if eng, ok := ra.engines[e.AccountName]; ok {
		ra.folders.SetServerDraftsFolder(e.AccountName, eng.DraftsFolderName())
	}
	ra.folders.SetFolders(e.AccountName, e.Folders, ra.draftStore)
	if err := ra.store.PutFolders(e.AccountName, e.Folders); err != nil {
		ui.AppLog.Warn("[%s] folder list not remembered for next time: %v", e.AccountName, err)
	}
	if !ra.isActiveAccount(e.AccountName) {
		return
	}
	if ra.currentFolder != nil {
		for _, f := range e.Folders {
			if f.Name == ra.currentFolder.Name {
				ra.currentFolder = f
				ra.refreshHeader()
				go ra.loadEnvelopes(ra.manager.Active(), f)
				return
			}
		}
		ra.clearOpenFolder()
	}
	ra.openFolderByName("INBOX")
	ra.refreshHeader()
}

// SetStore makes st the local mail copy the UI shows, and starts from what
// it holds: every account whose server has not answered yet gets the folders
// of the last run, and the active one its INBOX. Call before the UI runs.
func (ra *RootApp) SetStore(st *store.Store) {
	ra.store = st
	for _, acct := range ra.manager.All() {
		if ra.folders.Folders(acct.Name) != nil {
			continue // the server's own list is already here
		}
		folders := st.Folders(acct.Name)
		if len(folders) == 0 {
			continue
		}
		ra.folders.SetFolders(acct.Name, folders, ra.draftStore)
		if ra.isActiveAccount(acct.Name) && ra.currentFolder == nil {
			ra.openFolderByName("INBOX")
			ra.refreshHeader()
		}
	}
}

// SetLayoutStatePath restores the layout left last time from path and
// remembers future changes there.
func (ra *RootApp) SetLayoutStatePath(path string) {
	ra.layoutPath = path
	if st := config.LoadLayoutState(path); st.MailListRows > 0 {
		ra.split.SetTopRows(st.MailListRows)
	}
	ra.split.SetOnResize(func(rows int) {
		if err := config.SaveLayoutState(ra.layoutPath, config.LayoutState{MailListRows: rows}); err != nil {
			ui.AppLog.Warn("could not remember the layout: %v", err)
		}
	})
}

// Stop stops the application and removes the pages written for the browser.
func (ra *RootApp) Stop() {
	ra.browser.cleanup()
	ra.uiApp.Stop()
}
