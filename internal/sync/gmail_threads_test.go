package sync_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/mail/gmail/gmailtest"
	"github.com/zhecho/m9s/internal/store"
	msync "github.com/zhecho/m9s/internal/sync"
)

func TestGmailEngineImplementsThreads(t *testing.T) {
	var _ msync.Threads = (*msync.GmailEngine)(nil)
}

func fromRaw(from, subject, msgID string) []byte {
	return []byte(fmt.Sprintf("From: %s\r\nTo: x@example.com\r\nSubject: %s\r\n"+
		"Date: Mon, 02 Jan 2026 15:04:05 +0000\r\nMessage-ID: <%s>\r\n\r\nbody of %s\r\n", from, subject, msgID, subject))
}

// conversation seeds thread T: Bob writes (read), I reply (Sent), Ann
// answers (unread); plus a lone message in its own thread.
func conversation(ids *[4]string) func(f *gmailtest.Fake) {
	return func(f *gmailtest.Fake) {
		ids[0] = f.AddMessage(gmailtest.Message{ThreadID: "T", LabelIDs: []string{"INBOX"}, InternalDate: 1_000,
			Raw: fromRaw("Bob Stone <bob@example.com>", "Lunch", "1@x")})
		ids[1] = f.AddMessage(gmailtest.Message{ThreadID: "T", LabelIDs: []string{"SENT"}, InternalDate: 2_000,
			Raw: fromRaw("Me <me@example.com>", "Re: Lunch", "2@x")})
		ids[2] = f.AddMessage(gmailtest.Message{ThreadID: "T", LabelIDs: []string{"INBOX", "UNREAD"}, InternalDate: 3_000,
			Raw: fromRaw("Ann Lee <ann@example.com>", "Re: Lunch", "3@x")})
		ids[3] = f.AddMessage(gmailtest.Message{ThreadID: "L", LabelIDs: []string{"INBOX"}, InternalDate: 500,
			Raw: fromRaw("Cy <cy@example.com>", "Alone", "4@x")})
	}
}

func TestGmailEngine_FetchHeadersListsConversations(t *testing.T) {
	var ids [4]string
	h := newGmail(t, conversation(&ids))
	h.synced()
	envs, err := h.eng.FetchHeaders(context.Background(), "INBOX", 1, store.ListSize)
	if err != nil || len(envs) != 2 {
		t.Fatalf("FetchHeaders = %d rows, %v; want 2 conversations", len(envs), err)
	}
	var conv = envs[0]
	if conv.ThreadID != "T" {
		conv = envs[1]
	}
	if conv.ID != ids[2] || !conv.IsUnread() {
		t.Fatalf("conversation row = %+v, want the newest message, unread", conv)
	}
	// The count and participants cover the whole conversation, my reply in Sent included.
	if conv.ThreadCount != 3 {
		t.Errorf("ThreadCount = %d, want 3", conv.ThreadCount)
	}
	var who []string
	for _, a := range conv.Participants {
		who = append(who, a.Email)
	}
	if !slices.Equal(who, []string{"bob@example.com", "me@example.com", "ann@example.com"}) {
		t.Errorf("participants = %v", who)
	}
	// Trash stays one row per message.
	if err := h.eng.MoveToTrash(context.Background(), "INBOX", ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.MoveToTrash(context.Background(), "INBOX", ids[2]); err != nil {
		t.Fatal(err)
	}
	trash, _ := h.eng.FetchHeaders(context.Background(), "Trash", 1, store.ListSize)
	if len(trash) != 2 {
		t.Fatalf("Trash rows = %d, want one per message", len(trash))
	}
}

func TestGmailEngine_ThreadReadsWholeConversation(t *testing.T) {
	var ids [4]string
	h := newGmail(t, conversation(&ids))
	h.synced()
	msgs, err := h.eng.Thread(context.Background(), "T")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("thread has %d messages", len(msgs))
	}
	for i, want := range ids[:3] {
		if msgs[i].Envelope.ID != want {
			t.Fatalf("message %d = %s, want %s (oldest first)", i, msgs[i].Envelope.ID, want)
		}
	}
	if !strings.Contains(msgs[1].PlainText(), "body of Re: Lunch") || msgs[1].Envelope.From[0].Email != "me@example.com" {
		t.Fatalf("my reply = %+v %q", msgs[1].Envelope, msgs[1].PlainText())
	}
	for _, id := range ids[:3] {
		if _, ok := h.st.Body(gAcct, "", id); !ok {
			t.Errorf("body of %s not kept", id)
		}
	}
}

