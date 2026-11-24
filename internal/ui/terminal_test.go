package ui_test

import (
	"os/exec"
	"testing"

	"github.com/derailed/tcell/v2/terminfo"
	"github.com/zhecho/m9s/internal/ui"
)

// Inside tmux TERM is tmux-256color, which the tcell fork has no built-in
// entry for; its fallback ignored COLORTERM=truecolor, so themes were drawn
// with 256-colour approximations (Solarized turned brownish).
func TestPrepareTerminal_tmuxGetsTrueColor(t *testing.T) {
	if err := exec.Command("infocmp", "tmux-256color").Run(); err != nil {
		t.Skip("system terminfo has no tmux-256color entry")
	}
	t.Setenv("TERM", "tmux-256color")
	t.Setenv("COLORTERM", "truecolor")

	ui.PrepareTerminal()

	ti, err := terminfo.LookupTerminfo("tmux-256color")
	if err != nil {
		t.Fatalf("tmux-256color still unknown after PrepareTerminal: %v", err)
	}
	if ti.SetFgRGB == "" || ti.SetBgRGB == "" {
		t.Error("COLORTERM=truecolor must give tmux-256color 24-bit colour sequences")
	}
}

func TestPrepareTerminal_unknownOrEmptyTermIsHarmless(t *testing.T) {
	t.Setenv("TERM", "no-such-terminal-xyz")
	ui.PrepareTerminal()
	t.Setenv("TERM", "")
	ui.PrepareTerminal()
}
