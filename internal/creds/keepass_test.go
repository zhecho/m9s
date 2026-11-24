package creds

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/keepassxc"
)

// fakeClient is a scripted KeePassXC connection.
type fakeClient struct {
	mu          sync.Mutex
	locked      bool
	unlockAfter time.Duration // after an unlock was triggered; 0: never
	unlocked    chan struct{}
	knownAssoc  keepassxc.Association
	assocBlocks bool            // Associate waits for its context
	slow        map[string]bool // methods that wait for their context, like KeePassXC behind a dialog
	logins      map[string][]keepassxc.Login
	loginsErr   map[string][]error // consumed in order before logins is used

	sets                            []setCall
	setErr                          error
	associates, loginsCalls, closes int
	urls                            []string
}

func newFakeClient() *fakeClient {
	return &fakeClient{unlocked: make(chan struct{}, 8), logins: map[string][]keepassxc.Login{},
		loginsErr: map[string][]error{}, slow: map[string]bool{}, knownAssoc: keepassxc.Association{ID: "m9s", Key: "K0"}}
}

func (f *fakeClient) scheduleUnlock() {
	if f.unlockAfter > 0 {
		time.AfterFunc(f.unlockAfter, func() {
			f.mu.Lock()
			f.locked = false
			f.mu.Unlock()
			f.unlocked <- struct{}{}
		})
	}
}

