package view

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/zhecho/m9s/internal/creds"
)

func gridText(g *CredGrid, width int) string { return strings.Join(g.Lines(width), "\n") }

// lineWith returns the first line containing sub.
func lineWith(t *testing.T, g *CredGrid, width int, sub string) string {
	t.Helper()
	for _, l := range g.Lines(width) {
		if strings.Contains(l, sub) {
			return l
		}
	}
	t.Fatalf("no line contains %q in:\n%s", sub, gridText(g, width))
	return ""
}

func both(g *CredGrid, account string, source creds.Source, state creds.State, reason string) {
	for _, svc := range []string{"imap", "smtp"} {
		g.Apply(creds.Update{Account: account, Service: svc, Source: source, State: state, Reason: reason})
	}
}

func works(name string, source creds.Source) creds.Result {
	s := creds.Secret{Password: "x", Source: source, OK: true}
	return creds.Result{Account: name, IMAP: s, SMTP: s}
}

func failure(account, service string, source creds.Source, reason string) creds.Update {
	return creds.Update{Account: account, Service: service, Source: source, State: creds.Failed, Reason: reason}
}

func TestCredGrid_showsOrderAndRows(t *testing.T) {
	g := NewCredGrid([]string{"mitko", "survey"})
	text := gridText(g, 100)
	if !strings.Contains(text, "Tried in this order, first one found wins:") {
		t.Errorf("intro missing:\n%s", text)
	}
	header := lineWith(t, g, 100, "1 env")
	pos := -1
	for _, col := range []string{"account", "1 env", "2 keepassxc", "3 password_cmd", "4 plain text", "5 keychain"} {
		i := strings.Index(header, col)
		if i <= pos {
			t.Fatalf("header %q: %q missing or out of order", header, col)
		}
		pos = i
	}
	if strings.Index(text, "mitko") > strings.Index(text, "survey") || !strings.Contains(text, "survey") {
		t.Errorf("rows missing or out of config order:\n%s", text)
	}
	if !strings.Contains(text, "– not set") || !strings.Contains(text, "✔ used") || !strings.Contains(text, "✗ failed") {
		t.Errorf("legend missing:\n%s", text)
	}
}

func TestCredGrid_cellsFollowUpdates(t *testing.T) {
	g := NewCredGrid([]string{"mitko"})
	both(g, "mitko", creds.KeePass, creds.Pending, "")
	both(g, "mitko", creds.Plain, creds.Pending, "")
	if row := lineWith(t, g, 100, "mitko"); strings.Count(row, "waiting") != 2 {
		t.Errorf("pending sources must read 'waiting': %q", row)
	}
	both(g, "mitko", creds.KeePass, creds.Asking, "")
	if row := lineWith(t, g, 100, "mitko"); !strings.Contains(row, "… asking") {
		t.Errorf("row = %q, want … asking", row)
	}
	both(g, "mitko", creds.KeePass, creds.Used, "")
	both(g, "mitko", creds.Plain, creds.NotNeeded, "")
	row := lineWith(t, g, 100, "mitko")
	if !strings.Contains(row, "✔ used") || strings.Contains(row, "asking") || strings.Count(row, "·") != 1 || strings.Count(row, "–") != 3 {
		t.Errorf("row = %q, want –, ✔ used, –, ·, –", row)
	}
}

func TestCredGrid_failedCellSaysSo(t *testing.T) {
	g := NewCredGrid([]string{"mitko"})
	both(g, "mitko", creds.Cmd, creds.Failed, "password_cmd failed: exit status 1")
	if row := lineWith(t, g, 100, "mitko"); !strings.Contains(row, "✗") {
		t.Errorf("row = %q, want a ✗", row)
	}
}

func TestCredGrid_servicesThatDifferShowBoth(t *testing.T) {
	g := NewCredGrid([]string{"mitko"})
	g.Apply(creds.Update{Account: "mitko", Service: "imap", Source: creds.KeePass, State: creds.Used})
	g.Apply(failure("mitko", "smtp", creds.KeePass, "no entry"))
	g.Apply(creds.Update{Account: "mitko", Service: "imap", Source: creds.Plain, State: creds.NotNeeded})
	g.Apply(creds.Update{Account: "mitko", Service: "smtp", Source: creds.Plain, State: creds.Used})
	row := lineWith(t, g, 120, "mitko")
	if !strings.Contains(row, "✔ imap ✗ smtp") || !strings.Contains(row, "✔ smtp") {
		t.Errorf("row = %q, want '✔ imap ✗ smtp' under keepassxc and '✔ smtp' under plain text", row)
	}
}

