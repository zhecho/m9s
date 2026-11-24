package graph

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestLogin_microsoftRedirectAndPrompt(t *testing.T) {
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":3600}`)
	}))
	defer tokens.Close()
	cfg := OAuthConfig("cid")
	cfg.Endpoint = oauth2.Endpoint{AuthURL: cfg.Endpoint.AuthURL, TokenURL: tokens.URL, AuthStyle: oauth2.AuthStyleInParams}
	var seen url.Values
	open := func(authURL string) error {
		u, _ := url.Parse(authURL)
		seen = u.Query()
		go func() {
			resp, err := http.Get(seen.Get("redirect_uri") + "?code=c&state=" + url.QueryEscape(seen.Get("state")))
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
	var out bytes.Buffer
	tok, err := Login(context.Background(), cfg, open, &out)
	if err != nil || tok.RefreshToken != "rt" {
		t.Fatalf("Login = %+v %v", tok, err)
	}
	if r := seen.Get("redirect_uri"); !strings.HasPrefix(r, "http://localhost:") || strings.HasSuffix(r, "/") {
		t.Errorf("redirect_uri = %q, want http://localhost:<port> as registered", r)
	}
	if seen.Get("prompt") != "select_account" || seen.Get("access_type") != "" || seen.Get("code_challenge_method") != "S256" {
		t.Errorf("auth query = %v", seen)
	}
	if !strings.Contains(out.String(), "Sign in to Microsoft") {
		t.Errorf("out = %q", out.String())
	}
}
