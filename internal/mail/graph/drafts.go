package graph

import (
	"context"
	"encoding/base64"
	"net/http"
)

// mimeRequest is a POST of raw MIME, which Graph takes base64-encoded as
// text/plain. Not safe to repeat except on 429.
func (c *Client) mimeRequest(path string, raw []byte, out any) request {
	return request{
		method: http.MethodPost, url: c.url(path),
		body:        []byte(base64.StdEncoding.EncodeToString(raw)),
		contentType: "text/plain", out: out,
	}
}

// SendMIME sends raw (an RFC 5322 message). Graph files the sent copy in
// Sent Items itself.
func (c *Client) SendMIME(ctx context.Context, raw []byte) error {
	return c.call(ctx, c.mimeRequest("/me/sendMail", raw, nil))
}

// CreateDraftMIME stores raw as a new draft in Drafts and returns its id.
// Graph cannot update a draft made from MIME; saving again means a new
// draft and deleting the old one.
func (c *Client) CreateDraftMIME(ctx context.Context, raw []byte) (string, error) {
	var out apiMsg
	if err := c.call(ctx, c.mimeRequest("/me/messages", raw, &out)); err != nil {
		return "", err
	}
	return out.ID, nil
}
