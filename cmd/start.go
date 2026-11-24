package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/creds"
	"github.com/zhecho/m9s/internal/ui"
	"github.com/zhecho/m9s/internal/view"
)

// lookup is one run's password lookup against the real sources: the
// environment, the running KeePassXC, password commands, the config and the
// OS keychain.
type lookup struct {
	session *creds.Session
}

func newLookup() *lookup { return &lookup{session: creds.NewSession(nil)} }

// run looks up the passwords of accounts, reporting each step.
func (l *lookup) run(ctx context.Context, accounts []config.Account, onUpdate func(creds.Update), onNotice func(string)) ([]creds.Result, bool) {
	l.session.OnNotice = onNotice
	r := &creds.Resolver{
		Getenv:   os.Getenv,
		RunCmd:   config.RunPasswordCmd,
		Keychain: config.KeychainPassword,
		KeePass:  l.session,
		OnUpdate: onUpdate,
	}
	results := r.Resolve(ctx, accounts)
	return results, l.session.Waited()
}

func (l *lookup) skip()  { l.session.Skip() }
func (l *lookup) close() { l.session.Close() }

// startDeps is what resolveAccounts needs from the outside world.
type startDeps struct {
	run  func(ctx context.Context, accounts []config.Account, onUpdate func(creds.Update), onNotice func(string)) ([]creds.Result, bool)
	skip func()
	show func(view.CredWindowDeps) ([]creds.Result, view.CredOutcome, bool)
}

// resolveAccounts shows the startup window while every account's passwords
// are looked up, and returns the accounts m9s can start with: those that got
// both. results holds every account's outcome, the failed ones included.
//
// The error means m9s must not start: the user quit, or nothing works.
func resolveAccounts(ctx context.Context, cfg *config.Config, skin *ui.Skin, d startDeps) (working []config.Account, results map[string]creds.Result, err error) {
	if len(cfg.Accounts) == 0 && len(cfg.Disabled) > 0 {
		return nil, nil, fmt.Errorf("every account is disabled (enable: false): %s", strings.Join(cfg.Disabled, ", "))
	}
	if len(cfg.Accounts) == 0 {
		return nil, nil, errors.New("no accounts in config")
	}
	accountNames := make([]string, len(cfg.Accounts))
	services := map[string][]string{}
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		accountNames[i] = a.Name
		if a.IsGmail() || a.IsMicrosoft() {
			services[a.Name] = creds.ServicesOf(a)
		}
	}

	found, outcome, quit := d.show(view.CredWindowDeps{
		Skin:     skin,
		Accounts: accountNames,
		Services: services,
		Run: func(wctx context.Context, onUpdate func(creds.Update), onNotice func(string)) ([]creds.Result, bool) {
			// Either the caller or the window (q, Ctrl-C) can end the lookup.
			lctx, cancel := context.WithCancel(ctx)
			defer cancel()
			stop := context.AfterFunc(wctx, cancel)
			defer stop()
			return d.run(lctx, cfg.Accounts, onUpdate, onNotice)
		},
		Skip: d.skip,
	})
	if quit {
		return nil, nil, errors.New("quit before m9s started")
	}

	results = make(map[string]creds.Result, len(found))
	for _, r := range found {
		results[r.Account] = r
	}
	ok := make(map[string]bool, len(outcome.Working))
	for _, name := range outcome.Working {
		ok[name] = true
	}
	for _, a := range cfg.Accounts {
		if ok[a.Name] {
			working = append(working, a)
		}
	}
	if len(working) == 0 {
		// Printed after the window has closed: say why here too.
		var why strings.Builder
		why.WriteString("no account has a password")
		for _, a := range cfg.Accounts {
			r := results[a.Name]
			if len(r.Failures) == 0 {
				fmt.Fprintf(&why, "\n  %s: no password in any source (run: m9s login %s)", a.Name, a.Name)
			}
			for _, f := range r.Failures {
				fmt.Fprintf(&why, "\n  %s: %s %s: %s", a.Name, f.Service, f.Source, f.Reason)
			}
		}
		return nil, results, errors.New(why.String())
	}
	return working, results, nil
}

// passwordLogLines says, for the log, where each account's passwords came
// from and what failed on the way. Never a password.
func passwordLogLines(accounts []string, results map[string]creds.Result) []ui.Entry {
	var lines []ui.Entry
	for _, name := range accounts {
		r, ok := results[name]
		if !ok {
			continue
		}
		for _, f := range r.Failures {
			lines = append(lines, ui.Entry{Level: ui.LevelWarn, Msg: fmt.Sprintf("[%s] %s %s: %s", name, f.Service, f.Source, f.Reason)})
		}
		switch {
		case r.Works() && r.Provider == config.ProviderMicrosoft:
			lines = append(lines, ui.Entry{Level: ui.LevelInfo, Msg: fmt.Sprintf("[%s] secrets: token %s", name, r.Microsoft.RefreshToken.Source)})
		case r.Works() && r.Provider == config.ProviderGmail:
			lines = append(lines, ui.Entry{Level: ui.LevelInfo, Msg: fmt.Sprintf("[%s] secrets: client %s, token %s", name, r.Gmail.ClientSecret.Source, r.Gmail.RefreshToken.Source)})
		case r.Works():
			lines = append(lines, ui.Entry{Level: ui.LevelInfo, Msg: fmt.Sprintf("[%s] password: imap %s, smtp %s", name, r.IMAP.Source, r.SMTP.Source)})
		default:
			lines = append(lines, ui.Entry{Level: ui.LevelWarn, Msg: fmt.Sprintf("[%s] left out: no password", name)})
		}
	}
	return lines
}
