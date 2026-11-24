package gmailtest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Label is one label of the fake mailbox.
type Label struct {
	ID, Name, Type string // Type is "system" or "user"
}

// Message is one message of the fake mailbox.
type Message struct {
	ID, ThreadID string
	LabelIDs     []string
	InternalDate int64 // ms since the epoch
	Raw          []byte
}

type histRec struct {
	id     int
	kind   string // messageAdded, messageDeleted, labelAdded, labelRemoved
	msg    Message
	labels []string
}

// mailbox is the fake's in-memory model; every field is guarded by Fake.mu.
type mailbox struct {
	labels   []Label
	msgs     map[string]*Message
	drafts   map[string]string // draft id -> message id
	hist     []histRec
	histMin  int // history.list from an older start answers 404
	nextID   int
	nextDate int64
}

const apiPrefix = "/gmail/v1/users/me"

var defaultLabels = []Label{
	{"INBOX", "INBOX", "system"}, {"SENT", "SENT", "system"}, {"DRAFT", "DRAFT", "system"},
	{"TRASH", "TRASH", "system"}, {"SPAM", "SPAM", "system"}, {"STARRED", "STARRED", "system"},
	{"UNREAD", "UNREAD", "system"}, {"IMPORTANT", "IMPORTANT", "system"}, {"CHAT", "CHAT", "system"},
	{"CATEGORY_PERSONAL", "CATEGORY_PERSONAL", "system"},
}

func newMailbox() *mailbox {
	return &mailbox{
		labels:   slices.Clone(defaultLabels),
		msgs:     map[string]*Message{},
		drafts:   map[string]string{},
		nextDate: 1_700_000_000_000,
	}
}

// SetPageSizes sets how many ids one messages.list page holds and how many
// records one history.list page holds (0: the default of 100).
func (f *Fake) SetPageSizes(messages, history int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pageSize, f.histPageSize = messages, history
}

// bump advances the mailbox history id; f.mu must be held.
func (f *Fake) bump() int {
	n, _ := strconv.Atoi(f.History)
	n++
	f.History = strconv.Itoa(n)
	return n
}

func (f *Fake) record(kind string, m *Message, labels []string) {
	n := f.bump()
	f.mb.hist = append(f.mb.hist, histRec{id: n, kind: kind, msg: copyMsg(m), labels: slices.Clone(labels)})
}

func copyMsg(m *Message) Message {
	c := *m
	c.LabelIDs = slices.Clone(m.LabelIDs)
	c.Raw = slices.Clone(m.Raw)
	return c
}

// AddLabel adds a user label (or replaces one with the same id).
func (f *Fake) AddLabel(id, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mb.labels = slices.DeleteFunc(f.mb.labels, func(l Label) bool { return l.ID == id })
	f.mb.labels = append(f.mb.labels, Label{id, name, "user"})
}

// AddMessage stores m as newly arrived mail and returns its id. Empty ID,
// ThreadID and InternalDate are filled in.
func (f *Fake) AddMessage(m Message) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addLocked(m)
}

func (f *Fake) addLocked(m Message) string {
	f.mb.nextID++
	if m.ID == "" {
		m.ID = fmt.Sprintf("m%04d", f.mb.nextID)
	}
	if m.ThreadID == "" {
		m.ThreadID = "t" + m.ID
	}
	if m.InternalDate == 0 {
		f.mb.nextDate += 1000
		m.InternalDate = f.mb.nextDate
	}
	m.LabelIDs = slices.Clone(m.LabelIDs)
	f.mb.msgs[m.ID] = &m
	f.record("messageAdded", &m, nil)
	return m.ID
}

// ModifyLabels adds and removes labels on a message as Gmail web would.
func (f *Fake) ModifyLabels(id string, add, remove []string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.modifyLocked(id, add, remove) != nil
}

func (f *Fake) modifyLocked(id string, add, remove []string) *Message {
	m := f.mb.msgs[id]
	if m == nil {
		return nil
	}
	var added, removed []string
	for _, l := range add {
		if !slices.Contains(m.LabelIDs, l) {
			m.LabelIDs = append(m.LabelIDs, l)
			added = append(added, l)
		}
	}
	for _, l := range remove {
		if i := slices.Index(m.LabelIDs, l); i >= 0 {
			m.LabelIDs = slices.Delete(m.LabelIDs, i, i+1)
			removed = append(removed, l)
		}
	}
	if len(added) > 0 {
		f.record("labelAdded", m, added)
	}
	if len(removed) > 0 {
		f.record("labelRemoved", m, removed)
	}
	return m
}

