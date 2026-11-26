// Package store is m9s's local copy of the mail on the server: an SQLite
// index of every message (headers, flags, what has been downloaded) and one
// raw .eml file per downloaded message.
//
// The server stays the source of truth. Everything here can be deleted and is
// downloaded again.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure Go: the build stays cgo-free

	"github.com/zhecho/m9s/internal/mail"
)

// ListSize is how many of a folder's newest messages the mail list shows.
const ListSize = 50

// schemaVersion is bumped whenever the tables change. A version-2, -3 or
// -4 index is migrated in place (migrate.go); any other older index is
// emptied and filled again from the server.
const schemaVersion = 5

const schema = `
CREATE TABLE IF NOT EXISTS folder_state (
	account     TEXT    NOT NULL,
	folder      TEXT    NOT NULL,
	uidvalidity INTEGER NOT NULL DEFAULT 0, -- 0: not learned yet
	cursor      TEXT    NOT NULL DEFAULT '', -- Microsoft: the folder's delta link
	PRIMARY KEY (account, folder)
);
CREATE TABLE IF NOT EXISTS messages (` + messagesColumns + `);
CREATE INDEX IF NOT EXISTS messages_order ON messages (account, folder, sort_key);
CREATE INDEX IF NOT EXISTS messages_thread ON messages (account, thread_id);
CREATE TABLE IF NOT EXISTS folders (
	account TEXT PRIMARY KEY,
	data    TEXT NOT NULL -- the account's folder list as JSON
);
CREATE TABLE IF NOT EXISTS account_state (
	account TEXT PRIMARY KEY,
	cursor      TEXT    NOT NULL DEFAULT '', -- Gmail: the historyId synced up to
	token_saved INTEGER NOT NULL DEFAULT 0   -- Microsoft: unix seconds of the last token save; 0: never
);`

// messagesColumns is the messages table body, shared with the migration.
const messagesColumns = `
	account   TEXT    NOT NULL,
	folder    TEXT    NOT NULL,           -- for Gmail: a label
	id        TEXT    NOT NULL,           -- IMAP: the UID; Gmail: the message id
	sort_key  INTEGER NOT NULL,           -- newest first: IMAP UID, Gmail internalDate
	thread_id TEXT,                       -- NULL: the provider has no threads
	flags     TEXT    NOT NULL,           -- JSON array; changes on the server
	data      TEXT    NOT NULL,           -- the envelope as JSON; never changes
	has_body  INTEGER NOT NULL DEFAULT 0, -- 1: the .eml file is on disk
	has_attach INTEGER,                   -- from the body: 1 carries files; NULL: not known yet
	PRIMARY KEY (account, folder, id)
`

// Store is the local mail copy. A nil *Store holds nothing and forgets
// everything it is given. Safe for concurrent use.
type Store struct {
	mu  sync.Mutex
	db  *sql.DB
	dir string // root of the .eml files; "": message content is not kept
}

// DefaultDir is ~/.local/share/m9s/mail, or under $XDG_DATA_HOME when set.
// It is user data, not a cache: downloading a whole mailbox again is costly.
func DefaultDir() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "m9s", "mail")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "m9s", "mail")
}

// Open returns the store kept in dir: dir/index.db and, per message,
// dir/<account>/<folder>/<id>.eml (or dir/<account>/<id>.eml for a body
// shared by every folder holding the message). Everything is created readable by the
// user only. An index that cannot be used — not a database, or from another
// schema version — is rebuilt.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mail store: %w", err)
	}
	path := filepath.Join(dir, "index.db")
	db, err := openIndex(path)
	if err != nil {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(path + suffix)
		}
		if db, err = openIndex(path); err != nil {
			return nil, err
		}
	}
	return &Store{db: db, dir: dir}, nil
}

// NewMemory returns a store that lasts until the process exits and keeps
// headers only.
func NewMemory() *Store {
	db, err := sql.Open("sqlite", ":memory:")
	if err == nil {
		db.SetMaxOpenConns(1) // every connection would get its own empty database
		err = initSchema(db)
	}
	if err != nil {
		return nil // a nil store is valid: nothing is remembered
	}
	return &Store{db: db}
}

