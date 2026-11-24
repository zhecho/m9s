package graphtest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	netmail "net/mail"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zhecho/m9s/internal/mail"
)

// Message is one message of the fake mailbox. FolderID is set by the fake.
type Message struct {
	ID, FolderID      string
	ConversationID    string
	InternetMessageID string // with angle brackets, as Graph sends it
	Subject           string
	From              mail.Address
	To, CC            []mail.Address
	Received          time.Time
	IsRead            bool
	Flagged           bool
	HasAttachments    bool
	Categories        []string
	Raw               []byte

	seq int // change number of the last change
}

type folder struct {
	id, parent, name, wellKnown string
	hidden                      bool // never listed, reachable by id or well-known name
}

type removal struct {
	seq int
	id  string
}

// mailbox is the fake's in-memory model; every field is guarded by Fake.mu.
type mailbox struct {
	folders  []*folder
	msgs     map[string]*Message
	removals map[string][]removal // folder id -> messages that left it
	expired  map[string]bool      // folder id -> next delta link answers 410
	seq      int
	nextID   int
	nextDate time.Time
	pageSize int
	mail     string
	upn      string
	cats     []string
	sent     [][]byte
}

const rootID = "msgfolderroot"

var wellKnownFolders = []struct{ wk, name string }{
	{"inbox", "Inbox"}, {"sentitems", "Sent Items"}, {"drafts", "Drafts"},
	{"deleteditems", "Deleted Items"}, {"junkemail", "Junk Email"}, {"archive", "Archive"},
}

func newMailbox() *mailbox {
	mb := &mailbox{
		msgs:     map[string]*Message{},
		removals: map[string][]removal{},
		expired:  map[string]bool{},
		nextDate: time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC),
		mail:     "me@outlook.com",
		upn:      "me@outlook.com",
	}
	for _, w := range wellKnownFolders {
		mb.addFolder("", w.name, w.wk)
	}
	// Where permanentDelete puts messages, as Exchange does.
	purges := mb.addFolder("", "Purges", "recoverableitemspurges")
	mb.folder(purges).hidden = true
	return mb
}

func (mb *mailbox) newID(prefix string) string {
	mb.nextID++
	return fmt.Sprintf("%s-%04d", prefix, mb.nextID)
}

func (mb *mailbox) addFolder(parentID, name, wellKnown string) string {
	if wellKnown != "" {
		mb.folders = slices.DeleteFunc(mb.folders, func(f *folder) bool { return f.wellKnown == wellKnown })
	}
	id := mb.newID("fld")
	mb.folders = append(mb.folders, &folder{id: id, parent: parentID, name: name, wellKnown: wellKnown})
	return id
}

// folder finds a folder by id or well-known name.
func (mb *mailbox) folder(key string) *folder {
	for _, f := range mb.folders {
		if f.id == key || (f.wellKnown != "" && strings.EqualFold(f.wellKnown, key)) {
			return f
		}
	}
	return nil
}

func (mb *mailbox) bump(m *Message) {
	mb.seq++
	m.seq = mb.seq
}

func (mb *mailbox) leave(m *Message) {
	mb.seq++
	mb.removals[m.FolderID] = append(mb.removals[m.FolderID], removal{mb.seq, m.ID})
}

func (mb *mailbox) add(folderID string, m Message) string {
	if m.ID == "" {
		m.ID = mb.newID("msg")
	}
	if m.Received.IsZero() {
		mb.nextDate = mb.nextDate.Add(time.Minute)
		m.Received = mb.nextDate
	}
	if m.ConversationID == "" {
		m.ConversationID = "conv-" + m.ID
	}
	if m.InternetMessageID == "" {
		m.InternetMessageID = "<" + m.ID + "@fake.example>"
	}
	m.FolderID = folderID
	m.To, m.CC = slices.Clone(m.To), slices.Clone(m.CC)
	m.Categories, m.Raw = slices.Clone(m.Categories), slices.Clone(m.Raw)
	mb.msgs[m.ID] = &m
	mb.bump(&m)
	return m.ID
}

// --- test helpers ---

// SetMe sets what /me answers: its mail and userPrincipalName.
func (f *Fake) SetMe(mail, upn string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mb.mail, f.mb.upn = mail, upn
}

// SetPageSize caps every page (folders, listings, delta) at n items; 0 lifts it.
func (f *Fake) SetPageSize(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mb.pageSize = n
}

// FolderID returns the id of a well-known folder ("inbox", "archive", …), "" if none.
func (f *Fake) FolderID(wellKnown string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, fl := range f.mb.folders {
		if fl.wellKnown == wellKnown {
			return fl.id
		}
	}
	return ""
}

