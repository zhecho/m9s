// Package gmail is a minimal Gmail REST client.
package gmail

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
	"time"

	"golang.org/x/oauth2"
)

const (
	defaultBase = "https://gmail.googleapis.com"
	maxTries    = 5
	baseBackoff = 500 * time.Millisecond
	maxBackoff  = 8 * time.Second
	maxInFlight = 8
)

var (
	// ErrSignInAgain means the refresh token is no longer valid (invalid_grant or 401).
	ErrSignInAgain = errors.New("sign in again")
	// ErrNotFound means Gmail answered 404.
	ErrNotFound = errors.New("not found")
)

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API host (tests).
func WithBaseURL(u string) Option { return func(c *Client) { c.base = strings.TrimRight(u, "/") } }

// WithRefresh sets a hook that forces a token refresh. On the first 401 of a
// request it is called and the request retried once.
func WithRefresh(f func(ctx context.Context) error) Option { return func(c *Client) { c.refresh = f } }

// Client talks to the Gmail REST API as user "me".
type Client struct {
	base    string
	hc      *http.Client
	sem     chan struct{}
	quota   *quota
	sleep   func(context.Context, time.Duration) error
	refresh func(context.Context) error
}

// New wraps an (OAuth-authorised) http.Client.
func New(httpClient *http.Client, opts ...Option) *Client {
	c := &Client{
		base:  defaultBase,
		hc:    httpClient,
		sem:   make(chan struct{}, maxInFlight),
		quota: newQuota(quotaRate, quotaBurst, quotaReserve),
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

// APIError carries only the status code, Google's error.status string and
// the first error reason (a plain token such as "userRateLimitExceeded";
// never the message text).
type APIError struct {
	Code   int
	Status string
	Reason string
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("gmail: http %d", e.Code)
	if e.Status != "" {
		s += " " + e.Status
	}
	if e.Reason != "" {
		s += " (" + e.Reason + ")"
	}
	return s
}

// rateLimited reports Gmail refusing a request for going over a per-second
// quota: 429, or 403 with a rate-limit reason. Waiting it out is enough.
func (e *APIError) rateLimited() bool {
	if e.Code == http.StatusTooManyRequests {
		return true
	}
	return e.Code == http.StatusForbidden && (e.Reason == "rateLimitExceeded" || e.Reason == "userRateLimitExceeded")
}

func (e *APIError) Unwrap() error {
	switch e.Code {
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusUnauthorized:
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

func apiError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var env struct {
		Error struct {
			Status string `json:"status"`
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	_ = json.Unmarshal(b, &env)
	ae := &APIError{Code: resp.StatusCode, Status: env.Error.Status}
	if len(env.Error.Errors) > 0 && isToken(env.Error.Errors[0].Reason) {
		ae.Reason = env.Error.Errors[0].Reason
	}
	return ae
}

// isToken accepts a short identifier: letters, digits and underscores.
func isToken(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func isInvalidGrant(err error) bool {
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) {
		return false
	}
	return re.ErrorCode == "invalid_grant" || strings.Contains(string(re.Body), "invalid_grant")
}

// acquire spends units of the quota, then takes a slot of the requests in
// flight. The quota comes first, so no slot is held while waiting for it.
func (c *Client) acquire(ctx context.Context, units int) error {
	if units > 0 {
		if err := c.quota.take(ctx, units); err != nil {
			return err
		}
	}
	select {
	case c.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// getJSON GETs path (relative to /gmail/v1/users/me) and decodes into out.
func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	return c.call(ctx, http.MethodGet, path, nil, out, true)
}

// sendJSON sends in as the JSON body of a method request to path and decodes
// the answer into out (nil: ignore it). in nil sends no body. Requests that
// are not safe to repeat (idempotent false) are only retried on a rate
// limit (429, or 403 rateLimitExceeded), which Google answers before doing
// anything.
func (c *Client) sendJSON(ctx context.Context, method, path string, in, out any, idempotent bool) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return fmt.Errorf("gmail: encode request: %w", err)
		}
	}
	return c.call(ctx, method, path, body, out, idempotent)
}

// call runs one API request with refresh-on-401 and retries. body is
// re-read from the start on every try.
func (c *Client) call(ctx context.Context, method, path string, body []byte, out any, idempotent bool) error {
	var err error
	refreshed := false
	for try := 0; try < maxTries; try++ {
		var wait time.Duration
		wait, err = c.once(ctx, method, path, body, out)
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
		if errors.As(err, &ae) && ae.rateLimited() {
			c.quota.drain() // every request waits, not just this one
		}
		if !errors.As(err, &ae) || !(retryable(ae.Code) || ae.rateLimited()) || try == maxTries-1 {
			return err
		}
		if !idempotent && !ae.rateLimited() {
			return err
		}
		if wait == 0 {
			wait = baseBackoff << try
			if wait > maxBackoff {
				wait = maxBackoff
			}
		}
		if serr := c.sleep(ctx, wait); serr != nil {
			return serr
		}
	}
	return err
}

func (c *Client) once(ctx context.Context, method, path string, body []byte, out any) (time.Duration, error) {
	if err := c.acquire(ctx, cost(method, path)); err != nil {
		return 0, err
	}
	defer func() { <-c.sem }()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body) // a fresh reader per try
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/gmail/v1/users/me"+path, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, c.transportErr(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var wait time.Duration
		if s, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && s > 0 {
			wait = time.Duration(s) * time.Second
		}
		return wait, apiError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return 0, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return 0, fmt.Errorf("gmail: decode response: %w", err)
	}
	return 0, nil
}

// Profile returns the account's address and current history id.
func (c *Client) Profile(ctx context.Context) (email string, historyID string, err error) {
	var p struct {
		EmailAddress string `json:"emailAddress"`
		HistoryID    string `json:"historyId"`
	}
	if err := c.getJSON(ctx, "/profile", &p); err != nil {
		return "", "", err
	}
	return p.EmailAddress, p.HistoryID, nil
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
		return fmt.Errorf("gmail: token refresh failed: http %d %s", status, re.ErrorCode)
	}
	return fmt.Errorf("gmail: request failed: %w", err)
}

// APIInfo describes the API m9s talks to, from Google's public discovery
// document, e.g. "Gmail API / v1 (rev 20261106)". Gmail has no call that
// names a server version; the revision changes as Google ships updates.
func (c *Client) APIInfo(ctx context.Context) (string, error) {
	if err := c.acquire(ctx, 0); err != nil { // the discovery document is not Gmail API quota
		return "", err
	}
	defer func() { <-c.sem }()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/$discovery/rest?version=v1", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", c.transportErr(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", apiError(resp)
	}
	var doc struct {
		Title    string `json:"title"`
		Version  string `json:"version"`
		Revision string `json:"revision"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", fmt.Errorf("gmail: decode discovery: %w", err)
	}
	if doc.Title == "" {
		doc.Title = "Gmail API"
	}
	text := doc.Title + " / " + orDash(doc.Version)
	if doc.Revision != "" {
		text += " (rev " + doc.Revision + ")"
	}
	return text, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
