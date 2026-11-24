// internal/sync/discover.go
package sync

import (
	"strings"

	"github.com/zhecho/m9s/internal/mail"
)

// specialUseAttrs maps IMAP \SpecialUse attributes to canonical slot names.
var specialUseAttrs = map[string]string{
	`\Sent`:    "Sent",
	`\Drafts`:  "Drafts",
	`\Trash`:   "Trash",
	`\Junk`:    "Spam",
	`\Archive`: "Archive",
}

// nameHeuristics maps lowercase folder names to canonical slot names.
var nameHeuristics = map[string]string{
	"sent":          "Sent",
	"sent items":    "Sent",
	"sent mail":     "Sent",
	"sent messages": "Sent",
	"drafts":        "Drafts",
	"draft":         "Drafts",
	"trash":         "Trash",
	"deleted":       "Trash",
	"deleted items": "Trash",
	"spam":          "Spam",
	"junk":          "Spam",
	"junk e-mail":   "Spam",
	"junk email":    "Spam",
	"archive":       "Archive",
}

// TrashFolder returns the trash folder from allFolders, or nil if not found.
// Priority: \Trash IMAP special-use attribute > name heuristics.
func TrashFolder(allFolders []*mail.Folder) *mail.Folder {
	// Pass 1: IMAP special-use attribute (case-insensitive per RFC 6154).
	for _, f := range allFolders {
		for _, attr := range f.Attributes {
			if strings.EqualFold(attr, `\Trash`) {
				return f
			}
		}
	}
	// Pass 2: name heuristics.
	for _, f := range allFolders {
		switch strings.ToLower(f.DisplayName()) {
		case "trash", "deleted items", "deleted":
			return f
		}
	}
	return nil
}

// ArchiveFolder returns the folder mail is archived to, or nil if not found.
// Priority: \Archive IMAP special-use attribute > a folder called Archive.
func ArchiveFolder(allFolders []*mail.Folder) *mail.Folder {
	for _, f := range allFolders {
		for _, attr := range f.Attributes {
			if strings.EqualFold(attr, `\Archive`) {
				return f
			}
		}
	}
	for _, f := range allFolders {
		if strings.EqualFold(f.DisplayName(), "archive") {
			return f
		}
	}
	return nil
}

// SentFolder returns the folder sent mail is kept in, or nil if not found.
// Priority: \Sent IMAP special-use attribute > name heuristics.
func SentFolder(allFolders []*mail.Folder) *mail.Folder {
	for _, f := range allFolders {
		for _, attr := range f.Attributes {
			if strings.EqualFold(attr, `\Sent`) {
				return f
			}
		}
	}
	for _, f := range allFolders {
		switch strings.ToLower(f.DisplayName()) {
		case "sent items", "sent", "sent messages", "sent mail":
			return f
		}
	}
	return nil
}

// AutoDetect returns the well-known folders from allFolders:
// INBOX, Sent, Drafts, Trash, Spam/Junk, Archive.
// Priority: IMAP special-use attribute > name heuristic.
// Duplicate matches for the same slot are deduplicated.
func AutoDetect(allFolders []*mail.Folder) []*mail.Folder {
	// slot -> first matching folder
	slotWinner := make(map[string]*mail.Folder)

	// Pass 1: IMAP special-use attributes (highest priority)
	for _, f := range allFolders {
		for _, attr := range f.Attributes {
			if slot, ok := specialUseAttrs[attr]; ok {
				if _, exists := slotWinner[slot]; !exists {
					slotWinner[slot] = f
				}
			}
		}
	}

	// Pass 2: name heuristics (won't override attr winners)
	for _, f := range allFolders {
		lower := strings.ToLower(f.DisplayName())
		if slot, ok := nameHeuristics[lower]; ok {
			if _, exists := slotWinner[slot]; !exists {
				slotWinner[slot] = f
			}
		}
	}

	// Pass 3: always include INBOX
	for _, f := range allFolders {
		if strings.ToUpper(f.Name) == "INBOX" {
			slotWinner["INBOX"] = f
			break
		}
	}

	// Return winners in original list order, deduplicated by pointer.
	seen := make(map[*mail.Folder]bool)
	var result []*mail.Folder
	for _, f := range allFolders {
		for _, winner := range slotWinner {
			if winner == f && !seen[f] {
				result = append(result, f)
				seen[f] = true
			}
		}
	}
	return result
}
