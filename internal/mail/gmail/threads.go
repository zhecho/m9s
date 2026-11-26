package gmail

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

// threadPageSize is how many conversations one search page asks for: a
// search shows the newest few, not the whole mailbox.
const threadPageSize = 50

func threadPath(id string) string { return "/threads/" + url.PathEscape(id) }

// ListThreads returns the ids of the conversations matching query (Gmail
// search syntax, passed on as typed), newest first, and the token of the
// next page ("" on the last). Spam and Trash are left out unless the query
// asks for them, as on Gmail web.
func (c *Client) ListThreads(ctx context.Context, query, pageToken string) (ids []string, next string, err error) {
	ids, next, _, err = c.SearchThreads(ctx, query, pageToken)
	return ids, next, err
}

// SearchThreads is ListThreads that also returns Gmail's estimate of how
// many conversations match in all (resultSizeEstimate).
func (c *Client) SearchThreads(ctx context.Context, query, pageToken string) (ids []string, next string, estimate int, err error) {
	v := url.Values{"maxResults": {strconv.Itoa(threadPageSize)}}
	if query != "" {
		v.Set("q", query)
	}
	if pageToken != "" {
		v.Set("pageToken", pageToken)
	}
	var out struct {
		Threads []struct {
			ID string `json:"id"`
		} `json:"threads"`
		NextPageToken      string `json:"nextPageToken"`
		ResultSizeEstimate int    `json:"resultSizeEstimate"`
	}
	if err := c.getJSON(ctx, "/threads?"+v.Encode(), &out); err != nil {
		return nil, "", 0, err
	}
	ids = make([]string, 0, len(out.Threads))
	for _, t := range out.Threads {
		ids = append(ids, t.ID)
	}
	return ids, out.NextPageToken, out.ResultSizeEstimate, nil
}

// ThreadMessages returns the messages of a conversation, oldest first,
// with their ids, labels and dates but no content.
func (c *Client) ThreadMessages(ctx context.Context, id string) ([]*MsgMeta, error) {
	var t struct {
		Messages []apiMessage `json:"messages"`
	}
	if err := c.getJSON(ctx, threadPath(id)+"?format=minimal", &t); err != nil {
		return nil, err
	}
	out := make([]*MsgMeta, 0, len(t.Messages))
	for _, m := range t.Messages {
		out = append(out, m.meta())
	}
	return out, nil
}

// GetThread returns every message of a conversation, oldest first, each
// with its raw content. A message deleted while the thread is read is left
// out.
func (c *Client) GetThread(ctx context.Context, id string) ([]*MsgMeta, error) {
	metas, err := c.ThreadMessages(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]*MsgMeta, 0, len(metas))
	for _, m := range metas {
		full, err := c.GetRaw(ctx, m.ID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, full)
	}
	return out, nil
}

// ModifyThread adds and removes labels on every message of a conversation.
func (c *Client) ModifyThread(ctx context.Context, id string, add, remove []string) error {
	in := struct {
		Add    []string `json:"addLabelIds,omitempty"`
		Remove []string `json:"removeLabelIds,omitempty"`
	}{add, remove}
	return c.sendJSON(ctx, http.MethodPost, threadPath(id)+"/modify", in, nil, true)
}

// TrashThread moves every message of a conversation to the Trash.
func (c *Client) TrashThread(ctx context.Context, id string) error {
	return c.sendJSON(ctx, http.MethodPost, threadPath(id)+"/trash", nil, nil, true)
}
