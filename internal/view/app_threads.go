package view

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/zhecho/m9s/internal/mail"
	msync "github.com/zhecho/m9s/internal/sync"
	"github.com/zhecho/m9s/internal/ui"
)

// offlineConversation is said when a conversation is shown from the store
// because Gmail could not be asked.
const offlineConversation = "offline — conversation may be incomplete"

// selfAddress is the active account's own address: "me" in conversations,
// the From of what it writes.
func (ra *RootApp) selfAddress() string { return selfAddressOf(ra.manager.Active()) }

// selfAddressOf is acct's own address: gmail.user on Gmail and
// microsoft.user on Microsoft (neither has an smtp block), else smtp.user.
func selfAddressOf(acct *mail.Account) string {
	if acct == nil || acct.Config == nil {
		return ""
	}
	if acct.Config.IsGmail() {
		return acct.Config.Gmail.User
	}
	if acct.Config.IsMicrosoft() {
		return acct.Config.Microsoft.User
	}
	return acct.Config.SMTP.User
}

// defaultConvRest is how long the cursor rests on a conversation before
// Gmail is asked for it: j and k through the list cost no quota.
const defaultConvRest = 400 * time.Millisecond

// loadConversation shows env's conversation. When the local copy holds
// every body it is shown at once; Gmail is asked only once the cursor has
// rested on the row (rested), for what the copy lacks — a body not
// downloaded yet, or a message under a label m9s does not sync — and the
// preview changes only if Gmail's conversation differs. Off the UI
// goroutine.
func (ra *RootApp) loadConversation(acct *mail.Account, folder *mail.Folder, eng msync.Account, th msync.Threads,
	env *mail.Envelope, markSeen bool, gen int64) {
	local := ra.storedConversation(acct.Name, env)
	complete := len(local.missing) == 0
	if complete {
		local.offline = false
		ra.uiApp.QueueUpdateDraw(func() { ra.conversationArrived(acct, folder, env, local) })
	}
	if markSeen { // the row shows read already, rested or not
		go ra.markThreadRead(th, folder.Name, env)
	}
	if !ra.rested(gen) {
		return
	}
	c := ra.fetchConversation(acct.Name, eng, env)
	if complete && (c.offline || sameMessages(local, c)) {
		return
	}
	ra.uiApp.QueueUpdateDraw(func() { ra.conversationArrived(acct, folder, env, c) })
}

// rested waits convRest and reports whether the selection is still gen.
func (ra *RootApp) rested(gen int64) bool {
	d := time.Duration(ra.convRest.Load())
	if d <= 0 {
		d = defaultConvRest
	}
	time.Sleep(d)
	return ra.selGen.Load() == gen
}

// sameMessages reports whether a and b hold the same messages, in order.
func sameMessages(a, b *conversation) bool {
	return slices.EqualFunc(a.msgs, b.msgs, func(x, y *mail.Message) bool { return x.Envelope.ID == y.Envelope.ID })
}

// fetchConversation reads the whole conversation of a row from the server;
// when that fails it falls back to the messages and bodies in the store.
// Safe off the UI goroutine.
func (ra *RootApp) fetchConversation(account string, eng msync.Account, env *mail.Envelope) *conversation {
	if th, ok := eng.(msync.Threads); ok {
		msgs, err := th.Thread(context.Background(), env.ThreadID)
		if err == nil && len(msgs) > 0 {
			return newConversation(msgs, false)
		}
		if err != nil {
			ui.AppLog.Warn("[%s] conversation %s: %v", account, env.ThreadID, err)
		}
	}
	return ra.storedConversation(account, env)
}

// storedConversation is env's conversation as the store holds it: a
// placeholder (in missing) for a body never downloaded. It is marked
// offline: Gmail may hold more.
func (ra *RootApp) storedConversation(account string, env *mail.Envelope) *conversation {
	var msgs []*mail.Message
	missing := map[*mail.Message]bool{}
	placeholder := func(e *mail.Envelope) *mail.Message {
		m := &mail.Message{Envelope: e, TextBody: notDownloaded}
		missing[m] = true
		return m
	}
	for _, e := range ra.store.ThreadEnvelopes(account, env.ThreadID) {
		if raw, ok := ra.store.Body(account, "", e.ID); ok {
			msgs = append(msgs, mail.ParseMessage(e, raw))
		} else {
			msgs = append(msgs, placeholder(e))
		}
	}
	if len(msgs) == 0 {
		msgs = []*mail.Message{placeholder(env)}
	}
	c := newConversation(msgs, true)
	c.missing = missing
	return c
}

