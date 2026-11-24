// Package creds finds every account's passwords once, at startup, trying
// the sources in a fixed order and reporting each step, so the screen can
// show where a password came from and why one could not be found.
//
// Nothing here ever puts a password, a command's text or a command's output
// into an Update: reasons are safe to show and to log.
package creds

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zhecho/m9s/internal/config"
)

// Source is a place a password can come from, named as the screen shows it.
type Source string

const (
	Env      Source = "env"
	KeePass  Source = "keepassxc"
	Cmd      Source = "password_cmd"
	Plain    Source = "plain text"
	Keychain Source = "keychain"
)

// Order is the order the sources are tried in; the first to supply a
// password wins.
var Order = []Source{Env, KeePass, Cmd, Plain, Keychain}

// State is where one source stands for one account's service.
type State int

const (
	NotSet    State = iota // the source is not configured
	Pending                // configured, not reached yet
	Asking                 // being tried now; may wait on the user
	Used                   // supplied the password
	Failed                 // configured but failed; Reason says why
	NotNeeded              // a source tried earlier already supplied it
)

// Update is one step of the lookup.
type Update struct {
	Account string
	Service string // "imap" or "smtp"; for gmail "client" or "token"; for microsoft "token"
	Source  Source
	State   State
	Reason  string // only for Failed
}

// Secret is the outcome for one service.
type Secret struct {
	Password string
	Source   Source
	OK       bool
}

// Services of a gmail account: the OAuth client secret and the refresh
// token, both kept in KeePassXC.
const (
	ServiceClient = "client"
	ServiceToken  = "token"
)

// Where the gmail secrets are kept in KeePassXC: the entry's URL host.
// The user name is gmail.client_id for the client, gmail.user for the token.
const (
	GmailClientHost = "accounts.google.com"
	GmailTokenHost  = "oauth2.googleapis.com"
)

// MicrosoftTokenHost is where a microsoft account's refresh token is kept
// in KeePassXC (user name: microsoft.user). No one signs in at this host
// in a browser, so the entry never collides with a saved web password.
const MicrosoftTokenHost = "graph.microsoft.com"

// ServicesOf names the secrets an account needs, in lookup order: two,
// or for microsoft only the token.
func ServicesOf(a *config.Account) []string {
	if a.IsGmail() {
		return []string{ServiceClient, ServiceToken}
	}
	if a.IsMicrosoft() {
		return []string{ServiceToken}
	}
	return []string{"imap", "smtp"}
}

// GmailSecrets are a gmail account's secrets.
type GmailSecrets struct {
	ClientSecret, RefreshToken Secret
}

// MicrosoftSecrets are a microsoft account's secrets: only the refresh
// token (a public client has no client secret).
type MicrosoftSecrets struct {
	RefreshToken Secret
}

// Result is the outcome for one account.
type Result struct {
	Account    string
	Provider   string // as config.Account.ProviderName; "" reads as imap
	IMAP, SMTP Secret
	Gmail      GmailSecrets
	Microsoft  MicrosoftSecrets
	Failures   []Update // every Failed step, in the order it happened
}

// Works reports whether the account can be used: it needs all of its
// secrets (the two passwords, for gmail the client secret and token, for
// microsoft the token).
func (r Result) Works() bool {
	switch r.Provider {
	case config.ProviderGmail:
		return r.Gmail.ClientSecret.OK && r.Gmail.RefreshToken.OK
	case config.ProviderMicrosoft:
		return r.Microsoft.RefreshToken.OK
	}
	return r.IMAP.OK && r.SMTP.OK
}

// KeePassLookup returns the password of the KeePassXC entry for user on host.
type KeePassLookup interface {
	Password(ctx context.Context, host, user string) (string, error)
}

// Resolver looks passwords up. Fields left nil act as "has nothing".
type Resolver struct {
	Getenv func(string) string
	RunCmd func(ctx context.Context, cmd string) (string, error)
	// CmdTimeout is how long one password command may run; 0 means two
	// minutes. A command that waits for a prompt nobody answers must not
	// hold up every other account.
	CmdTimeout time.Duration
	Keychain   func(account, service string) (string, error)
	KeePass    KeePassLookup
	OnUpdate   func(Update)
}

// Resolve tries the sources in Order for every account, in the order given,
// IMAP before SMTP. Failures are part of the results; it never gives up on
// an account because a source failed.
//
// A gmail account's client secret and refresh token, and a microsoft
// account's refresh token, come from KeePassXC only: every other source is
// reported as not set.
func (r *Resolver) Resolve(ctx context.Context, accounts []config.Account) []Result {
	results := make([]Result, 0, len(accounts))
	for i := range accounts {
		a := &accounts[i]
		res := Result{Account: a.Name, Provider: a.ProviderName()}
		switch {
		case a.IsMicrosoft():
			res.Microsoft.RefreshToken = r.service(ctx, a, lookupSpec{service: ServiceToken,
				host: MicrosoftTokenHost, user: a.Microsoft.User, keepassOnly: true,
				missing: "not signed in: run m9s login " + a.Name}, &res)
		case a.IsGmail():
			res.Gmail.ClientSecret = r.service(ctx, a, lookupSpec{service: ServiceClient,
				host: GmailClientHost, user: a.Gmail.ClientID, keepassOnly: true}, &res)
			res.Gmail.RefreshToken = r.service(ctx, a, lookupSpec{service: ServiceToken,
				host: GmailTokenHost, user: a.Gmail.User, keepassOnly: true,
				missing: "not signed in: run m9s login " + a.Name}, &res)
		default:
			res.IMAP = r.service(ctx, a, lookupSpec{service: "imap", host: a.IMAP.Host, user: a.IMAP.User,
				cmd: a.IMAP.PasswordCmd, plain: a.IMAP.Password}, &res)
			res.SMTP = r.service(ctx, a, lookupSpec{service: "smtp", host: a.SMTP.Host, user: a.SMTP.User,
				cmd: a.SMTP.PasswordCmd, plain: a.SMTP.Password}, &res)
		}
		results = append(results, res)
	}
	return results
}