// stall waits for ctx when method is slow; it reports whether it did.
func (f *fakeClient) stall(ctx context.Context, method string) error {
	f.mu.Lock()
	slow := f.slow[method]
	f.mu.Unlock()
	if !slow {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeClient) IsOpen(ctx context.Context, trigger bool) error {
	if err := f.stall(ctx, "IsOpen"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.locked {
		return nil
	}
	if trigger {
		f.scheduleUnlock()
	}
	return keepassxc.ErrLocked
}

func (f *fakeClient) WaitUnlocked(ctx context.Context) error {
	select {
	case <-f.unlocked:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeClient) Associate(ctx context.Context) (keepassxc.Association, error) {
	f.mu.Lock()
	f.associates++
	blocks := f.assocBlocks
	f.mu.Unlock()
	if blocks {
		<-ctx.Done()
		return keepassxc.Association{}, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.knownAssoc = keepassxc.Association{ID: "m9s", Key: "K-new"}
	return f.knownAssoc, nil
}

func (f *fakeClient) TestAssociate(ctx context.Context, a keepassxc.Association) error {
	if err := f.stall(ctx, "TestAssociate"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if a != f.knownAssoc {
		return &keepassxc.ProtocolError{Code: "8", Message: "association failed"}
	}
	return nil
}

func (f *fakeClient) Logins(ctx context.Context, url string, a keepassxc.Association) ([]keepassxc.Login, error) {
	if err := f.stall(ctx, "Logins"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loginsCalls++
	f.urls = append(f.urls, url)
	if errs := f.loginsErr[url]; len(errs) > 0 {
		err := errs[0]
		f.loginsErr[url] = errs[1:]
		if errors.Is(err, keepassxc.ErrLocked) {
			f.locked = true
		}
		return nil, err
	}
	if len(f.logins[url]) == 0 {
		return nil, keepassxc.ErrNoLogins
	}
	return f.logins[url], nil
}

func (f *fakeClient) Close() error { f.closes++; return nil }

type setCall struct{ url, login, password, uuid string }

func (f *fakeClient) SetLogin(ctx context.Context, url, login, password, uuid string, a keepassxc.Association) error {
	if err := f.stall(ctx, "SetLogin"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	if a != f.knownAssoc {
		return &keepassxc.ProtocolError{Code: "8", Message: "association failed"}
	}
	f.sets = append(f.sets, setCall{url, login, password, uuid})
	if uuid == "" {
		f.logins[url] = append(f.logins[url], keepassxc.Login{User: login, Password: password, UUID: "new-uuid"})
		return nil
	}
	for i := range f.logins[url] {
		if f.logins[url][i].UUID == uuid {
			f.logins[url][i].Password = password
		}
	}
	return nil
}

// rig is a Session over a fakeClient with an in-memory association store.
type rig struct {
	c       *fakeClient
	s       *Session
	dials   int
	dialErr error
	stored  keepassxc.Association
	deletes int
	notices []string
	mu      sync.Mutex
}

func newRig() *rig {
	r := &rig{c: newFakeClient(), stored: keepassxc.Association{ID: "m9s", Key: "K0"}}
	r.s = &Session{
		Dial: func(context.Context) (kpClient, error) {
			r.dials++
			if r.dialErr != nil {
				return nil, r.dialErr
			}
			return r.c, nil
		},
		Assoc: AssocStore{
			Load: func() (string, string, error) {
				if r.stored.ID == "" {
					return "", "", errors.New("not found")
				}
				return r.stored.ID, r.stored.Key, nil
			},
			Save:   func(id, key string) error { r.stored = keepassxc.Association{ID: id, Key: key}; return nil },
			Delete: func() error { r.deletes++; r.stored = keepassxc.Association{}; return nil },
		},
		Timeout:     time.Second,
		NoticeDelay: 200 * time.Millisecond,
		OnNotice: func(text string) {
			r.mu.Lock()
			r.notices = append(r.notices, text)
			r.mu.Unlock()
		},
	}
	return r
}

func (r *rig) entry(host, user, pw string) {
	url := "https://" + host
	r.c.logins[url] = append(r.c.logins[url], keepassxc.Login{User: user, Password: pw})
}

func (r *rig) noticed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.notices...)
}

var bg = context.Background()

func TestSession_unlockedAndApproved_noNoticeNoWait(t *testing.T) {
	r := newRig()
	r.entry("mail.x.io", "a@x.io", "pw-a")
	got, err := r.s.Password(bg, "mail.x.io", "a@x.io")
	if err != nil || got != "pw-a" {
		t.Fatalf("Password = %q, %v", got, err)
	}
	if r.s.Waited() || len(r.noticed()) != 0 || r.c.associates != 0 {
		t.Errorf("waited %v notices %v associates %d; nothing needed asking", r.s.Waited(), r.noticed(), r.c.associates)
	}
	if len(r.c.urls) != 1 || r.c.urls[0] != "https://mail.x.io" {
		t.Errorf("asked KeePassXC for %v, want https://mail.x.io", r.c.urls)
	}
}

func TestSession_oneRequestPerHost(t *testing.T) {
	r := newRig()
	r.entry("mail.x.io", "a@x.io", "pa")
	r.entry("mail.x.io", "b@x.io", "pb")
	r.entry("mail.x.io", "c@x.io", "pc")
	r.entry("other.x.io", "a@x.io", "po")
	for _, tc := range []struct{ host, user, want string }{
		{"mail.x.io", "a@x.io", "pa"}, {"mail.x.io", "b@x.io", "pb"}, {"mail.x.io", "c@x.io", "pc"},
		{"other.x.io", "a@x.io", "po"}, {"mail.x.io", "a@x.io", "pa"},
	} {
		if got, err := r.s.Password(bg, tc.host, tc.user); err != nil || got != tc.want {
			t.Errorf("%s on %s = %q, %v; want %q", tc.user, tc.host, got, err, tc.want)
		}
	}
	if r.c.loginsCalls != 2 || r.dials != 1 {
		t.Errorf("logins requests %d dials %d, want 2 and 1", r.c.loginsCalls, r.dials)
	}
}

func TestSession_matchesUserIgnoringCaseAndSpaces(t *testing.T) {
	r := newRig()
	r.entry("mail.x.io", " Admin@X.io ", "pw")
	if got, err := r.s.Password(bg, "mail.x.io", "admin@x.io"); err != nil || got != "pw" {
		t.Errorf("Password = %q, %v", got, err)
	}
}

const noEntryText = "no entry for a@x.io on mail.x.io in KeePassXC — set the entry's URL field to https://mail.x.io and its user name to a@x.io"

func TestSession_noEntryForUser(t *testing.T) {
	r := newRig()
	r.entry("mail.x.io", "someone-else@x.io", "pw")
	_, err := r.s.Password(bg, "mail.x.io", "a@x.io")
	if err == nil || err.Error() != noEntryText {
		t.Errorf("err = %v\nwant  %s", err, noEntryText)
	}
	// One account's missing entry must not fail the others.
	if got, err := r.s.Password(bg, "mail.x.io", "someone-else@x.io"); err != nil || got != "pw" {
		t.Errorf("the other account: %q, %v", got, err)
	}
}

func TestSession_errNoLoginsReadsTheSame(t *testing.T) {
	r := newRig() // nothing on that host at all
	_, err := r.s.Password(bg, "mail.x.io", "a@x.io")
	if err == nil || err.Error() != noEntryText {
		t.Errorf("err = %v\nwant  %s", err, noEntryText)
	}
}

func TestSession_twoEntriesSameUser(t *testing.T) {
	r := newRig()
	r.entry("h", "a@x", "one")
	r.entry("h", "A@X", "two")
	_, err := r.s.Password(bg, "h", "a@x")
	if want := "2 entries for a@x on h in KeePassXC — remove or rename one"; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %s", err, want)
	}
}

func TestSession_lockedWaitsThenContinues(t *testing.T) {
	r := newRig()
	r.c.locked, r.c.unlockAfter = true, 30*time.Millisecond
	r.entry("mail.x.io", "a@x.io", "pw")
	got, err := r.s.Password(bg, "mail.x.io", "a@x.io")
	if err != nil || got != "pw" {
		t.Fatalf("Password = %q, %v", got, err)
	}
	n := r.noticed()
	if len(n) != 2 || n[0] != "KeePassXC is locked — unlock it in the KeePassXC window…" || n[1] != "" {
		t.Errorf("notices = %q, want the unlock text and then its removal", n)
	}
	if !r.s.Waited() {
		t.Error("Waited() must report the unlock wait")
	}
}

func TestSession_lockedTimesOut(t *testing.T) {
	r := newRig()
	r.c.locked = true
	r.s.Timeout = 50 * time.Millisecond
	_, err := r.s.Password(bg, "mail.x.io", "a@x.io")
	if want := "KeePassXC unlock was not completed (waited 50ms)"; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %s", err, want)
	}
	if n := r.noticed(); len(n) == 0 || n[len(n)-1] != "" {
		t.Errorf("the notice must be cleared when the wait ends: %q", n)
	}
}

func TestSession_timeoutTextInMinutes(t *testing.T) {
	if got := waitedText(2 * time.Minute); got != "2 min" {
		t.Errorf("waitedText = %q, want 2 min", got)
	}
}

func TestSession_skipEndsTheWait(t *testing.T) {
	r := newRig()
	r.c.locked = true
	r.s.Timeout = time.Minute
	go func() { time.Sleep(20 * time.Millisecond); r.s.Skip(); r.s.Skip() }()
	start := time.Now()
	_, err := r.s.Password(bg, "mail.x.io", "a@x.io")
	if want := "skipped — KeePassXC was not unlocked"; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %s", err, want)
	}
	if time.Since(start) > time.Second {
		t.Error("Skip did not end the wait")
	}
}

func TestSession_skipBeforeAnythingIsHarmless(t *testing.T) {
	r := newRig()
	r.entry("mail.x.io", "a@x.io", "pw")
	r.s.Skip() // Esc pressed while nothing was waiting
	if got, err := r.s.Password(bg, "mail.x.io", "a@x.io"); err != nil || got != "pw" {
		t.Errorf("Password = %q, %v; an early Esc must not disable KeePassXC", got, err)
	}
}

func TestSession_failureIsStickyAndImmediate(t *testing.T) {
	r := newRig()
	r.c.locked = true
	r.s.Timeout = 40 * time.Millisecond
	_, first := r.s.Password(bg, "mail.x.io", "a@x.io")
	start := time.Now()
	_, second := r.s.Password(bg, "mail.x.io", "b@x.io")
	if first == nil || second == nil || first.Error() != second.Error() {
		t.Errorf("first %v second %v, want the same reason", first, second)
	}
	if time.Since(start) > 20*time.Millisecond || r.dials != 1 {
		t.Errorf("the second account waited again (%.0fms, %d dials)", float64(time.Since(start).Milliseconds()), r.dials)
	}
}

func TestSession_firstRunAssociatesAndSaves(t *testing.T) {
	r := newRig()
	r.stored = keepassxc.Association{}
	r.entry("mail.x.io", "a@x.io", "pw")
	if got, err := r.s.Password(bg, "mail.x.io", "a@x.io"); err != nil || got != "pw" {
		t.Fatalf("Password = %q, %v", got, err)
	}
	if r.c.associates != 1 || r.stored != (keepassxc.Association{ID: "m9s", Key: "K-new"}) {
		t.Errorf("associates %d stored %+v", r.c.associates, r.stored)
	}
	if n := r.noticed(); len(n) != 2 || n[0] != "Approve the connection in KeePassXC and name it m9s…" || n[1] != "" {
		t.Errorf("notices = %q", n)
	}
	if !r.s.Waited() {
		t.Error("asking for approval is waiting on the user")
	}
}

func TestSession_refusedAssociationIsReplacedOnce(t *testing.T) {
	r := newRig()
	r.stored = keepassxc.Association{ID: "m9s", Key: "stale"}
	r.entry("mail.x.io", "a@x.io", "pw")
	if got, err := r.s.Password(bg, "mail.x.io", "a@x.io"); err != nil || got != "pw" {
		t.Fatalf("Password = %q, %v", got, err)
	}
	r.s.Password(bg, "mail.x.io", "a@x.io")
	if r.deletes != 1 || r.c.associates != 1 || r.stored.Key != "K-new" {
		t.Errorf("deletes %d associates %d stored %+v", r.deletes, r.c.associates, r.stored)
	}
}

func TestSession_approvalNotGiven(t *testing.T) {
	r := newRig()
	r.stored = keepassxc.Association{}
	r.c.assocBlocks = true
	r.s.Timeout = 40 * time.Millisecond
	_, err := r.s.Password(bg, "mail.x.io", "a@x.io")
	if want := "KeePassXC approval was not completed (waited 40ms)"; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %s", err, want)
	}
}

func TestSession_notRunning(t *testing.T) {
	for _, dialErr := range []error{keepassxc.ErrNotRunning, keepassxc.ErrUnavailable} {
		r := newRig()
		r.dialErr = dialErr
		_, err := r.s.Password(bg, "mail.x.io", "a@x.io")
		_, again := r.s.Password(bg, "mail.x.io", "b@x.io")
		if err == nil || err.Error() != dialErr.Error() || again == nil || again.Error() != dialErr.Error() {
			t.Errorf("err = %v / %v, want %v both times", err, again, dialErr)
		}
		if r.dials != 1 {
			t.Errorf("dialled %d times; a dead KeePassXC is asked once", r.dials)
		}
	}
}

func TestSession_connectionLostFailsEveryWaitingAccountOnce(t *testing.T) {
	r := newRig()
	r.c.loginsErr["https://mail.x.io"] = []error{keepassxc.ErrClosed}
	_, err := r.s.Password(bg, "mail.x.io", "a@x.io")
	_, again := r.s.Password(bg, "other.x.io", "b@x.io")
	want := "KeePassXC closed the connection"
	if err == nil || err.Error() != want || again == nil || again.Error() != want {
		t.Errorf("err = %v / %v, want %q both times", err, again, want)
	}
	if r.c.loginsCalls != 1 {
		t.Errorf("asked a dead connection %d times", r.c.loginsCalls)
	}
}

func TestSession_relockBetweenHostsWaitsAgain(t *testing.T) {
	r := newRig()
	r.entry("one.x.io", "a@x.io", "p1")
	r.entry("two.x.io", "a@x.io", "p2")
	r.c.loginsErr["https://two.x.io"] = []error{keepassxc.ErrLocked}
	r.c.unlockAfter = 20 * time.Millisecond
	if got, err := r.s.Password(bg, "one.x.io", "a@x.io"); err != nil || got != "p1" {
		t.Fatalf("first host: %q, %v", got, err)
	}
	if got, err := r.s.Password(bg, "two.x.io", "a@x.io"); err != nil || got != "p2" {
		t.Fatalf("second host after a re-lock: %q, %v", got, err)
	}
	if !r.s.Waited() {
		t.Error("the re-lock was waited for")
	}
}

func TestSession_callerCancels(t *testing.T) {
	r := newRig()
	r.c.locked = true
	ctx, cancel := context.WithCancel(bg)
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, err := r.s.Password(ctx, "mail.x.io", "a@x.io")
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("err = %v, want a cancelled reason", err)
	}
}

