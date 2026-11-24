package view

import (
	"fmt"
	"strings"

	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/mail"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	"github.com/zhecho/m9s/internal/ui"
)

// PreviewView is the bottom-right message preview pane.
type PreviewView struct {
	*tview.TextView
	skin      *ui.Skin
	focusFunc func()
	blurFunc  func()
}

// NewPreviewView creates the message preview widget.
func NewPreviewView(skin *ui.Skin) *PreviewView {
	tv := tview.NewTextView()
	tv.SetBorder(true)
	tv.SetTitle(" PREVIEW ")
	tv.SetTitleColor(skin.Color("header_fg"))
	tv.SetBorderColor(skin.Color("border"))
	tv.SetBackgroundColor(skin.Color("background"))
	tv.SetTextColor(skin.Color("foreground"))
	tv.SetDynamicColors(true)
	tv.SetScrollable(true)
	tv.SetWordWrap(true)
	return &PreviewView{TextView: tv, skin: skin}
}

// SetFocusFunc registers a callback invoked when this view gains focus.
func (pv *PreviewView) SetFocusFunc(fn func()) { pv.focusFunc = fn }

// SetBlurFunc registers a callback invoked when this view loses focus.
func (pv *PreviewView) SetBlurFunc(fn func()) { pv.blurFunc = fn }

// Focus overrides Box.Focus to fire the registered focusFunc.
func (pv *PreviewView) Focus(delegate func(tview.Primitive)) {
	if pv.focusFunc != nil {
		pv.focusFunc()
	}
	pv.TextView.Focus(delegate)
}

// Blur overrides Box.Blur to fire the registered blurFunc.
func (pv *PreviewView) Blur() {
	if pv.blurFunc != nil {
		pv.blurFunc()
	}
	pv.TextView.Blur()
}

// ShowMessage renders a fully-fetched message in the preview pane.
func (pv *PreviewView) ShowMessage(msg *mail.Message) {
	if msg == nil {
		pv.SetText("")
		return
	}
	env := msg.Envelope
	dim := pv.skin.Colors["dim"]
	header := fmt.Sprintf(
		"[%s::]From:[-:-:-]    %s\n[%s::]Subject:[-:-:-] %s\n[%s::]Date:[-:-:-]    %s\n",
		dim, env.FromDisplay(),
		dim, env.Subject,
		dim, env.Date.Format("Mon, 02 Jan 2006 15:04:05 -0700"),
	)
	header += categoriesLine(env, dim) + "\n"
	pv.SetText(header + msg.PlainText())
	pv.ScrollToBeginning()
}

// ShowConversation renders a conversation stacked: newest expanded, older
// messages one header line each.
func (pv *PreviewView) ShowConversation(c *conversation) {
	pv.SetText(c.render(pv.skin, false))
	pv.ScrollToBeginning() // older messages are one line each: the newest is near the top
}

// ShowDraft renders a local draft.
func (pv *PreviewView) ShowDraft(d smtpadapter.Draft) {
	dim := pv.skin.Colors["dim"]
	pv.SetText(fmt.Sprintf(
		"[%s::]From:[-:-:-]    %s\n[%s::]To:[-:-:-]      %s\n[%s::]Subject:[-:-:-] %s\n\n%s\n\n[%s::]Local draft — e to edit, d to delete[-:-:-]",
		dim, tview.Escape(d.From),
		dim, tview.Escape(strings.Join(d.To, ", ")),
		dim, tview.Escape(d.Subject),
		tview.Escape(d.Body),
		dim,
	))
	pv.ScrollToBeginning()
}

// ShowEnvelope renders a loading placeholder while the full message is fetched.
func (pv *PreviewView) ShowEnvelope(env *mail.Envelope) {
	dim := pv.skin.Colors["dim"]
	text := fmt.Sprintf(
		"[%s::]From:[-:-:-]    %s\n[%s::]Subject:[-:-:-] %s\n\n[grey::]Loading…[-:-:-]",
		dim, env.FromDisplay(),
		dim, env.Subject,
	)
	pv.SetText(text)
	pv.ScrollToBeginning()
}