// RemoveMessage permanently deletes a message.
func (f *Fake) RemoveMessage(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.removeLocked(id)
}

func (f *Fake) removeLocked(id string) bool {
	m := f.mb.msgs[id]
	if m == nil {
		return false
	}
	delete(f.mb.msgs, id)
	f.record("messageDeleted", m, nil)
	return true
}

// Message returns a copy of a stored message.
func (f *Fake) Message(id string) (Message, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.mb.msgs[id]
	if m == nil {
		return Message{}, false
	}
	return copyMsg(m), true
}

// MessageIDs returns the ids of every message carrying label ("" = all).
func (f *Fake) MessageIDs(label string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id, m := range f.mb.msgs {
		if label == "" || slices.Contains(m.LabelIDs, label) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// Drafts returns draft id -> message id.
func (f *Fake) Drafts() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(f.mb.drafts))
	for k, v := range f.mb.drafts {
		out[k] = v
	}
	return out
}

// ExpireHistory forgets every history record, as Gmail does after about a
// week: history.list from any earlier history id now answers 404.
func (f *Fake) ExpireHistory() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mb.hist = nil
	f.mb.histMin = f.bump()
}

// --- HTTP side ---

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	fmt.Fprint(w, `{"error":{"code":404,"status":"NOT_FOUND"}}`)
}

func badRequest(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprint(w, `{"error":{"code":400,"status":"INVALID_ARGUMENT"}}`)
}

type msgRef struct {
	ID       string   `json:"id"`
	ThreadID string   `json:"threadId"`
	LabelIDs []string `json:"labelIds,omitempty"`
}

func ref(m *Message) msgRef { return msgRef{m.ID, m.ThreadID, slices.Clone(m.LabelIDs)} }

