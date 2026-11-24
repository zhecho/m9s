package creds

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/config"
)

type kpCall struct{ host, user string }

type fakeKeePass struct {
	pw    map[string]string // host|user → password
	err   error             // returned for every call when set
	calls []kpCall
}

func (f *fakeKeePass) Password(_ context.Context, host, user string) (string, error) {
	f.calls = append(f.calls, kpCall{host, user})
	if f.err != nil {
		return "", f.err
	}
	if pw, ok := f.pw[host+"|"+user]; ok {
		return pw, nil
	}
	return "", &NoEntryError{Host: host, User: user}
}

// world is everything a lookup can consult, all fake.
type world struct {
	env      map[string]string
	cmds     map[string]string // command → output; missing → error
	keychain map[string]string // account/service
	kp       *fakeKeePass
	cmdRuns  []string
	updates  []Update
}

func (w *world) resolver() *Resolver {
	r := &Resolver{
		Getenv: func(k string) string { return w.env[k] },
		RunCmd: func(_ context.Context, c string) (string, error) {
			w.cmdRuns = append(w.cmdRuns, c)
			if out, ok := w.cmds[c]; ok {
				return out, nil
			}
			return "", errors.New("exit status 1")
		},
		Keychain: func(a, s string) (string, error) {
			if pw, ok := w.keychain[a+"/"+s]; ok {
				return pw, nil
			}
			return "", errors.New("secret not found in keyring")
		},
		OnUpdate: func(u Update) { w.updates = append(w.updates, u) },
	}
	if w.kp != nil {
		r.KeePass = w.kp
	}
	return r
}

func acct(name string, mod func(*config.Account)) config.Account {
	a := config.Account{Name: name}
	a.IMAP.Host, a.IMAP.User = "imap.x.io", name+"@x.io"
	a.SMTP.Host, a.SMTP.User = "smtp.x.io", name+"@x.io"
	if mod != nil {
		mod(&a)
	}
	return a
}

func one(t *testing.T, w *world, a config.Account) Result {
	t.Helper()
	res := w.resolver().Resolve(context.Background(), []config.Account{a})
	if len(res) != 1 || res[0].Account != a.Name {
		t.Fatalf("results = %+v", res)
	}
	return res[0]
}

func TestResolve_orderEnvFirst(t *testing.T) {
	w := &world{
		env:      map[string]string{"WORK_IMAP_PASSWORD": "e1", "WORK_SMTP_PASSWORD": "e2"},
		cmds:     map[string]string{"cmd": "c"},
		keychain: map[string]string{"work/imap": "k", "work/smtp": "k"},
		kp:       &fakeKeePass{pw: map[string]string{"imap.x.io|work@x.io": "kp", "smtp.x.io|work@x.io": "kp"}},
	}
	r := one(t, w, acct("work", func(a *config.Account) {
		a.KeePassXC = true
		a.IMAP.PasswordCmd, a.IMAP.Password = "cmd", "plain"
		a.SMTP.PasswordCmd, a.SMTP.Password = "cmd", "plain"
	}))
	if r.IMAP != (Secret{"e1", Env, true}) || r.SMTP != (Secret{"e2", Env, true}) || !r.Works() {
		t.Errorf("imap %+v smtp %+v, want both from env", r.IMAP, r.SMTP)
	}
	if len(w.kp.calls) != 0 || len(w.cmdRuns) != 0 {
		t.Errorf("lower sources were consulted: keepass %v cmds %v", w.kp.calls, w.cmdRuns)
	}
}

func TestResolve_envCommand(t *testing.T) {
	w := &world{env: map[string]string{"WORK_IMAP_PASSWORD_CMD": "pass show", "WORK_SMTP_PASSWORD_CMD": "pass show"},
		cmds: map[string]string{"pass show": "fromcmd"}}
	r := one(t, w, acct("work", nil))
	if r.IMAP != (Secret{"fromcmd", Env, true}) {
		t.Errorf("imap = %+v, want the env command's output under env", r.IMAP)
	}
}

