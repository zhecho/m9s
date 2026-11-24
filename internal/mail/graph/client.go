// Package graph is a minimal Microsoft Graph mail client: plain REST over
// net/http, no SDK.
package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const (
	defaultBase = "https://graph.microsoft.com"
	apiVersion  = "/v1.0"
	maxTries    = 5
	baseBackoff = 500 * time.Millisecond
	maxBackoff  = 8 * time.Second
	// Outlook allows four requests at a time per app and mailbox, and
	// 10,000 per ten minutes (about 16 a second).
	maxInFlight  = 4
	requestsPerS = 16
	burst        = 100

	// immutableIDs makes every id Graph returns stay the same when the
	// message moves to another folder.
	immutableIDs = `IdType="ImmutableId"`
)

var (
	// ErrSignInAgain means the refresh token is no longer valid
	// (invalid_grant, or 401 again after a refresh).
	ErrSignInAgain = errors.New("sign in again")
	// ErrNotFound means Graph answered 404.
	ErrNotFound = errors.New("not found")
	// ErrSyncReset means a delta link is no longer valid: the folder has
	// to be listed again from scratch.
	ErrSyncReset = errors.New("delta sync state expired")
)

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API host (tests); "/v1.0" is added to it.
func WithBaseURL(u string) Option { return func(c *Client) { c.root = strings.TrimRight(u, "/") } }

// WithRefresh sets a hook that forces a token refresh. On the first 401 of a
// request it is called and the request retried once.
func WithRefresh(f func(ctx context.Context) error) Option { return func(c *Client) { c.refresh = f } }

// WithRateLimit replaces the request rate (per second) and burst, which
// default to Outlook's limits. Tests use it to run many quick requests.
func WithRateLimit(perSecond, burst int) Option {
	return func(c *Client) { c.lim = newLimiter(perSecond, burst) }
}

// Client talks to Microsoft Graph as the signed-in user ("me").
type Client struct {
	root    string // scheme and host
	hc      *http.Client
	sem     chan struct{}
	lim     *limiter
	sleep   func(context.Context, time.Duration) error
	refresh func(context.Context) error
}

// New wraps an (OAuth-authorised) http.Client.
func New(httpClient *http.Client, opts ...Option) *Client {
	c := &Client{
		root:  defaultBase,
		hc:    httpClient,
		sem:   make(chan struct{}, maxInFlight),
		lim:   newLimiter(requestsPerS, burst),
		sleep: sleepCtx,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// limiter is a token bucket: up to burst requests at once, then rate a second.
type limiter struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
	rate   float64
	burst  float64
}

func newLimiter(rate, burst int) *limiter {
	return &limiter{tokens: float64(burst), last: time.Now(), rate: float64(rate), burst: float64(burst)}
}

func (l *limiter) wait(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := time.Now()
		l.tokens = min(l.burst, l.tokens+now.Sub(l.last).Seconds()*l.rate)
		l.last = now
		if l.tokens >= 1 {
			l.tokens--
			l.mu.Unlock()
			return nil
		}
		need := time.Duration((1 - l.tokens) / l.rate * float64(time.Second))
		l.mu.Unlock()
		if err := sleepCtx(ctx, need); err != nil {
			return err
		}
	}
}

// APIError carries only the status code and Graph's error code, never the
// message or the body.
type APIError struct {
	Code   int
	Status string // Graph's error.code, e.g. "ErrorItemNotFound"
}

func (e *APIError) Error() string {
	if e.Status == "" {
		return fmt.Sprintf("graph: http %d", e.Code)
	}
	return fmt.Sprintf("graph: http %d %s", e.Code, e.Status)
}

func (e *APIError) Unwrap() error {
	switch {
	case e.Code == http.StatusGone || strings.HasPrefix(strings.ToLower(e.Status), "syncstate"):
		return ErrSyncReset
	case e.Code == http.StatusNotFound:
		return ErrNotFound
	case e.Code == http.StatusUnauthorized:
		return ErrSignInAgain
	}
	return nil
}

func retryable(code int) bool {
	switch code {
	case 429, 500, 502, 503, 504:
		return true
	}
	return false
}

func errorCode(b []byte) string {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(b, &env)
	return env.Error.Code
}

func apiError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return &APIError{Code: resp.StatusCode, Status: errorCode(b)}
}

func retryAfter(v string) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	return 0
}

func backoff(try int) time.Duration {
	return min(baseBackoff<<try, maxBackoff)
}

func isInvalidGrant(err error) bool {
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) {
		return false
	}
	return re.ErrorCode == "invalid_grant" || strings.Contains(string(re.Body), "invalid_grant")
}

func (c *Client) acquire(ctx context.Context) error {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := c.lim.wait(ctx); err != nil {
		<-c.sem
		return err
	}
	return nil
}

// request is one API call. url is absolute (see c.url and c.link).
type request struct {
	method      string
	url         string
	body        []byte
	contentType string
	header      map[string]string
	idempotent  bool // false: only retried on 429, which Graph answers before acting
	out         any  // JSON answer is decoded into it; nil discards it
	raw         *[]byte
}

