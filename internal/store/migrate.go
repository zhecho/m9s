package store

import (
	"database/sql"
	"fmt"
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
		fmt.Sprintf("PRAGMA user_version = %d", schemaVersion),
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}
