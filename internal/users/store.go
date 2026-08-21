package users

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver — no cgo, static binary preserved (SPEC.md §4)
)

// Store is the panel's durable state: per-user traffic totals, presence and
// roster metadata (SPEC.md §4). SQLite keeps it crash-safe and queryable.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path. ":memory:"
// gives an ephemeral store (tests).
func Open(path string) (*Store, error) {
	dsn := path
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
		dsn = "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(wal)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// One connection: writes serialize naturally, and an in-memory database
	// stays a single database.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			email            TEXT PRIMARY KEY,        -- identity; email change = new row
			protocol         TEXT,                    -- from config parse
			security         TEXT,                    -- from config parse
			up_bytes_total   INTEGER NOT NULL DEFAULT 0,  -- durable
			down_bytes_total INTEGER NOT NULL DEFAULT 0,
			last_seen        INTEGER,                 -- unix seconds, NULL = never
			last_ips         TEXT,                    -- JSON array
			gone             INTEGER NOT NULL DEFAULT 0,  -- no longer in xray config; history kept
			first_seen       INTEGER NOT NULL
		)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// ApplyDeltas adds each user's reconciled delta to their durable totals,
// inserting rows for first-seen emails — a single transaction per poll
// (SPEC.md §4). Totals only ever grow.
func (s *Store) ApplyDeltas(ctx context.Context, deltas []Delta, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin poll transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO users (email, up_bytes_total, down_bytes_total, first_seen)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(email) DO UPDATE SET
			up_bytes_total   = up_bytes_total   + excluded.up_bytes_total,
			down_bytes_total = down_bytes_total + excluded.down_bytes_total`)
	if err != nil {
		return fmt.Errorf("prepare delta upsert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, delta := range deltas {
		if _, err := stmt.ExecContext(ctx, delta.Email, delta.Up, delta.Down, now.Unix()); err != nil {
			return fmt.Errorf("apply delta for %s: %w", delta.Email, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit poll transaction: %w", err)
	}
	return nil
}

// Users returns every known user, heaviest traffic first.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT email, protocol, security, up_bytes_total, down_bytes_total,
		       last_seen, last_ips, gone, first_seen
		FROM users
		ORDER BY up_bytes_total + down_bytes_total DESC, email`)
	if err != nil {
		return nil, fmt.Errorf("query users: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var list []User
	for rows.Next() {
		var user User
		var lastSeen sql.NullInt64
		var lastIPs sql.NullString
		var gone bool
		if err := rows.Scan(
			&user.Email, &user.Protocol, &user.Security,
			&user.UpBytesTotal, &user.DownBytesTotal,
			&lastSeen, &lastIPs, &gone, &user.FirstSeen,
		); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		if lastSeen.Valid {
			user.LastSeen = &lastSeen.Int64
		}
		if lastIPs.Valid {
			// last_ips is a JSON array (SPEC.md §4); tolerate malformed rows.
			_ = json.Unmarshal([]byte(lastIPs.String), &user.IPs)
		}
		user.Gone = gone
		list = append(list, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read users: %w", err)
	}
	return list, nil
}

// ExistingEmails reports which emails already have a row — the collector uses
// it to decide which raw counters seed new rows versus resume onto durable
// totals.
func (s *Store) ExistingEmails(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT email FROM users`)
	if err != nil {
		return nil, fmt.Errorf("query emails: %w", err)
	}
	defer func() { _ = rows.Close() }()

	known := map[string]bool{}
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, fmt.Errorf("scan email: %w", err)
		}
		known[email] = true
	}
	return known, rows.Err()
}
