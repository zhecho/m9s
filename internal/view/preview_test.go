package view

import (
	"strings"
	"testing"
	"time"

	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/ui"
)

func TestPreviewView_showMessage(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	pv := NewPreviewView(skin)
	msg := &mail.Message{
		Envelope: &mail.Envelope{
			Subject: "Test Subject",
			From:    []mail.Address{{Name: "Alice", Email: "alice@example.com"}},
			Date:    time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC),
		},
		TextBody: "Hello body",
	}
	pv.ShowMessage(msg)
	text := pv.GetText(false)
	if !strings.Contains(text, "Test Subject") {
		t.Errorf("expected subject in preview, got %q", text)
	}
	if !strings.Contains(text, "Hello body") {
		t.Errorf("expected body in preview, got %q", text)
	}
}

func TestPreviewView_showMessageNil(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	pv := NewPreviewView(skin)
	pv.ShowMessage(nil)
	if pv.GetText(false) != "" {
		t.Error("nil message should clear preview text")
	}
}

func TestPreviewView_showEnvelope(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	pv := NewPreviewView(skin)
	env := &mail.Envelope{
		Subject: "Loading Subject",
		From:    []mail.Address{{Email: "bob@example.com"}},
	}
	pv.ShowEnvelope(env)
	text := pv.GetText(false)
	if !strings.Contains(text, "Loading Subject") {
		t.Errorf("expected subject in envelope preview, got %q", text)
	}
}

func TestPreviewView_focusBlur(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	pv := NewPreviewView(skin)

	focused := 0
	blurred := 0
	pv.SetFocusFunc(func() { focused++ })
	pv.SetBlurFunc(func() { blurred++ })

	pv.Focus(func(p tview.Primitive) {})
	if focused != 1 {
		t.Errorf("want focusFunc called once, got %d", focused)
	}

	pv.Blur()
	if blurred != 1 {
		t.Errorf("want blurFunc called once, got %d", blurred)
	}
}

func TestPreviewView_focusBlur_noCallbacks(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	pv := NewPreviewView(skin)
	// No panic without registered callbacks.
	pv.Focus(func(p tview.Primitive) {})
	pv.Blur()
}
