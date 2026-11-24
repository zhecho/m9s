package ui_test

import (
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/ui"
)

func newTestStatusBar() *ui.StatusBar {
	skin, _ := ui.LoadSkin("")
	return ui.NewStatusBar(skin, nil)
}

func TestStatusBar_crumbs(t *testing.T) {
	sb := newTestStatusBar()
	sb.SetCrumbs("gmail", "INBOX")
	if got := sb.CrumbsText(); got != "<gmail> <INBOX>" {
		t.Errorf("CrumbsText() = %q, want %q", got, "<gmail> <INBOX>")
	}
}

func TestStatusBar_crumbsSkipEmptyParts(t *testing.T) {
	sb := newTestStatusBar()
	sb.SetCrumbs("gmail", "", "/bob")
	if got := sb.CrumbsText(); got != "<gmail> </bob>" {
		t.Errorf("CrumbsText() = %q, want %q", got, "<gmail> </bob>")
	}
}

func TestStatusBar_crumbsWithBrackets(t *testing.T) {
	sb := newTestStatusBar()
	sb.SetCrumbs("acct", "[Gmail]/All Mail")
	if got := sb.CrumbsText(); got != "<acct> <[Gmail]/All Mail>" {
		t.Errorf("CrumbsText() = %q; folder names with brackets must survive", got)
	}
}

func TestStatusBar_flashDoesNotTouchCrumbs(t *testing.T) {
	sb := newTestStatusBar()
	sb.SetCrumbs("gmail", "INBOX")
	sb.Info("x")
	if got := sb.CrumbsText(); got != "<gmail> <INBOX>" {
		t.Errorf("crumbs changed by a flash: %q", got)
	}
	if !strings.Contains(sb.FlashText(), "x") {
		t.Errorf("flash should contain the message, got %q", sb.FlashText())
	}
}

func TestStatusBar_info(t *testing.T) {
	sb := newTestStatusBar()
	sb.Info("hello")
	if !strings.Contains(sb.FlashText(), "hello") {
		t.Errorf("flash row should contain 'hello'")
	}
}

func TestStatusBar_error(t *testing.T) {
	sb := newTestStatusBar()
	sb.Error("boom")
	if !strings.Contains(sb.FlashText(), "boom") {
		t.Errorf("flash row should contain 'boom'")
	}
}

func TestStatusBar_flashOverwrite(t *testing.T) {
	// Call Info twice to exercise the flashTimer.Stop() path on second call.
	sb := newTestStatusBar()
	sb.Info("first")
	sb.Info("second") // triggers flashTimer != nil branch
	if !strings.Contains(sb.FlashText(), "second") {
		t.Errorf("flash row should show latest message, got %q", sb.FlashText())
	}
}
