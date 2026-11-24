// Package graphtest is an in-memory fake Microsoft Graph mail server for tests.
package graphtest

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Request is one logged request. Requests inside a $batch are not logged on
// their own; the $batch request carries them in its Body.
type Request struct {
	Method string
	Path   string
	Query  string
	Body   string
	Header http.Header
}

type reply struct {
	status  int
	body    string
	headers map[string]string
}

// Fake wraps an httptest.Server. Queued replies for a path are served first
// (in order), also to requests inside a $batch; after that the path's
// handler, then the mailbox answers.
type Fake struct {
	Server *httptest.Server

	mu       sync.Mutex
	log      []Request
	queues   map[string][]reply
	handlers map[string]http.HandlerFunc
	mb       *mailbox
}

// New starts a fake whose mailbox has the six well-known folders (Inbox,
// Sent Items, Drafts, Deleted Items, Junk Email, Archive), the hidden
// Purges folder (well-known "recoverableitemspurges") that permanentDelete
// moves messages to, and no messages. Close it with Close.
func New() *Fake {
	f := &Fake{
		queues:   map[string][]reply{},
		handlers: map[string]http.HandlerFunc{},
		mb:       newMailbox(),
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// URL is the base URL to give graph.WithBaseURL.
func (f *Fake) URL() string { return f.Server.URL }

// Close stops the server.
func (f *Fake) Close() { f.Server.Close() }

// Handle registers the handler for an exact path (e.g. "/v1.0/me").
func (f *Fake) Handle(path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[path] = h
}

// Queue appends a one-shot reply for path, served before the handler.
func (f *Fake) Queue(path string, status int, body string, headers map[string]string) {
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

// Count returns how many logged requests hit path.
func (f *Fake) Count(path string) int {
	n := 0
	for _, r := range f.Requests() {
		if r.Path == path {
			n++
		}
	}
	return n
}

// popQueue takes the next queued reply for path, if any; f.mu must be held.
func (f *Fake) popQueue(path string) *reply {
	qs := f.queues[path]
	if len(qs) == 0 {
		return nil
	}
	f.queues[path] = qs[1:]
	return &qs[0]
}

func (q *reply) write(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	for k, v := range q.headers {
		w.Header().Set(k, v)
	}
	w.WriteHeader(q.status)
	fmt.Fprint(w, q.body)
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	f.mu.Lock()
	f.log = append(f.log, Request{r.Method, r.URL.Path, r.URL.RawQuery, string(body), r.Header.Clone()})
	q := f.popQueue(r.URL.Path)
	h := f.handlers[r.URL.Path]
	f.mu.Unlock()
	switch {
	case q != nil:
		q.write(w)
	case h != nil:
		h(w, r)
	case r.URL.Path == "/v1.0/$batch" && r.Method == http.MethodPost:
		f.batch(w, r, body)
	default:
		f.mailboxHandler(w, r, body)
	}
}
