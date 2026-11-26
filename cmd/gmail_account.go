package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	gosync "sync"
	"time"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/creds"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/mail/gmail"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	"github.com/zhecho/m9s/internal/store"
	msync "github.com/zhecho/m9s/internal/sync"
	"github.com/zhecho/m9s/internal/ui"
	"golang.org/x/oauth2"
)

// gmailTimeout is how long Gmail, or the token endpoint, may take to start
// answering: a server that never answers is cut off, so polling goes on.
const gmailTimeout = 60 * time.Second

// gmailTransferTimeout bounds a whole API request, body included: long
// enough for a big raw download or an attachment sent on a slow link.
const gmailTransferTimeout = 10 * time.Minute

// gmailFactory makes the Gmail API client of a signed-in account.
type gmailFactory struct {
	oauth func(clientID, clientSecret string) *oauth2.Config
	opts  []gmail.Option // extra client options (tests: the fake's URL)
}

// defaultGmail talks to Google.
var defaultGmail = gmailFactory{oauth: gmail.OAuthConfig}

// client returns acct's Gmail client, authorised by the refresh token in r.
// Its refresh hook forces a new access token after a 401.
func (f gmailFactory) client(ctx context.Context, acct *config.Account, r creds.Result) *gmail.Client {
	c, _ := f.signedIn(ctx, acct, r)
	return c
}

// signedIn is client plus its token source, whose sign-in can be swapped.
func (f gmailFactory) signedIn(ctx context.Context, acct *config.Account, r creds.Result) (*gmail.Client, *refreshingSource) {
	cfg := f.oauth(acct.Gmail.ClientID, r.Gmail.ClientSecret.Password)
	tctx := context.WithValue(ctx, oauth2.HTTPClient, gmailTokenHTTPClient())
	src := &refreshingSource{ctx: tctx, cfg: cfg, refreshToken: r.Gmail.RefreshToken.Password}
	opts := append([]gmail.Option{gmail.WithRefresh(src.refresh)}, f.opts...)
	return gmail.New(gmailHTTPClient(src), opts...), src
}

// gmailTokenHTTPClient fetches access tokens: small answers, 60 s in all.
func gmailTokenHTTPClient() *http.Client { return &http.Client{Timeout: gmailTimeout} }

// gmailHTTPClient authorises every request with src's access token. Not
// oauth2.NewClient: it caches the token in front of src, so the refresh
// hook could not replace a token Google has rejected.
func gmailHTTPClient(src oauth2.TokenSource) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.ResponseHeaderTimeout = gmailTimeout
	return &http.Client{
		Transport: &oauth2.Transport{Source: src, Base: base},
		Timeout:   gmailTransferTimeout,
	}
}

// startupWarnings shows the first warning on the status bar (showError logs
// it too) and logs the rest: an account left out comes first, so it never
// vanishes from the screen unexplained.
func startupWarnings(buildWarns, themeWarns []string, showError func(string), log func(string)) {
	all := append(append([]string(nil), buildWarns...), themeWarns...)
	for i, w := range all {
		if i == 0 {
			showError(w)
		} else {
			log(w)
		}
	}
}

// refreshingSource caches the access token got with a refresh token until
// it expires or refresh drops it.
type refreshingSource struct {
	ctx context.Context
	cfg *oauth2.Config
	// onRotate, when set, is told every new refresh token the provider
	// hands out (Microsoft: on every refresh). Called without the lock held.
	onRotate func(refreshToken string)

	mu           gosync.Mutex
	refreshToken string
	tok          *oauth2.Token
}

func (s *refreshingSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	if s.tok.Valid() {
		tok := s.tok
		s.mu.Unlock()
		return tok, nil
	}
	tok, err := s.cfg.TokenSource(s.ctx, &oauth2.Token{RefreshToken: s.refreshToken}).Token()
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	rotated := ""
	if tok.RefreshToken != "" && tok.RefreshToken != s.refreshToken {
		s.refreshToken = tok.RefreshToken // the provider replaced it
		rotated = tok.RefreshToken
	}
	s.tok = tok
	onRotate := s.onRotate
	s.mu.Unlock()
	if rotated != "" && onRotate != nil {
		onRotate(rotated)
	}
	return tok, nil
}

// refresh drops the cached access token and gets a new one.
func (s *refreshingSource) refresh(context.Context) error {
	s.mu.Lock()
	s.tok = nil
	s.mu.Unlock()
	_, err := s.Token()
	return err
}

// reset swaps in a new sign-in and drops the cached access token.
func (s *refreshingSource) reset(cfg *oauth2.Config, refreshToken string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg, s.refreshToken, s.tok = cfg, refreshToken, nil
}

