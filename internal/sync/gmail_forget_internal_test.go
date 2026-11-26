package sync

import (
	"testing"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/mail/gmail"
	"github.com/zhecho/m9s/internal/store"
)

// A poll can fetch a message's labels just before the user deletes it for
// good (Empty Trash) and apply them just after. A message deleted for good
// must stay gone: Gmail never reuses a message id.
func TestGmailEngine_deletedMessageIsNotBroughtBack(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := NewGmailEngine(&config.Account{Name: "g"}, make(chan Event, 8), st, nil, nil)
	e.folders = []*mail.Folder{{Name: "Trash"}}
	e.labelOf = map[string]string{"Trash": "TRASH"}
	stale := &gmail.MsgMeta{ID: "m1", LabelIDs: []string{"TRASH"}, InternalDate: 1}
	if err := e.applyMeta(stale); err != nil {
		t.Fatal(err)
	}

	if err := e.forget([]string{"m1"}); err != nil { // Empty Trash
		t.Fatal(err)
	}
	if err := e.applyMeta(stale); err != nil { // the poll that was under way
		t.Fatal(err)
	}
	if _, ok := st.Envelope("g", "Trash", "m1"); ok {
		t.Error("a message deleted for good is back in Trash")
	}
}
