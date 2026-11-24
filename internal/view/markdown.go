package view

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"

	"github.com/zhecho/m9s/internal/ui"
)

// mdLine is one row of rendered Markdown, as plain text and tview-tagged.
type mdLine struct {
	plain, tagged string
	// wide is a code or table row: drawn as written, never wrapped (the
	// window scrolls sideways for it).
	wide bool
	// level is 1-6 on the first line of a heading, else 0; title is that
	// heading's text without its markup.
	level int
	title string
}

var (
	mdHeading  = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	mdFence    = regexp.MustCompile("^(\\s*)(```+|~~~+)")
	mdItem     = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.*)$`)
	mdTableSep = regexp.MustCompile(`^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?\s*$`)
	mdRule     = regexp.MustCompile(`^\s*(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$`)
	mdQuote    = regexp.MustCompile(`^\s*>\s?`)
	mdCellSep  = regexp.MustCompile(`(^|[^\\])\|`)
)

// Inline styles of a span; they combine.
const (
	fCode = 1 << iota
	fBold
	fItalic
	fLink
	fDim
)

type mdSpan struct {
	text  string
	flags int
}

// mdRenderer turns Markdown into lines for one width and skin.
type mdRenderer struct {
	skin  *ui.Skin
	width int
	out   []mdLine
}

// renderMarkdown lays md out for a window width columns wide: headings in
// the accent colour, underlined at the top two levels; paragraphs, list
// items and quotes reflowed to the width (list items wrap under their own
// text); tables aligned in columns; code as written. Everything the
// Markdown says is shown; only its markup goes.
func renderMarkdown(md string, skin *ui.Skin, width int) []mdLine {
	if width <= 0 {
		width = 80
	}
	r := &mdRenderer{skin: skin, width: width}
	r.blocks(strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n"))
	for len(r.out) > 0 && r.out[len(r.out)-1].plain == "" {
		r.out = r.out[:len(r.out)-1]
	}
	return r.out
}

// listItem is an open list item: where its marker was in the source, where
// its text starts on screen, and how many bullet lists enclose it.
type listItem struct {
	srcIndent, srcContent, outContent, bullets int
}

func indentOf(line string) int { return len(line) - len(strings.TrimLeft(line, " ")) }

// startsBlock reports whether line begins something other than paragraph text.
func startsBlock(lines []string, i int) bool {
	line := lines[i]
	return strings.TrimSpace(line) == "" || mdHeading.MatchString(line) || mdFence.MatchString(line) ||
		mdItem.MatchString(line) || mdQuote.MatchString(line) || mdRule.MatchString(line) || isTableStart(lines, i)
}

func isTableStart(lines []string, i int) bool {
	return strings.HasPrefix(strings.TrimSpace(lines[i]), "|") && i+1 < len(lines) && mdTableSep.MatchString(lines[i+1])
}

func (r *mdRenderer) blank() {
	if len(r.out) > 0 && r.out[len(r.out)-1].plain != "" {
		r.out = append(r.out, mdLine{})
	}
}

func (r *mdRenderer) blocks(lines []string) {
	var list []listItem
	for i := 0; i < len(lines); {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			r.blank()
			// A list goes on after a blank line only with another item or
			// indented text.
			if j := nextText(lines, i); j < 0 || (!mdItem.MatchString(lines[j]) && (len(list) == 0 || indentOf(lines[j]) < list[len(list)-1].srcContent)) {
				list = nil
			}
			i++
		case mdFence.MatchString(line):
			i = r.fence(lines, i, list)
		case mdHeading.MatchString(line) && indentOf(line) < 4:
			list = nil
			m := mdHeading.FindStringSubmatch(line)
			r.heading(len(m[1]), m[2])
			i++
		case isTableStart(lines, i):
			list = nil
			i = r.table(lines, i)
		case mdRule.MatchString(line) && !mdItem.MatchString(line):
			list = nil
			r.out = append(r.out, mdLine{plain: strings.Repeat("─", r.width),
				tagged: r.tag(fDim, "") + strings.Repeat("─", r.width) + "[-:-:-]"})
			i++
		case mdQuote.MatchString(line):
			var text []string
			for ; i < len(lines) && mdQuote.MatchString(lines[i]); i++ {
				text = append(text, mdQuote.ReplaceAllString(lines[i], ""))
			}
			r.wrap(parseInline(strings.Join(text, " ")), "│ ", "│ ", r.skin.ColorName("foreground"), 0)
		case mdItem.MatchString(line):
			m := mdItem.FindStringSubmatch(line)
			src := len(m[1])
			for len(list) > 0 && list[len(list)-1].srcIndent >= src {
				list = list[:len(list)-1]
			}
			item := listItem{srcIndent: src, srcContent: len(line) - len(m[3])}
			marker := m[2]
			bullet := marker == "-" || marker == "*" || marker == "+"
			if len(list) > 0 {
				item.outContent, item.bullets = list[len(list)-1].outContent, list[len(list)-1].bullets
			}
			indent := item.outContent
			if bullet {
				item.bullets++
				marker = []string{"•", "◦", "▪"}[min(item.bullets, 3)-1]
			} else {
				n, _ := strconv.Atoi(strings.TrimRight(marker, ".)"))
				marker = strconv.Itoa(n) + "."
			}
			item.outContent = indent + runewidth.StringWidth(marker) + 1
			list = append(list, item)
			text := []string{m[3]}
			for i++; i < len(lines) && !startsBlock(lines, i); i++ {
				text = append(text, strings.TrimSpace(lines[i]))
			}
			first := strings.Repeat(" ", indent) + marker + " "
			r.wrap(parseInline(strings.Join(text, " ")), first, strings.Repeat(" ", item.outContent), r.skin.ColorName("foreground"), 0)
		default:
			text := []string{trimmed}
			for i++; i < len(lines) && !startsBlock(lines, i); i++ {
				text = append(text, strings.TrimSpace(lines[i]))
			}
			prefix := ""
			if len(list) > 0 && indentOf(line) >= list[len(list)-1].srcContent {
				prefix = strings.Repeat(" ", list[len(list)-1].outContent) // a later paragraph of an item
			} else {
				list = nil
			}
			r.wrap(parseInline(strings.Join(text, " ")), prefix, prefix, r.skin.ColorName("foreground"), 0)
		}
	}
}

// nextText is the index of the next non-blank line after i, -1 if none.
func nextText(lines []string, i int) int {
	for j := i + 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) != "" {
			return j
		}
	}
	return -1
}

func (r *mdRenderer) heading(level int, text string) {
	if level <= 2 {
		r.blank()
	}
	accent := r.skin.ColorName("accent")
	before := len(r.out)
	spans := parseInline(text)
	r.wrap(spans, "", "", accent, fBold)
	if len(r.out) > before {
		var t strings.Builder
		for _, sp := range spans {
			t.WriteString(sp.text)
		}
		r.out[before].level, r.out[before].title = level, t.String()
	}
	if level > 2 {
		return
	}
	w := 0
	for _, l := range r.out[before:] {
		w = max(w, runewidth.StringWidth(l.plain))
	}
	rule := strings.Repeat(map[bool]string{true: "═", false: "─"}[level == 1], w)
	r.out = append(r.out, mdLine{plain: rule, tagged: fmt.Sprintf("[%s::]%s[-:-:-]", accent, rule)})
}

// fence copies a fenced code block as written, two columns in (more inside
// a list item), and returns the line after it.
func (r *mdRenderer) fence(lines []string, i int, list []listItem) int {
	m := mdFence.FindStringSubmatch(lines[i])
	indent, mark := len(m[1]), m[2]
	pad := "  "
	if len(list) > 0 {
		pad = strings.Repeat(" ", list[len(list)-1].outContent) + pad
	}
	for i++; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(strings.TrimSpace(line), mark[:3]) && strings.Trim(strings.TrimSpace(line), mark[:1]) == "" {
			return i + 1
		}
		cut := min(indent, indentOf(line))
		text := pad + line[cut:]
		r.out = append(r.out, mdLine{plain: text, tagged: r.tag(fDim, "") + tview.Escape(text) + "[-:-:-]", wide: true})
	}
	return i
}

// tableRow splits a table row into its cells, unescaping \|.
func tableRow(line string) []string {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "|")
	if strings.HasSuffix(s, "|") && !strings.HasSuffix(s, `\|`) {
		s = s[:len(s)-1]
	}
	var cells []string
	last := 0
	for _, loc := range mdCellSep.FindAllStringIndex(s, -1) {
		end := loc[1] - 1 // the "|"
		cells = append(cells, s[last:end])
		last = loc[1]
	}
	cells = append(cells, s[last:])
	for k, c := range cells {
		cells[k] = strings.TrimSpace(strings.ReplaceAll(c, `\|`, "|"))
	}
	return cells
}

// table draws a table in aligned columns and returns the line after it.
func (r *mdRenderer) table(lines []string, i int) int {
	header := tableRow(lines[i])
	var right []bool
	for _, c := range tableRow(lines[i+1]) {
		right = append(right, strings.HasSuffix(c, ":") && !strings.HasPrefix(c, ":"))
	}
	rows := [][][]mdSpan{}
	for _, c := range [][]string{header} {
		rows = append(rows, cellSpans(c, fBold))
	}
	for i += 2; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|"); i++ {
		rows = append(rows, cellSpans(tableRow(lines[i]), 0))
	}
	widths := make([]int, len(header))
	for _, row := range rows {
		for k := range min(len(row), len(widths)) {
			widths[k] = max(widths[k], spansWidth(row[k]))
		}
	}
	fg, dim := r.skin.ColorName("foreground"), r.tag(fDim, "")
	emit := func(row [][]mdSpan) {
		var p, t strings.Builder
		for k := range widths {
			var cell []mdSpan
			if k < len(row) {
				cell = row[k]
			}
			if k > 0 {
				p.WriteString(" │ ")
				t.WriteString(dim + " │ ")
			}
			gap := strings.Repeat(" ", widths[k]-spansWidth(cell))
			if k < len(right) && right[k] {
				p.WriteString(gap)
				t.WriteString(r.tag(0, fg) + gap)
			}
			for _, s := range cell {
				p.WriteString(s.text)
				t.WriteString(r.tag(s.flags, fg) + tview.Escape(s.text))
			}
			if k == len(widths)-1 {
				continue // no trailing spaces
			}
			if k >= len(right) || !right[k] {
				p.WriteString(gap)
				t.WriteString(r.tag(0, fg) + gap)
			}
		}
		r.out = append(r.out, mdLine{plain: strings.TrimRight(p.String(), " "), tagged: t.String() + "[-:-:-]", wide: true})
	}
	emit(rows[0])
	var rule []string
	for _, w := range widths {
		rule = append(rule, strings.Repeat("─", w))
	}
	sep := strings.Join(rule, "─┼─")
	r.out = append(r.out, mdLine{plain: sep, tagged: dim + sep + "[-:-:-]", wide: true})
	for _, row := range rows[1:] {
		emit(row)
	}
	return i
}

func cellSpans(cells []string, flags int) [][]mdSpan {
	out := make([][]mdSpan, len(cells))
	for k, c := range cells {
		for _, s := range parseInline(c) {
			s.flags |= flags
			out[k] = append(out[k], s)
		}
	}
	return out
}

func spansWidth(spans []mdSpan) int {
	w := 0
	for _, s := range spans {
		w += runewidth.StringWidth(s.text)
	}
	return w
}

// tag is the tview tag that draws a span with flags; base is the colour of
// plain text ("" for the dim colour).
func (r *mdRenderer) tag(flags int, base string) string {
	color := base
	switch {
	case flags&fDim != 0 || base == "":
		color = r.skin.ColorName("dim")
	case flags&fCode != 0:
		color = r.skin.ColorName("heading")
	}
	attrs := ""
	if flags&(fCode|fBold) != 0 {
		attrs += "b"
	}
	// tview has no italic: emphasis is underlined, like a link.
	if flags&(fItalic|fLink) != 0 {
		attrs += "u"
	}
	if attrs == "" {
		attrs = "-"
	}
	return "[" + color + "::" + attrs + "]"
}

// wrap word-wraps spans into lines: first starts the first line, rest the
// others; base is the text colour and extra flags apply to every span.
func (r *mdRenderer) wrap(spans []mdSpan, first, rest, base string, extra int) {
	type word []mdSpan
	var words []word
	var cur word
	for _, s := range spans {
		s.flags |= extra
		parts := strings.Split(s.text, " ")
		for k, part := range parts {
			if k > 0 && len(cur) > 0 {
				words = append(words, cur)
				cur = nil
			}
			if part != "" {
				cur = append(cur, mdSpan{part, s.flags})
			}
		}
	}
	if len(cur) > 0 {
		words = append(words, cur)
	}
	if len(words) == 0 {
		return
	}
	prefixTag := r.tag(extra, base)
	if strings.HasPrefix(first, "│") {
		prefixTag = r.tag(fDim, "")
	}
	var lp, lt strings.Builder
	lineW, n := 0, 0
	start := func(prefix string) {
		lp.Reset()
		lt.Reset()
		lp.WriteString(prefix)
		lt.WriteString(prefixTag + tview.Escape(prefix))
		lineW, n = runewidth.StringWidth(prefix), 0
	}
	flush := func() {
		r.out = append(r.out, mdLine{plain: lp.String(), tagged: lt.String() + "[-:-:-]"})
	}
	start(first)
	for _, w := range words {
		ww := spansWidth(w)
		if n > 0 && lineW+1+ww > r.width {
			flush()
			start(rest)
		}
		if n > 0 {
			lp.WriteString(" ")
			lt.WriteString(r.tag(extra, base) + " ")
			lineW++
		}
		for _, s := range w {
			lp.WriteString(s.text)
			lt.WriteString(r.tag(s.flags, base) + tview.Escape(s.text))
		}
		lineW += ww
		n++
	}
	flush()
}

// parseInline splits one paragraph's text into styled spans: code spans,
// **bold**, *italic* / _italic_, links (an anchor link shows its text, any
// other its text and address), <autolinks> and backslash escapes. Runs of
// white space become one space.
func parseInline(s string) []mdSpan {
	s = strings.Join(strings.Fields(s), " ")
	var out []mdSpan
	var buf strings.Builder
	bold, italic := false, false
	flags := func() int {
		f := 0
		if bold {
			f |= fBold
		}
		if italic {
			f |= fItalic
		}
		return f
	}
	flush := func() {
		if buf.Len() > 0 {
			out = append(out, mdSpan{buf.String(), flags()})
			buf.Reset()
		}
	}
	runeBefore := func(i int) rune {
		if i == 0 {
			return ' '
		}
		r, _ := utf8.DecodeLastRuneInString(s[:i])
		return r
	}
	runeAt := func(i int) rune {
		if i >= len(s) {
			return ' '
		}
		r, _ := utf8.DecodeRuneInString(s[i:])
		return r
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && strings.IndexByte("\\`*_{}[]()#+-.!|<>~", s[i+1]) >= 0:
			buf.WriteByte(s[i+1])
			i += 2
		case c == '`':
			n := 1
			for i+n < len(s) && s[i+n] == '`' {
				n++
			}
			ticks := strings.Repeat("`", n)
			end := strings.Index(s[i+n:], ticks)
			if end < 0 {
				buf.WriteString(ticks)
				i += n
				continue
			}
			flush()
			code := s[i+n : i+n+end]
			if len(code) > 1 && code[0] == ' ' && code[len(code)-1] == ' ' && strings.TrimSpace(code) != "" {
				code = code[1 : len(code)-1]
			}
			out = append(out, mdSpan{code, flags() | fCode})
			i += n + end + n
		case c == '*' || c == '_':
			n := 1
			for i+n < len(s) && s[i+n] == c && n < 3 {
				n++
			}
			before, after := runeBefore(i), runeAt(i+n)
			opens := !unicode.IsSpace(after)
			closes := !unicode.IsSpace(before)
			if c == '_' { // not inside words: snake_case stays as it is
				opens = opens && !unicode.IsLetter(before) && !unicode.IsDigit(before)
				closes = closes && !unicode.IsLetter(after) && !unicode.IsDigit(after)
			}
			marker := s[i : i+n]
			changed := false
			if n >= 2 {
				switch {
				case bold && closes:
					flush()
					bold, changed = false, true
				case !bold && opens && strings.Contains(s[i+n:], marker[:2]):
					flush()
					bold, changed = true, true
				}
			}
			if n == 1 || n == 3 {
				switch {
				case italic && closes:
					flush()
					italic, changed = false, true
				case !italic && opens && strings.Contains(s[i+n:], marker[:1]):
					flush()
					italic, changed = true, true
				}
			}
			if !changed {
				buf.WriteString(marker)
			}
			i += n
		case c == '[':
			text, url, next, ok := linkAt(s, i)
			if !ok {
				buf.WriteByte(c)
				i++
				continue
			}
			flush()
			for _, sp := range parseInline(text) {
				sp.flags |= flags() | fLink
				out = append(out, sp)
			}
			if !strings.HasPrefix(url, "#") && !sameAddress(url, text) {
				out = append(out, mdSpan{" (" + url + ")", flags() | fDim})
			}
			i = next
		case c == '<':
			end := strings.IndexByte(s[i:], '>')
			inner := ""
			if end > 0 {
				inner = s[i+1 : i+end]
			}
			if strings.HasPrefix(inner, "http://") || strings.HasPrefix(inner, "https://") || strings.HasPrefix(inner, "mailto:") {
				flush()
				out = append(out, mdSpan{strings.TrimPrefix(inner, "mailto:"), flags() | fLink})
				i += end + 1
				continue
			}
			buf.WriteByte(c)
			i++
		default:
			buf.WriteByte(c)
			i++
		}
	}
	flush()
	return out
}

// sameAddress reports whether a link's text is its address, give or take
// the scheme and a final slash.
func sameAddress(url, text string) bool {
	bare := strings.TrimSuffix(url, "/")
	for _, scheme := range []string{"https://", "http://", "mailto:"} {
		bare = strings.TrimPrefix(bare, scheme)
	}
	return text == url || text == bare
}

// linkAt reads [text](url) at s[i]; next is the index after it.
func linkAt(s string, i int) (text, url string, next int, ok bool) {
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				if j+1 >= len(s) || s[j+1] != '(' {
					return "", "", 0, false
				}
				end := strings.IndexByte(s[j+2:], ')')
				if end < 0 {
					return "", "", 0, false
				}
				return s[i+1 : j], strings.TrimSpace(s[j+2 : j+2+end]), j + 3 + end, true
			}
		}
	}
	return "", "", 0, false
}
