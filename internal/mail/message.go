package mail

import (
	"fmt"
	"strings"
	"time"
)

type Address struct {
	Name  string
	Email string
}

func (a Address) String() string {
	if a.Name == "" {
		return a.Email
	}
	return fmt.Sprintf("%s <%s>", a.Name, a.Email)
}

type Envelope struct {
	ID          string // opaque, unique within (account, folder)
	ThreadID    string // "" when the provider has no threads
	ThreadCount int    // >1 only on a conversation row
	// Participants are the senders of a conversation row's messages, oldest
	// first, each once; nil on a plain message row.
	Participants []Address `json:",omitempty"`
	SortKey      int64     // newest-first order: IMAP UID, Gmail internalDate ms
	// UID is for the IMAP adapter only.
	UID       uint32
	SeqNum    uint32
	MessageID string   // without angle brackets
	InReplyTo []string // without angle brackets
	Subject   string
	From      []Address
	To        []Address
	CC        []Address
	Date      time.Time
	Flags     []string
	HasAttach bool
	Size      uint32
	// Categories are a Microsoft message's Outlook categories (its tags);
	// nil for other providers.
	Categories []string `json:",omitempty"`
}

func (e *Envelope) IsUnread() bool {
	for _, f := range e.Flags {
		if f == `\Seen` {
			return false
		}
	}
	return true
}

func (e *Envelope) IsFlagged() bool {
	for _, f := range e.Flags {
		if f == `\Flagged` {
			return true
		}
	}
	return false
}

func (e *Envelope) FormattedDate(layout string) string {
	return e.Date.Format(layout)
}

func (e *Envelope) FromDisplay() string {
	if len(e.From) == 0 {
		return ""
	}
	if e.From[0].Name != "" {
		return e.From[0].Name
	}
	return e.From[0].Email
}

// Attachment is a file carried by a message. Inline ones are part of the
// message's look rather than files to save: usually pictures the HTML body
// shows by their ContentID (<img src="cid:...">).
type Attachment struct {
	Filename    string
	ContentType string
	ContentID   string // without angle brackets; "" if the part has none
	Inline      bool
	Data        []byte
}

type Message struct {
	Envelope    *Envelope
	TextBody    string
	HTMLBody    string
	Attachments []*Attachment
	// Headers are the message's header fields exactly as received, top to
	// bottom; Size is the raw message in bytes; Parts is its MIME structure.
	// All empty for a message whose content was never downloaded.
	Headers []HeaderField
	Size    int
	Parts   []Part
}

// HeaderField is one header field as received: Raw keeps its folded lines
// (without the final line break), Key is its name as written.
type HeaderField struct {
	Key string
	Raw string
}

// Value is the field's value unfolded onto one line, still as written (an
// encoded word stays encoded).
func (f HeaderField) Value() string {
	_, v, _ := strings.Cut(f.Raw, ":")
	return strings.Join(strings.Fields(v), " ")
}

// Part is one MIME part: Depth is 0 for the message itself; Size is the
// decoded content of a leaf part (0 for a multipart).
type Part struct {
	Depth       int
	ContentType string
	Filename    string
	Size        int
}

// HeaderValues returns the unfolded values of every field named key (any
// case), top to bottom.
func (m *Message) HeaderValues(key string) []string {
	var out []string
	for _, f := range m.Headers {
		if strings.EqualFold(f.Key, key) {
			out = append(out, f.Value())
		}
	}
	return out
}

// Files returns the attachments a reader would save: everything except what
// the message embeds in its own body.
func (m *Message) Files() []*Attachment {
	var out []*Attachment
	for _, a := range m.Attachments {
		if !a.Inline {
			out = append(out, a)
		}
	}
	return out
}

func (m *Message) PlainText() string {
	if m.TextBody != "" {
		return m.TextBody
	}
	return strings.TrimSpace(stripHTMLTags(m.HTMLBody))
}

func stripHTMLTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}
