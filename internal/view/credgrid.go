package view

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zhecho/m9s/internal/creds"
)

// CredKind is how the password lookup at startup ended.
type CredKind int

const (
	CredAllGood    CredKind = iota // every account has its passwords, nothing failed on the way
	CredFellBack                   // every account works, but a configured source failed
	CredSomeFailed                 // some accounts have no password; the rest work
	CredNoneWork                   // no account can be used
)

const (
	credPauseContinue = 5 * time.Second // before going on after a failure
	credPauseQuit     = 8 * time.Second // before closing when nothing works
)

// CredOutcome is what the startup window does once the lookup is over.
type CredOutcome struct {
	Kind    CredKind
	Pause   time.Duration // how long the window stays for reading; 0: not at all
	Working []string      // the accounts m9s goes on with, in config order
}

// CredGrid is the content of the startup window: one row per account, one
// column per password source in the order they are tried, filled in as the
// lookup goes. It is safe for the lookup's goroutine and the screen's.
type CredGrid struct {
	mu       sync.Mutex
	accounts []string
	cells    map[string]map[creds.Source]map[string]creds.State // account → source → service → state
	services map[string][2]string                               // account → its two services; default imap, smtp
	notice   string
	summary  []string         // what went wrong, once the lookup is over
	closing  func(int) string // the last line, for the seconds left
	left     int
}

// NewCredGrid makes the grid for accounts, in the order given.
func NewCredGrid(accounts []string) *CredGrid {
	g := &CredGrid{accounts: accounts, cells: map[string]map[creds.Source]map[string]creds.State{},
		services: map[string][2]string{}}
	for _, a := range accounts {
		g.cells[a] = map[creds.Source]map[string]creds.State{}
		for _, s := range creds.Order {
			g.cells[a][s] = map[string]creds.State{}
		}
	}
	return g
}

// SetServices names the services shown for account, as the lookup reports
// them (creds.ServicesOf); without it they are imap and smtp. An account
// with one service (microsoft: its token) gets one answer per cell.
func (g *CredGrid) SetServices(account string, services []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case len(services) == 1 && services[0] != "":
		// The same service twice: the two halves of a cell always agree.
		g.services[account] = [2]string{services[0], services[0]}
	case len(services) == 2 && services[0] != "" && services[1] != "":
		g.services[account] = [2]string{services[0], services[1]}
	}
}

// servicesOf is account's pair of services. The caller holds g.mu.
func (g *CredGrid) servicesOf(account string) [2]string {
	if s, ok := g.services[account]; ok {
		return s
	}
	return [2]string{"imap", "smtp"}
}

// Apply records one step of the lookup.
func (g *CredGrid) Apply(u creds.Update) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if row, ok := g.cells[u.Account]; ok {
		if cell, ok := row[u.Source]; ok {
			cell[u.Service] = u.State
		}
	}
}

// SetNotice sets what m9s is waiting for ("" for nothing).
func (g *CredGrid) SetNotice(text string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.notice = text
}

// Tick sets the seconds left on the closing line.
func (g *CredGrid) Tick(secondsLeft int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.left = secondsLeft
}

// Finish records how the lookup ended and says what the window does next.
// waited is not part of the decision: a wait already kept the window up.
func (g *CredGrid) Finish(results []creds.Result, waited bool) CredOutcome {
	g.mu.Lock()
	defer g.mu.Unlock()

	var out CredOutcome
	var failed []creds.Result
	var fellBack []creds.Result
	for _, r := range results {
		switch {
		case !r.Works():
			failed = append(failed, r)
		default:
			out.Working = append(out.Working, r.Account)
			if len(r.Failures) > 0 {
				fellBack = append(fellBack, r)
			}
		}
	}

	g.summary = nil
	switch {
	case len(out.Working) == 0:
		out.Kind, out.Pause = CredNoneWork, credPauseQuit
		g.summary = append(g.summary, "No account has a password.")
		g.summary = append(g.summary, reasonLines(failed)...)
		g.closing = func(s int) string { return fmt.Sprintf("m9s will close in %d s (press any key to close now)", s) }
	case len(failed) > 0:
		out.Kind, out.Pause = CredSomeFailed, credPauseContinue
		names := make([]string, len(failed))
		for i, r := range failed {
			names[i] = r.Account
		}
		g.summary = append(g.summary, "No password for: "+strings.Join(names, ", "))
		g.summary = append(g.summary, reasonLines(failed)...)
		if len(fellBack) > 0 {
			g.summary = append(g.summary, "Some sources failed:")
			g.summary = append(g.summary, reasonLines(fellBack)...)
		}
		g.closing = func(s int) string { return fmt.Sprintf("Continuing without them in %d s (press any key)", s) }
	case len(fellBack) > 0:
		out.Kind, out.Pause = CredFellBack, credPauseContinue
		g.summary = append(g.summary, "Some sources failed:")
		g.summary = append(g.summary, reasonLines(fellBack)...)
		g.closing = func(s int) string { return fmt.Sprintf("Continuing in %d s (press any key)", s) }
	default:
		out.Kind = CredAllGood
		g.closing = nil
	}
	g.left = int(out.Pause / time.Second)
	g.notice = ""
	return out
}