func TestResolve_keepassxcBeforePasswordCmd(t *testing.T) {
	w := &world{cmds: map[string]string{"cmd": "c"},
		kp: &fakeKeePass{pw: map[string]string{"imap.x.io|work@x.io": "kp1", "smtp.x.io|work@x.io": "kp2"}}}
	r := one(t, w, acct("work", func(a *config.Account) {
		a.KeePassXC = true
		a.IMAP.PasswordCmd, a.IMAP.Password = "cmd", "plain"
		a.SMTP.PasswordCmd, a.SMTP.Password = "cmd", "plain"
	}))
	if r.IMAP != (Secret{"kp1", KeePass, true}) || r.SMTP != (Secret{"kp2", KeePass, true}) {
		t.Errorf("imap %+v smtp %+v, want both from keepassxc", r.IMAP, r.SMTP)
	}
	if len(w.cmdRuns) != 0 {
		t.Errorf("password_cmd ran although KeePassXC had the password: %v", w.cmdRuns)
	}
}

func TestResolve_fallsThroughAFailedSource(t *testing.T) {
	w := &world{kp: &fakeKeePass{err: errors.New("KeePassXC is not running")}}
	r := one(t, w, acct("work", func(a *config.Account) {
		a.KeePassXC = true
		a.IMAP.Password, a.SMTP.Password = "p1", "p2"
	}))
	if r.IMAP != (Secret{"p1", Plain, true}) || r.SMTP != (Secret{"p2", Plain, true}) {
		t.Errorf("imap %+v smtp %+v, want the plain passwords", r.IMAP, r.SMTP)
	}
	if len(r.Failures) != 2 {
		t.Fatalf("failures = %+v, want one per service", r.Failures)
	}
	for i, svc := range []string{"imap", "smtp"} {
		want := Update{Account: "work", Service: svc, Source: KeePass, State: Failed, Reason: "KeePassXC is not running"}
		if r.Failures[i] != want {
			t.Errorf("failure %d = %+v, want %+v", i, r.Failures[i], want)
		}
	}
}

func TestResolve_failedPasswordCmdNoLongerEndsTheSearch(t *testing.T) {
	w := &world{keychain: map[string]string{"work/imap": "k1", "work/smtp": "k2"}}
	r := one(t, w, acct("work", func(a *config.Account) { a.IMAP.PasswordCmd, a.SMTP.PasswordCmd = "boom", "boom" }))
	if r.IMAP != (Secret{"k1", Keychain, true}) || r.SMTP != (Secret{"k2", Keychain, true}) {
		t.Errorf("imap %+v smtp %+v, want the keychain's", r.IMAP, r.SMTP)
	}
	if len(r.Failures) != 2 || r.Failures[0].Reason != "password_cmd failed: exit status 1" {
		t.Errorf("failures = %+v", r.Failures)
	}
}

func TestResolve_nothingAnywhere(t *testing.T) {
	w := &world{}
	r := one(t, w, acct("work", nil))
	if r.Works() || r.IMAP.OK || r.SMTP.OK || r.IMAP.Password != "" {
		t.Errorf("result = %+v, want nothing found", r)
	}
	if len(r.Failures) != 0 {
		t.Errorf("an empty keychain is 'not set', not a failure: %+v", r.Failures)
	}
	last := w.updates[len(w.updates)-1]
	if last.Source != Keychain || last.State != NotSet {
		t.Errorf("last update = %+v, want keychain NotSet", last)
	}
}

func TestResolve_imapAndSmtpMayDiffer(t *testing.T) {
	w := &world{kp: &fakeKeePass{pw: map[string]string{"imap.x.io|a@x.io": "kp"}}}
	r := one(t, w, acct("work", func(a *config.Account) {
		a.KeePassXC = true
		a.IMAP.User, a.SMTP.User = "a@x.io", "b@x.io"
		a.SMTP.Password = "plain"
	}))
	if r.IMAP.Source != KeePass || r.SMTP != (Secret{"plain", Plain, true}) || !r.Works() {
		t.Errorf("imap %+v smtp %+v", r.IMAP, r.SMTP)
	}
	if len(r.Failures) != 1 || r.Failures[0].Service != "smtp" || r.Failures[0].Source != KeePass {
		t.Errorf("failures = %+v, want smtp's KeePassXC miss", r.Failures)
	}
}

func TestResolve_oneServiceMissingMeansNotWorking(t *testing.T) {
	w := &world{}
	r := one(t, w, acct("work", func(a *config.Account) { a.IMAP.Password = "p" }))
	if r.Works() || !r.IMAP.OK || r.SMTP.OK {
		t.Errorf("result = %+v; an account needs both passwords to work", r)
	}
}