// AddFolder adds a folder under parentID ("" = top level) and returns its
// id. A non-empty wellKnown replaces the folder that had that name.
func (f *Fake) AddFolder(parentID, name, wellKnown string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mb.addFolder(parentID, name, wellKnown)
}

// SetFolderName renames a folder (e.g. to a localized name).
func (f *Fake) SetFolderName(id, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fl := f.mb.folder(id); fl != nil {
		fl.name = name
	}
}

// RemoveFolder deletes a folder and the messages in it.
func (f *Fake) RemoveFolder(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mb.folders = slices.DeleteFunc(f.mb.folders, func(fl *folder) bool { return fl.id == id })
	for mid, m := range f.mb.msgs {
		if m.FolderID == id {
			delete(f.mb.msgs, mid)
		}
	}
}

// AddMessage stores m in folderID as newly arrived mail and returns its id.
// Empty ID, Received, ConversationID and InternetMessageID are filled in.
func (f *Fake) AddMessage(folderID string, m Message) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mb.add(folderID, m)
}

// Move moves a message to another folder, keeping its id (immutable ids).
func (f *Fake) Move(id, folderID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.mb.msgs[id]; m != nil {
		f.mb.move(m, folderID)
	}
}

func (mb *mailbox) move(m *Message, folderID string) {
	if m.FolderID == folderID {
		return
	}
	mb.leave(m)
	m.FolderID = folderID
	mb.bump(m)
}

// Delete removes a message for good.
func (f *Fake) Delete(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.mb.msgs[id]; m != nil {
		f.mb.leave(m)
		delete(f.mb.msgs, id)
	}
}

// Update changes a message on the server; delta reports it as changed.
func (f *Fake) Update(id string, fn func(*Message)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.mb.msgs[id]; m != nil {
		fn(m)
		f.mb.bump(m)
	}
}

// Message returns a copy of a stored message.
func (f *Fake) Message(id string) (Message, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.mb.msgs[id]
	if m == nil {
		return Message{}, false
	}
	c := *m
	c.Categories, c.Raw = slices.Clone(m.Categories), slices.Clone(m.Raw)
	return c, true
}

// Sent returns the raw messages handed to sendMail.
func (f *Fake) Sent() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.mb.sent)
}

// SetMasterCategories sets the account's category list.
func (f *Fake) SetMasterCategories(names ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mb.cats = slices.Clone(names)
}

// ExpireDelta makes the next delta link of folderID answer 410 Gone.
func (f *Fake) ExpireDelta(folderID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mb.expired[folderID] = true
}

// --- HTTP ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": "fake: " + code}})
}

func addrJSON(a mail.Address) map[string]any {
	return map[string]any{"emailAddress": map[string]string{"name": a.Name, "address": a.Email}}
}

func addrsJSON(as []mail.Address) []any {
	out := []any{}
	for _, a := range as {
		out = append(out, addrJSON(a))
	}
	return out
}

func msgJSON(m *Message) map[string]any {
	flag := "notFlagged"
	if m.Flagged {
		flag = "flagged"
	}
	cats := m.Categories
	if cats == nil {
		cats = []string{}
	}
	out := map[string]any{
		"id": m.ID, "parentFolderId": m.FolderID, "conversationId": m.ConversationID,
		"internetMessageId": m.InternetMessageID, "subject": m.Subject,
		"toRecipients": addrsJSON(m.To), "ccRecipients": addrsJSON(m.CC),
		"receivedDateTime": m.Received.UTC().Format(time.RFC3339),
		"isRead":           m.IsRead, "flag": map[string]string{"flagStatus": flag},
		"hasAttachments": m.HasAttachments, "categories": cats,
	}
	if m.From.Email != "" {
		out["from"] = addrJSON(m.From)
	}
	return out
}

func (mb *mailbox) folderJSON(fl *folder) map[string]any {
	children, total, unread := 0, 0, 0
	for _, c := range mb.folders {
		if c.parent == fl.id {
			children++
		}
	}
	for _, m := range mb.msgs {
		if m.FolderID == fl.id {
			total++
			if !m.IsRead {
				unread++
			}
		}
	}
	parent := fl.parent
	if parent == "" {
		parent = rootID
	}
	return map[string]any{
		"id": fl.id, "displayName": fl.name, "parentFolderId": parent,
		"childFolderCount": children, "totalItemCount": total, "unreadItemCount": unread,
	}
}