// notDownloaded stands in, offline, for a body that was never downloaded.
const notDownloaded = "(not downloaded yet)"

// replyRefused is said when r or R is pressed on such a message.
const replyRefused = "not downloaded yet — reply once m9s is back online"

// conversationArrived shows a fetched conversation, unless the user moved
// on meanwhile. UI goroutine only.
func (ra *RootApp) conversationArrived(acct *mail.Account, folder *mail.Folder, env *mail.Envelope, c *conversation) {
	if ra.selectedEnv != env || !ra.isCurrent(acct, folder) {
		return
	}
	ra.conv = c
	ra.currentMsg = c.target()
	if has, known := c.hasFiles(); known && has != env.HasAttach {
		env.HasAttach = has
		ra.maillist.Redraw()
	}
	ra.preview.ShowConversation(c)
	if page, _ := ra.uiApp.Pages.GetFrontPage(); page == pageReader {
		ra.reader.ShowConversation(c, false)
	}
	if c.offline {
		ra.uiApp.StatusBar.Info(offlineConversation)
	}
	ra.resumeAttach(env)
	if ra.browserPending == env {
		ra.browserPending = nil
		ra.showInBrowser(ra.currentMsg)
	}
}

// markThreadRead marks every message of a conversation read, then refreshes
// the folder's counts. Off the UI goroutine.
func (ra *RootApp) markThreadRead(th msync.Threads, folder string, env *mail.Envelope) {
	if err := th.ModifyThread(context.Background(), env.ThreadID, []string{`\Seen`}, nil); err != nil {
		ui.AppLog.Warn("mark conversation read: %v", err)
		return
	}
	if eng, ok := th.(msync.Account); ok {
		eng.RefreshBadge(context.Background(), folder)
	}
}

// openReader shows the selected conversation full screen. Only a
// conversation row opens it: on a message row Enter does nothing, as before.
func (ra *RootApp) openReader() bool {
	if _, _, _, _, ok := ra.selectedConversation(); !ok || ra.conv == nil {
		return false
	}
	ra.reader.ShowConversation(ra.conv, false)
	ra.uiApp.Pages.SwitchToPage(pageReader)
	ra.uiApp.SetFocus(ra.reader)
	return true
}

// selectedConversation is the conversation row actions apply to, if the
// selected row is one.
func (ra *RootApp) selectedConversation() (*mail.Account, msync.Account, msync.Threads, *mail.Envelope, bool) {
	acct, env, folder := ra.manager.Active(), ra.selectedEnv, ra.currentFolder
	if acct == nil || env == nil || folder == nil || env.ThreadID == "" || ra.pane == paneFolders {
		return nil, nil, nil, nil, false
	}
	eng, ok := ra.engines[acct.Name]
	if !ok || !ra.conversationRows(acct.Name, folder.Name) {
		return nil, nil, nil, nil, false
	}
	th, ok := eng.(msync.Threads)
	return acct, eng, th, env, ok
}

// conversationAction handles u, * and a on a conversation row: they apply
// to every message of it, as on Gmail web. It reports false when the
// selected row is not a conversation (those keys do nothing there yet).
func (ra *RootApp) conversationAction(action ui.Action) bool {
	acct, eng, th, env, ok := ra.selectedConversation()
	if !ok {
		return false
	}
	var add, remove []string
	var done string
	leaves := false // the conversation leaves the open folder
	switch action {
	case ui.ActionMarkUnread:
		remove, done = []string{`\Seen`}, "Marked unread"
	case ui.ActionStar:
		if env.IsFlagged() {
			remove, done = []string{`\Flagged`}, "Star removed"
		} else {
			add, done = []string{`\Flagged`}, "Starred"
		}
	case ui.ActionArchive:
		remove, done, leaves = []string{"INBOX"}, "Archived", true
	default:
		return false
	}
	ra.applyToConversation(acct, eng, ra.currentFolder, done, leaves, func(ctx context.Context) error {
		return th.ModifyThread(ctx, env.ThreadID, add, remove)
	})
	return true
}

