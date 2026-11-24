package cmd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	gosync "sync"
	"testing"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/creds"
	"github.com/zhecho/m9s/internal/mail/graph"
	"github.com/zhecho/m9s/internal/mail/graph/graphtest"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	"github.com/zhecho/m9s/internal/store"
	msync "github.com/zhecho/m9s/internal/sync"
	"golang.org/x/oauth2"
)

// rotatingTokens is a token endpoint that hands out the refresh tokens
// given, one per refresh (the last one again when they run out).
func rotatingTokens(t *testing.T, refresh ...string) *httptest.Server {
	t.Helper()
	var mu gosync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		rt := refresh[min(n, len(refresh)-1)]
		n++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"at%d","refresh_token":%q,"token_type":"Bearer","expires_in":3600}`, n, rt)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRefreshingSource_onRotate(t *testing.T) {
	tokens := rotatingTokens(t, "rt-1", "rt-1", "rt-2")
	var got []string
	src := &refreshingSource{ctx: context.Background(), refreshToken: "rt-0",
		cfg:      &oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{TokenURL: tokens.URL, AuthStyle: oauth2.AuthStyleInParams}},
		onRotate: func(tok string) { got = append(got, tok) }}
	for range 3 {
		if err := src.refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 2 || got[0] != "rt-1" || got[1] != "rt-2" {
		t.Fatalf("onRotate calls = %v, want rt-1 then rt-2", got)
	}
}

func signedInMicrosoft() creds.Result {
	return creds.Result{Account: "ms", Provider: "microsoft",
		Microsoft: creds.MicrosoftSecrets{RefreshToken: creds.Secret{Password: "rt", Source: creds.KeePass, OK: true}}}
}

// fakeMicrosoftFactory points the client at f and its token refreshes at tokens.
func fakeMicrosoftFactory(f *graphtest.Fake, tokens *httptest.Server) microsoftFactory {
	return microsoftFactory{
		oauth: func(clientID string) *oauth2.Config {
			return &oauth2.Config{ClientID: clientID, Endpoint: oauth2.Endpoint{TokenURL: tokens.URL, AuthStyle: oauth2.AuthStyleInParams}}
		},
		opts: []graph.Option{graph.WithBaseURL(f.URL())},
	}
}

func TestBuildAccount_microsoft(t *testing.T) {
	f := graphtest.New()
	defer f.Close()
	tokens := rotatingTokens(t, "rt-new")
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var rotated []string
	b := &accountBuilder{ctx: context.Background(), events: make(chan msync.Event, 8), store: st,
		microsoft: fakeMicrosoftFactory(f, tokens),
		saverFor: func(acct *config.Account) func(string) {
			return func(tok string) { rotated = append(rotated, acct.Name+"|"+tok) }
		}}
	eng, err := b.buildAccount(microsoftAcct(), signedInMicrosoft())
	if err != nil {
		t.Fatal(err)
	}
	me, ok := eng.(*msync.MicrosoftEngine)
	if !ok {
		t.Fatalf("engine = %T", eng)
	}
	// A request refreshes the token, which rotates.
	if _, err := me.Folders(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rotated) != 1 || rotated[0] != "ms|rt-new" {
		t.Fatalf("rotations = %v", rotated)
	}

	if _, err := b.buildAccount(microsoftAcct(), creds.Result{Provider: "microsoft"}); err == nil || !strings.Contains(err.Error(), "m9s login ms") {
		t.Fatalf("not signed in: err = %v", err)
	}
	nb := &accountBuilder{ctx: context.Background(), events: make(chan msync.Event, 8), microsoft: fakeMicrosoftFactory(f, tokens)}
	if _, err := nb.buildAccount(microsoftAcct(), signedInMicrosoft()); err == nil {
		t.Fatal("built without a store")
	}
}

func TestCheckMicrosoft(t *testing.T) {
	var out strings.Builder
	deps := checkDeps{microsoft: func(context.Context, *config.Account, creds.Result) (string, error) {
		return "m9s-test@outlook.com", nil
	}}
	if failed := reportCheck([]config.Account{*microsoftAcct()}, []creds.Result{signedInMicrosoft()}, deps, &out); failed {
		t.Fatalf("failed: %s", out.String())
	}
	if !strings.Contains(out.String(), "[ms] microsoft: ✓ signed in as m9s-test@outlook.com") {
		t.Errorf("out = %q", out.String())
	}
	out.Reset()
	if failed := reportCheck([]config.Account{*microsoftAcct()}, []creds.Result{{Account: "ms", Provider: "microsoft"}}, deps, &out); !failed ||
		!strings.Contains(out.String(), "m9s login ms") {
		t.Errorf("not signed in: %q", out.String())
	}
}

func TestSendMicrosoft(t *testing.T) {
	f := graphtest.New()
	defer f.Close()
	tokens := rotatingTokens(t, "rt")
	d := smtpadapter.Draft{To: []string{"bob@example.com"}, Subject: "hi", Body: "hello"}
	if err := sendMicrosoft(context.Background(), microsoftAcct(), signedInMicrosoft(), fakeMicrosoftFactory(f, tokens), d); err != nil {
		t.Fatal(err)
	}
	sent := f.Sent()
	if len(sent) != 1 || !strings.Contains(string(sent[0]), "From: <M9s-Test@outlook.com>") {
		t.Fatalf("sent = %q", sent)
	}
}
