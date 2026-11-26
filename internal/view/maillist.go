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
	// The server search's title: its kind ("Gmail"), how many match
	// (searchTotal; 0: not said, searchEstimate: Gmail's estimate),
	// whether more pages follow, and what it is doing (searchBusy; with
	// searchPending, its first page is not here yet).
	searchKind                 string
	searchTotal                int
	searchEstimate, searchMore bool
	searchBusy                 string
	searchPending              bool
	dateFormat                 string
	self                       string // the owner's address: "me" among participants
	onSelect                   func(*mail.Envelope)
	marked                     map[string]bool // IDs of the messages picked for m and d
	onMarks                    func()          // called when the marks or the rows change
	// conversations: the rows are conversations (the title's count says so).
	conversations bool
	// listed: the rows come from the engine's Lister, already filtered over
	// the whole folder, a page at a time; the list does not filter them
	// again. total is the folder's (or the filter's) count, unread the
	// folder's unread count, for the title.
	listed        bool
	total, unread int
	// rowsFor is the filter text the loaded rows answer (listed mode).
	// While the text differs — typed, its answer not here yet — the loaded
	// rows are narrowed to it locally, so nothing that does not match can
	// be seen or selected.
	rowsFor string
	// allMarked: the marks are exactly what MarkAll gave (A); A again
	// selects none.
	allMarked bool
	// extra are the envelopes MarkAll marked beyond the loaded rows, so
	// actions reach them and a refresh that loads fewer rows keeps their
	// marks; extraOrder keeps their order.
	extra      map[string]*mail.Envelope
	extraOrder []string
	nearEnd    func() // called when the cursor reaches the last row
	focusFunc  func()
	blurFunc   func()
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
		extra:      map[string]*mail.Envelope{},
	}
	table.SetSelectionChangedFunc(func(row, col int) {
		if row >= 0 && row < len(mlv.visible) && mlv.onSelect != nil {
			mlv.onSelect(mlv.visible[row])
		}
		if mlv.nearEnd != nil && len(mlv.visible) > 0 && row == len(mlv.visible)-1 {
			mlv.nearEnd()
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

// SetMarksChangedFunc registers a callback invoked whenever the marks or the
// rows on screen change (the title counts both).
func (mlv *MailListView) SetMarksChangedFunc(fn func()) { mlv.onMarks = fn }

// ToggleMark marks the message on row, or unmarks it if it is marked.
func (mlv *MailListView) ToggleMark(row int) {
	if row < 0 || row >= len(mlv.visible) {
		return
	}
	id := mlv.visible[row].ID
	mlv.allMarked = false
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

// Marked returns the marked messages on screen, in list order, then the
// marked ones not loaded yet (MarkAll). A marked message the filter hides
// is left out: actions reach what the list shows or would show.
func (mlv *MailListView) Marked() []*mail.Envelope {
	var out []*mail.Envelope
	shown := make(map[string]bool, len(mlv.visible))
	for _, env := range mlv.visible {
		shown[env.ID] = true
		if mlv.marked[env.ID] {
			out = append(out, env)
		}
	}
	for _, id := range mlv.extraOrder {
		if env, ok := mlv.extra[id]; ok && !shown[id] && mlv.marked[id] {
			out = append(out, env)
		}
	}
	return out
}

// MarksBeyondLoaded reports whether a marked message is not loaded (A over
// a whole folder or filter).
func (mlv *MailListView) MarksBeyondLoaded() bool {
	loaded := make(map[string]bool, len(mlv.allEnvelopes))
	for _, env := range mlv.allEnvelopes {
		loaded[env.ID] = true
	}
	for id := range mlv.extra {
		if mlv.marked[id] && !loaded[id] {
			return true
		}
	}
	return false
}

// MarkAll marks envs: the rows the list shows and, for those not loaded,
// their envelopes, so actions reach them too.
func (mlv *MailListView) MarkAll(envs []*mail.Envelope) {
	loaded := make(map[string]bool, len(mlv.allEnvelopes))
	for _, env := range mlv.allEnvelopes {
		loaded[env.ID] = true
	}
	mlv.allMarked = true
	for _, env := range envs {
		mlv.marked[env.ID] = true
		if !loaded[env.ID] {
			if _, ok := mlv.extra[env.ID]; !ok {
				mlv.extraOrder = append(mlv.extraOrder, env.ID)
			}
			mlv.extra[env.ID] = env
		}
	}
	mlv.Redraw()
	mlv.marksChanged()
}

// dropExtra forgets the marks of messages that are not loaded.
func (mlv *MailListView) dropExtra() {
	for id := range mlv.extra {
		delete(mlv.marked, id)
	}
	mlv.extra = map[string]*mail.Envelope{}
	mlv.extraOrder = nil
	mlv.allMarked = false
}

// Unmark drops the marks of the messages with ids.
func (mlv *MailListView) Unmark(ids ...string) {
	changed := false
	mlv.allMarked = false
	for _, id := range ids {
		if mlv.marked[id] {
			delete(mlv.marked, id)
			changed = true
		}
		delete(mlv.extra, id)
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
	mlv.extra = map[string]*mail.Envelope{}
	mlv.extraOrder = nil
	mlv.allMarked = false
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
		if _, unloaded := mlv.extra[id]; !present[id] && !unloaded {
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
	mlv.extra = map[string]*mail.Envelope{}
	mlv.extraOrder = nil
	mlv.allMarked = false
	mlv.rowsFor = ""
	mlv.total, mlv.unread = 0, 0
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
//
// For a listed folder the rows already are the matches (the engine filters
// the whole folder): the text is kept for the title, and the new rows
// follow. A changed filter drops the marks of messages not loaded.
func (mlv *MailListView) SetFilter(query string) {
	if query != mlv.filter {
		mlv.dropExtra()
	}
	mlv.filter = query
	mlv.renderEnvelopes(mlv.filtered(query))
}

// SetListed says the rows come from the engine's Lister: filtered over the
// whole folder and loaded a page at a time.
func (mlv *MailListView) SetListed(on bool) { mlv.listed = on }

// SetRowsFor says which filter text the next rows answer (listed mode):
// they are shown as they come, without narrowing them again.
func (mlv *MailListView) SetRowsFor(filter string) { mlv.rowsFor = filter }

// AllMarked reports whether the marks are what MarkAll (A) gave.
func (mlv *MailListView) AllMarked() bool { return mlv.allMarked && len(mlv.marked) > 0 }

// Listed reports whether the rows come from the engine's Lister.
func (mlv *MailListView) Listed() bool { return mlv.listed }

// SetTotals gives the title the folder's count (or, with a filter, how many
// match) and the folder's unread count.
func (mlv *MailListView) SetTotals(total, unread int) {
	mlv.total, mlv.unread = total, unread
}

// Total is the count SetTotals gave: the folder's, or the filter's matches.
func (mlv *MailListView) Total() int { return mlv.total }

// LoadedCount is how many rows are loaded.
func (mlv *MailListView) LoadedCount() int { return len(mlv.allEnvelopes) }

// SetNearEndFunc registers fn, called when the cursor reaches the last row.
func (mlv *MailListView) SetNearEndFunc(fn func()) { mlv.nearEnd = fn }

// AppendEnvelopes adds the next page of rows below; rows already loaded are
// skipped. The cursor and the marks stay.
func (mlv *MailListView) AppendEnvelopes(envelopes []*mail.Envelope) {
	have := make(map[string]bool, len(mlv.allEnvelopes))
	for _, env := range mlv.allEnvelopes {
		have[env.ID] = true
	}
	added := false
	for _, env := range envelopes {
		if !have[env.ID] {
			have[env.ID] = true
			mlv.allEnvelopes = append(mlv.allEnvelopes, env)
			added = true
		}
	}
	if !added {
		return
	}
	mlv.allEnvelopes = mail.NewestFirst(mlv.allEnvelopes)
	// Rows sort by date, so a new one may land above the cursor: keep the
	// cursor on the message, not the row number.
	row, _ := mlv.GetSelection()
	var at string
	if row >= 0 && row < len(mlv.visible) {
		at = mlv.visible[row].ID
	}
	visible := mlv.filtered(mlv.filter)
	for i, env := range visible {
		if env.ID == at {
			row = i
			break
		}
	}
	mlv.renderEnvelopesAt(visible, row)
}

// filtered returns the envelopes that match query ("" matches all).
func (mlv *MailListView) filtered(query string) []*mail.Envelope {
	if query == "" || (mlv.listed && mlv.rowsFor == query) {
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

// SetConversations says whether the rows are conversations (Gmail), which
// the title's count names.
func (mlv *MailListView) SetConversations(on bool) { mlv.conversations = on }

// TitleText returns the pane title: what the list shows (the folder, a
// filter, a server search), how many rows of how many, the folder's unread
// count, the selection and what a search is doing. A filter and a search
// share one layout:
//
//	MAIL LIST [50 of 2,431 conversations · 131 unread · 3 selected]
//	MAIL LIST: filter /linkedin [50 of 312 · 4 selected]
//	MAIL LIST: Gmail search \linkedin [searching… ~20–30 s]
//	MAIL LIST: Gmail search \linkedin [50 of ~1,240 · loading more… ~20–30 s]
//	MAIL LIST: Outlook search \linkedin [50 of 50+]
func (mlv *MailListView) TitleText() string {
	switch {
	case mlv.search != "":
		kind := mlv.searchKind
		if kind == "" {
			kind = "server"
		}
		head := "MAIL LIST: " + kind + " search \\" + mlv.search
		if mlv.filter != "" {
			head += " /" + mlv.filter
		}
		return head + " [" + mlv.searchCount() + "]"
	case mlv.filter != "":
		return "MAIL LIST: filter /" + mlv.filter + " [" + mlv.withSelected(mlv.of(len(mlv.visible))) + "]"
	}
	shown := len(mlv.visible)
	if shown == 0 {
		return "MAIL LIST"
	}
	noun := "message"
	if mlv.conversations {
		noun = "conversation"
	}
	if max(shown, mlv.total) != 1 {
		noun += "s"
	}
	count := mlv.of(shown) + " " + noun
	if mlv.listed && mlv.unread > 0 {
		count += " · " + thousands(mlv.unread) + " unread"
	}
	return "MAIL LIST [" + mlv.withSelected(count) + "]"
}

// of is "50 of 2,431" when the folder (or the filter) has more than n,
// else "50".
func (mlv *MailListView) of(n int) string {
	if mlv.listed && mlv.total > n {
		return thousands(n) + " of " + thousands(mlv.total)
	}
	return thousands(n)
}

// withSelected adds the selection count, if any.
func (mlv *MailListView) withSelected(count string) string {
	if n := len(mlv.Marked()); n > 0 {
		count += " · " + thousands(n) + " selected"
	}
	return count
}

// searchCount is a server search's count: "searching… ~20–30 s" while the
// first page is on its way, then "50 of ~1,240" (Gmail's estimate), "50 of
// 87" (IMAP) or "50 of 50+" (Outlook, more to come), then what is loading.
func (mlv *MailListView) searchCount() string {
	if mlv.searchBusy != "" && mlv.searchPending {
		return mlv.searchBusy
	}
	shown, loaded := len(mlv.visible), len(mlv.allEnvelopes)
	count := thousands(shown)
	switch {
	case mlv.searchTotal > loaded:
		total := thousands(mlv.searchTotal)
		if mlv.searchEstimate {
			total = "~" + total
		}
		count += " of " + total
	case mlv.searchMore:
		count += " of " + thousands(loaded) + "+"
	}
	count = mlv.withSelected(count)
	if mlv.searchBusy != "" {
		count += " · " + mlv.searchBusy
	}
	return count
}

// SetSearchInfo gives the title a server search's kind ("Gmail", "IMAP",
// "Outlook"), how many match in all (0: not said), whether that is an
// estimate, and whether more pages follow.
func (mlv *MailListView) SetSearchInfo(kind string, total int, estimate, more bool) {
	mlv.searchKind, mlv.searchTotal, mlv.searchEstimate, mlv.searchMore = kind, total, estimate, more
}

// SetSearchBusy says in the title what the search is doing ("" for
// nothing); pending: its first page is not here yet, so the rows on screen
// are not its answer.
func (mlv *MailListView) SetSearchBusy(text string, pending bool) {
	mlv.searchBusy, mlv.searchPending = text, pending
}

// thousands writes n with a comma between each group of three digits.
func thousands(n int) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return s
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// ToggleMarkAll marks every message on screen (a filter's matches only),
// or, when all of them are marked already, unmarks them.
func (mlv *MailListView) ToggleMarkAll() {
	if len(mlv.visible) == 0 {
		return
	}
	all := true
	for _, env := range mlv.visible {
		if !mlv.marked[env.ID] {
			all = false
			break
		}
	}
	for _, env := range mlv.visible {
		if all {
			delete(mlv.marked, env.ID)
		} else {
			mlv.marked[env.ID] = true
		}
	}
	mlv.Redraw()
	mlv.marksChanged()
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
	mlv.marksChanged() // the title counts the rows
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
