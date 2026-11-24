package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/creds"
	"github.com/zhecho/m9s/internal/mail/gmail"
	"golang.org/x/oauth2"
)

// gmailDeps is what sign-in and sign-out of a gmail account need from the
// outside world: KeePassXC, the browser and Google.
type gmailDeps struct {
	secret    func(ctx context.Context, host, user string) (string, error)
	saveLogin func(ctx context.Context, host, user, password string) error
	signIn    func(ctx context.Context, cfg *oauth2.Config, out io.Writer) (*oauth2.Token, error)
	profile   func(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) (email string, err error)
	revoke    func(ctx context.Context, token string) error
}

// defaultGmailDeps talks to the running KeePassXC (one session, as at
// startup; its notices go to stderr), the browser (macOS open) and Google.
// close ends the KeePassXC connection.
func defaultGmailDeps() (deps gmailDeps, close func()) {
	session := creds.NewSession(func(text string) {
		if text != "" {
			fmt.Fprintln(os.Stderr, text)
		}
	})
	return gmailDeps{
		secret:    session.Password,
		saveLogin: session.SaveLogin,
		signIn: func(ctx context.Context, cfg *oauth2.Config, out io.Writer) (*oauth2.Token, error) {
			return gmail.Login(ctx, cfg, openBrowser, out)
		},
		profile: func(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) (string, error) {
			email, _, err := gmail.New(cfg.Client(ctx, tok)).Profile(ctx)
			return email, err
		},
		revoke: gmail.Revoke,
	}, session.Close
}

func openBrowser(url string) error { return exec.Command("open", url).Run() }

// errGmailServices is --imap or --smtp given for a gmail account.
var errGmailServices = errors.New("--imap/--smtp do not apply to a gmail account (it has one Google sign-in)")

// gmailOnlyBoth reports whether services is the default selection: a gmail
// account takes no --imap/--smtp.
func gmailOnlyBoth(services []string) error {
	if len(services) != 2 {
		return errGmailServices
	}
	return nil
}

// doGmailLogin signs acct in with Google and stores the refresh token in
// KeePassXC, after checking that the signed-in address is acct's.
func doGmailLogin(ctx context.Context, acct *config.Account, deps gmailDeps, out io.Writer) error {
	user := acct.Gmail.User
	clientSecret, err := deps.secret(ctx, creds.GmailClientHost, acct.Gmail.ClientID)
	if err != nil {
		return fmt.Errorf("OAuth client secret from KeePassXC: %w", err)
	}
	cfg := gmail.OAuthConfig(acct.Gmail.ClientID, clientSecret)

	tok, err := deps.signIn(ctx, cfg, out)
	if err != nil {
		return err
	}
	if tok.RefreshToken == "" {
		return errors.New("Google sent no refresh token: nothing stored")
	}
	email, err := deps.profile(ctx, cfg, tok)
	if err != nil {
		return fmt.Errorf("reading the signed-in address: %w: nothing stored", err)
	}
	if !strings.EqualFold(strings.TrimSpace(email), strings.TrimSpace(user)) {
		return fmt.Errorf("signed in as %s, config expects %s: nothing stored", email, user)
	}
	if err := deps.saveLogin(ctx, creds.GmailTokenHost, user, tok.RefreshToken); err != nil {
		return err
	}
	fmt.Fprintf(out, "token: stored in KeePassXC (https://%s / %s)\n", creds.GmailTokenHost, user)
	return nil
}

// doGmailLogout revokes acct's refresh token at Google. The KeePassXC
// protocol cannot delete entries, so the owner removes it.
func doGmailLogout(ctx context.Context, acct *config.Account, deps gmailDeps, out io.Writer) error {
	user := acct.Gmail.User
	token, err := deps.secret(ctx, creds.GmailTokenHost, user)
	if err != nil {
		return fmt.Errorf("token from KeePassXC: %w", err)
	}
	if err := deps.revoke(ctx, token); err != nil {
		return err
	}
	fmt.Fprintf(out, "revoked at Google; delete the KeePassXC entry https://%s / %s yourself\n", creds.GmailTokenHost, user)
	return nil
}
