package gmail

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// The costs are Gmail's quota units per method (developers.google.com/
// workspace/gmail/api/reference/quota).
func TestCost(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/profile", 1},
		{"GET", "/history?startHistoryId=1", 2},
		{"GET", "/labels", 1},
		{"GET", "/labels/INBOX", 1},
		{"GET", "/messages?labelIds=INBOX", 5},
		{"GET", "/messages/m1?format=metadata&metadataHeaders=From", 20},
		{"GET", "/messages/m1?format=minimal", 20},
		{"GET", "/messages/m1?format=raw", 60},
		{"GET", "/messages/m1", 60}, // the default format is full
		{"POST", "/messages/m1/modify", 5},
		{"POST", "/messages/m1/trash", 20},
		{"POST", "/messages/m1/untrash", 5},
		{"DELETE", "/messages/m1", 10},
		{"POST", "/messages/batchDelete", 50},
		{"POST", "/messages/batchModify", 50},
		{"POST", "/messages/send", 100},
		{"GET", "/threads?maxResults=50", 10},
		{"GET", "/threads/t1?format=minimal", 40},
		{"GET", "/threads/t1?format=metadata", 40},
		{"GET", "/threads/t1", 120},
		{"POST", "/threads/t1/modify", 10},
		{"POST", "/threads/t1/trash", 20},
		{"DELETE", "/threads/t1", 20},
		{"GET", "/drafts", 5},
		{"GET", "/drafts/d1?format=raw", 60},
		{"POST", "/drafts", 10},
		{"PUT", "/drafts/d1", 15},
		{"DELETE", "/drafts/d1", 10},
		{"POST", "/drafts/send", 100},
		{"GET", "/somethingNew", 20}, // unknown: charged as a message fetch
	} {
		if got := cost(tc.method, tc.path); got != tc.want {
			t.Errorf("cost(%s %s) = %d, want %d", tc.method, tc.path, got, tc.want)
		}
	}
}

// fakeClock is a clock whose sleeps advance it.
type fakeClock struct {
	t     time.Time
	slept time.Duration
}

func (c *fakeClock) now() time.Time { return c.t }
func (c *fakeClock) sleep(_ context.Context, d time.Duration) error {
	c.t = c.t.Add(d)
	c.slept += d
	return nil
}

func newTestQuota(rate, burst, reserve float64) (*quota, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	q := newQuota(rate, burst, reserve)
	q.now, q.sleep, q.last = clk.now, clk.sleep, clk.t
	return q, clk
}

func TestQuota_burstThenRate(t *testing.T) {
	q, clk := newTestQuota(10, 100, 0)
	ctx := context.Background()
	for range 5 {
		if err := q.take(ctx, 20); err != nil {
			t.Fatal(err)
		}
	}
	if clk.slept != 0 {
		t.Fatalf("slept %v within the burst", clk.slept)
	}
	if err := q.take(ctx, 20); err != nil { // the bucket is empty: 20 units at 10 a second
		t.Fatal(err)
	}
	if clk.slept != 2*time.Second {
		t.Fatalf("slept %v, want 2s", clk.slept)
	}
}

// Background work (the sync) leaves a reserve, so what the user does —
// opening a message, sending — does not wait behind it.
func TestQuota_backgroundLeavesTheReserve(t *testing.T) {
	q, clk := newTestQuota(10, 100, 50)
	bg := Background(context.Background())
	for range 2 {
		if err := q.take(bg, 20); err != nil {
			t.Fatal(err)
		}
	}
	if clk.slept != 0 {
		t.Fatalf("slept %v", clk.slept)
	}
	// 60 left: a background fetch would go under the reserve, so it waits…
	if err := q.take(bg, 20); err != nil {
		t.Fatal(err)
	}
	if clk.slept != time.Second {
		t.Fatalf("background slept %v, want 1s (to 70 units)", clk.slept)
	}
	// …while the user's goes at once, into the reserve.
	clk.slept = 0
	if err := q.take(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	if clk.slept != 0 {
		t.Fatalf("foreground slept %v", clk.slept)
	}
}

// Over a minute, burst plus rate stays under Gmail's 6,000 units per user.
func TestQuota_defaultStaysUnderTheMinuteLimit(t *testing.T) {
	if got := quotaBurst + quotaRate*60; got > 6000 {
		t.Fatalf("a minute may use %v units", got)
	}
	if quotaReserve < 100 {
		t.Fatalf("reserve %v does not cover a send (100 units)", quotaReserve)
	}
}

// A rate-limit answer empties the bucket: every request waits, not just
// the one that was refused.
func TestQuota_rateLimitEmptiesTheBucket(t *testing.T) {
	q, clk := newTestQuota(10, 100, 0)
	q.drain()
	if err := q.take(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if clk.slept != time.Second {
		t.Fatalf("slept %v, want 1s", clk.slept)
	}
}

func TestQuota_cancelStopsTheWait(t *testing.T) {
	q := newQuota(1, 1, 0) // real clock
	q.drain()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.take(ctx, 100); err == nil {
		t.Fatal("take waited past a cancelled context")
	}
}

// The client charges each request its cost.
func TestClient_chargesQuota(t *testing.T) {
	c, _, _ := newTest(t)
	q, _ := newTestQuota(1, 1000, 0)
	c.quota = q
	if err := c.call(context.Background(), http.MethodGet, "/profile", nil, nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetRaw(context.Background(), "missing"); err == nil {
		t.Fatal("want not found")
	}
	if left := q.units; left != 1000-1-60 {
		t.Fatalf("units left = %v, want 939", left)
	}
}
