package store

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/zhecho/m9s/internal/mail"
)

// v2Schema is the schema m9s used before message ids became text — copied
// here verbatim so the migration is tested against what is on disk.
const v2Schema = `
CREATE TABLE IF NOT EXISTS folder_state (
	account     TEXT    NOT NULL,
	folder      TEXT    NOT NULL,
	uidvalidity INTEGER NOT NULL DEFAULT 0, -- 0: not learned yet
	PRIMARY KEY (account, folder)
);
CREATE TABLE IF NOT EXISTS messages (
	account  TEXT    NOT NULL,
	folder   TEXT    NOT NULL,
	uid      INTEGER NOT NULL,
	flags    TEXT    NOT NULL,           -- JSON array; changes on the server
	data     TEXT    NOT NULL,           -- the envelope as JSON; never changes
	has_body INTEGER NOT NULL DEFAULT 0, -- 1: the .eml file is on disk
	PRIMARY KEY (account, folder, uid)
);
CREATE TABLE IF NOT EXISTS folders (
	account TEXT PRIMARY KEY,
	data    TEXT NOT NULL -- the account's folder list as JSON
);
PRAGMA user_version = 2;`

func openRaw(t *testing.T, dir string) *sql.DB {
	t.Helper()
	must(t, os.MkdirAll(dir, 0o700))
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "index.db"))
	must(t, err)
	db.SetMaxOpenConns(1)
	return db
}

// v2Row stores one message the way the v2 code did: envelope JSON without
// ID or SortKey.
func v2Row(t *testing.T, db *sql.DB, folder string, uid uint32, flags []string, hasBody int) {
	t.Helper()
	data, _ := json.Marshal(&mail.Envelope{UID: uid, Subject: "old"})
	if flags == nil {
		flags = []string{}
	}
	f, _ := json.Marshal(flags)
	_, err := db.Exec("INSERT INTO messages (account, folder, uid, flags, data, has_body) VALUES ('acct', ?, ?, ?, ?, ?)",
		folder, uid, string(f), string(data), hasBody)
	must(t, err)
}

func TestMigrateV2KeepsIMAPData(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mail")
	db := openRaw(t, dir)
	_, err := db.Exec(v2Schema)
	must(t, err)
	_, err = db.Exec("INSERT INTO folder_state (account, folder, uidvalidity) VALUES ('acct', 'INBOX', 7), ('acct', 'Sent', 9)")
	must(t, err)
	v2Row(t, db, "INBOX", 101, []string{`\Seen`}, 1)
	v2Row(t, db, "INBOX", 102, nil, 0)
	v2Row(t, db, "Sent", 5, []string{`\Seen`, `\Flagged`}, 0)
	_, err = db.Exec("INSERT INTO folders (account, data) VALUES ('acct', '[{\"Name\":\"INBOX\"}]')")
	must(t, err)
	must(t, db.Close())
	body := filepath.Join(dir, "acct", "INBOX", "101.eml")
	must(t, os.MkdirAll(filepath.Dir(body), 0o700))
	must(t, os.WriteFile(body, []byte("Subject: kept\r\n\r\nbody"), 0o600))

	s, err := Open(dir)
	must(t, err)
	defer s.Close()

	validity, flags, err := s.FolderState("acct", "INBOX")
	must(t, err)
	if validity != 7 {
		t.Errorf("uidvalidity = %d, want 7", validity)
	}
	if len(flags) != 2 || !reflect.DeepEqual(flags["101"], []string{`\Seen`}) || len(flags["102"]) != 0 {
		t.Errorf("flags = %v, want 101 seen and 102 unflagged", flags)
	}
	if raw, ok := s.Body("acct", "INBOX", "101"); !ok || string(raw) != "Subject: kept\r\n\r\nbody" {
		t.Errorf("Body(101) = %q, %v", raw, ok)
	}
	list, ok := s.Envelopes("acct", "INBOX")
	if !ok || len(list) != 2 || list[0].UID != 101 || list[1].UID != 102 {
		t.Fatalf("Envelopes = %+v, %v; want 101 then 102", list, ok)
	}
	if list[0].ID != "101" || list[0].SortKey != 101 || list[1].ID != "102" {
		t.Errorf("old envelopes not given ids: %+v", list)
	}
	if e, ok := s.Envelope("acct", "Sent", "5"); !ok || !e.IsFlagged() || e.ID != "5" {
		t.Errorf("Sent/5 = %+v, %v", e, ok)
	}
	if s.Folders("acct") == nil {
		t.Error("the folder list was lost")
	}
	missing, err := s.MissingBodies("acct", "INBOX")
	must(t, err)
	if !reflect.DeepEqual(missing, []string{"102"}) {
		t.Errorf("missing = %v, want [102]: downloaded bodies must not be fetched again", missing)
	}
	var version int
	must(t, s.db.QueryRow("PRAGMA user_version").Scan(&version))
	if version != schemaVersion {
		t.Errorf("user_version = %d, want %d", version, schemaVersion)
	}
	must(t, s.SetCursor("acct", "1")) // account_state exists
}

