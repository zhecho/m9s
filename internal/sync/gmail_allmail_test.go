package sync_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/mail/gmail/gmailtest"
	msync "github.com/zhecho/m9s/internal/sync"
)

const threadsPath = "/gmail/v1/users/me/threads"

func folderNamesOf(fs []*mail.Folder) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}

// allMailLists counts threads.list calls made for All Mail: no query, no label.
func allMailLists(f *gmailtest.Fake) int {
	n := 0
	for _, r := range f.Requests() {
		if r.Path == threadsPath && !strings.Contains(r.Query, "q=") && !strings.Contains(r.Query, "labelIds") {
			n++
		}
	}
	return n
}

func allMailEvent(h *gHarness) bool {
	for _, ev := range h.snapshot() {
		if nm, ok := ev.(msync.NewMail); ok && nm.FolderName == msync.GmailAllMail && nm.AccountName == gAcct {
			return true
		}
	}
	return false
}

func (h *gHarness) allMailThreads() []string {
	envs, _ := h.eng.FetchHeaders(context.Background(), msync.GmailAllMail, 1, 50)
	var ids []string
	for _, e := range envs {
		ids = append(ids, e.ThreadID)
	}
	slices.Sort(ids)
	return ids
}

// fourThreads: A in the Inbox, B archived (Work label only), C in Spam, D in Trash.
func fourThreads(f *gmailtest.Fake) {
	f.AddMessage(gmailtest.Message{ThreadID: "A", LabelIDs: []string{"INBOX"}, InternalDate: 4_000, Raw: gRaw("a", "a@x")})
	f.AddMessage(gmailtest.Message{ThreadID: "B", LabelIDs: []string{"Label_1"}, InternalDate: 3_000, Raw: gRaw("b", "b@x")})
	f.AddMessage(gmailtest.Message{ThreadID: "C", LabelIDs: []string{"SPAM"}, InternalDate: 2_000, Raw: gRaw("c", "c@x")})
	f.AddMessage(gmailtest.Message{ThreadID: "D", LabelIDs: []string{"TRASH"}, InternalDate: 1_000, Raw: gRaw("d", "d@x")})
}

// listed waits for All Mail's first listing to arrive.
func (h *gHarness) listed() {
	h.t.Helper()
	h.allMailThreads() // asks for it
	h.eventually("All Mail listed", func() bool { return allMailEvent(h) })
}

func TestGmailAllMail_folderAfterDrafts(t *testing.T) {
	h := newGmail(t, nil)
	h.synced()
	fs, _ := h.eng.Folders(context.Background())
	names := folderNamesOf(fs)
	i := slices.Index(names, msync.GmailAllMail)
	if i < 1 || names[i-1] != "Drafts" {
		t.Fatalf("folders = %v, want All Mail right after Drafts", names)
	}
	if _, known := h.st.Envelopes(gAcct, msync.GmailAllMail); known {
		t.Error("All Mail is in the local copy")
	}
	h.eventually("FoldersUpdated with All Mail", func() bool {
		for _, ev := range h.snapshot() {
			if fu, ok := ev.(msync.FoldersUpdated); ok && slices.Contains(folderNamesOf(fu.Folders), msync.GmailAllMail) {
				return true
			}
		}
		return false
	})
}

func TestGmailAllMail_listsEverythingButSpamAndTrash(t *testing.T) {
	h := newGmail(t, fourThreads)
	h.synced()
	h.listed()
	if got := h.allMailThreads(); !slices.Equal(got, []string{"A", "B"}) {
		t.Fatalf("All Mail = %v, want A and B", got)
	}
}

func TestGmailAllMail_relistsOnlyAfterAChange(t *testing.T) {
	h := newGmail(t, fourThreads)
	h.synced()
	h.listed()
	before := allMailLists(h.f)
	h.allMailThreads()
	h.allMailThreads()
	if n := allMailLists(h.f); n != before {
		t.Fatalf("All Mail listed %d more times without a change", n-before)
	}
	cursor := h.st.Cursor(gAcct)
	h.f.AddMessage(gmailtest.Message{ThreadID: "E", LabelIDs: []string{"Label_1"}, InternalDate: 5_000, Raw: gRaw("e", "e@x")})
	h.eventually("cursor moved", func() bool { return h.st.Cursor(gAcct) != cursor })
	h.eventually("E in All Mail", func() bool { return slices.Contains(h.allMailThreads(), "E") })
}

