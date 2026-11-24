package cmd

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/creds"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail/gmail"
	"github.com/zhecho/m9s/internal/mail/gmail/gmailtest"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	"github.com/zhecho/m9s/internal/store"
	msync "github.com/zhecho/m9s/internal/sync"
	"golang.org/x/oauth2"
)

// tokenServer answers the fake's /token with a new access token per
// request ("at-1", "at-2", ...) and remembers the forms it got.
type tokenServer struct {
	mu    gosync.Mutex
	forms []url.Values
	fail  string // an OAuth error code to answer with instead
}

func (ts *tokenServer) install(f *gmailtest.Fake) {
	f.Handle("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		ts.mu.Lock()
		ts.forms = append(ts.forms, r.PostForm)
		n, fail := len(ts.forms), ts.fail
		ts.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if fail != "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":%q,"error_description":"secret detail rt-secret"}`, fail)
			return
		}
		fmt.Fprintf(w, `{"access_token":"at-%d","token_type":"Bearer","expires_in":3600}`, n)
	})
}

func (ts *tokenServer) count() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(ts.forms)
}

// fakeGmailFactory signs in against f instead of Google.
func fakeGmailFactory(f *gmailtest.Fake) gmailFactory {
	return gmailFactory{
		oauth: func(id, secret string) *oauth2.Config {
			return &oauth2.Config{ClientID: id, ClientSecret: secret,
				Endpoint: oauth2.Endpoint{TokenURL: f.URL() + "/token", AuthStyle: oauth2.AuthStyleInParams}}
		},
		opts: []gmail.Option{gmail.WithBaseURL(f.URL())},
	}
}

func gmailAccount() config.Account {
	return config.Account{Name: "gm", Provider: config.ProviderGmail, KeePassXC: true,
		Gmail: config.GmailConfig{User: "me@example.com", ClientID: "cid.apps.googleusercontent.com"}}
}

func signedIn(name string) creds.Result {
	return creds.Result{Account: name, Provider: config.ProviderGmail, Gmail: creds.GmailSecrets{
		ClientSecret: creds.Secret{Password: "client-secret", Source: creds.KeePass, OK: true},
		RefreshToken: creds.Secret{Password: "rt-secret", Source: creds.KeePass, OK: true},
	}}
}

func TestBuildAccountGmail(t *testing.T) {
	f := gmailtest.New()
	defer f.Close()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	b := &accountBuilder{ctx: context.Background(), events: make(chan msync.Event, 8), store: st,
		drafts: draft.NewStore(t.TempDir()), gmail: fakeGmailFactory(f)}
	acct := gmailAccount()

	got, err := b.buildAccount(&acct, signedIn("gm"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.(*msync.GmailEngine); !ok {
		t.Fatalf("buildAccount = %T, want *sync.GmailEngine", got)
	}
}

// After "sign in again", Retry reads the sign-in again and the client uses
// the new refresh token and client secret: m9s login needs no restart.
func TestBuildAccountGmailRetryUsesNewSignIn(t *testing.T) {
	f := gmailtest.New()
	defer f.Close()
	var mu gosync.Mutex
	var forms []url.Values
	f.Handle("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		forms = append(forms, r.PostForm)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.PostForm.Get("refresh_token") != "rt-new" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		fmt.Fprint(w, `{"access_token":"at-new","token_type":"Bearer","expires_in":3600}`)
	})
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	events := make(chan msync.Event, 64)
	reads := 0
	b := &accountBuilder{ctx: context.Background(), events: events, store: st,
		drafts: draft.NewStore(t.TempDir()), gmail: fakeGmailFactory(f),
		reread: func(_ context.Context, a *config.Account) (string, string, error) {
			reads++
			if a.Name != "gm" {
				t.Errorf("reread account = %q", a.Name)
			}
			return "client-secret-2", "rt-new", nil
		}}
	acct := gmailAccount()
	eng, err := b.buildAccount(&acct, signedIn("gm"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-eng.(*msync.GmailEngine).Done() }()
	eng.Start(ctx)

	wait := func(want msync.ConnState) msync.AccountStatus {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case ev := <-events:
				if s, ok := ev.(msync.AccountStatus); ok && s.State == want {
					return s
				}
			case <-deadline:
				t.Fatalf("timed out waiting for state %v", want)
			}
		}
	}
	if s := wait(msync.StateFailed); !strings.Contains(s.Err.Error(), "sign in again") {
		t.Fatalf("first failure = %v", s.Err)
	}
	eng.Retry()
	wait(msync.StateConnected)

	if reads != 1 {
		t.Errorf("sign-in read %d times, want 1", reads)
	}
	mu.Lock()
	defer mu.Unlock()
	last := forms[len(forms)-1]
	if last.Get("refresh_token") != "rt-new" || last.Get("client_secret") != "client-secret-2" {
		t.Errorf("token request after Retry = %v", last)
	}
}

func TestBuildAccountGmailNeedsStore(t *testing.T) {
	f := gmailtest.New()
	defer f.Close()
	b := &accountBuilder{ctx: context.Background(), events: make(chan msync.Event, 8), gmail: fakeGmailFactory(f)}
	acct := gmailAccount()
	if _, err := b.buildAccount(&acct, signedIn("gm")); err == nil {
		t.Fatal("a gmail account without the local store must not be built")
	}
}

func TestBuildAccountGmailNotSignedIn(t *testing.T) {
	b := &accountBuilder{ctx: context.Background(), events: make(chan msync.Event, 8)}
	acct := gmailAccount()
	r := signedIn("gm")
	r.Gmail.RefreshToken = creds.Secret{}
	if _, err := b.buildAccount(&acct, r); err == nil || !strings.Contains(err.Error(), "m9s login gm") {
		t.Fatalf("err = %v, want a hint to sign in", err)
	}
}

func TestBuildAccountIMAP(t *testing.T) {
	b := &accountBuilder{ctx: context.Background(), events: make(chan msync.Event, 8)}
	acct := config.Account{Name: "a"}
	got, err := b.buildAccount(&acct, found("a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.(*msync.Engine); !ok {
		t.Fatalf("buildAccount = %T, want *sync.Engine", got)
	}
}

// The refresh hook given to the Gmail client forces a new access token: a
// 401 on the cached one is answered with a fresh token, not the same one.
func TestGmailClientRefreshesOn401(t *testing.T) {
	f := gmailtest.New()
	defer f.Close()
	ts := &tokenServer{}
	ts.install(f)
	var mu gosync.Mutex
	var auths []string
	f.Handle("/gmail/v1/users/me/profile", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") == "Bearer at-1" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"code":401,"status":"UNAUTHENTICATED"}}`)
			return
		}
		fmt.Fprint(w, `{"emailAddress":"me@example.com","historyId":"1"}`)
	})
	acct := gmailAccount()
	c := fakeGmailFactory(f).client(context.Background(), &acct, signedIn("gm"))

	email, _, err := c.Profile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if email != "me@example.com" {
		t.Errorf("email = %q", email)
	}
	if strings.Join(auths, ",") != "Bearer at-1,Bearer at-2" {
		t.Errorf("authorizations = %v, want at-1 then a fresh at-2", auths)
	}
	if ts.count() != 2 {
		t.Errorf("token requests = %d, want 2", ts.count())
	}
	form := ts.forms[0]
	if form.Get("refresh_token") != "rt-secret" || form.Get("client_secret") != "client-secret" ||
		form.Get("client_id") != "cid.apps.googleusercontent.com" {
		t.Errorf("token request form = %v", form)
	}
}

