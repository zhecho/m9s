package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/zhecho/m9s/internal/mail/oauthloop"
)

// Scope is full Gmail access. Narrower scopes cannot delete for good
// (Empty Trash), so m9s asks for this one.
const Scope = "https://mail.google.com/"

var (
	// loginTimeout is how long Login waits for the browser to come back.
	loginTimeout = 5 * time.Minute
	// revokeURL is Google's token revocation endpoint.
	revokeURL = "https://oauth2.googleapis.com/revoke"
)

// OAuthConfig is the desktop OAuth client for Google with the Gmail scope.
// Login sets the redirect URL for each sign-in.
func OAuthConfig(clientID, clientSecret string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       []string{Scope},
		Endpoint: oauth2.Endpoint{
			AuthURL:   "https://accounts.google.com/o/oauth2/auth",
			TokenURL:  "https://oauth2.googleapis.com/token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

// Login signs in through the browser (see oauthloop.Run) on a 127.0.0.1
// redirect. It asks for offline access with a fresh consent so Google
// returns a refresh token. It gives up after five minutes.
//
// Nothing secret is written to out or put into an error.
func Login(ctx context.Context, cfg *oauth2.Config, openBrowser func(url string) error, out io.Writer) (*oauth2.Token, error) {
	return oauthloop.Run(ctx, cfg, oauthloop.Options{
		Host: "127.0.0.1", Path: "/", Provider: "Google", Timeout: loginTimeout,
		AuthOptions: []oauth2.AuthCodeOption{oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent")},
	}, openBrowser, out)
}

// Revoke ends the sign-in a token (refresh or access) belongs to.
func Revoke(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, revokeURL,
		strings.NewReader(url.Values{"token": {token}}.Encode()))
	if err != nil {
		return errors.New("revoke: bad request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("revoke: Google could not be reached")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body)
	if body.Error != "" {
		return fmt.Errorf("revoke failed: HTTP %d %s", resp.StatusCode, oauthloop.SafeCode(body.Error))
	}
	return fmt.Errorf("revoke failed: HTTP %d", resp.StatusCode)
}
