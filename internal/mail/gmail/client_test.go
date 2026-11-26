package gmail

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/zhecho/m9s/internal/mail/gmail/gmailtest"
)

const profilePath = "/gmail/v1/users/me/profile"

func newTest(t *testing.T) (*Client, *gmailtest.Fake, *[]time.Duration) {
	t.Helper()
	f := gmailtest.New()
	t.Cleanup(f.Close)
	var sleeps []time.Duration
	c := New(f.Server.Client(), WithBaseURL(f.URL()))
	c.sleep = func(ctx context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil }
	return c, f, &sleeps
}

func TestProfile(t *testing.T) {
	c, _, _ := newTest(t)
	email, hid, err := c.Profile(context.Background())
	if err != nil || email != "me@example.com" || hid != "12345" {
		t.Fatalf("got %q %q %v", email, hid, err)
	}
}

func TestClient_RetriesOn429And5xx(t *testing.T) {
	c, f, sleeps := newTest(t)
	f.QueueHeaders(profilePath, 429, `{}`, map[string]string{"Retry-After": "3"})
	f.Queue(profilePath, 503, `{}`)
	if _, _, err := c.Profile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := f.Count(profilePath); n != 3 {
		t.Fatalf("calls = %d", n)
	}
	if len(*sleeps) != 2 || (*sleeps)[0] != 3*time.Second || (*sleeps)[1] != time.Second {
		t.Fatalf("sleeps = %v", *sleeps)
	}
}

// Gmail answers a user going over the per-second quota with 403 and a
// rate-limit reason: that is waited out like a 429, also for a request that
// is not safe to repeat (Google refused it before doing anything).
func TestClient_RetriesOn403RateLimit(t *testing.T) {
	c, f, sleeps := newTest(t)
	f.Queue(profilePath, 403, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"User-rate limit exceeded.",`+
		`"errors":[{"domain":"usageLimits","reason":"userRateLimitExceeded","message":"User-rate limit exceeded."}]}}`)
	f.Queue(profilePath, 403, `{"error":{"code":403,"status":"PERMISSION_DENIED",`+
		`"errors":[{"domain":"usageLimits","reason":"rateLimitExceeded"}]}}`)
	if err := c.sendJSON(context.Background(), http.MethodGet, "/profile", nil, nil, false); err != nil {
		t.Fatal(err)
	}
	if n := f.Count(profilePath); n != 3 {
		t.Fatalf("calls = %d", n)
	}
	if len(*sleeps) != 2 {
		t.Fatalf("sleeps = %v", *sleeps)
	}
}

// A 403 that is not a rate limit is not retried, and its reason is in the
// error so the log says what Google refused (never the message text).
func TestClient_403NamesTheReason(t *testing.T) {
	c, f, _ := newTest(t)
	f.Queue(profilePath, 403, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"SECRET",`+
		`"errors":[{"domain":"global","reason":"insufficientPermissions","message":"SECRET"}]}}`)
	_, _, err := c.Profile(context.Background())
	if err == nil || !strings.Contains(err.Error(), "403 PERMISSION_DENIED (insufficientPermissions)") || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
	if n := f.Count(profilePath); n != 1 {
		t.Fatalf("calls = %d, want no retry", n)
	}
}

// A reason that is not a plain token is left out of the error.
func TestClient_ReasonMustBeAToken(t *testing.T) {
	c, f, _ := newTest(t)
	f.Queue(profilePath, 403, `{"error":{"code":403,"status":"PERMISSION_DENIED","errors":[{"reason":"SECRET with spaces"}]}}`)
	_, _, err := c.Profile(context.Background())
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
}

func TestClient_GivesUpAfterFiveTries(t *testing.T) {
	c, f, _ := newTest(t)
	for i := 0; i < 6; i++ {
		f.Queue(profilePath, 500, `{"error":{"code":500,"status":"INTERNAL"}}`)
	}
	_, _, err := c.Profile(context.Background())
	if err == nil || !strings.Contains(err.Error(), "INTERNAL") {
		t.Fatalf("err = %v", err)
	}
	if n := f.Count(profilePath); n != 5 {
		t.Fatalf("calls = %d", n)
	}
}

func TestClient_ErrorHidesBody(t *testing.T) {
	c, f, _ := newTest(t)
	for i := 0; i < 5; i++ {
		f.Queue(profilePath, 500, `{"error":{"code":500,"status":"INTERNAL","message":"SECRET"}}`)
	}
	_, _, err := c.Profile(context.Background())
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
	f.Queue(profilePath, 400, `SECRET not json`)
	_, _, err = c.Profile(context.Background())
	if err == nil || strings.Contains(err.Error(), "SECRET") || !strings.Contains(err.Error(), "400") {
		t.Fatalf("err = %v", err)
	}
}

