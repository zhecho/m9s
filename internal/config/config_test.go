package config_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/config"
)

const sampleConfig = `
accounts:
  - name: gmail
    imap:
      host: imap.gmail.com
      port: 993
      tls: true
      user: me@gmail.com
      password_cmd: "echo secret"
    smtp:
      host: smtp.gmail.com
      port: 587
      starttls: true
      user: me@gmail.com
      password_cmd: "echo secret"
ui:
  skin: solarized-dark
  preview_ratio: 40
`

func TestLoad(t *testing.T) {
	f := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(f, []byte(sampleConfig), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(f)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Accounts) != 1 {
		t.Fatalf("want 1 account, got %d", len(cfg.Accounts))
	}
	if cfg.Accounts[0].Name != "gmail" {
		t.Errorf("want name=gmail, got %q", cfg.Accounts[0].Name)
	}
	if cfg.UI.PreviewRatio != 40 {
		t.Errorf("want preview_ratio=40, got %d", cfg.UI.PreviewRatio)
	}
}

func TestLoadDefaults(t *testing.T) {
	f := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(f, []byte("accounts: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(f)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UI.MailListRows != 10 {
		t.Errorf("want default mail_list_rows=10, got %d", cfg.UI.MailListRows)
	}
	if cfg.UI.PreviewRatio != 0 {
		t.Errorf("preview_ratio is deprecated and gets no default, got %d", cfg.UI.PreviewRatio)
	}
	if cfg.UI.DateFormat != "Jan 02 15:04" {
		t.Errorf("want default date_format, got %q", cfg.UI.DateFormat)
	}
}

func TestDuration_unmarshalYAML_invalid(t *testing.T) {
	_, err := config.LoadFromBytes([]byte(`
accounts:
  - name: test
    imap: {host: h, port: 993}
    folders:
      badge_poll: "notaduration"
`))
	if err == nil {
		t.Error("expected error on invalid duration")
	}
}

func TestLoad_notFound(t *testing.T) {
	_, err := config.Load("/no/such/file.yaml")
	if err == nil {
		t.Error("expected error loading nonexistent file")
	}
}

func TestLoadFromBytes_invalid(t *testing.T) {
	_, err := config.LoadFromBytes([]byte("{\ninvalid: [yaml"))
	if err == nil {
		t.Error("expected error on invalid YAML")
	}
}

func TestUIConfig_themeAndColors(t *testing.T) {
	cfg, err := config.LoadFromBytes([]byte("ui:\n  theme: dracula\n  colors:\n    accent: \"#ff79c6\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.ThemeName() != "dracula" || cfg.UI.Colors["accent"] != "#ff79c6" {
		t.Errorf("got theme %q colors %v", cfg.UI.ThemeName(), cfg.UI.Colors)
	}
}

func TestUIConfig_skinIsThemeAlias(t *testing.T) {
	cfg, _ := config.LoadFromBytes([]byte("ui:\n  skin: nord\n"))
	if cfg.UI.ThemeName() != "nord" {
		t.Errorf("want skin: to act as theme, got %q", cfg.UI.ThemeName())
	}
	cfg, _ = config.LoadFromBytes([]byte("ui:\n  skin: nord\n  theme: dracula\n"))
	if cfg.UI.ThemeName() != "dracula" {
		t.Errorf("theme: should win over skin:, got %q", cfg.UI.ThemeName())
	}
}

func TestLoadMailListRows(t *testing.T) {
	cfg, err := config.LoadFromBytes([]byte("ui:\n  mail_list_rows: 15\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.MailListRows != 15 {
		t.Errorf("mail_list_rows = %d, want 15", cfg.UI.MailListRows)
	}
}

func TestLayoutState_roundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "ui.json")
	if err := config.SaveLayoutState(path, config.LayoutState{MailListRows: 17}); err != nil {
		t.Fatalf("SaveLayoutState: %v", err)
	}
	st := config.LoadLayoutState(path)
	if st.MailListRows != 17 {
		t.Errorf("loaded %+v, want MailListRows 17", st)
	}
}

func TestLayoutState_missingOrBrokenFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if st := config.LoadLayoutState(filepath.Join(dir, "nope.json")); st.MailListRows != 0 {
		t.Errorf("missing file gave %+v", st)
	}
	broken := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(broken, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if st := config.LoadLayoutState(broken); st.MailListRows != 0 {
		t.Errorf("broken file gave %+v; it must be ignored, not fatal", st)
	}
}

const keepassAccountYAML = `
accounts:
  - name: a
    keepassxc: true
    imap: {host: mail.x.io, user: "%s"}
    smtp: {host: mail.x.io, user: "%s"}
`

func TestLoad_keepassxcTrue(t *testing.T) {
	cfg, err := config.LoadFromBytes([]byte(fmt.Sprintf(keepassAccountYAML, "a@x.io", "a@x.io")))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Accounts[0].KeePassXC {
		t.Error("keepassxc: true was not read")
	}
	plain, _ := config.LoadFromBytes([]byte("accounts:\n  - name: b\n"))
	if plain.Accounts[0].KeePassXC {
		t.Error("keepassxc must be off unless asked for")
	}
}

func TestLoad_keepassxcNeedsUser(t *testing.T) {
	for _, tc := range []struct{ imap, smtp, want string }{
		{"", "a@x.io", "imap.user"},
		{"a@x.io", "  ", "smtp.user"},
	} {
		_, err := config.LoadFromBytes([]byte(fmt.Sprintf(keepassAccountYAML, tc.imap, tc.smtp)))
		if err == nil || !strings.Contains(err.Error(), `account "a"`) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("imap %q smtp %q: err = %v, want it to name account \"a\" and %s", tc.imap, tc.smtp, err, tc.want)
		}
	}
}