// pageOf answers one page of items: $top and $skip from the query, capped
// by SetPageSize; nextLink repeats the request with the next $skip.
func (f *Fake) pageOf(w http.ResponseWriter, r *http.Request, items []any) {
	q := r.URL.Query()
	size, _ := strconv.Atoi(q.Get("$top"))
	if size <= 0 {
		size = 10
	}
	f.mu.Lock()
	if f.mb.pageSize > 0 && f.mb.pageSize < size {
		size = f.mb.pageSize
	}
	f.mu.Unlock()
	skip, _ := strconv.Atoi(q.Get("$skip"))
	skip = min(skip, len(items))
	end := min(skip+size, len(items))
	out := map[string]any{"value": items[skip:end]}
	if end < len(items) {
		q.Set("$skip", strconv.Itoa(end))
		out["@odata.nextLink"] = "http://" + r.Host + r.URL.Path + "?" + q.Encode()
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *Fake) mailboxHandler(w http.ResponseWriter, r *http.Request, body []byte) {
	path := strings.TrimPrefix(r.URL.Path, "/v1.0")
	if path == r.URL.Path {
		writeErr(w, http.StatusNotFound, "UnknownVersion")
		return
	}
	seg := strings.Split(strings.Trim(path, "/"), "/")
	if len(seg) == 0 || seg[0] != "me" {
		writeErr(w, http.StatusNotFound, "ResourceNotFound")
		return
	}
	seg = seg[1:]
	key := func(i int) string {
		if i < len(seg) {
			return seg[i]
		}
		return ""
	}
	switch {
	case len(seg) == 0 && r.Method == http.MethodGet:
		f.mu.Lock()
		out := map[string]any{"userPrincipalName": f.mb.upn, "mail": nil}
		if f.mb.mail != "" {
			out["mail"] = f.mb.mail
		}
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, out)
	case key(0) == "mailFolders":
		f.folders(w, r, seg[1:])
	case key(0) == "messages":
		f.messages(w, r, seg[1:], body)
	case key(0) == "sendMail" && len(seg) == 1 && r.Method == http.MethodPost:
		raw, ok := mimeBody(w, r, body)
		if !ok {
			return
		}
		f.mu.Lock()
		f.mb.sent = append(f.mb.sent, raw)
		if sent := f.mb.folder("sentitems"); sent != nil {
			f.mb.add(sent.id, Message{Subject: subjectOf(raw), IsRead: true, Raw: raw})
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	case key(0) == "outlook" && key(1) == "masterCategories" && r.Method == http.MethodGet:
		f.mu.Lock()
		items := []any{}
		for i, c := range f.mb.cats {
			items = append(items, map[string]any{"id": "cat-" + strconv.Itoa(i), "displayName": c, "color": "preset0"})
		}
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"value": items})
	default:
		writeErr(w, http.StatusNotFound, "ResourceNotFound")
	}
}

func (f *Fake) folders(w http.ResponseWriter, r *http.Request, seg []string) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "MethodNotAllowed")
		return
	}
	f.mu.Lock()
	if len(seg) == 0 {
		items := []any{}
		for _, fl := range f.mb.folders {
			if fl.parent == "" && !fl.hidden {
				items = append(items, f.mb.folderJSON(fl))
			}
		}
		f.mu.Unlock()
		f.pageOf(w, r, items)
		return
	}
	fl := f.mb.folder(seg[0])
	if fl == nil {
		f.mu.Unlock()
		writeErr(w, http.StatusNotFound, "ErrorFolderNotFound")
		return
	}
	switch {
	case len(seg) == 1:
		out := f.mb.folderJSON(fl)
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, out)
	case len(seg) == 2 && seg[1] == "childFolders":
		items := []any{}
		for _, c := range f.mb.folders {
			if c.parent == fl.id && !c.hidden {
				items = append(items, f.mb.folderJSON(c))
			}
		}
		f.mu.Unlock()
		f.pageOf(w, r, items)
	case len(seg) == 2 && seg[1] == "messages":
		items := []any{}
		for _, m := range f.mb.sorted(fl.id) {
			items = append(items, msgJSON(m))
		}
		f.mu.Unlock()
		f.pageOf(w, r, items)
	case len(seg) == 3 && seg[1] == "messages" && seg[2] == "delta":
		f.mu.Unlock()
		f.delta(w, r, fl.id)
	default:
		f.mu.Unlock()
		writeErr(w, http.StatusNotFound, "ResourceNotFound")
	}
}

