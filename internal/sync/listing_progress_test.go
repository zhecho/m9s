package sync_test

import (
	"fmt"
	"testing"

	"github.com/zhecho/m9s/internal/mail/gmail/gmailtest"
	"github.com/zhecho/m9s/internal/mail/graph/graphtest"
	msync "github.com/zhecho/m9s/internal/sync"
)

func listingOf(events []msync.Event, folder string) []msync.ListingProgress {
	var out []msync.ListingProgress
	for _, ev := range events {
		if p, ok := ev.(msync.ListingProgress); ok && p.FolderName == folder {
			out = append(out, p)
		}
	}
	return out
}

// The first listing of a label reports how far it is as each chunk is
// stored, so the open list fills while it runs and the header can say so.
func TestGmailEngine_FirstListingReportsProgress(t *testing.T) {
	const n = 150 // two chunks of headers
	h := newGmail(t, func(f *gmailtest.Fake) {
		for i := range n {
			f.AddMessage(gmailtest.Message{LabelIDs: []string{"INBOX"}, Raw: gRaw(fmt.Sprint("m", i), fmt.Sprintf("m%d@x", i))})
		}
	})
	h.synced()
	h.eventually("INBOX listing done", func() bool {
		ps := listingOf(h.snapshot(), "INBOX")
		return len(ps) > 0 && ps[len(ps)-1].Done
	})
	ps := listingOf(h.snapshot(), "INBOX")
	first, last := ps[0], ps[len(ps)-1]
	if first.AccountName != gAcct || first.Total != n || first.Stored != 0 || first.Done {
		t.Errorf("first report = %+v, want 0/%d", first, n)
	}
	if last.Stored != n || last.Total != n {
		t.Errorf("last report = %+v, want %d/%d done", last, n, n)
	}
	mid := false
	for _, p := range ps {
		if p.Stored > 0 && p.Stored < n && !p.Done {
			mid = true
		}
	}
	if !mid {
		t.Errorf("reports = %+v, want one between the chunks", ps)
	}
}

// A folder that is listed is reported done, also for Microsoft, whose
// listing is stored in one go.
func TestMicrosoftEngine_ListingReportsDone(t *testing.T) {
	h := newMicrosoft(t, func(f *graphtest.Fake) {
		f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "a", Raw: mRaw("a")})
		f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "b", Raw: mRaw("b")})
	})
	h.synced()
	h.eventually("INBOX listing done", func() bool {
		ps := listingOf(h.snapshot(), "INBOX")
		return len(ps) > 0 && ps[len(ps)-1].Done
	})
	ps := listingOf(h.snapshot(), "INBOX")
	if last := ps[len(ps)-1]; last.AccountName != mAcct || last.Stored != 2 || last.Total != 2 {
		t.Errorf("last report = %+v, want 2/2 done", last)
	}
}