func openIndex(path string) (*sql.DB, error) {
	// The file must be private before any mail is written to it.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("mail store: %w", err)
	}
	f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("mail store: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("mail store: %w", err)
	}
	db.SetMaxOpenConns(1) // the Store's mutex already serialises us
	if err := initSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("mail store %s: %w", path, err)
	}
	return db, nil
}

func initSchema(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == 2 && migrateV2(db) == nil {
		version = 4
	}
	if version == 3 && migrateV3(db) == nil {
		version = 4
	}
	if version == 4 && migrateV4(db) == nil {
		version = schemaVersion
	}
	if version != 0 && version != schemaVersion {
		for _, table := range []string{"messages", "messages_v3", "envelopes", "folder_state", "folders", "account_state"} {
			if _, err := db.Exec("DROP TABLE IF EXISTS " + table); err != nil {
				return err
			}
		}
	}
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	_, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion))
	return err
}

// Close releases the index. The store must not be used afterwards.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	return s.db.Close()
}

// --- what the UI shows ---

// envelopeColumns are what scanEnvelope reads, in its order.
const envelopeColumns = "id, sort_key, data, flags, has_attach"

// Envelopes returns the newest ListSize messages of a folder, oldest first —
// the order the server lists them in. ok is false when nothing is known
// about the folder; a folder known to be empty is ok and empty.
func (s *Store) Envelopes(account, folder string) (envelopes []*mail.Envelope, ok bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.knownFolder(account, folder) {
		return nil, false
	}
	envelopes, err := s.queryEnvelopes(`SELECT `+envelopeColumns+` FROM messages WHERE account = ? AND folder = ?
		ORDER BY sort_key DESC, id DESC LIMIT ?`, account, folder, ListSize)
	if err != nil {
		return nil, false
	}
	slices.Reverse(envelopes)
	return envelopes, true
}

func (s *Store) knownFolder(account, folder string) bool {
	var known int
	err := s.db.QueryRow("SELECT COUNT(*) FROM folder_state WHERE account = ? AND folder = ?", account, folder).Scan(&known)
	return err == nil && known > 0
}

func (s *Store) queryEnvelopes(query string, args ...any) ([]*mail.Envelope, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var envelopes []*mail.Envelope
	for rows.Next() {
		env, err := scanEnvelope(rows)
		if err != nil {
			return nil, err
		}
		envelopes = append(envelopes, env)
	}
	return envelopes, rows.Err()
}

// Envelope returns one stored message's headers.
func (s *Store) Envelope(account, folder, id string) (*mail.Envelope, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	env, err := scanEnvelope(s.db.QueryRow("SELECT "+envelopeColumns+" FROM messages WHERE account = ? AND folder = ? AND id = ?",
		account, folder, id))
	return env, err == nil
}

// EnvelopesByID returns the stored headers of the messages ids names that
// the account holds in any folder, by id. Gmail mirrors a message under
// every synced label it carries, with the same headers in each.
func (s *Store) EnvelopesByID(account string, ids []string) map[string]*mail.Envelope {
	if s == nil {
		return nil
	}
	out := make(map[string]*mail.Envelope)
	s.mu.Lock()
	defer s.mu.Unlock()
	for chunk := range slices.Chunk(ids, 500) {
		args := make([]any, 0, len(chunk)+1)
		args = append(args, account)
		for _, id := range chunk {
			args = append(args, id)
		}
		marks := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		rows, err := s.db.Query("SELECT "+envelopeColumns+" FROM messages WHERE account = ? AND id IN ("+marks+")", args...)
		if err != nil {
			return out
		}
		for rows.Next() {
			if env, err := scanEnvelope(rows); err == nil {
				out[env.ID] = env
			}
		}
		rows.Close()
	}
	return out
}

