package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/creds"
	"github.com/zhecho/m9s/internal/ui"
	"github.com/zhecho/m9s/internal/view"
)

func cfgWith(names ...string) *config.Config {
	cfg := &config.Config{}
	for _, n := range names {
		cfg.Accounts = append(cfg.Accounts, config.Account{Name: n})
	}
	return cfg
}

func found(name string) creds.Result {
	return creds.Result{Account: name,
		IMAP: creds.Secret{Password: "imap-" + name, Source: creds.KeePass, OK: true},
		SMTP: creds.Secret{Password: "smtp-" + name, Source: creds.KeePass, OK: true}}
}

// scriptedStart is startDeps whose window returns what the lookup found,
// with the outcome the real grid would give.
func scriptedStart(results []creds.Result, quit bool) (startDeps, *int) {
	shown := 0
	return startDeps{
		run: func(context.Context, []config.Account, func(creds.Update), func(string)) ([]creds.Result, bool) {
			return results, false
		},
		skip: func() {},
		show: func(d view.CredWindowDeps) ([]creds.Result, view.CredOutcome, bool) {
			shown++
			if quit {
				return nil, view.CredOutcome{}, true
			}
			res, waited := d.Run(context.Background(), func(creds.Update) {}, func(string) {})
			return res, view.NewCredGrid(d.Accounts).Finish(res, waited), false
		},
	}, &shown
}

func names(accounts []config.Account) string {
	var out []string
	for _, a := range accounts {
		out = append(out, a.Name)
	}
	return strings.Join(out, ",")
}

func TestStart_allWork(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	d, shown := scriptedStart([]creds.Result{found("a"), found("b")}, false)
	working, results, err := resolveAccounts(context.Background(), cfgWith("a", "b"), skin, d)
	if err != nil || names(working) != "a,b" || *shown != 1 {
		t.Fatalf("working %q err %v shown %d", names(working), err, *shown)
	}
	if results["b"].SMTP.Password != "smtp-b" || results["a"].IMAP.Password != "imap-a" {
		t.Errorf("results = %+v", results)
	}
}

func TestStart_continuesWithWorkingAccountsOnly(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	onlyIMAP := found("b")
	onlyIMAP.SMTP = creds.Secret{}
	d, _ := scriptedStart([]creds.Result{found("a"), onlyIMAP, found("c")}, false)
	working, results, err := resolveAccounts(context.Background(), cfgWith("a", "b", "c"), skin, d)
	if err != nil || names(working) != "a,c" {
		t.Fatalf("working %q err %v; want a,c in config order", names(working), err)
	}
	if _, ok := results["b"]; !ok {
		t.Error("the failed account's result must be kept, for the log")
	}
}

func TestStart_noneWorkIsAnError(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	bad := creds.Result{Account: "a", Failures: []creds.Update{{Account: "a", Service: "imap", Source: creds.KeePass, State: creds.Failed, Reason: "KeePassXC is not running"}}}
	d, _ := scriptedStart([]creds.Result{bad}, false)
	working, _, err := resolveAccounts(context.Background(), cfgWith("a"), skin, d)
	if err == nil || len(working) != 0 {
		t.Fatalf("working %q err %v", names(working), err)
	}
	// The window is gone when this is printed: the reasons must be in it.
	if msg := err.Error(); !strings.HasPrefix(msg, "no account has a password") || !strings.Contains(msg, "a: imap keepassxc: KeePassXC is not running") {
		t.Errorf("error = %q, want the reasons too", msg)
	}
}

func TestStart_quitDuringCredentialsStartsNothing(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	d, _ := scriptedStart([]creds.Result{found("a")}, true)
	working, _, err := resolveAccounts(context.Background(), cfgWith("a"), skin, d)
	if err == nil || len(working) != 0 {
		t.Errorf("working %q err %v; quitting the window must start nothing", names(working), err)
	}
}