// lookupSpec is what to look up for one service of an account.
type lookupSpec struct {
	service, host, user, cmd, plain string
	keepassOnly                     bool   // only KeePassXC is tried
	missing                         string // reason when KeePassXC has no entry; "" keeps KeePassXC's own
}

// attempt is one source for one service: whether it is configured, and how
// to try it. try returns the password, or a reason it failed; a reason of
// "" with an empty password means the source simply has nothing.
type attempt struct {
	source     Source
	configured bool
	try        func() (password, reason string)
}

func (r *Resolver) service(ctx context.Context, a *config.Account, spec lookupSpec, res *Result) Secret {
	service, host, user, cmd, plain := spec.service, spec.host, spec.user, spec.cmd, spec.plain
	var envValue, envCmd string
	if !spec.keepassOnly {
		envName := config.EnvKey(a.Name) + "_" + strings.ToUpper(service) + "_PASSWORD"
		envValue, envCmd = r.getenv(envName), r.getenv(envName+"_CMD")
	}

	attempts := []attempt{
		{Env, envValue != "" || envCmd != "", func() (string, string) {
			if envValue != "" {
				return envValue, ""
			}
			return r.command(ctx, envCmd, "env command")
		}},
		{KeePass, a.KeePassXC, func() (string, string) {
			switch {
			case r.KeePass == nil:
				return "", "KeePassXC support is not available"
			case ctx.Err() != nil:
				return "", "cancelled"
			}
			pw, err := r.KeePass.Password(ctx, host, user)
			var none *NoEntryError
			if spec.missing != "" && errors.As(err, &none) {
				return "", spec.missing
			}
			if err != nil {
				return "", err.Error()
			}
			if pw == "" {
				return "", "the KeePassXC entry has no password"
			}
			return pw, ""
		}},
		{Cmd, cmd != "", func() (string, string) { return r.command(ctx, cmd, "password_cmd") }},
		{Plain, plain != "", func() (string, string) { return plain, "" }},
		// The keychain is always worth a look; having nothing there is not a failure.
		{Keychain, !spec.keepassOnly, func() (string, string) {
			if r.Keychain == nil {
				return "", ""
			}
			pw, err := r.Keychain(a.Name, service)
			if err != nil {
				return "", ""
			}
			return pw, ""
		}},
	}

	if spec.keepassOnly {
		for i := range attempts {
			attempts[i].configured = attempts[i].configured && attempts[i].source == KeePass
		}
	}

	emit := func(source Source, state State, reason string) {
		u := Update{Account: a.Name, Service: service, Source: source, State: state, Reason: reason}
		if state == Failed {
			res.Failures = append(res.Failures, u)
		}
		if r.OnUpdate != nil {
			r.OnUpdate(u)
		}
	}

	for _, at := range attempts {
		if at.configured {
			emit(at.source, Pending, "")
		} else {
			emit(at.source, NotSet, "")
		}
	}
	for i, at := range attempts {
		if !at.configured {
			continue
		}
		emit(at.source, Asking, "")
		password, reason := at.try()
		switch {
		case reason != "":
			emit(at.source, Failed, reason)
		case password == "":
			emit(at.source, NotSet, "")
		default:
			emit(at.source, Used, "")
			for _, rest := range attempts[i+1:] {
				if rest.configured {
					emit(rest.source, NotNeeded, "")
				}
			}
			return Secret{Password: password, Source: at.source, OK: true}
		}
	}
	return Secret{}
}

// command runs a password command under the time limit; what names it in
// the reason.
func (r *Resolver) command(ctx context.Context, cmd, what string) (password, reason string) {
	if r.RunCmd == nil {
		return "", what + " failed: commands cannot be run"
	}
	limit := r.CmdTimeout
	if limit <= 0 {
		limit = 2 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	out, err := r.RunCmd(cctx, cmd)
	if err != nil {
		if cctx.Err() != nil && ctx.Err() == nil {
			return "", fmt.Sprintf("%s failed: timed out after %s", what, waitedText(limit))
		}
		return "", what + " failed: " + err.Error()
	}
	if out == "" {
		return "", what + " returned nothing"
	}
	return out, ""
}

func (r *Resolver) getenv(name string) string {
	if r.Getenv == nil {
		return ""
	}
	return r.Getenv(name)
}
