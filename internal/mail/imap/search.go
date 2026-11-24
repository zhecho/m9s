package imap

import (
	"context"
	"fmt"
	"strings"

	imap "github.com/emersion/go-imap/v2"
)

// Search performs a server-side SEARCH with the given criteria.
// Returns the matching UIDs.
func (ic *imapClient) Search(_ context.Context, folder string, criteria *imap.SearchCriteria) ([]uint32, error) {
	ic.opMu.Lock() // SELECT and the command that follows must not interleave
	defer ic.opMu.Unlock()
	ic.mu.Lock()
	c := ic.c
	ic.mu.Unlock()
	if c == nil {
		return nil, fmt.Errorf("imap: not connected")
	}

	if _, err := c.Select(folder, nil).Wait(); err != nil {
		return nil, fmt.Errorf("imap SELECT %s: %w", folder, err)
	}

	data, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("imap UID SEARCH: %w", err)
	}

	uidSet, ok := data.All.(imap.UIDSet)
	if !ok {
		return nil, nil
	}

	nums, _ := uidSet.Nums()
	uids := make([]uint32, len(nums))
	for i, u := range nums {
		uids[i] = uint32(u)
	}
	return uids, nil
}

// BuildSearchCriteria builds an IMAP SearchCriteria from a plain text query.
// Each whitespace-separated word is added as a TEXT criterion (AND logic).
func BuildSearchCriteria(query string) *imap.SearchCriteria {
	criteria := &imap.SearchCriteria{}
	words := strings.Fields(query)
	for _, w := range words {
		criteria.Text = append(criteria.Text, w)
	}
	if len(criteria.Text) == 0 {
		// Empty query — match all.
	}
	return criteria
}