// reasonLines lists, per account, each distinct reason once.
func reasonLines(results []creds.Result) []string {
	width := 0
	for _, r := range results {
		width = max(width, utf8.RuneCountInString(r.Account))
	}
	var lines []string
	for _, r := range results {
		seen := map[string]bool{}
		for _, f := range r.Failures {
			if !seen[f.Reason] {
				seen[f.Reason] = true
				lines = append(lines, fmt.Sprintf("  %-*s  %s", width, r.Account, f.Reason))
			}
		}
		if len(seen) == 0 {
			lines = append(lines, fmt.Sprintf("  %-*s  no password in any source", width, r.Account))
		}
	}
	return lines
}

var (
	credHeadersWide   = []string{"1 env", "2 keepassxc", "3 password_cmd", "4 plain text", "5 keychain"}
	credHeadersNarrow = []string{"1 env", "2 kpxc", "3 cmd", "4 plain", "5 keych"}
)

const credLegend = "– not set    · not needed, already found    ✔ used    ✗ failed"

// Lines returns the window's content for a terminal width columns wide, as
// many rows as it takes. No line is wider than width.
func (g *CredGrid) Lines(width int) []string { return g.LinesIn(width, 0) }

// LinesIn is Lines for a box at most height rows tall (0: no limit). When
// rows are short, what the user must act on — the notice, the reasons, the
// countdown — is kept; the intro, legend, spacing and then account rows go
// first.
func (g *CredGrid) LinesIn(width, height int) []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	nameW := len("account")
	for _, a := range g.accounts {
		nameW = max(nameW, utf8.RuneCountInString(a))
	}

	// Wide: full words. If that does not fit, short headers and short cells.
	headers, colW, narrow := credHeadersWide, make([]int, len(creds.Order)), false
	wideName := min(nameW, 22)
	total := wideName + 2
	cellW := 0 // the widest cell: "✔ imap ✗ smtp"
	for _, a := range g.accounts {
		svc := g.servicesOf(a)
		cellW = max(cellW, utf8.RuneCountInString("✔ "+svc[0]+" ✗ "+svc[1]))
	}
	for i, h := range headers {
		colW[i] = max(utf8.RuneCountInString(h), cellW) + 2
		total += colW[i]
	}
	if total > width {
		headers, narrow = credHeadersNarrow, true
		total = 0
		for i, h := range headers {
			colW[i] = utf8.RuneCountInString(h) + 1
			total += colW[i]
		}
		nameW = max(min(nameW, width-total-1), 4)
	} else {
		nameW = wideName
	}

	row := func(name string, cells []string) string {
		var b strings.Builder
		b.WriteString(pad(name, nameW))
		b.WriteString(map[bool]string{true: " ", false: "  "}[narrow])
		for i, c := range cells {
			b.WriteString(pad(c, colW[i]-1))
			b.WriteString(" ")
		}
		return strings.TrimRight(b.String(), " ")
	}

	intro := []string{"Tried in this order, first one found wins:", ""}
	header := row("account", headers)
	accounts := make([]string, 0, len(g.accounts))
	for _, a := range g.accounts {
		cells := make([]string, len(creds.Order))
		for i, s := range creds.Order {
			cells[i] = credCell(g.cells[a][s], g.servicesOf(a), narrow)
		}
		accounts = append(accounts, row(a, cells))
	}
	var notice []string
	if g.notice != "" {
		notice = append(wrap(g.notice, width), "esc  skip KeePassXC")
	}
	var summary []string
	for _, l := range g.summary {
		summary = append(summary, wrap(l, width)...)
	}
	var closing []string
	if g.closing != nil {
		closing = wrap(g.closing(g.left), width)
	}
	legend := []string{credLegend}

	// Assemble, dropping the least needed parts until it fits.
	gaps := true
	build := func() []string {
		var out []string
		out = append(out, intro...)
		out = append(out, header)
		out = append(out, accounts...)
		for _, block := range [][]string{notice, summary, closing, legend} {
			if len(block) == 0 {
				continue
			}
			if gaps {
				out = append(out, "")
			}
			out = append(out, block...)
		}
		return out
	}
	lines := build()
	if height > 0 {
		for _, drop := range []func(){
			func() { intro = nil },
			func() { legend = nil },
			func() { gaps = false },
		} {
			if len(lines) <= height {
				break
			}
			drop()
			lines = build()
		}
		if over := len(lines) - height; over > 0 && len(accounts) > 0 {
			// Keep as many account rows as fit, and say how many are hidden.
			keep := max(len(accounts)-over-1, 0)
			hidden := len(accounts) - keep
			accounts = append(accounts[:keep:keep], fmt.Sprintf("+%d more", hidden))
			lines = build()
		}
		if over := len(lines) - height; over > 0 && len(summary) > 1 {
			// Then the reasons, keeping their first line.
			summary = summary[:max(len(summary)-over, 1)]
			lines = build()
		}
		if len(lines) > height {
			lines = lines[len(lines)-height:] // the end matters most
		}
	}

	for i, l := range lines {
		lines[i] = cut(l, width)
	}
	return lines
}

