package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/version"
)

func TestVersionFlagPrintsVersion(t *testing.T) {
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetArgs([]string{"--version"})
	t.Cleanup(func() { rootCmd.SetOut(nil); rootCmd.SetArgs(nil) })
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, version.String()) {
		t.Fatalf("--version printed %q, want it to contain %q", got, version.String())
	}
}
