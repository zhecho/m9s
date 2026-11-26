package store

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/zhecho/m9s/internal/mail"
)

// migrateV2 moves a version-2 index (messages keyed by an IMAP UID) to
// version 3 (text ids, sort keys, threads) without losing anything: the UID
// becomes both the id and the sort key, and .eml files stay where they are,
// since an IMAP body path is dir/<account>/<folder>/<id>.eml either way.
// It runs in one transaction; on error nothing has changed and the caller
// rebuilds the index instead.
func migrateV2(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS messages_v3",
		"CREATE TABLE messages_v3 (" + messagesColumns + ")",
		`INSERT INTO messages_v3 (account, folder, id, sort_key, thread_id, flags, data, has_body)
			SELECT account, folder, CAST(uid AS TEXT), uid, NULL, flags, data, has_body FROM messages`,
		"DROP TABLE messages",
		"ALTER TABLE messages_v3 RENAME TO messages",
		`CREATE TABLE IF NOT EXISTS account_state (
			account TEXT PRIMARY KEY,
			cursor  TEXT NOT NULL DEFAULT ''
		)`,
		"PRAGMA user_version = 4", // migrateV4 takes it on from here
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// migrateV3 adds the attachment sign to a version-3 index. Every row starts
// with the sign unknown; FillAttachSigns works it out from the bodies on
// disk. On error nothing has changed and the caller rebuilds the index.
func migrateV3(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit
	for _, stmt := range []string{
		"ALTER TABLE messages ADD COLUMN has_attach INTEGER",
		"PRAGMA user_version = 4", // migrateV4 takes it on from here
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// migrateV4 adds a per-folder sync cursor (Microsoft's delta links) and
// the account's token save time to a version-4 index. Tables an index of
// that version lacks are made in their version-4 shape first. On error
// nothing has changed and the caller rebuilds the index.
func migrateV4(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS folder_state (
			account     TEXT    NOT NULL,
			folder      TEXT    NOT NULL,
			uidvalidity INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (account, folder)
		)`,
		`CREATE TABLE IF NOT EXISTS account_state (
			account TEXT PRIMARY KEY,
			cursor  TEXT NOT NULL DEFAULT ''
		)`,
		"ALTER TABLE folder_state ADD COLUMN cursor TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE account_state ADD COLUMN token_saved INTEGER NOT NULL DEFAULT 0",
		"PRAGMA user_version = 5", // migrateV5 takes it on from here
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// migrateV5 adds the filter's search column to a version-5 index and fills
// it from each row's envelope. An index migrated from version 2 has the
// column already. On error nothing has changed and the caller rebuilds the
// index.
func migrateV5(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit
	var has int
	if err := tx.QueryRow("SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'match_text'").Scan(&has); err != nil {
		return err
	}
	if has == 0 {
		if _, err := tx.Exec("ALTER TABLE messages ADD COLUMN match_text TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	rows, err := tx.Query("SELECT rowid, data FROM messages")
	if err != nil {
		return err
	}
	type fill struct {
		rowid int64
		text  string
	}
	var fills []fill
	for rows.Next() {
		var f fill
		var data string
		if err := rows.Scan(&f.rowid, &data); err != nil {
			rows.Close()
			return err
		}
		var env mail.Envelope
		if json.Unmarshal([]byte(data), &env) == nil {
			f.text = matchText(&env)
			fills = append(fills, f)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, f := range fills {
		if _, err := tx.Exec("UPDATE messages SET match_text = ? WHERE rowid = ?", f.text, f.rowid); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}
