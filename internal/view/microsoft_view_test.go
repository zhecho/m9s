package view

import (
	"strings"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/ui"
)

func categorized(cats ...string) *mail.Message {
	return &mail.Message{Envelope: &mail.Envelope{Subject: "s", Date: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC),
		From: []mail.Address{{Email: "bob@example.com"}}, Categories: cats}, TextBody: "body"}
}

func TestReader_showsCategories(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	rv := NewReaderView(skin, func() {})
	rv.ShowMessage(categorized("Work", "Travel"))
	if text := rv.GetText(true); !strings.Contains(text, "Categories: Work, Travel") {
		t.Errorf("reader = %q", text)
	}
	rv.ShowMessage(categorized())
	if text := rv.GetText(true); strings.Contains(text, "Categories:") {
		t.Errorf("reader shows an empty Categories line: %q", text)
	}
}

func TestPreview_showsCategories(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	pv := NewPreviewView(skin)
	pv.ShowMessage(categorized("Work"))
	if text := pv.GetText(true); !strings.Contains(text, "Categories: Work") {
		t.Errorf("preview = %q", text)
	}
	pv.ShowMessage(categorized())
	if text := pv.GetText(true); strings.Contains(text, "Categories:") {
		t.Errorf("preview shows an empty Categories line: %q", text)
	}
}

func TestSelfAddressOf_microsoft(t *testing.T) {
	acct := &mail.Account{Name: "ms", Config: &config.Account{Name: "ms", Provider: config.ProviderMicrosoft,
		Microsoft: config.MicrosoftConfig{User: "m9s-test@outlook.com"}}}
	if got := selfAddressOf(acct); got != "m9s-test@outlook.com" {
		t.Errorf("selfAddressOf = %q", got)
	}
}

func TestDraftCopyWords_microsoft(t *testing.T) {
	acct := &mail.Account{Name: "ms", Config: &config.Account{Name: "ms", Provider: config.ProviderMicrosoft}}
	confirm, done := draftCopyWords(acct)
	if confirm != "Its copy in Outlook's Drafts is deleted for good." || done != "Server draft deleted" {
		t.Errorf("wording = %q / %q", confirm, done)
	}
}
