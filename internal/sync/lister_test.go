package sync_test

import (
	"context"
	"errors"
	"fmt"
	gosync "sync"
	"testing"

	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/mail/gmail/gmailtest"
	"github.com/zhecho/m9s/internal/mail/graph/graphtest"
	"github.com/zhecho/m9s/internal/store"
	msync "github.com/zhecho/m9s/internal/sync"
)

var (
	_ msync.Lister = (*msync.GmailEngine)(nil)
	_ msync.Lister = (*msync.MicrosoftEngine)(nil)
	_ msync.Lister = (*msync.Engine)(nil)
)

// Gmail lists a label's conversations a page at a time from the local copy,
// filtered over the whole label.
func TestGmailEngine_ListPagesAndFilters(t *testing.T) {
	h := newGmail(t, func(f *gmailtest.Fake) {
		for i := range 60 {
			subject := fmt.Sprint("note ", i)
			if i%10 == 0 {
				subject = fmt.Sprint("Invoice ", i)
			}
			f.AddMessage(gmailtest.Message{ThreadID: fmt.Sprint("T", i), LabelIDs: []string{"INBOX"},
				InternalDate: int64(1_700_000_000_000 + i), Raw: gRaw(subject, fmt.Sprintf("m%d@x", i))})
		}
	})
	h.synced()
	ctx := context.Background()
	rows, total, err := h.eng.List(ctx, "INBOX", store.ListQuery{Limit: 50})
	if err != nil || total != 60 || len(rows) != 50 {
		t.Fatalf("page 1: %d rows of %d, %v", len(rows), total, err)
	}
	if rows[0].ThreadID != "T59" || rows[0].ThreadCount != 1 {
		t.Errorf("first row = %+v, want the newest conversation", rows[0])
	}
	rows, total, _ = h.eng.List(ctx, "INBOX", store.ListQuery{Offset: 50, Limit: 50})
	if total != 60 || len(rows) != 10 {
		t.Errorf("page 2: %d rows of %d, want 10 of 60", len(rows), total)
	}
	rows, total, _ = h.eng.List(ctx, "INBOX", store.ListQuery{Filter: "invoice", Limit: 50})
	if total != 6 || len(rows) != 6 {
		t.Errorf("filter: %d rows of %d, want 6", len(rows), total)
	}
	if _, _, err := h.eng.List(ctx, msync.GmailAllMail, store.ListQuery{Limit: 50}); !errors.Is(err, msync.ErrNotListable) {
		t.Errorf("All Mail: err = %v, want ErrNotListable", err)
	}
}

func TestMicrosoftEngine_ListFilters(t *testing.T) {
	h := newMicrosoft(t, func(f *graphtest.Fake) {
		f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "Invoice 7", Raw: mRaw("Invoice 7")})
		f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "lunch", Raw: mRaw("lunch")})
	})
	h.synced()
	rows, total, err := h.eng.List(context.Background(), "INBOX", store.ListQuery{Filter: "INVOICE", Limit: 50})
	if err != nil || total != 1 || len(rows) != 1 || rows[0].Subject != "Invoice 7" {
		t.Errorf("filter = %d rows of %d, %v", len(rows), total, err)
	}
}

// headerMock records the pages the IMAP list asks the server for.
type headerMock struct {
	*mockClient
	mu    gosync.Mutex
	pages []string
	rows  []*mail.Envelope
}

func (m *headerMock) FetchHeaders(_ context.Context, folder string, page, size int) ([]*mail.Envelope, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pages = append(m.pages, fmt.Sprintf("%s %d %d", folder, page, size))
	return m.rows, nil
}

// IMAP: the unfiltered first page comes from the server (new mail shows at
// once); filters and later pages from the local copy.
func TestEngine_ListServerFirstPageStoreForTheRest(t *testing.T) {
	st := store.NewMemory()
	var stored []*mail.Envelope
	for i := 1; i <= 70; i++ {
		stored = append(stored, &mail.Envelope{ID: fmt.Sprint(i), UID: uint32(i), SortKey: int64(i), Subject: fmt.Sprint("msg ", i)})
	}
	stored[4].Subject = "Invoice"
	if err := st.AddMessages("acct", "INBOX", stored); err != nil {
		t.Fatal(err)
	}
	cmd := &headerMock{mockClient: newMock(nil), rows: stored[:3]}
	e := msync.NewEngineWithClients(newTestAccount("acct"), make(chan msync.Event, 8), newMock(nil), cmd)
	e.SetStore(st)
	ctx := context.Background()

	rows, total, err := e.List(ctx, "INBOX", store.ListQuery{Limit: 100})
	if err != nil || len(rows) != 3 || total != 70 {
		t.Fatalf("first page: %d rows of %d, %v", len(rows), total, err)
	}
	if len(cmd.pages) != 1 || cmd.pages[0] != "INBOX 1 100" {
		t.Errorf("server pages = %v, want one of 100 from INBOX", cmd.pages)
	}
	rows, total, _ = e.List(ctx, "INBOX", store.ListQuery{Filter: "invoice", Limit: 50})
	if total != 1 || len(rows) != 1 || rows[0].ID != "5" {
		t.Errorf("filter = %d rows of %d", len(rows), total)
	}
	rows, _, _ = e.List(ctx, "INBOX", store.ListQuery{Offset: 50, Limit: 50})
	if len(rows) != 20 || len(cmd.pages) != 1 {
		t.Errorf("page 2 = %d rows (server asked %d times), want 20 from the local copy", len(rows), len(cmd.pages))
	}
	rows, _, _ = e.List(ctx, "INBOX", store.ListQuery{})
	if len(rows) != 70 {
		t.Errorf("all = %d rows, want 70", len(rows))
	}
}
