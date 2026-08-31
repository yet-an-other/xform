package users

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	// The roster store (user-management spec §3): one row per adopted user —
	// the panel-held source of truth the config is rendered from. Adoption
	// only ever adds, so a hand-edited config cannot rewrite a stored
	// Client ID.
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS roster (
			email      TEXT PRIMARY KEY,        -- identity; email change = new row
			client_id  TEXT NOT NULL,           -- UUID credential
			inbounds   TEXT NOT NULL,           -- JSON array of VLESS inbound tags
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create roster schema: %w", err)
	}
	// The xray row's durable aggregate totals — panel-level state hosted here
	// because the Store owns the database file. Exactly one row (id = 1).
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS xray_totals (
			id               INTEGER PRIMARY KEY CHECK (id = 1),
			up_bytes_total   INTEGER NOT NULL,
			down_bytes_total INTEGER NOT NULL
		)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create totals schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// lastSeenGrow is the shared upsert clause: last_seen takes the newer of
// the stored and incoming values — never regressing, and never turning
// NULL into a timestamp when the incoming report carries none.
const lastSeenGrow = `CASE WHEN excluded.last_seen IS NULL THEN last_seen
                        ELSE MAX(COALESCE(last_seen, 0), excluded.last_seen) END`

// ApplyPoll flushes one poll's state — reconciled traffic deltas, presence
// observations, and, when the xray config changed, the parsed roster — in a
// single transaction (SPEC.md §4). Totals only ever
// grow and last_seen never regresses: movement inside the poll's window
// marks the user seen now (the traffic-delta heuristic, SPEC.md §3), and an
// online user's row records their connection set as the last-known IPs.
//
// A non-nil roster syncs the config-defined labels and adopts the config's
// VLESS clients: roster emails gain rows (totals at zero until their first
// traffic) and protocol · security, every user edited out of the config
// becomes gone — retained with their history, never deleted (SPEC.md §4) —
// and each client joins the roster store with its Client ID and inbound
// attachments. A nil roster leaves the roster alone.
func (s *Store) ApplyPoll(ctx context.Context, deltas []Delta, presence []Presence, roster *RosterParse, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin poll transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	deltaStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO users (email, up_bytes_total, down_bytes_total, last_seen, first_seen)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(email) DO UPDATE SET
			up_bytes_total   = up_bytes_total   + excluded.up_bytes_total,
			down_bytes_total = down_bytes_total + excluded.down_bytes_total,
			last_seen        = `+lastSeenGrow)
	if err != nil {
		return fmt.Errorf("prepare delta upsert: %w", err)
	}
	defer func() { _ = deltaStmt.Close() }()

	for _, delta := range deltas {
		seenNow := sql.NullInt64{Int64: now.Unix(), Valid: delta.SeenNow}
		if _, err := deltaStmt.ExecContext(ctx, delta.Email, delta.Up, delta.Down, seenNow, now.Unix()); err != nil {
			return fmt.Errorf("apply delta for %s: %w", delta.Email, err)
		}
	}

	// An online user may have no traffic counters yet, so presence upserts
	// too. last_ips tracks the latest observation; a report without IPs
	// keeps the last-known set.
	presenceStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO users (email, last_seen, last_ips, first_seen)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(email) DO UPDATE SET
			last_seen = `+lastSeenGrow+`,
			last_ips  = COALESCE(excluded.last_ips, last_ips)`)
	if err != nil {
		return fmt.Errorf("prepare presence upsert: %w", err)
	}
	defer func() { _ = presenceStmt.Close() }()

	for _, user := range presence {
		lastSeen := sql.NullInt64{Int64: user.LastSeen, Valid: user.LastSeen > 0}
		var lastIPs sql.NullString
		if len(user.IPs) > 0 {
			ips, err := json.Marshal(user.IPs)
			if err != nil {
				return fmt.Errorf("encode IPs for %s: %w", user.Email, err)
			}
			lastIPs = sql.NullString{String: string(ips), Valid: true}
		}
		if _, err := presenceStmt.ExecContext(ctx, user.Email, lastSeen, lastIPs, now.Unix()); err != nil {
			return fmt.Errorf("apply presence for %s: %w", user.Email, err)
		}
	}

	if roster != nil {
		if err := syncLabels(ctx, tx, roster.Labels, now); err != nil {
			return err
		}
		if err := adoptClients(ctx, tx, roster.Clients, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit poll transaction: %w", err)
	}
	return nil
}

// syncLabels applies a fresh config parse's labels inside the poll
// transaction: everyone not in the config becomes gone (a no-op for rows
// already gone), then roster emails are upserted — new users appear with
// zero totals, returning users lose the gone flag, and labels follow the
// config.
func syncLabels(ctx context.Context, tx *sql.Tx, roster map[string]RosterUser, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE users SET gone = 1 WHERE gone = 0`); err != nil {
		return fmt.Errorf("mark gone users: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO users (email, protocol, security, gone, first_seen)
		VALUES (?, ?, ?, 0, ?)
		ON CONFLICT(email) DO UPDATE SET
			protocol = excluded.protocol,
			security = excluded.security,
			gone     = 0`)
	if err != nil {
		return fmt.Errorf("prepare roster upsert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for email, user := range roster {
		if _, err := stmt.ExecContext(ctx, email, user.Protocol, user.Security, now.Unix()); err != nil {
			return fmt.Errorf("sync roster for %s: %w", email, err)
		}
	}
	return nil
}

