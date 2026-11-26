package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/zhecho/m9s/internal/ui"
)

// cliRun runs m9s with args as a command would, logging to a state dir of
// its own, and returns the log file's text.
func cliRun(t *testing.T, args ...string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	saved := ui.AppLog
	ui.AppLog = &ui.Logger{}
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		ui.AppLog = saved
		rootCmd.SetOut(nil)
		rootCmd.SetArgs(nil)
		resetFlags(rootCmd)
	})
	err := rootCmd.Execute()
	finishCLILog(err)
	data, _ := os.ReadFile(filepath.Join(dir, "m9s", "m9s.log"))
	return string(data)
}

// resetFlags puts every flag back to its default, as a new process has it.
func resetFlags(c *cobra.Command) {
	reset := func(f *pflag.Flag) { _ = f.Value.Set(f.DefValue); f.Changed = false }
	c.Flags().VisitAll(reset)
	c.PersistentFlags().VisitAll(reset)
	for _, sub := range c.Commands() {
		resetFlags(sub)
	}
}

// A command is logged as typed, then how it ended.
func TestCLILog_commandAndOutcome(t *testing.T) {
	log := cliRun(t, "check", "-c", "/nonexistent.yaml", "-a", "work")
	if !strings.Contains(log, "INFO  [cli] m9s check --account=work --config=/nonexistent.yaml\n") {
		t.Errorf("the command line is not logged:\n%s", log)
	}
	if !strings.Contains(log, "ERROR [cli] m9s check: failed: config: reading config") {
		t.Errorf("the failure is not logged:\n%s", log)
	}
}

// What a message says stays out of the log.
func TestCLILog_sendBodyIsNotLogged(t *testing.T) {
	log := cliRun(t, "send", "--to", "a@example.com", "--subject", "hi", "--body", "secret text", "-c", "/nonexistent.yaml")
	if strings.Contains(log, "secret text") || !strings.Contains(log, "--body=…(11 chars)") {
		t.Errorf("log:\n%s", log)
	}
}

// --version and --help are not logged.
func TestCLILog_notForVersion(t *testing.T) {
	if log := cliRun(t, "--version"); log != "" {
		t.Errorf("--version was logged:\n%s", log)
	}
}

// What a command prints as its result goes to the log line by line; a ✗
// line is a warning.
func TestCLILog_outputLines(t *testing.T) {
	saved := ui.AppLog
	ui.AppLog = &ui.Logger{}
	t.Cleanup(func() { ui.AppLog = saved })
	w := &cliLineLog{}
	w.Write([]byte("[work] IMAP: ✓ connected\n[work] SMTP: ✗ auth failed\nhal"))
	w.Write([]byte("f\n"))
	w.Write([]byte("no end"))
	w.Flush()
	var got []string
	for _, e := range ui.AppLog.Entries() {
		got = append(got, e.Level.String()+" "+e.Msg)
	}
	want := "INFO [cli] [work] IMAP: ✓ connected|WARN [cli] [work] SMTP: ✗ auth failed|INFO [cli] half|INFO [cli] no end"
	if strings.Join(got, "|") != want {
		t.Errorf("entries:\n%s\nwant\n%s", strings.Join(got, "|"), want)
	}
}

// The open m9s shows a command's lines in its log as they come.
func TestFollowLog_showsCommandLines(t *testing.T) {
	dir := t.TempDir()
	saved := ui.AppLog
	ui.AppLog = &ui.Logger{}
	t.Cleanup(func() { ui.AppLog = saved })
	lf, err := startLogFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	stop := followLog(lf, 20*time.Millisecond)
	defer stop()
	cli, _ := ui.OpenLogFile(filepath.Join(dir, "m9s.log"), 0)
	cli.Write(ui.Entry{Time: time.Now(), Msg: "[cli] m9s login work"})
	cli.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, e := range ui.AppLog.Entries() {
			if e.Msg == "[cli] m9s login work" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the command's line never reached the open m9s's log")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
