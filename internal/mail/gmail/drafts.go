package gmail

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

type draftMessage struct {
	Raw      string `json:"raw"`
	ThreadID string `json:"threadId,omitempty"`
}

func draftPath(id string) string { return "/drafts/" + url.PathEscape(id) }

// CreateDraft saves raw as a new draft and returns the draft's id. A
// non-empty threadID files it in that conversation.
func (c *Client) CreateDraft(ctx context.Context, raw []byte, threadID string) (string, error) {
	in := struct {
		Message draftMessage `json:"message"`
	}{draftMessage{encodeRaw(raw), threadID}}
	var out struct {
		ID string `json:"id"`
	}
	if err := c.sendJSON(ctx, http.MethodPost, "/drafts", in, &out, false); err != nil {
		return "", err
	}
	return out.ID, nil
}

// UpdateDraft replaces the content of a draft, keeping its id.
func (c *Client) UpdateDraft(ctx context.Context, draftID string, raw []byte) error {
	in := struct {
		ID      string       `json:"id"`
		Message draftMessage `json:"message"`
	}{draftID, draftMessage{Raw: encodeRaw(raw)}}
	return c.sendJSON(ctx, http.MethodPut, draftPath(draftID), in, nil, true)
}

// DeleteDraft permanently deletes a draft.
func (c *Client) DeleteDraft(ctx context.Context, draftID string) error {
	return c.sendJSON(ctx, http.MethodDelete, draftPath(draftID), nil, nil, true)
}

// DraftRef is a draft and the message it holds now (a draft's message id
// changes on every update).
type DraftRef struct {
	ID        string
	MessageID string
}

// ListDrafts returns every draft of the mailbox with its message id.
func (c *Client) ListDrafts(ctx context.Context) ([]DraftRef, error) {
	var out []DraftRef
	token := ""
	for {
		v := url.Values{"maxResults": {strconv.Itoa(listPageSize)}}
		if token != "" {
			v.Set("pageToken", token)
		}
		var page struct {
			Drafts []struct {
				ID      string `json:"id"`
				Message struct {
					ID string `json:"id"`
				} `json:"message"`
			} `json:"drafts"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := c.getJSON(ctx, "/drafts?"+v.Encode(), &page); err != nil {
			return nil, err
		}
		for _, d := range page.Drafts {
			out = append(out, DraftRef{ID: d.ID, MessageID: d.Message.ID})
		}
		if page.NextPageToken == "" {
			return out, nil
		}
		token = page.NextPageToken
	}
}