// accountBuilder makes the account engines m9s runs with.
type accountBuilder struct {
	ctx    context.Context
	events chan<- msync.Event
	store  *store.Store
	drafts *draft.Store
	gmail  gmailFactory
	// reread reads a gmail account's client secret and refresh token again,
	// for Retry after "sign in again". nil: only a restart picks them up.
	reread func(ctx context.Context, acct *config.Account) (clientSecret, refreshToken string, err error)

	microsoft microsoftFactory
	// rereadMicrosoft reads a microsoft account's refresh token again, as
	// reread does for gmail.
	rereadMicrosoft func(ctx context.Context, acct *config.Account) (refreshToken string, err error)
	// saverFor returns what a microsoft account's new refresh tokens are
	// handed to (its token saver); nil: they are kept in memory only.
	saverFor func(acct *config.Account) func(refreshToken string)
}

// readGmailSignIn reads acct's client secret and refresh token from
// KeePassXC again, in a session of its own: the startup one is closed, and
// a long-lived one would answer from its cache. KeePassXC's notices (locked,
// approve) go to the log. Errors never carry a secret.
func readGmailSignIn(ctx context.Context, acct *config.Account) (string, string, error) {
	s := creds.NewSession(func(text string) {
		if text != "" {
			ui.AppLog.Info("[%s] %s", acct.Name, text)
		}
	})
	defer s.Close()
	secret, err := s.Password(ctx, creds.GmailClientHost, acct.Gmail.ClientID)
	if err != nil {
		return "", "", fmt.Errorf("OAuth client secret from KeePassXC: %w", err)
	}
	token, err := s.Password(ctx, creds.GmailTokenHost, acct.Gmail.User)
	if err != nil {
		return "", "", fmt.Errorf("token from KeePassXC: %w", err)
	}
	ui.AppLog.Info("[%s] KeePassXC entries: %s (client secret), %s (token)", acct.Name,
		s.Where(ctx, creds.GmailClientHost, acct.Gmail.ClientID), s.Where(ctx, creds.GmailTokenHost, acct.Gmail.User))
	return secret, token, nil
}

// buildAccount makes the engine of cfg with the secrets in r: the Gmail API
// engine for a gmail account, the Graph one for a microsoft account, else
// the IMAP/SMTP one. It is not started.
func (b *accountBuilder) buildAccount(cfg *config.Account, r creds.Result) (msync.Account, error) {
	if cfg.IsMicrosoft() {
		return b.buildMicrosoft(cfg, r)
	}
	if cfg.IsGmail() {
		if !r.Works() {
			return nil, fmt.Errorf("not signed in (run: m9s login %s)", cfg.Name)
		}
		if b.store == nil {
			return nil, errors.New("a gmail account needs the local mail store")
		}
		api, src := b.gmail.signedIn(b.ctx, cfg, r)
		eng := msync.NewGmailEngine(cfg, b.events, b.store, b.drafts, api)
		if b.reread != nil {
			eng.SetReauth(func(ctx context.Context) error {
				secret, token, err := b.reread(ctx, cfg)
				if err != nil {
					return err
				}
				src.reset(b.gmail.oauth(cfg.Gmail.ClientID, secret), token)
				return nil
			})
		}
		return eng, nil
	}
	eng := msync.NewEngine(cfg, b.events)
	eng.SetPassword(r.IMAP.Password)
	eng.SetSMTPPassword(r.SMTP.Password)
	eng.SetDraftStore(b.drafts)
	if b.store != nil {
		eng.SetStore(b.store)
	}
	return eng, nil
}

// gmailProfile signs in (refreshing the access token) and reads the address.
func gmailProfile(f gmailFactory) func(ctx context.Context, acct *config.Account, r creds.Result) (string, error) {
	return func(ctx context.Context, acct *config.Account, r creds.Result) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, gmailTimeout)
		defer cancel()
		email, _, err := f.client(ctx, acct, r).Profile(ctx)
		return email, err
	}
}

// gmailMissing says why a gmail account has no sign-in, from r's failures.
func gmailMissing(acct *config.Account, r creds.Result) []string {
	var reasons []string
	for _, f := range r.Failures {
		if f.Service == creds.ServiceClient || f.Service == creds.ServiceToken {
			reasons = append(reasons, fmt.Sprintf("%s %s: %s", f.Service, f.Source, f.Reason))
		}
	}
	if len(reasons) == 0 {
		reasons = append(reasons, fmt.Sprintf("not set in KeePassXC (run: m9s login %s)", acct.Name))
	}
	return reasons
}

// sendGmail sends d from acct through the Gmail API, without starting an
// engine: Gmail files it in Sent itself.
func sendGmail(ctx context.Context, acct *config.Account, r creds.Result, f gmailFactory, d smtpadapter.Draft) error {
	if !r.Works() {
		return fmt.Errorf("%q is not signed in (run: m9s login %s): %s", acct.Name, acct.Name,
			strings.Join(gmailMissing(acct, r), "; "))
	}
	d.From = acct.Gmail.User
	eng := msync.NewGmailEngine(acct, nil, nil, nil, f.client(ctx, acct, r))
	_, err := eng.Send(ctx, d)
	return err
}