func TestSession_closeClosesTheConnection(t *testing.T) {
	r := newRig()
	r.entry("mail.x.io", "a@x.io", "pw")
	r.s.Close() // before any use: nothing to close
	r.s.Password(bg, "mail.x.io", "a@x.io")
	r.s.Close()
	r.s.Close()
	if r.c.closes != 1 {
		t.Errorf("closes = %d, want 1", r.c.closes)
	}
}

func TestSession_neverPutsAPasswordInAnError(t *testing.T) {
	r := newRig()
	r.entry("h", "a@x", "s3cret-one")
	r.entry("h", "a@x", "s3cret-two")
	_, err := r.s.Password(bg, "h", "a@x")
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("err = %v", err)
	}
}

func TestSession_slowAnswerHasTheBudget(t *testing.T) {
	for _, method := range []string{"IsOpen", "TestAssociate", "Logins"} {
		r := newRig()
		r.c.slow[method] = true
		r.s.Timeout = 60 * time.Millisecond
		start := time.Now()
		_, err := r.s.Password(bg, "mail.x.io", "a@x.io")
		if want := "KeePassXC did not answer (waited 60ms)"; err == nil || err.Error() != want {
			t.Errorf("%s slow: err = %v, want %s", method, err, want)
		}
		if time.Since(start) > time.Second {
			t.Errorf("%s slow: the budget did not end the wait", method)
		}
		// KeePassXC as a whole is given up on: no second wait.
		start = time.Now()
		r.s.Password(bg, "mail.x.io", "b@x.io")
		if time.Since(start) > 20*time.Millisecond {
			t.Errorf("%s slow: the next account waited again", method)
		}
	}
}

