package graph

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/mail/graph/graphtest"
)

func TestClient_me(t *testing.T) {
	c, f, _ := newTest(t)
	f.SetMe("me@outlook.com", "me@outlook.com")
	if got, err := c.Me(context.Background()); err != nil || got != "me@outlook.com" {
		t.Fatalf("Me = %q, %v", got, err)
	}
	// Personal accounts may have no mail property: the sign-in name is the address.
	f.SetMe("", "solo@hotmail.com")
	if got, _ := c.Me(context.Background()); got != "solo@hotmail.com" {
		t.Fatalf("Me = %q", got)
	}
}

func folderByID(fs []Folder, id string) *Folder {
	for i := range fs {
		if fs[i].ID == id {
			return &fs[i]
		}
	}
	return nil
}

func TestClient_folders(t *testing.T) {
	c, f, _ := newTest(t)
	inbox := f.FolderID("inbox")
	f.SetFolderName(inbox, "Входящи")
	projects := f.AddFolder("", "Projects", "")
	y2026 := f.AddFolder(projects, "2026", "")
	f.AddMessage(inbox, graphtest.Message{Subject: "new"})
	f.AddMessage(inbox, graphtest.Message{Subject: "old", IsRead: true})

	fs, err := c.Folders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	in := folderByID(fs, inbox)
	if in == nil || in.WellKnown != "inbox" || in.DisplayName != "Входящи" || in.Total != 2 || in.Unread != 1 || in.ParentID != "" {
		t.Fatalf("inbox = %+v", in)
	}
	p, y := folderByID(fs, projects), folderByID(fs, y2026)
	if p == nil || p.ParentID != "" || p.WellKnown != "" {
		t.Fatalf("Projects = %+v", p)
	}
	if y == nil || y.ParentID != projects || y.DisplayName != "2026" {
		t.Fatalf("2026 = %+v", y)
	}
	for _, wk := range []string{"sentitems", "drafts", "deleteditems", "junkemail", "archive"} {
		if g := folderByID(fs, f.FolderID(wk)); g == nil || g.WellKnown != wk {
			t.Errorf("%s = %+v", wk, g)
		}
	}
}

func TestClient_foldersWithoutArchive(t *testing.T) {
	c, f, _ := newTest(t)
	f.RemoveFolder(f.FolderID("archive"))
	fs, err := c.Folders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range fs {
		if g.WellKnown == "archive" {
			t.Fatalf("archive reported: %+v", g)
		}
	}
	if len(fs) != 5 {
		t.Fatalf("folders = %+v", fs)
	}
}

func TestClient_foldersFollowPages(t *testing.T) {
	c, f, _ := newTest(t)
	f.SetPageSize(2)
	p := f.AddFolder("", "P", "")
	for _, n := range []string{"a", "b", "c"} {
		f.AddFolder(p, n, "")
	}
	fs, err := c.Folders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 6+1+3 {
		t.Fatalf("got %d folders", len(fs))
	}
}

func ids(ms []Msg) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ID)
	}
	slices.Sort(out)
	return out
}

func TestClient_deltaRounds(t *testing.T) {
	c, f, _ := newTest(t)
	ctx := context.Background()
	inbox := f.FolderID("inbox")
	a := f.AddMessage(inbox, graphtest.Message{Subject: "a"})
	b := f.AddMessage(inbox, graphtest.Message{Subject: "b"})

	changed, removed, link, err := c.Delta(ctx, inbox, "")
	if err != nil || !slices.Equal(ids(changed), []string{a, b}) || len(removed) != 0 || link == "" {
		t.Fatalf("first round: %v %v %q %v", ids(changed), removed, link, err)
	}

	nc := f.AddMessage(inbox, graphtest.Message{Subject: "c"})
	f.Delete(a)
	changed, removed, link2, err := c.Delta(ctx, inbox, link)
	if err != nil || !slices.Equal(ids(changed), []string{nc}) || !slices.Equal(removed, []string{a}) || link2 == "" {
		t.Fatalf("second round: %v %v %q %v", ids(changed), removed, link2, err)
	}

	changed, removed, _, err = c.Delta(ctx, inbox, link2)
	if err != nil || len(changed) != 0 || len(removed) != 0 {
		t.Fatalf("quiet round: %v %v %v", ids(changed), removed, err)
	}

	f.ExpireDelta(inbox)
	if _, _, _, err := c.Delta(ctx, inbox, link2); !errors.Is(err, ErrSyncReset) {
		t.Fatalf("expired: err = %v", err)
	}
}

