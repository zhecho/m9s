package draft

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
)

// Store persists drafts as .eml + .meta.json pairs on the local filesystem.
type Store struct {
	baseDir string
}

// NewStore creates a Store rooted at baseDir.
func NewStore(baseDir string) *Store {
	return &Store{baseDir: baseDir}
}

func (s *Store) accountDir(account string) string {
	return filepath.Join(s.baseDir, account)
}

func (s *Store) emlPath(account, id string) string {
	return filepath.Join(s.accountDir(account), id+".eml")
}

func (s *Store) metaPath(account, id string) string {
	return filepath.Join(s.accountDir(account), id+".meta.json")
}

// Save writes a draft to disk and returns its ID.
func (s *Store) Save(account string, d smtpadapter.Draft) (string, error) {
	dir := s.accountDir(account)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("draft mkdir: %w", err)
	}
	id := newID()
	eml, err := smtpadapter.FormatDraft(d)
	if err != nil {
		return "", fmt.Errorf("format draft: %w", err)
	}
	if err := os.WriteFile(s.emlPath(account, id), eml, 0600); err != nil {
		return "", fmt.Errorf("write eml: %w", err)
	}
	now := time.Now().UTC()
	meta := Meta{
		ID:        id,
		Account:   account,
		Subject:   d.Subject,
		Dirty:     true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	metaData, err := json.Marshal(meta)
	if err != nil {
		_ = os.Remove(s.emlPath(account, id))
		return "", fmt.Errorf("marshal meta: %w", err)
	}
	if err := os.WriteFile(s.metaPath(account, id), metaData, 0600); err != nil {
		_ = os.Remove(s.emlPath(account, id))
		return "", fmt.Errorf("write meta: %w", err)
	}
	return id, nil
}

// Update replaces the content of an existing draft, keeping its id. The
// draft becomes dirty and forgets its server copy, which no longer matches;
// the id of that copy is returned ("" if none) so the caller can remove it.
func (s *Store) Update(account, id string, d smtpadapter.Draft) (prevID string, err error) {
	data, err := os.ReadFile(s.metaPath(account, id))
	if err != nil {
		return "", fmt.Errorf("read meta: %w", err)
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return "", fmt.Errorf("parse meta: %w", err)
	}
	eml, err := smtpadapter.FormatDraft(d)
	if err != nil {
		return "", fmt.Errorf("format draft: %w", err)
	}
	if err := os.WriteFile(s.emlPath(account, id), eml, 0600); err != nil {
		return "", fmt.Errorf("write eml: %w", err)
	}
	prevID = m.SyncedID
	m.Subject = d.Subject
	m.SyncedID = ""
	m.Dirty = true
	m.UpdatedAt = time.Now().UTC()
	updated, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal meta: %w", err)
	}
	return prevID, os.WriteFile(s.metaPath(account, id), updated, 0600)
}

// Load reads and parses the draft .eml for the given id.
func (s *Store) Load(account, id string) (smtpadapter.Draft, error) {
	data, err := os.ReadFile(s.emlPath(account, id))
	if err != nil {
		return smtpadapter.Draft{}, fmt.Errorf("read eml: %w", err)
	}
	return smtpadapter.ParseDraft(data)
}

// Delete removes the .eml and .meta.json for the given id.
func (s *Store) Delete(account, id string) error {
	_ = os.Remove(s.emlPath(account, id))
	_ = os.Remove(s.metaPath(account, id))
	return nil
}

// List returns metadata for all drafts of the given account.
func (s *Store) List(account string) ([]Meta, error) {
	dir := s.accountDir(account)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("readdir: %w", err)
	}
	var metas []Meta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".meta.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var m Meta
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		metas = append(metas, m)
	}
	return metas, nil
}

// SetLocalOnly marks a draft as kept on this machine only (never uploaded),
// or clears that mark.
func (s *Store) SetLocalOnly(account, id string, localOnly bool) error {
	data, err := os.ReadFile(s.metaPath(account, id))
	if err != nil {
		return fmt.Errorf("read meta: %w", err)
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("parse meta: %w", err)
	}
	m.LocalOnly = localOnly
	m.UpdatedAt = time.Now().UTC()
	updated, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	return os.WriteFile(s.metaPath(account, id), updated, 0600)
}

// MarkDirty sets dirty=true on the meta file, signalling the draft needs re-upload.
func (s *Store) MarkDirty(account, id string) error {
	data, err := os.ReadFile(s.metaPath(account, id))
	if err != nil {
		return fmt.Errorf("read meta: %w", err)
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("parse meta: %w", err)
	}
	m.Dirty = true
	m.UpdatedAt = time.Now().UTC()
	updated, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	return os.WriteFile(s.metaPath(account, id), updated, 0600)
}

// MarkSynced records the server copy id and clears the dirty flag.
func (s *Store) MarkSynced(account, id, syncedID string) error {
	data, err := os.ReadFile(s.metaPath(account, id))
	if err != nil {
		return fmt.Errorf("read meta: %w", err)
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("parse meta: %w", err)
	}
	m.SyncedID = syncedID
	m.Dirty = false
	m.UpdatedAt = time.Now().UTC()
	updated, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	return os.WriteFile(s.metaPath(account, id), updated, 0600)
}