func TestSession_slowAnswerCanBeSkipped(t *testing.T) {
	r := newRig()
	r.c.slow["Logins"] = true
	r.s.Timeout = time.Minute
	go func() { time.Sleep(30 * time.Millisecond); r.s.Skip() }()
	start := time.Now()
	_, err := r.s.Password(bg, "mail.x.io", "a@x.io")
	if want := "skipped — KeePassXC did not answer"; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %s", err, want)
	}
	if time.Since(start) > time.Second {
		t.Error("Esc did not end the wait")
	}
}

func TestSession_slowAnswerShowsANoticeAfterAWhile(t *testing.T) {
	r := newRig()
	r.c.slow["Logins"] = true
	r.s.Timeout = 400 * time.Millisecond
	r.s.NoticeDelay = 50 * time.Millisecond
	r.s.Password(bg, "mail.x.io", "a@x.io")
	n := r.noticed()
	if len(n) != 2 || n[0] != "Waiting for KeePassXC — allow m9s access to the entry if it asks…" || n[1] != "" {
		t.Errorf("notices = %q, want the access hint and then its removal", n)
	}
	if !r.s.Waited() {
		t.Error("a wait the user was told about counts as waiting")
	}
}

func TestSession_quickAnswersShowNoNotice(t *testing.T) {
	r := newRig()
	r.entry("mail.x.io", "a@x.io", "pw")
	r.s.NoticeDelay = time.Second
	r.s.Password(bg, "mail.x.io", "a@x.io")
	if len(r.noticed()) != 0 || r.s.Waited() {
		t.Errorf("notices %q waited %v; a normal start must flash by", r.noticed(), r.s.Waited())
	}
}

