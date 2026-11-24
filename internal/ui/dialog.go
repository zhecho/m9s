package ui

import (
	"strings"
	"unicode/utf8"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

// DialogButton is one button of a Dialog. A Danger button is drawn in the
// skin's error colour so a destructive choice stands out.
type DialogButton struct {
	Label  string
	Danger bool
}

// Dialog is a centred confirmation box. Unlike tview.Modal it can colour
// buttons individually. Esc is the same as choosing a button labelled
// "Cancel" (or the last button if there is none).
type Dialog struct {
	*tview.Flex
	form    *tview.Form
	text    string
	buttons []DialogButton
	focus   int
}

// NewDialog builds a dialog with focus on buttons[focus]. done receives the
// chosen button's index and label.
func NewDialog(skin *Skin, text string, buttons []DialogButton, focus int, done func(index int, label string)) *Dialog {
	d := &Dialog{text: text, buttons: buttons, focus: focus}

	body := tview.NewTextView()
	body.SetDynamicColors(false)
	body.SetTextAlign(tview.AlignCenter)
	body.SetText(text)
	body.SetTextColor(skin.Color("foreground"))
	body.SetBackgroundColor(skin.Color("selected_bg"))

	form := tview.NewForm()
	form.SetButtonsAlign(tview.AlignCenter)
	form.SetBackgroundColor(skin.Color("selected_bg"))
	for i, b := range buttons {
		i, label := i, b.Label
		form.AddButton(label, func() { done(i, label) })
		btn := form.GetButton(i)
		if b.Danger {
			// Red at rest; inverted when selected, so it is unmistakable either way.
			btn.SetBackgroundColor(skin.Color("flash_err"))
			btn.SetLabelColor(skin.Color("selected_fg"))
			btn.SetBackgroundColorActivated(skin.Color("selected_fg"))
			btn.SetLabelColorActivated(skin.Color("flash_err"))
		} else {
			btn.SetBackgroundColor(skin.Color("header_bg"))
			btn.SetLabelColor(skin.Color("header_fg"))
			btn.SetBackgroundColorActivated(skin.Color("accent"))
			btn.SetLabelColorActivated(skin.Color("background"))
		}
	}
	form.SetCancelFunc(func() {
		i := len(buttons) - 1
		for j, b := range buttons {
			if b.Label == "Cancel" {
				i = j
			}
		}
		if i >= 0 {
			done(i, buttons[i].Label)
		}
	})
	d.form = form

	lines := strings.Split(text, "\n")
	width := 0
	for _, l := range lines {
		width = max(width, utf8.RuneCountInString(l))
	}
	buttonsWidth := 0
	for _, b := range buttons {
		buttonsWidth += utf8.RuneCountInString(b.Label) + 6
	}
	width = max(width, buttonsWidth) + 6
	height := len(lines) + 6

	box := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(body, len(lines)+1, 0, false).
		AddItem(form, 3, 0, true)
	box.SetBorder(true)
	box.SetBorderColor(skin.Color("flash_err"))
	box.SetBackgroundColor(skin.Color("selected_bg"))

	d.Flex = tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(box, height, 0, true).
			AddItem(nil, 0, 1, false), width, 0, true).
		AddItem(nil, 0, 1, false)
	return d
}

// Focus puts the keyboard on the initially focused button.
func (d *Dialog) Focus(delegate func(p tview.Primitive)) {
	d.form.SetFocus(d.focus) // no form fields: index n is button n
	delegate(d.form)
}

// HasFocus reports whether the dialog's buttons have focus.
func (d *Dialog) HasFocus() bool { return d.form.HasFocus() }

// InputHandler forwards keys to the buttons.
func (d *Dialog) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return d.form.InputHandler()
}

// ButtonLabels returns the labels in order (for tests).
func (d *Dialog) ButtonLabels() []string {
	out := make([]string, len(d.buttons))
	for i, b := range d.buttons {
		out[i] = b.Label
	}
	return out
}

// Buttons returns the buttons as given (for tests).
func (d *Dialog) Buttons() []DialogButton { return append([]DialogButton(nil), d.buttons...) }

// Text returns the dialog's message (for tests).
func (d *Dialog) Text() string { return d.text }