func checkGmail(t *testing.T, f *gmailtest.Fake, r creds.Result) (string, bool) {
	t.Helper()
	acct := gmailAccount()
	deps := checkDeps{
		imap: func(*config.IMAPConfig, string) (int, error) {
			t.Error("IMAP login for a gmail account")
			return 0, nil
		},
		smtp:  func(*config.SMTPConfig, string) error { t.Error("SMTP login for a gmail account"); return nil },
		gmail: gmailProfile(fakeGmailFactory(f)),
	}
	var out bytes.Buffer
	failed := reportCheck([]config.Account{acct}, []creds.Result{r}, deps, &out)
	return out.String(), failed
}

func TestCheckGmailSignedIn(t *testing.T) {
	f := gmailtest.New()
	defer f.Close()
	ts := &tokenServer{}
	ts.install(f)

	out, failed := checkGmail(t, f, signedIn("gm"))
	if failed {
		t.Errorf("failed although signed in:\n%s", out)
	}
	if !strings.Contains(out, "[gm] gmail: ✓ signed in as me@example.com") {
		t.Errorf("output:\n%s", out)
	}
	if strings.Contains(out, "IMAP") || strings.Contains(out, "SMTP") {
		t.Errorf("IMAP/SMTP lines for a gmail account:\n%s", out)
	}
	if ts.count() != 1 {
		t.Errorf("token requests = %d, want 1 (the refresh)", ts.count())
	}
	if strings.Contains(out, "rt-secret") || strings.Contains(out, "client-secret") || strings.Contains(out, "at-1") {
		t.Errorf("a secret was printed:\n%s", out)
	}
}

func TestCheckGmailOtherAddress(t *testing.T) {
	f := gmailtest.New()
	defer f.Close()
	(&tokenServer{}).install(f)
	f.Email = "someone@else.com"
	out, failed := checkGmail(t, f, signedIn("gm"))
	if !failed || !strings.Contains(out, "✗ signed in as someone@else.com, config expects me@example.com") {
		t.Errorf("failed=%v output:\n%s", failed, out)
	}
}

