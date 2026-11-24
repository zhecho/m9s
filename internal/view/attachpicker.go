package view

import (
	"fmt"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/ui"
)

// AttachmentPicker is a centred list of a message's files: Enter picks the
// row under the cursor (or, for save, "All files"), Esc cancels.
type AttachmentPicker struct {
	*tview.Flex
	list    *tview.List
	files   []*mail.Attachment
	withAll bool
	choose  func([]*mail.Attachment)
	cancel  func()
}

// NewAttachmentPicker lists files; withAll puts "All files (N)" first and
// titles it for saving, else for opening.
func NewAttachmentPicker(skin *ui.Skin, files []*mail.Attachment, withAll bool, choose func([]*mail.Attachment), cancel func()) *AttachmentPicker {
	p := &AttachmentPicker{files: files, withAll: withAll, choose: choose, cancel: cancel}
	p.list = tview.NewList()
	p.list.ShowSecondaryText(false)
	p.list.SetBackgroundColor(skin.Color("selected_bg"))
	p.list.SetMainTextColor(skin.Color("foreground"))
	p.list.SetSelectedBackgroundColor(skin.Color("accent"))
	p.list.SetSelectedTextColor(skin.Color("background"))
	p.list.SetBorder(true)
	p.list.SetBorderColor(skin.Color("accent"))
	title := " Open attachment "
	if withAll {
		title = " Save attachment "
		p.list.AddItem(fmt.Sprintf("All files (%d)", len(files)), "", 0, nil)
	}
	p.list.SetTitle(title)
	width := 34
	for i, a := range files {
		name := safeName(a, i+1)
		row := fmt.Sprintf("%-26s %8s", name, humanSize(len(a.Data)))
		width = max(width, len([]rune(row))+4)
		p.list.AddItem(tview.Escape(row), "", 0, nil)
	}
	height := min(p.list.GetItemCount()+2, 20)
	p.Flex = tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(p.list, height, 0, true).
			AddItem(nil, 0, 1, false), min(width, 72), 0, true).
		AddItem(nil, 0, 1, false)
	return p
}

// Focus puts the keyboard on the list.
func (p *AttachmentPicker) Focus(delegate func(tview.Primitive)) { delegate(p.list) }

// HasFocus reports whether the list has focus.
func (p *AttachmentPicker) HasFocus() bool { return p.list.HasFocus() }

// InputHandler handles Enter and Esc and leaves movement to the list.
func (p *AttachmentPicker) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return func(ev *tcell.EventKey, setFocus func(tview.Primitive)) {
		switch ev.Key() {
		case tcell.KeyEscape:
			p.cancel()
		case tcell.KeyEnter:
			i := p.list.GetCurrentItem()
			if p.withAll {
				if i == 0 {
					p.choose(p.files)
					return
				}
				i--
			}
			if i >= 0 && i < len(p.files) {
				p.choose([]*mail.Attachment{p.files[i]})
			}
		default:
			if h := p.list.InputHandler(); h != nil {
				h(ev, setFocus)
			}
		}
	}
}
