package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/mail/gmail/gmailtest"
)

func rawMsg(subject, msgID string) []byte {
	return []byte(fmt.Sprintf("From: Bob <bob@example.com>\r\nTo: me@example.com\r\nSubject: %s\r\n"+
		"Date: Mon, 02 Jan 2026 15:04:05 +0000\r\nMessage-ID: <%s>\r\n\r\nhello ~~~ ???\r\n", subject, msgID))
}

func TestLabelsMapping(t *testing.T) {
	c, f, _ := newTest(t)
	f.AddLabel("Label_1", "Work/2026")
	f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX", "UNREAD"}, Raw: rawMsg("a", "a@x")})
	f.AddMessage(gmailtest.Message{ThreadID: "tX", LabelIDs: []string{"INBOX"}, Raw: rawMsg("b", "b@x")})
	f.AddMessage(gmailtest.Message{ThreadID: "tX", LabelIDs: []string{"INBOX", "UNREAD"}, Raw: rawMsg("c", "c@x")})

	labels, err := c.Labels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	hidden := map[string]bool{}
	for _, l := range labels {
		name, ok := FolderName(l)
		if !ok {
			hidden[l.ID] = true
			continue
		}
		got[l.ID] = name
	}
	want := map[string]string{"INBOX": "INBOX", "SENT": "Sent", "DRAFT": "Drafts", "TRASH": "Trash",
		"SPAM": "Spam", "STARRED": "Starred", "Label_1": "Work/2026"}
	for id, name := range want {
		if got[id] != name {
			t.Errorf("label %s -> %q, want %q", id, got[id], name)
		}
	}
	for _, id := range []string{"UNREAD", "IMPORTANT", "CHAT", "CATEGORY_PERSONAL"} {
		if !hidden[id] {
			t.Errorf("%s should be hidden", id)
		}
	}
	if len(got) != len(want) {
		t.Errorf("folders = %v", got)
	}

	inbox, err := c.Label(context.Background(), "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if inbox.MessagesTotal != 3 || inbox.MessagesUnread != 2 || inbox.ThreadsTotal != 2 || inbox.ThreadsUnread != 2 {
		t.Fatalf("counts = %+v", inbox)
	}
}

func TestListMessageIDsPages(t *testing.T) {
	c, f, _ := newTest(t)
	f.SetPageSizes(2, 0)
	var want []string
	for i := 0; i < 5; i++ {
		want = append(want, f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX"}, Raw: rawMsg("s", "x@y")}))
	}
	f.AddMessage(gmailtest.Message{LabelIDs: []string{"SENT"}, Raw: rawMsg("s", "x@y")})
	var got []string
	token := ""
	for {
		ids, next, err := c.ListMessageIDs(context.Background(), "INBOX", "", token)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ids...)
		if next == "" {
			break
		}
		token = next
	}
	slices.Reverse(want) // newest first
	if !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
}

