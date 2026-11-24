package main

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/mattn/go-runewidth"
)

// The README is shown in :help as well as on the forge, so it keeps to
// standard Markdown: markdownlint's default rules, except line length
// (MD013: tables and code would break it) and duplicate headings that are
// not siblings (MD024 siblings_only: "Config" under Gmail and Microsoft).

var (
	rmHeading   = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	rmFence     = regexp.MustCompile("^\\s*(```|~~~)(.*)$")
	rmBullet    = regexp.MustCompile(`^(\s*)([-*+])\s+`)
	rmOrdered   = regexp.MustCompile(`^(\s*)(\d+)[.)]\s+`)
	rmTableSep  = regexp.MustCompile(`^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?\s*$`)
	rmCodeSpan  = regexp.MustCompile("`+[^`]*`+")
	rmLink      = regexp.MustCompile(`!?\[[^\]]*\]\([^)]*\)`)
	rmAutolink  = regexp.MustCompile(`<https?://[^>]+>`)
	rmBareURL   = regexp.MustCompile(`https?://[^\s)>\]]+`)
	rmFragment  = regexp.MustCompile(`\]\(#([^)]+)\)`)
	rmHTMLish   = regexp.MustCompile(`<[A-Za-z/!][^>]*>`)
	rmEmphLine  = regexp.MustCompile(`^\s*(\*\*[^*]+\*\*|\*[^*]+\*|__[^_]+__|_[^_]+_)\s*$`)
	rmTableCell = regexp.MustCompile(`(^|[^\\])\|`)
)

// slug is GitHub's heading anchor: lower case, punctuation dropped, spaces
// to "-".
func slug(heading string) string {
	heading = rmCodeSpan.ReplaceAllStringFunc(heading, func(s string) string { return strings.Trim(s, "`") })
	heading = rmLink.ReplaceAllStringFunc(heading, func(s string) string {
		return s[strings.Index(s, "[")+1 : strings.Index(s, "]")]
	})
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}

func tableCells(line string) int {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "|")
	if strings.HasSuffix(s, "|") && !strings.HasSuffix(s, `\|`) {
		s = s[:len(s)-1]
	}
	s = rmCodeSpan.ReplaceAllString(s, "x")
	return len(rmTableCell.FindAllStringIndex(s, -1)) + 1
}

