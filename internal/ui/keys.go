package ui

import (
	"os"
	"sort"

	"github.com/derailed/tcell/v2"
	"gopkg.in/yaml.v3"
)

// Action is a named user action.
type Action string

const (
	ActionNavigateDown      Action = "navigate_down"
	ActionNavigateUp        Action = "navigate_up"
	ActionScrollPreviewDown Action = "scroll_preview_down"
	ActionScrollPreviewUp   Action = "scroll_preview_up"
	ActionFocusLeft         Action = "focus_left"
	ActionFocusRight        Action = "focus_right"
	ActionTabCycle          Action = "tab_cycle"
	ActionOpen              Action = "open"
	ActionCompose           Action = "compose"
	ActionReply             Action = "reply"
	ActionReplyAll          Action = "reply_all"
	ActionForward           Action = "forward"
	ActionDelete            Action = "delete"
	ActionMarkUnread        Action = "mark_unread"
	ActionStar              Action = "star"
	ActionSync              Action = "sync"
	ActionArchive           Action = "archive"
	ActionSearchLocal       Action = "search_local"
	ActionLabels            Action = "labels"
	ActionSwitchAccountNext Action = "switch_account_next"
	ActionSwitchAccountPrev Action = "switch_account_prev"
	ActionCommandBar        Action = "command_bar"
	ActionBack              Action = "back"
	ActionJumpTop           Action = "jump_top"
	ActionJumpBottom        Action = "jump_bottom"
	ActionLog               Action = "log"
	ActionEdit              Action = "edit"
	ActionOpenBrowser       Action = "open_browser"
	ActionSaveAttachments   Action = "save_attachments"
	ActionOpenAttachment    Action = "open_attachment"
	ActionHelp              Action = "help"
	ActionMove              Action = "move"
	ActionMark              Action = "mark"
)

var defaultBindings = map[Action]rune{
	ActionNavigateDown:      'j',
	ActionNavigateUp:        'k',
	ActionScrollPreviewDown: 'J',
	ActionScrollPreviewUp:   'K',
	ActionFocusLeft:         'h',
	ActionLog:               'l',
	ActionCompose:           'c',
	ActionReply:             'r',
	ActionReplyAll:          'R',
	ActionForward:           'f',
	ActionDelete:            'd',
	ActionMarkUnread:        'u',
	ActionStar:              '*',
	ActionSync:              'S',
	ActionArchive:           'a',
	ActionSearchLocal:       '/',
	ActionLabels:            'L',
	ActionSwitchAccountNext: ']',
	ActionSwitchAccountPrev: '[',
	ActionCommandBar:        ':',
	ActionBack:              'q',
	ActionEdit:              'e',
	ActionOpenBrowser:       'b',
	ActionSaveAttachments:   's',
	ActionOpenAttachment:    'o',
	ActionHelp:              '?',
	ActionMove:              'm',
	ActionMark:              ' ',
}

// knownActions are the names a key file may bind.
var knownActions = map[Action]bool{
	ActionNavigateDown: true, ActionNavigateUp: true, ActionScrollPreviewDown: true,
	ActionScrollPreviewUp: true, ActionFocusLeft: true, ActionFocusRight: true,
	ActionTabCycle: true, ActionOpen: true, ActionCompose: true, ActionReply: true,
	ActionReplyAll: true, ActionForward: true, ActionDelete: true, ActionMarkUnread: true,
	ActionStar: true, ActionSync: true, ActionArchive: true, ActionSearchLocal: true,
	ActionLabels: true, ActionSwitchAccountNext: true, ActionSwitchAccountPrev: true,
	ActionCommandBar: true, ActionBack: true, ActionJumpTop: true, ActionJumpBottom: true,
	ActionLog: true, ActionEdit: true, ActionOpenBrowser: true,
	ActionSaveAttachments: true, ActionOpenAttachment: true, ActionHelp: true, ActionMove: true,
	ActionMark: true,
}

// KeyMap maps runes → Actions.
type KeyMap map[rune]Action

// LoadKeyMap loads keybindings from a YAML file, falling back to defaults.
// Overrides are applied in name order. One naming an unknown action is
// skipped, and one taking a key another action holds unbinds that action;
// both are logged, so no key is shared by two actions.
func LoadKeyMap(path string) (KeyMap, error) {
	bindings := make(map[Action]rune, len(defaultBindings))
	for a, r := range defaultBindings {
		bindings[a] = r
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			var overrides map[string]string
			if err := yaml.Unmarshal(data, &overrides); err == nil {
				applyOverrides(bindings, overrides)
			}
		}
	}
	km := make(KeyMap, len(bindings))
	for a, r := range bindings {
		km[r] = a
	}
	return km, nil
}

func applyOverrides(bindings map[Action]rune, overrides map[string]string) {
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		v := overrides[name]
		if len(v) != 1 {
			continue
		}
		a, r := Action(name), rune(v[0])
		if !knownActions[a] {
			AppLog.Warn("key file: unknown action %q", name)
			continue
		}
		for other, held := range bindings {
			if held == r && other != a {
				delete(bindings, other)
				AppLog.Warn("key %c: %s replaces %s", r, a, other)
			}
		}
		bindings[a] = r
	}
}

// Lookup returns the Action for a key event, or "" if unbound.
func (km KeyMap) Lookup(ev *tcell.EventKey) Action {
	if ev.Key() == tcell.KeyRune {
		return km[ev.Rune()]
	}
	switch ev.Key() {
	case tcell.KeyTab:
		return ActionTabCycle
	case tcell.KeyEnter:
		return ActionOpen
	}
	return ""
}

// KeyFor returns the key bound to a as a printable string ("c", "/", ":"),
// or "" if a is unbound.
func (km KeyMap) KeyFor(a Action) string {
	for r, bound := range km {
		if bound == a {
			return string(r)
		}
	}
	return ""
}