func TestClient_deltaFollowsPagesAndMoves(t *testing.T) {
	c, f, _ := newTest(t)
	ctx := context.Background()
	f.SetPageSize(1)
	inbox, archive := f.FolderID("inbox"), f.FolderID("archive")
	a := f.AddMessage(inbox, graphtest.Message{Subject: "a"})
	b := f.AddMessage(inbox, graphtest.Message{Subject: "b"})
	f.AddMessage(inbox, graphtest.Message{Subject: "c"})
	changed, _, link, err := c.Delta(ctx, inbox, "")
	if err != nil || len(changed) != 3 {
		t.Fatalf("paged first round: %d %v", len(changed), err)
	}
	_, _, alink, err := c.Delta(ctx, archive, "")
	if err != nil {
		t.Fatal(err)
	}

	f.Move(a, archive)
	f.Update(b, func(m *graphtest.Message) { m.IsRead = true })
	changed, removed, _, err := c.Delta(ctx, inbox, link)
	if err != nil || !slices.Equal(ids(changed), []string{b}) || !changed[0].IsRead || !slices.Equal(removed, []string{a}) {
		t.Fatalf("inbox after move: %v %v %v", ids(changed), removed, err)
	}
	changed, _, _, err = c.Delta(ctx, archive, alink)
	if err != nil || !slices.Equal(ids(changed), []string{a}) || changed[0].FolderID != archive {
		t.Fatalf("archive after move: %v %v", ids(changed), err)
	}
}

func TestClient_messageFields(t *testing.T) {
	c, f, _ := newTest(t)
	inbox := f.FolderID("inbox")
	when := time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)
	id := f.AddMessage(inbox, graphtest.Message{
		Subject:           "Отчет",
		From:              mail.Address{Name: "Bob", Email: "bob@example.com"},
		To:                []mail.Address{{Email: "me@outlook.com"}},
		CC:                []mail.Address{{Name: "Ann", Email: "ann@example.com"}},
		Received:          when,
		IsRead:            true,
		Flagged:           true,
		HasAttachments:    true,
		Categories:        []string{"Work", "Travel"},
		ConversationID:    "conv1",
		InternetMessageID: "<x1@example.com>",
	})
	ms, err := c.List(context.Background(), inbox)
	if err != nil || len(ms) != 1 {
		t.Fatalf("List: %v %v", ms, err)
	}
	m := ms[0]
	if m.ID != id || m.FolderID != inbox || m.Subject != "Отчет" || m.ConversationID != "conv1" ||
		m.InternetMessageID != "x1@example.com" || !m.Received.Equal(when) ||
		!m.IsRead || !m.Flagged || !m.HasAttachments || !slices.Equal(m.Categories, []string{"Work", "Travel"}) {
		t.Fatalf("msg = %+v", m)
	}
	if len(m.From) != 1 || m.From[0] != (mail.Address{Name: "Bob", Email: "bob@example.com"}) ||
		len(m.To) != 1 || m.To[0].Email != "me@outlook.com" || len(m.CC) != 1 || m.CC[0].Name != "Ann" {
		t.Fatalf("addresses = %+v / %+v / %+v", m.From, m.To, m.CC)
	}
	g, err := c.Get(context.Background(), id)
	if err != nil || g.ID != id || g.Subject != "Отчет" {
		t.Fatalf("Get = %+v %v", g, err)
	}
}

func TestClient_listFollowsPages(t *testing.T) {
	c, f, _ := newTest(t)
	f.SetPageSize(2)
	inbox := f.FolderID("inbox")
	for i := 0; i < 5; i++ {
		f.AddMessage(inbox, graphtest.Message{Subject: "m"})
	}
	ms, err := c.List(context.Background(), inbox)
	if err != nil || len(ms) != 5 {
		t.Fatalf("List: %d %v", len(ms), err)
	}
}

