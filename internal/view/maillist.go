package view

import (
	"fmt"
	"strings"

	tcell "github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/ui"
)

// MailListView is the top-right table of email envelopes.
type MailListView struct {
	*tview.Table
	skin         *ui.Skin
	allEnvelopes []*mail.Envelope
	visible      []*mail.Envelope // rows on screen: allEnvelopes narrowed by filter
	filter       string
	search       string // the server search the rows answer, or ""
	dateFormat   string
	self         string // the owner's address: "me" among participants
	onSelect     func(*mail.Envelope)
	marked       map[string]bool // IDs of the messages picked for m and d
	onMarks      func()          // called when the marks change
	focusFunc    func()
	blurFunc     func()
}

// NewMailListView creates the email list table.
func NewMailListView(skin *ui.Skin, dateFormat string, onSelect func(*mail.Envelope)) *MailListView {
	table := tview.NewTable()
	table.SetBorder(true)
	table.SetTitle(" MAIL LIST ")
	table.SetTitleColor(skin.Color("header_fg"))
	table.SetBorderColor(skin.Color("border"))
	table.SetBackgroundColor(skin.Color("background"))
	table.SetSelectable(true, false)
	table.SetSelectedStyle(skin.Style("selected_fg", "selected_bg"))

	mlv := &MailListView{
		Table:      table,
		skin:       skin,
		dateFormat: dateFormat,
		onSelect:   onSelect,
		marked:     map[string]bool{},
	}
	table.SetSelectionChangedFunc(func(row, col int) {
		if row >= 0 && row < len(mlv.visible) && mlv.onSelect != nil {
			mlv.onSelect(mlv.visible[row])
		}
	})
	return mlv
}

// SetSelf sets the owner's address, shown as "me" among the participants of
// a conversation row.
func (mlv *MailListView) SetSelf(addr string) { mlv.self = addr }

// SetFocusFunc registers a callback invoked when this view gains focus.
func (mlv *MailListView) SetFocusFunc(fn func()) { mlv.focusFunc = fn }

// SetBlurFunc registers a callback invoked when this view loses focus.
func (mlv *MailListView) SetBlurFunc(fn func()) { mlv.blurFunc = fn }

// Focus overrides Box.Focus to fire the registered focusFunc.
func (mlv *MailListView) Focus(delegate func(tview.Primitive)) {
	if mlv.focusFunc != nil {
		mlv.focusFunc()
	}
	mlv.Table.Focus(delegate)
}

// Blur overrides Box.Blur to fire the registered blurFunc.
func (mlv *MailListView) Blur() {
	if mlv.blurFunc != nil {
		mlv.blurFunc()
	}
	mlv.Table.Blur()
}

// InputHandler makes g and Home jump to the first message. The table itself
// would stop at the second row: it assumes row 0 is a column header, and the
// mail list has none.
func (mlv *MailListView) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	inner := mlv.Table.InputHandler()
	return func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
		top := event.Key() == tcell.KeyHome || (event.Key() == tcell.KeyRune && event.Rune() == 'g')
		if top && len(mlv.visible) > 0 {
			mlv.Select(0, 0)
			return
		}
		inner(event, setFocus)
	}
}

// markClick wraps the table's mouse handler: a click with a modifier held
// (shift, ctrl, alt or cmd, as far as the terminal reports it) marks or
// unmarks the row under the pointer instead of moving the cursor there.
func (mlv *MailListView) markClick(inner mouseHandler) mouseHandler {
	return func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (bool, tview.Primitive) {
		const mods = tcell.ModShift | tcell.ModCtrl | tcell.ModAlt | tcell.ModMeta
		x, y := event.Position()
		if action == tview.MouseLeftClick && event.Modifiers()&mods != 0 && mlv.InRect(x, y) {
			if row := mlv.rowAt(y); row >= 0 {
				mlv.ToggleMark(row)
			}
			setFocus(mlv)
			return true, nil
		}
		return inner(action, event, setFocus)
	}
}

// rowAt returns the message row drawn at screen row y, or -1.
func (mlv *MailListView) rowAt(y int) int {
	_, top, _, height := mlv.GetInnerRect()
	if y < top || y >= top+height {
		return -1
	}
	offset, _ := mlv.GetOffset()
	row := y - top + offset
	if row >= len(mlv.visible) {
		return -1
	}
	return row
}

// SetMarksChangedFunc registers a callback invoked whenever the marks change.
func (mlv *MailListView) SetMarksChangedFunc(fn func()) { mlv.onMarks = fn }

