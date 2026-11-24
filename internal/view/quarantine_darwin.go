//go:build darwin

package view

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// markDownloaded gives f the quarantine mark browsers give downloads, so
// macOS checks it (Gatekeeper) before anything in it runs — also for types
// m9s's own list does not know, and for apps inside a saved archive. It is
// set on the open file, before any data is written, so the file never
// exists unmarked. The user-approved bit (0x40) is never set.
func markDownloaded(f *os.File) error {
	value := fmt.Sprintf("0081;%x;m9s;", time.Now().Unix())
	return unix.Fsetxattr(int(f.Fd()), "com.apple.quarantine", []byte(value), 0)
}
