package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/creds"
	"golang.org/x/oauth2"
)

func gmailCfg() *config.Config {
	return &config.Config{Accounts: []config.Account{{Name: "gm", Provider: config.ProviderGmail, KeePassXC: true,
		Gmail: config.GmailConfig{User: "Me@Gmail.com", ClientID: "cid.apps.googleusercontent.com"}}}}
}

type gmailFake struct {
	secrets  map[string]string // host|user
	email    string
	signErr  error
	saved    []string // host|user|password
	revoked  []string
	gotCfg   *oauth2.Config
	signedIn bool
}

func (f *gmailFake) deps() gmailDeps {
	return gmailDeps{
		secret: func(_ context.Context, host, user string) (string, error) {
			if s, ok := f.secrets[host+"|"+user]; ok {
				return s, nil
			}
			return "", &creds.NoEntryError{Host: host, User: user}
		},
		saveLogin: func(_ context.Context, host, user, pw string) error {
			f.saved = append(f.saved, host+"|"+user+"|"+pw)
			return nil
		},
		signIn: func(_ context.Context, cfg *oauth2.Config, _ io.Writer) (*oauth2.Token, error) {
			f.gotCfg, f.signedIn = cfg, true
			if f.signErr != nil {
				return nil, f.signErr
			}
			return &oauth2.Token{AccessToken: "at-secret", RefreshToken: "rt-secret"}, nil
		},
		profile: func(_ context.Context, _ *oauth2.Config, tok *oauth2.Token) (string, error) {
			if tok.RefreshToken != "rt-secret" {
				return "", errors.New("wrong token")
			}
			return f.email, nil
		},
		revoke: func(_ context.Context, tok string) error { f.revoked = append(f.revoked, tok); return nil },
	}
}

func newGmailFake(email string) *gmailFake {
	return &gmailFake{email: email, secrets: map[string]string{
		"accounts.google.com|cid.apps.googleusercontent.com": "client-secret",
	}}
}

func TestGmailLoginStoresToken(t *testing.T) {
	f := newGmailFake("me@gmail.com")
	var out bytes.Buffer
	acct := &gmailCfg().Accounts[0]
	if err := doGmailLogin(context.Background(), acct, f.deps(), &out); err != nil {
		t.Fatal(err)
	}
	if f.gotCfg == nil || f.gotCfg.ClientID != "cid.apps.googleusercontent.com" || f.gotCfg.ClientSecret != "client-secret" {
		t.Errorf("oauth config = %+v", f.gotCfg)
	}
	if len(f.saved) != 1 || f.saved[0] != "oauth2.googleapis.com|Me@Gmail.com|rt-secret" {
		t.Errorf("saved = %v", f.saved)
	}
	want := "token: stored in KeePassXC (https://oauth2.googleapis.com / Me@Gmail.com)"
	if !strings.Contains(out.String(), want) {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
	for _, s := range []string{"rt-secret", "at-secret", "client-secret"} {
		if strings.Contains(out.String(), s) {
			t.Errorf("output leaks %q", s)
		}
	}
}

func TestGmailLoginRefusesOtherAccount(t *testing.T) {
	f := newGmailFake("someone-else@gmail.com")
	err := doGmailLogin(context.Background(), &gmailCfg().Accounts[0], f.deps(), &bytes.Buffer{})
	want := "signed in as someone-else@gmail.com, config expects Me@Gmail.com: nothing stored"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if len(f.saved) != 0 {
		t.Errorf("SaveLogin called: %v", f.saved)
	}
}

func TestGmailLoginWithoutClientSecret(t *testing.T) {
	f := newGmailFake("me@gmail.com")
	f.secrets = nil
	err := doGmailLogin(context.Background(), &gmailCfg().Accounts[0], f.deps(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "client secret") || f.signedIn {
		t.Fatalf("err = %v, signed in %v", err, f.signedIn)
	}
}

func TestGmailLoginSignInFailureStoresNothing(t *testing.T) {
	f := newGmailFake("me@gmail.com")
	f.signErr = errors.New("sign-in timed out after 5m0s")
	err := doGmailLogin(context.Background(), &gmailCfg().Accounts[0], f.deps(), &bytes.Buffer{})
	if err == nil || len(f.saved) != 0 {
		t.Fatalf("err = %v saved %v", err, f.saved)
	}
}

func TestGmailLoginRejectsServiceFlags(t *testing.T) {
	f := newGmailFake("me@gmail.com")
	for _, svcs := range [][]string{{"imap"}, {"smtp"}} {
		err := runLogin(context.Background(), gmailCfg(), "gm", svcs, true, loginDeps{}, f.deps(), &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "--imap/--smtp") {
			t.Errorf("%v: err = %v", svcs, err)
		}
	}
	if f.signedIn {
		t.Error("signed in despite rejected flags")
	}
	if err := runLogin(context.Background(), gmailCfg(), "gm", loginServices(false, false), true, loginDeps{}, f.deps(), &bytes.Buffer{}); err != nil {
		t.Errorf("no flags: %v", err)
	}
}

func TestGmailLogoutRevokes(t *testing.T) {
	f := newGmailFake("me@gmail.com")
	f.secrets["oauth2.googleapis.com|Me@Gmail.com"] = "rt-secret"
	var out bytes.Buffer
	if err := runLogout(context.Background(), gmailCfg(), "gm", loginServices(false, false), f.deps(), &out); err != nil {
		t.Fatal(err)
	}
	if len(f.revoked) != 1 || f.revoked[0] != "rt-secret" {
		t.Errorf("revoked = %v", f.revoked)
	}
	want := "revoked at Google; delete the KeePassXC entry https://oauth2.googleapis.com / Me@Gmail.com yourself"
	if !strings.Contains(out.String(), want) || strings.Contains(out.String(), "rt-secret") {
		t.Errorf("output = %q", out.String())
	}
	if err := runLogout(context.Background(), gmailCfg(), "gm", []string{"imap"}, f.deps(), &out); err == nil {
		t.Error("--imap must be rejected for gmail logout")
	}
}

func TestGmailLogoutWithoutToken(t *testing.T) {
	f := newGmailFake("me@gmail.com")
	err := runLogout(context.Background(), gmailCfg(), "gm", loginServices(false, false), f.deps(), &bytes.Buffer{})
	if err == nil || len(f.revoked) != 0 {
		t.Errorf("err = %v revoked %v", err, f.revoked)
	}
}
