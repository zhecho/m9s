package sync_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/zhecho/m9s/internal/mail/gmail/gmailtest"
	"github.com/zhecho/m9s/internal/mail/graph/graphtest"
	"github.com/zhecho/m9s/internal/store"
	msync "github.com/zhecho/m9s/internal/sync"
)

var (
	_ msync.PagedSearch = (*msync.GmailEngine)(nil)
	_ msync.PagedSearch = (*msync.MicrosoftEngine)(nil)
	_ msync.PagedSearch = (*msync.Engine)(nil)
)

// Gmail: a page of conversations, Gmail's estimate of how many match, and
// the cursor of the next page.
func TestGmailEngine_SearchPage(t *testing.T) {
	h := newGmail(t, func(f *gmailtest.Fake) {
		for i := range 60 {
			f.AddMessage(gmailtest.Message{ThreadID: fmt.Sprint("T", i), LabelIDs: []string{"INBOX"},
				InternalDate: int64(1_700_000_000_000 + i), Raw: gRaw(fmt.Sprint("linkedin ", i), fmt.Sprintf("m%d@x", i))})
		}
	})
	h.synced()
	ctx := context.Background()
	first, err := h.eng.SearchPage(ctx, "INBOX", "linkedin", "")
	if err != nil || len(first.Rows) != 50 || first.Total != 60 || !first.Estimate || first.Next == "" {
		t.Fatalf("page 1 = %d rows, total %d (estimate %v), next %q, %v", len(first.Rows), first.Total, first.Estimate, first.Next, err)
	}
	second, err := h.eng.SearchPage(ctx, "INBOX", "linkedin", first.Next)
	if err != nil || len(second.Rows) != 10 || second.Next != "" {
		t.Errorf("page 2 = %d rows, next %q, %v", len(second.Rows), second.Next, err)
	}
}

// Outlook does not say how many match; a next page is there or not.
func TestMicrosoftEngine_SearchPage(t *testing.T) {
	h := newMicrosoft(t, func(f *graphtest.Fake) {
		for i := range 60 {
			f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: fmt.Sprint("invoice ", i)})
		}
	})
	h.synced()
	ctx := context.Background()
	first, err := h.eng.SearchPage(ctx, "INBOX", "invoice", "")
	if err != nil || len(first.Rows) != 50 || first.Total != 0 || first.Next == "" {
		t.Fatalf("page 1 = %d rows, total %d, next %q, %v", len(first.Rows), first.Total, first.Next, err)
	}
	second, err := h.eng.SearchPage(ctx, "INBOX", "invoice", first.Next)
	if err != nil || len(second.Rows) != 10 || second.Next != "" {
		t.Errorf("page 2 = %d rows, next %q, %v", len(second.Rows), second.Next, err)
	}
}

// IMAP: the server lists every match, so the total is exact.
func TestEngine_SearchPage(t *testing.T) {
	var uids []uint32
	for u := uint32(1); u <= store.ListSize+10; u++ {
		uids = append(uids, u)
	}
	mc := &mockClient{searchResp: uids}
	eng := msync.NewEngineWithClients(newTestAccount("test"), make(chan msync.Event, 4), mc, mc)
	ctx := context.Background()
	first, err := eng.SearchPage(ctx, "INBOX", "x", "")
	if err != nil || len(first.Rows) != 50 || first.Total != 60 || first.Estimate || first.Next == "" {
		t.Fatalf("page 1 = %d rows, total %d (estimate %v), next %q, %v", len(first.Rows), first.Total, first.Estimate, first.Next, err)
	}
	if first.Rows[0].ID != "60" {
		t.Errorf("first row %s, want the newest", first.Rows[0].ID)
	}
	second, err := eng.SearchPage(ctx, "INBOX", "x", first.Next)
	if err != nil || len(second.Rows) != 10 || second.Next != "" || second.Rows[0].ID != "10" {
		t.Errorf("page 2 = %d rows, next %q, %v", len(second.Rows), second.Next, err)
	}
}
