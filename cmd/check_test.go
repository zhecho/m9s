package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/creds"
)

func checkWith(results []creds.Result, imapErr, smtpErr error) (string, bool, *[]string) {
	var used []string
	deps := checkDeps{
		imap: func(_ *config.IMAPConfig, pass string) (int, error) { used = append(used, pass); return 7, imapErr },
		smtp: func(_ *config.SMTPConfig, pass string) error { used = append(used, pass); return smtpErr },
	}
	var out bytes.Buffer
	failed := reportCheck(cfgWith("a").Accounts, results, deps, &out)
	return out.String(), failed, &used
}

func TestCheck_reportsKeepassxcAsSource(t *testing.T) {
	out, failed, used := checkWith([]creds.Result{found("a")}, nil, nil)
	if failed {
		t.Errorf("failed although both logins work:\n%s", out)
	}
	for _, want := range []string{"[a] IMAP: ✓ connected, 7 folders (password: keepassxc)", "[a] SMTP: ✓ auth OK (password: keepassxc)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Join(*used, ",") != "imap-a,smtp-a" {
		t.Errorf("logged in with %v", *used)
	}
	if strings.Contains(out, "imap-a") {
		t.Errorf("a password was printed:\n%s", out)
	}
}

func TestCheck_printsEveryFailureReason(t *testing.T) {
	r := creds.Result{Account: "a", SMTP: creds.Secret{Password: "p", Source: creds.Plain, OK: true},
		Failures: []creds.Update{
			{Account: "a", Service: "imap", Source: creds.KeePass, State: creds.Failed, Reason: "KeePassXC is not running"},
			{Account: "a", Service: "imap", Source: creds.Cmd, State: creds.Failed, Reason: "password_cmd failed: exit status 1"},
			{Account: "a", Service: "smtp", Source: creds.KeePass, State: creds.Failed, Reason: "KeePassXC is not running"},
		}}
	out, failed, used := checkWith([]creds.Result{r}, nil, nil)
	if !failed {
		t.Error("an account without an IMAP password must fail the check")
	}
	for _, want := range []string{"[a] IMAP: ✗ no password", "keepassxc: KeePassXC is not running", "password_cmd: password_cmd failed: exit status 1",
		"[a] SMTP: ✓ auth OK (password: plain text)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if len(*used) != 1 {
		t.Errorf("logins attempted = %v, want only SMTP", *used)
	}
}

func TestCheck_nothingConfiguredSuggestsLogin(t *testing.T) {
	out, failed, _ := checkWith([]creds.Result{{Account: "a"}}, nil, nil)
	if !failed || !strings.Contains(out, "m9s login a") {
		t.Errorf("failed %v out:\n%s", failed, out)
	}
}

func TestCheck_loginErrorFails(t *testing.T) {
	out, failed, _ := checkWith([]creds.Result{found("a")}, errors.New("auth: bad"), nil)
	if !failed || !strings.Contains(out, "[a] IMAP: ✗ auth: bad") {
		t.Errorf("failed %v out:\n%s", failed, out)
	}
}