// scanEnvelope reads the envelopeColumns of a row. Envelopes stored before
// ids were text carry only a UID; they get their id and sort key from the row.
func scanEnvelope(row interface{ Scan(...any) error }) (*mail.Envelope, error) {
	var id, data, flags string
	var sortKey int64
	var hasAttach sql.NullInt64
	if err := row.Scan(&id, &sortKey, &data, &flags, &hasAttach); err != nil {
		return nil, err
	}
	env := new(mail.Envelope)
	if err := json.Unmarshal([]byte(data), env); err != nil {
		return nil, err
	}
	if env.ID == "" {
		env.ID = id
	}
	if env.SortKey == 0 {
		env.SortKey = sortKey
	}
	env.Flags = nil
	if err := json.Unmarshal([]byte(flags), &env.Flags); err != nil {
		return nil, err
	}
	if len(env.Flags) == 0 {
		env.Flags = nil
	}
	if hasAttach.Valid { // the body's answer beats the server's guess
		env.HasAttach = hasAttach.Int64 == 1
	}
	return env, nil
}

// errNoID refuses an envelope that names no message: no ID and no UID.
var errNoID = errors.New("mail store: envelope has neither ID nor UID")

// errNoFolder refuses a folder-level change without a folder name: "" means
// the account's shared bodies, which only PruneSharedBodies may delete.
var errNoFolder = errors.New("mail store: no folder name")

// keyOf is the id and sort key a message is stored under. An envelope from
// an IMAP code path that only set the UID is keyed by it; one with neither
// is refused rather than stored as "0".
func keyOf(env *mail.Envelope) (id string, sortKey int64, err error) {
	id, sortKey = env.ID, env.SortKey
	if id == "" {
		if env.UID == 0 {
			return "", 0, errNoID
		}
		id = strconv.FormatUint(uint64(env.UID), 10)
	}
	if sortKey == 0 {
		sortKey = int64(env.UID)
	}
	return id, sortKey, nil
}

