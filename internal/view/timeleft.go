package view

import (
	"fmt"
	"time"

	msync "github.com/zhecho/m9s/internal/sync"
)

// etaAfter is how long a listing or copy runs before its time left is
// shown: before that the rate says little.
const etaAfter = 10 * time.Second

// progressStart is where a running listing or copy began: when, and how
// far it already was.
type progressStart struct {
	at   time.Time
	done int
}

func listingKey(p msync.ListingProgress) string {
	return "listing\x00" + p.AccountName + "\x00" + p.FolderName
}
func mirrorKey(account string) string { return "mirror\x00" + account }

// trackStart remembers where the run of key began, and forgets it when the
// run ends.
func (ra *RootApp) trackStart(key string, done int, finished bool) {
	if finished {
		delete(ra.started, key)
		return
	}
	if _, ok := ra.started[key]; ok {
		return
	}
	if ra.started == nil {
		ra.started = make(map[string]progressStart)
	}
	ra.started[key] = progressStart{at: ra.now(), done: done}
}

// timeLeft estimates the rest of the run of key from its rate so far, as
// " · ~12m left"; "" while it is too early to tell.
func (ra *RootApp) timeLeft(key string, done, total int) string {
	s, ok := ra.started[key]
	if !ok {
		return ""
	}
	elapsed := ra.now().Sub(s.at)
	if elapsed < etaAfter || done <= s.done || done >= total {
		return ""
	}
	left := time.Duration(float64(elapsed) * float64(total-done) / float64(done-s.done))
	return " · ~" + shortDuration(left) + " left"
}

// shortDuration writes d as "2h05m", "12m" or "<1m".
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