// trashConversation asks, then moves every message of a conversation to
// the Trash.
func (ra *RootApp) trashConversation(acct *mail.Account, eng msync.Account, folder *mail.Folder, env *mail.Envelope) {
	th, ok := eng.(msync.Threads)
	if !ok {
		return
	}
	what := fmt.Sprintf("conversation %q", env.Subject)
	if env.ThreadCount > 1 {
		what = fmt.Sprintf("conversation %q (%d messages)", env.Subject, env.ThreadCount)
	}
	ra.showDeleteModal(fmt.Sprintf("Move %s to %s?", what, eng.TrashFolderName()), []string{"Delete", "Cancel"}, func() {
		ra.applyToConversation(acct, eng, folder, "Moved to "+eng.TrashFolderName(), true, func(ctx context.Context) error {
			return th.TrashThread(ctx, env.ThreadID)
		})
	})
}

// applyToConversation runs a change to a conversation in the background,
// says how it went and reloads the list. When the conversation leaves the
// folder an open reader closes, as Gmail web goes back to the list.
func (ra *RootApp) applyToConversation(acct *mail.Account, eng msync.Account, folder *mail.Folder, done string, leaves bool, change func(context.Context) error) {
	ra.applyChange("conversation", acct, eng, folder, done, leaves, change)
}

// applyChange runs change off the UI goroutine, says how it went, and
// reloads folder's list and counts; leaves says the mail leaves folder, so
// an open reader closes. tag names the action in the log.
func (ra *RootApp) applyChange(tag string, acct *mail.Account, eng msync.Account, folder *mail.Folder, done string, leaves bool, change func(context.Context) error) {
	go func() {
		err := change(context.Background())
		ra.uiApp.QueueUpdateDraw(func() {
			if err != nil {
				ui.AppLog.Error("[%s] %s failed: %v", tag, done, err)
				ra.uiApp.StatusBar.Error("Failed: " + err.Error())
				return
			}
			ra.uiApp.StatusBar.Info(done)
			if page, _ := ra.uiApp.Pages.GetFrontPage(); leaves && page == pageReader {
				ra.closeReader()
			}
		})
		if err == nil {
			go eng.RefreshBadge(context.Background(), folder.Name)
			go ra.loadEnvelopes(acct, folder)
		}
	}()
}

// labelsHint is said when the account has no labels to pick from.
const labelsHint = "labels: not available for this account"

// openLabelPicker handles L: a checklist of the account's user labels with
// the ones the selected message (or every message of the selected
// conversation) carries ticked. Enter applies the difference; Esc gives the
// keyboard back to where it was.
func (ra *RootApp) openLabelPicker() {
	acct := ra.manager.Active()
	if acct == nil {
		return
	}
	eng, ok := ra.engines[acct.Name]
	var lab msync.Labels
	if ok {
		lab, ok = eng.(msync.Labels)
	}
	if !ok {
		ra.uiApp.StatusBar.Info(labelsHint)
		return
	}
	env, folder := ra.selectedEnv, ra.currentFolder
	if env == nil || folder == nil || ra.pane == paneFolders {
		return
	}
	var names []string
	for _, f := range lab.AccountLabels() {
		// System labels (special-use folders, INBOX) are not for picking.
		if len(f.Attributes) == 0 && f.Name != "INBOX" && f.Name != "STARRED" && f.Name != "IMPORTANT" {
			names = append(names, f.Name)
		}
	}
	if len(names) == 0 {
		ra.uiApp.StatusBar.Info("no labels to pick from")
		return
	}
	_, _, th, _, isConv := ra.selectedConversation()
	threadID := ""
	if isConv {
		threadID = env.ThreadID
	}
	previous := ra.uiApp.GetFocus()
	closePicker := func() {
		ra.uiApp.Pages.RemovePage("label-picker")
		ra.labelPicker = nil
		if previous != nil {
			ra.uiApp.SetFocus(previous)
		}
	}
	ra.labelPicker = NewLabelPicker(ra.uiApp.Skin, names, lab.CurrentLabels(threadID, env.ID),
		func(add, remove []string) {
			closePicker()
			if len(add)+len(remove) == 0 {
				return
			}
			ra.applyToConversation(acct, eng, folder, "Labels updated", false, func(ctx context.Context) error {
				if isConv {
					return th.ModifyThread(ctx, threadID, add, remove)
				}
				return lab.ModifyLabels(ctx, folder.Name, env.ID, add, remove)
			})
		}, closePicker)
	ra.uiApp.Pages.AddPage("label-picker", ra.labelPicker, true, true)
	ra.uiApp.SetFocus(ra.labelPicker)
}
