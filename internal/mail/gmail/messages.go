package gmail

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// MsgMeta is a message as Gmail describes it. Raw is only set by GetRaw,
// Headers only by GetMetadata.
type MsgMeta struct {
	ID           string
	ThreadID     string
	LabelIDs     []string
	InternalDate int64 // ms since the epoch: when Gmail received the message
	Raw          []byte
	Headers      map[string]string
}

// metadataHeaders are the headers GetMetadata asks for: what an envelope
// shows, what threading needs, and Content-Type for the attachment sign.
var metadataHeaders = []string{"From", "To", "Cc", "Subject", "Date", "Message-ID", "In-Reply-To", "Content-Type"}

// batchDeleteMax is the most ids Gmail takes in one batchDelete.
const batchDeleteMax = 1000

// listPageSize is how many ids one messages.list page asks for (Gmail's maximum).
const listPageSize = 500

type apiMessage struct {
	ID           string   `json:"id"`
	ThreadID     string   `json:"threadId"`
	LabelIDs     []string `json:"labelIds"`
	InternalDate string   `json:"internalDate"`
	Raw          string   `json:"raw"`
	Payload      struct {
		Headers []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"headers"`
	} `json:"payload"`
}

func (m *apiMessage) meta() *MsgMeta {
	date, _ := strconv.ParseInt(m.InternalDate, 10, 64)
	return &MsgMeta{ID: m.ID, ThreadID: m.ThreadID, LabelIDs: m.LabelIDs, InternalDate: date}
}

// ListMessageIDs returns one page of the ids of messages carrying labelID
// (any label when "") and matching query (Gmail search syntax, may be ""),
// newest first, and the token of the next page ("" on the last).
func (c *Client) ListMessageIDs(ctx context.Context, labelID, query, pageToken string) (ids []string, next string, err error) {
	v := url.Values{"maxResults": {strconv.Itoa(listPageSize)}}
	if labelID != "" {
		v.Set("labelIds", labelID)
	}
	if labelID == "TRASH" || labelID == "SPAM" {
		v.Set("includeSpamTrash", "true")
	}
	if query != "" {
		v.Set("q", query)
	}
	if pageToken != "" {
		v.Set("pageToken", pageToken)
	}
	var out struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
		NextPageToken string `json:"nextPageToken"`
	}
	if err := c.getJSON(ctx, "/messages?"+v.Encode(), &out); err != nil {
		return nil, "", err
	}
	ids = make([]string, 0, len(out.Messages))
	for _, m := range out.Messages {
		ids = append(ids, m.ID)
	}
	return ids, out.NextPageToken, nil
}

func msgPath(id string) string { return "/messages/" + url.PathEscape(id) }

// GetMetadata returns a message's labels, thread and envelope headers.
func (c *Client) GetMetadata(ctx context.Context, id string) (*MsgMeta, error) {
	v := url.Values{"format": {"metadata"}, "metadataHeaders": metadataHeaders}
	var m apiMessage
	if err := c.getJSON(ctx, msgPath(id)+"?"+v.Encode(), &m); err != nil {
		return nil, err
	}
	meta := m.meta()
	meta.Headers = make(map[string]string, len(m.Payload.Headers))
	for _, h := range m.Payload.Headers {
		for _, want := range metadataHeaders {
			if strings.EqualFold(h.Name, want) {
				meta.Headers[want] = h.Value
			}
		}
	}
	return meta, nil
}

// GetRaw returns a message with its raw RFC 5322 content.
func (c *Client) GetRaw(ctx context.Context, id string) (*MsgMeta, error) {
	var m apiMessage
	if err := c.getJSON(ctx, msgPath(id)+"?format=raw", &m); err != nil {
		return nil, err
	}
	raw, err := decodeRaw(m.Raw)
	if err != nil {
		return nil, fmt.Errorf("gmail: message %s: bad raw encoding", id)
	}
	meta := m.meta()
	meta.Raw = raw
	return meta, nil
}

// decodeRaw decodes base64url, padded or not.
func decodeRaw(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

func encodeRaw(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }

// Modify adds and removes labels on a message.
func (c *Client) Modify(ctx context.Context, id string, add, remove []string) error {
	in := struct {
		Add    []string `json:"addLabelIds,omitempty"`
		Remove []string `json:"removeLabelIds,omitempty"`
	}{add, remove}
	return c.sendJSON(ctx, http.MethodPost, msgPath(id)+"/modify", in, nil, true)
}

// Trash moves a message to the Trash.
func (c *Client) Trash(ctx context.Context, id string) error {
	return c.sendJSON(ctx, http.MethodPost, msgPath(id)+"/trash", nil, nil, true)
}

// Delete permanently deletes a message.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.sendJSON(ctx, http.MethodDelete, msgPath(id), nil, nil, true)
}

// BatchDelete permanently deletes messages, at most batchDeleteMax per call.
func (c *Client) BatchDelete(ctx context.Context, ids []string) error {
	for start := 0; start < len(ids); start += batchDeleteMax {
		chunk := ids[start:min(start+batchDeleteMax, len(ids))]
		in := struct {
			IDs []string `json:"ids"`
		}{chunk}
		if err := c.sendJSON(ctx, http.MethodPost, "/messages/batchDelete", in, nil, true); err != nil {
			return err
		}
	}
	return nil
}

// Send sends raw (an RFC 5322 message) and returns the sent message. A
// non-empty threadID keeps a reply in its conversation.
func (c *Client) Send(ctx context.Context, raw []byte, threadID string) (*MsgMeta, error) {
	in := struct {
		Raw      string `json:"raw"`
		ThreadID string `json:"threadId,omitempty"`
	}{encodeRaw(raw), threadID}
	var out apiMessage
	if err := c.sendJSON(ctx, http.MethodPost, "/messages/send", in, &out, false); err != nil {
		return nil, err
	}
	return out.meta(), nil
}
