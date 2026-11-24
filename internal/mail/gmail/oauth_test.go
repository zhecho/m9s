package gmail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// fakeGoogle is a token endpoint; the auth endpoint is never fetched by
// Login itself, only handed to the opener.
type fakeGoogle struct {
	srv  *httptest.Server
	mu   sync.Mutex
	form url.Values
}

func newFakeGoogle(t *testing.T, reply func(w http.ResponseWriter)) *fakeGoogle {
	t.Helper()
	g := &fakeGoogle{}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		r.ParseForm()
		g.mu.Lock()
		g.form = r.PostForm
		g.mu.Unlock()
		reply(w)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGoogle) config() *oauth2.Config {
	cfg := OAuthConfig("cid.apps.googleusercontent.com", "client-secret")
	cfg.Endpoint = oauth2.Endpoint{AuthURL: g.srv.URL + "/auth", TokenURL: g.srv.URL + "/token",
		AuthStyle: oauth2.AuthStyleInParams}
	return cfg
}

func okToken(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":3600}`)
}

// redirectWith answers the authorization URL as Google would after consent.
func redirectWith(t *testing.T, authURL string, mod func(q url.Values)) error {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		return err
	}
	q := u.Query()
	back := url.Values{"code": {"the-code"}, "state": {q.Get("state")}}
	if mod != nil {
		mod(back)
	}
	resp, err := http.Get(q.Get("redirect_uri") + "?" + back.Encode())
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func TestOAuthConfig(t *testing.T) {
	cfg := OAuthConfig("id", "secret")
	if cfg.ClientID != "id" || cfg.ClientSecret != "secret" || len(cfg.Scopes) != 1 || cfg.Scopes[0] != Scope ||
		cfg.Endpoint.AuthURL != "https://accounts.google.com/o/oauth2/auth" ||
		cfg.Endpoint.TokenURL != "https://oauth2.googleapis.com/token" {
		t.Errorf("config = %+v", cfg)
	}
}

func TestLoginLoopbackPKCE(t *testing.T) {
	g := newFakeGoogle(t, okToken)
	var authQ url.Values
	opener := func(authURL string) error {
		u, _ := url.Parse(authURL)
		authQ = u.Query()
		if !strings.HasPrefix(authURL, g.srv.URL+"/auth?") {
			t.Errorf("auth URL %q is not the configured endpoint", authURL)
		}
		return redirectWith(t, authURL, nil)
	}
	var out bytes.Buffer
	tok, err := Login(context.Background(), g.config(), opener, &out)
	if err != nil {
		t.Fatal(err)
	}
	if tok.RefreshToken != "rt" || tok.AccessToken != "at" {
		t.Errorf("token = %+v", tok)
	}

	for k, want := range map[string]string{"code_challenge_method": "S256", "access_type": "offline",
		"prompt": "consent", "scope": Scope, "response_type": "code", "client_id": "cid.apps.googleusercontent.com"} {
		if got := authQ.Get(k); got != want {
			t.Errorf("auth %s = %q, want %q", k, got, want)
		}
	}
	redirect := authQ.Get("redirect_uri")
	if !strings.HasPrefix(redirect, "http://127.0.0.1:") {
		t.Errorf("redirect_uri = %q, want loopback 127.0.0.1", redirect)
	}
	if authQ.Get("state") == "" {
		t.Error("no state")
	}

	g.mu.Lock()
	form := g.form
	g.mu.Unlock()
	verifier := form.Get("code_verifier")
	if verifier == "" {
		t.Fatal("no code_verifier posted")
	}
	sum := sha256.Sum256([]byte(verifier))
	if got := base64.RawURLEncoding.EncodeToString(sum[:]); got != authQ.Get("code_challenge") {
		t.Errorf("code_challenge %q is not S256(verifier) %q", authQ.Get("code_challenge"), got)
	}
	if form.Get("code") != "the-code" || form.Get("redirect_uri") != redirect || form.Get("grant_type") != "authorization_code" {
		t.Errorf("token form = %v", form)
	}
	if !strings.Contains(out.String(), authQ.Get("state")) {
		t.Errorf("the URL must also be printed:\n%s", out.String())
	}
	for _, secret := range []string{"client-secret", "rt", "at\n"} {
		if strings.Contains(out.String(), secret) {
			t.Errorf("output leaks %q:\n%s", secret, out.String())
		}
	}
}

func TestLoginRejectsWrongState(t *testing.T) {
	g := newFakeGoogle(t, okToken)
	opener := func(authURL string) error {
		return redirectWith(t, authURL, func(q url.Values) { q.Set("state", "forged") })
	}
	_, err := Login(context.Background(), g.config(), opener, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("err = %v, want a state mismatch", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.form != nil {
		t.Error("the code was exchanged despite the wrong state")
	}
}

func TestLoginDeniedConsent(t *testing.T) {
	g := newFakeGoogle(t, okToken)
	opener := func(authURL string) error {
		return redirectWith(t, authURL, func(q url.Values) { q.Del("code"); q.Set("error", "access_denied") })
	}
	_, err := Login(context.Background(), g.config(), opener, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("err = %v, want access_denied", err)
	}
}

func TestLoginTokenErrorHidesBody(t *testing.T) {
	g := newFakeGoogle(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant","error_description":"leaky-detail client-secret"}`)
	})
	_, err := Login(context.Background(), g.config(), func(u string) error { return redirectWith(t, u, nil) }, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") || strings.Contains(err.Error(), "leaky-detail") {
		t.Fatalf("err = %v, want invalid_grant without the body", err)
	}
}

func TestLoginOpenerFailureStillWaits(t *testing.T) {
	g := newFakeGoogle(t, okToken)
	opener := func(authURL string) error {
		go func() { time.Sleep(20 * time.Millisecond); redirectWith(t, authURL, nil) }()
		return errors.New("no browser")
	}
	var out bytes.Buffer
	if _, err := Login(context.Background(), g.config(), opener, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no browser") {
		t.Errorf("the opener failure must be mentioned:\n%s", out.String())
	}
}

func TestLoginTimesOut(t *testing.T) {
	old := loginTimeout
	loginTimeout = 50 * time.Millisecond
	t.Cleanup(func() { loginTimeout = old })
	g := newFakeGoogle(t, okToken)
	_, err := Login(context.Background(), g.config(), func(string) error { return nil }, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a time-out", err)
	}
}

func TestRevoke(t *testing.T) {
	var form url.Values
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		form = r.PostForm
		if r.Method != http.MethodPost || r.URL.Path != "/revoke" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		if status != http.StatusOK {
			fmt.Fprint(w, `{"error":"invalid_token","error_description":"leaky-detail"}`)
		}
	}))
	defer srv.Close()
	old := revokeURL
	revokeURL = srv.URL + "/revoke"
	t.Cleanup(func() { revokeURL = old })

	if err := Revoke(context.Background(), "rt-123"); err != nil {
		t.Fatal(err)
	}
	if form.Get("token") != "rt-123" {
		t.Errorf("form = %v", form)
	}
	status = http.StatusBadRequest
	err := Revoke(context.Background(), "rt-123")
	if err == nil || !strings.Contains(err.Error(), "invalid_token") || strings.Contains(err.Error(), "leaky") || strings.Contains(err.Error(), "rt-123") {
		t.Errorf("err = %v", err)
	}
}
