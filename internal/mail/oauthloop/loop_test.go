package oauthloop

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// tokenServer answers the code exchange and remembers its form.
func tokenServer(t *testing.T) (*httptest.Server, *url.Values) {
	t.Helper()
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &form
}

// browser follows the auth URL as the provider would after consent.
func browser(t *testing.T, seen *url.Values) func(string) error {
	return func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		*seen = u.Query()
		go func() {
			back := seen.Get("redirect_uri") + "?code=the-code&state=" + url.QueryEscape(seen.Get("state"))
			resp, err := http.Get(back)
			if err != nil {
				t.Errorf("redirect: %v", err)
				return
			}
			resp.Body.Close()
		}()
		return nil
	}
}

func TestOAuthLoop_exchangesCode(t *testing.T) {
	srv, form := tokenServer(t)
	cfg := &oauth2.Config{ClientID: "cid", Scopes: []string{"a", "b"},
		Endpoint: oauth2.Endpoint{AuthURL: "https://login.example/authorize", TokenURL: srv.URL, AuthStyle: oauth2.AuthStyleInParams}}
	var seen url.Values
	var out bytes.Buffer
	tok, err := Run(context.Background(), cfg, Options{Host: "localhost", Provider: "Microsoft",
		AuthOptions: []oauth2.AuthCodeOption{oauth2.SetAuthURLParam("prompt", "select_account")}, Timeout: 5 * time.Second},
		browser(t, &seen), &out)
	if err != nil {
		t.Fatal(err)
	}
	if tok.RefreshToken != "rt" || tok.AccessToken != "at" {
		t.Fatalf("token = %+v", tok)
	}
	redirect := seen.Get("redirect_uri")
	if !strings.HasPrefix(redirect, "http://localhost:") || strings.HasSuffix(redirect, "/") {
		t.Errorf("redirect_uri = %q, want http://localhost:<port> without a path", redirect)
	}
	for k, want := range map[string]string{"code_challenge_method": "S256", "prompt": "select_account", "client_id": "cid", "scope": "a b"} {
		if got := seen.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if seen.Get("access_type") != "" {
		t.Errorf("access_type sent: %q", seen.Get("access_type"))
	}
	if form.Get("code") != "the-code" || form.Get("code_verifier") == "" || form.Get("redirect_uri") != redirect {
		t.Errorf("exchange form = %v", *form)
	}
	if !strings.Contains(out.String(), "Sign in to Microsoft in the browser") || strings.Contains(out.String(), "the-code") {
		t.Errorf("out = %q", out.String())
	}
}

func TestOAuthLoop_slashRedirectOnLoopbackIP(t *testing.T) {
	srv, _ := tokenServer(t)
	cfg := &oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{AuthURL: "https://login.example/a", TokenURL: srv.URL}}
	var seen url.Values
	if _, err := Run(context.Background(), cfg, Options{Host: "127.0.0.1", Path: "/", Provider: "Google", Timeout: 5 * time.Second},
		browser(t, &seen), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if r := seen.Get("redirect_uri"); !strings.HasPrefix(r, "http://127.0.0.1:") || !strings.HasSuffix(r, "/") {
		t.Errorf("redirect_uri = %q", r)
	}
}
