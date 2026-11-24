package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
	"github.com/zhecho/m9s/internal/config"
)

func testCfg() *config.Config {
	return &config.Config{Accounts: []config.Account{
		{Name: "work", IMAP: config.IMAPConfig{Host: "i"}, SMTP: config.SMTPConfig{Host: "s"}},
		{Name: "legacy", IMAP: config.IMAPConfig{Password: "old"}, SMTP: config.SMTPConfig{PasswordCmd: "echo x"}},
	}}
}

func fakeDeps(pw string, imapErr, smtpErr error) (loginDeps, *[]string) {
	var calls []string
	return loginDeps{
		readPassword: func(string) (string, error) { return pw, nil },
		verifyIMAP:   func(*config.IMAPConfig, string) error { calls = append(calls, "imap"); return imapErr },
		verifySMTP:   func(*config.SMTPConfig, string) error { calls = append(calls, "smtp"); return smtpErr },
	}, &calls
}

func TestLogin_storesBothServices(t *testing.T) {
	keyring.MockInit()
	deps, calls := fakeDeps("secret", nil, nil)
	if err := doLogin(testCfg(), "work", loginServices(false, false), true, deps, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, svc := range []string{"imap", "smtp"} {
		got, err := config.KeychainPassword("work", svc)
		if err != nil || got != "secret" {
			t.Errorf("%s: keychain holds %q (err %v)", svc, got, err)
		}
	}
	if strings.Join(*calls, ",") != "imap,smtp" {
		t.Errorf("want both verified, got %v", *calls)
	}
}

func TestLogin_verifyFailureStoresNothing(t *testing.T) {
	keyring.MockInit()
	deps, _ := fakeDeps("typo", nil, errors.New("535 auth failed"))
	err := doLogin(testCfg(), "work", loginServices(false, false), true, deps, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "535") {
		t.Fatalf("want auth error, got %v", err)
	}
	if _, err := config.KeychainPassword("work", "imap"); err == nil {
		t.Error("imap password stored although smtp verification failed")
	}
}

func TestLogin_onlySMTPNoVerify(t *testing.T) {
	keyring.MockInit()
	deps, calls := fakeDeps("s", nil, nil)
	if err := doLogin(testCfg(), "work", loginServices(false, true), false, deps, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Errorf("--no-verify should skip verification, got %v", *calls)
	}
	if _, err := config.KeychainPassword("work", "imap"); err == nil {
		t.Error("imap should not be stored with --smtp")
	}
}

func TestLogin_unknownAccount(t *testing.T) {
	keyring.MockInit()
	deps, _ := fakeDeps("x", nil, nil)
	if err := doLogin(testCfg(), "nope", loginServices(false, false), true, deps, &bytes.Buffer{}); err == nil {
		t.Error("want error for unknown account")
	}
}

func TestLogin_emptyPasswordRejected(t *testing.T) {
	keyring.MockInit()
	deps, _ := fakeDeps("", nil, nil)
	if err := doLogin(testCfg(), "work", loginServices(false, false), false, deps, &bytes.Buffer{}); err == nil {
		t.Error("want error for empty password")
	}
}

func TestLogin_warnsWhenConfigShadowsKeychain(t *testing.T) {
	keyring.MockInit()
	deps, _ := fakeDeps("new", nil, nil)
	var out bytes.Buffer
	if err := doLogin(testCfg(), "legacy", loginServices(false, false), false, deps, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "imap.password") || !strings.Contains(s, "smtp.password_cmd") {
		t.Errorf("want shadowing warnings for both services, got:\n%s", s)
	}
}

func TestLogout_removesEntries(t *testing.T) {
	keyring.MockInit()
	config.SetKeychainPassword("work", "imap", "x")
	config.SetKeychainPassword("work", "smtp", "y")
	if err := doLogout(testCfg(), "work", loginServices(false, false), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, svc := range []string{"imap", "smtp"} {
		if _, err := config.KeychainPassword("work", svc); err == nil {
			t.Errorf("%s still stored", svc)
		}
	}
}

func TestShadowingField_keepassxc(t *testing.T) {
	acct := &config.Account{Name: "a", KeePassXC: true}
	acct.IMAP.Password = "p"
	if got := shadowingField(acct, "imap"); got != "keepassxc: true" {
		t.Errorf("shadowingField = %q, want keepassxc: true (it is tried before password)", got)
	}
}
