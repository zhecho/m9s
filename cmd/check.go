package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/creds"
	"github.com/zhecho/m9s/internal/mail/gmail"
	imapadapter "github.com/zhecho/m9s/internal/mail/imap"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
)

var checkAccount string

var checkCmd = &cobra.Command{
	Use:          "check",
	Short:        "Test IMAP and SMTP (or Gmail / Microsoft) connectivity for configured accounts",
	SilenceUsage: true,
	RunE:         runCheck,
}

func init() {
	checkCmd.Flags().StringVarP(&checkAccount, "account", "a", "", "account name (default: all accounts)")
	rootCmd.AddCommand(checkCmd)
}

// checkDeps are the logins a check performs: IMAP and SMTP, or the Gmail
// sign-in (a token refresh, then the profile's address).
type checkDeps struct {
	imap  func(cfg *config.IMAPConfig, pass string) (folders int, err error)
	smtp  func(cfg *config.SMTPConfig, pass string) error
	gmail func(ctx context.Context, acct *config.Account, r creds.Result) (email string, err error)
	// microsoft signs a microsoft account in and reads its address.
	microsoft func(ctx context.Context, acct *config.Account, r creds.Result) (address string, err error)
}

func defaultCheckDeps() checkDeps {
	return checkDeps{
		imap: func(cfg *config.IMAPConfig, pass string) (int, error) {
			client := imapadapter.New(cfg)
			if err := client.Connect(context.Background(), pass); err != nil {
				return 0, err
			}
			folders, err := client.Folders(context.Background())
			_ = client.Close() // best-effort logout
			if err != nil {
				return 0, fmt.Errorf("folders: %w", err)
			}
			return len(folders), nil
		},
		smtp:      func(cfg *config.SMTPConfig, pass string) error { return smtpadapter.Probe(cfg, pass) },
		gmail:     gmailProfile(defaultGmail),
		microsoft: microsoftAddress(defaultMicrosoft),
	}
}

func runCheck(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	var accounts []config.Account
	for _, a := range cfg.Accounts {
		if checkAccount == "" || a.Name == checkAccount {
			accounts = append(accounts, a)
		}
	}
	if checkAccount != "" && len(accounts) == 0 {
		if err := disabledAccount(cfg, checkAccount); err != nil {
			return err
		}
		return fmt.Errorf("account %q not found", checkAccount)
	}

	// The same lookup m9s does at startup, reported as lines. A wait on
	// KeePassXC is announced on stderr; Ctrl-C ends it.
	look := newLookup()
	results, _ := look.run(cmd.Context(), accounts, nil, func(text string) {
		if text != "" {
			fmt.Fprintln(os.Stderr, text)
		}
	})
	look.close()

	if reportCheck(accounts, results, defaultCheckDeps(), os.Stdout) {
		os.Exit(1)
	}
	return nil
}

// reportCheck tries both logins of every account with the passwords found
// and prints one line per service, naming the source of the password. It
// reports whether anything failed.
func reportCheck(accounts []config.Account, results []creds.Result, deps checkDeps, out io.Writer) (failed bool) {
	byName := make(map[string]creds.Result, len(results))
	for _, r := range results {
		byName[r.Account] = r
	}
	for i := range accounts {
		acct := &accounts[i]
		r := byName[acct.Name]
		if acct.IsMicrosoft() {
			if !checkMicrosoftAccount(acct, r, deps, out) {
				failed = true
			}
			continue
		}
		if acct.IsGmail() {
			if !checkGmailAccount(acct, r, deps, out) {
				failed = true
			}
			continue
		}

		noPassword := func(label, service string) {
			failed = true
			fmt.Fprintf(out, "[%s] %s: ✗ no password\n", acct.Name, label)
			reasons := 0
			for _, f := range r.Failures {
				if f.Service == service {
					fmt.Fprintf(out, "    %s: %s\n", f.Source, f.Reason)
					reasons++
				}
			}
			if reasons == 0 {
				fmt.Fprintf(out, "    not set in any source (run: m9s login %s)\n", acct.Name)
			}
		}

		if !r.IMAP.OK {
			noPassword("IMAP", "imap")
		} else if folders, err := deps.imap(&acct.IMAP, r.IMAP.Password); err != nil {
			fmt.Fprintf(out, "[%s] IMAP: ✗ %v\n", acct.Name, err)
			failed = true
		} else {
			fmt.Fprintf(out, "[%s] IMAP: ✓ connected, %d folders (password: %s)\n", acct.Name, folders, r.IMAP.Source)
		}

		if !r.SMTP.OK {
			noPassword("SMTP", "smtp")
		} else if err := deps.smtp(&acct.SMTP, r.SMTP.Password); err != nil {
			fmt.Fprintf(out, "[%s] SMTP: ✗ %v\n", acct.Name, err)
			failed = true
		} else {
			fmt.Fprintf(out, "[%s] SMTP: ✓ auth OK (password: %s)\n", acct.Name, r.SMTP.Source)
		}
	}
	return failed
}

// checkGmailAccount signs acct in with Google and prints one line; it
// reports whether that worked. Never a secret or a token endpoint body.
func checkGmailAccount(acct *config.Account, r creds.Result, deps checkDeps, out io.Writer) bool {
	if !r.Works() {
		fmt.Fprintf(out, "[%s] gmail: ✗ not signed in\n", acct.Name)
		for _, reason := range gmailMissing(acct, r) {
			fmt.Fprintf(out, "    %s\n", reason)
		}
		return false
	}
	email, err := deps.gmail(context.Background(), acct, r)
	switch {
	case errors.Is(err, gmail.ErrSignInAgain):
		fmt.Fprintf(out, "[%s] gmail: ✗ sign in again (run: m9s login %s)\n", acct.Name, acct.Name)
		return false
	case err != nil:
		fmt.Fprintf(out, "[%s] gmail: ✗ %v\n", acct.Name, err)
		return false
	case !strings.EqualFold(strings.TrimSpace(email), strings.TrimSpace(acct.Gmail.User)):
		fmt.Fprintf(out, "[%s] gmail: ✗ signed in as %s, config expects %s\n", acct.Name, email, acct.Gmail.User)
		return false
	}
	fmt.Fprintf(out, "[%s] gmail: ✓ signed in as %s (secrets: client %s, token %s)\n",
		acct.Name, email, r.Gmail.ClientSecret.Source, r.Gmail.RefreshToken.Source)
	return true
}
