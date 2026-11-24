package mail

import (
	"bytes"
	"io"
	"strings"

	gomail "github.com/emersion/go-message/mail"
)

// ParseMessage turns a raw RFC 5322 message into a Message for env. It is
// provider-neutral: IMAP and Gmail both hand over the message as the server
// holds it.
func ParseMessage(env *Envelope, raw []byte) *Message {
	msg := &Message{Envelope: env}
	if len(raw) > 0 {
		parseBody(msg, raw)
	}
	return msg
}

// parseBody parses a raw RFC 5322 message into text/html parts.
func parseBody(msg *Message, raw []byte) {
	r, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		// Fallback: treat entire raw as text.
		msg.TextBody = string(raw)
		return
	}
	for {
		part, err := r.NextPart()
		if err != nil {
			break
		}
		switch h := part.Header.(type) {
		case *gomail.InlineHeader:
			ct, _, _ := h.ContentType()
			data, readErr := io.ReadAll(part.Body)
			if readErr != nil {
				continue
			}
			switch strings.ToLower(ct) {
			case "text/plain":
				msg.TextBody = string(data)
			case "text/html":
				msg.HTMLBody = string(data)
			default:
				// Not a body: something the HTML shows, usually a picture
				// it refers to by Content-ID.
				_, params, _ := h.ContentDisposition()
				filename := params["filename"]
				if filename == "" {
					_, ctParams, _ := h.ContentType()
					filename = ctParams["name"]
				}
				msg.Attachments = append(msg.Attachments, &Attachment{
					Filename:    filename,
					ContentType: strings.ToLower(ct),
					ContentID:   strings.Trim(strings.TrimSpace(h.Get("Content-Id")), "<>"),
					Inline:      true,
					Data:        data,
				})
			}
		case *gomail.AttachmentHeader:
			filename, _ := h.Filename()
			ct, _, _ := h.ContentType()
			disp, _, _ := h.ContentDisposition()
			cid := strings.Trim(strings.TrimSpace(h.Get("Content-Id")), "<>")
			data, readErr := io.ReadAll(part.Body)
			if readErr != nil {
				continue
			}
			msg.Attachments = append(msg.Attachments, &Attachment{
				Filename:    filename,
				ContentType: ct,
				ContentID:   cid,
				Inline:      !IsFile(disp, ct, cid != ""),
				Data:        data,
			})
		}
	}
}

// HasFiles reports whether raw carries a file to save — the same answer as
// len(ParseMessage(env, raw).Files()) > 0 — without reading any part's data.
func HasFiles(raw []byte) bool {
	r, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return false
	}
	for {
		part, err := r.NextPart()
		if err != nil {
			return false
		}
		if h, ok := part.Header.(*gomail.AttachmentHeader); ok {
			ct, _, _ := h.ContentType()
			disp, _, _ := h.ContentDisposition()
			if IsFile(disp, ct, strings.TrimSpace(h.Get("Content-Id")) != "") {
				return true
			}
		}
	}
}

// IsFile is m9s's one rule for "a file the owner would save", shared by the
// parser, HasFiles and the IMAP structure check so the 📎 sign and the file
// list agree. A part is a file when its disposition says attachment, or when
// it is neither inline nor text and the HTML does not show it by Content-ID
// (a picture sent without a disposition, as .NET mailers do).
func IsFile(disposition, mediaType string, hasContentID bool) bool {
	disposition, mediaType = strings.ToLower(disposition), strings.ToLower(mediaType)
	switch {
	case disposition == "attachment":
		return true
	case disposition == "inline", strings.HasPrefix(mediaType, "text/"), strings.HasPrefix(mediaType, "multipart/"):
		return false
	default:
		return !hasContentID
	}
}
