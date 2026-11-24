// Package oauthloop is the browser sign-in m9s uses for API accounts: OAuth
// 2.0 authorization code with PKCE and a loopback redirect.
package oauthloop

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// Options says how one provider's sign-in differs.
type Options struct {
	// Host is the redirect's host name: "127.0.0.1" (Google) or "localhost"
	// (what a Microsoft app registration names). The listener is always on
	// 127.0.0.1.
	Host string
	// Path ends the redirect URL: "/" or "" (none).
	Path string
	// Provider names the provider in what the owner reads: "Google".
	Provider string
	// AuthOptions are added to the consent page URL.
	AuthOptions []oauth2.AuthCodeOption
	// Timeout is how long the browser may take to come back.
	Timeout time.Duration
}

// Run signs in through the browser: it listens on a loopback port, has
// openBrowser show the provider's consent page (the URL is printed to out
// too), takes the code sent back, checks the state, and exchanges the code
// with its PKCE verifier. It gives up after o.Timeout.
//
// Nothing secret is written to out or put into an error.
func Run(ctx context.Context, cfg *oauth2.Config, o Options, openBrowser func(url string) error, out io.Writer) (*oauth2.Token, error) {
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("sign-in: cannot listen on the loopback address: %w", err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	c := *cfg
	c.RedirectURL = "http://" + net.JoinHostPort(o.Host, port) + o.Path

	state, err := randomState()
	if err != nil {
		ln.Close()
		return nil, err
	}
	verifier := oauth2.GenerateVerifier()

	type answer struct {
		code string
		err  error
	}
	answers := make(chan answer, 1)
	give := func(a answer) {
		select {
		case answers <- a:
		default: // the first answer counts
		}
	}
	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			q := r.URL.Query()
			switch {
			case q.Get("state") != state:
				http.Error(w, "m9s: this sign-in answer does not belong to the sign-in in progress.", http.StatusBadRequest)
				give(answer{err: errors.New("sign-in failed: the answer's state does not match (not from this sign-in)")})
			case q.Get("error") != "":
				http.Error(w, "m9s: sign-in was not completed. You can close this tab.", http.StatusOK)
				give(answer{err: fmt.Errorf("sign-in failed: %s answered %s", o.Provider, SafeCode(q.Get("error")))})
			case q.Get("code") == "":
				http.Error(w, "m9s: no sign-in code in this answer.", http.StatusBadRequest)
				give(answer{err: fmt.Errorf("sign-in failed: %s sent no code", o.Provider)})
			default:
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				fmt.Fprint(w, "m9s: signed in. You can close this tab.")
				give(answer{code: q.Get("code")})
			}
		}),
	}
	go srv.Serve(ln)
	defer func() {
		sctx, scancel := context.WithTimeout(context.Background(), time.Second)
		defer scancel()
		srv.Shutdown(sctx)
	}()

	opts := append([]oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}, o.AuthOptions...)
	authURL := c.AuthCodeURL(state, opts...)
	fmt.Fprintf(out, "Sign in to %s in the browser. If it does not open, visit:\n\n  %s\n\n", o.Provider, authURL)
	if err := openBrowser(authURL); err != nil {
		fmt.Fprintf(out, "could not open the browser (%v); open the address above yourself\n", err)
	}

	var a answer
	select {
	case a = <-answers:
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("sign-in timed out after %s", o.Timeout)
		}
		return nil, ctx.Err()
	}
	if a.err != nil {
		return nil, a.err
	}

	tok, err := c.Exchange(ctx, a.code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, safeTokenError(err, o)
	}
	return tok, nil
}

// safeTokenError keeps only the OAuth error code of a token endpoint
// failure: the body is never shown.
func safeTokenError(err error, o Options) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		if re.ErrorCode != "" {
			return fmt.Errorf("sign-in failed: token exchange answered %s", SafeCode(re.ErrorCode))
		}
		if re.Response != nil {
			return fmt.Errorf("sign-in failed: token exchange answered HTTP %d", re.Response.StatusCode)
		}
		return errors.New("sign-in failed: token exchange failed")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("sign-in timed out after %s", o.Timeout)
	}
	return fmt.Errorf("sign-in failed: %s could not be reached for the token", o.Provider)
}

// SafeCode is an OAuth error code fit to show: short, plain characters.
func SafeCode(s string) string {
	if len(s) > 40 {
		s = s[:40]
	}
	return strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || r == '.' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '?'
	}, s)
}

func randomState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("sign-in: no randomness for the state")
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
