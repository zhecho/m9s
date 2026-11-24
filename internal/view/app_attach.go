package view

import (
	"fmt"
	"strings"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/ui"
)

const (
	pageAttachPicker = "attach-picker"
	pageAttachDir    = "attach-dir"

	// notDownloadedAttach is said when a save or open needs a body m9s
	// cannot fetch right now.
	notDownloadedAttach = "not downloaded yet — try again when connected"

	defaultSaveDir = "~/Downloads"
)

// attachRequest is a save or open waiting for the body of env.
type attachRequest struct {
	env    *mail.Envelope
	action ui.Action
}

// attachKey handles s and o: it acts on the shown message, or fetches the
// selected one first. UI goroutine only.
func (ra *RootApp) attachKey(action ui.Action) {
	env := ra.selectedEnv
	if env == nil {
		return
	}
	if ra.currentMsg == nil {
		ra.attachPending = &attachRequest{env: env, action: action}
		ra.uiApp.StatusBar.Info("Loading message…")
		ra.bodyLoader(ra.manager.Active(), ra.currentFolder, env, false)
		return
	}
	ra.attachFor(action)
}

// resumeAttach runs a save or open that was waiting for env's body.
func (ra *RootApp) resumeAttach(env *mail.Envelope) {
	if p := ra.attachPending; p != nil && p.env.ID == env.ID {
		ra.attachPending = nil
		ra.attachFor(p.action)
	}
}

// bodyFailed reports a body that could not be downloaded: to the save or
// open waiting for it, if any, else as any failed fetch. UI goroutine only.
func (ra *RootApp) bodyFailed(acct *mail.Account, env *mail.Envelope, err error) {
	if p := ra.attachPending; p != nil && p.env.ID == env.ID {
		ra.attachPending = nil
		ui.AppLog.Warn("[%s] fetch body: %v", acct.Name, err)
		ra.uiApp.StatusBar.Error(notDownloadedAttach)
		return
	}
	ra.fetchFailed(acct.Name, "fetch body", err)
}

// attachmentFiles are the files s and o offer: on a conversation row in the
// mail list, those of every downloaded message of the conversation; else
// those of the shown message. missing reports that a body is not here.
func (ra *RootApp) attachmentFiles() (files []*mail.Attachment, missing bool) {
	if _, _, _, _, isConv := ra.selectedConversation(); isConv && ra.conv != nil && ra.mailListFocused() {
		for _, m := range ra.conv.msgs {
			if !ra.conv.downloaded(m) {
				missing = true
				continue
			}
			files = append(files, m.Files()...)
		}
		return files, missing
	}
	if ra.currentMsg == nil {
		return nil, true
	}
	if ra.conv != nil && !ra.conv.downloaded(ra.currentMsg) {
		return nil, true
	}
	return ra.currentMsg.Files(), false
}

// attachFor offers the files for action: straight away when there is one,
// else through the picker.
func (ra *RootApp) attachFor(action ui.Action) {
	files, missing := ra.attachmentFiles()
	if len(files) == 0 {
		if missing {
			ra.uiApp.StatusBar.Error(notDownloadedAttach)
		} else {
			ra.uiApp.StatusBar.Info("No attachments")
		}
		return
	}
	if len(files) == 1 {
		ra.attachChosen(action, files)
		return
	}
	previous := ra.uiApp.GetFocus()
	closePicker := func() {
		ra.uiApp.Pages.RemovePage(pageAttachPicker)
		ra.attachPicker = nil
		if previous != nil {
			ra.uiApp.SetFocus(previous)
		}
	}
	ra.attachPicker = NewAttachmentPicker(ra.uiApp.Skin, files, action == ui.ActionSaveAttachments,
		func(picked []*mail.Attachment) {
			closePicker()
			ra.attachChosen(action, picked)
		}, closePicker)
	ra.uiApp.Pages.AddPage(pageAttachPicker, ra.attachPicker, true, true)
	ra.uiApp.SetFocus(ra.attachPicker)
}

// attachChosen saves or opens the picked files.
func (ra *RootApp) attachChosen(action ui.Action, files []*mail.Attachment) {
	if action == ui.ActionOpenAttachment {
		ra.openAttachment(files[0])
		return
	}
	if dir := ra.cfg.UI.AttachmentDir; dir != "" {
		ra.saveTo(dir, files)
		return
	}
	ra.askSaveDir(files)
}

// openAttachment hands one file to its default app, unless it could run a
// program.
func (ra *RootApp) openAttachment(a *mail.Attachment) {
	name := safeName(a, 1)
	if runnable(name) {
		key := ra.uiApp.KeyMap.KeyFor(ui.ActionSaveAttachments)
		if key == "" {
			key = "s"
		}
		ra.uiApp.StatusBar.Error(fmt.Sprintf("%s can run programs: save it with %s instead", name, key))
		return
	}
	path, err := ra.browser.writeFile(name, a.Data)
	if err == nil {
		err = ra.openBrowser(path)
	}
	if err != nil {
		ra.uiApp.StatusBar.Error("open attachment: " + err.Error())
		return
	}
	ra.uiApp.StatusBar.Info("Opened " + name)
}

// saveTo saves files into dir (as the owner typed it; ~ is expanded).
func (ra *RootApp) saveTo(dir string, files []*mail.Attachment) {
	saved, err := saveFiles(expandHome(dir), files)
	if err != nil {
		msg := "save attachments: " + err.Error()
		if len(saved) > 0 {
			msg = fmt.Sprintf("saved %d of %d files to %s; %s", len(saved), len(files), dir, msg)
		}
		ra.uiApp.StatusBar.Error(msg)
		return
	}
	if len(saved) == 1 {
		ra.uiApp.StatusBar.Info(fmt.Sprintf("Saved %s to %s", saved[0], dir))
		return
	}
	ra.uiApp.StatusBar.Info(fmt.Sprintf("Saved %d files to %s", len(saved), dir))
}

// askSaveDir asks where to save, pre-filled with the folder used last.
func (ra *RootApp) askSaveDir(files []*mail.Attachment) {
	start := ra.lastSaveDir
	if start == "" {
		start = defaultSaveDir
	}
	previous := ra.uiApp.GetFocus()
	closeAsk := func() {
		ra.uiApp.Pages.RemovePage(pageAttachDir)
		ra.attachDir = nil
		if previous != nil {
			ra.uiApp.SetFocus(previous)
		}
	}
	skin := ra.uiApp.Skin
	input := tview.NewInputField().SetLabel("Save to: ").SetText(start)
	input.SetBorder(true)
	input.SetTitle(fmt.Sprintf(" Save %d file(s) · enter saves · esc cancels ", len(files)))
	input.SetBorderColor(skin.Color("accent"))
	input.SetBackgroundColor(skin.Color("selected_bg"))
	input.SetFieldBackgroundColor(skin.Color("background"))
	input.SetFieldTextColor(skin.Color("foreground"))
	input.SetLabelColor(skin.Color("accent"))
	input.SetDoneFunc(func(key tcell.Key) {
		dir := strings.TrimSpace(input.GetText())
		closeAsk()
		if key != tcell.KeyEnter || dir == "" {
			return
		}
		ra.lastSaveDir = dir
		ra.saveTo(dir, files)
	})
	ra.attachDir = input
	box := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(input, 3, 0, true).
			AddItem(nil, 0, 1, false), 64, 0, true).
		AddItem(nil, 0, 1, false)
	ra.uiApp.Pages.AddPage(pageAttachDir, box, true, true)
	ra.uiApp.SetFocus(input)
}
