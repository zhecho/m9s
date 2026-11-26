package cmd

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/zhecho/m9s/internal/instance"
	"github.com/zhecho/m9s/internal/ui"
)

// errChecksFailed ends m9s check when a login failed: the lines already
// said which, so Execute exits 1 without printing more.
var errChecksFailed = errors.New("some checks failed")

// cliLog is the log of the m9s command running (login, logout, check,
// send): it goes to the same file as the mail app's, without rotating it.
var cliLog struct {
	lf   *ui.LogFile
	cmd  *cobra.Command
	name string
	out  *cliLineLog
}

func init() { rootCmd.PersistentPreRun = startCLILog }

// startCLILog logs a command as it was typed (a message's text left out),
// and from then on what it prints as its result. The mail app itself,
// --version and --help are not commands to log.
func startCLILog(c *cobra.Command, args []string) {
	switch {
	case c == rootCmd, c.Name() == "help", c.Name() == "completion", strings.HasPrefix(c.Name(), "__"):
		return
	}
	lf, err := ui.OpenLogFile(filepath.Join(instance.StateDir(), logFileName), 0) // only the mail app rotates
	if err != nil {
		return // a command works without its log
	}
	out := &cliLineLog{}
	cliLog.lf, cliLog.cmd, cliLog.name, cliLog.out = lf, c, c.CommandPath(), out
	ui.AppLog.SetSink(lf.Write)
	ui.AppLog.Info("[cli] %s", commandLine(c, args))
	c.SetOut(io.MultiWriter(c.OutOrStdout(), out))
}

// finishCLILog logs how the command ended and closes its log.
func finishCLILog(err error) {
	if cliLog.lf == nil {
		return
	}
	cliLog.out.Flush()
	switch {
	case err == nil:
		ui.AppLog.Info("[cli] %s: done", cliLog.name)
	case errors.Is(err, errChecksFailed):
		ui.AppLog.Warn("[cli] %s: done, some checks failed", cliLog.name)
	default:
		ui.AppLog.Error("[cli] %s: failed: %v", cliLog.name, err)
	}
	ui.AppLog.SetSink(nil)
	cliLog.lf.Close()
	cliLog.cmd.SetOut(nil)
	cliLog.lf, cliLog.cmd, cliLog.out = nil, nil, nil
}

// commandLine is the command as typed: its path, its arguments and the
// flags given. A message's text (--body) is replaced by its length.
func commandLine(c *cobra.Command, args []string) string {
	parts := []string{c.CommandPath()}
	for _, a := range args {
		parts = append(parts, quoteArg(a))
	}
	c.Flags().Visit(func(f *pflag.Flag) {
		switch {
		case f.Name == "body":
			parts = append(parts, fmt.Sprintf("--body=…(%d chars)", utf8.RuneCountInString(f.Value.String())))
		case f.Value.Type() == "bool" && f.Value.String() == "true":
			parts = append(parts, "--"+f.Name)
		default:
			parts = append(parts, "--"+f.Name+"="+quoteArg(f.Value.String()))
		}
	})
	return strings.Join(parts, " ")
}

func quoteArg(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\"'") {
		return strconv.Quote(s)
	}
	return s
}

// cliLineLog logs what a command prints, line by line; a line with ✗ (a
// failed check) is a warning.
type cliLineLog struct{ partial string }

func (w *cliLineLog) Write(p []byte) (int, error) {
	text := w.partial + string(p)
	lines := strings.Split(text, "\n")
	w.partial = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		w.log(line)
	}
	return len(p), nil
}

// Flush logs a last line that did not end.
func (w *cliLineLog) Flush() {
	if w.partial != "" {
		w.log(w.partial)
		w.partial = ""
	}
}

func (w *cliLineLog) log(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	level := ui.LevelInfo
	if strings.Contains(line, "✗") {
		level = ui.LevelWarn
	}
	ui.AppLog.Add(level, "[cli] %s", line)
}

// followLog shows in the open m9s's log the lines m9s commands append to
// the file, every interval, until stop.
func followLog(lf *ui.LogFile, interval time.Duration) (stop func()) {
	log := ui.AppLog
	return lf.Follow(interval, log.AddEntries)
}
