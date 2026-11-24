package creds

import (
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/config"
)

func microsoftAcct(name string) config.Account {
	return config.Account{Name: name, Provider: config.ProviderMicrosoft, KeePassXC: true,
		Microsoft: config.MicrosoftConfig{User: "me@outlook.com", ClientID: "00000000-0000-0000-0000-000000000000"}}
}

func TestResolve_microsoftToken(t *testing.T) {
	kp := &fakeKeePass{pw: map[string]string{"graph.microsoft.com|me@outlook.com": "refresh-token"}}
	w := &world{
		env:      map[string]string{"MS_TOKEN_PASSWORD": "e"},
		keychain: map[string]string{"ms/token": "k"},
		kp:       kp,
	}
	r := one(t, w, microsoftAcct("ms"))
	if !r.Works() || !r.Microsoft.RefreshToken.OK || r.Microsoft.RefreshToken.Password != "refresh-token" ||
		r.Microsoft.RefreshToken.Source != KeePass {
		t.Fatalf("result = %+v", r)
	}
	if len(kp.calls) != 1 || kp.calls[0] != (kpCall{MicrosoftTokenHost, "me@outlook.com"}) {
		t.Errorf("KeePassXC calls = %v, want only the token", kp.calls)
	}
	for _, u := range w.updates {
		if u.Service != ServiceToken {
			t.Errorf("update for service %q", u.Service)
		}
	}
	if s := ServicesOf(&[]config.Account{microsoftAcct("ms")}[0]); len(s) != 1 || s[0] != ServiceToken {
		t.Errorf("ServicesOf = %v", s)
	}
}

func TestResolve_microsoftNotSignedIn(t *testing.T) {
	r := one(t, &world{kp: &fakeKeePass{pw: map[string]string{}}}, microsoftAcct("ms"))
	if r.Works() {
		t.Fatalf("works without a token: %+v", r)
	}
	if len(r.Failures) != 1 || r.Failures[0].Service != ServiceToken ||
		!strings.Contains(r.Failures[0].Reason, "m9s login ms") {
		t.Errorf("failures = %+v", r.Failures)
	}
}