// decodeRaw accepts base64url with or without padding.
func decodeRaw(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// mailboxHandler serves every API path that has no handler of its own.
func (f *Fake) mailboxHandler(w http.ResponseWriter, r *http.Request, body []byte) {
	path, ok := strings.CutPrefix(r.URL.Path, apiPrefix)
	if !ok {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && len(parts) == 1 && parts[0] == "labels":
		f.listLabels(w)
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "labels":
		f.getLabel(w, parts[1])
	case r.Method == http.MethodGet && len(parts) == 1 && parts[0] == "messages":
		f.listMessages(w, r)
	case r.Method == http.MethodGet && len(parts) == 1 && parts[0] == "history":
		f.listHistory(w, r)
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "messages":
		f.getMessage(w, r, parts[1])
	case r.Method == http.MethodPost && len(parts) == 2 && parts[0] == "messages" && parts[1] == "send":
		f.send(w, body)
	case r.Method == http.MethodPost && len(parts) == 2 && parts[0] == "messages" && parts[1] == "batchDelete":
		var in struct{ IDs []string }
		if json.Unmarshal(body, &in) != nil {
			badRequest(w)
			return
		}
		for _, id := range in.IDs {
			f.removeLocked(id)
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "messages" && parts[2] == "modify":
		var in struct{ AddLabelIDs, RemoveLabelIDs []string }
		if json.Unmarshal(body, &in) != nil {
			badRequest(w)
			return
		}
		m := f.modifyLocked(parts[1], in.AddLabelIDs, in.RemoveLabelIDs)
		if m == nil {
			notFound(w)
			return
		}
		writeJSON(w, ref(m))
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "messages" && parts[2] == "trash":
		m := f.modifyLocked(parts[1], []string{"TRASH"}, []string{"INBOX"})
		if m == nil {
			notFound(w)
			return
		}
		writeJSON(w, ref(m))
	case r.Method == http.MethodDelete && len(parts) == 2 && parts[0] == "messages":
		if !f.removeLocked(parts[1]) {
			notFound(w)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case parts[0] == "drafts":
		f.draftsHandler(w, r, parts, body)
	case r.Method == http.MethodGet && len(parts) == 1 && parts[0] == "threads":
		f.listThreads(w, r)
	case parts[0] == "threads" && len(parts) >= 2:
		f.threadsHandler(w, r, parts, body)
	default:
		http.NotFound(w, r)
	}
}

func (f *Fake) listLabels(w http.ResponseWriter) {
	out := struct {
		Labels []map[string]string `json:"labels"`
	}{}
	for _, l := range f.mb.labels {
		out.Labels = append(out.Labels, map[string]string{"id": l.ID, "name": l.Name, "type": l.Type})
	}
	writeJSON(w, out)
}

func (f *Fake) getLabel(w http.ResponseWriter, id string) {
	i := slices.IndexFunc(f.mb.labels, func(l Label) bool { return l.ID == id })
	if i < 0 {
		notFound(w)
		return
	}
	l := f.mb.labels[i]
	var total, unread int
	threads, unreadThreads := map[string]bool{}, map[string]bool{}
	for _, m := range f.mb.msgs {
		if !listed(m, []string{id}, false) {
			continue
		}
		total++
		threads[m.ThreadID] = true
		if slices.Contains(m.LabelIDs, "UNREAD") {
			unread++
			unreadThreads[m.ThreadID] = true
		}
	}
	writeJSON(w, map[string]any{"id": l.ID, "name": l.Name, "type": l.Type,
		"messagesTotal": total, "messagesUnread": unread,
		"threadsTotal": len(threads), "threadsUnread": len(unreadThreads)})
}

// listed reports whether messages.list with labels returns m: every label
// must be on it, and mail in Trash or Spam is left out unless asked for.
func listed(m *Message, labels []string, spamTrash bool) bool {
	for _, l := range labels {
		if !slices.Contains(m.LabelIDs, l) {
			return false
		}
		if l == "TRASH" || l == "SPAM" {
			spamTrash = true
		}
	}
	if !spamTrash && (slices.Contains(m.LabelIDs, "TRASH") || slices.Contains(m.LabelIDs, "SPAM")) {
		return false
	}
	return true
}

// matches implements the few search operators the tests use.
func matches(m *Message, q string) bool {
	for _, tok := range strings.Fields(q) {
		switch {
		case tok == "is:unread":
			if !slices.Contains(m.LabelIDs, "UNREAD") {
				return false
			}
		case tok == "is:starred":
			if !slices.Contains(m.LabelIDs, "STARRED") {
				return false
			}
		case strings.HasPrefix(tok, "from:"):
			msg, err := mail.ReadMessage(bytes.NewReader(m.Raw))
			if err != nil || !strings.Contains(strings.ToLower(msg.Header.Get("From")), strings.ToLower(strings.TrimPrefix(tok, "from:"))) {
				return false
			}
		case strings.HasPrefix(tok, "rfc822msgid:"):
			want := strings.Trim(strings.TrimPrefix(tok, "rfc822msgid:"), "<>")
			msg, err := mail.ReadMessage(bytes.NewReader(m.Raw))
			if err != nil || strings.Trim(msg.Header.Get("Message-ID"), "<> ") != want {
				return false
			}
		}
	}
	return true
}

func (f *Fake) listMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var all []*Message
	for _, m := range f.mb.msgs {
		if listed(m, q["labelIds"], q.Get("includeSpamTrash") == "true") && matches(m, q.Get("q")) {
			all = append(all, m)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].InternalDate != all[j].InternalDate {
			return all[i].InternalDate > all[j].InternalDate
		}
		return all[i].ID > all[j].ID
	})
	size := f.pageSize
	if size <= 0 {
		size = 100
	}
	if n, err := strconv.Atoi(q.Get("maxResults")); err == nil && n > 0 && n < size {
		size = n
	}
	start, _ := strconv.Atoi(q.Get("pageToken"))
	out := map[string]any{"resultSizeEstimate": len(all)}
	var page []msgRef
	for i := start; i < len(all) && i < start+size; i++ {
		page = append(page, msgRef{ID: all[i].ID, ThreadID: all[i].ThreadID})
	}
	if page != nil {
		out["messages"] = page
	}
	if start+size < len(all) {
		out["nextPageToken"] = strconv.Itoa(start + size)
	}
	writeJSON(w, out)
}

// listThreads answers threads.list: the conversations holding a message
// that messages.list would return for the same query, newest first.
func (f *Fake) listThreads(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	newest := map[string]*Message{}
	for _, m := range f.mb.msgs {
		if !listed(m, q["labelIds"], q.Get("includeSpamTrash") == "true") || !matches(m, q.Get("q")) {
			continue
		}
		if cur := newest[m.ThreadID]; cur == nil || m.InternalDate > cur.InternalDate {
			newest[m.ThreadID] = m
		}
	}
	var all []*Message
	for _, m := range newest {
		all = append(all, m)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].InternalDate != all[j].InternalDate {
			return all[i].InternalDate > all[j].InternalDate
		}
		return all[i].ThreadID > all[j].ThreadID
	})
	size := 100
	if n, err := strconv.Atoi(q.Get("maxResults")); err == nil && n > 0 && n < size {
		size = n
	}
	start, _ := strconv.Atoi(q.Get("pageToken"))
	out := map[string]any{"resultSizeEstimate": len(all)}
	var page []map[string]string
	for i := start; i < len(all) && i < start+size; i++ {
		page = append(page, map[string]string{"id": all[i].ThreadID})
	}
	if page != nil {
		out["threads"] = page
	}
	if start+size < len(all) {
		out["nextPageToken"] = strconv.Itoa(start + size)
	}
	writeJSON(w, out)
}