func TestStart_zeroAccountsInConfig(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	d, shown := scriptedStart(nil, false)
	_, _, err := resolveAccounts(context.Background(), cfgWith(), skin, d)
	if err == nil || err.Error() != "no accounts in config" || *shown != 0 {
		t.Errorf("err %v shown %d", err, *shown)
	}
}

func TestStart_windowGetsAccountsInConfigOrder(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	var got []string
	d, _ := scriptedStart([]creds.Result{found("z"), found("a")}, false)
	inner := d.show
	d.show = func(w view.CredWindowDeps) ([]creds.Result, view.CredOutcome, bool) {
		got = w.Accounts
		if w.Skip == nil || w.Skin == nil {
			t.Error("the window needs Skip and the skin")
		}
		return inner(w)
	}
	resolveAccounts(context.Background(), cfgWith("z", "a"), skin, d)
	if strings.Join(got, ",") != "z,a" {
		t.Errorf("window accounts = %v", got)
	}
}

func TestPasswordLogLines_levels(t *testing.T) {
	bad := creds.Result{Account: "b", Failures: []creds.Update{{Account: "b", Service: "imap", Source: creds.KeePass, State: creds.Failed, Reason: "KeePassXC is not running"}}}
	lines := passwordLogLines([]string{"a", "b"}, map[string]creds.Result{"a": found("a"), "b": bad})
	has := func(lv ui.Level, msg string) bool {
		for _, e := range lines {
			if e.Level == lv && e.Msg == msg {
				return true
			}
		}
		return false
	}
	for _, want := range []ui.Entry{
		{Level: ui.LevelInfo, Msg: "[a] password: imap keepassxc, smtp keepassxc"},
		{Level: ui.LevelWarn, Msg: "[b] imap keepassxc: KeePassXC is not running"},
		{Level: ui.LevelWarn, Msg: "[b] left out: no password"},
	} {
		if !has(want.Level, want.Msg) {
			t.Errorf("missing %v %q in %+v", want.Level, want.Msg, lines)
		}
	}
	for _, e := range lines {
		if strings.Contains(e.Msg, "imap-a") || strings.Contains(e.Msg, "smtp-a") {
			t.Errorf("a password reached the log: %q", e.Msg)
		}
	}
}

func TestRootCommand_printsAnErrorOnceWithoutUsage(t *testing.T) {
	if !rootCmd.SilenceUsage || !rootCmd.SilenceErrors {
		t.Errorf("SilenceUsage %v SilenceErrors %v: a failed start must print its error once, without the usage text",
			rootCmd.SilenceUsage, rootCmd.SilenceErrors)
	}
}

func TestStart_windowGetsGmailServices(t *testing.T) {
	skin, _ := ui.LoadSkin("")
	cfg := cfgWith("a")
	cfg.Accounts = append(cfg.Accounts, config.Account{Name: "gm", Provider: config.ProviderGmail})
	var got map[string][]string
	d, _ := scriptedStart([]creds.Result{found("a")}, false)
	inner := d.show
	d.show = func(w view.CredWindowDeps) ([]creds.Result, view.CredOutcome, bool) {
		got = w.Services
		return inner(w)
	}
	resolveAccounts(context.Background(), cfg, skin, d)
	if strings.Join(got["gm"], ",") != "client,token" || len(got["a"]) != 0 {
		t.Errorf("window services = %v, want only gm: client,token", got)
	}
}

func TestPasswordLogLines_gmail(t *testing.T) {
	ok := creds.Secret{Password: "secret-x", Source: creds.KeePass, OK: true}
	gm := creds.Result{Account: "gm", Provider: config.ProviderGmail, Gmail: creds.GmailSecrets{ClientSecret: ok, RefreshToken: ok}}
	lines := passwordLogLines([]string{"gm"}, map[string]creds.Result{"gm": gm})
	if len(lines) != 1 || lines[0].Level != ui.LevelInfo || lines[0].Msg != "[gm] secrets: client keepassxc, token keepassxc" {
		t.Errorf("log = %+v", lines)
	}
}
