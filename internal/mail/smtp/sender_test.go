package smtp_test

import (
	"bytes"
	"mime"
	netmail "net/mail"
	"regexp"
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/config"
	m9ssmtp "github.com/zhecho/m9s/internal/mail/smtp"
)

func TestNewSender(t *testing.T) {
	cfg := &config.SMTPConfig{
		Host: "smtp.example.com",
		Port: 587,
		User: "user@example.com",
	}
	s := m9ssmtp.New(cfg)
	if s == nil {
		t.Fatal("New returned nil")
	}
}

func TestProbe_unreachable(t *testing.T) {
	cfg := &config.SMTPConfig{
		Host: "localhost",
		Port: 1, // guaranteed refused
		User: "u",
	}
	err := m9ssmtp.Probe(cfg, "pass")
	if err == nil {
		t.Fatal("expected error for unreachable host")
	}
	if !strings.Contains(err.Error(), "dial failed") {
		t.Errorf("expected 'dial failed' in error, got: %v", err)
	}
}

func TestFormatDraft(t *testing.T) {
	d := m9ssmtp.Draft{
		From:    "me@example.com",
		To:      []string{"alice@example.com"},
		Subject: "Hello",
		Body:    "Hi there",
	}
	eml, err := m9ssmtp.FormatDraft(d)
	if err != nil {
		t.Fatalf("FormatDraft: %v", err)
	}
	content := string(eml)
	for _, want := range []string{"From: me@example.com", "To: alice@example.com", "Subject: Hello", "Hi there"} {
		if !strings.Contains(content, want) {
			t.Errorf("FormatDraft missing %q, got:\n%s", want, content)
		}
	}
}

func TestParseDraft(t *testing.T) {
	raw := "From: me@example.com\r\nTo: alice@example.com\r\nSubject: Hello\r\n\r\nHi there"
	d, err := m9ssmtp.ParseDraft([]byte(raw))
	if err != nil {
		t.Fatalf("ParseDraft: %v", err)
	}
	if d.From != "me@example.com" {
		t.Errorf("want From=me@example.com, got %q", d.From)
	}
	if len(d.To) != 1 || d.To[0] != "alice@example.com" {
		t.Errorf("want To=[alice@example.com], got %v", d.To)
	}
	if d.Subject != "Hello" {
		t.Errorf("want Subject=Hello, got %q", d.Subject)
	}
	if !strings.Contains(d.Body, "Hi there") {
		t.Errorf("want body to contain Hi there, got %q", d.Body)
	}
}

func TestFormatDraft_withCC(t *testing.T) {
	d := m9ssmtp.Draft{
		From:    "me@example.com",
		To:      []string{"alice@example.com"},
		CC:      []string{"carol@example.com"},
		Subject: "CC Test",
		Body:    "body",
	}
	eml, err := m9ssmtp.FormatDraft(d)
	if err != nil {
		t.Fatalf("FormatDraft: %v", err)
	}
	if !strings.Contains(string(eml), "Cc: carol@example.com") {
		t.Errorf("expected Cc header in output, got:\n%s", eml)
	}
}

func TestFormatDraft_withInReplyTo(t *testing.T) {
	d := m9ssmtp.Draft{
		From:      "me@example.com",
		To:        []string{"alice@example.com"},
		Subject:   "Re: test",
		InReplyTo: "<msg123@example.com>",
		Body:      "body",
	}
	eml, err := m9ssmtp.FormatDraft(d)
	if err != nil {
		t.Fatalf("FormatDraft: %v", err)
	}
	if !strings.Contains(string(eml), "In-Reply-To: <msg123@example.com>") {
		t.Errorf("expected In-Reply-To header, got:\n%s", eml)
	}
}

func TestParseDraft_withCC(t *testing.T) {
	raw := "From: me@example.com\r\nTo: a@example.com\r\nCc: b@example.com, c@example.com\r\nSubject: CC\r\n\r\nbody"
	d, err := m9ssmtp.ParseDraft([]byte(raw))
	if err != nil {
		t.Fatalf("ParseDraft: %v", err)
	}
	if len(d.CC) != 2 {
		t.Errorf("want 2 CC addresses, got %d: %v", len(d.CC), d.CC)
	}
}

