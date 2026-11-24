package ui

import (
	"os"

	"github.com/derailed/tcell/v2/terminfo"
	"github.com/derailed/tcell/v2/terminfo/dynamic"
)

// PrepareTerminal makes sure tcell's terminal database knows $TERM before the
// screen is created. The tcell fork m9s uses has no built-in entry for some
// common names (notably tmux-256color, the TERM inside tmux); for those it
// falls back to the system terminfo but then ignores COLORTERM=truecolor, and
// every theme colour is rounded to the 256-colour palette. Registering the
// system entry up front sends it through the normal lookup, which honours
// COLORTERM. Errors are ignored: tcell's own fallback still applies.
func PrepareTerminal() {
	term := os.Getenv("TERM")
	if term == "" {
		return
	}
	if _, err := terminfo.LookupTerminfo(term); err == nil {
		return // built in: nothing to do
	}
	ti, _, err := dynamic.LoadTerminfo(term)
	if err != nil {
		return
	}
	ti.Name = term // look it up by the exact name in $TERM
	terminfo.AddTerminfo(ti)
}
