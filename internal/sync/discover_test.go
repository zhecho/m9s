// internal/sync/discover_test.go
package sync_test

import (
	"testing"

	"github.com/zhecho/m9s/internal/mail"
	msync "github.com/zhecho/m9s/internal/sync"
)

func folders(names ...string) []*mail.Folder {
	fs := make([]*mail.Folder, len(names))
	for i, n := range names {
		fs[i] = &mail.Folder{Name: n, Delimiter: "/"}
	}
	return fs
}

func foldersWithAttr(name, attr string) *mail.Folder {
	return &mail.Folder{Name: name, Delimiter: "/", Attributes: []string{attr}}
}

func TestAutoDetect_inbox(t *testing.T) {
	all := folders("INBOX", "Random")
	got := msync.AutoDetect(all)
	if len(got) != 1 || got[0].Name != "INBOX" {
		t.Errorf("want [INBOX], got %v", folderNames(got))
	}
}

func TestAutoDetect_nameHeuristics(t *testing.T) {
	all := folders("INBOX", "Sent", "Drafts", "Trash", "Spam", "Work")
	got := msync.AutoDetect(all)
	names := folderNames(got)
	for _, want := range []string{"INBOX", "Sent", "Drafts", "Trash", "Spam"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("want %q in auto-detected folders, got %v", want, names)
		}
	}
	for _, notWant := range []string{"Work"} {
		for _, n := range names {
			if n == notWant {
				t.Errorf("did not want %q in auto-detected folders, got %v", notWant, names)
			}
		}
	}
}

func TestAutoDetect_specialUseAttrs(t *testing.T) {
	all := []*mail.Folder{
		{Name: "INBOX", Delimiter: "/"},
		foldersWithAttr("MySent", `\Sent`),
		foldersWithAttr("MyDrafts", `\Drafts`),
	}
	got := msync.AutoDetect(all)
	names := folderNames(got)
	for _, want := range []string{"INBOX", "MySent", "MyDrafts"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("want %q in auto-detected, got %v", want, names)
		}
	}
}

func TestAutoDetect_deduplicates(t *testing.T) {
	// "Sent" matches both name heuristic and \Sent attribute — should appear once
	all := []*mail.Folder{
		{Name: "INBOX", Delimiter: "/"},
		{Name: "Sent", Delimiter: "/", Attributes: []string{`\Sent`}},
	}
	got := msync.AutoDetect(all)
	count := 0
	for _, f := range got {
		if f.Name == "Sent" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("Sent appears %d times, want 1", count)
	}
}

func folderNames(fs []*mail.Folder) []string {
	names := make([]string, len(fs))
	for i, f := range fs {
		names[i] = f.Name
	}
	return names
}

func TestSentFolder(t *testing.T) {
	if f := msync.SentFolder(folders("INBOX", "Sent Items", "Trash")); f == nil || f.Name != "Sent Items" {
		t.Fatalf("name heuristic: got %v, want Sent Items", f)
	}
	attr := append(folders("INBOX", "Sent"), foldersWithAttr("Gesendet", `\Sent`))
	if f := msync.SentFolder(attr); f == nil || f.Name != "Gesendet" {
		t.Fatalf(`\Sent attribute must win: got %v`, f)
	}
	if f := msync.SentFolder(folders("INBOX", "Drafts")); f != nil {
		t.Fatalf("no sent folder: got %v", f.Name)
	}
}