func TestParseDraft_withInReplyTo(t *testing.T) {
	raw := "From: me@example.com\r\nIn-Reply-To: <orig@example.com>\r\nSubject: Re\r\n\r\nbody"
	d, err := m9ssmtp.ParseDraft([]byte(raw))
	if err != nil {
		t.Fatalf("ParseDraft: %v", err)
	}
	if d.InReplyTo != "<orig@example.com>" {
		t.Errorf("want InReplyTo=<orig@example.com>, got %q", d.InReplyTo)
	}
}

func buildAndParse(t *testing.T, d m9ssmtp.Draft) *netmail.Message {
	t.Helper()
	raw, err := m9ssmtp.BuildMessage(d)
	if err != nil {
		t.Fatalf("BuildMessage: %v", err)
	}
	msg, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("built message is not valid RFC 5322: %v\n%s", err, raw)
	}
	return msg
}

func TestBuildMessage_hasMessageIDOnSenderDomain(t *testing.T) {
	msg := buildAndParse(t, m9ssmtp.Draft{
		From: "M9s <m9s@example.com>", To: []string{"a@example.com"}, Subject: "Hi", Body: "body",
	})
	id := msg.Header.Get("Message-Id")
	if !regexp.MustCompile(`^<[A-Za-z0-9.\-]+@example\.com>$`).MatchString(id) {
		t.Errorf("want Message-ID <...@example.com>, got %q", id)
	}
}

func TestBuildMessage_messageIDUnique(t *testing.T) {
	d := m9ssmtp.Draft{From: "me@example.com", To: []string{"a@example.com"}, Subject: "Hi"}
	a := buildAndParse(t, d).Header.Get("Message-Id")
	b := buildAndParse(t, d).Header.Get("Message-Id")
	if a == b {
		t.Errorf("want unique Message-IDs, both were %q", a)
	}
}

func TestBuildMessage_threadingHeaders(t *testing.T) {
	msg := buildAndParse(t, m9ssmtp.Draft{
		From: "me@example.com", To: []string{"a@example.com"}, Subject: "Re: Hi",
		InReplyTo: "<p@example.com>", References: "<r@example.com> <p@example.com>",
	})
	if got := msg.Header.Get("In-Reply-To"); got != "<p@example.com>" {
		t.Errorf("In-Reply-To = %q", got)
	}
	if got := msg.Header.Get("References"); got != "<r@example.com> <p@example.com>" {
		t.Errorf("References = %q", got)
	}
}

func TestBuildMessage_encodesNonASCIISubject(t *testing.T) {
	msg := buildAndParse(t, m9ssmtp.Draft{
		From: "me@example.com", To: []string{"a@example.com"}, Subject: "Здравей 👋",
	})
	raw := msg.Header.Get("Subject")
	for _, r := range raw {
		if r > 127 {
			t.Fatalf("Subject header contains raw non-ASCII: %q", raw)
		}
	}
	decoded, err := new(mime.WordDecoder).DecodeHeader(raw)
	if err != nil || decoded != "Здравей 👋" {
		t.Errorf("decoded subject = %q (err %v)", decoded, err)
	}
}

func TestDraftRoundTrip_keepsReferences(t *testing.T) {
	in := m9ssmtp.Draft{
		From: "me@example.com", To: []string{"a@example.com"}, Subject: "Re: Hi",
		InReplyTo: "<p@example.com>", References: "<r@example.com> <p@example.com>",
	}
	eml, _ := m9ssmtp.FormatDraft(in)
	out, _ := m9ssmtp.ParseDraft(eml)
	if out.References != in.References {
		t.Errorf("References lost in round trip: got %q", out.References)
	}
}

func TestSentCopy_addsBccOnlyToTheCopy(t *testing.T) {
	msg := []byte("From: a@x\r\nTo: b@x\r\n\r\nhi")
	if got := m9ssmtp.SentCopy(msg, m9ssmtp.Draft{}); !bytes.Equal(got, msg) {
		t.Fatalf("no Bcc: copy must equal the sent message, got %q", got)
	}
	got := m9ssmtp.SentCopy(msg, m9ssmtp.Draft{BCC: []string{"c@x", "d@x"}})
	if !strings.HasPrefix(string(got), "Bcc: c@x, d@x\r\nFrom: a@x") {
		t.Fatalf("copy = %q", got)
	}
	if strings.Contains(string(msg), "Bcc") {
		t.Fatal("sent message must not be changed")
	}
}
