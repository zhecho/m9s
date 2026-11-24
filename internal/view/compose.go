package view

import (
	"fmt"
	"strings"
	"time"

	"github.com/zhecho/m9s/internal/mail"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
)

// buildReplyDraft constructs a reply Draft from an incoming message.
// If replyAll is true, all To addresses except the account's own are added
// to CC. The reply is from the account's own address (gmail.user on Gmail).
func buildReplyDraft(acct *mail.Account, msg *mail.Message, replyAll bool) smtpadapter.Draft {
	self := selfAddressOf(acct)
	draft := smtpadapter.Draft{
		From:    self,
		To:      []string{msg.Envelope.From[0].Email},
		Subject: "Re: " + msg.Envelope.Subject,
		Body:    "\n\n--- Original Message ---\n" + msg.PlainText(),
	}
	if id := msg.Envelope.MessageID; id != "" {
		// References = parent's ancestry + parent (RFC 5322 §3.6.4). The IMAP
		// envelope has no References, so the parent's In-Reply-To stands in.
		var refs []string
		for _, r := range msg.Envelope.InReplyTo {
			refs = append(refs, "<"+r+">")
		}
		draft.InReplyTo = "<" + id + ">"
		draft.References = strings.Join(append(refs, draft.InReplyTo), " ")
	}
	if replyAll {
		for _, addr := range msg.Envelope.To {
			if !strings.EqualFold(addr.Email, self) {
				draft.CC = append(draft.CC, addr.Email)
			}
		}
	}
	return draft
}

// buildForwardDraft constructs a forward of msg, from the account's own
// address, with nobody to send it to yet. The original's headers head the
// quoted text, as mail clients do. Attachments are not carried: their names
// are listed in the draft and returned.
func buildForwardDraft(acct *mail.Account, msg *mail.Message) (smtpadapter.Draft, []string) {
	env := msg.Envelope
	list := func(addrs []mail.Address) string {
		out := make([]string, len(addrs))
		for i, a := range addrs {
			out[i] = a.String()
		}
		return strings.Join(out, ", ")
	}
	var b strings.Builder
	b.WriteString("\n\n---------- Forwarded message ---------\n")
	fmt.Fprintf(&b, "From: %s\n", list(env.From))
	if !env.Date.IsZero() {
		fmt.Fprintf(&b, "Date: %s\n", env.Date.Format(time.RFC1123Z))
	}
	fmt.Fprintf(&b, "Subject: %s\n", env.Subject)
	if len(env.To) > 0 {
		fmt.Fprintf(&b, "To: %s\n", list(env.To))
	}
	if len(env.CC) > 0 {
		fmt.Fprintf(&b, "Cc: %s\n", list(env.CC))
	}
	var dropped []string
	for i, f := range msg.Files() {
		dropped = append(dropped, safeName(f, i+1))
	}
	if len(dropped) > 0 {
		fmt.Fprintf(&b, "Attachments not included: %s\n", strings.Join(dropped, ", "))
	}
	b.WriteString("\n")
	b.WriteString(msg.PlainText())
	return smtpadapter.Draft{From: selfAddressOf(acct), Subject: forwardSubject(env.Subject), Body: b.String()}, dropped
}

// draftFromMessage turns a message from the server's Drafts folder back into
// an editable draft.
func draftFromMessage(msg *mail.Message) smtpadapter.Draft {
	env := msg.Envelope
	d := smtpadapter.Draft{Subject: env.Subject, Body: msg.PlainText()}
	if len(env.From) > 0 {
		d.From = env.From[0].String()
	}
	for _, a := range env.To {
		d.To = append(d.To, a.String())
	}
	for _, a := range env.CC {
		d.CC = append(d.CC, a.String())
	}
	if len(env.InReplyTo) > 0 {
		d.InReplyTo = "<" + env.InReplyTo[0] + ">"
		d.References = d.InReplyTo
	}
	return d
}