// url is the absolute URL of an API path such as "/me/messages".
func (c *Client) url(path string) string { return c.root + apiVersion + path }

// link checks a URL Graph handed back (nextLink, deltaLink) before it is
// followed: it must point at the API, so the token never leaves it.
func (c *Client) link(u string) (string, error) {
	if strings.HasPrefix(u, c.root+apiVersion+"/") {
		return u, nil
	}
	host := u
	if _, rest, ok := strings.Cut(u, "://"); ok {
		host, _, _ = strings.Cut(rest, "/")
	}
	return "", fmt.Errorf("graph: refusing to follow a link to %s", host)
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	return c.call(ctx, request{method: http.MethodGet, url: c.url(path), out: out, idempotent: true})
}

// sendJSON sends in (nil: no body) as JSON to path and decodes the answer
// into out (nil: ignore it).
func (c *Client) sendJSON(ctx context.Context, method, path string, in, out any, idempotent bool) error {
	r := request{method: method, url: c.url(path), out: out, idempotent: idempotent}
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("graph: encode request: %w", err)
		}
		r.body, r.contentType = b, "application/json"
	}
	return c.call(ctx, r)
}

// call runs one API request with refresh-on-401 and retries. The body is
// re-read from the start on every try.
func (c *Client) call(ctx context.Context, r request) error {
	var err error
	refreshed := false
	for try := 0; try < maxTries; try++ {
		var wait time.Duration
		wait, err = c.once(ctx, r)
		if err == nil {
			return nil
		}
		var ae *APIError
		if errors.As(err, &ae) && ae.Code == http.StatusUnauthorized && c.refresh != nil && !refreshed {
			refreshed = true
			if rerr := c.refresh(ctx); rerr != nil {
				return c.transportErr(ctx, rerr)
			}
			try--
			continue
		}
		if !errors.As(err, &ae) || !retryable(ae.Code) || try == maxTries-1 {
			return err
		}
		if !r.idempotent && ae.Code != http.StatusTooManyRequests {
			return err
		}
		if wait == 0 {
			wait = backoff(try)
		}
		if serr := c.sleep(ctx, wait); serr != nil {
			return serr
		}
	}
	return err
}

func (c *Client) once(ctx context.Context, r request) (time.Duration, error) {
	if err := c.acquire(ctx); err != nil {
		return 0, err
	}
	defer func() { <-c.sem }()
	var rd io.Reader
	if r.body != nil {
		rd = bytes.NewReader(r.body) // a fresh reader per try
	}
	req, err := http.NewRequestWithContext(ctx, r.method, r.url, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Prefer", immutableIDs)
	if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}
	for k, v := range r.header {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, c.transportErr(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return retryAfter(resp.Header.Get("Retry-After")), apiError(resp)
	}
	switch {
	case r.raw != nil:
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, c.transportErr(ctx, err)
		}
		*r.raw = b
	case r.out != nil:
		if err := json.NewDecoder(resp.Body).Decode(r.out); err != nil {
			return 0, fmt.Errorf("graph: decode response: %w", err)
		}
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	}
	return 0, nil
}

// transportErr converts a transport/refresh failure into an error that never
// contains an HTTP body.
func (c *Client) transportErr(ctx context.Context, err error) error {
	if isInvalidGrant(err) {
		return ErrSignInAgain
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		status := 0
		if re.Response != nil {
			status = re.Response.StatusCode
		}
		return fmt.Errorf("graph: token refresh failed: http %d %s", status, re.ErrorCode)
	}
	return fmt.Errorf("graph: request failed: %w", err)
}

// page is one page of a collection.
type page[T any] struct {
	Value     []T    `json:"value"`
	NextLink  string `json:"@odata.nextLink"`
	DeltaLink string `json:"@odata.deltaLink"`
}

// collect GETs url and every page after it, up to max items (0: all).
func collect[T any](ctx context.Context, c *Client, url string, header map[string]string, max int) ([]T, error) {
	var all []T
	for url != "" {
		var p page[T]
		if err := c.call(ctx, request{method: http.MethodGet, url: url, header: header, out: &p, idempotent: true}); err != nil {
			return nil, err
		}
		all = append(all, p.Value...)
		if max > 0 && len(all) >= max {
			return all[:max], nil
		}
		if p.NextLink == "" {
			break
		}
		var err error
		if url, err = c.link(p.NextLink); err != nil {
			return nil, err
		}
	}
	return all, nil
}

// Me returns the signed-in account's address: its mail, else its sign-in name.
func (c *Client) Me(ctx context.Context) (string, error) {
	var me struct {
		Mail string `json:"mail"`
		UPN  string `json:"userPrincipalName"`
	}
	if err := c.getJSON(ctx, "/me?$select=mail,userPrincipalName", &me); err != nil {
		return "", err
	}
	if me.Mail != "" {
		return me.Mail, nil
	}
	return me.UPN, nil
}

// APIInfo describes the API m9s talks to. Graph has no call that names a
// server version, so this is the version the client is written against.
func (c *Client) APIInfo(context.Context) (string, error) {
	return "Microsoft Graph / v1.0", nil
}