func (f *Fake) getMessage(w http.ResponseWriter, r *http.Request, id string) {
	m := f.mb.msgs[id]
	if m == nil {
		notFound(w)
		return
	}
	q := r.URL.Query()
	out := map[string]any{"id": m.ID, "threadId": m.ThreadID, "labelIds": m.LabelIDs,
		"internalDate": strconv.FormatInt(m.InternalDate, 10), "sizeEstimate": len(m.Raw)}
	switch q.Get("format") {
	case "raw":
		enc := base64.RawURLEncoding
		if f.PadRaw {
			enc = base64.URLEncoding
		}
		out["raw"] = enc.EncodeToString(m.Raw)
	case "metadata":
		var headers []map[string]string
		if msg, err := mail.ReadMessage(bytes.NewReader(m.Raw)); err == nil {
			for _, name := range q["metadataHeaders"] {
				if v := msg.Header.Get(name); v != "" {
					headers = append(headers, map[string]string{"name": name, "value": v})
				}
			}
		}
		out["payload"] = map[string]any{"headers": headers}
	}
	writeJSON(w, out)
}

func (f *Fake) listHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	start, err := strconv.Atoi(q.Get("startHistoryId"))
	if err != nil {
		badRequest(w)
		return
	}
	if start < f.mb.histMin {
		notFound(w)
		return
	}
	types := q["historyTypes"]
	var recs []histRec
	for _, h := range f.mb.hist {
		if h.id > start && (len(types) == 0 || slices.Contains(types, h.kind)) {
			recs = append(recs, h)
		}
	}
	size := f.histPageSize
	if size <= 0 {
		size = 100
	}
	from, _ := strconv.Atoi(q.Get("pageToken"))
	var page []map[string]any
	for i := from; i < len(recs) && i < from+size; i++ {
		h := recs[i]
		entry := map[string]any{"id": strconv.Itoa(h.id)}
		item := map[string]any{"message": ref(&h.msg)}
		switch h.kind {
		case "messageAdded":
			entry["messagesAdded"] = []any{item}
		case "messageDeleted":
			entry["messagesDeleted"] = []any{item}
		case "labelAdded":
			item["labelIds"] = h.labels
			entry["labelsAdded"] = []any{item}
		case "labelRemoved":
			item["labelIds"] = h.labels
			entry["labelsRemoved"] = []any{item}
		}
		page = append(page, entry)
	}
	out := map[string]any{"historyId": f.History}
	if page != nil {
		out["history"] = page
	}
	if from+size < len(recs) {
		out["nextPageToken"] = strconv.Itoa(from + size)
	}
	writeJSON(w, out)
}

func (f *Fake) send(w http.ResponseWriter, body []byte) {
	var in struct{ Raw, ThreadID string }
	if json.Unmarshal(body, &in) != nil {
		badRequest(w)
		return
	}
	raw, err := decodeRaw(in.Raw)
	if err != nil {
		badRequest(w)
		return
	}
	id := f.addLocked(Message{ThreadID: in.ThreadID, LabelIDs: []string{"SENT"}, Raw: raw})
	writeJSON(w, ref(f.mb.msgs[id]))
}

