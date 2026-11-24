package view

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tcell "github.com/derailed/tcell/v2"
	"github.com/zhecho/m9s/internal/creds"
	"github.com/zhecho/m9s/internal/ui"
)

// snapScreen keeps a copy of what was last shown, so a test can read the
// screen while the window keeps drawing: the simulated screen itself is not
// safe to read from another goroutine.
type snapScreen struct {
	tcell.SimulationScreen
	mu   sync.Mutex
	snap string
}

func (s *snapScreen) Show() {
	s.SimulationScreen.Show()
	cells, w, h := s.SimulationScreen.GetContents()
	var b strings.Builder
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if rs := cells[y*w+x].Runes; len(rs) > 0 {
				b.WriteRune(rs[0])
			} else {
				b.WriteRune(' ')
			}
		}
		b.WriteRune('\n')
	}
	s.mu.Lock()
	s.snap = b.String()
	s.mu.Unlock()
}

// credRig runs ShowCredentials on a simulated terminal.
type credRig struct {
	t      *testing.T
	screen *snapScreen
	deps   CredWindowDeps
	skips  atomic.Int32
	afters atomic.Int32
	ticks  chan time.Time // what After hands out; nil: never fires

	done    chan struct{}
	results []creds.Result
	outcome CredOutcome
	quit    bool
}

func newCredRig(t *testing.T, accounts ...string) *credRig {
	t.Helper()
	screen := &snapScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	screen.SetSize(110, 30)
	t.Cleanup(screen.Fini)
	skin, _ := ui.LoadSkin("")
	r := &credRig{t: t, screen: screen, done: make(chan struct{})}
	r.deps = CredWindowDeps{
		Skin: skin, Accounts: accounts, Screen: screen,
		Skip: func() { r.skips.Add(1) },
		After: func(time.Duration) <-chan time.Time {
			r.afters.Add(1)
			return r.ticks
		},
	}
	return r
}

func (r *credRig) start() {
	go func() {
		r.results, r.outcome, r.quit = ShowCredentials(r.deps)
		close(r.done)
	}()
}

func (r *credRig) text() string {
	r.screen.mu.Lock()
	defer r.screen.mu.Unlock()
	return r.screen.snap
}

func (r *credRig) waitFor(sub string) {
	r.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(r.text(), sub) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatalf("screen never showed %q:\n%s", sub, r.text())
}

func (r *credRig) returned(within time.Duration) bool {
	select {
	case <-r.done:
		return true
	case <-time.After(within):
		return false
	}
}

func (r *credRig) key(k tcell.Key, ch rune) { r.screen.InjectKey(k, ch, tcell.ModNone) }

func TestCredWindow_allGoodReturnsWithoutPausing(t *testing.T) {
	r := newCredRig(t, "a")
	r.deps.Run = func(_ context.Context, onUpdate func(creds.Update), _ func(string)) ([]creds.Result, bool) {
		onUpdate(creds.Update{Account: "a", Service: "imap", Source: creds.Env, State: creds.Used})
		return []creds.Result{works("a", creds.Env)}, false
	}
	r.start()
	if !r.returned(time.Second) {
		t.Fatal("the window must flash by when nothing needs waiting")
	}
	if r.quit || r.outcome.Kind != CredAllGood || len(r.results) != 1 || r.afters.Load() != 0 {
		t.Errorf("quit %v outcome %+v results %d timers %d", r.quit, r.outcome, len(r.results), r.afters.Load())
	}
}

func TestCredWindow_staysWhileRunIsWaiting(t *testing.T) {
	r := newCredRig(t, "mitko")
	release := make(chan struct{})
	r.deps.Run = func(_ context.Context, onUpdate func(creds.Update), onNotice func(string)) ([]creds.Result, bool) {
		onUpdate(creds.Update{Account: "mitko", Service: "imap", Source: creds.KeePass, State: creds.Asking})
		onNotice("KeePassXC is locked — unlock it in the KeePassXC window…")
		<-release
		return []creds.Result{works("mitko", creds.KeePass)}, true
	}
	r.start()
	r.waitFor("… asking")
	r.waitFor("KeePassXC is locked")
	if text := r.text(); !strings.Contains(text, "getting passwords") || !strings.Contains(text, "2 keepassxc") || !strings.Contains(text, "mitko") {
		t.Errorf("window content:\n%s", text)
	}
	if r.returned(50 * time.Millisecond) {
		t.Fatal("the window closed while KeePassXC was being asked")
	}
	close(release)
	if !r.returned(time.Second) || r.outcome.Kind != CredAllGood {
		t.Errorf("after the answer the window must close by itself: outcome %+v", r.outcome)
	}
}

func TestCredWindow_escCallsSkip(t *testing.T) {
	r := newCredRig(t, "a")
	release := make(chan struct{})
	r.deps.Run = func(_ context.Context, _ func(creds.Update), onNotice func(string)) ([]creds.Result, bool) {
		onNotice("KeePassXC is locked")
		<-release
		return []creds.Result{works("a", creds.Plain)}, true
	}
	r.start()
	r.waitFor("KeePassXC is locked")
	r.key(tcell.KeyEscape, 0)
	deadline := time.Now().Add(time.Second)
	for r.skips.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if r.skips.Load() != 1 {
		t.Errorf("Skip called %d times, want 1", r.skips.Load())
	}
	r.key(tcell.KeyRune, 'x') // other keys do nothing while waiting
	if r.returned(50 * time.Millisecond) {
		t.Error("a key other than q closed the window during a wait")
	}
	close(release)
	r.returned(time.Second)
}