// ToggleMark marks the message on row, or unmarks it if it is marked.
func (mlv *MailListView) ToggleMark(row int) {
	if row < 0 || row >= len(mlv.visible) {
		return
	}
	id := mlv.visible[row].ID
	if mlv.marked[id] {
		delete(mlv.marked, id)
	} else {
		mlv.marked[id] = true
	}
	mlv.drawRow(row, mlv.visible[row])
	mlv.marksChanged()
}

// MarkCursor toggles the mark on the cursor row and moves the cursor to the
// next row, so marking a run of messages is a run of spaces.
func (mlv *MailListView) MarkCursor() {
	row, _ := mlv.GetSelection()
	if row < 0 || row >= len(mlv.visible) {
		return
	}
	mlv.ToggleMark(row)
	if row+1 < len(mlv.visible) {
		mlv.Select(row+1, 0)
	}
}

// Marked returns the marked messages on screen, in list order. A marked
// message the filter hides is left out: m and d act on what is shown.
func (mlv *MailListView) Marked() []*mail.Envelope {
	var out []*mail.Envelope
	for _, env := range mlv.visible {
		if mlv.marked[env.ID] {
			out = append(out, env)
		}
	}
	return out
}

// Unmark drops the marks of the messages with ids.
func (mlv *MailListView) Unmark(ids ...string) {
	changed := false
	for _, id := range ids {
		if mlv.marked[id] {
			delete(mlv.marked, id)
			changed = true
		}
	}
	if changed {
		mlv.Redraw()
		mlv.marksChanged()
	}
}

// ClearMarks unmarks every message.
func (mlv *MailListView) ClearMarks() {
	if len(mlv.marked) == 0 {
		return
	}
	mlv.marked = map[string]bool{}
	mlv.Redraw()
	mlv.marksChanged()
}

func (mlv *MailListView) marksChanged() {
	if mlv.onMarks != nil {
		mlv.onMarks()
	}
}

// pruneMarks drops the marks of messages no longer in the folder.
func (mlv *MailListView) pruneMarks() {
	if len(mlv.marked) == 0 {
		return
	}
	present := make(map[string]bool, len(mlv.allEnvelopes))
	for _, env := range mlv.allEnvelopes {
		present[env.ID] = true
	}
	for id := range mlv.marked {
		if !present[id] {
			delete(mlv.marked, id)
		}
	}
}

// SetEnvelopes populates the table with a new list, newest on top, and drops
// any filter and marks.
func (mlv *MailListView) SetEnvelopes(envelopes []*mail.Envelope) {
	mlv.allEnvelopes = mail.NewestFirst(envelopes)
	mlv.filter = ""
	mlv.marked = map[string]bool{}
	mlv.renderEnvelopes(mlv.allEnvelopes)
}

// ReplaceEnvelopes reloads the list in place, newest on top: the filter is kept and the
// message with keepID stays selected if it is still visible (otherwise the
// top row). Rows are rendered once, so a message the filter hides is never
// selected on the way. Marks stay on the messages still in the list.
func (mlv *MailListView) ReplaceEnvelopes(envelopes []*mail.Envelope, keepID string) {
	mlv.allEnvelopes = mail.NewestFirst(envelopes)
	mlv.pruneMarks()
	visible := mlv.filtered(mlv.filter)
	row := 0
	for i, env := range visible {
		if keepID != "" && env.ID == keepID {
			row = i
			break
		}
	}
	mlv.renderEnvelopesAt(visible, row)
}

// SetFilter narrows the list to messages whose sender name, sender address
// or subject contains query, case-insensitively. An empty query shows all.
func (mlv *MailListView) SetFilter(query string) {
	mlv.filter = query
	mlv.renderEnvelopes(mlv.filtered(query))
}

// filtered returns the envelopes that match query ("" matches all).
func (mlv *MailListView) filtered(query string) []*mail.Envelope {
	if query == "" {
		return mlv.allEnvelopes
	}
	q := strings.ToLower(query)
	var out []*mail.Envelope
	for _, env := range mlv.allEnvelopes {
		if envelopeMatches(env, q) {
			out = append(out, env)
		}
	}
	return out
}

func envelopeMatches(env *mail.Envelope, lowerQuery string) bool {
	if strings.Contains(strings.ToLower(env.Subject), lowerQuery) {
		return true
	}
	for _, from := range env.From {
		if strings.Contains(strings.ToLower(from.Name), lowerQuery) ||
			strings.Contains(strings.ToLower(from.Email), lowerQuery) {
			return true
		}
	}
	return false
}

