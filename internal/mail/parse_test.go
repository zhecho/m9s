package mail_test

import (
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/mail"
)

// A newsletter-style mail: HTML that shows a picture carried inside the mail
// (referenced as cid:logo@m9s), plus an ordinary PDF attachment.
const mailWithEmbeddedPicture = "From: a@example.com\r\n" +
	"Subject: pic\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=MIX\r\n\r\n" +
	"--MIX\r\n" +
	"Content-Type: multipart/related; boundary=REL\r\n\r\n" +
	"--REL\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n\r\n" +
	"<p>Hi <img src=\"cid:logo@m9s\"></p>\r\n" +
	"--REL\r\n" +
	"Content-Type: image/png\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-ID: <logo@m9s>\r\n" +
	"Content-Disposition: inline; filename=logo.png\r\n\r\n" +
	"iVBORw0KGgo=\r\n" +
	"--REL--\r\n" +
	"--MIX\r\n" +
	"Content-Type: application/pdf\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-Disposition: attachment; filename=bill.pdf\r\n\r\n" +
	"JVBERg==\r\n" +
	"--MIX--\r\n"

func TestParseMessage_keepsPicturesEmbeddedInTheHTML(t *testing.T) {
	msg := mail.ParseMessage(&mail.Envelope{UID: 1}, []byte(mailWithEmbeddedPicture))

	if !strings.Contains(msg.HTMLBody, "cid:logo@m9s") {
		t.Fatalf("html body = %q", msg.HTMLBody)
	}
	var pic, pdf *mail.Attachment
	for _, a := range msg.Attachments {
		switch a.ContentType {
		case "image/png":
			pic = a
		case "application/pdf":
			pdf = a
		}
	}
	if pic == nil {
		t.Fatalf("the embedded picture was dropped; attachments: %+v", msg.Attachments)
	}
	if pic.ContentID != "logo@m9s" || !pic.Inline || pic.Filename != "logo.png" {
		t.Errorf("picture = id %q inline %v name %q, want logo@m9s, inline, logo.png", pic.ContentID, pic.Inline, pic.Filename)
	}
	if string(pic.Data) != "\x89PNG\r\n\x1a\n" {
		t.Errorf("picture bytes = %q, want the decoded PNG signature", pic.Data)
	}
	if pdf == nil || pdf.Inline || pdf.Filename != "bill.pdf" {
		t.Errorf("pdf attachment = %+v, want a plain attachment named bill.pdf", pdf)
	}
}

func TestParseMessage_textPartsAreNotAttachments(t *testing.T) {
	raw := "From: a@example.com\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=ALT\r\n\r\n" +
		"--ALT\r\nContent-Type: text/plain\r\n\r\nhello\r\n" +
		"--ALT\r\nContent-Type: text/html\r\n\r\n<b>hello</b>\r\n--ALT--\r\n"
	msg := mail.ParseMessage(&mail.Envelope{UID: 1}, []byte(raw))
	if len(msg.Attachments) != 0 {
		t.Errorf("attachments = %+v, want none", msg.Attachments)
	}
	if msg.TextBody != "hello" || msg.HTMLBody != "<b>hello</b>" {
		t.Errorf("text %q html %q", msg.TextBody, msg.HTMLBody)
	}
}

func TestParseMessageMovedKeepsBehaviour(t *testing.T) {
	msg := mail.ParseMessage(&mail.Envelope{ID: "7"}, []byte(mailWithEmbeddedPicture))
	if msg.Envelope.ID != "7" || !strings.Contains(msg.HTMLBody, "cid:logo@m9s") || len(msg.Files()) != 1 {
		t.Errorf("parsed = %+v", msg)
	}
	if empty := mail.ParseMessage(&mail.Envelope{}, nil); empty.TextBody != "" || empty.HTMLBody != "" {
		t.Error("an empty raw message must give an empty body")
	}
}

const plainMail = "From: a@example.com\r\nSubject: hi\r\n\r\njust text\r\n"

// inlineOnlyMail carries a picture its HTML shows, and no file to save.
const inlineOnlyMail = "From: a@example.com\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/related; boundary=REL\r\n\r\n" +
	"--REL\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n\r\n" +
	"<p><img src=\"cid:logo@m9s\"></p>\r\n" +
	"--REL\r\n" +
	"Content-Type: image/png\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-ID: <logo@m9s>\r\n" +
	"Content-Disposition: inline; filename=logo.png\r\n\r\n" +
	"iVBORw0KGgo=\r\n" +
	"--REL--\r\n"

func TestHasFiles_matchesFiles(t *testing.T) {
	for _, c := range []struct {
		name string
		raw  string
		want bool
	}{
		{"plain", plainMail, false},
		{"pdf attachment", mailWithEmbeddedPicture, true},
		{"inline picture only", inlineOnlyMail, false},
		{"pdf without disposition", pdfNoDispositionMail, true},
		{"picture by Content-ID without disposition", cidNoDispositionMail, false},
	} {
		files := len(mail.ParseMessage(&mail.Envelope{}, []byte(c.raw)).Files()) > 0
		if got := mail.HasFiles([]byte(c.raw)); got != files || got != c.want {
			t.Errorf("%s: HasFiles = %v, Files() says %v, want %v", c.name, got, files, c.want)
		}
	}
}

// pdfNoDispositionMail names its PDF only in Content-Type, as some mailers do.
const pdfNoDispositionMail = "From: a@example.com\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=MIX\r\n\r\n" +
	"--MIX\r\n" +
	"Content-Type: text/plain\r\n\r\n" +
	"see attached\r\n" +
	"--MIX\r\n" +
	"Content-Type: application/pdf; name=bill.pdf\r\n" +
	"Content-Transfer-Encoding: base64\r\n\r\n" +
	"JVBERg==\r\n" +
	"--MIX--\r\n"

// cidNoDispositionMail shows a picture by Content-ID and gives it no
// disposition (typical of .NET mailers): it is not a file to save.
const cidNoDispositionMail = "From: a@example.com\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/related; boundary=REL\r\n\r\n" +
	"--REL\r\n" +
	"Content-Type: text/html\r\n\r\n" +
	"<img src=\"cid:logo@m9s\">\r\n" +
	"--REL\r\n" +
	"Content-Type: image/png; name=logo.png\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-ID: <logo@m9s>\r\n\r\n" +
	"iVBORw0KGgo=\r\n" +
	"--REL--\r\n"

func TestIsFile(t *testing.T) {
	for _, c := range []struct {
		disp, mediaType string
		cid             bool
		want            bool
	}{
		{"attachment", "application/pdf", false, true},
		{"attachment", "image/png", true, true}, // said so explicitly
		{"", "application/pdf", false, true},
		{"", "image/png", true, false}, // shown by the HTML
		{"inline", "image/png", false, false},
		{"", "text/plain", false, false},
		{"attachment", "text/plain", false, true},
	} {
		if got := mail.IsFile(c.disp, c.mediaType, c.cid); got != c.want {
			t.Errorf("IsFile(%q, %q, cid %v) = %v, want %v", c.disp, c.mediaType, c.cid, got, c.want)
		}
	}
}
