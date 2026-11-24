package view

import (
	"slices"
	"strings"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/ui"
)

// pageMovePicker is the page of the folder picker m opens.
const pageMovePicker = "move-picker"

// FolderPicker is a centred list of folders, sorted by name: Enter picks the
// one under the cursor, Esc cancels.
type FolderPicker struct {
	*tview.Flex
	list   *tview.List
	items  []string
	choose func(string)
	cancel func()
}

// NewFolderPicker lists folders under title.
func NewFolderPicker(skin *ui.Skin, title string, folders []string, choose func(string), cancel func()) *FolderPicker {
	items := slices.Clone(folders)
	slices.SortFunc(items, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	p := &FolderPicker{list: tview.NewList(), items: items, choose: choose, cancel: cancel}
	p.list.ShowSecondaryText(false)
	p.list.SetBackgroundColor(skin.Color("selected_bg"))
	p.list.SetMainTextColor(skin.Color("foreground"))
	p.list.SetSelectedBackgroundColor(skin.Color("accent"))
	p.list.SetSelectedTextColor(skin.Color("background"))
	p.list.SetBorder(true)
	p.list.SetBorderColor(skin.Color("accent"))
	p.list.SetTitle(" " + tview.Escape(title) + " ")
	width := len([]rune(title)) + 6
	for _, name := range items {
		width = max(width, len([]rune(name))+4)
		p.list.AddItem(tview.Escape(name), "", 0, nil)
	}
	height := min(len(items)+2, 20)
	p.Flex = tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(p.list, height, 0, true).
			AddItem(nil, 0, 1, false), min(width, 60), 0, true).
		AddItem(nil, 0, 1, false)
	return p
}

// Items are the folders in the order shown.
func (p *FolderPicker) Items() []string { return slices.Clone(p.items) }

// Focus puts the keyboard on the list.
func (p *FolderPicker) Focus(delegate func(tview.Primitive)) { delegate(p.list) }

// HasFocus reports whether the list has focus.
func (p *FolderPicker) HasFocus() bool { return p.list.HasFocus() }

// InputHandler handles Enter and Esc and leaves movement to the list.
func (p *FolderPicker) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return func(ev *tcell.EventKey, setFocus func(tview.Primitive)) {
		switch ev.Key() {
		case tcell.KeyEscape:
			p.cancel()
		case tcell.KeyEnter:
			if i := p.list.GetCurrentItem(); i >= 0 && i < len(p.items) {
				p.choose(p.items[i])
			}
		default:
			if h := p.list.InputHandler(); h != nil {
				h(ev, setFocus)
			}
		}
	}
}