func TestClient_NotFound(t *testing.T) {
	c, f, _ := newTest(t)
	f.Queue(profilePath, 404, `{"error":{"code":404,"status":"NOT_FOUND"}}`)
	_, _, err := c.Profile(context.Background())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestClient_Unauthorized(t *testing.T) {
	c, f, _ := newTest(t)
	f.Queue(profilePath, 401, `{}`)
	_, _, err := c.Profile(context.Background())
	if !errors.Is(err, ErrSignInAgain) {
		t.Fatalf("err = %v", err)
	}
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClient_InvalidGrantIsErrSignInAgain(t *testing.T) {
	hc := &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		return nil, &oauth2.RetrieveError{
			Response:  &http.Response{StatusCode: 400},
			Body:      []byte(`{"error":"invalid_grant","error_description":"SECRET"}`),
			ErrorCode: "invalid_grant",
		}
	})}
	c := New(hc, WithBaseURL("http://example.invalid"))
	_, _, err := c.Profile(context.Background())
	if !errors.Is(err, ErrSignInAgain) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
}

func TestClient_ContextCancelStopsRetry(t *testing.T) {
	f := gmailtest.New()
	defer f.Close()
	for i := 0; i < 5; i++ {
		f.Queue(profilePath, 503, `{}`)
	}
	c := New(f.Server.Client(), WithBaseURL(f.URL()))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.Profile(ctx); err == nil {
		t.Fatal("want error")
	}
}

func TestClient_401RefreshesThenRetriesOnce(t *testing.T) {
	c, f, _ := newTest(t)
	n := 0
	c.refresh = func(context.Context) error { n++; return nil }
	f.Queue(profilePath, 401, `{}`)
	if _, _, err := c.Profile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n != 1 || f.Count(profilePath) != 2 {
		t.Fatalf("refreshes=%d calls=%d", n, f.Count(profilePath))
	}
}

func TestClient_Second401IsErrSignInAgain(t *testing.T) {
	c, f, _ := newTest(t)
	n := 0
	c.refresh = func(context.Context) error { n++; return nil }
	f.Queue(profilePath, 401, `{}`)
	f.Queue(profilePath, 401, `{}`)
	_, _, err := c.Profile(context.Background())
	if !errors.Is(err, ErrSignInAgain) || n != 1 || f.Count(profilePath) != 2 {
		t.Fatalf("err=%v refreshes=%d calls=%d", err, n, f.Count(profilePath))
	}
}

func TestClient_RefreshInvalidGrantIsErrSignInAgain(t *testing.T) {
	c, f, _ := newTest(t)
	c.refresh = func(context.Context) error {
		return &oauth2.RetrieveError{ErrorCode: "invalid_grant", Body: []byte("SECRET")}
	}
	f.Queue(profilePath, 401, `{}`)
	_, _, err := c.Profile(context.Background())
	if !errors.Is(err, ErrSignInAgain) {
		t.Fatalf("err=%v", err)
	}
}

func TestClient_RetrieveErrorHidesBody(t *testing.T) {
	hc := &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		return nil, &oauth2.RetrieveError{
			Response:  &http.Response{StatusCode: 401},
			Body:      []byte(`{"error":"invalid_client","error_description":"SECRET"}`),
			ErrorCode: "invalid_client",
		}
	})}
	c := New(hc, WithBaseURL("http://example.invalid"))
	_, _, err := c.Profile(context.Background())
	if err == nil || strings.Contains(err.Error(), "SECRET") || !strings.Contains(err.Error(), "invalid_client") {
		t.Fatalf("err=%v", err)
	}
}

func TestClient_APIInfo(t *testing.T) {
	f := gmailtest.New()
	defer f.Close()
	f.Handle("/$discovery/rest", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("version") != "v1" {
			t.Errorf("query = %q, want version=v1", r.URL.RawQuery)
		}
		fmt.Fprint(w, `{"name":"gmail","version":"v1","revision":"20261106","title":"Gmail API","resources":{}}`)
	})
	c := New(f.Server.Client(), WithBaseURL(f.URL()))
	got, err := c.APIInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "Gmail API / v1 (rev 20261106)" {
		t.Errorf("APIInfo = %q", got)
	}
}