func TestGmailAllMail_moveToInboxOnlyAddsTheLabel(t *testing.T) {
	var single string
	h := newGmail(t, func(f *gmailtest.Fake) {
		fourThreads(f)
		single = f.AddMessage(gmailtest.Message{ThreadID: "S", LabelIDs: []string{"Label_1"}, Raw: gRaw("s", "s@x")})
	})
	h.synced()
	ctx := context.Background()
	if err := h.eng.ModifyThread(ctx, "B", []string{"INBOX"}, []string{msync.GmailAllMail}); err != nil {
		t.Fatal(err)
	}
	for _, id := range h.f.MessageIDs("Label_1") {
		if m, _ := h.f.Message(id); m.ThreadID == "B" && !slices.Contains(m.LabelIDs, "INBOX") {
			t.Errorf("B's labels = %v, want INBOX added and Label_1 kept", m.LabelIDs)
		}
	}
	if err := h.eng.Move(ctx, msync.GmailAllMail, single, "INBOX"); err != nil {
		t.Fatal(err)
	}
	if m, _ := h.f.Message(single); !slices.Contains(m.LabelIDs, "INBOX") || !slices.Contains(m.LabelIDs, "Label_1") {
		t.Errorf("labels after a move from All Mail = %v, want INBOX added and Label_1 kept", m.LabelIDs)
	}
	got := h.eng.MoveTargets(msync.GmailAllMail)
	if !slices.Contains(got, "INBOX") || slices.Contains(got, msync.GmailAllMail) {
		t.Errorf("MoveTargets(All Mail) = %v, want INBOX and not All Mail", got)
	}
}

func TestGmailAllMail_archiveKeepsTheRow(t *testing.T) {
	h := newGmail(t, fourThreads)
	h.synced()
	h.listed()
	cursor := h.st.Cursor(gAcct)
	if err := h.eng.ModifyThread(context.Background(), "A", nil, []string{"INBOX"}); err != nil {
		t.Fatal(err)
	}
	h.eventually("cursor moved", func() bool { return h.st.Cursor(gAcct) != cursor })
	lists := allMailLists(h.f)
	h.eventually("relisted", func() bool { h.allMailThreads(); return allMailLists(h.f) > lists })
	h.eventually("A still in All Mail", func() bool { return slices.Contains(h.allMailThreads(), "A") })
}

func TestGmailAllMail_listingFailureKeepsRows(t *testing.T) {
	h := newGmail(t, fourThreads)
	h.synced()
	h.listed()
	cursor := h.st.Cursor(gAcct)
	h.f.Queue(threadsPath, 400, `{"error":{"code":400,"message":"bad"}}`)
	h.f.AddMessage(gmailtest.Message{ThreadID: "E", LabelIDs: []string{"Label_1"}, InternalDate: 5_000, Raw: gRaw("e", "e@x")})
	h.eventually("cursor moved", func() bool { return h.st.Cursor(gAcct) != cursor })
	lists := allMailLists(h.f)
	h.eventually("a listing tried", func() bool { h.allMailThreads(); return allMailLists(h.f) > lists })
	if got := h.allMailThreads(); !slices.Contains(got, "A") || !slices.Contains(got, "B") {
		t.Fatalf("after a failed listing All Mail = %v, want the old rows", got)
	}
	h.eventually("retried and E listed", func() bool { return slices.Contains(h.allMailThreads(), "E") })
}

