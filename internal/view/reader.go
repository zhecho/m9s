package view

import (
	"fmt"
	"strings"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/ui"
)

// ReaderView is the full-screen message reader (shown on Enter).
type ReaderView struct {
	*tview.TextView
	skin    *ui.Skin
	onClose func()
}

// NewReaderView creates the full-screen reader widget.
func NewReaderView(skin *ui.Skin, onClose func()) *ReaderView {
	tv := tview.NewTextView()
	tv.SetBorder(true)
	tv.SetTitleColor(skin.Color("accent"))
	tv.SetBorderColor(skin.Color("border"))
	tv.SetBackgroundColor(skin.Color("background"))
	tv.SetTextColor(skin.Color("foreground"))
	tv.SetDynamicColors(true)
	tv.SetScrollable(true)
	tv.SetWordWrap(true)

	rv := &ReaderView{TextView: tv, skin: skin, onClose: onClose}
	tv.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape ||
			(event.Key() == tcell.KeyRune && event.Rune() == 'q') {
			if rv.onClose != nil {
				rv.onClose()
			}
			return nil
		}
		return event
	})
	return rv
}

// ShowMessage renders a full message in the reader view.
func (rv *ReaderView) ShowMessage(msg *mail.Message) {
	env := msg.Envelope
	dim := rv.skin.Colors["dim"]
	acc := rv.skin.Colors["accent"]

	var froms, tos []string
	for _, a := range env.From {
		froms = append(froms, a.String())
	}
	for _, a := range env.To {
		tos = append(tos, a.String())
	}

	header := fmt.Sprintf(
		"[%s::]From:[-:-:-]    %s\n[%s::]To:[-:-:-]      %s\n[%s::]Subject:[-:-:-] [%s::]%s[-:-:-]\n[%s::]Date:[-:-:-]    %s\n",
		dim, strings.Join(froms, ", "),
		dim, strings.Join(tos, ", "),
		dim, acc, env.Subject,
		dim, env.Date.Format("Mon, 02 Jan 2006 15:04:05 -0700"),
	)
	if files := msg.Files(); len(files) > 0 {
		header += fmt.Sprintf("[%s::]Attach:[-:-:-]  %d file(s)\n", dim, len(files))
	}
	header += categoriesLine(env, dim)
	rv.SetTitle(" " + env.Subject + " ")
	rv.SetText(header + "\n" + msg.PlainText())
	rv.ScrollToBeginning()
}

// categoriesLine is the header line naming a message's Outlook categories,
// "" when it has none.
func categoriesLine(env *mail.Envelope, dim string) string {
	if len(env.Categories) == 0 {
		return ""
	}
	return fmt.Sprintf("[%s::]Categories:[-:-:-] %s\n", dim, tview.Escape(strings.Join(env.Categories, ", ")))
}

// ShowConversation renders a conversation stacked, the cursor's header
// marked: Tab and Shift-Tab move it, Enter expands or collapses. keepScroll
// leaves the view where it was (a message was expanded or collapsed).
func (rv *ReaderView) ShowConversation(c *conversation, keepScroll bool) {
	row, _ := rv.GetScrollOffset()
	rv.SetTitle(" " + c.subject() + " ")
	rv.SetText(c.render(rv.skin, true))
	if keepScroll {
		rv.ScrollTo(row, 0)
	} else {
		rv.ScrollToBeginning()
	}
}
