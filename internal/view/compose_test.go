package view

import (
	"strings"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/mail"
)

func makeTestAcct(smtpUser, imapHost string) *mail.Account {
	return &mail.Account{
		Name: "test",
		Config: &config.Account{
			SMTP: config.SMTPConfig{User: smtpUser},
			IMAP: config.IMAPConfig{Host: imapHost},
		},
	}
}

func makeTestMsg(subject, fromEmail string, toEmails []string, body string) *mail.Message {
	to := make([]mail.Address, len(toEmails))
	for i, e := range toEmails {
		to[i] = mail.Address{Email: e}
	}
	return &mail.Message{
		Envelope: &mail.Envelope{
			UID:       42,
			MessageID: "orig-123@example.com",
			Subject:   subject,
			From:      []mail.Address{{Email: fromEmail}},
			To:        to,
			Date:      time.Now(),
		},
		TextBody: body,
	}
}

func TestBuildReplyDraft_basic(t *testing.T) {
	acct := makeTestAcct("me@example.com", "imap.example.com")
	msg := makeTestMsg("Hello", "alice@example.com", []string{"me@example.com"}, "original text")

	d := buildReplyDraft(acct, msg, false)

	if len(d.To) != 1 || d.To[0] != "alice@example.com" {
		t.Errorf("want To=[alice@example.com], got %v", d.To)
	}
	if d.Subject != "Re: Hello" {
		t.Errorf("want Subject=Re: Hello, got %q", d.Subject)
	}
	if d.InReplyTo != "<orig-123@example.com>" {
		t.Errorf("want InReplyTo=<orig-123@example.com>, got %q", d.InReplyTo)
	}
	if d.References != "<orig-123@example.com>" {
		t.Errorf("want References=<orig-123@example.com>, got %q", d.References)
	}
	if !strings.Contains(d.Body, "original text") {
		t.Errorf("want body to contain 'original text', got %q", d.Body)
	}
	if len(d.CC) != 0 {
		t.Errorf("want no CC for simple reply, got %v", d.CC)
	}
}

func TestBuildReplyDraft_replyAll_cc(t *testing.T) {
	acct := makeTestAcct("me@example.com", "imap.example.com")
	msg := makeTestMsg("Hello", "alice@example.com",
		[]string{"bob@example.com", "carol@example.com"}, "text")

	d := buildReplyDraft(acct, msg, true)

	if len(d.CC) != 2 {
		t.Errorf("want 2 CC entries (bob, carol), got %v", d.CC)
	}
}

func TestBuildReplyDraft_replyAll_excludesSelf(t *testing.T) {
	acct := makeTestAcct("me@example.com", "imap.example.com")
	msg := makeTestMsg("Hello", "alice@example.com",
		[]string{"me@example.com", "bob@example.com"}, "text")

	d := buildReplyDraft(acct, msg, true)

	for _, cc := range d.CC {
		if cc == "me@example.com" {
			t.Error("CC must not include sender's own address")
		}
	}
	if len(d.CC) != 1 || d.CC[0] != "bob@example.com" {
		t.Errorf("want CC=[bob@example.com], got %v", d.CC)
	}
}

func TestBuildReplyDraft_referencesChain(t *testing.T) {
	acct := makeTestAcct("me@example.com", "imap.example.com")
	msg := makeTestMsg("Re: Hello", "alice@example.com", []string{"me@example.com"}, "x")
	msg.Envelope.InReplyTo = []string{"root-1@example.com"}

	d := buildReplyDraft(acct, msg, false)

	want := "<root-1@example.com> <orig-123@example.com>"
	if d.References != want {
		t.Errorf("want References=%q, got %q", want, d.References)
	}
}

func TestBuildReplyDraft_noMessageID(t *testing.T) {
	acct := makeTestAcct("me@example.com", "imap.example.com")
	msg := makeTestMsg("Hello", "alice@example.com", []string{"me@example.com"}, "x")
	msg.Envelope.MessageID = ""

	d := buildReplyDraft(acct, msg, false)

	if d.InReplyTo != "" || d.References != "" {
		t.Errorf("want no threading headers, got InReplyTo=%q References=%q", d.InReplyTo, d.References)
	}
}