func TestResolve_keepassxcUsesServiceHostAndUser(t *testing.T) {
	kp := &fakeKeePass{pw: map[string]string{"imap.x.io|in@x.io": "a", "smtp.x.io|out@x.io": "b"}}
	one(t, &world{kp: kp}, acct("work", func(a *config.Account) {
		a.KeePassXC = true
		a.IMAP.User, a.SMTP.User = "in@x.io", "out@x.io"
	}))
	want := []kpCall{{"imap.x.io", "in@x.io"}, {"smtp.x.io", "out@x.io"}}
	if !slices.Equal(kp.calls, want) {
		t.Errorf("KeePassXC asked for %v, want %v", kp.calls, want)
	}
}

func TestResolve_keepassxcOffIsNeverAsked(t *testing.T) {
	kp := &fakeKeePass{}
	one(t, &world{kp: kp}, acct("work", func(a *config.Account) { a.IMAP.Password, a.SMTP.Password = "p", "p" }))
	if len(kp.calls) != 0 {
		t.Errorf("KeePassXC asked for an account without keepassxc: true: %v", kp.calls)
	}
}

func TestResolve_keepassxcWithoutSupport(t *testing.T) {
	r := one(t, &world{}, acct("work", func(a *config.Account) { a.KeePassXC = true }))
	if len(r.Failures) != 2 || r.Failures[0].Reason != "KeePassXC support is not available" {
		t.Errorf("failures = %+v", r.Failures)
	}
}

func TestResolve_updatesTellTheGridEverything(t *testing.T) {
	w := &world{kp: &fakeKeePass{pw: map[string]string{"imap.x.io|work@x.io": "kp", "smtp.x.io|work@x.io": "kp"}}}
	one(t, w, acct("work", func(a *config.Account) {
		a.KeePassXC = true
		a.IMAP.Password, a.SMTP.Password = "plain", "plain"
	}))
	var imap []string
	for _, u := range w.updates {
		if u.Service == "imap" {
			imap = append(imap, fmt.Sprintf("%s:%d", u.Source, u.State))
		}
	}
	want := []string{
		fmt.Sprintf("env:%d", NotSet), fmt.Sprintf("keepassxc:%d", Pending), fmt.Sprintf("password_cmd:%d", NotSet),
		fmt.Sprintf("plain text:%d", Pending), fmt.Sprintf("keychain:%d", Pending),
		fmt.Sprintf("keepassxc:%d", Asking), fmt.Sprintf("keepassxc:%d", Used),
		fmt.Sprintf("plain text:%d", NotNeeded), fmt.Sprintf("keychain:%d", NotNeeded),
	}
	if !slices.Equal(imap, want) {
		t.Errorf("imap updates =\n %v\nwant\n %v", imap, want)
	}
}

func TestResolve_passwordsAreNotAltered(t *testing.T) {
	plain, kpw := " p\"ä ", " q\n "
	w := &world{kp: &fakeKeePass{pw: map[string]string{"imap.x.io|work@x.io": kpw}}}
	r := one(t, w, acct("work", func(a *config.Account) {
		a.KeePassXC = true
		a.SMTP.User = "other@x.io"
		a.SMTP.Password = plain
	}))
	if r.IMAP.Password != kpw || r.SMTP.Password != plain {
		t.Errorf("imap %q smtp %q, want %q and %q byte for byte", r.IMAP.Password, r.SMTP.Password, kpw, plain)
	}
}

func TestResolve_emptyResultIsAFailure(t *testing.T) {
	w := &world{env: map[string]string{"WORK_IMAP_PASSWORD_CMD": "empty", "WORK_SMTP_PASSWORD_CMD": "empty"},
		cmds: map[string]string{"empty": ""}}
	r := one(t, w, acct("work", func(a *config.Account) { a.IMAP.Password, a.SMTP.Password = "p", "p" }))
	if r.IMAP.Source != Plain || len(r.Failures) != 2 || r.Failures[0].Reason != "env command returned nothing" {
		t.Errorf("imap %+v failures %+v", r.IMAP, r.Failures)
	}
}

