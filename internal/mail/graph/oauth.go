package graph

import (
	"context"
	"io"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/microsoft"

	"github.com/zhecho/m9s/internal/mail/oauthloop"
)

// Scopes are what m9s asks for: offline_access for a refresh token,
// mail read/write and send, MailboxSettings.Read for the category list and
// User.Read for the signed-in address.
var Scopes = []string{"offline_access", "Mail.ReadWrite", "Mail.Send", "MailboxSettings.Read", "User.Read"}

// OAuthConfig is the public (secret-less) client for the "common"
// authority, which serves personal and work accounts alike. The sign-in
// sets the redirect URL and uses PKCE.
func OAuthConfig(clientID string) *oauth2.Config {
	ep := microsoft.AzureADEndpoint("common")
	ep.AuthStyle = oauth2.AuthStyleInParams
	return &oauth2.Config{ClientID: clientID, Scopes: Scopes, Endpoint: ep}
}

// loginTimeout is how long Login waits for the browser to come back.
var loginTimeout = 5 * time.Minute

// Login signs in through the browser (see oauthloop.Run). The redirect is
// http://localhost:<port>, which a "Mobile and desktop applications"
// registration with redirect http://localhost accepts on any port; the
// owner picks the account to sign in with every time.
func Login(ctx context.Context, cfg *oauth2.Config, openBrowser func(url string) error, out io.Writer) (*oauth2.Token, error) {
	return oauthloop.Run(ctx, cfg, oauthloop.Options{
		Host: "localhost", Provider: "Microsoft", Timeout: loginTimeout,
		AuthOptions: []oauth2.AuthCodeOption{oauth2.SetAuthURLParam("prompt", "select_account")},
	}, openBrowser, out)
}