func TestRunPasswordCmd_errorHidesCommandAndOutput(t *testing.T) {
	_, err := config.RunPasswordCmd(context.Background(), "echo s3cret; echo s3cret >&2; exit 3")
	if err == nil {
		t.Fatal("a failing command must be an error")
	}
	if msg := err.Error(); strings.Contains(msg, "s3cret") || strings.Contains(msg, "echo") || !strings.Contains(msg, "exit status 3") {
		t.Errorf("error = %q; want only the exit status, no command text or output", msg)
	}
}

func TestRunPasswordCmd_trimsOnlyTheTrailingNewline(t *testing.T) {
	got, err := config.RunPasswordCmd(context.Background(), `printf ' p w \r\n'`)
	if err != nil || got != " p w " {
		t.Errorf("got %q, %v; want ' p w '", got, err)
	}
}

func TestEnvKey(t *testing.T) {
	if got := config.EnvKey("my work-1"); got != "MY_WORK_1" {
		t.Errorf("EnvKey = %q", got)
	}
}

func TestRunPasswordCmd_stopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := config.RunPasswordCmd(ctx, "sleep 5")
	if err == nil || err.Error() != "timed out" {
		t.Errorf("err = %v, want 'timed out'", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("a hanging command held the lookup for %s", time.Since(start))
	}
}

const gmailAcct = `
accounts:
  - name: g
    provider: gmail
    keepassxc: true
    gmail:
      user: me@gmail.com
      client_id: cid
`

func TestProviderDefaultsToIMAP(t *testing.T) {
	cfg, err := config.LoadFromBytes([]byte(sampleConfig))
	if err != nil {
		t.Fatal(err)
	}
	a := &cfg.Accounts[0]
	if a.ProviderName() != "imap" || a.IsGmail() {
		t.Fatalf("provider = %q", a.ProviderName())
	}
}

func TestGmailAccountNeedsUserAndClientID(t *testing.T) {
	_, err := config.LoadFromBytes([]byte("accounts:\n  - name: g\n    provider: gmail\n    keepassxc: true\n    gmail:\n      user: me@gmail.com\n"))
	if err == nil || !strings.Contains(err.Error(), "needs gmail.client_id") {
		t.Fatalf("err = %v", err)
	}
	_, err = config.LoadFromBytes([]byte("accounts:\n  - name: g\n    provider: gmail\n    keepassxc: true\n    gmail:\n      client_id: cid\n"))
	if err == nil || !strings.Contains(err.Error(), "needs gmail.user") {
		t.Fatalf("err = %v", err)
	}
}

func TestGmailAccountNeedsKeePassXC(t *testing.T) {
	_, err := config.LoadFromBytes([]byte("accounts:\n  - name: g\n    provider: gmail\n    gmail:\n      user: me@gmail.com\n      client_id: cid\n"))
	if err == nil || !strings.Contains(err.Error(), "gmail accounts need keepassxc: true") {
		t.Fatalf("err = %v", err)
	}
}

