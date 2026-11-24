// internal/config/folder_config_test.go
package config_test

import (
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/config"
	"gopkg.in/yaml.v3"
)

func TestDuration_unmarshal(t *testing.T) {
	type wrapper struct {
		D config.Duration `yaml:"d"`
	}
	cases := []struct {
		input string
		want  time.Duration
	}{
		{"d: 60s", 60 * time.Second},
		{"d: 10m", 10 * time.Minute},
		{"d: 1h", time.Hour},
		{"d: 0", 0},
	}
	for _, c := range cases {
		var w wrapper
		if err := yaml.Unmarshal([]byte(c.input), &w); err != nil {
			t.Fatalf("unmarshal %q: %v", c.input, err)
		}
		if w.D.D() != c.want {
			t.Errorf("input %q: want %v, got %v", c.input, c.want, w.D.D())
		}
	}
}

func TestFolderConfig_defaults(t *testing.T) {
	raw := `
accounts:
  - name: test
    imap:
      host: imap.example.com
      port: 993
    smtp:
      host: smtp.example.com
      port: 587
`
	cfg, err := config.LoadFromBytes([]byte(raw))
	if err != nil {
		t.Fatalf("LoadFromBytes: %v", err)
	}
	fc := cfg.Accounts[0].Folders
	if fc.BadgePoll.D() != 60*time.Second {
		t.Errorf("default BadgePoll: want 60s, got %v", fc.BadgePoll.D())
	}
	if fc.BodyCachePoll.D() != 10*time.Minute {
		t.Errorf("default BodyCachePoll: want 10m, got %v", fc.BodyCachePoll.D())
	}
	if !fc.ShouldAutoDetect() {
		t.Error("AutoDetect should default to true")
	}
}

func TestFolderConfig_explicit(t *testing.T) {
	raw := `
accounts:
  - name: test
    imap:
      host: imap.example.com
      port: 993
    smtp:
      host: smtp.example.com
      port: 587
    folders:
      auto_detect: false
      badge_poll: 30s
      body_cache_poll: 5m
      include:
        - name: "Work"
          badge_poll: 15s
      exclude:
        - "Spam"
`
	cfg, err := config.LoadFromBytes([]byte(raw))
	if err != nil {
		t.Fatalf("LoadFromBytes: %v", err)
	}
	fc := cfg.Accounts[0].Folders
	if fc.ShouldAutoDetect() {
		t.Error("AutoDetect should be false when explicitly set")
	}
	if fc.BadgePoll.D() != 30*time.Second {
		t.Errorf("BadgePoll: want 30s, got %v", fc.BadgePoll.D())
	}
	if len(fc.Include) != 1 || fc.Include[0].Name != "Work" {
		t.Errorf("Include: want [{Work}], got %v", fc.Include)
	}
	if fc.Include[0].BadgePoll.D() != 15*time.Second {
		t.Errorf("per-folder BadgePoll: want 15s, got %v", fc.Include[0].BadgePoll.D())
	}
	if len(fc.Exclude) != 1 || fc.Exclude[0] != "Spam" {
		t.Errorf("Exclude: want [Spam], got %v", fc.Exclude)
	}
}