func TestGetMetadata(t *testing.T) {
	c, f, _ := newTest(t)
	id := f.AddMessage(gmailtest.Message{ThreadID: "t1", LabelIDs: []string{"INBOX", "UNREAD"}, InternalDate: 1700000123456, Raw: rawMsg("Hi", "abc@x")})
	m, err := c.GetMetadata(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != id || m.ThreadID != "t1" || m.InternalDate != 1700000123456 || !slices.Equal(m.LabelIDs, []string{"INBOX", "UNREAD"}) {
		t.Fatalf("meta = %+v", m)
	}
	if m.Headers["Subject"] != "Hi" || m.Headers["Message-ID"] != "<abc@x>" || m.Headers["From"] != "Bob <bob@example.com>" {
		t.Fatalf("headers = %v", m.Headers)
	}
	if m.Raw != nil {
		t.Fatal("metadata must not carry raw")
	}
	if _, err := c.GetMetadata(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestGetRawDecodesBase64URL(t *testing.T) {
	for _, padded := range []bool{false, true} {
		c, f, _ := newTest(t)
		f.PadRaw = padded
		raw := rawMsg("ünïcode ~~~", "r@x") // ~ and ? produce base64url-only characters
		raw = append(raw, 0xff, 0xfe)       // and a length that needs padding
		id := f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX"}, Raw: raw})
		m, err := c.GetRaw(context.Background(), id)
		if err != nil {
			t.Fatalf("padded=%v: %v", padded, err)
		}
		if string(m.Raw) != string(raw) || m.ID != id {
			t.Fatalf("padded=%v: raw = %q", padded, m.Raw)
		}
	}
}

func TestModifyTrashDelete(t *testing.T) {
	c, f, _ := newTest(t)
	ctx := context.Background()
	id := f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX", "UNREAD"}, Raw: rawMsg("s", "x@y")})
	if err := c.Modify(ctx, id, []string{"STARRED"}, []string{"UNREAD"}); err != nil {
		t.Fatal(err)
	}
	m, _ := f.Message(id)
	if !slices.Contains(m.LabelIDs, "STARRED") || slices.Contains(m.LabelIDs, "UNREAD") {
		t.Fatalf("labels = %v", m.LabelIDs)
	}
	if err := c.Trash(ctx, id); err != nil {
		t.Fatal(err)
	}
	if m, _ := f.Message(id); !slices.Contains(m.LabelIDs, "TRASH") {
		t.Fatalf("labels = %v", m.LabelIDs)
	}
	if err := c.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Message(id); ok {
		t.Fatal("still there")
	}
	if err := c.Delete(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestBatchDeleteChunks(t *testing.T) {
	c, f, _ := newTest(t)
	var ids []string
	for i := 0; i < 3; i++ {
		ids = append(ids, f.AddMessage(gmailtest.Message{LabelIDs: []string{"TRASH"}, Raw: rawMsg("s", "x@y")}))
	}
	if err := c.BatchDelete(context.Background(), nil); err != nil || f.Count("/gmail/v1/users/me/messages/batchDelete") != 0 {
		t.Fatalf("empty batch: err=%v", err)
	}
	if err := c.BatchDelete(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
	if len(f.MessageIDs("")) != 0 {
		t.Fatal("not deleted")
	}
	many := make([]string, 2500)
	for i := range many {
		many[i] = fmt.Sprint("z", i)
	}
	if err := c.BatchDelete(context.Background(), many); err != nil {
		t.Fatal(err)
	}
	if n := f.Count("/gmail/v1/users/me/messages/batchDelete"); n != 4 {
		t.Fatalf("calls = %d, want 1 + 3", n)
	}
}

func TestSendPassesThreadID(t *testing.T) {
	c, f, _ := newTest(t)
	raw := rawMsg("Re: hi", "reply@x")
	m, err := c.Send(context.Background(), raw, "thread-42")
	if err != nil {
		t.Fatal(err)
	}
	if m.ThreadID != "thread-42" || m.ID == "" {
		t.Fatalf("sent = %+v", m)
	}
	stored, ok := f.Message(m.ID)
	if !ok || string(stored.Raw) != string(raw) || !slices.Contains(stored.LabelIDs, "SENT") {
		t.Fatalf("stored = %+v", stored)
	}
	// A new conversation sends no threadId at all.
	if _, err := c.Send(context.Background(), raw, ""); err != nil {
		t.Fatal(err)
	}
	reqs := f.Requests()
	last := reqs[len(reqs)-1]
	if strings.Contains(last.Body, "threadId") {
		t.Fatalf("body = %s", last.Body)
	}
}

func TestSendRetriesOnlyOn429WithSameBody(t *testing.T) {
	c, f, sleeps := newTest(t)
	const path = "/gmail/v1/users/me/messages/send"
	f.Queue(path, 429, `{}`)
	if _, err := c.Send(context.Background(), rawMsg("s", "x@y"), ""); err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, r := range f.Requests() {
		if r.Path == path {
			bodies = append(bodies, r.Body)
		}
	}
	if len(bodies) != 2 || bodies[0] == "" || bodies[0] != bodies[1] || len(*sleeps) != 1 {
		t.Fatalf("bodies = %q sleeps = %v", bodies, *sleeps)
	}
	// A 503 may have been delivered: never sent twice.
	f.Queue(path, 503, `{}`)
	if _, err := c.Send(context.Background(), rawMsg("s", "x@y"), ""); err == nil {
		t.Fatal("want error")
	}
	if n := f.Count(path); n != 3 {
		t.Fatalf("calls = %d", n)
	}
}

func TestModifyRetriesWithFreshBody(t *testing.T) {
	c, f, _ := newTest(t)
	id := f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX"}, Raw: rawMsg("s", "x@y")})
	path := "/gmail/v1/users/me/messages/" + id + "/modify"
	f.Queue(path, 503, `{}`)
	if err := c.Modify(context.Background(), id, []string{"STARRED"}, nil); err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, r := range f.Requests() {
		if r.Path == path {
			bodies = append(bodies, r.Body)
		}
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] || !strings.Contains(bodies[1], "STARRED") {
		t.Fatalf("bodies = %q", bodies)
	}
}

func TestDrafts(t *testing.T) {
	c, f, _ := newTest(t)
	ctx := context.Background()
	id, err := c.CreateDraft(ctx, rawMsg("draft 1", "d1@x"), "t9")
	if err != nil || id == "" {
		t.Fatalf("create: %q %v", id, err)
	}
	mid := f.Drafts()[id]
	if m, _ := f.Message(mid); m.ThreadID != "t9" || !slices.Contains(m.LabelIDs, "DRAFT") {
		t.Fatalf("draft message = %+v", m)
	}
	if err := c.UpdateDraft(ctx, id, rawMsg("draft 2", "d2@x")); err != nil {
		t.Fatal(err)
	}
	if m, _ := f.Message(f.Drafts()[id]); !strings.Contains(string(m.Raw), "draft 2") {
		t.Fatalf("not updated: %q", m.Raw)
	}
	if err := c.DeleteDraft(ctx, id); err != nil {
		t.Fatal(err)
	}
	if len(f.Drafts()) != 0 {
		t.Fatal("draft left")
	}
	if err := c.UpdateDraft(ctx, id, rawMsg("x", "x@x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update gone draft: %v", err)
	}
	// The raw travels as base64url inside message.raw.
	var body struct{ Message struct{ Raw string } }
	for _, r := range f.Requests() {
		if r.Path == "/gmail/v1/users/me/drafts" {
			_ = json.Unmarshal([]byte(r.Body), &body)
		}
	}
	if body.Message.Raw == "" || strings.ContainsAny(body.Message.Raw, "+/=") {
		t.Fatalf("raw = %q", body.Message.Raw)
	}
}

func TestHistoryChanges(t *testing.T) {
	c, f, _ := newTest(t)
	f.SetPageSizes(0, 2) // several history pages
	ctx := context.Background()
	keep := f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX"}, Raw: rawMsg("s", "x@y")})
	gone := f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX"}, Raw: rawMsg("s", "x@y")})
	_, start, _ := c.Profile(ctx)

	added := f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX"}, Raw: rawMsg("s", "x@y")})
	f.ModifyLabels(keep, []string{"STARRED"}, nil)
	f.ModifyLabels(added, nil, []string{"INBOX"}) // added and changed: reported once, as added
	f.RemoveMessage(gone)
	transient := f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX"}, Raw: rawMsg("s", "x@y")})
	f.RemoveMessage(transient) // added then deleted: only deleted

	ch, newID, err := c.History(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	_, now, _ := c.Profile(ctx)
	if newID != now {
		t.Fatalf("new id %q, want %q", newID, now)
	}
	if !slices.Equal(ch.Added, []string{added}) {
		t.Errorf("added = %v", ch.Added)
	}
	if d := slices.Sorted(slices.Values(ch.Deleted)); !slices.Equal(d, []string{gone, transient}) {
		t.Errorf("deleted = %v", ch.Deleted)
	}
	if !slices.Equal(ch.LabelsChanged, []string{keep}) {
		t.Errorf("labels changed = %v", ch.LabelsChanged)
	}
	// The labels a changed message carries now come with the change: the
	// poll need not fetch it again.
	if got := slices.Sorted(slices.Values(ch.Labels[keep])); !slices.Equal(got, []string{"INBOX", "STARRED"}) {
		t.Errorf("labels of %s = %v, want INBOX and STARRED", keep, ch.Labels[keep])
	}
	for _, r := range f.Requests() {
		if r.Path == "/gmail/v1/users/me/history" {
			for _, typ := range []string{"messageAdded", "messageDeleted", "labelAdded", "labelRemoved"} {
				if !strings.Contains(r.Query, "historyTypes="+typ) {
					t.Fatalf("query %q lacks %s", r.Query, typ)
				}
			}
		}
	}
	// Nothing new: same id back, no changes.
	ch, again, err := c.History(ctx, newID)
	if err != nil || again != newID || len(ch.Added)+len(ch.Deleted)+len(ch.LabelsChanged) != 0 {
		t.Fatalf("idle history: %+v %q %v", ch, again, err)
	}
}

func TestHistoryNotFound(t *testing.T) {
	c, f, _ := newTest(t)
	ctx := context.Background()
	_, start, _ := c.Profile(ctx)
	f.ExpireHistory()
	_, _, err := c.History(ctx, start)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if n := f.Count("/gmail/v1/users/me/history"); n != 1 {
		t.Fatalf("404 must not be retried: %d calls", n)
	}
}

func TestGetMetadata_returnsContentType(t *testing.T) {
	c, f, _ := newTest(t)
	raw := []byte("From: Bob <bob@example.com>\r\nSubject: bill\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=MIX\r\n\r\n--MIX--\r\n")
	id := f.AddMessage(gmailtest.Message{ThreadID: "t1", LabelIDs: []string{"INBOX"}, Raw: raw})
	m, err := c.GetMetadata(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(m.Headers["Content-Type"], "multipart/mixed") {
		t.Errorf("Content-Type = %q, want it asked for and returned", m.Headers["Content-Type"])
	}
}
