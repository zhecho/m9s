package gmail

import (
	"context"
	"net/url"
)

// HistoryChange is what happened to the mailbox since a history id, by
// message id. A message is in at most one list: Deleted wins over Added,
// and Added over LabelsChanged.
type HistoryChange struct {
	Added         []string
	Deleted       []string
	LabelsChanged []string
}

// historyTypes are the changes History asks Gmail for.
var historyTypes = []string{"messageAdded", "messageDeleted", "labelAdded", "labelRemoved"}

type historyRef struct {
	Message struct {
		ID string `json:"id"`
	} `json:"message"`
}

// History returns the changes since startID and the history id they reach.
// It answers ErrNotFound when startID is too old for Gmail to remember: the
// caller must then list the mailbox again.
func (c *Client) History(ctx context.Context, startID string) (HistoryChange, string, error) {
	state := map[string]int{} // id -> 1 added, 2 deleted, 3 labels changed
	var order []string
	note := func(refs []historyRef, kind int) {
		for _, r := range refs {
			id := r.Message.ID
			prev, seen := state[id]
			if !seen {
				order = append(order, id)
			}
			switch {
			case !seen, kind == 2:
				state[id] = kind
			case kind == 1 && prev == 3:
				state[id] = 1
			}
		}
	}
	var newID, token string
	for {
		v := url.Values{"startHistoryId": {startID}, "historyTypes": historyTypes, "maxResults": {"500"}}
		if token != "" {
			v.Set("pageToken", token)
		}
		var out struct {
			History []struct {
				MessagesAdded   []historyRef `json:"messagesAdded"`
				MessagesDeleted []historyRef `json:"messagesDeleted"`
				LabelsAdded     []historyRef `json:"labelsAdded"`
				LabelsRemoved   []historyRef `json:"labelsRemoved"`
			} `json:"history"`
			NextPageToken string `json:"nextPageToken"`
			HistoryID     string `json:"historyId"`
		}
		if err := c.getJSON(ctx, "/history?"+v.Encode(), &out); err != nil {
			return HistoryChange{}, "", err
		}
		for _, h := range out.History {
			note(h.MessagesAdded, 1)
			note(h.LabelsAdded, 3)
			note(h.LabelsRemoved, 3)
			note(h.MessagesDeleted, 2)
		}
		if out.HistoryID != "" {
			newID = out.HistoryID
		}
		if token = out.NextPageToken; token == "" {
			break
		}
	}
	if newID == "" {
		newID = startID
	}
	var ch HistoryChange
	for _, id := range order {
		switch state[id] {
		case 1:
			ch.Added = append(ch.Added, id)
		case 2:
			ch.Deleted = append(ch.Deleted, id)
		case 3:
			ch.LabelsChanged = append(ch.LabelsChanged, id)
		}
	}
	return ch, newID, nil
}