func TestGmailAllMail_userLabelOfThatName(t *testing.T) {
	h := newGmailSetup(t, func(f *gmailtest.Fake) {
		f.AddLabel("Label_2", "All Mail")
		f.AddMessage(gmailtest.Message{ThreadID: "L", LabelIDs: []string{"Label_2"}, Raw: gRaw("l", "l@x")})
	}, nil)
	h.stop()
	h.start("Work", "All Mail")
	h.synced()
	fs, _ := h.eng.Folders(context.Background())
	n := 0
	for _, name := range folderNamesOf(fs) {
		if name == msync.GmailAllMail {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("folders = %v, want one All Mail", folderNamesOf(fs))
	}
	h.eventually("label rows from the store", func() bool {
		_, known := h.st.Envelopes(gAcct, msync.GmailAllMail)
		return known
	})
	lists := allMailLists(h.f)
	if got := h.allMailThreads(); !slices.Equal(got, []string{"L"}) {
		t.Errorf("All Mail rows = %v, want the label's thread L", got)
	}
	if allMailLists(h.f) != lists {
		t.Error("the label was listed as the virtual All Mail")
	}
}

func allMailEvents(h *gHarness) int {
	n := 0
	for _, ev := range h.snapshot() {
		if nm, ok := ev.(msync.NewMail); ok && nm.FolderName == msync.GmailAllMail {
			n++
		}
	}
	return n
}

func TestGmailAllMail_failedFirstListingIsReportedAndRetriedAfterAChange(t *testing.T) {
	h := newGmail(t, fourThreads)
	h.synced()
	h.f.Queue(threadsPath, 400, `{"error":{"code":400,"message":"bad"}}`)
	h.allMailThreads() // asks; the listing fails
	h.eventually("the failure reported", func() bool {
		_, err := h.eng.FetchHeaders(context.Background(), msync.GmailAllMail, 1, 50)
		return err != nil
	})
	before := allMailEvents(h)
	cursor := h.st.Cursor(gAcct)
	h.f.AddMessage(gmailtest.Message{ThreadID: "E", LabelIDs: []string{"Label_1"}, InternalDate: 5_000, Raw: gRaw("e", "e@x")})
	h.eventually("cursor moved", func() bool { return h.st.Cursor(gAcct) != cursor })
	h.eventually("an open All Mail told to reload", func() bool { return allMailEvents(h) > before })
}

func TestGmailAllMail_starAndTrashShowAtOnce(t *testing.T) {
	h := newGmail(t, fourThreads)
	h.synced()
	h.listed()
	ctx := context.Background()
	if err := h.eng.ModifyThread(ctx, "A", []string{`\Flagged`}, nil); err != nil {
		t.Fatal(err)
	}
	envs, _ := h.eng.FetchHeaders(ctx, msync.GmailAllMail, 1, 50)
	for _, e := range envs {
		if e.ThreadID == "A" && !e.IsFlagged() {
			t.Error("A not starred in All Mail right after the star")
		}
	}
	if err := h.eng.TrashThread(ctx, "B"); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(h.allMailThreads(), "B") {
		t.Error("B still in All Mail right after it was trashed")
	}
}

func TestGmailAllMail_rowsAreCopies(t *testing.T) {
	h := newGmail(t, fourThreads)
	h.synced()
	h.listed()
	envs, _ := h.eng.FetchHeaders(context.Background(), msync.GmailAllMail, 1, 50)
	envs[0].Flags = append(envs[0].Flags, "MARK")
	again, _ := h.eng.FetchHeaders(context.Background(), msync.GmailAllMail, 1, 50)
	if slices.Contains(again[0].Flags, "MARK") {
		t.Error("a change to a handed-out row reached the cache")
	}
}

func TestGmailAllMail_foldersFallbackKeepsAllMail(t *testing.T) {
	h := newGmail(t, nil)
	h.synced()
	h.f.Queue("/gmail/v1/users/me/labels", 400, `{"error":{"code":400,"message":"bad"}}`)
	fs, _ := h.eng.Folders(context.Background())
	if !slices.Contains(folderNamesOf(fs), msync.GmailAllMail) {
		t.Errorf("folders after a failed label read = %v, want All Mail", folderNamesOf(fs))
	}
}
