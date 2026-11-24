package view

import (
	"slices"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/ui"
)

// LabelPicker is a centred checklist of labels: Space toggles the label
// under the cursor, Enter applies the difference to the starting state, Esc
// cancels.
type LabelPicker struct {
	*tview.Flex
	list    *tview.List
	names   []string
	start   map[string]bool
	checked map[string]bool
	apply   func(add, remove []string)
	cancel  func()
}

// NewLabelPicker lists names, with those in checked ticked.
func NewLabelPicker(skin *ui.Skin, names, checked []string, apply func(add, remove []string), cancel func()) *LabelPicker {
	p := &LabelPicker{names: names, start: map[string]bool{}, checked: map[string]bool{}, apply: apply, cancel: cancel}
	for _, n := range checked {
		p.start[n], p.checked[n] = true, true
	}
	p.list = tview.NewList()
	p.list.ShowSecondaryText(false)
	p.list.SetBackgroundColor(skin.Color("selected_bg"))
	p.list.SetMainTextColor(skin.Color("foreground"))
	p.list.SetSelectedBackgroundColor(skin.Color("accent"))
	p.list.SetSelectedTextColor(skin.Color("background"))
	p.list.SetBorder(true)
	p.list.SetTitle(" Labels · space toggles · enter applies ")
	p.list.SetBorderColor(skin.Color("accent"))
	for _, n := range names {
		p.list.AddItem(p.row(n), "", 0, nil)
	}
	height := min(len(names)+2, 20)
	p.Flex = tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(p.list, height, 0, true).
			AddItem(nil, 0, 1, false), 44, 0, true).
		AddItem(nil, 0, 1, false)
	return p
}

func (p *LabelPicker) row(name string) string {
	mark := "[ ] "
	if p.checked[name] {
		mark = "[x] "
	}
	return tview.Escape(mark + name)
}

// Names returns the listed labels (for tests).
func (p *LabelPicker) Names() []string { return slices.Clone(p.names) }

// Focus puts the keyboard on the list.
func (p *LabelPicker) Focus(delegate func(tview.Primitive)) { delegate(p.list) }

// HasFocus reports whether the list has focus.
func (p *LabelPicker) HasFocus() bool { return p.list.HasFocus() }

// InputHandler handles the picker's keys and leaves movement to the list.
func (p *LabelPicker) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return func(ev *tcell.EventKey, setFocus func(tview.Primitive)) {
		switch {
		case ev.Key() == tcell.KeyEscape:
			p.cancel()
		case ev.Key() == tcell.KeyEnter:
			var add, remove []string
			for _, n := range p.names {
				switch {
				case p.checked[n] && !p.start[n]:
					add = append(add, n)
				case !p.checked[n] && p.start[n]:
					remove = append(remove, n)
				}
			}
			p.apply(add, remove)
		case ev.Key() == tcell.KeyRune && ev.Rune() == ' ':
			i := p.list.GetCurrentItem()
			if i >= 0 && i < len(p.names) {
				n := p.names[i]
				p.checked[n] = !p.checked[n]
				p.list.SetItemText(i, p.row(n), "")
			}
		default:
			p.list.InputHandler()(ev, setFocus)
		}
	}
}
