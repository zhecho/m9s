//go:build darwin

package view

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/mail"
	"golang.org/x/sys/unix"
)

func quarantineOf(t *testing.T, path string) string {
	t.Helper()
	buf := make([]byte, 256)
	n, err := unix.Getxattr(path, "com.apple.quarantine", buf)
	if err != nil {
		t.Fatalf("%s has no quarantine mark: %v", path, err)
	}
	return string(buf[:n])
}

func TestSaveFiles_marksDownloaded(t *testing.T) {
	dir := t.TempDir()
	if _, err := saveFiles(dir, []*mail.Attachment{file("x.command", "#!/bin/sh")}); err != nil {
		t.Fatal(err)
	}
	if q := quarantineOf(t, filepath.Join(dir, "x.command")); !strings.HasPrefix(q, "0081;") || !strings.Contains(q, ";m9s;") {
		t.Errorf("quarantine = %q", q)
	}
}

func TestWriteFile_marksDownloaded(t *testing.T) {
	bf := &browserFiles{base: t.TempDir()}
	defer bf.cleanup()
	p, err := bf.writeFile("a.pdf", []byte("PDF"))
	if err != nil {
		t.Fatal(err)
	}
	if q := quarantineOf(t, p); !strings.HasPrefix(q, "0081;") {
		t.Errorf("quarantine = %q", q)
	}
}