func TestCredGrid_unknownAccountIsIgnored(t *testing.T) {
	g := NewCredGrid([]string{"mitko"})
	g.Apply(creds.Update{Account: "ghost", Service: "imap", Source: creds.Env, State: creds.Used})
	if strings.Contains(gridText(g, 100), "ghost") {
		t.Error("an update for an account that is not in the grid must not add a row")
	}
}

func TestCredGrid_noticeAndSkipHint(t *testing.T) {
	g := NewCredGrid([]string{"mitko"})
	g.SetNotice("KeePassXC is locked — unlock it in the KeePassXC window…")
	text := gridText(g, 100)
	if !strings.Contains(text, "KeePassXC is locked") || !strings.Contains(text, "esc") || !strings.Contains(text, "skip KeePassXC") {
		t.Errorf("notice or hint missing:\n%s", text)
	}
	g.SetNotice("")
	if text := gridText(g, 100); strings.Contains(text, "locked") || strings.Contains(text, "skip KeePassXC") {
		t.Errorf("notice must go away:\n%s", text)
	}
}

func TestCredGrid_finishAllGood(t *testing.T) {
	g := NewCredGrid([]string{"a", "b"})
	out := g.Finish([]creds.Result{works("a", creds.KeePass), works("b", creds.Env)}, false)
	if out.Kind != CredAllGood || out.Pause != 0 || strings.Join(out.Working, ",") != "a,b" {
		t.Errorf("outcome = %+v", out)
	}
}

func TestCredGrid_finishFellBack(t *testing.T) {
	g := NewCredGrid([]string{"a", "b"})
	a := works("a", creds.Plain)
	a.Failures = []creds.Update{failure("a", "imap", creds.KeePass, "KeePassXC is not running"), failure("a", "smtp", creds.KeePass, "KeePassXC is not running")}
	out := g.Finish([]creds.Result{a, works("b", creds.Env)}, false)
	if out.Kind != CredFellBack || out.Pause != 5*time.Second || len(out.Working) != 2 {
		t.Errorf("outcome = %+v", out)
	}
	text := gridText(g, 100)
	if !strings.Contains(text, "Some sources failed:") || strings.Count(text, "KeePassXC is not running") != 1 || !strings.Contains(text, "Continuing in 5 s") {
		t.Errorf("fallback must be explained once per account and reason:\n%s", text)
	}
}

