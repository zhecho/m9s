package view

import (
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/mail"
	msync "github.com/zhecho/m9s/internal/sync"
	"github.com/zhecho/m9s/internal/ui"
)

// The top bar's User: is the account's own address: microsoft.user and
// gmail.user for API accounts, which have no imap block.
func TestHeader_userOfEveryProvider(t *testing.T) {
	for _, c := range []struct {
		cfg  config.Account
		want string
	}{
		{config.Account{Name: "ms", Provider: config.ProviderMicrosoft,
			Microsoft: config.MicrosoftConfig{User: "m9s-test@outlook.com"}}, "m9s-test@outlook.com"},
		{config.Account{Name: "gm", Provider: config.ProviderGmail,
			Gmail: config.GmailConfig{User: "me@gmail.com"}}, "me@gmail.com"},
		{config.Account{Name: "im", IMAP: config.IMAPConfig{User: "imap@x.io"}}, "imap@x.io"},
	} {
		skin, _ := ui.LoadSkin("")
		km, _ := ui.LoadKeyMap("")
		cfg := c.cfg
		ra := NewRootApp(ui.NewApp(skin, km), &config.Config{},
			mail.NewManager([]*mail.Account{{Name: cfg.Name, Config: &cfg}}),
			map[string]msync.Account{}, nil, make(chan msync.Event))
		ra.refreshHeader()
		if got := infoLine(ra, 1); !strings.Contains(got, "User:") || !strings.Contains(got, c.want) {
			t.Errorf("%s: user line = %q, want %q", cfg.Name, got, c.want)
		}
	}
}