func TestCheckGmailSignInAgain(t *testing.T) {
	f := gmailtest.New()
	defer f.Close()
	(&tokenServer{fail: "invalid_grant"}).install(f)
	out, failed := checkGmail(t, f, signedIn("gm"))
	if !failed || !strings.Contains(out, "[gm] gmail: ✗ sign in again (run: m9s login gm)") {
		t.Errorf("failed=%v output:\n%s", failed, out)
	}
	if strings.Contains(out, "rt-secret") || strings.Contains(out, "secret detail") {
		t.Errorf("the token endpoint body was printed:\n%s", out)
	}
}

func TestCheckGmailNotSignedIn(t *testing.T) {
	f := gmailtest.New()
	defer f.Close()
	r := signedIn("gm")
	r.Gmail.RefreshToken = creds.Secret{}
	r.Failures = []creds.Update{{Account: "gm", Service: creds.ServiceToken, Source: creds.KeePass,
		State: creds.Failed, Reason: "no entry"}}
	out, failed := checkGmail(t, f, r)
	if !failed {
		t.Error("an account without a refresh token must fail the check")
	}
	for _, want := range []string{"[gm] gmail: ✗ not signed in", "keepassxc: no entry"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if len(f.Requests()) != 0 {
		t.Errorf("Gmail was asked although not signed in: %v", f.Requests())
	}
}

func TestSendGmail(t *testing.T) {
	f := gmailtest.New()
	defer f.Close()
	(&tokenServer{}).install(f)
	acct := gmailAccount()
	d := smtpadapter.Draft{To: []string{"you@example.com"}, Subject: "hello", Body: "test body"}

	if err := sendGmail(context.Background(), &acct, signedIn("gm"), fakeGmailFactory(f), d); err != nil {
		t.Fatal(err)
	}
	if n := f.Count("/gmail/v1/users/me/messages/send"); n != 1 {
		t.Fatalf("sends = %d, want 1", n)
	}
	ids := f.MessageIDs("SENT")
	if len(ids) != 1 {
		t.Fatalf("sent messages = %v", ids)
	}
	m, _ := f.Message(ids[0])
	raw := string(m.Raw)
	for _, want := range []string{"From: <me@example.com>", "To: you@example.com", "Subject: hello", "test body"} {
		if !strings.Contains(raw, want) {
			t.Errorf("missing %q in sent message:\n%s", want, raw)
		}
	}
}

func TestSendGmailNotSignedIn(t *testing.T) {
	f := gmailtest.New()
	defer f.Close()
	acct := gmailAccount()
	r := signedIn("gm")
	r.Gmail.ClientSecret = creds.Secret{}
	err := sendGmail(context.Background(), &acct, r, fakeGmailFactory(f), smtpadapter.Draft{To: []string{"x@y"}, Subject: "s"})
	if err == nil || !strings.Contains(err.Error(), "m9s login gm") {
		t.Fatalf("err = %v", err)
	}
	if len(f.Requests()) != 0 {
		t.Errorf("Gmail was asked although not signed in")
	}
}

func TestStartupWarningsShowFirstBuildFailure(t *testing.T) {
	var shown, logged []string
	startupWarnings([]string{"[gm] left out: a gmail account needs the local mail store", "[x] left out: y"},
		[]string{"theme: bad colour"},
		func(s string) { shown = append(shown, s) }, func(s string) { logged = append(logged, s) })
	if strings.Join(shown, "|") != "[gm] left out: a gmail account needs the local mail store" {
		t.Errorf("status bar = %v, want the first left-out account", shown)
	}
	// The shown one is logged by StatusBar.Error itself; the rest here.
	if strings.Join(logged, "|") != "[x] left out: y|theme: bad colour" {
		t.Errorf("log = %v", logged)
	}
}

func TestStartupWarningsThemeOnly(t *testing.T) {
	var shown, logged []string
	startupWarnings(nil, []string{"t1", "t2"},
		func(s string) { shown = append(shown, s) }, func(s string) { logged = append(logged, s) })
	if strings.Join(shown, "|") != "t1" || strings.Join(logged, "|") != "t2" {
		t.Errorf("shown = %v, logged = %v", shown, logged)
	}
}

func TestGmailTransportTimeouts(t *testing.T) {
	hc := gmailHTTPClient(&refreshingSource{})
	if hc.Timeout != 10*time.Minute {
		t.Errorf("overall timeout = %v, want 10m (big downloads and sends)", hc.Timeout)
	}
	ot, ok := hc.Transport.(*oauth2.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *oauth2.Transport", hc.Transport)
	}
	base, ok := ot.Base.(*http.Transport)
	if !ok {
		t.Fatalf("base transport = %T, want *http.Transport", ot.Base)
	}
	if base.ResponseHeaderTimeout != 60*time.Second {
		t.Errorf("response header timeout = %v, want 60s", base.ResponseHeaderTimeout)
	}
	if base == http.DefaultTransport {
		t.Error("the shared default transport was modified")
	}
	if tc := gmailTokenHTTPClient(); tc.Timeout != 60*time.Second {
		t.Errorf("token client timeout = %v, want 60s", tc.Timeout)
	}
}