// PutEnvelopes records the mail list the server just returned for a folder:
// its newest messages. They are added or updated, and stored messages the
// list shows to be gone from the server are removed. Older mail the list
// does not reach is left alone.
func (s *Store) PutEnvelopes(account, folder string, list []*mail.Envelope) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// The list covers every sort key from its lowest upwards — or the whole
	// folder, when the folder has less than a page.
	var from int64
	listed := make(map[string]bool, len(list))
	for i, env := range list {
		id, key, err := keyOf(env)
		if err != nil {
			return err
		}
		listed[id] = true
		if len(list) >= ListSize && (i == 0 || key < from) {
			from = key
		}
	}
	var gone []string
	rows, err := s.db.Query("SELECT id FROM messages WHERE account = ? AND folder = ? AND sort_key >= ?", account, folder, from)
	if err != nil {
		return wrap(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return wrap(err)
		}
		if !listed[id] {
			gone = append(gone, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return wrap(err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return wrap(err)
	}
	defer tx.Rollback() // no-op after Commit
	if _, err := tx.Exec("INSERT OR IGNORE INTO folder_state (account, folder) VALUES (?, ?)", account, folder); err != nil {
		return wrap(err)
	}
	if err := s.upsert(tx, account, folder, list); err != nil {
		return err
	}
	if err := remove(tx, account, folder, gone); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrap(err)
	}
	s.removeFiles(account, folder, gone)
	return nil
}

// upsert adds or updates messages; an update keeps has_body. A new row
// starts downloaded when its account already holds the message's shared
// body (dir/<account>/<id>.eml — a Gmail message given another label). Only
// shared bodies live directly in the account directory, so an IMAP row never
// inherits the body of the same UID in another folder.
func (s *Store) upsert(tx *sql.Tx, account, folder string, list []*mail.Envelope) error {
	for _, env := range list {
		flags := env.Flags
		if flags == nil {
			flags = []string{}
		}
		flagsJSON, _ := json.Marshal(flags)
		data, err := json.Marshal(env)
		if err != nil {
			return wrap(err)
		}
		id, sortKey, err := keyOf(env)
		if err != nil {
			return err
		}
		var thread any // NULL: no threads
		if env.ThreadID != "" {
			thread = env.ThreadID
		}
		hasBody := 0
		if s.hasSharedBody(account, id) {
			hasBody = 1
		}
		if _, err := tx.Exec(`INSERT INTO messages (account, folder, id, sort_key, thread_id, flags, data, has_body) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (account, folder, id) DO UPDATE SET sort_key = excluded.sort_key, thread_id = excluded.thread_id,
				flags = excluded.flags, data = excluded.data`,
			account, folder, id, sortKey, thread, string(flagsJSON), string(data), hasBody); err != nil {
			return wrap(err)
		}
	}
	return nil
}

func remove(tx *sql.Tx, account, folder string, ids []string) error {
	for _, id := range ids {
		if _, err := tx.Exec("DELETE FROM messages WHERE account = ? AND folder = ? AND id = ?", account, folder, id); err != nil {
			return wrap(err)
		}
	}
	return nil
}

// --- conversations ---

// threadKey groups a folder's messages into conversations: by thread id, or
// a message of its own when it has none. The prefixes keep a message id
// from ever matching a thread id.
const threadKey = "CASE WHEN thread_id IS NULL THEN 'm:' || id ELSE 't:' || thread_id END"

// Threads returns the newest ListSize conversations of a folder, oldest
// first like Envelopes. Each is the envelope of its latest message with
// ThreadCount set, unread if any of its messages is unread and flagged if
// any is flagged. Messages without a thread are conversations of one.
func (s *Store) Threads(account, folder string) []*mail.Envelope {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT `+threadKey+` AS k FROM messages WHERE account = ? AND folder = ?
		GROUP BY k ORDER BY MAX(sort_key) DESC LIMIT ?`, account, folder, ListSize)
	if err != nil {
		return nil
	}
	var keys []string
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			keys = append(keys, k)
		}
	}
	rows.Close()

	var out []*mail.Envelope
	for _, k := range keys {
		members, err := s.queryEnvelopes(`SELECT `+envelopeColumns+` FROM messages
			WHERE account = ? AND folder = ? AND `+threadKey+` = ? ORDER BY sort_key DESC, id DESC`, account, folder, k)
		if err != nil || len(members) == 0 {
			continue
		}
		latest := members[0]
		unread, flagged, files := false, false, false
		for _, m := range members {
			unread = unread || m.IsUnread()
			flagged = flagged || m.IsFlagged()
			files = files || m.HasAttach
		}
		latest.HasAttach = files
		if unread {
			latest.Flags = slices.DeleteFunc(latest.Flags, func(f string) bool { return f == `\Seen` })
		}
		if flagged && !latest.IsFlagged() {
			latest.Flags = append(latest.Flags, `\Flagged`)
		}
		if len(latest.Flags) == 0 {
			latest.Flags = nil
		}
		latest.ThreadCount = len(members)
		out = append(out, latest)
	}
	slices.Reverse(out)
	return out
}

// ThreadEnvelopes returns every stored message of a conversation, from any
// folder, oldest first, each message once.
func (s *Store) ThreadEnvelopes(account, threadID string) []*mail.Envelope {
	if s == nil || threadID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.queryEnvelopes(`SELECT `+envelopeColumns+` FROM messages WHERE account = ? AND thread_id = ?
		ORDER BY sort_key, id`, account, threadID)
	if err != nil {
		return nil
	}
	seen := make(map[string]bool, len(all))
	var out []*mail.Envelope
	for _, env := range all {
		if !seen[env.ID] {
			seen[env.ID] = true
			out = append(out, env)
		}
	}
	return out
}

// ThreadOfMessageID returns the conversation of the stored message whose
// Message-ID header (without angle brackets) is messageID, or "" when none
// is stored or it has no thread.
func (s *Store) ThreadOfMessageID(account, messageID string) string {
	if s == nil || messageID == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var thread string
	_ = s.db.QueryRow(`SELECT thread_id FROM messages WHERE account = ? AND thread_id IS NOT NULL
		AND json_extract(data, '$.MessageID') = ? LIMIT 1`, account, messageID).Scan(&thread)
	return thread
}

// --- folders and account state ---

// Folders returns a copy of the folder list last stored for an account, or
// nil when there is none.
func (s *Store) Folders(account string) []*mail.Folder {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var data string
	if s.db.QueryRow("SELECT data FROM folders WHERE account = ?", account).Scan(&data) != nil {
		return nil
	}
	var folders []*mail.Folder
	if json.Unmarshal([]byte(data), &folders) != nil {
		return nil
	}
	return folders
}

// PutFolders replaces the folder list stored for an account.
func (s *Store) PutFolders(account string, folders []*mail.Folder) error {
	if s == nil {
		return nil
	}
	data, err := json.Marshal(folders)
	if err != nil {
		return wrap(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.Exec("INSERT INTO folders (account, data) VALUES (?, ?) ON CONFLICT (account) DO UPDATE SET data = excluded.data",
		account, string(data))
	return wrap(err)
}

// Cursor returns where an account's incremental sync got to (Gmail: a
// historyId), or "" when it has none.
func (s *Store) Cursor(account string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var cursor string
	_ = s.db.QueryRow("SELECT cursor FROM account_state WHERE account = ?", account).Scan(&cursor)
	return cursor
}

// SetCursor records where an account's incremental sync got to.
func (s *Store) SetCursor(account, cursor string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO account_state (account, cursor) VALUES (?, ?)
		ON CONFLICT (account) DO UPDATE SET cursor = excluded.cursor`, account, cursor)
	return wrap(err)
}

