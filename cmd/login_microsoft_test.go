package cmd

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/creds"
	"golang.org/x/oauth2"
)

func microsoftAcct() *config.Account {
	return &config.Account{Name: "ms", Provider: config.ProviderMicrosoft, KeePassXC: true,
		Microsoft: config.MicrosoftConfig{User: "M9s-Test@outlook.com", ClientID: "cid"}}
}

type microsoftFake struct {
	address string
	saved   []string // host|user|password
	gotCfg  *oauth2.Config
}

func (f *microsoftFake) deps() microsoftDeps {
	return microsoftDeps{
		saveLogin: func(_ context.Context, host, user, pw string) error {
			f.saved = append(f.saved, host+"|"+user+"|"+pw)
			return nil
		},
		signIn: func(_ context.Context, cfg *oauth2.Config, _ io.Writer) (*oauth2.Token, error) {
			f.gotCfg = cfg
			return &oauth2.Token{AccessToken: "at-secret", RefreshToken: "rt-secret"}, nil
		},
		me: func(context.Context, *oauth2.Config, *oauth2.Token) (string, error) { return f.address, nil },
	}
}

func TestMicrosoftLogin_stores(t *testing.T) {
	f := &microsoftFake{address: "m9s-test@outlook.com"}
	var out bytes.Buffer
	if err := doMicrosoftLogin(context.Background(), microsoftAcct(), f.deps(), &out); err != nil {
		t.Fatal(err)
	}
	if len(f.saved) != 1 || f.saved[0] != creds.MicrosoftTokenHost+"|M9s-Test@outlook.com|rt-secret" {
		t.Fatalf("saved = %v", f.saved)
	}
	if f.gotCfg.ClientID != "cid" || f.gotCfg.ClientSecret != "" {
		t.Errorf("oauth config = %+v", f.gotCfg)
	}
	want := "token: stored in KeePassXC (https://graph.microsoft.com / M9s-Test@outlook.com)"
	if !strings.Contains(out.String(), want) || strings.Contains(out.String(), "rt-secret") {
		t.Errorf("out = %q", out.String())
	}
}

func TestMicrosoftLogin_wrongAccount(t *testing.T) {
	f := &microsoftFake{address: "someone.else@outlook.com"}
	err := doMicrosoftLogin(context.Background(), microsoftAcct(), f.deps(), io.Discard)
	if err == nil || !strings.HasSuffix(err.Error(), "nothing stored") ||
		!strings.Contains(err.Error(), "someone.else@outlook.com") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err = %v", err)
	}
	if len(f.saved) != 0 {
		t.Fatalf("saved = %v", f.saved)
	}
}

func TestMicrosoftLogout_says_where(t *testing.T) {
	var out bytes.Buffer
	if err := doMicrosoftLogout(microsoftAcct(), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"account.live.com/consent/Manage", "myapps.microsoft.com", "https://graph.microsoft.com / M9s-Test@outlook.com"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("out lacks %q: %s", want, out.String())
		}
	}
}

func TestRunLogin_microsoftTakesNoServiceFlags(t *testing.T) {
	cfg := &config.Config{Accounts: []config.Account{*microsoftAcct()}}
	err := runLogin(context.Background(), cfg, "ms", []string{"imap"}, false, loginDeps{}, gmailDeps{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "microsoft") {
		t.Fatalf("err = %v", err)
	}
}
