//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// TestMain runs prerequisite checks before any e2e test executes.
func TestMain(m *testing.M) {
	if err := exec.Command("tmux", "-V").Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tmux not found in PATH — install tmux to run e2e tests")
		os.Exit(1)
	}
	if err := exec.Command("which", "m9s").Run(); err != nil {
		fmt.Fprintln(os.Stderr, "m9s not found in PATH — run 'go install .' first")
		os.Exit(1)
	}
	os.Exit(m.Run())
}
