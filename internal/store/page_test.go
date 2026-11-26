package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/mail"
)

func pageEnv(i int, subject, from string) *mail.Envelope {
	return &mail.Envelope{ID: fmt.Sprintf("m%03d", i), SortKey: int64(i), Subject: subject,
		From: []mail.Address{{Name: from, Email: "x@example.com"}},
		Date: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute)}
}

func ids(envs []*mail.Envelope) []string {
	out := make([]string, len(envs))
	for i, e := range envs {
		out[i] = e.ID
	}
	return out
}

// The list pages through the whole folder, newest first, and says how many
// there are in all.
func TestPage_pagesNewestFirst(t *testing.T) {
	s := NewMemory()
	var list []*mail.Envelope
	for i := 1; i <= 120; i++ {
		list = append(list, pageEnv(i, "msg", "Bob"))
	}
	must(t, s.AddMessages("a", "INBOX", list))

	for _, tc := range []struct{ offset, want int }{{0, 50}, {50, 50}, {100, 20}} {
		rows, total, err := s.Page("a", "INBOX", ListQuery{Offset: tc.offset, Limit: 50})
		must(t, err)
		if total != 120 || len(rows) != tc.want {
			t.Fatalf("offset %d: %d rows of %d, want %d of 120", tc.offset, len(rows), total, tc.want)
		}
		if first := fmt.Sprintf("m%03d", 120-tc.offset); rows[0].ID != first {
			t.Errorf("offset %d starts at %s, want %s", tc.offset, rows[0].ID, first)
		}
	}
	all, _, _ := s.Page("a", "INBOX", ListQuery{})
	if len(all) != 120 {
		t.Errorf("Limit 0 gave %d rows, want all 120", len(all))
	}
}

// The filter matches subject, sender name and sender address over the whole
// folder, ignoring case — Cyrillic too — and takes % and _ literally.
func TestPage_filtersTheWholeFolder(t *testing.T) {
	s := NewMemory()
	must(t, s.AddMessages("a", "INBOX", []*mail.Envelope{
		pageEnv(1, "Оферта за ремонт", "Иван"),
		pageEnv(2, "Invoice 2026", "Acme"),
		pageEnv(3, "lunch", "Bob"),
		pageEnv(4, "100% done", "Carol"),
		pageEnv(5, "a_b", "Dan"),
		pageEnv(6, "nothing", "Eve"),
	}))
	for q, want := range map[string]string{
		"оферта":  "m001",
		"INVOICE": "m002",
		"acme":    "m002",
		"иван":    "m001",
		"100%":    "m004",
		"a_b":     "m005",
	} {
		rows, total, err := s.Page("a", "INBOX", ListQuery{Filter: q, Limit: 50})
		must(t, err)
		if total != 1 || len(rows) != 1 || rows[0].ID != want {
			t.Errorf("filter %q = %v (total %d), want [%s]", q, ids(rows), total, want)
		}
	}
	if rows, total, _ := s.Page("a", "INBOX", ListQuery{Filter: "x@example.com", Limit: 50}); total != 6 || len(rows) != 6 {
		t.Errorf("sender address matched %d, want 6", total)
	}
}

// A conversation matches when any of its messages does; the total counts
// conversations.
func TestPage_conversations(t *testing.T) {
	s := NewMemory()
	a1, a2, b := pageEnv(1, "Quote request", "Ann"), pageEnv(2, "Re: Quote request", "Me"), pageEnv(3, "Other", "Bob")
	a1.ThreadID, a2.ThreadID, b.ThreadID = "T1", "T1", "T2"
	a2.Subject = "Re: thanks" // the newest message does not match "quote" itself
	must(t, s.AddMessages("a", "INBOX", []*mail.Envelope{a1, a2, b}))

	rows, total, err := s.Page("a", "INBOX", ListQuery{Conversations: true, Limit: 50})
	must(t, err)
	if total != 2 || len(rows) != 2 || rows[0].ThreadID != "T2" || rows[1].ThreadCount != 2 {
		t.Fatalf("conversations = %v (total %d)", ids(rows), total)
	}
	rows, total, _ = s.Page("a", "INBOX", ListQuery{Conversations: true, Filter: "quote", Limit: 50})
	if total != 1 || len(rows) != 1 || rows[0].ThreadID != "T1" || rows[0].ID != "m002" {
		t.Errorf("filtered conversations = %v (total %d), want T1 by its latest message", ids(rows), total)
	}
}

// A version-5 index gains the search column and fills it for its rows.
func TestMigrateV5FillsTheSearchColumn(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mail")
	s, err := Open(dir)
	must(t, err)
	must(t, s.AddMessages("a", "INBOX", []*mail.Envelope{pageEnv(1, "Оферта", "Иван")}))
	must(t, s.Close())
	db := openRaw(t, dir)
	_, err = db.Exec("ALTER TABLE messages DROP COLUMN match_text; PRAGMA user_version = 5")
	must(t, err)
	must(t, db.Close())

	s, err = Open(dir)
	must(t, err)
	defer s.Close()
	if rows, _, _ := s.Page("a", "INBOX", ListQuery{Filter: "оферта", Limit: 50}); len(rows) != 1 {
		t.Errorf("an old row does not match after the migration: %v", ids(rows))
	}
}

// Building conversation rows must not scan the label once per conversation:
// A on a big Gmail label lists every one of them.
func TestPage_conversationsScale(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "mail"))
	must(t, err)
	defer s.Close()
	var list []*mail.Envelope
	for i := 1; i <= 6000; i++ {
		e := pageEnv(i, "msg", "Bob")
		e.ThreadID = fmt.Sprint("T", i/2) // conversations of two
		list = append(list, e)
	}
	must(t, s.AddMessages("a", "INBOX", list))
	start := time.Now()
	rows, total, err := s.Page("a", "INBOX", ListQuery{Conversations: true})
	must(t, err)
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("listing %d conversations took %v", total, took)
	}
	if len(rows) != total || total != 3001 {
		t.Errorf("rows %d, total %d, want 3001", len(rows), total)
	}
}

// A conversation is ordered by its newest message, also when only an older
// one matches the filter; ties have a fixed order, so pages neither skip
// nor repeat a row.
func TestPage_filteredConversationOrderedByItsNewest(t *testing.T) {
	s := NewMemory()
	old, reply, other := pageEnv(1, "Quote request", "Ann"), pageEnv(9, "Re: thanks", "Me"), pageEnv(5, "Quote two", "Bob")
	old.ThreadID, reply.ThreadID, other.ThreadID = "T1", "T1", "T2"
	must(t, s.AddMessages("a", "INBOX", []*mail.Envelope{old, reply, other}))
	rows, _, _ := s.Page("a", "INBOX", ListQuery{Conversations: true, Filter: "quote", Limit: 50})
	if len(rows) != 2 || rows[0].ThreadID != "T1" {
		t.Errorf("order = %v, want T1 first (its newest message is the newest)", ids(rows))
	}
}

// A body that arrives after Close (a fetch still running when m9s quits) is
// not written: the store's folder may already be on its way out.
func TestPutBodyAfterCloseWritesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mail")
	s, err := Open(dir)
	must(t, err)
	must(t, s.AddMessages("a", "INBOX", []*mail.Envelope{pageEnv(1, "x", "y")}))
	must(t, s.Close())
	_ = s.PutBody("a", "INBOX", "m001", []byte("Subject: late\r\n\r\nbody"))
	if _, err := os.Stat(s.bodyPath("a", "INBOX", "m001")); !os.IsNotExist(err) {
		t.Errorf("a body was written after Close (stat: %v)", err)
	}
}