func (f *Fake) draftsHandler(w http.ResponseWriter, r *http.Request, parts []string, body []byte) {
	var in struct {
		Message struct{ Raw, ThreadID string }
	}
	readRaw := func() ([]byte, bool) {
		if json.Unmarshal(body, &in) != nil {
			return nil, false
		}
		raw, err := decodeRaw(in.Message.Raw)
		return raw, err == nil
	}
	switch {
	case r.Method == http.MethodGet && len(parts) == 1:
		ids := make([]string, 0, len(f.mb.drafts))
		for did := range f.mb.drafts {
			ids = append(ids, did)
		}
		sort.Strings(ids)
		type draftRef struct {
			ID      string `json:"id"`
			Message msgRef `json:"message"`
		}
		out := struct {
			Drafts []draftRef `json:"drafts"`
		}{}
		for _, did := range ids {
			if m := f.mb.msgs[f.mb.drafts[did]]; m != nil {
				out.Drafts = append(out.Drafts, draftRef{did, ref(m)})
			}
		}
		writeJSON(w, out)
	case r.Method == http.MethodPost && len(parts) == 1:
		raw, ok := readRaw()
		if !ok {
			badRequest(w)
			return
		}
		mid := f.addLocked(Message{ThreadID: in.Message.ThreadID, LabelIDs: []string{"DRAFT"}, Raw: raw})
		did := "r" + mid
		f.mb.drafts[did] = mid
		writeJSON(w, map[string]any{"id": did, "message": ref(f.mb.msgs[mid])})
	case r.Method == http.MethodPut && len(parts) == 2:
		old, known := f.mb.drafts[parts[1]]
		if !known {
			notFound(w)
			return
		}
		raw, ok := readRaw()
		if !ok {
			badRequest(w)
			return
		}
		thread := ""
		if m := f.mb.msgs[old]; m != nil {
			thread = m.ThreadID
		}
		f.removeLocked(old)
		mid := f.addLocked(Message{ThreadID: thread, LabelIDs: []string{"DRAFT"}, Raw: raw})
		f.mb.drafts[parts[1]] = mid
		writeJSON(w, map[string]any{"id": parts[1], "message": ref(f.mb.msgs[mid])})
	case r.Method == http.MethodDelete && len(parts) == 2:
		mid, known := f.mb.drafts[parts[1]]
		if !known {
			notFound(w)
			return
		}
		delete(f.mb.drafts, parts[1])
		f.removeLocked(mid)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

// thread returns the messages of a conversation, oldest first.
func (f *Fake) thread(id string) []*Message {
	var out []*Message
	for _, m := range f.mb.msgs {
		if m.ThreadID == id {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].InternalDate != out[j].InternalDate {
			return out[i].InternalDate < out[j].InternalDate
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (f *Fake) threadsHandler(w http.ResponseWriter, r *http.Request, parts []string, body []byte) {
	msgs := f.thread(parts[1])
	if len(msgs) == 0 {
		notFound(w)
		return
	}
	switch {
	case r.Method == http.MethodGet && len(parts) == 2:
		var out []map[string]any
		for _, m := range msgs {
			out = append(out, map[string]any{"id": m.ID, "threadId": m.ThreadID, "labelIds": m.LabelIDs,
				"internalDate": strconv.FormatInt(m.InternalDate, 10)})
		}
		writeJSON(w, map[string]any{"id": parts[1], "messages": out})
	case r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "modify":
		var in struct{ AddLabelIDs, RemoveLabelIDs []string }
		if json.Unmarshal(body, &in) != nil {
			badRequest(w)
			return
		}
		for _, m := range msgs {
			f.modifyLocked(m.ID, in.AddLabelIDs, in.RemoveLabelIDs)
		}
		writeJSON(w, map[string]any{"id": parts[1]})
	case r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "trash":
		for _, m := range msgs {
			f.modifyLocked(m.ID, []string{"TRASH"}, []string{"INBOX"})
		}
		writeJSON(w, map[string]any{"id": parts[1]})
	default:
		http.NotFound(w, r)
	}
}