// lintMarkdown returns one line per rule broken, "line N: MDxxx …".
func lintMarkdown(md string) []string {
	var issues []string
	bad := func(n int, rule, format string, args ...any) {
		issues = append(issues, fmt.Sprintf("line %d: %s %s", n, rule, fmt.Sprintf(format, args...)))
	}
	if !strings.HasSuffix(md, "\n") || strings.HasSuffix(md, "\n\n") {
		bad(0, "MD047", "file must end with exactly one newline")
	}
	lines := strings.Split(strings.TrimSuffix(md, "\n"), "\n")
	blank := func(i int) bool { return i < 0 || i >= len(lines) || strings.TrimSpace(lines[i]) == "" }

	type heading struct {
		level  int
		text   string
		parent string
	}
	var headings []heading
	slugs := map[string]bool{}
	parents := make([]string, 7) // nearest heading text per level
	inFence, fenceStart := false, 0
	var fenceLines []string
	lastLevel, h1s := 0, 0
	inList, inTable := false, false
	for i, line := range lines {
		n := i + 1
		if strings.Contains(line, "\t") {
			bad(n, "MD010", "hard tab")
		}
		if strings.TrimRight(line, " ") != line && !(strings.HasSuffix(line, "  ") && !strings.HasSuffix(line, "   ") && !inFence && !blank(i+1)) {
			bad(n, "MD009", "trailing spaces")
		}
		if i > 0 && blank(i) && blank(i-1) && !inFence {
			bad(n, "MD012", "more than one blank line")
		}
		if m := rmFence.FindStringSubmatch(line); m != nil {
			if !inFence {
				inFence, fenceStart = true, n
				if strings.TrimSpace(m[2]) == "" {
					bad(n, "MD040", "fenced code without a language")
				}
				if !blank(i - 1) {
					bad(n, "MD031", "no blank line before a fence")
				}
			} else {
				inFence = false
				if !blank(i + 1) {
					bad(n, "MD031", "no blank line after a fence")
				}
				dollars := len(fenceLines) > 0
				for _, l := range fenceLines {
					dollars = dollars && strings.HasPrefix(strings.TrimSpace(l), "$ ")
				}
				if dollars {
					bad(fenceStart, "MD014", "every line starts with $ and no output is shown")
				}
				fenceLines = nil
			}
			continue
		}
		if inFence {
			if strings.TrimSpace(line) != "" {
				fenceLines = append(fenceLines, line)
			}
			continue
		}
		if m := rmHeading.FindStringSubmatch(line); m != nil && !strings.HasPrefix(line, " ") {
			level, text := len(m[1]), m[2]
			if level == 1 {
				h1s++
				if i != 0 {
					bad(n, "MD041/MD025", "the only top-level heading must be the first line")
				}
			}
			if lastLevel > 0 && level > lastLevel+1 {
				bad(n, "MD001", "heading jumps from level %d to %d", lastLevel, level)
			}
			lastLevel = level
			if (i > 0 && !blank(i-1)) || !blank(i+1) {
				bad(n, "MD022", "heading needs a blank line before and after")
			}
			if strings.ContainsAny(text[len(text)-1:], ".,;:!") {
				bad(n, "MD026", "heading ends in punctuation")
			}
			parent := strings.Join(parents[1:level], "/")
			for _, h := range headings {
				if h.level == level && h.text == text && h.parent == parent {
					bad(n, "MD024", "duplicate sibling heading %q", text)
				}
			}
			headings = append(headings, heading{level, text, parent})
			parents[level] = text
			for l := level + 1; l < len(parents); l++ {
				parents[l] = ""
			}
			s := slug(text)
			for k := 1; slugs[s]; k++ { // GitHub numbers repeats
				s = fmt.Sprintf("%s-%d", slug(text), k)
			}
			slugs[s] = true
			inList, inTable = false, false
			continue
		}
		isItem := rmBullet.MatchString(line) || rmOrdered.MatchString(line)
		if m := rmBullet.FindStringSubmatch(line); m != nil && m[2] != "-" {
			bad(n, "MD004", "list marker %q, use -", m[2])
		}
		if isItem && !inList && !blank(i-1) {
			bad(n, "MD032", "no blank line before a list")
		}
		if isItem {
			inList = true
		} else if blank(i) {
			if inList && !blank(i+1) && !strings.HasPrefix(lines[i+1], " ") && !rmBullet.MatchString(lines[i+1]) && !rmOrdered.MatchString(lines[i+1]) {
				inList = false
			}
		}
		isRow := strings.HasPrefix(strings.TrimSpace(line), "|")
		if isRow && !inTable {
			inTable = true
			if !blank(i - 1) {
				bad(n, "MD058", "no blank line before a table")
			}
			if i+1 >= len(lines) || !rmTableSep.MatchString(lines[i+1]) {
				bad(n, "MD055", "table without a header separator")
			} else if tableCells(lines[i+1]) != tableCells(line) {
				bad(n, "MD056", "separator has %d columns, header %d", tableCells(lines[i+1]), tableCells(line))
			}
		}
		if inTable && isRow && !rmTableSep.MatchString(line) {
			if want := tableCells(lines[i-countRowsBack(lines, i)]); tableCells(line) != want {
				bad(n, "MD056", "row has %d columns, header %d", tableCells(line), want)
			}
		}
		if inTable && !isRow {
			inTable = false
			if !blank(i) {
				bad(n, "MD058", "no blank line after a table")
			}
		}
		text := rmCodeSpan.ReplaceAllString(line, "")
		text = rmAutolink.ReplaceAllString(text, "")
		text = rmLink.ReplaceAllString(text, "")
		if rmBareURL.MatchString(text) {
			bad(n, "MD034", "bare URL: wrap it in <> or a link")
		}
		if m := rmHTMLish.FindString(text); m != "" {
			bad(n, "MD033", "raw HTML %q (put it in a code span)", m)
		}
		if rmEmphLine.MatchString(line) && blank(i-1) && blank(i+1) {
			bad(n, "MD036", "emphasis used instead of a heading")
		}
	}
	if inFence {
		bad(fenceStart, "MD031", "fence never closed")
	}
	if h1s != 1 {
		bad(0, "MD025", "%d top-level headings, want 1", h1s)
	}
	for i, line := range lines {
		for _, m := range rmFragment.FindAllStringSubmatch(line, -1) {
			if !slugs[m[1]] {
				bad(i+1, "MD051", "link to #%s, which no heading has", m[1])
			}
		}
	}
	return issues
}

