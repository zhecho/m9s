package cmd

import (
	"context"
	"fmt"
	gosync "sync"
	"time"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/creds"
	"github.com/zhecho/m9s/internal/store"
	"github.com/zhecho/m9s/internal/ui"
)

// Microsoft replaces a refresh token on every use and lets an unused one
// expire after 90 days: the newest one has to reach KeePassXC.
const (
	tokenSaveEvery = 24 * time.Hour     // at most one save a day…
	tokenRetry     = time.Hour          // …and after a failed one, wait this long
	tokenWarnAfter = 7 * 24 * time.Hour // no save for this long: tell the owner
	tokenWarnEvery = 24 * time.Hour
	tokenTick      = time.Minute
)

// tokenSaver writes a Microsoft account's newest refresh token to
// KeePassXC: right after the first rotation of a run, then when it changed,
// at most once a day. A save never asks for an unlock or an approval; when
// it cannot be done it is tried again an hour later, and after a week
// without a save the owner is told once a day. m9s goes on with the token
// in memory whatever happens.
type tokenSaver struct {
	account string
	save    func(ctx context.Context, token string) error
	store   interface {
		TokenSaved(account string) time.Time
		SetTokenSaved(account string, t time.Time) error
	}
	now  func() time.Time
	warn func(string)

	mu         gosync.Mutex
	latest     string    // the newest token
	saved      string    // the token last saved this run
	savedRun   bool      // a save worked this run
	retryAt    time.Time // no save before this, after a failure
	warnedAt   time.Time
	firstCheck time.Time // when this run first looked: the start of the count when nothing was ever saved
}

// newTokenSaver saves acct's token through a quiet KeePassXC session of its
// own each time.
func newTokenSaver(acct *config.Account, st *store.Store) *tokenSaver {
	user := acct.Microsoft.User
	return &tokenSaver{
		account: acct.Name,
		save: func(ctx context.Context, token string) error {
			s := creds.NewSession(func(text string) {
				if text != "" {
					ui.AppLog.Info("[%s] %s", acct.Name, text)
				}
			})
			s.Timeout = time.Minute // KeePassXC may ask to update the entry
			defer s.Close()
			return s.SaveLoginQuiet(ctx, creds.MicrosoftTokenHost, user, token)
		},
		store: st,
		now:   time.Now,
		warn:  func(string) {},
	}
}

// Rotated remembers token as the newest refresh token.
func (t *tokenSaver) Rotated(token string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.latest = token
}

// Tick saves the newest token when it is due, and warns when the last save
// is too long ago.
func (t *tokenSaver) Tick(ctx context.Context) {
	now := t.now()
	last := t.store.TokenSaved(t.account)
	t.mu.Lock()
	if t.firstCheck.IsZero() {
		t.firstCheck = now
	}
	token := t.latest
	due := token != "" && token != t.saved && !now.Before(t.retryAt) &&
		(!t.savedRun || now.Sub(last) >= tokenSaveEvery)
	t.mu.Unlock()

	if due {
		err := t.save(ctx, token)
		t.mu.Lock()
		if err == nil {
			t.saved, t.savedRun = token, true
			last = now
			t.mu.Unlock()
			if serr := t.store.SetTokenSaved(t.account, now); serr != nil {
				ui.AppLog.Warn("[%s] token saved, but not when: %v", t.account, serr)
			}
			ui.AppLog.Info("[%s] Microsoft sign-in saved to KeePassXC", t.account)
		} else {
			t.retryAt = now.Add(tokenRetry)
			t.mu.Unlock()
			ui.AppLog.Warn("[%s] Microsoft sign-in not saved to KeePassXC (trying again in an hour): %v", t.account, err)
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	since := last
	if since.IsZero() {
		since = t.firstCheck
	}
	if now.Sub(since) >= tokenWarnAfter && now.Sub(t.warnedAt) >= tokenWarnEvery {
		t.warnedAt = now
		t.warn(fmt.Sprintf("Microsoft sign-in not saved for %d days — unlock KeePassXC", int(now.Sub(since)/(24*time.Hour))))
	}
}

// run ticks every minute until ctx ends.
func (t *tokenSaver) run(ctx context.Context) {
	ticker := time.NewTicker(tokenTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.Tick(ctx)
		}
	}
}
