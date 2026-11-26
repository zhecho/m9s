package store

import (
	"strings"

	"github.com/zhecho/m9s/internal/mail"
)

// ListQuery asks for one page of a folder's list.
type ListQuery struct {
	// Filter keeps the rows whose subject, sender name or sender address
	// contains it, ignoring case; "" keeps all. A conversation is kept when
	// any of its messages matches.
	Filter string
	// Conversations lists one row per conversation (Gmail) instead of one
	// per message.
	Conversations bool
	// Offset rows are skipped, newest first; Limit is the page size, 0 for
	// all of them.
	Offset, Limit int
}

// matchText is what the filter searches in a message: its subject and each
// sender's name and address, lowercased (any script, not only ASCII).
func matchText(env *mail.Envelope) string {
	parts := []string{env.Subject}
	for _, a := range env.From {
		parts = append(parts, a.Name, a.Email)
	}
	return strings.ToLower(strings.Join(parts, "\n"))
}

// likeEscaper makes a filter literal inside LIKE: % and _ match themselves.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// Page returns one page of folder's list, newest first, and how many rows
// the query matches in all.
func (s *Store) Page(account, folder string, q ListQuery) ([]*mail.Envelope, int, error) {
	if s == nil {
		return nil, 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	where, args := "account = ? AND folder = ?", []any{account, folder}
	if q.Filter != "" {
		where += ` AND match_text LIKE ? ESCAPE '\'`
		args = append(args, "%"+likeEscaper.Replace(strings.ToLower(q.Filter))+"%")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = -1 // SQLite: no limit
	}
	page := append(append([]any{}, args...), limit, max(q.Offset, 0))

	if !q.Conversations {
		var total int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM messages WHERE "+where, args...).Scan(&total); err != nil {
			return nil, 0, wrap(err)
		}
		rows, err := s.queryEnvelopes(`SELECT `+envelopeColumns+` FROM messages WHERE `+where+`
			ORDER BY sort_key DESC, id DESC LIMIT ? OFFSET ?`, page...)
		return rows, total, wrap(err)
	}

	// A conversation matches when any of its messages does, and is ordered
	// by its newest message, matching or not; k breaks ties, so pages
	// neither skip nor repeat a row.
	groupWhere, groupArgs := "account = ? AND folder = ?", []any{account, folder}
	having := ""
	if q.Filter != "" {
		having = ` HAVING SUM(match_text LIKE ? ESCAPE '\') > 0`
		groupArgs = append(groupArgs, args[2])
	}
	groups := `SELECT ` + threadKey + ` AS k, MAX(sort_key) AS newest FROM messages WHERE ` + groupWhere + ` GROUP BY k` + having
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM ("+groups+")", groupArgs...).Scan(&total); err != nil {
		return nil, 0, wrap(err)
	}
	keyRows, err := s.db.Query("SELECT k FROM ("+groups+") ORDER BY newest DESC, k DESC LIMIT ? OFFSET ?",
		append(groupArgs, limit, max(q.Offset, 0))...)
	if err != nil {
		return nil, 0, wrap(err)
	}
	var keys []string
	for keyRows.Next() {
		var k string
		if keyRows.Scan(&k) == nil {
			keys = append(keys, k)
		}
	}
	keyRows.Close()
	out := s.threadRows(account, folder, keys)
	return out, total, nil
}