// TokenSaved returns when the account's sign-in token was last saved
// (Microsoft: to KeePassXC), to the second; zero when never.
func (s *Store) TokenSaved(account string) time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var secs int64
	_ = s.db.QueryRow("SELECT token_saved FROM account_state WHERE account = ?", account).Scan(&secs)
	if secs == 0 {
		return time.Time{}
	}
	return time.Unix(secs, 0)
}

// SetTokenSaved records when the account's sign-in token was saved,
// keeping its cursor.
func (s *Store) SetTokenSaved(account string, t time.Time) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO account_state (account, token_saved) VALUES (?, ?)
		ON CONFLICT (account) DO UPDATE SET token_saved = excluded.token_saved`, account, t.Unix())
	return wrap(err)
}

// FolderCursor returns where a folder's incremental sync got to
// (Microsoft: its delta link), or "" when it has none.
func (s *Store) FolderCursor(account, folder string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var cursor string
	_ = s.db.QueryRow("SELECT cursor FROM folder_state WHERE account = ? AND folder = ?", account, folder).Scan(&cursor)
	return cursor
}

// SetFolderCursor records where a folder's incremental sync got to,
// keeping its UIDVALIDITY.
func (s *Store) SetFolderCursor(account, folder, cursor string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO folder_state (account, folder, cursor) VALUES (?, ?, ?)
		ON CONFLICT (account, folder) DO UPDATE SET cursor = excluded.cursor`, account, folder, cursor)
	return wrap(err)
}

// --- message content ---

// folderDir is where a folder's .eml files live. Account and folder names
// are escaped, so "Work/2026" is one directory and no name can leave dir.
func (s *Store) folderDir(account, folder string) string {
	return filepath.Join(s.dir, url.PathEscape(account), url.PathEscape(folder))
}

// bodyPath is a message's .eml file: per folder, or for folder "" the one
// file the account's folders share (a Gmail message under several labels).
func (s *Store) bodyPath(account, folder, id string) string {
	name := url.PathEscape(id) + ".eml"
	if folder == "" {
		return filepath.Join(s.dir, url.PathEscape(account), name)
	}
	return filepath.Join(s.folderDir(account, folder), name)
}