func TestGmailEngine_ModifyThreadMapsFlagsAndArchives(t *testing.T) {
	var ids [4]string
	h := newGmail(t, conversation(&ids))
	h.synced()
	ctx := context.Background()
	if err := h.eng.ModifyThread(ctx, "T", []string{`\Seen`, `\Flagged`}, nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids[:3] {
		m, _ := h.f.Message(id)
		if slices.Contains(m.LabelIDs, "UNREAD") || !slices.Contains(m.LabelIDs, "STARRED") {
			t.Fatalf("%s labels = %v", id, m.LabelIDs)
		}
	}
	if e := h.env("INBOX", ids[2]); e.IsUnread() || !e.IsFlagged() {
		t.Fatalf("store not refreshed: %v", e.Flags)
	}
	if err := h.eng.ModifyThread(ctx, "T", []string{`\Seen`}, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.ModifyThread(ctx, "T", nil, []string{`\Seen`}); err != nil {
		t.Fatal(err)
	}
	if m, _ := h.f.Message(ids[0]); !slices.Contains(m.LabelIDs, "UNREAD") {
		t.Fatalf("mark unread: %v", m.LabelIDs)
	}
	// Archive: INBOX is a folder name that is also the label id.
	if err := h.eng.ModifyThread(ctx, "T", nil, []string{"INBOX"}); err != nil {
		t.Fatal(err)
	}
	if got := h.ids("INBOX"); !slices.Equal(got, []string{ids[3]}) {
		t.Fatalf("INBOX after archive = %v", got)
	}
	// A folder name maps to its label id.
	if err := h.eng.ModifyThread(ctx, "T", []string{"Work"}, nil); err != nil {
		t.Fatal(err)
	}
	if m, _ := h.f.Message(ids[1]); !slices.Contains(m.LabelIDs, "Label_1") {
		t.Fatalf("Work label: %v", m.LabelIDs)
	}
}

func TestGmailEngine_TrashThreadTrashesEveryMessage(t *testing.T) {
	var ids [4]string
	h := newGmail(t, conversation(&ids))
	h.synced()
	if err := h.eng.TrashThread(context.Background(), "T"); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids[:3] {
		if m, _ := h.f.Message(id); !slices.Contains(m.LabelIDs, "TRASH") {
			t.Fatalf("%s labels = %v", id, m.LabelIDs)
		}
	}
	if got := h.ids("INBOX"); !slices.Equal(got, []string{ids[3]}) {
		t.Fatalf("INBOX after trash = %v", got)
	}
	if got := h.ids("Trash"); len(got) != 3 {
		t.Fatalf("Trash = %v", got)
	}
}

func TestThreadFetchSkipsStoredBodies(t *testing.T) {
	var ids [4]string
	h := newGmail(t, conversation(&ids))
	h.synced()
	h.eventually("bodies", func() bool {
		for _, id := range ids[:3] {
			if _, ok := h.st.Body(gAcct, "", id); !ok {
				return false
			}
		}
		return true
	})
	before := map[string]int{}
	for _, id := range ids[:3] {
		before[id] = rawFetches(h.f, id)
	}
	msgs, err := h.eng.Thread(context.Background(), "T")
	if err != nil || len(msgs) != 3 {
		t.Fatalf("Thread = %d, %v", len(msgs), err)
	}
	for _, id := range ids[:3] {
		if n := rawFetches(h.f, id); n != before[id] {
			t.Errorf("stored body of %s downloaded again (%d raw gets)", id, n-before[id])
		}
	}
	if !strings.Contains(msgs[2].PlainText(), "body of Re: Lunch") || msgs[2].Envelope.From[0].Email != "ann@example.com" || !msgs[2].Envelope.IsUnread() {
		t.Fatalf("stored message = %+v %q", msgs[2].Envelope, msgs[2].PlainText())
	}
}

func TestGmailEngine_Labels(t *testing.T) {
	var ids [4]string
	h := newGmail(t, conversation(&ids))
	h.synced()
	ctx := context.Background()
	var lab msync.Labels = h.eng
	if names := []string{}; true {
		for _, f := range lab.AccountLabels() {
			names = append(names, f.Name)
		}
		if !slices.Contains(names, "Work") {
			t.Fatalf("AccountLabels = %v", names)
		}
	}
	if got := lab.CurrentLabels("", ids[0]); slices.Contains(got, "Work") {
		t.Fatalf("CurrentLabels = %v", got)
	}
	if err := lab.ModifyLabels(ctx, "INBOX", ids[0], []string{"Work"}, nil); err != nil {
		t.Fatal(err)
	}
	if m, _ := h.f.Message(ids[0]); !slices.Contains(m.LabelIDs, "Label_1") {
		t.Fatalf("server labels = %v", m.LabelIDs)
	}
	if got := lab.CurrentLabels("T", ids[0]); !slices.Contains(got, "Work") {
		t.Fatalf("CurrentLabels(thread) = %v", got)
	}
	if err := lab.ModifyLabels(ctx, "INBOX", ids[0], nil, []string{"Work"}); err != nil {
		t.Fatal(err)
	}
	if got := lab.CurrentLabels("", ids[0]); slices.Contains(got, "Work") {
		t.Fatalf("after removal CurrentLabels = %v", got)
	}
}

func TestGmailEngine_SearchReturnsConversationRows(t *testing.T) {
	var ids [4]string
	h := newGmail(t, conversation(&ids))
	h.synced()
	envs, err := h.eng.Search(context.Background(), "INBOX", "from:ann")
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 1 || envs[0].ThreadID != "T" || envs[0].ThreadCount != 3 || envs[0].IsUnread() == false {
		t.Fatalf("rows = %+v", envs)
	}
	if len(envs[0].Participants) != 3 {
		t.Fatalf("participants = %+v", envs[0].Participants)
	}
}

func TestGmailEngine_SearchFindsMailOutsideSyncedLabels(t *testing.T) {
	var ids [4]string
	h := newGmail(t, func(f *gmailtest.Fake) {
		conversation(&ids)(f)
		f.AddMessage(gmailtest.Message{ThreadID: "Z", LabelIDs: []string{"CHAT"}, InternalDate: 9_000,
			Raw: fromRaw("Zed <zed@example.com>", "Old news", "9@x")})
	})
	h.synced()
	envs, err := h.eng.Search(context.Background(), "INBOX", "from:zed")
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 1 || envs[0].Subject != "Old news" || envs[0].ThreadCount != 1 || len(envs[0].Participants) != 1 {
		t.Fatalf("rows = %+v", envs)
	}
}

func TestGmailSearchPassesQueryVerbatim(t *testing.T) {
	var ids [4]string
	h := newGmail(t, conversation(&ids))
	h.synced()
	if _, err := h.eng.Search(context.Background(), "INBOX", "from:bob has:attachment"); err != nil {
		t.Fatal(err)
	}
	for _, r := range h.f.Requests() {
		if strings.HasSuffix(r.Path, "/threads") && strings.Contains(r.Query, "q=from%3Abob+has%3Aattachment") {
			return
		}
	}
	t.Fatalf("no threads.list with q=from:bob has:attachment in %+v", h.f.Requests())
}

func TestGmailEngine_SearchWritesNothingToTheStore(t *testing.T) {
	var ids [4]string
	h := newGmail(t, func(f *gmailtest.Fake) {
		conversation(&ids)(f)
		f.AddMessage(gmailtest.Message{ThreadID: "Z", LabelIDs: []string{"CHAT"}, InternalDate: 9_000,
			Raw: fromRaw("Zed <zed@example.com>", "Old news", "9@x")})
	})
	h.synced()
	before := len(h.st.ThreadEnvelopes(gAcct, "Z"))
	if _, err := h.eng.Search(context.Background(), "INBOX", "from:zed"); err != nil {
		t.Fatal(err)
	}
	if after := len(h.st.ThreadEnvelopes(gAcct, "Z")); after != before {
		t.Fatalf("store grew from %d to %d", before, after)
	}
}