// wrap breaks s into lines of at most width columns at spaces. In a
// reason line ("  name  reason") the continuation lines line up under the
// reason; otherwise under the line's own leading spaces.
func wrap(s string, width int) []string {
	if width <= 0 || utf8.RuneCountInString(s) <= width {
		return []string{s}
	}
	// keep is the part of the first line kept as it is: leading spaces, and
	// for a reason line the account name and the gap after it.
	keep := len(s) - len(strings.TrimLeft(s, " "))
	if gap := strings.Index(s[keep:], "  "); gap > 0 {
		rest := s[keep+gap:]
		keep += gap + len(rest) - len(strings.TrimLeft(rest, " "))
	}
	indent := utf8.RuneCountInString(s[:keep])
	if indent > width/2 {
		indent, keep = 0, 0
	}

	out := []string{}
	line, lineW, empty := s[:keep], indent, true
	for _, word := range strings.Fields(s[keep:]) {
		w := utf8.RuneCountInString(word)
		if !empty && lineW+1+w > width {
			out = append(out, line)
			line, lineW, empty = strings.Repeat(" ", indent), indent, true
		}
		if empty {
			line, lineW, empty = line+word, lineW+w, false
		} else {
			line, lineW = line+" "+word, lineW+1+w
		}
	}
	return append(out, line)
}

// credCell words one account's standing with one source, from its two
// services (imap and smtp, or for gmail client and token); narrow names a
// service by its first letter.
func credCell(states map[string]creds.State, services [2]string, narrow bool) string {
	imap, smtp := states[services[0]], states[services[1]]
	word := func(wide, short string) string {
		if narrow {
			return short
		}
		return wide
	}
	switch {
	case imap == creds.Asking || smtp == creds.Asking:
		return word("… asking", "…")
	case imap == smtp:
		switch imap {
		case creds.Used:
			return word("✔ used", "✔")
		case creds.Failed:
			return word("✗ failed", "✗")
		case creds.Pending:
			return word("waiting", "wait")
		case creds.NotNeeded:
			return "·"
		}
		return "–"
	}
	// The two services differ: say what happened to each that got an answer.
	var parts []string
	for _, svc := range []struct {
		name, short string
		state       creds.State
	}{{services[0], services[0][:1], imap}, {services[1], services[1][:1], smtp}} {
		switch svc.state {
		case creds.Used:
			parts = append(parts, "✔"+word(" "+svc.name, svc.short))
		case creds.Failed:
			parts = append(parts, "✗"+word(" "+svc.name, svc.short))
		}
	}
	switch {
	case len(parts) > 0:
		return strings.Join(parts, " ")
	case imap == creds.Pending || smtp == creds.Pending:
		return word("waiting", "wait")
	case imap == creds.NotNeeded || smtp == creds.NotNeeded:
		return "·"
	}
	return "–"
}

// pad cuts or fills s to exactly width columns.
func pad(s string, width int) string {
	s = cut(s, width)
	return s + strings.Repeat(" ", max(width-utf8.RuneCountInString(s), 0))
}

// cut shortens s to width columns, ending in … when something was lost.
func cut(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= width {
		return s
	}
	r := []rune(s)
	return string(r[:width-1]) + "…"
}
