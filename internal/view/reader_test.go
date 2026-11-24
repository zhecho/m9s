package view

import (
	"strings"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/ui"
)

func TestReaderView_showMessage(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	rv := NewReaderView(skin, nil)
	msg := &mail.Message{
		Envelope: &mail.Envelope{
			Subject: "Reader Subject",
			From:    []mail.Address{{Name: "Alice", Email: "alice@example.com"}},
			To:      []mail.Address{{Email: "bob@example.com"}},
			Date:    time.Date(2026, 3, 28, 9, 0, 0, 0, time.UTC),
		},
		TextBody: "Reader body text",
	}
	rv.ShowMessage(msg)
	text := rv.GetText(false)
	if !strings.Contains(text, "Reader Subject") {
		t.Errorf("expected subject in reader, got %q", text)
	}
	if !strings.Contains(text, "Reader body text") {
		t.Errorf("expected body in reader, got %q", text)
	}
}

func TestReaderView_showMessage_withAttachments(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	rv := NewReaderView(skin, nil)
	msg := &mail.Message{
		Envelope: &mail.Envelope{
			Subject: "With Attachment",
			From:    []mail.Address{{Email: "a@example.com"}},
			Date:    time.Now(),
		},
		TextBody:    "body",
		Attachments: []*mail.Attachment{{Filename: "file.pdf"}},
	}
	rv.ShowMessage(msg)
	text := rv.GetText(false)
	if !strings.Contains(text, "1 file") {
		t.Errorf("expected attachment count in reader, got %q", text)
	}
}

func TestReaderView_showMessage_embeddedPicturesAreNotCountedAsFiles(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	rv := NewReaderView(skin, nil)
	rv.ShowMessage(&mail.Message{
		Envelope:    &mail.Envelope{Subject: "Newsletter", From: []mail.Address{{Email: "a@example.com"}}, Date: time.Now()},
		TextBody:    "body",
		Attachments: []*mail.Attachment{{Filename: "logo.png", Inline: true}},
	})
	if text := rv.GetText(false); strings.Contains(text, "file(s)") {
		t.Errorf("a picture inside the mail was announced as an attached file: %q", text)
	}
}

func TestNewReaderView_onClose(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	closed := false
	rv := NewReaderView(skin, func() { closed = true })
	if rv == nil {
		t.Fatal("NewReaderView returned nil")
	}
	_ = closed // onClose is called via key event, just verify construction
}
