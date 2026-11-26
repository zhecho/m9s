package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/zhecho/m9s/internal/mail"
)

// Msg is a message as Graph describes it. InternetMessageID has no angle
// brackets. Graph has no In-Reply-To property; it is in the raw body.
type Msg struct {
	ID, FolderID, ConversationID, InternetMessageID, Subject string
	From, To, CC                                             []mail.Address
	Received                                                 time.Time
	IsRead, Flagged, HasAttachments                          bool
	Categories                                               []string
}

const msgSelect = "id,parentFolderId,conversationId,internetMessageId,subject,from,toRecipients,ccRecipients," +
	"receivedDateTime,isRead,flag,hasAttachments,categories"

const (
	listPage   = 500 // messages per listing page ($top; Graph allows 1000)
	deltaPage  = 200 // messages per delta page (odata.maxpagesize)
	searchPage = 250
	// searchPageSize is a SearchPage page: as many as the mail list loads.
	searchPageSize = 50
	searchMax      = 500 // results Search returns at most
	batchMax       = 20  // requests per $batch (Graph's limit)
)

type apiAddr struct {
	EmailAddress struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	} `json:"emailAddress"`
}

func (a apiAddr) addr() mail.Address {
	return mail.Address{Name: a.EmailAddress.Name, Email: a.EmailAddress.Address}
}

func addrs(as []apiAddr) []mail.Address {
	if len(as) == 0 {
		return nil
	}
	out := make([]mail.Address, 0, len(as))
	for _, a := range as {
		out = append(out, a.addr())
	}
	return out
}

type apiMsg struct {
	ID                string    `json:"id"`
	ParentFolderID    string    `json:"parentFolderId"`
	ConversationID    string    `json:"conversationId"`
	InternetMessageID string    `json:"internetMessageId"`
	Subject           string    `json:"subject"`
	From              *apiAddr  `json:"from"`
	To                []apiAddr `json:"toRecipients"`
	CC                []apiAddr `json:"ccRecipients"`
	Received          time.Time `json:"receivedDateTime"`
	IsRead            bool      `json:"isRead"`
	Flag              struct {
		FlagStatus string `json:"flagStatus"`
	} `json:"flag"`
	HasAttachments bool            `json:"hasAttachments"`
	Categories     []string        `json:"categories"`
	Removed        json.RawMessage `json:"@removed"`
}

func (m *apiMsg) msg() Msg {
	out := Msg{
		ID: m.ID, FolderID: m.ParentFolderID, ConversationID: m.ConversationID,
		InternetMessageID: strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(m.InternetMessageID), "<"), ">"),
		Subject:           m.Subject, To: addrs(m.To), CC: addrs(m.CC), Received: m.Received,
		IsRead: m.IsRead, Flagged: m.Flag.FlagStatus == "flagged", HasAttachments: m.HasAttachments,
	}
	if m.From != nil && m.From.EmailAddress.Address != "" {
		out.From = []mail.Address{m.From.addr()}
	}
	if len(m.Categories) > 0 {
		out.Categories = m.Categories
	}
	return out
}

