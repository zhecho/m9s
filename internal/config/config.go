package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration with YAML support for strings like "60s", "10m".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Value == "0" || value.Value == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", value.Value, err)
	}
	*d = Duration(parsed)
	return nil
}

// D returns the underlying time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// FolderEntry configures one explicit folder with optional per-folder overrides.
type FolderEntry struct {
	Name          string   `yaml:"name"`
	BadgePoll     Duration `yaml:"badge_poll"`      // 0 = use account default
	BodyCachePoll Duration `yaml:"body_cache_poll"` // 0 = disabled for this folder
}

// FolderConfig is the per-account folder visibility and sync configuration.
type FolderConfig struct {
	AutoDetectRaw *bool         `yaml:"auto_detect"`
	BadgePoll     Duration      `yaml:"badge_poll"`
	BodyCachePoll Duration      `yaml:"body_cache_poll"`
	Include       []FolderEntry `yaml:"include"`
	Exclude       []string      `yaml:"exclude"`
	// Archive names the folder a archives to on an IMAP account; empty
	// detects it (\Archive, else a folder called Archive).
	Archive string `yaml:"archive"`
}

// ShouldAutoDetect returns true if auto-detection is enabled (default: true).
func (fc *FolderConfig) ShouldAutoDetect() bool {
	if fc.AutoDetectRaw == nil {
		return true
	}
	return *fc.AutoDetectRaw
}

type IMAPConfig struct {
	Host          string `yaml:"host"`
	Port          int    `yaml:"port"`
	TLS           bool   `yaml:"tls"`
	TLSSkipVerify bool   `yaml:"tls_skip_verify"`
	User          string `yaml:"user"`
	Password      string `yaml:"password"`
	PasswordCmd   string `yaml:"password_cmd"`
}

type SMTPConfig struct {
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	TLS         bool   `yaml:"tls"`
	User        string `yaml:"user"`
	Password    string `yaml:"password"`
	PasswordCmd string `yaml:"password_cmd"`
}

// Mail providers an account can use.
const (
	ProviderIMAP      = "imap"
	ProviderGmail     = "gmail"
	ProviderMicrosoft = "microsoft"
)

// GmailConfig is the Gmail API account block.
type GmailConfig struct {
	User     string `yaml:"user"`
	ClientID string `yaml:"client_id"`
}

// MicrosoftConfig is the Microsoft 365 (Graph API) account block. ClientID
// is the owner's own app registration; it is not a secret.
type MicrosoftConfig struct {
	User     string `yaml:"user"`
	ClientID string `yaml:"client_id"`
}

type Account struct {
	Name string `yaml:"name"`
	// Enable false leaves the account out, as if it were not in the
	// config; omitted means enabled.
	Enable *bool `yaml:"enable"`
	// Provider is "imap" (default), "gmail" or "microsoft".
	Provider  string          `yaml:"provider"`
	Gmail     GmailConfig     `yaml:"gmail"`
	Microsoft MicrosoftConfig `yaml:"microsoft"`
	// KeePassXC takes this account's passwords from the running KeePassXC
	// app: the entry whose URL is the service's host and whose user name is
	// the service's user.
	KeePassXC bool         `yaml:"keepassxc"`
	IMAP      IMAPConfig   `yaml:"imap"`
	SMTP      SMTPConfig   `yaml:"smtp"`
	Folders   FolderConfig `yaml:"folders"`
}

// Enabled reports whether the account is loaded: enable is omitted or true.
func (a *Account) Enabled() bool { return a.Enable == nil || *a.Enable }

// ProviderName returns the account's provider; empty means imap.
func (a *Account) ProviderName() string {
	if a.Provider == "" {
		return ProviderIMAP
	}
	return a.Provider
}

// IsGmail reports whether the account uses the Gmail API.
func (a *Account) IsGmail() bool { return a.ProviderName() == ProviderGmail }

// IsMicrosoft reports whether the account uses Microsoft Graph.
func (a *Account) IsMicrosoft() bool { return a.ProviderName() == ProviderMicrosoft }

type UIConfig struct {
	Theme        string            `yaml:"theme"`
	Colors       map[string]string `yaml:"colors"` // per-role overrides on top of Theme
	Skin         string            `yaml:"skin"`   // deprecated alias for Theme
	Editor       string            `yaml:"editor"`
	DateFormat   string            `yaml:"date_format"`
	PreviewRatio int               `yaml:"preview_ratio"` // deprecated: replaced by mail_list_rows, ignored
	// MailListRows is how many messages the mail list shows by default; the
	// preview pane gets the rest. Dragging the border overrides it, and the
	// dragged size is remembered (see LayoutState).
	MailListRows int `yaml:"mail_list_rows"`
	// AttachmentDir is where s saves attachments without asking; "" asks.
	AttachmentDir string `yaml:"attachment_dir"`
}

