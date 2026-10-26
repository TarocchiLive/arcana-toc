// Package store provides SQLite persistence for queue entries, playback history,
// and service state. Platform adapters own credentials separately.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

// QueueEntry is one business-queue row. Platform+TrackID identify the song
// (invariant 2); ID is the independent entry identity that allows duplicate
// requests of the same song. Signed playback URLs are never stored here.
type QueueEntry struct {
	ID          int64     `json:"id"`
	Platform    string    `json:"platform"`
	TrackID     string    `json:"track_id"`
	Title       string    `json:"title"`
	Artist      string    `json:"artist"`
	VIP         bool      `json:"vip"`
	Status      string    `json:"status"` // pending|playing|done|failed|skipped
	RequestedBy string    `json:"requested_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// HistoryEntry is an immutable snapshot of a queue entry that started playback.
type HistoryEntry struct {
	ID          int64     `json:"id"`
	QueueID     int64     `json:"queue_id"`
	Platform    string    `json:"platform"`
	TrackID     string    `json:"track_id"`
	Title       string    `json:"title"`
	Artist      string    `json:"artist"`
	VIP         bool      `json:"vip"`
	Outcome     string    `json:"outcome"`
	RequestedBy string    `json:"requested_by"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
}

var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS queue_entries (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		platform TEXT NOT NULL,
		track_id TEXT NOT NULL,
		title TEXT NOT NULL DEFAULT '',
		artist TEXT NOT NULL DEFAULT '',
		vip INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'pending',
		requested_by TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_queue_entries_status ON queue_entries(status)`,
	`CREATE TABLE IF NOT EXISTS service_state (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS playback_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		queue_id INTEGER NOT NULL,
		platform TEXT NOT NULL,
		track_id TEXT NOT NULL,
		title TEXT NOT NULL DEFAULT '',
		artist TEXT NOT NULL DEFAULT '',
		vip INTEGER NOT NULL DEFAULT 0,
		outcome TEXT NOT NULL,
		requested_by TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL,
		finished_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_playback_history_finished ON playback_history(finished_at DESC, id DESC)`,
}

// Store wraps the SQLite handle.
type Store struct {
	db *sql.DB
}

// Open creates dataDir, opens the database and initializes its schema.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	info, err := os.Lstat(dataDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("unsafe data directory")
	}
	// MkdirAll keeps existing modes; tighten in case the dir pre-exists loose.
	if err := os.Chmod(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("restrict data dir permissions: %w", err)
	}
	dbPath := filepath.Join(dataDir, "arcanatoc.db")
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			return nil, errors.New("unsafe SQLite file")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	fd, err := unix.Open(dbPath, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "SQLite state")
	if err := errors.Join(f.Chmod(0600), f.Close()); err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: dbPath, RawQuery: "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// Single connection: SQLite allows one writer; this avoids BUSY storms.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := os.Chmod(dbPath, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("restrict db permissions: %w", err)
	}
	s := &Store{db: db}
	if err := s.initializeSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) initializeSchema() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("initialize schema: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range schemaStatements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("initialize schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("initialize schema: %w", err)
	}
	return nil
}

// AddQueueEntry inserts a pending entry and returns its independent id.
func (s *Store) AddQueueEntry(e QueueEntry) (int64, error) {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = e.CreatedAt
	}
	if e.Status == "" {
		e.Status = "pending"
	}
	res, err := s.db.Exec(`INSERT INTO queue_entries
		(platform, track_id, title, artist, vip, status, requested_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Platform, e.TrackID, e.Title, e.Artist, e.VIP, e.Status, e.RequestedBy,
		stamp(e.CreatedAt), stamp(e.UpdatedAt))
	if err != nil {
		return 0, fmt.Errorf("insert queue entry: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("queue entry id: %w", err)
	}
	return id, nil
}

// ReplaceQueue atomically replaces every queue row, preserving the interrupted
// playing entry in history. Input order and duplicate tracks are retained.
func (s *Store) ReplaceQueue(entries []QueueEntry) error {
	if len(entries) == 0 {
		return errors.New("replace queue: empty playlist")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("replace queue: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	at := now()
	if _, err := tx.Exec(`INSERT INTO playback_history
		(queue_id, platform, track_id, title, artist, vip, outcome, requested_by, started_at, finished_at)
		SELECT id, platform, track_id, title, artist, vip, 'replaced', requested_by, updated_at, ?
		FROM queue_entries WHERE status = 'playing'`, at); err != nil {
		return fmt.Errorf("archive replaced playback: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM queue_entries`); err != nil {
		return fmt.Errorf("clear replaced queue: %w", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO queue_entries
		(platform, track_id, title, artist, vip, status, requested_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'pending', ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare replacement queue: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for _, entry := range entries {
		if entry.Platform == "" || entry.TrackID == "" {
			return errors.New("replace queue: invalid track")
		}
		if _, err := stmt.Exec(entry.Platform, entry.TrackID, entry.Title, entry.Artist,
			entry.VIP, entry.RequestedBy, at, at); err != nil {
			return fmt.Errorf("insert replacement queue: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replacement queue: %w", err)
	}
	return nil
}

// QueueEntries returns entries ordered by id; statuses filters when non-empty.
func (s *Store) QueueEntries(statuses ...string) ([]QueueEntry, error) {
	query := `SELECT id, platform, track_id, title, artist, vip, status, requested_by, created_at, updated_at
		FROM queue_entries`
	var args []any
	if len(statuses) > 0 {
		query += fmt.Sprintf(" WHERE status IN (%s)", placeholders(len(statuses)))
		for _, st := range statuses {
			args = append(args, st)
		}
	}
	query += " ORDER BY id"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("select queue entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []QueueEntry
	for rows.Next() {
		var e QueueEntry
		var created, updated string
		if err := rows.Scan(&e.ID, &e.Platform, &e.TrackID, &e.Title, &e.Artist, &e.VIP, &e.Status,
			&e.RequestedBy, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan queue entry: %w", err)
		}
		if e.CreatedAt, err = parseStamp(created); err != nil {
			return nil, err
		}
		if e.UpdatedAt, err = parseStamp(updated); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpdateQueueStatus transitions one entry and bumps its updated_at.
func (s *Store) UpdateQueueStatus(id int64, status string) error {
	res, err := s.db.Exec(`UPDATE queue_entries SET status = ?, updated_at = ? WHERE id = ?`,
		status, now(), id)
	if err != nil {
		return fmt.Errorf("update queue entry %d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("update queue entry %d: %w", id, os.ErrNotExist)
	}
	return nil
}

// DeleteQueueEntry removes an entry regardless of its status.
func (s *Store) DeleteQueueEntry(id int64) error {
	res, err := s.db.Exec(`DELETE FROM queue_entries WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete queue entry %d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("delete queue entry %d: %w", id, os.ErrNotExist)
	}
	return nil
}

// ArchiveQueueEntry atomically snapshots one active queue entry into playback
// history and removes it from the active queue.
func (s *Store) ArchiveQueueEntry(id int64, outcome string) error {
	if outcome == "" {
		return errors.New("archive queue entry: outcome is empty")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("archive queue entry %d: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	finished := time.Now().UTC()
	result, err := tx.Exec(`INSERT INTO playback_history
		(queue_id, platform, track_id, title, artist, vip, outcome, requested_by, started_at, finished_at)
		SELECT id, platform, track_id, title, artist, vip, ?, requested_by, updated_at, ?
		FROM queue_entries WHERE id = ?`, outcome, stamp(finished), id)
	if err != nil {
		return fmt.Errorf("archive queue entry %d: %w", id, err)
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return fmt.Errorf("archive queue entry %d: %w", id, os.ErrNotExist)
	}
	if _, err := tx.Exec(`DELETE FROM queue_entries WHERE id = ?`, id); err != nil {
		return fmt.Errorf("remove archived queue entry %d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit archived queue entry %d: %w", id, err)
	}
	return nil
}

// HistoryEntries returns immutable playback history, newest completion first.
func (s *Store) HistoryEntries() ([]HistoryEntry, error) {
	rows, err := s.db.Query(`SELECT id, queue_id, platform, track_id, title, artist, vip, outcome, requested_by, started_at, finished_at
		FROM playback_history ORDER BY finished_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("select playback history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var entries []HistoryEntry
	for rows.Next() {
		var entry HistoryEntry
		var started, finished string
		if err := rows.Scan(&entry.ID, &entry.QueueID, &entry.Platform, &entry.TrackID, &entry.Title, &entry.Artist, &entry.VIP, &entry.Outcome, &entry.RequestedBy, &started, &finished); err != nil {
			return nil, fmt.Errorf("scan playback history: %w", err)
		}
		if entry.StartedAt, err = parseStamp(started); err != nil {
			return nil, err
		}
		if entry.FinishedAt, err = parseStamp(finished); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// SetState marshals value to JSON under key.
func (s *Store) SetState(key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal state %q: %w", key, err)
	}
	if _, err := s.db.Exec(`INSERT INTO service_state (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, string(raw), now()); err != nil {
		return fmt.Errorf("set state %q: %w", key, err)
	}
	return nil
}

// GetState unmarshals key into dst and reports whether it existed.
func (s *Store) GetState(key string, dst any) (bool, error) {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM service_state WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get state %q: %w", key, err)
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return false, fmt.Errorf("unmarshal state %q: %w", key, err)
	}
	return true, nil
}

func placeholders(n int) string {
	out := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			out += ","
		}
		out += "?"
	}
	return out
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseStamp(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", s, err)
	}
	return t, nil
}

func now() string { return stamp(time.Now()) }
