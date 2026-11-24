package draft_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail/smtp"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
)

func TestStore_saveAndLoad(t *testing.T) {
	dir := t.TempDir()
	s := draft.NewStore(dir)

	d := smtp.Draft{
		From:    "me@example.com",
		To:      []string{"you@example.com"},
		Subject: "Test draft",
		Body:    "Hello",
	}
	id, err := s.Save("acct1", d)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if id == "" {
		t.Fatal("Save returned empty id")
	}

	loaded, err := s.Load("acct1", id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Subject != "Test draft" {
		t.Errorf("want Subject='Test draft', got %q", loaded.Subject)
	}
	if loaded.Body != "Hello" {
		t.Errorf("want Body='Hello', got %q", loaded.Body)
	}
}

func TestStore_list(t *testing.T) {
	dir := t.TempDir()
	s := draft.NewStore(dir)

	for i := 0; i < 3; i++ {
		_, err := s.Save("acct1", smtp.Draft{Subject: "draft"})
		if err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	metas, err := s.List("acct1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 3 {
		t.Errorf("want 3 drafts, got %d", len(metas))
	}
}

func TestStore_delete(t *testing.T) {
	dir := t.TempDir()
	s := draft.NewStore(dir)

	id, _ := s.Save("acct1", smtp.Draft{Subject: "to delete"})
	if err := s.Delete("acct1", id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	metas, _ := s.List("acct1")
	for _, m := range metas {
		if m.ID == id {
			t.Error("draft still present after Delete")
		}
	}
}

func TestStore_markSynced(t *testing.T) {
	dir := t.TempDir()
	s := draft.NewStore(dir)

	id, _ := s.Save("acct1", smtp.Draft{Subject: "sync me"})
	if err := s.MarkSynced("acct1", id, "42"); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	metas, _ := s.List("acct1")
	var found *draft.Meta
	for i := range metas {
		if metas[i].ID == id {
			found = &metas[i]
			break
		}
	}
	if found == nil {
		t.Fatal("draft not found after MarkSynced")
	}
	if found.SyncedID != "42" {
		t.Errorf("want SyncedID=42, got %q", found.SyncedID)
	}
	if found.Dirty {
		t.Error("want Dirty=false after MarkSynced")
	}
}

func TestStore_markDirty(t *testing.T) {
	dir := t.TempDir()
	s := draft.NewStore(dir)

	id, _ := s.Save("acct1", smtp.Draft{Subject: "dirty me"})
	// MarkSynced first so Dirty is false.
	if err := s.MarkSynced("acct1", id, "99"); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	if err := s.MarkDirty("acct1", id); err != nil {
		t.Fatalf("MarkDirty: %v", err)
	}

	metas, _ := s.List("acct1")
	var found *draft.Meta
	for i := range metas {
		if metas[i].ID == id {
			found = &metas[i]
			break
		}
	}
	if found == nil {
		t.Fatal("draft not found after MarkDirty")
	}
	if !found.Dirty {
		t.Error("want Dirty=true after MarkDirty")
	}
}

func TestStore_load_notFound(t *testing.T) {
	dir := t.TempDir()
	s := draft.NewStore(dir)
	_, err := s.Load("acct1", "nonexistent-id")
	if err == nil {
		t.Error("expected error loading nonexistent draft")
	}
}

func TestStore_markDirty_notFound(t *testing.T) {
	dir := t.TempDir()
	s := draft.NewStore(dir)
	err := s.MarkDirty("acct1", "nonexistent-id")
	if err == nil {
		t.Error("expected error on MarkDirty for nonexistent id")
	}
}

func TestStore_markSynced_notFound(t *testing.T) {
	dir := t.TempDir()
	s := draft.NewStore(dir)
	err := s.MarkSynced("acct1", "nonexistent-id", "1")
	if err == nil {
		t.Error("expected error on MarkSynced for nonexistent id")
	}
}

func TestStore_emptyAccount(t *testing.T) {
	dir := t.TempDir()
	s := draft.NewStore(dir)

	metas, err := s.List("no-such-account")
	if err != nil {
		t.Fatalf("List on empty account: %v", err)
	}
	if len(metas) != 0 {
		t.Errorf("want 0 drafts, got %d", len(metas))
	}
}

func TestStore_UpdateReplacesContentAndForgetsServerCopy(t *testing.T) {
	s := draft.NewStore(t.TempDir())
	id, err := s.Save("acct", smtpadapter.Draft{Subject: "first", Body: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSynced("acct", id, "7"); err != nil {
		t.Fatal(err)
	}

	prev, err := s.Update("acct", id, smtpadapter.Draft{Subject: "second", Body: "two"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if prev != "7" {
		t.Errorf("Update returned previous server UID %q, want 7", prev)
	}
	d, err := s.Load("acct", id)
	if err != nil || d.Subject != "second" || !strings.Contains(d.Body, "two") {
		t.Errorf("Load after Update = %+v, %v", d, err)
	}
	metas, _ := s.List("acct")
	if len(metas) != 1 || metas[0].ID != id || metas[0].Subject != "second" || !metas[0].Dirty || metas[0].SyncedID != "" {
		t.Errorf("meta after Update = %+v; want same id, new subject, dirty, no server copy", metas)
	}
}

func TestStore_UpdateUnknownDraft(t *testing.T) {
	s := draft.NewStore(t.TempDir())
	if _, err := s.Update("acct", "nope", smtpadapter.Draft{Body: "x"}); err == nil {
		t.Error("Update of an unknown draft must fail")
	}
}

func TestStore_SetLocalOnly(t *testing.T) {
	s := draft.NewStore(t.TempDir())
	id, err := s.Save("acct", smtpadapter.Draft{Subject: "mine", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetLocalOnly("acct", id, true); err != nil {
		t.Fatalf("SetLocalOnly: %v", err)
	}
	metas, _ := s.List("acct")
	if len(metas) != 1 || !metas[0].LocalOnly {
		t.Fatalf("meta = %+v, want LocalOnly", metas)
	}
	if err := s.SetLocalOnly("acct", id, false); err != nil {
		t.Fatal(err)
	}
	if metas, _ = s.List("acct"); metas[0].LocalOnly {
		t.Error("LocalOnly not cleared")
	}
}

func TestLegacySyncedUIDRead(t *testing.T) {
	for in, want := range map[string]string{`{"id":"a","synced_uid":42}`: "42", `{"id":"a","synced_uid":0}`: "", `{"id":"a","synced_id":"r-9","synced_uid":5}`: "r-9", `{"id":"a","synced_id":"gd1"}`: "gd1"} {
		var m draft.Meta
		if err := json.Unmarshal([]byte(in), &m); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if m.SyncedID != want {
			t.Errorf("%s: SyncedID=%q want %q", in, m.SyncedID, want)
		}
	}
}

func TestMarkSyncedStoresText(t *testing.T) {
	dir := t.TempDir()
	s := draft.NewStore(dir)
	id, _ := s.Save("acct", smtpadapter.Draft{Subject: "x", Body: "b"})
	if err := s.MarkSynced("acct", id, "r-123"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "acct", id+".meta.json"))
	if !strings.Contains(string(raw), `"synced_id":"r-123"`) || strings.Contains(string(raw), "synced_uid") {
		t.Errorf("meta = %s", raw)
	}
}