// adoptClients brings the config's VLESS clients into the roster store,
// additively and idempotently (user-management spec §4): a new email lands
// with its config Client ID and attachments; a known email only ever gains
// attachments it did not have — never a rewritten Client ID, because the
// store is the source of truth and the hand edit is drift. An unchanged
// re-read writes nothing. Attachments not in the config are left alone:
// re-applying them is convergence's job, not adoption's.
func adoptClients(ctx context.Context, tx *sql.Tx, clients map[string]RosterClient, now time.Time) error {
	stored := map[string][]string{}
	rows, err := tx.QueryContext(ctx, `SELECT email, inbounds FROM roster`)
	if err != nil {
		return fmt.Errorf("read roster for adoption: %w", err)
	}
	for rows.Next() {
		var email, inbounds string
		if err := rows.Scan(&email, &inbounds); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan roster row: %w", err)
		}
		// inbounds is a JSON array; tolerate malformed rows.
		var tags []string
		_ = json.Unmarshal([]byte(inbounds), &tags)
		stored[email] = tags
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read roster rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close roster rows: %w", err)
	}

	// Sorted emails keep the write order deterministic.
	emails := make([]string, 0, len(clients))
	for email := range clients {
		emails = append(emails, email)
	}
	slices.Sort(emails)

	insert, err := tx.PrepareContext(ctx, `
		INSERT INTO roster (email, client_id, inbounds, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare roster adopt: %w", err)
	}
	defer func() { _ = insert.Close() }()
	update, err := tx.PrepareContext(ctx, `
		UPDATE roster SET inbounds = ?, updated_at = ? WHERE email = ?`)
	if err != nil {
		return fmt.Errorf("prepare roster attachment update: %w", err)
	}
	defer func() { _ = update.Close() }()

	stamp := now.Unix()
	for _, email := range emails {
		client := clients[email]
		if client.Inbounds == nil {
			client.Inbounds = []string{}
		}
		inbounds, err := json.Marshal(client.Inbounds)
		if err != nil {
			return fmt.Errorf("encode attachments for %s: %w", email, err)
		}
		tags, known := stored[email]
		if !known {
			if _, err := insert.ExecContext(ctx, email, client.ClientID, string(inbounds), stamp, stamp); err != nil {
				return fmt.Errorf("adopt %s: %w", email, err)
			}
			continue
		}
		merged := slices.Clone(tags)
		for _, tag := range client.Inbounds {
			if !slices.Contains(merged, tag) {
				merged = append(merged, tag)
			}
		}
		if len(merged) == len(tags) {
			continue // an unchanged re-read writes nothing
		}
		encoded, err := json.Marshal(merged)
		if err != nil {
			return fmt.Errorf("encode attachments for %s: %w", email, err)
		}
		if _, err := update.ExecContext(ctx, string(encoded), stamp, email); err != nil {
			return fmt.Errorf("adopt attachments for %s: %w", email, err)
		}
	}
	return nil
}

// Users returns every known user, heaviest traffic first. Roster fields
// (Client ID, inbounds) join from the roster store: null for users the
// config never adopted.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT u.email, u.protocol, u.security, u.up_bytes_total, u.down_bytes_total,
		       u.last_seen, u.last_ips, u.gone, u.first_seen,
		       r.client_id, r.inbounds
		FROM users u
		LEFT JOIN roster r ON r.email = u.email
		ORDER BY u.up_bytes_total + u.down_bytes_total DESC, u.email`)
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
		var clientID sql.NullString
		var inbounds sql.NullString
		if err := rows.Scan(
			&user.Email, &user.Protocol, &user.Security,
			&user.UpBytesTotal, &user.DownBytesTotal,
			&lastSeen, &lastIPs, &gone, &user.FirstSeen,
			&clientID, &inbounds,
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
		if clientID.Valid {
			user.ClientID = &clientID.String
		}
		if inbounds.Valid {
			// inbounds is a JSON array of VLESS inbound tags (user-management
			// spec §3); tolerate malformed rows.
			_ = json.Unmarshal([]byte(inbounds.String), &user.Inbounds)
		}
		user.Gone = gone
		list = append(list, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read users: %w", err)
	}
	return list, nil
}

// LoadTrafficTotals returns the durable aggregate xray traffic totals;
// found is false on the first boot with persistence (no row yet).
func (s *Store) LoadTrafficTotals(ctx context.Context) (up, down uint64, found bool, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT up_bytes_total, down_bytes_total FROM xray_totals WHERE id = 1`).Scan(&up, &down)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, fmt.Errorf("load traffic totals: %w", err)
	}
	return up, down, true, nil
}

// SaveTrafficTotals persists the durable aggregate xray traffic totals
// (upserts the single row).
func (s *Store) SaveTrafficTotals(ctx context.Context, up, down uint64) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO xray_totals (id, up_bytes_total, down_bytes_total) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			up_bytes_total   = excluded.up_bytes_total,
			down_bytes_total = excluded.down_bytes_total`, up, down); err != nil {
		return fmt.Errorf("save traffic totals: %w", err)
	}
	return nil
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