// rowsOf is the WHERE clause naming the rows a body belongs to: the one row
// of folder, or every row of the id when the body is shared (folder "").
func rowsOf(account, folder, id string) (string, []any) {
	if folder == "" {
		return "account = ? AND id = ?", []any{account, id}
	}
	return "account = ? AND folder = ? AND id = ?", []any{account, folder, id}
}

// hasSharedBody reports whether the account's shared .eml of id is on disk.
func (s *Store) hasSharedBody(account, id string) bool {
	if s.dir == "" {
		return false
	}
	info, err := os.Stat(s.bodyPath(account, "", id))
	return err == nil && info.Mode().IsRegular()
}

// PutBody saves a message's raw content (RFC 5322, as the server sent it).
// The message must already be stored. With folder "" the content is shared:
// every folder holding the message has it.
func (s *Store) PutBody(account, folder, id string, raw []byte) error {
	if s == nil || s.dir == "" {
		return nil
	}
	path := s.bodyPath(account, folder, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return wrap(err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return wrap(err)
	}
	if err := os.Rename(tmp, path); err != nil { // never a half-written message
		return wrap(err)
	}
	files := boolInt(mail.HasFiles(raw))
	s.mu.Lock()
	defer s.mu.Unlock()
	where, args := rowsOf(account, folder, id)
	_, err := s.db.Exec("UPDATE messages SET has_body = 1, has_attach = ? WHERE "+where, append([]any{files}, args...)...)
	return wrap(err)
}

// Body returns a message's raw content if it has been downloaded; folder ""
// reads the shared content. A file that has gone missing is forgotten, so
// it is downloaded again.
func (s *Store) Body(account, folder, id string) ([]byte, bool) {
	if s == nil || s.dir == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	where, args := rowsOf(account, folder, id)
	var has int
	if s.db.QueryRow("SELECT COALESCE(MAX(has_body), 0) FROM messages WHERE "+where, args...).Scan(&has) != nil || has == 0 {
		return nil, false
	}
	raw, err := os.ReadFile(s.bodyPath(account, folder, id))
	if err != nil {
		_, _ = s.db.Exec("UPDATE messages SET has_body = 0 WHERE "+where, args...)
		return nil, false
	}
	return raw, true
}

// removeFiles deletes per-folder .eml files. Shared bodies are left to
// PruneSharedBodies: another folder may still hold the message.
func (s *Store) removeFiles(account, folder string, ids []string) {
	if s.dir == "" || folder == "" {
		return
	}
	for _, id := range ids {
		_ = os.Remove(s.bodyPath(account, folder, id))
	}
}

// PruneSharedBodies deletes the shared .eml files of an account that no
// stored message refers to any more.
func (s *Store) PruneSharedBodies(account string) error {
	if s == nil || s.dir == "" {
		return nil
	}
	accountDir := filepath.Join(s.dir, url.PathEscape(account))
	entries, err := os.ReadDir(accountDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return wrap(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".eml")
		if e.IsDir() || !ok {
			continue
		}
		id, err := url.PathUnescape(name)
		if err != nil {
			continue
		}
		var n int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM messages WHERE account = ? AND id = ?", account, id).Scan(&n); err != nil {
			return wrap(err)
		}
		if n == 0 {
			_ = os.Remove(filepath.Join(accountDir, e.Name()))
		}
	}
	return nil
}

// --- what the background copy needs ---

