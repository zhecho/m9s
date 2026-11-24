package keepassxc

import (
	"os"
	"path/filepath"
)

const socketName = "org.keepassxc.KeePassXC.BrowserServer"

// SocketPath returns KeePassXC's browser-integration socket. It exists only
// while KeePassXC runs with "Enable browser integration" ticked; otherwise
// the error is ErrUnavailable.
func SocketPath() (string, error) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	dirs := []string{os.Getenv("TMPDIR"), os.TempDir(), runtime}
	if runtime != "" {
		dirs = append(dirs, filepath.Join(runtime, "app", "org.keepassxc.KeePassXC"))
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		path := filepath.Join(dir, socketName)
		if st, err := os.Stat(path); err == nil && st.Mode()&os.ModeSocket != 0 {
			return path, nil
		}
	}
	return "", ErrUnavailable
}