// sorted returns a folder's messages, newest first; f.mu must be held.
func (mb *mailbox) sorted(folderID string) []*Message {
	var out []*Message
	for _, m := range mb.msgs {
		if folderID == "" || m.FolderID == folderID {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Received.Equal(out[j].Received) {
			return out[i].Received.After(out[j].Received)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// delta answers a messages/delta round. Links carry the change number the
// round starts after ($deltatoken=T); a page link also carries the number
// the round ends at and the offset ($skiptoken=T.U.K). A first round
// (no token) lists the folder and reports no removals.
func (f *Fake) delta(w http.ResponseWriter, r *http.Request, folderID string) {
	q := r.URL.Query()
	since, upto, skip, hasToken := 0, -1, 0, false
	if t := q.Get("$deltatoken"); t != "" {
		since, _ = strconv.Atoi(t)
		hasToken = true
	}
	if t := q.Get("$skiptoken"); t != "" {
		p := strings.Split(t, ".")
		if len(p) != 3 {
			writeErr(w, http.StatusBadRequest, "BadSkipToken")
			return
		}
		since, _ = strconv.Atoi(p[0])
		upto, _ = strconv.Atoi(p[1])
		skip, _ = strconv.Atoi(p[2])
		hasToken = true
	}
	size := 10
	for part := range strings.SplitSeq(r.Header.Get("Prefer"), ",") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(part), "odata.maxpagesize="); ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				size = n
			}
		}
	}

	f.mu.Lock()
	if hasToken && f.mb.expired[folderID] {
		delete(f.mb.expired, folderID)
		f.mu.Unlock()
		writeErr(w, http.StatusGone, "SyncStateNotFound")
		return
	}
	if f.mb.pageSize > 0 && f.mb.pageSize < size {
		size = f.mb.pageSize
	}
	if upto < 0 {
		upto = f.mb.seq
	}
	var changed []*Message
	for _, m := range f.mb.msgs {
		if m.FolderID == folderID && m.seq > since && m.seq <= upto {
			changed = append(changed, m)
		}
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].seq < changed[j].seq })
	items := []any{}
	for _, m := range changed {
		items = append(items, msgJSON(m))
	}
	if since > 0 {
		seen := map[string]bool{}
		for _, rm := range f.mb.removals[folderID] {
			if rm.seq <= since || rm.seq > upto || seen[rm.id] {
				continue
			}
			if m := f.mb.msgs[rm.id]; m != nil && m.FolderID == folderID {
				continue // it came back
			}
			seen[rm.id] = true
			items = append(items, map[string]any{"id": rm.id, "@removed": map[string]string{"reason": "deleted"}})
		}
	}
	f.mu.Unlock()

	skip = min(skip, len(items))
	end := min(skip+size, len(items))
	out := map[string]any{"value": items[skip:end]}
	base := "http://" + r.Host + r.URL.Path + "?"
	if end < len(items) {
		out["@odata.nextLink"] = base + "$skiptoken=" + fmt.Sprintf("%d.%d.%d", since, upto, end)
	} else {
		out["@odata.deltaLink"] = base + "$deltatoken=" + strconv.Itoa(upto)
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *Fake) messages(w http.ResponseWriter, r *http.Request, seg []string, body []byte) {
	if len(seg) == 0 {
		switch r.Method {
		case http.MethodGet:
			f.search(w, r)
		case http.MethodPost:
			raw, ok := mimeBody(w, r, body)
			if !ok {
				return
			}
			f.mu.Lock()
			drafts := f.mb.folder("drafts")
			if drafts == nil {
				f.mu.Unlock()
				writeErr(w, http.StatusNotFound, "ErrorFolderNotFound")
				return
			}
			id := f.mb.add(drafts.id, Message{Subject: subjectOf(raw), IsRead: true, Raw: raw})
			out := msgJSON(f.mb.msgs[id])
			f.mu.Unlock()
			writeJSON(w, http.StatusCreated, out)
		default:
			writeErr(w, http.StatusMethodNotAllowed, "MethodNotAllowed")
		}
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.mb.msgs[seg[0]]
	if m == nil {
		writeErr(w, http.StatusNotFound, "ErrorItemNotFound")
		return
	}
	switch {
	case len(seg) == 1 && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, msgJSON(m))
	case len(seg) == 1 && r.Method == http.MethodPatch:
		var in struct {
			IsRead *bool `json:"isRead"`
			Flag   *struct {
				FlagStatus string `json:"flagStatus"`
			} `json:"flag"`
			Categories *[]string `json:"categories"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			writeErr(w, http.StatusBadRequest, "BadRequest")
			return
		}
		if in.IsRead != nil {
			m.IsRead = *in.IsRead
		}
		if in.Flag != nil {
			m.Flagged = in.Flag.FlagStatus == "flagged"
		}
		if in.Categories != nil {
			m.Categories = slices.Clone(*in.Categories)
		}
		f.mb.bump(m)
		writeJSON(w, http.StatusOK, msgJSON(m))
	case len(seg) == 2 && seg[1] == "$value" && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(m.Raw)
	case len(seg) == 2 && seg[1] == "move" && r.Method == http.MethodPost:
		var in struct {
			DestinationID string `json:"destinationId"`
		}
		_ = json.Unmarshal(body, &in)
		dst := f.mb.folder(in.DestinationID)
		if dst == nil {
			writeErr(w, http.StatusNotFound, "ErrorFolderNotFound")
			return
		}
		f.mb.move(m, dst.id)
		writeJSON(w, http.StatusCreated, msgJSON(m))
	case len(seg) == 2 && seg[1] == "permanentDelete" && r.Method == http.MethodPost:
		// Exchange keeps the message in the hidden Purges folder for a
		// while; a GET by id still finds it there.
		purges := f.mb.folder("recoverableitemspurges")
		switch {
		case purges != nil && m.FolderID == purges.id:
			writeErr(w, http.StatusForbidden, "ErrorCannotDeleteObject")
			return
		case purges != nil:
			f.mb.move(m, purges.id)
		default:
			f.mb.leave(m)
			delete(f.mb.msgs, m.ID)
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeErr(w, http.StatusNotFound, "ResourceNotFound")
	}
}

// search matches the $search text (field prefixes and quotes dropped)
// against subjects and senders, case-insensitively.
func (f *Fake) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("$search")
	if q == "" {
		writeErr(w, http.StatusBadRequest, "SearchRequired")
		return
	}
	q = strings.TrimSuffix(strings.TrimPrefix(q, `"`), `"`)
	q = strings.NewReplacer(`\"`, ``, `\\`, `\`, `"`, ``).Replace(q)
	if _, after, ok := strings.Cut(q, ":"); ok {
		q = after
	}
	q = strings.ToLower(strings.TrimSpace(q))
	f.mu.Lock()
	items := []any{}
	for _, m := range f.mb.sorted("") {
		if strings.Contains(strings.ToLower(m.Subject), q) || strings.Contains(strings.ToLower(m.From.Email), q) {
			items = append(items, msgJSON(m))
		}
	}
	f.mu.Unlock()
	f.pageOf(w, r, items)
}

// mimeBody decodes a base64 MIME request body; Graph wants text/plain.
func mimeBody(w http.ResponseWriter, r *http.Request, body []byte) ([]byte, bool) {
	if ct := r.Header.Get("Content-Type"); ct != "text/plain" {
		writeErr(w, http.StatusBadRequest, "fake supports MIME bodies only, got "+ct)
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(string(body))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "ErrorMimeContentInvalidBase64String")
		return nil, false
	}
	return raw, true
}

func subjectOf(raw []byte) string {
	m, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	return m.Header.Get("Subject")
}

// batch runs each request of a JSON $batch through the queues and the
// mailbox and answers with their results.
func (f *Fake) batch(w http.ResponseWriter, r *http.Request, body []byte) {
	var in struct {
		Requests []struct {
			ID      string            `json:"id"`
			Method  string            `json:"method"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Body    json.RawMessage   `json:"body"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(body, &in); err != nil || len(in.Requests) == 0 || len(in.Requests) > 20 {
		writeErr(w, http.StatusBadRequest, "BadBatch")
		return
	}
	out := []any{}
	for _, sub := range in.Requests {
		u, err := url.Parse(sub.URL)
		if err != nil || !strings.HasPrefix(sub.URL, "/") {
			out = append(out, map[string]any{"id": sub.ID, "status": http.StatusBadRequest})
			continue
		}
		req := httptest.NewRequest(sub.Method, "http://"+r.Host+"/v1.0"+u.String(), bytes.NewReader(sub.Body))
		for k, v := range sub.Headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		f.mu.Lock()
		q := f.popQueue(req.URL.Path)
		f.mu.Unlock()
		if q != nil {
			q.write(rec)
		} else {
			f.mailboxHandler(rec, req, sub.Body)
		}
		res := map[string]any{"id": sub.ID, "status": rec.Code}
		hdr := map[string]string{}
		for k := range rec.Header() {
			hdr[k] = rec.Header().Get(k)
		}
		res["headers"] = hdr
		if b := bytes.TrimSpace(rec.Body.Bytes()); len(b) > 0 && json.Valid(b) {
			res["body"] = json.RawMessage(b)
		}
		out = append(out, res)
	}
	writeJSON(w, http.StatusOK, map[string]any{"responses": out})
}