func someFailedRun(_ context.Context, _ func(creds.Update), _ func(string)) ([]creds.Result, bool) {
	return []creds.Result{works("a", creds.Env),
		{Account: "b", Failures: []creds.Update{failure("b", "imap", creds.Cmd, "password_cmd failed: exit status 1")}}}, false
}

func TestCredWindow_pauseEndsOnAnyKey(t *testing.T) {
	r := newCredRig(t, "a", "b")
	r.deps.Run = someFailedRun
	r.start()
	r.waitFor("No password for: b")
	r.waitFor("Continuing without them in 5 s")
	if r.returned(50 * time.Millisecond) {
		t.Fatal("the window closed before the pause was over")
	}
	r.key(tcell.KeyRune, 'x')
	if !r.returned(time.Second) {
		t.Fatal("any key must end the pause")
	}
	if r.quit || r.outcome.Kind != CredSomeFailed || strings.Join(r.outcome.Working, ",") != "a" {
		t.Errorf("quit %v outcome %+v", r.quit, r.outcome)
	}
}

func TestCredWindow_pauseEndsOnTimer(t *testing.T) {
	r := newCredRig(t, "a", "b")
	r.deps.Run = someFailedRun
	ticks := make(chan time.Time)
	r.ticks = ticks
	r.start()
	r.waitFor("Continuing without them in 5 s")
	ticks <- time.Now()
	r.waitFor("Continuing without them in 4 s")
	for i := 0; i < 4; i++ {
		ticks <- time.Now()
	}
	if !r.returned(time.Second) || r.outcome.Kind != CredSomeFailed || r.quit {
		t.Errorf("after 5 seconds the window must close: outcome %+v quit %v", r.outcome, r.quit)
	}
}

func TestCredWindow_noneWorkCountsDownFromEight(t *testing.T) {
	r := newCredRig(t, "a")
	r.deps.Run = func(context.Context, func(creds.Update), func(string)) ([]creds.Result, bool) {
		return []creds.Result{{Account: "a"}}, false
	}
	r.start()
	r.waitFor("m9s will close in 8 s")
	r.key(tcell.KeyEnter, 0)
	if !r.returned(time.Second) || r.outcome.Kind != CredNoneWork {
		t.Errorf("outcome %+v", r.outcome)
	}
}

func TestCredWindow_quitWhileWaiting(t *testing.T) {
	for name, press := range map[string]func(*credRig){
		"q":      func(r *credRig) { r.key(tcell.KeyRune, 'q') },
		"ctrl-c": func(r *credRig) { r.key(tcell.KeyCtrlC, 0) },
	} {
		t.Run(name, func(t *testing.T) {
			r := newCredRig(t, "a")
			cancelled := make(chan struct{})
			r.deps.Run = func(ctx context.Context, _ func(creds.Update), onNotice func(string)) ([]creds.Result, bool) {
				onNotice("KeePassXC is locked")
				<-ctx.Done()
				close(cancelled)
				return nil, true
			}
			r.start()
			r.waitFor("KeePassXC is locked")
			press(r)
			if !r.returned(time.Second) || !r.quit {
				t.Fatalf("quit = %v; q and Ctrl-C must leave at once", r.quit)
			}
			select {
			case <-cancelled:
			case <-time.After(time.Second):
				t.Error("the lookup was not cancelled")
			}
		})
	}
}

func TestCredWindow_quitDuringThePause(t *testing.T) {
	r := newCredRig(t, "a", "b")
	r.deps.Run = someFailedRun
	r.start()
	r.waitFor("Continuing without them")
	r.key(tcell.KeyRune, 'q')
	if !r.returned(time.Second) || !r.quit {
		t.Errorf("quit = %v; q during the pause means quit, not continue", r.quit)
	}
}

func TestCredWindow_tinyTerminalDoesNotPanic(t *testing.T) {
	r := newCredRig(t, "a", "b")
	r.screen.SetSize(12, 4)
	r.deps.Run = someFailedRun
	r.start()
	time.Sleep(30 * time.Millisecond)
	r.key(tcell.KeyRune, 'x')
	if !r.returned(time.Second) {
		t.Error("did not return on a tiny terminal")
	}
}

func TestCredWindow_shortTerminalShowsWhatItWaitsFor(t *testing.T) {
	r := newCredRig(t, "mitko-example", "m9s-example", "e2e-example", "survey", "admin-survey")
	r.screen.SetSize(60, 10)
	release := make(chan struct{})
	r.deps.Run = func(_ context.Context, _ func(creds.Update), onNotice func(string)) ([]creds.Result, bool) {
		onNotice("KeePassXC is locked — unlock it in the KeePassXC window…")
		<-release
		return []creds.Result{works("mitko-example", creds.KeePass)}, true
	}
	r.start()
	r.waitFor("KeePassXC is locked")
	r.waitFor("skip KeePassXC")
	close(release)
	r.returned(time.Second)
}
