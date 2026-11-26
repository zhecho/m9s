package gmail

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Gmail allows each user 6,000 quota units a minute per Cloud project
// (developers.google.com/workspace/gmail/api/reference/quota); each method
// costs a fixed number of units. The client spends at most quotaBurst at
// once and then quotaRate a second, so a minute never uses more than
// quotaBurst + 60*quotaRate = 5,700. Background work (the sync) leaves
// quotaReserve in the bucket for what the user does.
const (
	quotaRate    = 85.0  // units a second
	quotaBurst   = 600.0 // units
	quotaReserve = 150.0 // units kept for the user: covers a send (100)
)

type backgroundKey struct{}

// Background marks ctx as background work: its requests wait until the
// quota has more than the reserve left, so the user's requests go first.
func Background(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundKey{}, true)
}

func isBackground(ctx context.Context) bool {
	b, _ := ctx.Value(backgroundKey{}).(bool)
	return b
}

// WithQuota replaces the quota (units a second, burst and background
// reserve), which default to Gmail's per-user limit. Tests use it to run
// many quick requests.
func WithQuota(perSecond, burst, reserve float64) Option {
	return func(c *Client) { c.quota = newQuota(perSecond, burst, reserve) }
}

// quota is a token bucket of Gmail quota units.
type quota struct {
	mu                   sync.Mutex
	units                float64 // free now; below 0 after a drain
	last                 time.Time
	rate, burst, reserve float64

	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

func newQuota(rate, burst, reserve float64) *quota {
	return &quota{units: burst, last: time.Now(), rate: rate, burst: burst, reserve: reserve,
		now: time.Now, sleep: sleepCtx}
}

// take waits until cost units are free — plus the reserve, for background
// work — and spends them.
func (q *quota) take(ctx context.Context, cost int) error {
	need := float64(cost)
	if isBackground(ctx) {
		need += q.reserve
	}
	need = min(need, q.burst) // a request dearer than the bucket still goes once it is full
	for {
		q.mu.Lock()
		now := q.now()
		q.units = min(q.burst, q.units+now.Sub(q.last).Seconds()*q.rate)
		q.last = now
		if q.units >= need {
			q.units -= float64(cost)
			q.mu.Unlock()
			return nil
		}
		wait := time.Duration((need - q.units) / q.rate * float64(time.Second))
		q.mu.Unlock()
		if err := q.sleep(ctx, wait); err != nil {
			return err
		}
	}
}

// drain empties the bucket after Gmail said the quota is used up.
func (q *quota) drain() {
	q.mu.Lock()
	q.units = min(q.units, 0)
	q.mu.Unlock()
}

// cost is the quota units of one request: method and path relative to
// /gmail/v1/users/me, with its query.
func cost(method, path string) int {
	path, rawQuery, _ := strings.Cut(path, "?")
	query, _ := url.ParseQuery(rawQuery)
	parts := strings.Split(strings.Trim(path, "/"), "/")
	full := func() bool { // the default format is full
		f := query.Get("format")
		return f == "" || f == "full" || f == "raw" || f == "html"
	}
	switch parts[0] {
	case "profile":
		return 1
	case "history":
		return 2
	case "labels":
		if method == http.MethodGet {
			return 1
		}
		return 5
	case "messages":
		if len(parts) == 1 {
			return 5 // list
		}
		switch parts[1] {
		case "send":
			return 100
		case "batchDelete", "batchModify":
			return 50
		case "import", "insert":
			return 25
		}
		if len(parts) == 2 {
			switch method {
			case http.MethodGet:
				if full() {
					return 60
				}
				return 20
			case http.MethodDelete:
				return 10
			}
		}
		switch parts[len(parts)-1] {
		case "modify", "untrash":
			return 5
		case "trash":
			return 20
		}
	case "threads":
		if len(parts) == 1 {
			return 10 // list
		}
		if len(parts) == 2 {
			switch method {
			case http.MethodGet:
				if query.Get("format") == "" || query.Get("format") == "full" {
					return 120
				}
				return 40
			case http.MethodDelete:
				return 20
			}
		}
		switch parts[len(parts)-1] {
		case "modify", "untrash":
			return 10
		case "trash":
			return 20
		}
	case "drafts":
		if len(parts) == 1 {
			if method == http.MethodGet {
				return 5 // list
			}
			return 10 // create
		}
		if parts[1] == "send" {
			return 100
		}
		switch method {
		case http.MethodGet:
			if full() {
				return 60
			}
			return 20
		case http.MethodPut:
			return 15
		case http.MethodDelete:
			return 10
		}
	}
	return 20 // unknown: as much as a message fetch
}
