package view

import (
	"fmt"
	"sort"
	"strings"

	"github.com/zhecho/m9s/internal/mail"
)

// cmdKind says what a : command resolved to.
type cmdKind int

const (
	cmdNone cmdKind = iota // empty input: nothing to do
	cmdAccount
	cmdFolder
	cmdQuit
	cmdHelp
	cmdConfig
)

// cmdTarget is the result of resolving a : command.
type cmdTarget struct {
	Kind cmdKind
	Name string // account name, or the folder's full name
}

// maxAmbiguous is how many candidates an ambiguity error lists.
const maxAmbiguous = 5

func sortedAccounts(accounts []string) []string {
	out := append([]string(nil), accounts...)
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

// openableFolders returns the folders a command may open, sorted by full name.
func openableFolders(folders []*mail.Folder) []*mail.Folder {
	var out []*mail.Folder
	for _, f := range folders {
		if !f.IsNoSelect() {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

func ambiguous(text string, targets []cmdTarget) error {
	names := make([]string, 0, maxAmbiguous+1)
	for i, t := range targets {
		if i == maxAmbiguous {
			names = append(names, "…")
			break
		}
		names = append(names, t.Name)
	}
	return fmt.Errorf("%q is ambiguous: %s", text, strings.Join(names, ", "))
}

// resolveCommand maps what was typed at the : prompt to a target. folders are
// the active account's. Matching is case-insensitive; a folder matches by its
// full name or by its last path segment. Exact names win over prefixes, an
// account wins over a folder of the same name, and both win over q/quit,
// help and config.
func resolveCommand(text string, accounts []string, folders []*mail.Folder) (cmdTarget, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return cmdTarget{Kind: cmdNone}, nil
	}
	accounts = sortedAccounts(accounts)
	folders = openableFolders(folders)

	for _, a := range accounts {
		if strings.EqualFold(a, text) {
			return cmdTarget{Kind: cmdAccount, Name: a}, nil
		}
	}
	var byDisplay []cmdTarget
	for _, f := range folders {
		if strings.EqualFold(f.Name, text) {
			return cmdTarget{Kind: cmdFolder, Name: f.Name}, nil
		}
		if strings.EqualFold(f.DisplayName(), text) {
			byDisplay = append(byDisplay, cmdTarget{Kind: cmdFolder, Name: f.Name})
		}
	}
	switch {
	case len(byDisplay) == 1:
		return byDisplay[0], nil
	case len(byDisplay) > 1:
		return cmdTarget{}, ambiguous(text, byDisplay)
	}

	if strings.EqualFold(text, "q") || strings.EqualFold(text, "quit") {
		return cmdTarget{Kind: cmdQuit}, nil
	}
	if strings.EqualFold(text, helpCommand) {
		return cmdTarget{Kind: cmdHelp}, nil
	}
	if strings.EqualFold(text, configCommand) {
		return cmdTarget{Kind: cmdConfig}, nil
	}

	var matches []cmdTarget
	for _, a := range accounts {
		if hasPrefixFold(a, text) {
			matches = append(matches, cmdTarget{Kind: cmdAccount, Name: a})
		}
	}
	for _, f := range folders {
		if hasPrefixFold(f.Name, text) || hasPrefixFold(f.DisplayName(), text) {
			matches = append(matches, cmdTarget{Kind: cmdFolder, Name: f.Name})
		}
	}
	switch len(matches) {
	case 0:
		return cmdTarget{}, fmt.Errorf("no account or folder %q", text)
	case 1:
		return matches[0], nil
	default:
		return cmdTarget{}, ambiguous(text, matches)
	}
}

// helpCommand opens the list of shortcuts.
const helpCommand = "help"

// configCommand edits the config file and restarts m9s with it.
const configCommand = "config"

// suggestCommand returns the completion offered as ghost text for what has
// been typed so far, or "". Accounts are offered first, then folders by their
// short name, then by full name, then help and config. Quit is never
// suggested.
func suggestCommand(text string, accounts []string, folders []*mail.Folder) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	for _, a := range sortedAccounts(accounts) {
		if hasPrefixFold(a, text) {
			return a
		}
	}
	folders = openableFolders(folders)
	display := make([]string, 0, len(folders))
	for _, f := range folders {
		display = append(display, f.DisplayName())
	}
	sort.Slice(display, func(i, j int) bool { return strings.ToLower(display[i]) < strings.ToLower(display[j]) })
	for _, d := range display {
		if hasPrefixFold(d, text) {
			return d
		}
	}
	for _, f := range folders {
		if hasPrefixFold(f.Name, text) {
			return f.Name
		}
	}
	for _, c := range []string{helpCommand, configCommand} {
		if hasPrefixFold(c, text) {
			return c
		}
	}
	return ""
}
