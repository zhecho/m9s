package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/draft"
	"github.com/zhecho/m9s/internal/instance"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/store"
	msync "github.com/zhecho/m9s/internal/sync"
	"github.com/zhecho/m9s/internal/ui"
	"github.com/zhecho/m9s/internal/version"
	"github.com/zhecho/m9s/internal/view"
)

var cfgPath string

// readme is the README text :help shows below the shortcuts.
var readme string

// SetReadme hands over the README compiled into the binary.
func SetReadme(text string) { readme = text }

var rootCmd = &cobra.Command{
	Use:     "m9s",
	Short:   "Terminal-based email client for IMAP/SMTP, Gmail and Microsoft 365 / Outlook.com",
	Version: version.String(),
	RunE:    run,
	// Execute prints a failure once, without the usage text: after the
	// startup window closes, its reasons are what matters (m9s --help has usage).
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	home, _ := os.UserHomeDir()
	defaultCfg := filepath.Join(home, ".config", "m9s", "config.yaml")
	rootCmd.PersistentFlags().StringVarP(&cfgPath, "config", "c", defaultCfg, "config file path")
}

// errRestart ends run when :config saved a new config: Execute starts m9s
// again, after run has closed the store and the screen.
var errRestart = errors.New("restart")

func Execute() {
	err := rootCmd.Execute()
	finishCLILog(err)
	if errors.Is(err, errRestart) {
		err = restartSelf()
	}
	if errors.Is(err, errChecksFailed) {
		os.Exit(1) // its lines said what failed
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(cmd *cobra.Command, args []string) error {
	// One m9s at a time, before anything else (the passwords window).
	release, ok := lockInstance(instance.StateDir(), os.Stderr, time.Sleep)
	if !ok {
		os.Exit(1)
	}
	defer release() // before a :config restart, which takes it anew
	if lf, err := startLogFile(instance.StateDir()); err != nil {
		ui.AppLog.Warn("log not kept on disk: %v", err)
	} else {
		defer lf.Close()
		defer followLog(lf, time.Second)() // m9s commands' lines, as they come
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	// Load theme: built-in or ~/.config/m9s/themes/<name>.yaml, plus ui.colors overrides
	home, _ := os.UserHomeDir()
	themesDir := filepath.Join(home, ".config", "m9s", "themes")
	skin, themeWarns := ui.ResolveTheme(cfg.UI.ThemeName(), themesDir, cfg.UI.Colors)
	// The page b opens: its Light and Dark modes (ui.browser), from the same themes.
	browserThemes, browserWarns := ui.ResolveBrowserThemes(skin, cfg.UI.Browser.LightTheme, cfg.UI.Browser.DarkTheme, themesDir)
	themeWarns = append(themeWarns, browserWarns...)

	// Load keybindings
	kbPath := filepath.Join(home, ".config", "m9s", "keybindings.yaml")
	km, _ := ui.LoadKeyMap(kbPath)

	// Find every account's passwords, once, in the startup window. Only the
	// accounts that got both are started.
	ui.PrepareTerminal() // before any screen exists: true colour inside tmux
	look := newLookup()
	allAccounts := make([]string, len(cfg.Accounts))
	for i, a := range cfg.Accounts {
		allAccounts[i] = a.Name
	}
	working, found, err := resolveAccounts(cmd.Context(), cfg, skin,
		startDeps{run: look.run, skip: look.skip, show: view.ShowCredentials})
	look.close()
	if err != nil {
		return err
	}
	cfg.Accounts = working

	// Build accounts, sync engines (which also send), and draft store
	accounts := make([]*mail.Account, 0, len(cfg.Accounts))
	engines := make(map[string]msync.Account)

	events := make(chan msync.Event, 32)
	draftStore := draft.NewStore(filepath.Join(home, ".local", "share", "m9s", "drafts"))

	// The local copy of all mail: the screen is filled from it before any
	// server has answered, and each account is copied into it in the
	// background. Without it m9s works as before, from the server only.
	mailStore, storeErr := store.Open(store.DefaultDir())
	if storeErr == nil {
		defer mailStore.Close()
	}

	// Each microsoft account's newest refresh token goes to KeePassXC.
	var savers []*tokenSaver
	builder := &accountBuilder{ctx: cmd.Context(), events: events, store: mailStore, drafts: draftStore,
		gmail: defaultGmail, reread: readGmailSignIn,
		microsoft: defaultMicrosoft, rereadMicrosoft: readMicrosoftSignIn,
		saverFor: func(acct *config.Account) func(string) {
			s := newTokenSaver(acct, mailStore)
			savers = append(savers, s)
			return s.Rotated
		}}
	var buildWarns []string
	// Full capacity: the engines keep pointers into built.
	built := make([]config.Account, 0, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		built = append(built, a)
		acctCfg := &built[len(built)-1]
		eng, err := builder.buildAccount(acctCfg, found[acctCfg.Name])
		if err != nil {
			buildWarns = append(buildWarns, fmt.Sprintf("[%s] left out: %v", acctCfg.Name, err))
			built = built[:len(built)-1]
			continue
		}
		engines[acctCfg.Name] = eng
	}
	if len(built) == 0 {
		return fmt.Errorf("no account could be started: %s", strings.Join(buildWarns, "; "))
	}
	cfg.Accounts = built
	for i := range cfg.Accounts {
		acctCfg := &cfg.Accounts[i]
		accounts = append(accounts, &mail.Account{Name: acctCfg.Name, Config: acctCfg})
		engines[acctCfg.Name].Start(cmd.Context())
	}
	manager := mail.NewManager(accounts)

	uiApp := ui.NewApp(skin, km)
	ui.AppLog.Info("m9s started (press l to toggle this log, q to close)")
	for _, e := range append(disabledLogLines(cfg), passwordLogLines(allAccounts, found)...) {
		ui.AppLog.Add(e.Level, "%s", e.Msg)
	}
	if storeErr != nil {
		ui.AppLog.Warn("mail is not saved locally: %v", storeErr)
	}
	startupWarnings(buildWarns, themeWarns, uiApp.StatusBar.Error,
		func(line string) { ui.AppLog.Warn("%s", line) })
	for _, s := range savers {
		s.warn = func(text string) { // the status bar logs it too
			uiApp.QueueUpdateDraw(func() { uiApp.StatusBar.Error(text) })
		}
		go s.run(cmd.Context())
	}
	rootView := view.NewRootApp(uiApp, cfg, manager, engines, draftStore, events)
	rootView.SetBrowserThemes(browserThemes)
	rootView.SetLayoutStatePath(config.DefaultLayoutStatePath())
	rootView.SetConfigPath(cfgPath)
	rootView.SetReadme(readme)
	if mailStore != nil {
		rootView.SetStore(mailStore) // start from what is already on disk
		go fillAttachSigns(mailStore)
	}
	uiApp.EnableMouse(true) // drag the border between mail list and preview
	if cfg.UI.PreviewRatio != 0 {
		ui.AppLog.Warn("ui.preview_ratio is no longer used; set ui.mail_list_rows (default 10) instead")
	}

	defer rootView.Stop()
	if err := uiApp.Run(); err != nil {
		return err
	}
	if rootView.RestartRequested() {
		return errRestart
	}
	return nil
}

// fillAttachSigns gives mail stored by an older m9s its attachment sign, from
// the bodies already on disk, a batch at a time.
func fillAttachSigns(st *store.Store) {
	for {
		n, err := st.FillAttachSigns(200)
		if err != nil {
			ui.AppLog.Warn("attachment signs of stored mail: %v", err)
			return
		}
		if n == 0 {
			return
		}
	}
}
