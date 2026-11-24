package view

import (
	"fmt"
	"strings"

	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/ui"
)

// conversation is a thread as the preview and the reader show it: its
// messages stacked oldest to newest, the newest expanded and the others one
// header line each. cursor is the header line Tab and Enter act on.
type conversation struct {
	msgs     []*mail.Message // oldest first
	expanded []bool
	cursor   int
	offline  bool // read from the store: may be incomplete
	// missing holds the messages whose body was never downloaded: they
	// show a placeholder, which must never be quoted in a reply.
	missing map[*mail.Message]bool
}

// downloaded reports whether m's body is really here, not a placeholder.
func (c *conversation) downloaded(m *mail.Message) bool { return !c.missing[m] }

func newConversation(msgs []*mail.Message, offline bool) *conversation {
	c := &conversation{msgs: msgs, expanded: make([]bool, len(msgs)), offline: offline}
	if n := len(msgs); n > 0 {
		c.expanded[n-1] = true
		c.cursor = n - 1
	}
	return c
}

// move puts the cursor on the next (+1) or previous (-1) header, wrapping.
func (c *conversation) move(step int) {
	if n := len(c.msgs); n > 0 {
		c.cursor = ((c.cursor+step)%n + n) % n
	}
}

// toggle expands or collapses the message under the cursor.
func (c *conversation) toggle() {
	if c.cursor < len(c.expanded) {
		c.expanded[c.cursor] = !c.expanded[c.cursor]
	}
}

// target is the message reply and forward act on: the one under the cursor
// when it is expanded, else the newest expanded one, else the newest.
func (c *conversation) target() *mail.Message {
	if len(c.msgs) == 0 {
		return nil
	}
	if c.expanded[c.cursor] {
		return c.msgs[c.cursor]
	}
	for i := len(c.msgs) - 1; i >= 0; i-- {
		if c.expanded[i] {
			return c.msgs[i]
		}
	}
	return c.msgs[len(c.msgs)-1]
}

// subject is the conversation's subject, as its first message has it.
func (c *conversation) subject() string {
	if len(c.msgs) == 0 {
		return ""
	}
	return c.msgs[0].Envelope.Subject
}

// render draws the stacked messages; withCursor marks the cursor's header.
func (c *conversation) render(skin *ui.Skin, withCursor bool) string {
	dim, acc := skin.Colors["dim"], skin.Colors["accent"]
	var b strings.Builder
	fmt.Fprintf(&b, "[%s::]Subject:[-:-:-] [%s::]%s[-:-:-]  [%s::](%d messages)[-:-:-]\n",
		dim, acc, tview.Escape(c.subject()), dim, len(c.msgs))
	for i, m := range c.msgs {
		env := m.Envelope
		mark, style := "▸", ""
		if c.expanded[i] {
			mark = "▾"
		}
		if withCursor && i == c.cursor {
			style = "::r"
		}
		b.WriteString("\n")
		if style != "" {
			fmt.Fprintf(&b, "[%s]", style)
		}
		fmt.Fprintf(&b, "%s %s  %s", mark, tview.Escape(env.FromDisplay()),
			env.Date.Format("Mon, 02 Jan 2006 15:04"))
		if style != "" {
			b.WriteString("[-:-:-]")
		}
		b.WriteString("\n")
		if !c.expanded[i] {
			continue
		}
		var tos []string
		for _, a := range env.To {
			tos = append(tos, a.String())
		}
		fmt.Fprintf(&b, "[%s::]To:[-:-:-] %s\n", dim, tview.Escape(strings.Join(tos, ", ")))
		if files := m.Files(); len(files) > 0 {
			fmt.Fprintf(&b, "[%s::]Attach:[-:-:-] %d file(s)\n", dim, len(files))
		}
		b.WriteString("\n" + tview.Escape(m.PlainText()) + "\n")
	}
	return b.String()
}

// participantsText is the sender cell of a conversation row, Gmail style:
// first names in order of first message, "me" for the owner; a lone
// sender keeps the full name.
func participantsText(env *mail.Envelope, self string) string {
	if len(env.Participants) == 0 {
		return env.FromDisplay()
	}
	var names []string
	for _, a := range env.Participants {
		switch {
		case self != "" && strings.EqualFold(a.Email, self):
			names = append(names, "me")
		case len(env.Participants) == 1 && a.Name != "":
			names = append(names, a.Name)
		case a.Name != "":
			names = append(names, strings.Fields(a.Name)[0])
		default:
			local, _, _ := strings.Cut(a.Email, "@")
			names = append(names, local)
		}
	}
	return strings.Join(names, ", ")
}

// hasFiles reports whether any downloaded message of c carries files; known
// is false when a message without files may just not be downloaded yet.
func (c *conversation) hasFiles() (has, known bool) {
	known = true
	for _, m := range c.msgs {
		if !c.downloaded(m) {
			known = false
			continue
		}
		if len(m.Files()) > 0 {
			return true, true
		}
	}
	return false, known
}