// FolderState returns what is stored for a folder: the UIDVALIDITY it was
// stored under (0 if not learned yet) and every stored id with its flags.
func (s *Store) FolderState(account, folder string) (uidValidity uint32, flags map[string][]string, err error) {
	flags = make(map[string][]string)
	if s == nil {
		return 0, flags, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.db.QueryRow("SELECT uidvalidity FROM folder_state WHERE account = ? AND folder = ?", account, folder).Scan(&uidValidity)
	if err != nil && err != sql.ErrNoRows {
		return 0, nil, wrap(err)
	}
	rows, err := s.db.Query("SELECT id, flags FROM messages WHERE account = ? AND folder = ?", account, folder)
	if err != nil {
		return 0, nil, wrap(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			return 0, nil, wrap(err)
		}
		var f []string
		_ = json.Unmarshal([]byte(text), &f)
		flags[id] = f
	}
	return uidValidity, flags, wrap(rows.Err())
}

// SetUIDValidity records the UIDVALIDITY of a folder, keeping what is stored.
func (s *Store) SetUIDValidity(account, folder string, uidValidity uint32) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO folder_state (account, folder, uidvalidity) VALUES (?, ?, ?)
		ON CONFLICT (account, folder) DO UPDATE SET uidvalidity = excluded.uidvalidity`, account, folder, uidValidity)
	return wrap(err)
}

// ResetFolder drops everything stored for a folder and records its new
// UIDVALIDITY: the server renumbered it, so stored UIDs mean nothing now.
func (s *Store) ResetFolder(account, folder string, uidValidity uint32) error {
	if s == nil {
		return nil
	}
	if folder == "" {
		return errNoFolder
	}
	if err := s.dropFolder(account, folder); err != nil {
		return err
	}
	return s.SetUIDValidity(account, folder, uidValidity)
}

// dropFolder refuses folder "": it would delete the whole account directory.
func (s *Store) dropFolder(account, folder string) error {
	if folder == "" {
		return errNoFolder
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, table := range []string{"messages", "folder_state"} {
		if _, err := s.db.Exec("DELETE FROM "+table+" WHERE account = ? AND folder = ?", account, folder); err != nil {
			return wrap(err)
		}
	}
	if s.dir != "" {
		_ = os.RemoveAll(s.folderDir(account, folder))
	}
	return nil
}

// PruneFolders drops every stored folder of account that is not in keep —
// folders deleted or renamed on the server. A stored folder named "" is
// skipped: dropping it would reach the account's shared bodies.
func (s *Store) PruneFolders(account string, keep []string) error {
	if s == nil {
		return nil
	}
	kept := make(map[string]bool, len(keep))
	for _, name := range keep {
		kept[name] = true
	}
	s.mu.Lock()
	rows, err := s.db.Query("SELECT folder FROM folder_state WHERE account = ? UNION SELECT DISTINCT folder FROM messages WHERE account = ?",
		account, account)
	var gone []string
	for err == nil && rows.Next() {
		var name string
		if err = rows.Scan(&name); err == nil && name != "" && !kept[name] {
			gone = append(gone, name)
		}
	}
	if rows != nil {
		rows.Close()
	}
	s.mu.Unlock()
	if err != nil {
		return wrap(err)
	}
	for _, name := range gone {
		if err := s.dropFolder(account, name); err != nil {
			return err
		}
	}
	return nil
}

// AddMessages stores headers of messages, updating any already stored.
func (s *Store) AddMessages(account, folder string, list []*mail.Envelope) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return wrap(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT OR IGNORE INTO folder_state (account, folder) VALUES (?, ?)", account, folder); err != nil {
		return wrap(err)
	}
	if err := s.upsert(tx, account, folder, list); err != nil {
		return err
	}
	return wrap(tx.Commit())
}

// RemoveMessages drops messages that are gone from a folder, with their
// per-folder files. A shared body stays until PruneSharedBodies. Folder ""
// is refused.
func (s *Store) RemoveMessages(account, folder string, ids []string) error {
	if s == nil || len(ids) == 0 {
		return nil
	}
	if folder == "" {
		return errNoFolder
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return wrap(err)
	}
	defer tx.Rollback()
	if err := remove(tx, account, folder, ids); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrap(err)
	}
	s.removeFiles(account, folder, ids)
	return nil
}

// SetFlags records flags that changed on the server, by id, in one folder.
func (s *Store) SetFlags(account, folder string, flags map[string][]string) error {
	return s.setFlags("account = ? AND folder = ? AND id = ?", []any{account, folder}, flags)
}

// SetFlagsEverywhere records flags that changed on the server, by id, in
// every folder holding the message (Gmail: flags belong to the message, not
// to one label).
func (s *Store) SetFlagsEverywhere(account string, flags map[string][]string) error {
	return s.setFlags("account = ? AND id = ?", []any{account}, flags)
}

func (s *Store) setFlags(where string, args []any, flags map[string][]string) error {
	if s == nil || len(flags) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return wrap(err)
	}
	defer tx.Rollback()
	for id, f := range flags {
		if f == nil {
			f = []string{}
		}
		text, _ := json.Marshal(f)
		params := append([]any{string(text)}, args...)
		if _, err := tx.Exec("UPDATE messages SET flags = ? WHERE "+where, append(params, id)...); err != nil {
			return wrap(err)
		}
	}
	return wrap(tx.Commit())
}

// MissingBodies returns the ids of a folder's messages whose content has not
// been downloaded yet, newest first.
func (s *Store) MissingBodies(account, folder string) ([]string, error) {
	if s == nil || s.dir == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT id FROM messages WHERE account = ? AND folder = ? AND has_body = 0 ORDER BY sort_key DESC, id DESC",
		account, folder)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, wrap(err)
		}
		ids = append(ids, id)
	}
	return ids, wrap(rows.Err())
}

// Counts returns how many messages of an account are stored and how many of
// them have their content downloaded.
func (s *Store) Counts(account string) (messages, bodies int) {
	if s == nil {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.db.QueryRow("SELECT COUNT(*), COALESCE(SUM(has_body), 0) FROM messages WHERE account = ?", account).Scan(&messages, &bodies)
	return messages, bodies
}

func wrap(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("mail store: %w", err)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// FillAttachSigns works out the attachment sign of up to max stored
// messages whose body is on disk but was saved before m9s kept the sign. It
// returns how many rows it handled; 0 means none are left. A row whose body
// file has gone missing is marked as having no body, as Body would, so it is
// downloaded again.
func (s *Store) FillAttachSigns(max int) (int, error) {
	if s == nil || s.dir == "" {
		return 0, nil
	}
	type key struct{ account, folder, id string }
	s.mu.Lock()
	rows, err := s.db.Query(`SELECT account, folder, id FROM messages
		WHERE has_body = 1 AND has_attach IS NULL LIMIT ?`, max)
	if err != nil {
		s.mu.Unlock()
		return 0, wrap(err)
	}
	var todo []key
	for rows.Next() {
		var k key
		if rows.Scan(&k.account, &k.folder, &k.id) == nil {
			todo = append(todo, k)
		}
	}
	rows.Close()
	s.mu.Unlock()

	for _, k := range todo {
		raw, err := os.ReadFile(s.bodyPath(k.account, k.folder, k.id))
		if err != nil {
			raw, err = os.ReadFile(s.bodyPath(k.account, "", k.id))
		}
		files := 0
		if err == nil {
			files = boolInt(mail.HasFiles(raw))
		}
		s.mu.Lock()
		if err != nil { // no file: Body will notice and download it again
			_, err = s.db.Exec("UPDATE messages SET has_body = 0 WHERE account = ? AND folder = ? AND id = ?", k.account, k.folder, k.id)
		} else {
			_, err = s.db.Exec("UPDATE messages SET has_attach = ? WHERE account = ? AND folder = ? AND id = ?", files, k.account, k.folder, k.id)
		}
		s.mu.Unlock()
		if err != nil {
			return 0, wrap(err)
		}
	}
	return len(todo), nil
}

// KnownAttachSigns returns the attachment signs of a folder's messages that
// were learned from their bodies, by id; a message whose sign is only the
// server's guess is left out.
func (s *Store) KnownAttachSigns(account, folder string) map[string]bool {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, has_attach FROM messages
		WHERE account = ? AND folder = ? AND has_attach IS NOT NULL`, account, folder)
	if err != nil {
		return nil
	}
	defer rows.Close()
	known := map[string]bool{}
	for rows.Next() {
		var id string
		var has int
		if rows.Scan(&id, &has) == nil {
			known[id] = has == 1
		}
	}
	return known
}
