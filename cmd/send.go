package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhecho/m9s/internal/config"
	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
)

var (
	sendTo      string
	sendSubject string
	sendBody    string
	sendAccount string
)

var sendCmd = &cobra.Command{
	Use:          "send",
	Short:        "Send a test email via configured SMTP (or the Gmail / Microsoft Graph API)",
	SilenceUsage: true,
	RunE:         runSend,
}

func init() {
	sendCmd.Flags().StringVar(&sendTo, "to", "", "recipient address (required)")
	sendCmd.Flags().StringVar(&sendSubject, "subject", "", "email subject (required)")
	sendCmd.Flags().StringVar(&sendBody, "body", "", "email body")
	sendCmd.Flags().StringVarP(&sendAccount, "account", "a", "", "account name (default: first account)")
	_ = sendCmd.MarkFlagRequired("to")
	_ = sendCmd.MarkFlagRequired("subject")
	rootCmd.AddCommand(sendCmd)
}

func runSend(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if len(cfg.Accounts) == 0 {
		return fmt.Errorf("no accounts configured")
	}

	var acctCfg *config.Account
	for i := range cfg.Accounts {
		if sendAccount == "" || cfg.Accounts[i].Name == sendAccount {
			acctCfg = &cfg.Accounts[i]
			break
		}
	}
	if acctCfg == nil {
		if err := disabledAccount(cfg, sendAccount); err != nil {
			return err
		}
		return fmt.Errorf("account %q not found", sendAccount)
	}

	// The same lookup m9s does at startup, for this one account.
	look := newLookup()
	results, _ := look.run(cmd.Context(), []config.Account{*acctCfg}, nil, func(text string) {
		if text != "" {
			fmt.Fprintln(os.Stderr, text)
		}
	})
	look.close()
	if acctCfg.IsMicrosoft() {
		d := smtpadapter.Draft{To: []string{sendTo}, Subject: sendSubject, Body: sendBody}
		if err := sendMicrosoft(cmd.Context(), acctCfg, results[0], defaultMicrosoft, d); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Sent.")
		return nil
	}
	if acctCfg.IsGmail() {
		d := smtpadapter.Draft{To: []string{sendTo}, Subject: sendSubject, Body: sendBody}
		if err := sendGmail(cmd.Context(), acctCfg, results[0], defaultGmail, d); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Sent.")
		return nil
	}
	if !results[0].SMTP.OK {
		reasons := []string{}
		for _, f := range results[0].Failures {
			if f.Service == "smtp" {
				reasons = append(reasons, fmt.Sprintf("%s: %s", f.Source, f.Reason))
			}
		}
		if len(reasons) == 0 {
			reasons = append(reasons, fmt.Sprintf("not set in any source (run: m9s login %s)", acctCfg.Name))
		}
		return fmt.Errorf("no SMTP password for %q: %s", acctCfg.Name, strings.Join(reasons, "; "))
	}
	pass := results[0].SMTP.Password

	s := smtpadapter.New(&acctCfg.SMTP)
	draft := smtpadapter.Draft{
		From:    acctCfg.SMTP.User,
		To:      []string{sendTo},
		Subject: sendSubject,
		Body:    sendBody,
	}
	if _, err := s.Send(pass, draft); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Sent.")
	return nil
}
