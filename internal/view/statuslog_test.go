package view

import (
	"testing"

	"github.com/zhecho/m9s/internal/ui"
)

// Status messages reach the log with the account they are about in front,
// as the engines' own lines have it.
func TestStatusLog_accountInFront(t *testing.T) {
	saved := ui.AppLog
	ui.AppLog = &ui.Logger{}
	t.Cleanup(func() { ui.AppLog = saved })
	ra := newWiredRootApp(t, "zhechev", "work")
	for _, msg := range []string{
		"Opened in browser",           // the open account
		"Saved 65.pdf to ~/Downloads", // the open account
		"work: reconnected",           // names another account
		"zhechev: gmail: http 403 PERMISSION_DENIED",
		"[keepassxc] locked", // already tagged
	} {
		ra.uiApp.StatusBar.Info(msg)
	}
	ra.uiApp.StatusBar.InfoApp("config unchanged") // about no account
	want := []string{
		"[zhechev] Opened in browser",
		"[zhechev] Saved 65.pdf to ~/Downloads",
		"[work] reconnected",
		"[zhechev] gmail: http 403 PERMISSION_DENIED",
		"[keepassxc] locked",
		"config unchanged",
	}
	var got []string
	for _, e := range ui.AppLog.Entries() {
		got = append(got, e.Msg)
	}
	if len(got) < len(want) {
		t.Fatalf("log = %q", got)
	}
	got = got[len(got)-len(want):]
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}
