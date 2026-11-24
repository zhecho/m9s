package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/config"
)

func enableCfg() *config.Config {
	return &config.Config{Accounts: []config.Account{{Name: "work"}}, Disabled: []string{"old"}}
}

func TestFindAccount_saysDisabled(t *testing.T) {
	_, err := findAccount(enableCfg(), "old")
	if err == nil || !strings.Contains(err.Error(), `account "old" is disabled`) || !strings.Contains(err.Error(), "enable: false") {
		t.Fatalf("err = %v", err)
	}
	_, err = findAccount(enableCfg(), "nope")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown account: err = %v", err)
	}
}

func TestStart_everyAccountDisabled(t *testing.T) {
	cfg := &config.Config{Disabled: []string{"a", "b"}}
	_, _, err := resolveAccounts(context.Background(), cfg, nil, startDeps{})
	if err == nil || !strings.Contains(err.Error(), "every account is disabled") {
		t.Fatalf("err = %v", err)
	}
}

func TestDisabledLogLines(t *testing.T) {
	lines := disabledLogLines(enableCfg())
	if len(lines) != 1 || lines[0].Msg != "[old] disabled (enable: false): not loaded" {
		t.Fatalf("lines = %+v", lines)
	}
}
