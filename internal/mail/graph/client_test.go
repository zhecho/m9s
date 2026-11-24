package graph

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/zhecho/m9s/internal/mail/graph/graphtest"
)

const mePath = "/v1.0/me"

func newTest(t *testing.T) (*Client, *graphtest.Fake, *[]time.Duration) {
	t.Helper()
	f := graphtest.New()
	t.Cleanup(f.Close)
	var sleeps []time.Duration
	c := New(f.Server.Client(), WithBaseURL(f.URL()))
	c.sleep = func(ctx context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil }
	return c, f, &sleeps
}

func TestClient_sendsImmutableIDHeader(t *testing.T) {
	c, f, _ := newTest(t)
	ctx := context.Background()
	inbox := f.FolderID("inbox")
	id := f.AddMessage(inbox, graphtest.Message{Subject: "a", Raw: []byte("Subject: a\r\n\r\nx")})
	if _, err := c.Me(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Folders(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := c.Delta(ctx, inbox, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Raw(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := c.PermanentDeleteAll(ctx, []string{id}); err != nil {
		t.Fatal(err)
	}
	reqs := f.Requests()
	if len(reqs) < 5 {
		t.Fatalf("only %d requests logged", len(reqs))
	}
	for _, r := range reqs {
		if p := r.Header.Get("Prefer"); !strings.Contains(p, `IdType="ImmutableId"`) {
			t.Errorf("%s %s: Prefer = %q", r.Method, r.Path, p)
		}
	}
	// The requests inside a $batch carry it too.
	for _, r := range reqs {
		if r.Path == "/v1.0/$batch" && !strings.Contains(r.Body, `IdType=\"ImmutableId\"`) {
			t.Errorf("batch body without the header: %s", r.Body)
		}
	}
}

func TestClient_retryAfter(t *testing.T) {
	c, f, sleeps := newTest(t)
	f.Queue(mePath, 429, `{"error":{"code":"TooManyRequests"}}`, map[string]string{"Retry-After": "1"})
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := f.Count(mePath); n != 2 {
		t.Fatalf("calls = %d", n)
	}
	if !slices.Equal(*sleeps, []time.Duration{time.Second}) {
		t.Fatalf("sleeps = %v", *sleeps)
	}
}

func TestClient_backoffWithoutRetryAfter(t *testing.T) {
	c, f, sleeps := newTest(t)
	f.Queue(mePath, 503, `{}`, nil)
	f.Queue(mePath, 503, `{}`, nil)
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*sleeps, []time.Duration{500 * time.Millisecond, time.Second}) {
		t.Fatalf("sleeps = %v", *sleeps)
	}
}

func TestClient_givesUpAfterFiveTries(t *testing.T) {
	c, f, _ := newTest(t)
	for i := 0; i < 6; i++ {
		f.Queue(mePath, 500, `{"error":{"code":"generalException","message":"SECRET"}}`, nil)
	}
	_, err := c.Me(context.Background())
	if err == nil || !strings.Contains(err.Error(), "generalException") || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
	if n := f.Count(mePath); n != 5 {
		t.Fatalf("calls = %d", n)
	}
}

func TestClient_sendNotRetriedOn503(t *testing.T) {
	c, f, _ := newTest(t)
	f.Queue("/v1.0/me/sendMail", 503, `{}`, nil)
	if err := c.SendMIME(context.Background(), []byte("Subject: x\r\n\r\nx")); err == nil {
		t.Fatal("want error")
	}
	if n := f.Count("/v1.0/me/sendMail"); n != 1 {
		t.Fatalf("calls = %d: a send that may have gone out was repeated", n)
	}
}

func TestClient_notFound(t *testing.T) {
	c, _, _ := newTest(t)
	_, err := c.Get(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestClient_401RefreshesThenRetriesOnce(t *testing.T) {
	c, f, _ := newTest(t)
	n := 0
	c.refresh = func(context.Context) error { n++; return nil }
	f.Queue(mePath, 401, `{}`, nil)
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n != 1 || f.Count(mePath) != 2 {
		t.Fatalf("refreshes=%d calls=%d", n, f.Count(mePath))
	}
}

func TestClient_second401IsErrSignInAgain(t *testing.T) {
	c, f, _ := newTest(t)
	c.refresh = func(context.Context) error { return nil }
	f.Queue(mePath, 401, `{}`, nil)
	f.Queue(mePath, 401, `{}`, nil)
	if _, err := c.Me(context.Background()); !errors.Is(err, ErrSignInAgain) {
		t.Fatalf("err = %v", err)
	}
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClient_invalidGrantIsErrSignInAgain(t *testing.T) {
	hc := &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		return nil, &oauth2.RetrieveError{
			Response:  &http.Response{StatusCode: 400},
			Body:      []byte(`{"error":"invalid_grant","error_description":"SECRET"}`),
			ErrorCode: "invalid_grant",
		}
	})}
	c := New(hc, WithBaseURL("http://example.invalid"))
	_, err := c.Me(context.Background())
	if !errors.Is(err, ErrSignInAgain) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
}

func TestClient_syncStateCodeIsErrSyncReset(t *testing.T) {
	c, f, _ := newTest(t)
	inbox := f.FolderID("inbox")
	f.Queue("/v1.0/me/mailFolders/"+inbox+"/messages/delta", 400, `{"error":{"code":"SyncStateNotFound"}}`, nil)
	if _, _, _, err := c.Delta(context.Background(), inbox, ""); !errors.Is(err, ErrSyncReset) {
		t.Fatalf("err = %v", err)
	}
}

// A link from a response is only followed when it points back at the API:
// the bearer token must never go to another host.
func TestClient_refusesForeignLinks(t *testing.T) {
	c, f, _ := newTest(t)
	f.Queue("/v1.0/me/mailFolders", 200, `{"value":[],"@odata.nextLink":"https://evil.example/v1.0/me/mailFolders?$skip=1"}`, nil)
	want := "graph: refusing to follow a link to evil.example"
	if _, err := c.Folders(context.Background()); err == nil || err.Error() != want {
		t.Fatalf("err = %v", err)
	}
	if _, _, _, err := c.Delta(context.Background(), f.FolderID("inbox"), "https://evil.example/x"); err == nil || err.Error() != want {
		t.Fatalf("delta link: err = %v", err)
	}
}

func TestClient_searchQuotes(t *testing.T) {
	c, f, _ := newTest(t)
	if _, err := c.Search(context.Background(), `subject:"q3 report" a\b`); err != nil {
		t.Fatal(err)
	}
	var got *graphtest.Request
	for _, r := range f.Requests() {
		if r.Path == "/v1.0/me/messages" && r.Method == http.MethodGet {
			got = &r
		}
	}
	if got == nil {
		t.Fatal("no search request")
	}
	q, err := url.ParseQuery(got.Query)
	if err != nil {
		t.Fatal(err)
	}
	if s := q.Get("$search"); s != `"subject:\"q3 report\" a\\b"` {
		t.Errorf("$search = %s", s)
	}
	if strings.Contains(got.Query, "+") {
		t.Errorf("query %q encodes spaces as +", got.Query)
	}
	if h := got.Header.Get("ConsistencyLevel"); h != "eventual" {
		t.Errorf("ConsistencyLevel = %q", h)
	}
}

func TestClient_APIInfo(t *testing.T) {
	c, f, _ := newTest(t)
	got, err := c.APIInfo(context.Background())
	if err != nil || got != "Microsoft Graph / v1.0" {
		t.Fatalf("APIInfo = %q, %v", got, err)
	}
	if n := len(f.Requests()); n != 0 {
		t.Errorf("APIInfo made %d requests", n)
	}
}

func TestOAuthConfig(t *testing.T) {
	cfg := OAuthConfig("cid")
	if cfg.ClientID != "cid" || cfg.ClientSecret != "" {
		t.Fatalf("client = %q / secret %q", cfg.ClientID, cfg.ClientSecret)
	}
	want := []string{"offline_access", "Mail.ReadWrite", "Mail.Send", "MailboxSettings.Read", "User.Read"}
	if !slices.Equal(cfg.Scopes, want) {
		t.Errorf("scopes = %v", cfg.Scopes)
	}
	for _, raw := range []string{cfg.Endpoint.AuthURL, cfg.Endpoint.TokenURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Host != "login.microsoftonline.com" || !strings.Contains(u.Path, "/common/") {
			t.Errorf("endpoint %q", raw)
		}
	}
	if cfg.Endpoint.AuthStyle != oauth2.AuthStyleInParams {
		t.Errorf("AuthStyle = %v: a public client sends its id in the form", cfg.Endpoint.AuthStyle)
	}
}

// WithRateLimit replaces Outlook's limits (tests run many quick requests).
func TestClient_rateLimitOption(t *testing.T) {
	f := graphtest.New()
	t.Cleanup(f.Close)
	c := New(f.Server.Client(), WithBaseURL(f.URL()), WithRateLimit(1, 1))
	start := time.Now()
	for range 3 {
		if _, err := c.Me(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 1500*time.Millisecond {
		t.Fatalf("3 requests at 1/s took %v", d)
	}
}
