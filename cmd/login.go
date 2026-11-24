package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhecho/m9s/internal/config"
	imapadapter "github.com/zhecho/m9s/internal/mail/imap"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
	"golang.org/x/term"
)

var (
	loginIMAPOnly bool
	loginSMTPOnly bool
	loginNoVerify bool
)

var loginCmd = &cobra.Command{
	Use:   "login <account>",
	Short: "Store an account password in the OS keychain, or sign a Gmail or Microsoft account in",
	Long: `Prompts for the account password, verifies it against the IMAP and SMTP
servers, and stores it in the OS keychain under service "m9s" as
"<account>/imap" and "<account>/smtp". Remove password/password_cmd from the
account in config.yaml so the keychain entry is used.

The password is read from stdin when it is not a terminal.

For a gmail account (provider: gmail) it signs in with Google in the browser
instead, using the OAuth client secret stored in KeePassXC, and stores the
refresh token in KeePassXC (https://oauth2.googleapis.com / gmail.user).

For a microsoft account (provider: microsoft) it signs in with Microsoft in
the browser and stores the refresh token in KeePassXC
(https://graph.microsoft.com / microsoft.user).`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(c *cobra.Command, args []string) error {
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		gdeps, done := gmailDepsFor(cfg, args[0])
		defer done()
		return runLogin(c.Context(), cfg, args[0], loginServices(loginIMAPOnly, loginSMTPOnly), !loginNoVerify,
			defaultLoginDeps(), gdeps, c.OutOrStdout())
	},
}

var logoutCmd = &cobra.Command{
	Use:          "logout <account>",
	Short:        "Remove an account password from the OS keychain, or sign a Gmail or Microsoft account out",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(c *cobra.Command, args []string) error {
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		gdeps, done := gmailDepsFor(cfg, args[0])
		defer done()
		return runLogout(c.Context(), cfg, args[0], loginServices(loginIMAPOnly, loginSMTPOnly), gdeps, c.OutOrStdout())
	},
}

// gmailDepsFor connects to KeePassXC only when name is a gmail account.
func gmailDepsFor(cfg *config.Config, name string) (gmailDeps, func()) {
	if acct, err := findAccount(cfg, name); err == nil && acct.IsGmail() {
		return defaultGmailDeps()
	}
	return gmailDeps{}, func() {}
}

// runLogin signs a gmail account in with Google, or stores a password for
// any other account.
func runLogin(ctx context.Context, cfg *config.Config, name string, services []string, verify bool,
	deps loginDeps, gdeps gmailDeps, out io.Writer) error {
	acct, err := findAccount(cfg, name)
	if err != nil {
		return err
	}
	if acct.IsMicrosoft() {
		if len(services) != 2 {
			return errMicrosoftServices
		}
		mdeps, done := newMicrosoftDeps()
		defer done()
		return doMicrosoftLogin(ctx, acct, mdeps, out)
	}
	if acct.IsGmail() {
		if err := gmailOnlyBoth(services); err != nil {
			return err
		}
		return doGmailLogin(ctx, acct, gdeps, out)
	}
	return doLogin(cfg, name, services, verify, deps, out)
}

// runLogout signs a gmail account out, or removes any other account's
// keychain passwords.
func runLogout(ctx context.Context, cfg *config.Config, name string, services []string, gdeps gmailDeps, out io.Writer) error {
	acct, err := findAccount(cfg, name)
	if err != nil {
		return err
	}
	if acct.IsMicrosoft() {
		if len(services) != 2 {
			return errMicrosoftServices
		}
		return doMicrosoftLogout(acct, out)
	}
	if acct.IsGmail() {
		if err := gmailOnlyBoth(services); err != nil {
			return err
		}
		return doGmailLogout(ctx, acct, gdeps, out)
	}
	return doLogout(cfg, name, services, out)
}

func init() {
	for _, c := range []*cobra.Command{loginCmd, logoutCmd} {
		c.Flags().BoolVar(&loginIMAPOnly, "imap", false, "only the IMAP password (not for gmail accounts)")
		c.Flags().BoolVar(&loginSMTPOnly, "smtp", false, "only the SMTP password (not for gmail accounts)")
		rootCmd.AddCommand(c)
	}
	loginCmd.Flags().BoolVar(&loginNoVerify, "no-verify", false, "store without logging in to the servers first")
}

