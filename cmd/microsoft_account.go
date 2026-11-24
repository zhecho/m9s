package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/creds"
	"github.com/zhecho/m9s/internal/mail/graph"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	msync "github.com/zhecho/m9s/internal/sync"
	"github.com/zhecho/m9s/internal/ui"
	"golang.org/x/oauth2"
)

// microsoftFactory makes the Graph client of a signed-in account.
type microsoftFactory struct {
	oauth func(clientID string) *oauth2.Config
	opts  []graph.Option // extra client options (tests: the fake's URL)
}

// defaultMicrosoft talks to Microsoft.
var defaultMicrosoft = microsoftFactory{oauth: graph.OAuthConfig}

// signedIn returns acct's Graph client, authorised by refreshToken, and its
// token source, whose sign-in can be swapped and which reports rotations.
func (f microsoftFactory) signedIn(ctx context.Context, acct *config.Account, refreshToken string) (*graph.Client, *refreshingSource) {
	cfg := f.oauth(acct.Microsoft.ClientID)
	tctx := context.WithValue(ctx, oauth2.HTTPClient, gmailTokenHTTPClient())
	src := &refreshingSource{ctx: tctx, cfg: cfg, refreshToken: refreshToken}
	opts := append([]graph.Option{graph.WithRefresh(src.refresh)}, f.opts...)
	return graph.New(gmailHTTPClient(src), opts...), src
}

// buildMicrosoft makes the engine of a microsoft account (not started).
func (b *accountBuilder) buildMicrosoft(cfg *config.Account, r creds.Result) (msync.Account, error) {
	if !r.Works() {
		return nil, fmt.Errorf("not signed in (run: m9s login %s)", cfg.Name)
	}
	if b.store == nil {
		return nil, errors.New("a microsoft account needs the local mail store")
	}
	api, src := b.microsoft.signedIn(b.ctx, cfg, r.Microsoft.RefreshToken.Password)
	if b.saverFor != nil {
		src.onRotate = b.saverFor(cfg)
	}
	eng := msync.NewMicrosoftEngine(cfg, b.events, b.store, b.drafts, api)
	if b.rereadMicrosoft != nil {
		eng.SetReauth(func(ctx context.Context) error {
			token, err := b.rereadMicrosoft(ctx, cfg)
			if err != nil {
				return err
			}
			src.reset(b.microsoft.oauth(cfg.Microsoft.ClientID), token)
			return nil
		})
	}
	return eng, nil
}

// readMicrosoftSignIn reads acct's refresh token from KeePassXC again, in
// a session of its own (see readGmailSignIn).
func readMicrosoftSignIn(ctx context.Context, acct *config.Account) (string, error) {
	s := creds.NewSession(func(text string) {
		if text != "" {
			ui.AppLog.Info("[%s] %s", acct.Name, text)
		}
	})
	defer s.Close()
	token, err := s.Password(ctx, creds.MicrosoftTokenHost, acct.Microsoft.User)
	if err != nil {
		return "", fmt.Errorf("token from KeePassXC: %w", err)
	}
	return token, nil
}

// microsoftAddress signs in (refreshing the access token) and reads the
// address. A token rotated by it is not saved: the one in KeePassXC stays
// valid.
func microsoftAddress(f microsoftFactory) func(ctx context.Context, acct *config.Account, r creds.Result) (string, error) {
	return func(ctx context.Context, acct *config.Account, r creds.Result) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, gmailTimeout)
		defer cancel()
		api, _ := f.signedIn(ctx, acct, r.Microsoft.RefreshToken.Password)
		return api.Me(ctx)
	}
}

// microsoftMissing says why a microsoft account has no sign-in.
func microsoftMissing(acct *config.Account, r creds.Result) []string {
	var reasons []string
	for _, f := range r.Failures {
		if f.Service == creds.ServiceToken {
			reasons = append(reasons, fmt.Sprintf("%s %s: %s", f.Service, f.Source, f.Reason))
		}
	}
	if len(reasons) == 0 {
		reasons = append(reasons, fmt.Sprintf("not set in KeePassXC (run: m9s login %s)", acct.Name))
	}
	return reasons
}

// checkMicrosoftAccount signs acct in and prints one line; it reports
// whether that worked. Never a secret or a token endpoint body.
func checkMicrosoftAccount(acct *config.Account, r creds.Result, deps checkDeps, out io.Writer) bool {
	if !r.Works() {
		fmt.Fprintf(out, "[%s] microsoft: ✗ not signed in\n", acct.Name)
		for _, reason := range microsoftMissing(acct, r) {
			fmt.Fprintf(out, "    %s\n", reason)
		}
		return false
	}
	address, err := deps.microsoft(context.Background(), acct, r)
	switch {
	case errors.Is(err, graph.ErrSignInAgain):
		fmt.Fprintf(out, "[%s] microsoft: ✗ sign in again (run: m9s login %s)\n", acct.Name, acct.Name)
		return false
	case err != nil:
		fmt.Fprintf(out, "[%s] microsoft: ✗ %v\n", acct.Name, err)
		return false
	case !strings.EqualFold(strings.TrimSpace(address), strings.TrimSpace(acct.Microsoft.User)):
		fmt.Fprintf(out, "[%s] microsoft: ✗ signed in as %s, config expects %s\n", acct.Name, address, acct.Microsoft.User)
		return false
	}
	fmt.Fprintf(out, "[%s] microsoft: ✓ signed in as %s (token: %s)\n", acct.Name, address, r.Microsoft.RefreshToken.Source)
	return true
}

// sendMicrosoft sends d from acct through Graph, without starting an
// engine: Graph files it in Sent Items itself.
func sendMicrosoft(ctx context.Context, acct *config.Account, r creds.Result, f microsoftFactory, d smtpadapter.Draft) error {
	if !r.Works() {
		return fmt.Errorf("%q is not signed in (run: m9s login %s): %s", acct.Name, acct.Name,
			strings.Join(microsoftMissing(acct, r), "; "))
	}
	d.From = acct.Microsoft.User
	api, _ := f.signedIn(ctx, acct, r.Microsoft.RefreshToken.Password)
	_, err := msync.NewMicrosoftEngine(acct, nil, nil, nil, api).Send(ctx, d)
	return err
}