// Envelopes returns every message of the open folder, filtered out or not,
// newest first.
func (mlv *MailListView) Envelopes() []*mail.Envelope { return mlv.allEnvelopes }

// SetSearch marks the rows as the answer to a server search ("" for none),
// which the title says.
func (mlv *MailListView) SetSearch(query string) { mlv.search = query }

// FilterQuery returns the active filter, or "".
func (mlv *MailListView) FilterQuery() string { return mlv.filter }

// VisibleCount returns the number of messages on screen.
func (mlv *MailListView) VisibleCount() int { return len(mlv.visible) }

// TitleText returns the pane title, including the active filter and the
// number of marked messages, if any.
func (mlv *MailListView) TitleText() string {
	title := mlv.baseTitle()
	if n := len(mlv.Marked()); n > 0 {
		title += fmt.Sprintf(" [%d selected]", n)
	}
	return title
}

func (mlv *MailListView) baseTitle() string {
	if mlv.search != "" {
		if mlv.filter != "" {
			return "SEARCH: " + mlv.search + " </" + mlv.filter + ">"
		}
		return "SEARCH: " + mlv.search
	}
	if mlv.filter == "" {
		return "MAIL LIST"
	}
	return "MAIL LIST </" + mlv.filter + ">"
}

func (mlv *MailListView) renderEnvelopes(envelopes []*mail.Envelope) {
	mlv.renderEnvelopesAt(envelopes, 0)
}

// renderEnvelopesAt draws envelopes and selects row (clamped).
func (mlv *MailListView) renderEnvelopesAt(envelopes []*mail.Envelope, row int) {
	mlv.visible = envelopes
	mlv.Clear()
	for row, env := range envelopes {
		mlv.drawRow(row, env)
	}
	if len(envelopes) > 0 {
		mlv.Select(min(max(row, 0), len(envelopes)-1), 0)
	} else if mlv.onSelect != nil {
		mlv.onSelect(nil) // nothing on screen: nothing is selected
	}
}

// markSign is the sign of a marked row; it takes the place of the read and
// star sign.
const markSign = "✓ "

// drawRow fills one row's cells from env: the read and star sign (or the
// mark), colours, participants, subject, attachment sign and date.
func (mlv *MailListView) drawRow(row int, env *mail.Envelope) {
	fromFg := mlv.skin.Color("foreground")
	subjFg := mlv.skin.Color("foreground")
	if env.IsUnread() {
		fromFg = mlv.skin.Color("unread")
		subjFg = mlv.skin.Color("unread")
	}
	indicator := "  "
	if env.IsUnread() {
		indicator = "● "
	}
	if env.IsFlagged() {
		indicator = "★ "
	}
	if mlv.marked[env.ID] {
		indicator = markSign
		fromFg = mlv.skin.Color("accent")
		subjFg = mlv.skin.Color("accent")
	}
	subject := env.Subject
	if env.ThreadCount > 1 {
		subject = fmt.Sprintf("%s (%d)", subject, env.ThreadCount)
	}
	mlv.SetCell(row, 0, tview.NewTableCell(indicator+participantsText(env, mlv.self)).
		SetTextColor(fromFg).SetMaxWidth(20))
	mlv.SetCell(row, 1, tview.NewTableCell(subject).
		SetTextColor(subjFg).SetExpansion(1))
	mlv.SetCell(row, 2, mlv.signCell(env))
	mlv.SetCell(row, 3, tview.NewTableCell(env.FormattedDate(mlv.dateFormat)).
		SetTextColor(mlv.skin.Color("dim")).SetAlign(tview.AlignRight))
}

// attachSign marks a row whose message (or conversation) carries files.
const attachSign = "📎"

// signCell is a row's attachment column: the sign, or empty.
func (mlv *MailListView) signCell(env *mail.Envelope) *tview.TableCell {
	sign := ""
	if env.HasAttach {
		sign = attachSign
	}
	return tview.NewTableCell(sign).SetTextColor(mlv.skin.Color("dim"))
}

// Redraw updates every visible row in place, e.g. after its read state or
// attachment sign changed. It does not select anything, so no body is
// loaded because of it.
func (mlv *MailListView) Redraw() {
	for row, env := range mlv.visible {
		mlv.drawRow(row, env)
	}
}
