package gmail

import (
	"context"
	"net/url"
	"strings"
)

// Label is a Gmail label. The counts are only filled by Label (Gmail's
// labels.list leaves them out).
type Label struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Type           string `json:"type"` // "system" or "user"
	MessagesTotal  int    `json:"messagesTotal"`
	MessagesUnread int    `json:"messagesUnread"`
	ThreadsTotal   int    `json:"threadsTotal"`
	ThreadsUnread  int    `json:"threadsUnread"`
}

// Labels lists every label of the mailbox.
func (c *Client) Labels(ctx context.Context) ([]Label, error) {
	var out struct {
		Labels []Label `json:"labels"`
	}
	if err := c.getJSON(ctx, "/labels", &out); err != nil {
		return nil, err
	}
	return out.Labels, nil
}

// Label returns one label with its message and conversation counts.
func (c *Client) Label(ctx context.Context, id string) (*Label, error) {
	var l Label
	if err := c.getJSON(ctx, "/labels/"+url.PathEscape(id), &l); err != nil {
		return nil, err
	}
	return &l, nil
}

// systemFolders are the system labels shown as folders, by the folder
// names m9s uses for every provider.
var systemFolders = map[string]string{
	"INBOX":   "INBOX",
	"SENT":    "Sent",
	"DRAFT":   "Drafts",
	"TRASH":   "Trash",
	"SPAM":    "Spam",
	"STARRED": "Starred",
}

// FolderName is the folder a label is shown as; ok is false for labels that
// are not folders (UNREAD, IMPORTANT, CHAT, CATEGORY_* and other system
// labels). User labels keep their name, nested by "/".
func FolderName(l Label) (name string, ok bool) {
	if name, ok := systemFolders[l.ID]; ok {
		return name, true
	}
	if l.Type == "system" || strings.HasPrefix(l.ID, "CATEGORY_") || l.Name == "" {
		return "", false
	}
	return l.Name, true
}