func TestSaveLoginUpdatesExisting(t *testing.T) {
	r := newRig()
	url := "https://oauth2.googleapis.com"
	r.c.logins[url] = []keepassxc.Login{
		{User: "other@gmail.com", Password: "x", UUID: "u-other"},
		{User: "Me@Gmail.com", Password: "old", UUID: "u-me"},
	}
	if err := r.s.SaveLogin(bg, "oauth2.googleapis.com", "me@gmail.com", "new-token"); err != nil {
		t.Fatal(err)
	}
	if len(r.c.sets) != 1 || r.c.sets[0] != (setCall{url, "me@gmail.com", "new-token", "u-me"}) {
		t.Errorf("set-login calls = %+v, want one update of u-me", r.c.sets)
	}
	// The cache is not stale afterwards.
	if got, err := r.s.Password(bg, "oauth2.googleapis.com", "me@gmail.com"); err != nil || got != "new-token" {
		t.Errorf("Password after save = %q, %v", got, err)
	}
}

func TestSaveLoginCreatesWhenMissing(t *testing.T) {
	r := newRig()
	if err := r.s.SaveLogin(bg, "oauth2.googleapis.com", "me@gmail.com", "tok"); err != nil {
		t.Fatal(err)
	}
	if len(r.c.sets) != 1 || r.c.sets[0] != (setCall{"https://oauth2.googleapis.com", "me@gmail.com", "tok", ""}) {
		t.Errorf("set-login calls = %+v, want one create", r.c.sets)
	}
	if got, err := r.s.Password(bg, "oauth2.googleapis.com", "me@gmail.com"); err != nil || got != "tok" {
		t.Errorf("Password after save = %q, %v", got, err)
	}
}