func TestClient_rawAndMIME(t *testing.T) {
	c, f, _ := newTest(t)
	ctx := context.Background()
	raw := []byte("Subject: hi\r\n\r\nbody\r\n")
	id := f.AddMessage(f.FolderID("inbox"), graphtest.Message{Raw: raw})
	got, err := c.Raw(ctx, id)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("Raw = %q %v", got, err)
	}

	out := []byte("To: bob@example.com\r\nSubject: out\r\n\r\nhello\r\n")
	if err := c.SendMIME(ctx, out); err != nil {
		t.Fatal(err)
	}
	draft := []byte("Subject: draft\r\n\r\nlater\r\n")
	did, err := c.CreateDraftMIME(ctx, draft)
	if err != nil || did == "" {
		t.Fatalf("CreateDraftMIME = %q %v", did, err)
	}
	want := map[string][]byte{"/v1.0/me/sendMail": out, "/v1.0/me/messages": draft}
	for _, r := range f.Requests() {
		w, ok := want[r.Path]
		if !ok || r.Method != "POST" {
			continue
		}
		delete(want, r.Path)
		if ct := r.Header.Get("Content-Type"); ct != "text/plain" {
			t.Errorf("%s: Content-Type = %q", r.Path, ct)
		}
		if r.Body != base64.StdEncoding.EncodeToString(w) {
			t.Errorf("%s: body = %q", r.Path, r.Body)
		}
	}
	if len(want) != 0 {
		t.Fatalf("requests missing: %v", want)
	}
	if sent := f.Sent(); len(sent) != 1 || string(sent[0]) != string(out) {
		t.Fatalf("sent = %q", sent)
	}
	d, err := c.Get(ctx, did)
	if err != nil || d.FolderID != f.FolderID("drafts") || d.Subject != "draft" {
		t.Fatalf("draft = %+v %v", d, err)
	}
}