func TestCredGrid_finishSomeFailed(t *testing.T) {
	g := NewCredGrid([]string{"mitko", "survey"})
	survey := creds.Result{Account: "survey", IMAP: creds.Secret{Password: "x", Source: creds.Plain, OK: true},
		Failures: []creds.Update{failure("survey", "smtp", creds.Cmd, "password_cmd failed: exit status 1")}}
	out := g.Finish([]creds.Result{works("mitko", creds.KeePass), survey}, true)
	if out.Kind != CredSomeFailed || out.Pause != 5*time.Second || strings.Join(out.Working, ",") != "mitko" {
		t.Errorf("outcome = %+v; an account with only its IMAP password does not work", out)
	}
	text := gridText(g, 100)
	for _, want := range []string{"No password for: survey", "password_cmd failed: exit status 1", "Continuing without them in 5 s (press any key)"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestCredGrid_failedAccountWithoutAReasonStillExplained(t *testing.T) {
	g := NewCredGrid([]string{"a", "b"})
	g.Finish([]creds.Result{works("a", creds.Env), {Account: "b"}}, false)
	if text := gridText(g, 100); !strings.Contains(text, "no password in any source") {
		t.Errorf("an account with nothing configured needs a reason too:\n%s", text)
	}
}

func TestCredGrid_finishNoneWork(t *testing.T) {
	g := NewCredGrid([]string{"a"})
	out := g.Finish([]creds.Result{{Account: "a", Failures: []creds.Update{failure("a", "imap", creds.KeePass, "KeePassXC is not running")}}}, false)
	if out.Kind != CredNoneWork || out.Pause != 8*time.Second || len(out.Working) != 0 {
		t.Errorf("outcome = %+v", out)
	}
	text := gridText(g, 100)
	if !strings.Contains(text, "No account has a password.") || !strings.Contains(text, "m9s will close in 8 s (press any key to close now)") {
		t.Errorf("text:\n%s", text)
	}
	g.Tick(3)
	if text := gridText(g, 100); !strings.Contains(text, "m9s will close in 3 s") {
		t.Errorf("countdown not shown:\n%s", text)
	}
}

func TestCredGrid_rendersInNarrowTerminal(t *testing.T) {
	names := []string{"mitko-example", "m9s-example", "e2e-example", "survey", "an-account-with-a-very-long-name-indeed-yes"}
	g := NewCredGrid(names)
	for _, n := range names {
		both(g, n, creds.KeePass, creds.Failed, "KeePassXC is not running or its browser integration is off (Settings → Browser Integration)")
	}
	g.SetNotice("KeePassXC is locked — unlock it in the KeePassXC window…")
	var res []creds.Result
	for _, n := range names {
		res = append(res, creds.Result{Account: n, Failures: []creds.Update{failure(n, "imap", creds.KeePass, "KeePassXC is not running or its browser integration is off (Settings → Browser Integration)")}})
	}
	g.Finish(res, false)
	for _, width := range []int{60, 40, 20, 5, 0} {
		lines := g.Lines(width)
		for _, l := range lines {
			if utf8.RuneCountInString(l) > max(width, 0) {
				t.Errorf("width %d: line is %d wide: %q", width, utf8.RuneCountInString(l), l)
			}
		}
	}
	text := gridText(g, 60)
	if !strings.Contains(text, "No account has a password.") || !strings.Contains(text, "m9s will close in 8 s") {
		t.Errorf("outcome lines must survive a narrow terminal:\n%s", text)
	}
	if !strings.Contains(text, "1 env") || !strings.Contains(text, "5 keych") {
		t.Errorf("all five sources must stay visible at 60 columns:\n%s", text)
	}
}

func TestCredGrid_zeroAccounts(t *testing.T) {
	g := NewCredGrid(nil)
	if out := g.Finish(nil, false); out.Kind != CredNoneWork {
		t.Errorf("outcome = %+v", out)
	}
	g.Lines(80)
}

func fiveAccountsLocked() (*CredGrid, []string) {
	names := []string{"mitko-example", "m9s-example", "e2e-example", "survey", "an-account-with-a-very-long-name"}
	g := NewCredGrid(names)
	for _, n := range names {
		both(g, n, creds.KeePass, creds.Asking, "")
	}
	g.SetNotice("KeePassXC is locked — unlock it in the KeePassXC window…")
	return g, names
}

func TestCredGrid_shortTerminalKeepsTheNotice(t *testing.T) {
	g, _ := fiveAccountsLocked()
	lines := g.LinesIn(60, 6) // header, 5 accounts and the notice need 8
	text := strings.Join(lines, "\n")
	if len(lines) > 6 {
		t.Errorf("%d lines for 6 rows:\n%s", len(lines), text)
	}
	if !strings.Contains(text, "KeePassXC is locked") || !strings.Contains(text, "skip KeePassXC") {
		t.Errorf("the notice and the Esc hint must survive a short terminal:\n%s", text)
	}
	if !strings.Contains(text, "1 env") || !strings.Contains(text, "more") {
		t.Errorf("the header stays and cut accounts are counted:\n%s", text)
	}
}

func TestCredGrid_shortTerminalKeepsTheOutcome(t *testing.T) {
	g, names := fiveAccountsLocked()
	var res []creds.Result
	for _, n := range names {
		res = append(res, creds.Result{Account: n, Failures: []creds.Update{failure(n, "imap", creds.KeePass, "KeePassXC is not running")}})
	}
	g.Finish(res, false)
	lines := g.LinesIn(60, 10)
	text := strings.Join(lines, "\n")
	if len(lines) > 10 {
		t.Errorf("%d lines for 10 rows:\n%s", len(lines), text)
	}
	for _, want := range []string{"No account has a password.", "m9s will close in 8 s"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in a 10-row terminal:\n%s", want, text)
		}
	}
}

func TestCredGrid_tallEnoughShowsEverything(t *testing.T) {
	g, _ := fiveAccountsLocked()
	if all, fit := g.Lines(100), g.LinesIn(100, 40); strings.Join(all, "\n") != strings.Join(fit, "\n") {
		t.Errorf("with room to spare nothing may be dropped:\n%s\n---\n%s", strings.Join(all, "\n"), strings.Join(fit, "\n"))
	}
}

func TestCredGrid_longReasonsWrap(t *testing.T) {
	reason := "no entry for mitko.bombata@example.com on mail.example.com in KeePassXC — set the entry's URL field to https://mail.example.com and its user name to mitko.bombata@example.com"
	g := NewCredGrid([]string{"mitko", "ok"})
	g.Finish([]creds.Result{{Account: "mitko", Failures: []creds.Update{failure("mitko", "imap", creds.KeePass, reason)}}, works("ok", creds.Env)}, false)
	lines := g.Lines(80)
	for _, l := range lines {
		if utf8.RuneCountInString(l) > 80 {
			t.Errorf("line wider than 80: %q", l)
		}
		if strings.HasSuffix(l, "…") {
			t.Errorf("a reason was cut off instead of wrapped: %q", l)
		}
	}
	joined := strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
	if !strings.Contains(joined, "and its user name to mitko.bombata@example.com") {
		t.Errorf("the end of the reason, which says what to do, is lost:\n%s", strings.Join(lines, "\n"))
	}
}

func TestCredGridGmailColumns(t *testing.T) {
	g := NewCredGrid([]string{"gm", "work"})
	g.SetServices("gm", []string{creds.ServiceClient, creds.ServiceToken})
	g.Apply(creds.Update{Account: "gm", Service: creds.ServiceClient, Source: creds.KeePass, State: creds.Used})
	g.Apply(failure("gm", creds.ServiceToken, creds.KeePass, "not signed in: run m9s login gm"))
	g.Apply(creds.Update{Account: "work", Service: "imap", Source: creds.KeePass, State: creds.Used})
	g.Apply(failure("work", "smtp", creds.KeePass, "no entry"))

	if row := lineWith(t, g, 140, "gm "); !strings.Contains(row, "✔ client ✗ token") {
		t.Errorf("wide gmail row = %q, want '✔ client ✗ token'", row)
	}
	if row := lineWith(t, g, 140, "work"); !strings.Contains(row, "✔ imap ✗ smtp") {
		t.Errorf("wide imap row = %q, want '✔ imap ✗ smtp'", row)
	}
	if row := lineWith(t, g, 60, "gm "); !strings.Contains(row, "✔c ✗t") {
		t.Errorf("narrow gmail row = %q, want '✔c ✗t'", row)
	}
	if row := lineWith(t, g, 60, "work"); !strings.Contains(row, "✔i ✗s") {
		t.Errorf("narrow imap row = %q, want '✔i ✗s'", row)
	}
	for _, l := range g.Lines(140) {
		if utf8.RuneCountInString(l) > 140 {
			t.Errorf("line wider than 140: %q", l)
		}
	}
}

func TestCredGridGmailBothUsed(t *testing.T) {
	g := NewCredGrid([]string{"gm"})
	g.SetServices("gm", []string{creds.ServiceClient, creds.ServiceToken})
	for _, svc := range []string{creds.ServiceClient, creds.ServiceToken} {
		g.Apply(creds.Update{Account: "gm", Service: svc, Source: creds.KeePass, State: creds.Used})
	}
	if row := lineWith(t, g, 140, "gm"); !strings.Contains(row, "✔ used") {
		t.Errorf("row = %q, want ✔ used", row)
	}
}

// A Microsoft account has one secret, its token: each cell answers for it
// alone, never "✔ token ✗ token".
func TestCredGrid_oneService(t *testing.T) {
	g := NewCredGrid([]string{"ms", "work"})
	g.SetServices("ms", []string{creds.ServiceToken})
	g.Apply(failure("ms", creds.ServiceToken, creds.KeePass, "not signed in: run m9s login ms"))
	g.Apply(creds.Update{Account: "work", Service: "imap", Source: creds.KeePass, State: creds.Used})
	g.Apply(failure("work", "smtp", creds.KeePass, "no entry"))
	row := lineWith(t, g, 140, "ms ")
	if !strings.Contains(row, "✗ failed") || strings.Contains(row, "imap") || strings.Contains(row, "smtp") {
		t.Errorf("row = %q, want one '✗ failed' cell", row)
	}
	g.Apply(creds.Update{Account: "ms", Service: creds.ServiceToken, Source: creds.KeePass, State: creds.Used})
	if row := lineWith(t, g, 60, "ms "); !strings.Contains(row, "✔") || strings.Contains(row, "✔t") {
		t.Errorf("narrow row = %q, want a plain ✔", row)
	}
}