func toMsgs(ms []apiMsg) []Msg {
	out := make([]Msg, 0, len(ms))
	for i := range ms {
		out = append(out, ms[i].msg())
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }

// qv escapes a query value; spaces become %20, not "+", which OData does
// not read as a space everywhere.
func qv(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }

func msgPath(id string) string    { return "/me/messages/" + url.PathEscape(id) }
func folderPath(id string) string { return "/me/mailFolders/" + url.PathEscape(id) }

// Delta returns what changed in a folder since link, Graph's delta link from
// the previous round ("" for a first round, which returns every message),
// and the link for the next round. removed holds ids that left the folder
// (deleted or moved away). An expired link gives ErrSyncReset.
func (c *Client) Delta(ctx context.Context, folderID, link string) (changed []Msg, removed []string, next string, err error) {
	u := c.url(folderPath(folderID) + "/messages/delta?$select=" + qv(msgSelect))
	if link != "" {
		if u, err = c.link(link); err != nil {
			return nil, nil, "", err
		}
	}
	header := map[string]string{"Prefer": immutableIDs + ", odata.maxpagesize=" + itoa(deltaPage)}
	// The last word on an id wins: a page may change what an earlier one said.
	state := map[string]int{} // id -> index in changed, or -1 when removed
	for {
		var p page[apiMsg]
		if err := c.call(ctx, request{method: http.MethodGet, url: u, header: header, out: &p, idempotent: true}); err != nil {
			return nil, nil, "", err
		}
		for i := range p.Value {
			m := &p.Value[i]
			if idx, ok := state[m.ID]; ok && idx >= 0 {
				changed[idx].ID = "" // superseded
			}
			if m.Removed != nil {
				state[m.ID] = -1
				continue
			}
			state[m.ID] = len(changed)
			changed = append(changed, m.msg())
		}
		if p.DeltaLink != "" {
			next = p.DeltaLink
			break
		}
		if p.NextLink == "" {
			return nil, nil, "", errors.New("graph: delta ended without a link")
		}
		if u, err = c.link(p.NextLink); err != nil {
			return nil, nil, "", err
		}
	}
	kept := changed[:0]
	for _, m := range changed {
		if m.ID != "" {
			kept = append(kept, m)
		}
	}
	for id, idx := range state {
		if idx < 0 {
			removed = append(removed, id)
		}
	}
	return kept, removed, next, nil
}

// List returns every message in a folder (a full re-check), newest first.
func (c *Client) List(ctx context.Context, folderID string) ([]Msg, error) {
	u := c.url(folderPath(folderID) + "/messages?$top=" + itoa(listPage) + "&$select=" + qv(msgSelect))
	ms, err := collect[apiMsg](ctx, c, u, nil, 0)
	if err != nil {
		return nil, err
	}
	return toMsgs(ms), nil
}

// Get returns one message.
func (c *Client) Get(ctx context.Context, id string) (Msg, error) {
	var m apiMsg
	if err := c.getJSON(ctx, msgPath(id)+"?$select="+qv(msgSelect), &m); err != nil {
		return Msg{}, err
	}
	return m.msg(), nil
}

// Raw returns a message's MIME content (RFC 5322).
func (c *Client) Raw(ctx context.Context, id string) ([]byte, error) {
	var raw []byte
	err := c.call(ctx, request{method: http.MethodGet, url: c.url(msgPath(id) + "/$value"), raw: &raw, idempotent: true})
	return raw, err
}

func (c *Client) patch(ctx context.Context, id string, in any) error {
	return c.sendJSON(ctx, http.MethodPatch, msgPath(id), in, nil, true)
}

// SetRead marks a message read or unread.
func (c *Client) SetRead(ctx context.Context, id string, read bool) error {
	return c.patch(ctx, id, map[string]bool{"isRead": read})
}

// SetFlagged flags or unflags a message.
func (c *Client) SetFlagged(ctx context.Context, id string, on bool) error {
	status := "notFlagged"
	if on {
		status = "flagged"
	}
	return c.patch(ctx, id, map[string]any{"flag": map[string]string{"flagStatus": status}})
}

// SetCategories replaces a message's categories (none clears them).
func (c *Client) SetCategories(ctx context.Context, id string, cats []string) error {
	if cats == nil {
		cats = []string{}
	}
	return c.patch(ctx, id, map[string][]string{"categories": cats})
}

// Move moves a message to a folder (an id or a well-known name) and returns
// its id, which immutable ids keep unchanged.
func (c *Client) Move(ctx context.Context, id, folderID string) (string, error) {
	var out apiMsg
	if err := c.sendJSON(ctx, http.MethodPost, msgPath(id)+"/move", map[string]string{"destinationId": folderID}, &out, true); err != nil {
		return "", err
	}
	if out.ID == "" {
		return id, nil
	}
	return out.ID, nil
}

// PermanentDelete deletes a message for good, wherever it is. Exchange
// moves it to the hidden Recoverable Items/Purges folder, where a GET by id
// still finds it for a while. Deleting a message that is already gone is
// not an error.
func (c *Client) PermanentDelete(ctx context.Context, id string) error {
	err := c.sendJSON(ctx, http.MethodPost, msgPath(id)+"/permanentDelete", nil, nil, true)
	var ae *APIError
	if errors.As(err, &ae) && alreadyDeleted(ae.Code, ae.Status) {
		return nil
	}
	return err
}

// alreadyDeleted tells a permanentDelete answer that means the message is
// gone: 404, or the 403 ErrorCannotDeleteObject Exchange gives for one
// already in Purges.
func alreadyDeleted(status int, code string) bool {
	return status == http.StatusNotFound || (status == http.StatusForbidden && code == "ErrorCannotDeleteObject")
}

type batchReq struct {
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

type batchResp struct {
	ID      string            `json:"id"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// PermanentDeleteAll deletes messages for good, batchMax per $batch
// request. Graph runs a batch's requests side by side and may throttle
// some of them: only those are sent again. An id already gone (see
// PermanentDelete) counts as deleted.
func (c *Client) PermanentDeleteAll(ctx context.Context, ids []string) error {
	for start := 0; start < len(ids); start += batchMax {
		if err := c.deleteBatch(ctx, ids[start:min(start+batchMax, len(ids))]); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) deleteBatch(ctx context.Context, ids []string) error {
	pending := ids
	for try := 0; ; try++ {
		in := struct {
			Requests []batchReq `json:"requests"`
		}{}
		for i, id := range pending {
			in.Requests = append(in.Requests, batchReq{
				ID: itoa(i), Method: http.MethodPost, URL: msgPath(id) + "/permanentDelete",
				Headers: map[string]string{"Prefer": immutableIDs},
			})
		}
		var out struct {
			Responses []batchResp `json:"responses"`
		}
		if err := c.sendJSON(ctx, http.MethodPost, "/$batch", in, &out, true); err != nil {
			return err
		}
		var again []string
		var wait time.Duration
		var lastErr error
		answered := 0
		for _, r := range out.Responses {
			i, err := strconv.Atoi(r.ID)
			if err != nil || i < 0 || i >= len(pending) {
				continue
			}
			answered++
			switch {
			case r.Status/100 == 2 || alreadyDeleted(r.Status, errorCode(r.Body)):
			case retryable(r.Status):
				again = append(again, pending[i])
				wait = max(wait, retryAfter(r.Headers["Retry-After"]))
				lastErr = &APIError{Code: r.Status, Status: errorCode(r.Body)}
			default:
				return &APIError{Code: r.Status, Status: errorCode(r.Body)}
			}
		}
		if answered != len(pending) {
			return fmt.Errorf("graph: $batch answered %d of %d requests", answered, len(pending))
		}
		if len(again) == 0 {
			return nil
		}
		if try == maxTries-1 {
			return lastErr
		}
		if wait == 0 {
			wait = backoff(try)
		}
		if err := c.sleep(ctx, wait); err != nil {
			return err
		}
		pending = again
	}
}

// MasterCategories returns the names of the account's categories.
func (c *Client) MasterCategories(ctx context.Context) ([]string, error) {
	var out struct {
		Value []struct {
			DisplayName string `json:"displayName"`
		} `json:"value"`
	}
	if err := c.getJSON(ctx, "/me/outlook/masterCategories", &out); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out.Value))
	for _, v := range out.Value {
		names = append(names, v.DisplayName)
	}
	return names, nil
}

// SearchPage runs one page of an Outlook search: the first for link "",
// else the page link names (the nextLink of the last). It returns the next
// page's link, "" on the last.
func (c *Client) SearchPage(ctx context.Context, text, link string) ([]Msg, string, error) {
	u := link
	if u == "" {
		quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(text) + `"`
		u = c.url("/me/messages?$search=" + qv(quoted) + "&$top=" + itoa(searchPageSize) + "&$select=" + qv(msgSelect))
	} else {
		var err error
		if u, err = c.link(link); err != nil {
			return nil, "", err
		}
	}
	var p page[apiMsg]
	if err := c.call(ctx, request{method: http.MethodGet, url: u, header: map[string]string{"ConsistencyLevel": "eventual"}, out: &p, idempotent: true}); err != nil {
		return nil, "", err
	}
	return toMsgs(p.Value), p.NextLink, nil
}

// Search runs an Outlook search (e.g. `from:bob subject:"q3 report"`) over
// the whole mailbox and returns up to searchMax messages.
func (c *Client) Search(ctx context.Context, text string) ([]Msg, error) {
	quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(text) + `"`
	u := c.url("/me/messages?$search=" + qv(quoted) + "&$top=" + itoa(searchPage) + "&$select=" + qv(msgSelect))
	ms, err := collect[apiMsg](ctx, c, u, map[string]string{"ConsistencyLevel": "eventual"}, searchMax)
	if err != nil {
		return nil, err
	}
	return toMsgs(ms), nil
}