// ThemeName returns the configured theme, honouring the legacy skin: key.
func (u UIConfig) ThemeName() string {
	if u.Theme != "" {
		return u.Theme
	}
	return u.Skin
}

type SearchConfig struct {
	LocalIndex   string `yaml:"local_index"`
	ServerSearch bool   `yaml:"server_search"`
}

type Config struct {
	Accounts []Account `yaml:"accounts"` // the enabled accounts only
	// Disabled names the accounts with enable: false, in config order; they
	// are not in Accounts and their settings are not checked.
	Disabled []string     `yaml:"-"`
	UI       UIConfig     `yaml:"ui"`
	Search   SearchConfig `yaml:"search"`
}

func applyDefaults(cfg *Config) {
	if cfg.UI.MailListRows <= 0 {
		cfg.UI.MailListRows = 10
	}
	if cfg.UI.DateFormat == "" {
		cfg.UI.DateFormat = "Jan 02 15:04"
	}
	if cfg.Search.LocalIndex == "" {
		home, _ := os.UserHomeDir()
		cfg.Search.LocalIndex = home + "/.cache/m9s/index"
	}
	for i := range cfg.Accounts {
		fc := &cfg.Accounts[i].Folders
		if fc.BadgePoll == 0 {
			fc.BadgePoll = Duration(60 * time.Second)
			if cfg.Accounts[i].IsGmail() || cfg.Accounts[i].IsMicrosoft() {
				fc.BadgePoll = Duration(30 * time.Second)
			}
		}
		if fc.BodyCachePoll == 0 {
			fc.BodyCachePoll = Duration(10 * time.Minute)
		}
	}
}

// LoadFromBytes parses config from raw YAML bytes. Used in tests.
func LoadFromBytes(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	enabled := cfg.Accounts[:0]
	for _, a := range cfg.Accounts {
		if a.Enabled() {
			enabled = append(enabled, a)
		} else {
			cfg.Disabled = append(cfg.Disabled, a.Name)
		}
	}
	cfg.Accounts = enabled
	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// validate rejects settings that cannot work.
func validate(cfg *Config) error {
	for _, a := range cfg.Accounts {
		switch a.ProviderName() {
		case ProviderIMAP:
		case ProviderGmail:
			if !a.KeePassXC {
				return fmt.Errorf("account %q: gmail accounts need keepassxc: true", a.Name)
			}
			if strings.TrimSpace(a.Gmail.User) == "" {
				return fmt.Errorf("account %q: provider: gmail needs gmail.user", a.Name)
			}
			if strings.TrimSpace(a.Gmail.ClientID) == "" {
				return fmt.Errorf("account %q: provider: gmail needs gmail.client_id", a.Name)
			}
			continue
		case ProviderMicrosoft:
			// KeePassXC is the only home of the refresh token.
			if !a.KeePassXC {
				return fmt.Errorf("account %q: microsoft accounts need keepassxc: true", a.Name)
			}
			if strings.TrimSpace(a.Microsoft.User) == "" {
				return fmt.Errorf("account %q: provider: microsoft needs microsoft.user", a.Name)
			}
			if strings.TrimSpace(a.Microsoft.ClientID) == "" {
				return fmt.Errorf("account %q: provider: microsoft needs microsoft.client_id", a.Name)
			}
			continue
		default:
			return fmt.Errorf("account %q: unknown provider %q", a.Name, a.Provider)
		}
		if !a.KeePassXC {
			continue
		}
		// KeePassXC entries are told apart by user name.
		if strings.TrimSpace(a.IMAP.User) == "" {
			return fmt.Errorf("account %q: keepassxc: true needs imap.user", a.Name)
		}
		if strings.TrimSpace(a.SMTP.User) == "" {
			return fmt.Errorf("account %q: keepassxc: true needs smtp.user", a.Name)
		}
	}
	return nil
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	return LoadFromBytes(data)
}

// EnvKey is the account name as it appears in environment variable names:
// upper case, everything but letters and digits replaced by "_".
func EnvKey(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// RunPasswordCmd runs a password command in the shell and returns its
// output without the trailing newline. It is stopped when ctx ends. The
// error says only how the command ended: neither its text nor anything it
// printed, since both may be secret.
func RunPasswordCmd(ctx context.Context, cmd string) (string, error) {
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.WaitDelay = time.Second // do not wait on children that keep the output open
	out, err := c.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", errors.New("timed out")
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("exit status %d", exit.ExitCode())
		}
		return "", errors.New("could not be run")
	}
	return strings.TrimRight(string(out), "\n\r"), nil
}