// countRowsBack is how many lines above i the table's header row is.
func countRowsBack(lines []string, i int) int {
	k := 0
	for i-k-1 >= 0 && strings.HasPrefix(strings.TrimSpace(lines[i-k-1]), "|") {
		k++
	}
	return k
}

func TestReadme_isStandardMarkdown(t *testing.T) {
	for _, issue := range lintMarkdown(readme) {
		t.Error(issue)
	}
}

func TestLintMarkdown_catchesTheRules(t *testing.T) {
	md := "# A\n## B\n\n```\nx\n```\n\n```sh\n$ ls\n```\n* item\n\nSee http://x.io and <b> and [c](#nope).\n\n| a | b |\n|---|---|\n| 1 |\n"
	got := strings.Join(lintMarkdown(md), "\n")
	for _, rule := range []string{"MD022", "MD040", "MD031", "MD014", "MD004", "MD032", "MD034", "MD033", "MD051", "MD056"} {
		if !strings.Contains(got, rule) {
			t.Errorf("%s not reported:\n%s", rule, got)
		}
	}
	if issues := lintMarkdown("# A\n\n## B\n\n- one\n- two\n\n```yaml\nk: v\n```\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n[b](#b) <https://x.io>\n"); len(issues) != 0 {
		t.Errorf("clean markdown reported: %v", issues)
	}
}

// The screen sketches in the README are drawn with box characters: every
// line of one box has the same width, or the drawing (here and in :help)
// comes out crooked.
func TestReadme_sketchBoxesAreStraight(t *testing.T) {
	inFence, start := false, 0
	var box []string
	check := func() {
		widths := map[int][]int{}
		for k, l := range box {
			w := runewidth.StringWidth(strings.TrimRight(l, " "))
			widths[w] = append(widths[w], start+k+1)
		}
		if len(widths) > 1 {
			t.Errorf("sketch at line %d has box lines of %d widths: %v", start, len(widths), widths)
		}
		box = nil
	}
	for i, line := range strings.Split(readme, "\n") {
		if rmFence.MatchString(line) {
			if inFence {
				check()
			}
			inFence, start = !inFence, i+1
			continue
		}
		trimmed := strings.TrimLeft(line, " ")
		if inFence && trimmed != "" && strings.ContainsRune("┌│├└", []rune(trimmed)[0]) {
			box = append(box, line)
		}
	}
}

// Every account type's section has the same sub-sections, in the same order.
func TestReadme_accountSectionsAreParallel(t *testing.T) {
	sections := map[string]string{
		"IMAP/SMTP accounts": "IMAP",
		"Gmail (API)":        "Gmail",
		"Microsoft 365 / Outlook.com (Graph API)": "Outlook",
	}
	subs := map[string][]string{}
	current, fence := "", false
	for _, line := range strings.Split(readme, "\n") {
		if rmFence.MatchString(line) {
			fence = !fence
		}
		m := rmHeading.FindStringSubmatch(line)
		if fence || m == nil {
			continue
		}
		switch len(m[1]) {
		case 2:
			current = m[2]
		case 3:
			if _, ok := sections[current]; ok {
				subs[current] = append(subs[current], m[2])
			}
		}
	}
	for name, x := range sections {
		want := []string{"Setup (one time)", "Config", "Credentials", "Signing in", "Using " + x + " in m9s"}
		if got := strings.Join(subs[name], " | "); got != strings.Join(want, " | ") {
			t.Errorf("## %s has\n  %s\nwant\n  %s", name, got, strings.Join(want, " | "))
		}
	}
}