func TestResolve_noSecretInAnyReason(t *testing.T) {
	secrets := []string{"s3cretA", "s3cretB", "s3cretC"}
	w := &world{
		env:      map[string]string{"WORK_IMAP_PASSWORD_CMD": "show s3cretA"},
		cmds:     map[string]string{"cmdB": "s3cretB"},
		keychain: map[string]string{"work/smtp": "s3cretC"},
		kp:       &fakeKeePass{err: errors.New("KeePassXC is not running")},
	}
	r := one(t, w, acct("work", func(a *config.Account) {
		a.KeePassXC = true
		a.IMAP.PasswordCmd = "cmdB"
		a.SMTP.PasswordCmd = "show s3cretA"
	}))
	check := func(where, text string) {
		for _, s := range secrets {
			if strings.Contains(text, s) {
				t.Errorf("%s leaks %q: %q", where, s, text)
			}
		}
	}
	for _, u := range w.updates {
		check("update", u.Reason)
	}
	for _, f := range r.Failures {
		check("failure", f.Reason)
	}
}

func TestResolve_zeroAccounts(t *testing.T) {
	if res := (&world{}).resolver().Resolve(context.Background(), nil); len(res) != 0 {
		t.Errorf("results = %+v", res)
	}
}

func TestResolve_nilCallbacksAndFuncs(t *testing.T) {
	r := &Resolver{} // nothing wired: must not panic
	res := r.Resolve(context.Background(), []config.Account{acct("work", func(a *config.Account) { a.IMAP.Password, a.SMTP.Password = "p", "p" })})
	if !res[0].Works() {
		t.Errorf("plain passwords need no helpers: %+v", res[0])
	}
}

func TestResolve_cancelledContextStopsAsking(t *testing.T) {
	kp := &fakeKeePass{pw: map[string]string{"imap.x.io|work@x.io": "kp"}}
	w := &world{kp: kp}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := w.resolver().Resolve(ctx, []config.Account{acct("work", func(a *config.Account) {
		a.KeePassXC = true
		a.IMAP.Password, a.SMTP.Password = "p", "p"
	})})
	if len(kp.calls) != 0 {
		t.Errorf("KeePassXC asked after cancellation: %v", kp.calls)
	}
	if !res[0].Works() || res[0].IMAP.Source != Plain {
		t.Errorf("the remaining sources must still be tried: %+v", res[0])
	}
}

func TestResolve_accountsInConfigOrder(t *testing.T) {
	w := &world{}
	res := w.resolver().Resolve(context.Background(), []config.Account{acct("b", nil), acct("a", nil)})
	if len(res) != 2 || res[0].Account != "b" || res[1].Account != "a" {
		t.Errorf("order = %+v", res)
	}
}

func TestResolve_hangingPasswordCmdTimesOut(t *testing.T) {
	w := &world{keychain: map[string]string{"work/imap": "k", "work/smtp": "k"}}
	r := w.resolver()
	r.CmdTimeout = 50 * time.Millisecond
	r.RunCmd = func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done() // a command waiting for a prompt nobody answers
		return "", errors.New("timed out")
	}
	start := time.Now()
	res := r.Resolve(context.Background(), []config.Account{acct("work", func(a *config.Account) {
		a.IMAP.PasswordCmd, a.SMTP.PasswordCmd = "op read x", "op read x"
	})})
	if time.Since(start) > time.Second {
		t.Fatalf("the lookup waited %s on a hanging command", time.Since(start))
	}
	if res[0].IMAP.Source != Keychain || len(res[0].Failures) != 2 ||
		res[0].Failures[0].Reason != "password_cmd failed: timed out after 50ms" {
		t.Errorf("result = %+v", res[0])
	}
}

func TestResolve_defaultCommandTimeIsTwoMinutes(t *testing.T) {
	var deadline time.Time
	r := &Resolver{RunCmd: func(ctx context.Context, _ string) (string, error) {
		deadline, _ = ctx.Deadline()
		return "p", nil
	}}
	r.Resolve(context.Background(), []config.Account{acct("work", func(a *config.Account) { a.IMAP.PasswordCmd = "x" })})
	if left := time.Until(deadline); left < 119*time.Second || left > 121*time.Second {
		t.Errorf("command time limit = %s, want 2 min", left)
	}
}