// loginServices returns the services selected by --imap/--smtp (both when neither is set).
func loginServices(imapOnly, smtpOnly bool) []string {
	switch {
	case imapOnly && !smtpOnly:
		return []string{"imap"}
	case smtpOnly && !imapOnly:
		return []string{"smtp"}
	default:
		return []string{"imap", "smtp"}
	}
}

type loginDeps struct {
	readPassword func(prompt string) (string, error)
	verifyIMAP   func(cfg *config.IMAPConfig, password string) error
	verifySMTP   func(cfg *config.SMTPConfig, password string) error
}

func defaultLoginDeps() loginDeps {
	return loginDeps{
		readPassword: readPassword,
		verifyIMAP: func(cfg *config.IMAPConfig, pw string) error {
			client := imapadapter.New(cfg)
			if err := client.Connect(context.Background(), pw); err != nil {
				return err
			}
			return client.Close()
		},
		verifySMTP: smtpadapter.Probe,
	}
}

func findAccount(cfg *config.Config, name string) (*config.Account, error) {
	for i := range cfg.Accounts {
		if cfg.Accounts[i].Name == name {
			return &cfg.Accounts[i], nil
		}
	}
	if err := disabledAccount(cfg, name); err != nil {
		return nil, err
	}
	names := make([]string, len(cfg.Accounts))
	for i, a := range cfg.Accounts {
		names[i] = a.Name
	}
	return nil, fmt.Errorf("account %q not found (configured: %s)", name, strings.Join(names, ", "))
}

func doLogin(cfg *config.Config, name string, services []string, verify bool, deps loginDeps, out io.Writer) error {
	acct, err := findAccount(cfg, name)
	if err != nil {
		return err
	}
	pw, err := deps.readPassword(fmt.Sprintf("Password for %s (%s): ", name, strings.Join(services, "+")))
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	if pw == "" {
		return fmt.Errorf("empty password, nothing stored")
	}

	if verify {
		for _, svc := range services {
			var err error
			if svc == "imap" {
				err = deps.verifyIMAP(&acct.IMAP, pw)
			} else {
				err = deps.verifySMTP(&acct.SMTP, pw)
			}
			if err != nil {
				return fmt.Errorf("%s login failed, nothing stored: %w", svc, err)
			}
			fmt.Fprintf(out, "%s: ✓ login OK\n", svc)
		}
	}

	for _, svc := range services {
		if err := config.SetKeychainPassword(name, svc, pw); err != nil {
			return fmt.Errorf("keychain %s: %w", svc, err)
		}
		fmt.Fprintf(out, "%s: stored in keychain as %s / %s/%s\n", svc, config.KeychainService, name, svc)
		switch field := shadowingField(acct, svc); field {
		case "":
		case "keepassxc: true":
			fmt.Fprintf(out, "  note: %s is set for this account and is tried first; the keychain is used when KeePassXC has no answer\n", field)
		default:
			fmt.Fprintf(out, "  note: %s.%s is set in config and takes precedence; remove it to use the keychain\n", svc, field)
		}
	}
	return nil
}

func doLogout(cfg *config.Config, name string, services []string, out io.Writer) error {
	if _, err := findAccount(cfg, name); err != nil {
		return err
	}
	for _, svc := range services {
		if err := config.DeleteKeychainPassword(name, svc); err != nil {
			return fmt.Errorf("keychain %s: %w", svc, err)
		}
		fmt.Fprintf(out, "%s: removed from keychain\n", svc)
	}
	return nil
}

// shadowingField names the config setting that is tried before the keychain.
func shadowingField(acct *config.Account, svc string) string {
	cmd, plain := acct.IMAP.PasswordCmd, acct.IMAP.Password
	if svc == "smtp" {
		cmd, plain = acct.SMTP.PasswordCmd, acct.SMTP.Password
	}
	switch {
	case acct.KeePassXC:
		return "keepassxc: true"
	case cmd != "":
		return "password_cmd"
	case plain != "":
		return "password"
	}
	return ""
}

func readPassword(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	return string(b), err
}
