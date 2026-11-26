package cmd

import (
	"context"
	"errors"
	"github.com/zhecho/m9s/internal/ui"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/keepassxc"
)

type memSaved struct{ t time.Time }

func (m *memSaved) TokenSaved(string) time.Time { return m.t }
func (m *memSaved) SetTokenSaved(_ string, t time.Time) error {
	m.t = t
	return nil
}

type saverRig struct {
	s     *tokenSaver
	now   time.Time
	saved []string
	err   error
	warns []string
	store *memSaved
}

func newSaverRig() *saverRig {
	r := &saverRig{now: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC), store: &memSaved{}}
	r.s = &tokenSaver{account: "ms",
		save: func(_ context.Context, tok string) (string, error) {
			if r.err != nil {
				return "", r.err
			}
			r.saved = append(r.saved, tok)
			return "Root/Email/ms token", nil
		},
		store: r.store, now: func() time.Time { return r.now }, warn: func(s string) { r.warns = append(r.warns, s) },
	}
	return r
}

func (r *saverRig) at(d time.Duration) { r.now = r.now.Add(d) }

func TestTokenSaver_firstRotationSavesOnce(t *testing.T) {
	r := newSaverRig()
	r.s.Tick(context.Background())
	if len(r.saved) != 0 {
		t.Fatalf("saved before any rotation: %v", r.saved)
	}
	r.s.Rotated("a")
	r.s.Tick(context.Background())
	if len(r.saved) != 1 || r.saved[0] != "a" || !r.store.t.Equal(r.now) {
		t.Fatalf("first rotation: saved %v at %v", r.saved, r.store.t)
	}
	r.at(time.Hour)
	r.s.Rotated("b")
	r.s.Tick(context.Background())
	if len(r.saved) != 1 {
		t.Fatalf("saved again within a day: %v", r.saved)
	}
	r.at(24 * time.Hour)
	r.s.Tick(context.Background())
	if len(r.saved) != 2 || r.saved[1] != "b" {
		t.Fatalf("a day later: %v", r.saved)
	}
	r.at(25 * time.Hour)
	r.s.Tick(context.Background())
	if len(r.saved) != 2 {
		t.Fatalf("saved an unchanged token: %v", r.saved)
	}
}

func TestTokenSaver_lockedRetriesHourly(t *testing.T) {
	r := newSaverRig()
	r.err = keepassxc.ErrLocked
	r.s.Rotated("a")
	r.s.Tick(context.Background()) // fails
	r.err = nil
	r.at(30 * time.Minute)
	r.s.Tick(context.Background())
	if len(r.saved) != 0 {
		t.Fatalf("retried after 30 min: %v", r.saved)
	}
	r.at(31 * time.Minute)
	r.s.Tick(context.Background())
	if len(r.saved) != 1 || r.saved[0] != "a" {
		t.Fatalf("not retried after an hour: %v", r.saved)
	}
}

func TestTokenSaver_warnsAfterSevenDays(t *testing.T) {
	r := newSaverRig()
	r.store.t = r.now.Add(-8 * 24 * time.Hour)
	r.err = errors.New("KeePassXC is not running")
	r.s.Rotated("a")
	r.s.Tick(context.Background())
	want := "Microsoft sign-in not saved for 8 days — unlock KeePassXC"
	if len(r.warns) != 1 || r.warns[0] != want {
		t.Fatalf("warns = %q", r.warns)
	}
	r.at(2 * time.Hour)
	r.s.Tick(context.Background())
	if len(r.warns) != 1 {
		t.Fatalf("warned again after 2 h: %q", r.warns)
	}
	r.at(25 * time.Hour)
	r.s.Tick(context.Background())
	if len(r.warns) != 2 || r.warns[1] != "Microsoft sign-in not saved for 9 days — unlock KeePassXC" {
		t.Fatalf("warns = %q", r.warns)
	}
}

func TestTokenSaver_noWarningWhenSaved(t *testing.T) {
	r := newSaverRig()
	r.store.t = r.now.Add(-8 * 24 * time.Hour)
	r.s.Rotated("a")
	r.s.Tick(context.Background())
	if len(r.warns) != 0 {
		t.Fatalf("warned although the save worked: %q", r.warns)
	}
}

// The log names the entry the sign-in was saved to, by its path.
func TestTokenSaver_savedLineNamesTheEntry(t *testing.T) {
	r := newSaverRig()
	r.s.Rotated("a")
	r.s.Tick(context.Background())
	want := "[ms] Microsoft sign-in saved to KeePassXC: Root/Email/ms token"
	for _, e := range ui.AppLog.Entries() {
		if e.Msg == want {
			return
		}
	}
	t.Errorf("no log line %q", want)
}