func TestSaveLoginRefusesAmbiguous(t *testing.T) {
	r := newRig()
	r.entry("h", "a@x", "one")
	r.entry("h", "A@X", "two")
	if err := r.s.SaveLogin(bg, "h", "a@x", "p"); err == nil || len(r.c.sets) != 0 {
		t.Errorf("err = %v, sets = %v; two entries must not be guessed between", err, r.c.sets)
	}
}

func TestSaveLoginErrorHasNoSecret(t *testing.T) {
	r := newRig()
	r.c.setErr = &keepassxc.ProtocolError{Code: "3", Message: "nope"}
	err := r.s.SaveLogin(bg, "h", "a@x", "sekrit-token")
	if err == nil || strings.Contains(err.Error(), "sekrit-token") {
		t.Errorf("err = %v", err)
	}
}

func TestSession_noEntryIsNoEntryError(t *testing.T) {
	r := newRig()
	_, err := r.s.Password(bg, "mail.x.io", "a@x.io")
	var ne *NoEntryError
	if !errors.As(err, &ne) || ne.Host != "mail.x.io" || ne.User != "a@x.io" {
		t.Errorf("err = %#v, want *NoEntryError", err)
	}
}

// quietClient records what SaveLoginQuiet asks of KeePassXC.
type quietClient struct {
	*fakeClient
	triggers []bool
	waits    int
}

