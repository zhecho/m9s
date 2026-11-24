package mail_test

import (
	"slices"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/mail"
)

func TestEnvelopeIsUnread(t *testing.T) {
	e := &mail.Envelope{Flags: []string{}}
	if !e.IsUnread() {
		t.Error("envelope with no flags should be unread")
	}
	e.Flags = []string{`\Seen`}
	if e.IsUnread() {
		t.Error("envelope with \\Seen should not be unread")
	}
}

func TestEnvelopeFlagged(t *testing.T) {
	e := &mail.Envelope{Flags: []string{`\Flagged`}}
	if !e.IsFlagged() {
		t.Error("envelope with \\Flagged should be flagged")
	}
}

func TestFolderDisplayName(t *testing.T) {
	f := &mail.Folder{Name: "INBOX", Delimiter: "/"}
	if f.DisplayName() != "INBOX" {
		t.Errorf("want INBOX, got %q", f.DisplayName())
	}
	f2 := &mail.Folder{Name: "Work/Projects", Delimiter: "/"}
	if f2.DisplayName() != "Projects" {
		t.Errorf("want Projects, got %q", f2.DisplayName())
	}
}

func TestAddressString(t *testing.T) {
	a := mail.Address{Name: "Alice", Email: "alice@example.com"}
	if a.String() != "Alice <alice@example.com>" {
		t.Errorf("unexpected: %q", a.String())
	}
	b := mail.Address{Email: "bob@example.com"}
	if b.String() != "bob@example.com" {
		t.Errorf("unexpected: %q", b.String())
	}
}

func TestManagerActiveAccount(t *testing.T) {
	mgr := mail.NewManager([]*mail.Account{
		{Name: "a"},
		{Name: "b"},
	})
	if mgr.Active().Name != "a" {
		t.Errorf("want first account active, got %q", mgr.Active().Name)
	}
	mgr.Next()
	if mgr.Active().Name != "b" {
		t.Errorf("want b after Next(), got %q", mgr.Active().Name)
	}
	mgr.Next()
	if mgr.Active().Name != "a" {
		t.Errorf("want wrap-around to a, got %q", mgr.Active().Name)
	}
	mgr.Prev()
	if mgr.Active().Name != "b" {
		t.Errorf("want b after Prev(), got %q", mgr.Active().Name)
	}
}

func TestEnvelopeDateFormat(t *testing.T) {
	e := &mail.Envelope{Date: time.Date(2026, 3, 27, 10, 23, 0, 0, time.UTC)}
	if e.FormattedDate("Jan 02 15:04") != "Mar 27 10:23" {
		t.Errorf("unexpected: %q", e.FormattedDate("Jan 02 15:04"))
	}
}

func TestEnvelopeNotFlagged(t *testing.T) {
	e := &mail.Envelope{Flags: []string{`\Seen`}}
	if e.IsFlagged() {
		t.Error("envelope without \\Flagged should not be flagged")
	}
}

func TestEnvelopeFromDisplay(t *testing.T) {
	e := &mail.Envelope{From: []mail.Address{{Name: "Alice", Email: "alice@example.com"}}}
	if e.FromDisplay() != "Alice" {
		t.Errorf("want name, got %q", e.FromDisplay())
	}
	e2 := &mail.Envelope{From: []mail.Address{{Email: "alice@example.com"}}}
	if e2.FromDisplay() != "alice@example.com" {
		t.Errorf("want email, got %q", e2.FromDisplay())
	}
	e3 := &mail.Envelope{}
	if e3.FromDisplay() != "" {
		t.Errorf("want empty, got %q", e3.FromDisplay())
	}
}

func TestMessagePlainText(t *testing.T) {
	m := &mail.Message{TextBody: "hello"}
	if m.PlainText() != "hello" {
		t.Errorf("want text body, got %q", m.PlainText())
	}
	m2 := &mail.Message{HTMLBody: "<b>world</b>"}
	if m2.PlainText() != "world" {
		t.Errorf("want stripped html, got %q", m2.PlainText())
	}
	m3 := &mail.Message{}
	if m3.PlainText() != "" {
		t.Errorf("want empty, got %q", m3.PlainText())
	}
}

