package gmail

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/zhecho/m9s/internal/mail/gmail/gmailtest"
)

func TestGetThreadReturnsRawMessagesOldestFirst(t *testing.T) {
	c, f, _ := newTest(t)
	newer := f.AddMessage(gmailtest.Message{ThreadID: "T", LabelIDs: []string{"SENT"}, InternalDate: 2000, Raw: rawMsg("re", "re@x")})
	older := f.AddMessage(gmailtest.Message{ThreadID: "T", LabelIDs: []string{"INBOX"}, InternalDate: 1000, Raw: rawMsg("hi", "hi@x")})
	f.AddMessage(gmailtest.Message{ThreadID: "other", LabelIDs: []string{"INBOX"}, Raw: rawMsg("x", "x@x")})

	msgs, err := c.GetThread(context.Background(), "T")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].ID != older || msgs[1].ID != newer {
		t.Fatalf("thread = %+v", msgs)
	}
	if msgs[0].ThreadID != "T" || !slices.Equal(msgs[1].LabelIDs, []string{"SENT"}) || len(msgs[0].Raw) == 0 {
		t.Fatalf("message = %+v", msgs[0])
	}
	if _, err := c.GetThread(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown thread: %v", err)
	}
}

func TestModifyAndTrashThreadActOnEveryMessage(t *testing.T) {
	c, f, _ := newTest(t)
	a := f.AddMessage(gmailtest.Message{ThreadID: "T", LabelIDs: []string{"INBOX", "UNREAD"}, Raw: rawMsg("a", "a@x")})
	b := f.AddMessage(gmailtest.Message{ThreadID: "T", LabelIDs: []string{"INBOX"}, Raw: rawMsg("b", "b@x")})

	if err := c.ModifyThread(context.Background(), "T", []string{"STARRED"}, []string{"UNREAD"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a, b} {
		m, _ := f.Message(id)
		if !slices.Contains(m.LabelIDs, "STARRED") || slices.Contains(m.LabelIDs, "UNREAD") {
			t.Fatalf("%s labels = %v", id, m.LabelIDs)
		}
	}
	if err := c.TrashThread(context.Background(), "T"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a, b} {
		if m, _ := f.Message(id); !slices.Contains(m.LabelIDs, "TRASH") {
			t.Fatalf("%s labels after trash = %v", id, m.LabelIDs)
		}
	}
	if err := c.TrashThread(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown thread: %v", err)
	}
}

func TestListThreadsPassesQueryAndPages(t *testing.T) {
	c, f, _ := newTest(t)
	f.AddMessage(gmailtest.Message{ThreadID: "A", LabelIDs: []string{"INBOX"}, InternalDate: 1000, Raw: rawMsg("a", "a@x")})
	f.AddMessage(gmailtest.Message{ThreadID: "B", LabelIDs: []string{"INBOX"}, InternalDate: 2000, Raw: rawMsg("b", "b@x")})
	f.AddMessage(gmailtest.Message{ThreadID: "B", LabelIDs: []string{"INBOX", "UNREAD"}, InternalDate: 3000, Raw: rawMsg("b2", "b2@x")})

	ids, next, err := c.ListThreads(context.Background(), "", "")
	if err != nil || next != "" || !slices.Equal(ids, []string{"B", "A"}) {
		t.Fatalf("ids = %v next %q err %v, want [B A] newest first", ids, next, err)
	}
	ids, _, err = c.ListThreads(context.Background(), "is:unread", "")
	if err != nil || !slices.Equal(ids, []string{"B"}) {
		t.Fatalf("is:unread ids = %v err %v", ids, err)
	}
}
