package view

import (
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/ui"
)

// maxNameBytes is the longest file name macOS and Linux file systems take.
const maxNameBytes = 255

// preferredExt overrides mime's alphabetical first pick where it is odd
// (image/jpeg would become .jfif).
var preferredExt = map[string]string{"image/jpeg": ".jpg", "text/plain": ".txt"}

// safeName is the file name an attachment is saved under: only the last path
// element of what the sender wrote, without control or format characters or
// leading dots, at most 255
// bytes; a part without a usable name becomes attachment-<n> plus an
// extension from its content type.
func safeName(a *mail.Attachment, n int) string {
	name := strings.ReplaceAll(a.Filename, `\`, "/")
	name = name[strings.LastIndex(name, "/")+1:]
	name = strings.Map(func(r rune) rune {
		// Control and format characters (right-to-left overrides, zero-width
		// marks) can make "invoice<U+202E>fdp.exe" look like "invoiceexe.pdf".
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, name)
	// No hidden files; no trailing dots or spaces hiding the real type.
	name = strings.TrimRight(strings.TrimLeft(strings.TrimSpace(name), "."), ". ")
	if name == "" {
		name = fmt.Sprintf("attachment-%d%s", n, extensionFor(a.ContentType))
	}
	if len(name) <= maxNameBytes {
		return name
	}
	ext := filepath.Ext(name)
	if len(ext) > 16 {
		ext = ""
	}
	stem := name[:len(name)-len(ext)]
	cut := maxNameBytes - len(ext)
	for cut > 0 && !utf8.RuneStart(stem[cut]) {
		cut--
	}
	return stem[:cut] + ext
}

func extensionFor(contentType string) string {
	ct, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	if ext, ok := preferredExt[ct]; ok {
		return ext
	}
	if exts, _ := mime.ExtensionsByType(ct); len(exts) > 0 {
		return exts[0]
	}
	return ""
}

// uniquePath is dir/name, or dir/"stem (n).ext" for the first n that is
// free: a file the owner already has is never replaced.
func uniquePath(dir, name string) string {
	p := filepath.Join(dir, name)
	if _, err := os.Lstat(p); os.IsNotExist(err) {
		return p
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			return p
		}
	}
}

// runnableExts are file types that run code when opened.
var runnableExts = map[string]bool{
	".app": true, ".command": true, ".sh": true, ".bash": true, ".zsh": true,
	".pkg": true, ".dmg": true, ".exe": true, ".bat": true, ".cmd": true,
	".jar": true, ".scpt": true, ".applescript": true, ".workflow": true,
	".terminal": true, ".py": true, ".rb": true, ".pl": true,
	// Not programs, but opening them launches something or installs it.
	".webloc": true, ".fileloc": true, ".inetloc": true, ".url": true,
	".desktop": true, ".jnlp": true, ".mpkg": true, ".scptd": true,
	".tool": true, ".action": true, ".prefpane": true, ".osax": true,
	".mobileconfig": true, ".shortcut": true,
}

// runnable reports whether opening name could run a program. m9s writes the
// file itself, so macOS would not warn first.
func runnable(name string) bool {
	return runnableExts[strings.ToLower(filepath.Ext(name))]
}

// saveFiles writes files into dir, creating it if needed, marked as
// downloaded (see markDownloaded), and returns the names they were saved
// under. It never replaces an existing file and stops
// at the first error, removing a file it could not finish.
func saveFiles(dir string, files []*mail.Attachment) ([]string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var saved []string
	for i, a := range files {
		p := uniquePath(dir, safeName(a, i+1))
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return saved, err
		}
		if err := markDownloaded(f); err != nil { // e.g. a FAT drive: keep the file
			ui.AppLog.Warn("%s saved without the downloaded-file mark: %v", filepath.Base(p), err)
		}
		_, err = f.Write(a.Data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(p)
			return saved, err
		}
		saved = append(saved, filepath.Base(p))
	}
	return saved, nil
}

// expandHome turns a leading ~ or ~/ into the home folder.
func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
}

// humanSize is n bytes as people read it: 38 B, 5.0 KB, 212 KB, 1.4 MB.
func humanSize(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v, unit := float64(n)/1024, "KB"
	if v >= 1024 {
		v, unit = v/1024, "MB"
	}
	if v >= 1024 {
		v, unit = v/1024, "GB"
	}
	if v < 10 {
		return fmt.Sprintf("%.1f %s", v, unit)
	}
	return fmt.Sprintf("%.0f %s", v, unit)
}
