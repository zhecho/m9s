package search_test

import (
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/search"
)

func TestIndexAndSearch(t *testing.T) {
	idx, err := search.Open(t.TempDir() + "/idx")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer idx.Close()

	env := &mail.Envelope{
		UID:     1,
		Subject: "Deploy is done",
		From:    []mail.Address{{Name: "Bob", Email: "bob@co.io"}},
		Date:    time.Now(),
	}
	msg := &mail.Message{Envelope: env, TextBody: "Staging is green."}

	if err := idx.Index("INBOX", msg); err != nil {
		t.Fatalf("Index: %v", err)
	}

	uids, err := idx.Search("INBOX", "deploy")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(uids) != 1 || uids[0] != 1 {
		t.Errorf("want [1], got %v", uids)
	}

	uids2, err := idx.Search("INBOX", "xyzzy")
	if err != nil {
		t.Fatalf("Search no-match: %v", err)
	}
	if len(uids2) != 0 {
		t.Errorf("want empty, got %v", uids2)
	}
}