func TestClient_flagsCategoriesMove(t *testing.T) {
	c, f, _ := newTest(t)
	ctx := context.Background()
	id := f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "x", Categories: []string{"Travel"}})
	if err := c.SetRead(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if err := c.SetFlagged(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if err := c.SetCategories(ctx, id, []string{"Work"}); err != nil {
		t.Fatal(err)
	}
	m, _ := c.Get(ctx, id)
	if !m.IsRead || !m.Flagged || !slices.Equal(m.Categories, []string{"Work"}) {
		t.Fatalf("after set: %+v", m)
	}
	if err := c.SetFlagged(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if err := c.SetCategories(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	reqs := f.Requests()
	if last := reqs[len(reqs)-1]; last.Method != "PATCH" || last.Body != `{"categories":[]}` {
		t.Fatalf("clearing categories sent %s %s", last.Method, last.Body)
	}
	m, _ = c.Get(ctx, id)
	if m.Flagged || len(m.Categories) != 0 {
		t.Fatalf("after clear: %+v", m)
	}
	archive := f.FolderID("archive")
	nid, err := c.Move(ctx, id, archive)
	if err != nil || nid != id {
		t.Fatalf("Move = %q %v", nid, err)
	}
	if m, _ := c.Get(ctx, id); m.FolderID != archive {
		t.Fatalf("after move: %+v", m)
	}
}

func TestClient_permanentDelete(t *testing.T) {
	c, f, _ := newTest(t)
	id := f.AddMessage(f.FolderID("deleteditems"), graphtest.Message{Subject: "x"})
	ctx := context.Background()
	if err := c.PermanentDelete(ctx, id); err != nil {
		t.Fatal(err)
	}
	// As on real Graph: the message moves to the hidden Recoverable Items
	// Purges folder and can still be fetched by id, but no folder m9s
	// sees holds it any more.
	m, err := c.Get(ctx, id)
	if err != nil || m.FolderID == "" || m.FolderID != f.FolderID("recoverableitemspurges") {
		t.Fatalf("after delete: %+v %v", m, err)
	}
	fs, _ := c.Folders(ctx)
	if folderByID(fs, m.FolderID) != nil {
		t.Fatalf("Purges is listed: %+v", fs)
	}
	if ms, _ := c.List(ctx, f.FolderID("deleteditems")); len(ms) != 0 {
		t.Fatalf("still in Deleted Items: %+v", ms)
	}
}

func batchSizes(t *testing.T, f *graphtest.Fake) []int {
	t.Helper()
	var sizes []int
	for _, r := range f.Requests() {
		if r.Path != "/v1.0/$batch" {
			continue
		}
		var in struct {
			Requests []json.RawMessage `json:"requests"`
		}
		if err := json.Unmarshal([]byte(r.Body), &in); err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, len(in.Requests))
	}
	return sizes
}

func TestClient_permanentDeleteAllBatches(t *testing.T) {
	c, f, _ := newTest(t)
	trash := f.FolderID("deleteditems")
	var all []string
	for i := 0; i < 45; i++ {
		all = append(all, f.AddMessage(trash, graphtest.Message{Subject: "x"}))
	}
	if err := c.PermanentDeleteAll(context.Background(), all); err != nil {
		t.Fatal(err)
	}
	if s := batchSizes(t, f); !slices.Equal(s, []int{20, 20, 5}) {
		t.Fatalf("batches = %v", s)
	}
	purges := f.FolderID("recoverableitemspurges")
	for _, id := range all {
		if m, _ := f.Message(id); m.FolderID != purges {
			t.Fatalf("%s survived in %s", id, m.FolderID)
		}
	}
}

// Graph runs a batch's requests in parallel and throttles some of them on
// their own: only those are sent again, after their Retry-After. One already
// gone (404) counts as deleted.
func TestClient_permanentDeleteAllRetriesThrottledItems(t *testing.T) {
	c, f, sleeps := newTest(t)
	trash := f.FolderID("deleteditems")
	a := f.AddMessage(trash, graphtest.Message{Subject: "a"})
	b := f.AddMessage(trash, graphtest.Message{Subject: "b"})
	f.Queue("/v1.0/me/messages/"+b+"/permanentDelete", 429, `{"error":{"code":"ApplicationThrottled"}}`, map[string]string{"Retry-After": "2"})
	if err := c.PermanentDeleteAll(context.Background(), []string{a, b, "gone"}); err != nil {
		t.Fatal(err)
	}
	if s := batchSizes(t, f); !slices.Equal(s, []int{3, 1}) {
		t.Fatalf("batches = %v", s)
	}
	if !slices.Equal(*sleeps, []time.Duration{2 * time.Second}) {
		t.Fatalf("sleeps = %v", *sleeps)
	}
	if m, _ := f.Message(b); m.FolderID != f.FolderID("recoverableitemspurges") {
		t.Fatalf("b survived in %s", m.FolderID)
	}
}

func TestClient_permanentDeleteAllFailsOnOtherErrors(t *testing.T) {
	c, f, _ := newTest(t)
	a := f.AddMessage(f.FolderID("deleteditems"), graphtest.Message{Subject: "a"})
	f.Queue("/v1.0/me/messages/"+a+"/permanentDelete", 403, `{"error":{"code":"ErrorAccessDenied","message":"SECRET"}}`, nil)
	err := c.PermanentDeleteAll(context.Background(), []string{a})
	if err == nil || !strings.Contains(err.Error(), "ErrorAccessDenied") || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
}

func TestClient_masterCategories(t *testing.T) {
	c, f, _ := newTest(t)
	f.SetMasterCategories("Work", "Travel")
	got, err := c.MasterCategories(context.Background())
	if err != nil || !slices.Equal(got, []string{"Work", "Travel"}) {
		t.Fatalf("MasterCategories = %v %v", got, err)
	}
}

func TestClient_searchFinds(t *testing.T) {
	c, f, _ := newTest(t)
	id := f.AddMessage(f.FolderID("inbox"), graphtest.Message{Subject: "invoice 42"})
	f.AddMessage(f.FolderID("archive"), graphtest.Message{Subject: "lunch"})
	got, err := c.Search(context.Background(), "invoice")
	if err != nil || !slices.Equal(ids(got), []string{id}) {
		t.Fatalf("Search = %v %v", ids(got), err)
	}
}

// Real Graph answers a second permanentDelete with 403
// ErrorCannotDeleteObject (the message is already in Purges), not 404.
// Either way the message is gone, so it is not an error.
func TestClient_permanentDeleteTwice(t *testing.T) {
	c, f, _ := newTest(t)
	ctx := context.Background()
	id := f.AddMessage(f.FolderID("deleteditems"), graphtest.Message{Subject: "x"})
	if err := c.PermanentDelete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := c.PermanentDelete(ctx, id); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if err := c.PermanentDeleteAll(ctx, []string{id}); err != nil {
		t.Fatalf("batch delete of a purged message: %v", err)
	}
}

func TestClient_permanentDeleteOtherForbiddenFails(t *testing.T) {
	c, f, _ := newTest(t)
	id := f.AddMessage(f.FolderID("deleteditems"), graphtest.Message{Subject: "x"})
	f.Queue("/v1.0/me/messages/"+id+"/permanentDelete", 403, `{"error":{"code":"ErrorAccessDenied"}}`, nil)
	if err := c.PermanentDelete(context.Background(), id); err == nil {
		t.Fatal("access denied reported as success")
	}
}

func TestClient_folder(t *testing.T) {
	c, f, _ := newTest(t)
	inbox := f.FolderID("inbox")
	f.AddMessage(inbox, graphtest.Message{Subject: "a"})
	f.AddMessage(inbox, graphtest.Message{Subject: "b", IsRead: true})
	got, err := c.Folder(context.Background(), inbox)
	if err != nil || got.ID != inbox || got.Total != 2 || got.Unread != 1 || got.DisplayName != "Inbox" {
		t.Fatalf("Folder = %+v %v", got, err)
	}
	if _, err := c.Folder(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing folder: %v", err)
	}
}
