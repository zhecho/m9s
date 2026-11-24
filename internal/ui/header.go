package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

// HeaderHeight is the number of rows the header occupies.
const HeaderHeight = 8

const (
	headerGutter   = 3 // blank columns after the info block and each hint column
	headerMinInfoW = 30
)

var headerLogo = []string{
	` __  __  ___      `,
	`|  \/  |/ _ \ ___ `,
	`| |\/| | (_) / __|`,
	`| |  | |\__, \__ \`,
	`|_|  |_|  /_/|___/`,
}

// Logo returns the m9s ASCII logo, one row per line.
func Logo() []string { return append([]string(nil), headerLogo...) }

// HeaderInfo is everything the info block on the left of the header shows.
type HeaderInfo struct {
	Account, User, Folder string // empty Folder renders "-"
	Unseen, Total         uint32
	HasCounts             bool   // false renders Unread as "-"
	Status                string // plain text, e.g. "● connected"
	StatusRole            string // skin role for Status: "dim", "flash_ok" or "flash_err"
	Provider, Version     string // shown as "Provider / Version"; empty parts render "-"
	Server                string // the remote server's name and version; empty renders "-"
}

// Header is the k9s-style top bar: an info block on the left, key hints in
// columns, and a logo on the right. It only renders what it is given.
type Header struct {
	*tview.Box
	skin *Skin

	info     *tview.TextView
	infoText []string // plain info rows
	hintCols []*tview.TextView
	hintText [][]string // plain rows per hint column
	logo     *tview.TextView
}

// NewHeader creates an empty header.
func NewHeader(skin *Skin) *Header {
	h := &Header{
		Box:  tview.NewBox(),
		skin: skin,
		info: tview.NewTextView(),
		logo: tview.NewTextView(),
	}
	h.SetBackgroundColor(skin.Color("header_bg"))
	h.styleCell(h.info)
	h.styleCell(h.logo)
	h.logo.SetText(fmt.Sprintf("[%s::b]%s", skin.ColorName("heading"), tview.Escape(strings.Join(headerLogo, "\n"))))
	h.SetInfo(HeaderInfo{})
	return h
}

func (h *Header) styleCell(tv *tview.TextView) {
	tv.SetDynamicColors(true)
	tv.SetBorder(false)
	tv.SetWrap(false)
	tv.SetBackgroundColor(h.skin.Color("header_bg"))
}

// SetInfo replaces the info block.
func (h *Header) SetInfo(info HeaderInfo) {
	label := h.skin.ColorName("heading")
	value := h.skin.ColorName("header_fg")

	folder := info.Folder
	if folder == "" {
		folder = "-"
	}
	unreadPlain, unreadTagged := "-", "-"
	if info.HasCounts {
		unreadPlain = fmt.Sprintf("%d of %d", info.Unseen, info.Total)
		unreadTagged = fmt.Sprintf("[%s::b]%d[-:-:-] [%s::]of %d", h.skin.ColorName("unread"), info.Unseen, value, info.Total)
	}
	statusRole := info.StatusRole
	if statusRole == "" {
		statusRole = "dim"
	}

	provider := orDash(info.Provider) + " / " + orDash(info.Version)

	rows := []struct{ label, plain, tagged string }{
		{"Account:", info.Account, bold(value, info.Account)},
		{"User:", info.User, bold(value, info.User)},
		{"Folder:", folder, bold(value, folder)},
		{"Unread:", unreadPlain, unreadTagged},
		{"Status:", info.Status, bold(h.skin.ColorName(statusRole), info.Status)},
		{"Provider:", provider, bold(value, provider)},
		{"RSRV:", orDash(info.Server), bold(value, orDash(info.Server))},
	}
	plain := make([]string, len(rows))
	tagged := make([]string, len(rows))
	for i, r := range rows {
		plain[i] = fmt.Sprintf("%-9s %s", r.label, r.plain)
		tagged[i] = fmt.Sprintf("[%s::]%-9s[-:-:-] %s", label, r.label, r.tagged)
	}
	h.infoText = plain
	h.info.SetText(strings.Join(tagged, "\n"))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func bold(color, text string) string {
	return fmt.Sprintf("[%s::b]%s[-:-:-]", color, tview.Escape(text))
}

// SetHints replaces the key hints. general fills columns of HeaderHeight rows
// top to bottom; pane hints (may be nil) always start a new last column.
// Hints with an empty Key are skipped.
func (h *Header) SetHints(general, pane []KeyHint) {
	var cols [][]KeyHint
	general = boundHints(general)
	for len(general) > 0 {
		n := min(HeaderHeight, len(general))
		cols = append(cols, general[:n])
		general = general[n:]
	}
	if pane = boundHints(pane); len(pane) > 0 {
		// More pane hints than rows would be clipped; none of the panes has that many.
		cols = append(cols, pane[:min(HeaderHeight, len(pane))])
	}

	key := h.skin.ColorName("accent")
	desc := h.skin.ColorName("header_fg")
	h.hintCols = h.hintCols[:0]
	h.hintText = h.hintText[:0]
	for _, col := range cols {
		keyW := 0
		for _, hint := range col {
			keyW = max(keyW, utf8.RuneCountInString(hint.Key)+2)
		}
		plain := make([]string, len(col))
		tagged := make([]string, len(col))
		for i, hint := range col {
			k := "<" + hint.Key + ">"
			pad := strings.Repeat(" ", keyW-utf8.RuneCountInString(k))
			plain[i] = k + pad + " " + hint.Desc
			tagged[i] = fmt.Sprintf("[%s::b]%s[-:-:-]%s [%s::]%s[-:-:-]", key, tview.Escape(k), pad, desc, tview.Escape(hint.Desc))
		}
		tv := tview.NewTextView()
		h.styleCell(tv)
		tv.SetText(strings.Join(tagged, "\n"))
		h.hintCols = append(h.hintCols, tv)
		h.hintText = append(h.hintText, plain)
	}
}

func boundHints(hints []KeyHint) []KeyHint {
	var out []KeyHint
	for _, hint := range hints {
		if hint.Key != "" {
			out = append(out, hint)
		}
	}
	return out
}

// InfoText returns the info block as plain text, one row per line (for tests).
func (h *Header) InfoText() string { return strings.Join(h.infoText, "\n") }

// HintsText returns all hint columns as plain text laid out side by side at
// full width, one header row per line (for tests).
func (h *Header) HintsText() string {
	widths := h.hintWidths()
	rows := make([]string, HeaderHeight)
	for r := range rows {
		var b strings.Builder
		for c, col := range h.hintText {
			cell := ""
			if r < len(col) {
				cell = col[r]
			}
			b.WriteString(cell)
			b.WriteString(strings.Repeat(" ", widths[c]-utf8.RuneCountInString(cell)))
		}
		rows[r] = strings.TrimRight(b.String(), " ")
	}
	return strings.Join(rows, "\n")
}

func maxWidth(lines []string) int {
	w := 0
	for _, l := range lines {
		w = max(w, utf8.RuneCountInString(l))
	}
	return w
}

func (h *Header) infoWidth() int {
	return max(headerMinInfoW, maxWidth(h.infoText)+headerGutter)
}

func (h *Header) hintWidths() []int {
	widths := make([]int, len(h.hintText))
	for i, col := range h.hintText {
		widths[i] = maxWidth(col) + headerGutter
	}
	return widths
}

// headerLayout decides what fits in width: the info block always gets infoW;
// the logo is dropped first, then hint columns from the right. colW are the
// hint-column widths, left to right.
func headerLayout(width, infoW int, colW []int, logoW int) (cols int, logo bool) {
	used := infoW
	if used > width {
		return 0, false
	}
	for _, w := range colW {
		if used+w > width {
			return cols, false
		}
		used += w
		cols++
	}
	return cols, used+logoW <= width
}

// Draw lays out and draws the header.
func (h *Header) Draw(screen tcell.Screen) {
	h.Box.DrawForSubclass(screen, h)
	x, y, width, height := h.GetInnerRect()
	if width <= 0 || height <= 0 {
		return
	}
	infoW := h.infoWidth()
	colW := h.hintWidths()
	logoW := maxWidth(headerLogo)
	cols, logo := headerLayout(width, infoW, colW, logoW)

	h.info.SetRect(x, y, min(infoW, width), height)
	h.info.Draw(screen)

	cx := x + infoW
	for i := range cols {
		h.hintCols[i].SetRect(cx, y, colW[i], height)
		h.hintCols[i].Draw(screen)
		cx += colW[i]
	}
	if logo {
		h.logo.SetRect(x+width-logoW, y, logoW, height)
		h.logo.Draw(screen)
	}
}