func TestMigrateFailureFallsBackToRebuild(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mail")
	db := openRaw(t, dir)
	// A v2 index whose messages table is not what v2 wrote: the copy fails.
	_, err := db.Exec(`CREATE TABLE messages (account TEXT, folder TEXT, junk TEXT);
		INSERT INTO messages VALUES ('acct', 'INBOX', 'x');
		CREATE TABLE folder_state (account TEXT, folder TEXT, uidvalidity INTEGER, PRIMARY KEY (account, folder));
		INSERT INTO folder_state VALUES ('acct', 'INBOX', 7);
		PRAGMA user_version = 2;`)
	must(t, err)
	must(t, db.Close())

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("a failed migration must rebuild, not fail: %v", err)
	}
	defer s.Close()
	if _, ok := s.Envelopes("acct", "INBOX"); ok {
		t.Error("the rebuilt index still knows INBOX")
	}
	must(t, s.AddMessages("acct", "INBOX", envs(1)))
	if _, ok := s.Envelope("acct", "INBOX", "1"); !ok {
		t.Error("the rebuilt index does not work")
	}
	var version int
	must(t, s.db.QueryRow("PRAGMA user_version").Scan(&version))
	if version != schemaVersion {
		t.Errorf("user_version = %d, want %d", version, schemaVersion)
	}
}

// v3Messages is the messages table of schema version 3, verbatim.
const v3Messages = `CREATE TABLE messages (
	account   TEXT    NOT NULL,
	folder    TEXT    NOT NULL,
	id        TEXT    NOT NULL,
	sort_key  INTEGER NOT NULL,
	thread_id TEXT,
	flags     TEXT    NOT NULL,
	data      TEXT    NOT NULL,
	has_body  INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (account, folder, id)
);
PRAGMA user_version = 3;`

func TestMigrateV3KeepsData(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mail")
	db := openRaw(t, dir)
	_, err := db.Exec(v3Messages)
	must(t, err)
	data, _ := json.Marshal(&mail.Envelope{ID: "9", SortKey: 9, Subject: "kept", HasAttach: true})
	_, err = db.Exec("INSERT INTO messages (account, folder, id, sort_key, flags, data, has_body) VALUES ('acct', 'INBOX', '9', 9, '[]', ?, 1)", string(data))
	must(t, err)
	must(t, db.Close())

	s, err := Open(dir)
	must(t, err)
	defer s.Close()
	var version int
	must(t, s.db.QueryRow("PRAGMA user_version").Scan(&version))
	if version != schemaVersion {
		t.Errorf("user_version = %d, want %d", version, schemaVersion)
	}
	e, ok := s.Envelope("acct", "INBOX", "9")
	if !ok || e.Subject != "kept" || !e.HasAttach {
		t.Errorf("after migration: %+v (ok %v); want the row kept, sign from its JSON", e, ok)
	}
}

// v4Schema is schema version 4, verbatim: no per-folder cursor, no token
// save time.
const v4Schema = `
CREATE TABLE folder_state (
	account     TEXT    NOT NULL,
	folder      TEXT    NOT NULL,
	uidvalidity INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (account, folder)
);
CREATE TABLE messages (
	account    TEXT    NOT NULL,
	folder     TEXT    NOT NULL,
	id         TEXT    NOT NULL,
	sort_key   INTEGER NOT NULL,
	thread_id  TEXT,
	flags      TEXT    NOT NULL,
	data       TEXT    NOT NULL,
	has_body   INTEGER NOT NULL DEFAULT 0,
	has_attach INTEGER,
	PRIMARY KEY (account, folder, id)
);
CREATE TABLE folders (
	account TEXT PRIMARY KEY,
	data    TEXT NOT NULL
);
CREATE TABLE account_state (
	account TEXT PRIMARY KEY,
	cursor  TEXT NOT NULL DEFAULT ''
);
PRAGMA user_version = 4;`

func TestMigrateV4KeepsData(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mail")
	db := openRaw(t, dir)
	_, err := db.Exec(v4Schema)
	must(t, err)
	data, _ := json.Marshal(&mail.Envelope{ID: "m1", SortKey: 5, Subject: "kept"})
	_, err = db.Exec("INSERT INTO messages (account, folder, id, sort_key, flags, data, has_body) VALUES ('acct', 'INBOX', 'm1', 5, '[]', ?, 0)", string(data))
	must(t, err)
	_, err = db.Exec("INSERT INTO folder_state (account, folder, uidvalidity) VALUES ('acct', 'INBOX', 7)")
	must(t, err)
	_, err = db.Exec("INSERT INTO account_state (account, cursor) VALUES ('acct', 'h9')")
	must(t, err)
	must(t, db.Close())

	s, err := Open(dir)
	must(t, err)
	defer s.Close()
	var version int
	must(t, s.db.QueryRow("PRAGMA user_version").Scan(&version))
	if version != 5 || schemaVersion != 5 {
		t.Errorf("user_version = %d, schemaVersion = %d, want 5", version, schemaVersion)
	}
	if e, ok := s.Envelope("acct", "INBOX", "m1"); !ok || e.Subject != "kept" {
		t.Errorf("row lost: %+v %v", e, ok)
	}
	if c := s.Cursor("acct"); c != "h9" {
		t.Errorf("Cursor = %q, want h9", c)
	}
	if uv, _, _ := s.FolderState("acct", "INBOX"); uv != 7 {
		t.Errorf("uidvalidity = %d, want 7", uv)
	}
	if c := s.FolderCursor("acct", "INBOX"); c != "" {
		t.Errorf("FolderCursor = %q, want empty", c)
	}
	if !s.TokenSaved("acct").IsZero() {
		t.Errorf("TokenSaved = %v, want zero", s.TokenSaved("acct"))
	}
}