func TestGmailAccountIgnoresIMAPBlocks(t *testing.T) {
	cfg, err := config.LoadFromBytes([]byte(gmailAcct + "    imap:\n      host: imap.gmail.com\n    smtp:\n      host: smtp.gmail.com\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Accounts[0].IsGmail() {
		t.Fatal("not gmail")
	}
}

func TestGmailBadgePollDefault30s(t *testing.T) {
	cfg, err := config.LoadFromBytes([]byte(gmailAcct))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Accounts[0].Folders.BadgePoll.D(); got != 30*time.Second {
		t.Fatalf("gmail badge poll = %v", got)
	}
	cfg, err = config.LoadFromBytes([]byte(sampleConfig))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Accounts[0].Folders.BadgePoll.D(); got != 60*time.Second {
		t.Fatalf("imap badge poll = %v", got)
	}
	cfg, err = config.LoadFromBytes([]byte(gmailAcct + "    folders:\n      badge_poll: 90s\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Accounts[0].Folders.BadgePoll.D(); got != 90*time.Second {
		t.Fatalf("explicit badge poll = %v", got)
	}
}

func TestUnknownProviderRejected(t *testing.T) {
	_, err := config.LoadFromBytes([]byte("accounts:\n  - name: g\n    provider: pop3\n"))
	if err == nil || !strings.Contains(err.Error(), `unknown provider "pop3"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidate_microsoft(t *testing.T) {
	block := func(kp, user, cid string) string {
		return "accounts:\n  - name: outlook\n    provider: microsoft\n    keepassxc: " + kp +
			"\n    microsoft:\n      user: " + user + "\n      client_id: " + cid + "\n"
	}
	for _, c := range []struct{ yaml, want string }{
		{block("false", "me@outlook.com", "cid"), `account "outlook": microsoft accounts need keepassxc: true`},
		{block("true", `""`, "cid"), `account "outlook": provider: microsoft needs microsoft.user`},
		{block("true", "me@outlook.com", `""`), `account "outlook": provider: microsoft needs microsoft.client_id`},
	} {
		if _, err := config.LoadFromBytes([]byte(c.yaml)); err == nil || err.Error() != c.want {
			t.Errorf("err = %v, want %q", err, c.want)
		}
	}
	cfg, err := config.LoadFromBytes([]byte(block("true", "me@outlook.com", "00000000-0000-0000-0000-000000000000")))
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Accounts[0]
	if !a.IsMicrosoft() || a.IsGmail() || a.ProviderName() != config.ProviderMicrosoft ||
		a.Microsoft.User != "me@outlook.com" || a.Microsoft.ClientID != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("account = %+v", a)
	}
	if a.Folders.BadgePoll.D() != 30*time.Second {
		t.Errorf("BadgePoll = %v, want 30s", a.Folders.BadgePoll.D())
	}
}

func TestEnable_disabledAccountsAreLeftOut(t *testing.T) {
	cfg, err := config.LoadFromBytes([]byte(`
accounts:
  - name: plain
    imap: {host: h, user: a@x.io}
  - name: off
    enable: false
    provider: microsoft          # incomplete: no microsoft block
  - name: on
    enable: true
    imap: {host: h, user: b@x.io}
`))
	if err != nil {
		t.Fatalf("a disabled account's settings must not be checked: %v", err)
	}
	var names []string
	for _, a := range cfg.Accounts {
		names = append(names, a.Name)
	}
	if strings.Join(names, ",") != "plain,on" {
		t.Errorf("accounts = %v, want plain,on", names)
	}
	if strings.Join(cfg.Disabled, ",") != "off" {
		t.Errorf("Disabled = %v, want off", cfg.Disabled)
	}
	if !cfg.Accounts[0].Enabled() || !cfg.Accounts[1].Enabled() {
		t.Error("enable omitted or true must mean enabled")
	}
}

func TestEnable_everyAccountDisabled(t *testing.T) {
	cfg, err := config.LoadFromBytes([]byte("accounts:\n  - name: a\n    enable: false\n  - name: b\n    enable: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Accounts) != 0 || strings.Join(cfg.Disabled, ",") != "a,b" {
		t.Errorf("accounts %v, disabled %v", cfg.Accounts, cfg.Disabled)
	}
}