func (q *quietClient) IsOpen(ctx context.Context, trigger bool) error {
	q.triggers = append(q.triggers, trigger)
	return q.fakeClient.IsOpen(ctx, trigger)
}

func (q *quietClient) WaitUnlocked(ctx context.Context) error {
	q.waits++
	return q.fakeClient.WaitUnlocked(ctx)
}

func quietRig() (*rig, *quietClient) {
	r := newRig()
	q := &quietClient{fakeClient: r.c}
	r.s.Dial = func(context.Context) (kpClient, error) {
		r.dials++
		if r.dialErr != nil {
			return nil, r.dialErr
		}
		return q, nil
	}
	return r, q
}

func TestSaveLoginQuiet_updatesTheEntry(t *testing.T) {
	r, q := quietRig()
	url := "https://graph.microsoft.com"
	r.c.logins[url] = []keepassxc.Login{{User: "me@outlook.com", Password: "old", UUID: "u-me"}}
	if err := r.s.SaveLoginQuiet(bg, "graph.microsoft.com", "me@outlook.com", "new"); err != nil {
		t.Fatal(err)
	}
	if len(r.c.sets) != 1 || r.c.sets[0] != (setCall{url, "me@outlook.com", "new", "u-me"}) {
		t.Errorf("set-login calls = %+v", r.c.sets)
	}
	if len(q.triggers) != 1 || q.triggers[0] {
		t.Errorf("IsOpen triggers = %v, want one false", q.triggers)
	}
	if n := len(r.noticed()); n != 0 {
		t.Errorf("notices = %v", r.noticed())
	}
}

func TestSaveLoginQuiet_lockedNeverUnlocks(t *testing.T) {
	r, q := quietRig()
	r.c.locked = true
	r.c.unlockAfter = 10 * time.Millisecond // would unlock if asked to
	err := r.s.SaveLoginQuiet(bg, "graph.microsoft.com", "me@outlook.com", "tok")
	if !errors.Is(err, keepassxc.ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
	if len(q.triggers) != 1 || q.triggers[0] || q.waits != 0 {
		t.Errorf("IsOpen triggers = %v, waits = %d: an unlock was asked for", q.triggers, q.waits)
	}
	if len(r.c.sets) != 0 || strings.Contains(err.Error(), "tok") {
		t.Errorf("sets = %v, err = %v", r.c.sets, err)
	}
}

func TestSaveLoginQuiet_noAssociationNeverPrompts(t *testing.T) {
	r, _ := quietRig()
	r.stored = keepassxc.Association{}
	err := r.s.SaveLoginQuiet(bg, "graph.microsoft.com", "me@outlook.com", "tok")
	if !errors.Is(err, ErrNotApproved) {
		t.Fatalf("err = %v, want ErrNotApproved", err)
	}
	if r.c.associates != 0 || len(r.c.sets) != 0 {
		t.Errorf("associates = %d, sets = %v", r.c.associates, r.c.sets)
	}
}

func TestSaveLoginQuiet_refusedAssociationIsKept(t *testing.T) {
	r, _ := quietRig()
	r.c.knownAssoc = keepassxc.Association{ID: "m9s", Key: "other"}
	err := r.s.SaveLoginQuiet(bg, "graph.microsoft.com", "me@outlook.com", "tok")
	if !errors.Is(err, ErrNotApproved) {
		t.Fatalf("err = %v, want ErrNotApproved", err)
	}
	if r.c.associates != 0 || r.deletes != 0 {
		t.Errorf("associates = %d, deletes = %d: a quiet save changed the approval", r.c.associates, r.deletes)
	}
}

func TestSaveLoginQuiet_notRunning(t *testing.T) {
	r, _ := quietRig()
	r.dialErr = keepassxc.ErrNotRunning
	if err := r.s.SaveLoginQuiet(bg, "graph.microsoft.com", "me@outlook.com", "tok"); !errors.Is(err, keepassxc.ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
}
