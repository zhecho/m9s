package view

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/mail"
	msync "github.com/zhecho/m9s/internal/sync"
)

func TestDraftCopyWordsPerProvider(t *testing.T) {
	gm := &mail.Account{Name: "gm", Config: &config.Account{Name: "gm", Provider: config.ProviderGmail}}
	confirm, done := draftCopyWords(gm)
	for _, s := range []string{confirm, done} {
		if strings.Contains(s, "Deleted Items") {
			t.Errorf("gmail wording claims a move to Deleted Items: %q", s)
		}
	}
	if done != "Server draft deleted" {
		t.Errorf("gmail done = %q", done)
	}
	if !strings.Contains(confirm, "deleted") {
		t.Errorf("gmail confirm = %q, want it to say the copy is deleted", confirm)
	}

	imap := &mail.Account{Name: "a", Config: &config.Account{Name: "a"}}
	confirm, done = draftCopyWords(imap)
	if confirm != "Its copy in the server's Drafts folder is moved to Deleted Items." || done != "Draft moved to Deleted Items" {
		t.Errorf("imap wording changed: %q / %q", confirm, done)
	}
}

func TestPermanentDeleteFailureWords(t *testing.T) {
	if got := permanentDeleteFailure(fmt.Errorf("wrapped: %w", msync.ErrNotInTrash)); got != "Not deleted: the message is no longer in Trash" {
		t.Errorf("not in trash = %q", got)
	}
	if got := permanentDeleteFailure(errors.New("boom")); got != "Delete failed: boom" {
		t.Errorf("other = %q", got)
	}
}
