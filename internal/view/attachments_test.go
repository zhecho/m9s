package view

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/zhecho/m9s/internal/mail"
)

func TestSafeName(t *testing.T) {
	long := strings.Repeat("я", 150) + ".pdf" // 300 bytes before the extension
	for _, c := range []struct {
		name, ct string
		n        int
		want     string
	}{
		{"../../.ssh/authorized_keys", "", 1, "authorized_keys"},
		{`..\evil.exe`, "", 1, "evil.exe"},
		{"a\x00b.pdf", "", 1, "ab.pdf"},
		{"Фактура №5.pdf", "", 1, "Фактура №5.pdf"},
		{"", "application/pdf", 2, "attachment-2.pdf"},
		{"..", "", 1, "attachment-1"},
		{"invoice\u202Efdp.exe", "", 1, "invoicefdp.exe"}, // right-to-left override: shows as "invoiceexe.pdf"
		{"a\u200Bb\uFEFF.pdf", "", 1, "ab.pdf"},           // invisible format characters
		{".bashrc", "", 1, "bashrc"},                      // no hidden files
		{"...", "", 3, "attachment-3"},
		{"Setup.app. ", "", 1, "Setup.app"}, // trailing dots and spaces hide the real type
	} {
		if got := safeName(&mail.Attachment{Filename: c.name, ContentType: c.ct}, c.n); got != c.want {
			t.Errorf("safeName(%q, %q) = %q, want %q", c.name, c.ct, got, c.want)
		}
	}
	got := safeName(&mail.Attachment{Filename: long}, 1)
	if len(got) > 255 || !utf8.ValidString(got) || !strings.HasSuffix(got, ".pdf") {
		t.Errorf("long name -> %d bytes, valid %v, %q", len(got), utf8.ValidString(got), got[len(got)-8:])
	}
}

func touch(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "report.pdf"), "x")
	if got := filepath.Base(uniquePath(dir, "report.pdf")); got != "report (1).pdf" {
		t.Errorf("got %q, want report (1).pdf", got)
	}
	touch(t, filepath.Join(dir, "report (1).pdf"), "x")
	if got := filepath.Base(uniquePath(dir, "report.pdf")); got != "report (2).pdf" {
		t.Errorf("got %q, want report (2).pdf", got)
	}
	touch(t, filepath.Join(dir, "noext"), "x")
	if got := filepath.Base(uniquePath(dir, "noext")); got != "noext (1)" {
		t.Errorf("got %q, want noext (1)", got)
	}
}

func TestRunnable(t *testing.T) {
	for name, want := range map[string]bool{"x.SH": true, "Setup.app": true, "run.command": true, "a.pdf": false, "README": false,
		"go.webloc": true, "f.fileloc": true, "i.inetloc": true, "s.url": true, "l.desktop": true, "j.jnlp": true,
		"m.mpkg": true, "d.scptd": true, "t.tool": true, "a.action": true, "p.prefpane": true, "o.osax": true,
		"c.mobileconfig": true, "s.shortcut": true, "photo.jpg": false} {
		if got := runnable(name); got != want {
			t.Errorf("runnable(%q) = %v, want %v", name, got, want)
		}
	}
}

func file(name, body string) *mail.Attachment {
	return &mail.Attachment{Filename: name, ContentType: "application/octet-stream", Data: []byte(body)}
}

func TestSaveFiles_neverOverwrites(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "report.pdf"), "mine")
	saved, err := saveFiles(dir, []*mail.Attachment{file("report.pdf", "theirs")})
	if err != nil || len(saved) != 1 || saved[0] != "report (1).pdf" {
		t.Fatalf("saved %v, %v", saved, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "report.pdf")); string(b) != "mine" {
		t.Errorf("the owner's file now holds %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "report (1).pdf")); string(b) != "theirs" {
		t.Errorf("saved file holds %q", b)
	}
}

func TestSaveFiles_sameNameTwice(t *testing.T) {
	dir := t.TempDir()
	saved, err := saveFiles(dir, []*mail.Attachment{file("image.png", "1"), file("image.png", "2")})
	if err != nil || strings.Join(saved, "|") != "image.png|image (1).png" {
		t.Errorf("saved %v, %v", saved, err)
	}
}

func TestSaveFiles_createsMissingFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "sub")
	if _, err := saveFiles(dir, []*mail.Attachment{file("a.txt", "x")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err != nil {
		t.Error(err)
	}
}

func TestSaveFiles_dirIsAFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "notadir")
	touch(t, dir, "x")
	if _, err := saveFiles(dir, []*mail.Attachment{file("a.txt", "x")}); err == nil {
		t.Error("saving into a file did not fail")
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	for in, want := range map[string]string{"~": home, "~/Downloads": filepath.Join(home, "Downloads"), "/tmp/x": "/tmp/x", "~bob/x": "~bob/x"} {
		if got := expandHome(in); got != want {
			t.Errorf("expandHome(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int]string{38: "38 B", 217088: "212 KB", 1468006: "1.4 MB", 5120: "5.0 KB"} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}
