// Package gmailtest is an in-memory fake Gmail REST server for tests.
package gmailtest

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Request is one logged request.
type Request struct {
	Method string
	Path   string
	Query  string
	Body   string
}

type reply struct {
	status  int
	body    string
	headers map[string]string
}

// Fake wraps an httptest.Server. Queued replies for a path are served first
// (in order); after that the path's default handler answers.
type Fake struct {
	Server *httptest.Server

	mu       sync.Mutex
	log      []Request
	queues   map[string][]reply
	Email    string
	History  string
	handlers map[string]http.HandlerFunc

	// The mailbox answers every path without a handler or queued reply.
	mb           *mailbox
	pageSize     int
	histPageSize int
	// PadRaw makes format=raw answers use padded base64url.
	PadRaw bool
}

// New starts a fake with a default profile. Close it with Close.
func New() *Fake {
	f := &Fake{
		queues:   map[string][]reply{},
		handlers: map[string]http.HandlerFunc{},
		Email:    "me@example.com",
		History:  "12345",
		mb:       newMailbox(),
	}
	f.Handle("/gmail/v1/users/me/profile", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		e, h := f.Email, f.History
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"emailAddress":%q,"historyId":%q}`, e, h)
	})
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// URL is the base URL to give gmail.WithBaseURL.
func (f *Fake) URL() string { return f.Server.URL }

// HistoryID returns the mailbox's current history id.
func (f *Fake) HistoryID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.History
}

// Close stops the server.
func (f *Fake) Close() { f.Server.Close() }

// Handle registers the default handler for an exact path.
func (f *Fake) Handle(path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[path] = h
}

// Queue appends a one-shot reply for path, served before the handler.
func (f *Fake) Queue(path string, status int, body string) {
	f.QueueHeaders(path, status, body, nil)
}

// QueueHeaders is Queue with response headers (e.g. Retry-After).
func (f *Fake) QueueHeaders(path string, status int, body string, headers map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queues[path] = append(f.queues[path], reply{status, body, headers})
}

// Requests returns a copy of the request log.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.log...)
}

// Count returns how many requests hit path.
func (f *Fake) Count(path string) int {
	n := 0
	for _, r := range f.Requests() {
		if r.Path == path {
			n++
		}
	}
	return n
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	f.mu.Lock()
	f.log = append(f.log, Request{r.Method, r.URL.Path, r.URL.RawQuery, string(body)})
	var q *reply
	if qs := f.queues[r.URL.Path]; len(qs) > 0 {
		q = &qs[0]
		f.queues[r.URL.Path] = qs[1:]
	}
	h := f.handlers[r.URL.Path]
	f.mu.Unlock()
	if q != nil {
		w.Header().Set("Content-Type", "application/json")
		for k, v := range q.headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(q.status)
		fmt.Fprint(w, q.body)
		return
	}
	if h == nil {
		f.mailboxHandler(w, r, body)
		return
	}
	h(w, r)
}
