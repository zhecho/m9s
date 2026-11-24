package creds

import (
	"errors"
	"testing"

	"github.com/zhecho/m9s/internal/config"
)

func gmailAcct(name string) config.Account {
	a := config.Account{Name: name, Provider: config.ProviderGmail, KeePassXC: true,
		Gmail: config.GmailConfig{User: "me@gmail.com", ClientID: "cid.apps.googleusercontent.com"}}
	// Leftover blocks are ignored for gmail.
	a.IMAP.Host, a.IMAP.User, a.IMAP.Password = "imap.gmail.com", "me@gmail.com", "plain"
	a.SMTP.Host, a.SMTP.User, a.SMTP.PasswordCmd = "smtp.gmail.com", "me@gmail.com", "cmd"
	return a
}

func TestGmailResolveUsesKeePassOnly(t *testing.T) {
	kp := &fakeKeePass{pw: map[string]string{
		"accounts.google.com|cid.apps.googleusercontent.com": "client-secret",
		"oauth2.googleapis.com|me@gmail.com":                 "refresh-token",
	}}
	w := &world{
		env:      map[string]string{"GM_IMAP_PASSWORD": "e", "GM_SMTP_PASSWORD": "e", "GM_CLIENT_PASSWORD": "e", "GM_TOKEN_PASSWORD": "e"},
		cmds:     map[string]string{"cmd": "c"},
		keychain: map[string]string{"gm/imap": "k", "gm/smtp": "k", "gm/client": "k", "gm/token": "k"},
		kp:       kp,
	}
	r := one(t, w, gmailAcct("gm"))

	if !r.Gmail.ClientSecret.OK || r.Gmail.ClientSecret.Password != "client-secret" || r.Gmail.ClientSecret.Source != KeePass {
		t.Errorf("client secret = %+v", r.Gmail.ClientSecret)
	}
	if !r.Gmail.RefreshToken.OK || r.Gmail.RefreshToken.Password != "refresh-token" || r.Gmail.RefreshToken.Source != KeePass {
		t.Errorf("refresh token = %+v", r.Gmail.RefreshToken)
	}
	if r.IMAP.OK || r.SMTP.OK {
		t.Errorf("imap/smtp must not be looked up for gmail: %+v %+v", r.IMAP, r.SMTP)
	}
	if !r.Works() {
		t.Error("Works() = false with both gmail secrets")
	}
	if len(w.cmdRuns) != 0 {
		t.Errorf("commands run for gmail: %v", w.cmdRuns)
	}
	want := []kpCall{{GmailClientHost, "cid.apps.googleusercontent.com"}, {GmailTokenHost, "me@gmail.com"}}
	if len(kp.calls) != 2 || kp.calls[0] != want[0] || kp.calls[1] != want[1] {
		t.Errorf("KeePassXC calls = %v, want %v", kp.calls, want)
	}
	services := map[string]bool{}
	for _, u := range w.updates {
		services[u.Service] = true
		if u.Source != KeePass && u.State != NotSet {
			t.Errorf("source %s is %d for gmail, want not set", u.Source, u.State)
		}
	}
	if len(services) != 2 || !services[ServiceClient] || !services[ServiceToken] {
		t.Errorf("update services = %v, want client and token", services)
	}
}

func TestGmailMissingTokenReason(t *testing.T) {
	kp := &fakeKeePass{pw: map[string]string{"accounts.google.com|cid.apps.googleusercontent.com": "s"}}
	r := one(t, &world{kp: kp}, gmailAcct("gm"))
	if r.Works() || r.Gmail.RefreshToken.OK {
		t.Fatalf("works without a token: %+v", r)
	}
	if len(r.Failures) != 1 || r.Failures[0].Service != ServiceToken || r.Failures[0].Source != KeePass ||
		r.Failures[0].Reason != "not signed in: run m9s login gm" {
		t.Errorf("failures = %+v", r.Failures)
	}
}

func TestGmailOtherKeePassErrorKeepsItsReason(t *testing.T) {
	r := one(t, &world{kp: &fakeKeePass{err: errors.New("KeePassXC is locked")}}, gmailAcct("gm"))
	if len(r.Failures) != 2 || r.Failures[1].Reason != "KeePassXC is locked" {
		t.Errorf("failures = %+v", r.Failures)
	}
}

func TestWorksPerProvider(t *testing.T) {
	ok := Secret{Password: "x", OK: true}
	for _, tc := range []struct {
		name string
		r    Result
		want bool
	}{
		{"imap both", Result{IMAP: ok, SMTP: ok}, true},
		{"imap one", Result{IMAP: ok}, false},
		{"gmail both", Result{Provider: config.ProviderGmail, Gmail: GmailSecrets{ClientSecret: ok, RefreshToken: ok}}, true},
		{"gmail no token", Result{Provider: config.ProviderGmail, Gmail: GmailSecrets{ClientSecret: ok}}, false},
		{"gmail with only imap secrets", Result{Provider: config.ProviderGmail, IMAP: ok, SMTP: ok}, false},
		{"imap with only gmail secrets", Result{Gmail: GmailSecrets{ClientSecret: ok, RefreshToken: ok}}, false},
	} {
		if got := tc.r.Works(); got != tc.want {
			t.Errorf("%s: Works() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestServicesOf(t *testing.T) {
	g := gmailAcct("gm")
	if got := ServicesOf(&g); len(got) != 2 || got[0] != ServiceClient || got[1] != ServiceToken {
		t.Errorf("gmail services = %v", got)
	}
	i := acct("w", nil)
	if got := ServicesOf(&i); len(got) != 2 || got[0] != "imap" || got[1] != "smtp" {
		t.Errorf("imap services = %v", got)
	}
}
