package ui

import "testing"

// A status message goes to the log as SetLogText words it; the bar shows
// it as it is. App-wide messages are logged as they are.
func TestStatusBar_logText(t *testing.T) {
	saved := AppLog
	AppLog = &Logger{}
	t.Cleanup(func() { AppLog = saved })
	skin, _ := LoadSkin("")
	sb := NewStatusBar(skin, nil)
	sb.SetLogText(func(msg string) string { return "[work] " + msg })
	sb.Info("Opened in browser")
	if sb.FlashText() != "Opened in browser" {
		t.Errorf("bar shows %q", sb.FlashText())
	}
	sb.Error("open attachment: boom")
	sb.InfoApp("config unchanged")
	sb.ErrorApp("no editor")
	var got []string
	for _, e := range AppLog.Entries() {
		got = append(got, e.Msg)
	}
	want := []string{"[work] Opened in browser", "[work] open attachment: boom", "config unchanged", "no editor"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("log = %q, want %q", got, want)
		}
	}
}
