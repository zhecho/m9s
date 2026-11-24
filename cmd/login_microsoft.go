package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/creds"
	"github.com/zhecho/m9s/internal/mail/graph"
	"golang.org/x/oauth2"
)

// microsoftDeps is what sign-in of a microsoft account needs from the
// outside world: KeePassXC, the browser and Microsoft.
type microsoftDeps struct {
	saveLogin func(ctx context.Context, host, user, password string) error
	signIn    func(ctx context.Context, cfg *oauth2.Config, out io.Writer) (*oauth2.Token, error)
	me        func(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) (address string, err error)
}

// newMicrosoftDeps makes the real deps; close ends the KeePassXC
// connection. A variable so tests can replace it.
var newMicrosoftDeps = func() (microsoftDeps, func()) {
	session := creds.NewSession(func(text string) {
		if text != "" {
			fmt.Fprintln(os.Stderr, text)
		}
	})
	return microsoftDeps{
		saveLogin: session.SaveLogin,
		signIn: func(ctx context.Context, cfg *oauth2.Config, out io.Writer) (*oauth2.Token, error) {
			return graph.Login(ctx, cfg, openBrowser, out)
		},
		me: func(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) (string, error) {
			return graph.New(cfg.Client(ctx, tok)).Me(ctx)
		},
	}, session.Close
}

// errMicrosoftServices is --imap or --smtp given for a microsoft account.
var errMicrosoftServices = errors.New("--imap/--smtp do not apply to a microsoft account (it has one Microsoft sign-in)")

// doMicrosoftLogin signs acct in with Microsoft and stores the refresh
// token in KeePassXC, after checking that the signed-in address is acct's.
func doMicrosoftLogin(ctx context.Context, acct *config.Account, deps microsoftDeps, out io.Writer) error {
	user := acct.Microsoft.User
	cfg := graph.OAuthConfig(acct.Microsoft.ClientID)
	tok, err := deps.signIn(ctx, cfg, out)
	if err != nil {
		return err
	}
	if tok.RefreshToken == "" {
		return errors.New("Microsoft sent no refresh token (is offline_access granted?): nothing stored")
	}
	address, err := deps.me(ctx, cfg, tok)
	if err != nil {
		return fmt.Errorf("reading the signed-in address: %w: nothing stored", err)
	}
	if !strings.EqualFold(strings.TrimSpace(address), strings.TrimSpace(user)) {
		return fmt.Errorf("signed in as %s, config expects %s: nothing stored", address, user)
	}
	if err := deps.saveLogin(ctx, creds.MicrosoftTokenHost, user, tok.RefreshToken); err != nil {
		return err
	}
	fmt.Fprintf(out, "token: stored in KeePassXC (https://%s / %s)\n", creds.MicrosoftTokenHost, user)
	return nil
}

// doMicrosoftLogout says how to end acct's sign-in: Microsoft has no call
// that revokes one public client's token, and the KeePassXC protocol cannot
// delete entries.
func doMicrosoftLogout(acct *config.Account, out io.Writer) error {
	fmt.Fprintf(out, "Microsoft cannot revoke one app's sign-in from here. To end it:\n"+
		"  - remove the m9s app's access at https://account.live.com/consent/Manage (personal account)\n"+
		"    or https://myapps.microsoft.com (work or school account)\n"+
		"  - delete the KeePassXC entry https://%s / %s\n", creds.MicrosoftTokenHost, acct.Microsoft.User)
	return nil
}
