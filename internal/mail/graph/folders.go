package graph

import (
	"context"
	"errors"
	"net/url"
)

// Folder is a mail folder. ParentID is "" for a top-level folder.
// WellKnown is Graph's name for the special folders (inbox, sentitems,
// drafts, deleteditems, junkemail, archive), "" for the others; it is how
// they are recognised whatever their (localized) display name.
type Folder struct {
	ID, ParentID, DisplayName, WellKnown string
	Total, Unread                        int
}

// WellKnownNames are the special folders Folders recognises.
var WellKnownNames = []string{"inbox", "sentitems", "drafts", "deleteditems", "junkemail", "archive"}

const folderSelect = "id,displayName,parentFolderId,childFolderCount,totalItemCount,unreadItemCount"

// folderPage is how many folders one page asks for.
const folderPage = 100

type apiFolder struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	ChildCount  int    `json:"childFolderCount"`
	Total       int    `json:"totalItemCount"`
	Unread      int    `json:"unreadItemCount"`
}

func folderListURL(c *Client, path string) string {
	return c.url(path + "?$top=" + itoa(folderPage) + "&$select=" + folderSelect)
}

// Folder returns one folder with its counts. ParentID is left empty and
// WellKnown is not looked up.
func (c *Client) Folder(ctx context.Context, id string) (Folder, error) {
	var f apiFolder
	if err := c.getJSON(ctx, "/me/mailFolders/"+url.PathEscape(id)+"?$select="+folderSelect, &f); err != nil {
		return Folder{}, err
	}
	return Folder{ID: f.ID, DisplayName: f.DisplayName, Total: f.Total, Unread: f.Unread}, nil
}

// Folders returns every mail folder, each parent before its children.
func (c *Client) Folders(ctx context.Context) ([]Folder, error) {
	top, err := collect[apiFolder](ctx, c, folderListURL(c, "/me/mailFolders"), nil, 0)
	if err != nil {
		return nil, err
	}
	var out []Folder
	var walk func(parent string, fs []apiFolder) error
	walk = func(parent string, fs []apiFolder) error {
		for _, f := range fs {
			out = append(out, Folder{ID: f.ID, ParentID: parent, DisplayName: f.DisplayName, Total: f.Total, Unread: f.Unread})
			if f.ChildCount == 0 {
				continue
			}
			kids, err := collect[apiFolder](ctx, c, folderListURL(c, "/me/mailFolders/"+url.PathEscape(f.ID)+"/childFolders"), nil, 0)
			if err != nil {
				return err
			}
			if err := walk(f.ID, kids); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk("", top); err != nil {
		return nil, err
	}

	byID := make(map[string]*Folder, len(out))
	for i := range out {
		byID[out[i].ID] = &out[i]
	}
	for _, wk := range WellKnownNames {
		var f struct {
			ID string `json:"id"`
		}
		err := c.getJSON(ctx, "/me/mailFolders/"+wk+"?$select=id", &f)
		if errors.Is(err, ErrNotFound) {
			continue // e.g. no Archive folder on this account
		}
		if err != nil {
			return nil, err
		}
		if g := byID[f.ID]; g != nil {
			g.WellKnown = wk
		}
	}
	return out, nil
}
