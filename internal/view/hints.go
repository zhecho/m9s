package view

import "github.com/zhecho/m9s/internal/ui"

// paneKind identifies which of the three panes has focus.
type paneKind int

const (
	paneFolders paneKind = iota
	paneMailList
	panePreview
)

// generalHints are the keys that work from every pane. Labels of rebindable
// actions come from km; an unbound action yields an empty Key, which the
// header skips.
func generalHints(km ui.KeyMap) []ui.KeyHint {
	accounts := ""
	prev, next := km.KeyFor(ui.ActionSwitchAccountPrev), km.KeyFor(ui.ActionSwitchAccountNext)
	switch {
	case prev != "" && next != "":
		accounts = prev + " " + next
	case prev != "":
		accounts = prev
	default:
		accounts = next
	}
	return []ui.KeyHint{
		{Key: "j/k", Desc: "Navigate"},
		{Key: "g/G", Desc: "Top/Bottom"},
		{Key: "tab", Desc: "Next pane"},
		{Key: accounts, Desc: "Account"},
		{Key: km.KeyFor(ui.ActionCommandBar), Desc: "Command"},
		{Key: km.KeyFor(ui.ActionCompose), Desc: "Compose"},
		{Key: km.KeyFor(ui.ActionSync), Desc: "Sync"},
		{Key: km.KeyFor(ui.ActionLog), Desc: "Logs"},
		{Key: km.KeyFor(ui.ActionSearchLocal), Desc: "Filter"},
		{Key: km.KeyFor(ui.ActionHelp), Desc: "Help"},
		{Key: km.KeyFor(ui.ActionBack), Desc: "Quit"},
	}
}

// paneHints are the keys that only make sense in the focused pane. Only keys
// that actually do something there are listed.
// canEdit adds <e> Edit: on a local draft, or in the server's Drafts folder.
func paneHints(km ui.KeyMap, pane paneKind, canEdit bool) []ui.KeyHint {
	var hints []ui.KeyHint
	if pane == paneFolders {
		hints = []ui.KeyHint{{Key: "enter", Desc: "Open"}}
		if canEdit {
			hints = append(hints,
				ui.KeyHint{Key: km.KeyFor(ui.ActionEdit), Desc: "Edit"},
				ui.KeyHint{Key: km.KeyFor(ui.ActionDelete), Desc: "Delete"})
		}
		return hints
	}
	hints = []ui.KeyHint{
		{Key: km.KeyFor(ui.ActionReply), Desc: "Reply"},
		{Key: km.KeyFor(ui.ActionReplyAll), Desc: "Reply all"},
		{Key: km.KeyFor(ui.ActionDelete), Desc: "Delete"},
		{Key: km.KeyFor(ui.ActionMarkUnread), Desc: "Unread"},
		{Key: km.KeyFor(ui.ActionOpenBrowser), Desc: "Browser"},
		{Key: km.KeyFor(ui.ActionSaveAttachments), Desc: "Save"},
		{Key: km.KeyFor(ui.ActionOpenAttachment), Desc: "Open"},
	}
	if canEdit {
		hints = append(hints, ui.KeyHint{Key: km.KeyFor(ui.ActionEdit), Desc: "Edit"})
	}
	return hints
}

// promptHints replace all other hints while the : or / prompt is open.
func promptHints(command bool) []ui.KeyHint {
	hints := []ui.KeyHint{
		{Key: "enter", Desc: "Accept"},
		{Key: "esc", Desc: "Cancel"},
	}
	if command {
		hints = append(hints, ui.KeyHint{Key: "tab", Desc: "Complete"})
	}
	return hints
}