func TestFolderIsNoSelect(t *testing.T) {
	f := &mail.Folder{Attributes: []string{`\NoSelect`}}
	if !f.IsNoSelect() {
		t.Error("want NoSelect=true")
	}
	f2 := &mail.Folder{Attributes: []string{`\HasChildren`}}
	if f2.IsNoSelect() {
		t.Error("want NoSelect=false")
	}
}

func TestFolderDepth(t *testing.T) {
	f := &mail.Folder{Name: "INBOX", Delimiter: "/"}
	if f.Depth() != 0 {
		t.Errorf("want 0, got %d", f.Depth())
	}
	f2 := &mail.Folder{Name: "Work/Projects/Active", Delimiter: "/"}
	if f2.Depth() != 2 {
		t.Errorf("want 2, got %d", f2.Depth())
	}
	f3 := &mail.Folder{Name: "INBOX", Delimiter: ""}
	if f3.Depth() != 0 {
		t.Errorf("want 0 for empty delimiter, got %d", f3.Depth())
	}
}

func TestManagerAll(t *testing.T) {
	mgr := mail.NewManager([]*mail.Account{{Name: "a"}, {Name: "b"}})
	all := mgr.All()
	if len(all) != 2 {
		t.Errorf("want 2, got %d", len(all))
	}
}

func TestManagerSetActive(t *testing.T) {
	mgr := mail.NewManager([]*mail.Account{{Name: "a"}, {Name: "b"}})
	mgr.SetActive("b")
	if mgr.Active().Name != "b" {
		t.Errorf("want b, got %q", mgr.Active().Name)
	}
	// unknown name — no change
	mgr.SetActive("nonexistent")
	if mgr.Active().Name != "b" {
		t.Errorf("unknown name should not change active account")
	}
}

func TestManagerEmptyAccounts(t *testing.T) {
	mgr := mail.NewManager(nil)
	if mgr.Active() != nil {
		t.Error("empty manager should return nil active")
	}
	mgr.Next() // must not panic
	mgr.Prev() // must not panic
}

func TestNewestFirstUsesSortKey(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 11, d, 9, 0, 0, 0, time.UTC) }
	// UIDs deliberately disagree with SortKey: SortKey must decide the tie.
	in := []*mail.Envelope{
		{ID: "a", UID: 50, SortKey: 1, Date: day(1)},
		{ID: "b", UID: 1, SortKey: 9, Date: day(3)},
		{ID: "c", UID: 40, SortKey: 4, Date: day(2)},
		{ID: "d", UID: 2, SortKey: 5, Date: day(2)}, // same moment as c: higher SortKey first
	}
	got := mail.NewestFirst(in)
	var ids []string
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	if want := []string{"b", "d", "c", "a"}; !slices.Equal(ids, want) {
		t.Errorf("order = %v, want %v", ids, want)
	}
	if in[0].ID != "a" || in[1].ID != "b" {
		t.Error("the caller's slice must not be reordered")
	}
}

func TestNewestFirst_comparesInstantsAcrossTimeZones(t *testing.T) {
	sofia := time.FixedZone("EET", 2*3600)
	in := []*mail.Envelope{
		{UID: 1, Date: time.Date(2026, 11, 2, 10, 0, 0, 0, sofia)},   // 08:00 UTC
		{UID: 2, Date: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)}, // 09:00 UTC: later
	}
	if got := mail.NewestFirst(in); got[0].UID != 2 {
		t.Errorf("first = UID %d, want 2 (the later instant)", got[0].UID)
	}
}

func TestMessageFiles_leavesOutEmbeddedPictures(t *testing.T) {
	msg := &mail.Message{Attachments: []*mail.Attachment{
		{Filename: "logo.png", Inline: true, ContentID: "logo"},
		{Filename: "bill.pdf"},
	}}
	files := msg.Files()
	if len(files) != 1 || files[0].Filename != "bill.pdf" {
		t.Errorf("Files() = %+v, want only bill.pdf", files)
	}
}
