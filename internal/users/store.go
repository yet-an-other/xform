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
	"strings"
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
			disabled         INTEGER NOT NULL DEFAULT 0,  -- off the Roster (ADR-0007); history kept
			first_seen       INTEGER NOT NULL
		)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	// The roster store (user-management spec §3, ADR-0007): one row per
	// managed user — the panel-held source of truth the config is rendered
	// from. A disabled user's row stays, flagged disabled (CONTEXT.md: never
	// erased); re-adding or re-enabling the email revives it. Adoption only
	// ever adds, so a hand-edited config cannot rewrite a stored Client ID.
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS roster (
			email      TEXT PRIMARY KEY,        -- identity; email change = new row
			client_id  TEXT NOT NULL,           -- UUID credential
			inbounds   TEXT NOT NULL,           -- JSON array of VLESS inbound tags
			disabled   INTEGER NOT NULL DEFAULT 0,  -- off the Roster (ADR-0007); history kept
			deleting   INTEGER NOT NULL DEFAULT 0,  -- purge in progress (ADR-0007): apply, then erase (issue #59)
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create roster schema: %w", err)
	}
	// Databases from before the disable rename carry a gone column; it
	// becomes disabled in place, flags intact (ADR-0007).
	for _, table := range []string{"users", "roster"} {
		if err := migrateGoneToDisabled(db, table); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	// Databases from before the delete act carry no deleting column; it is
	// added in place, every row defaulting to not-deleting (ADR-0007, issue #59).
	if err := migrateAddDeleting(db); err != nil {
		_ = db.Close()
		return nil, err
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

// migrateGoneToDisabled renames one table's gone column to disabled in
// place; a table carrying neither column gains disabled. A database
// already on disabled is untouched (ADR-0007).
func migrateGoneToDisabled(db *sql.DB, table string) error {
	columns, err := tableColumns(db, table)
	if err != nil {
		return fmt.Errorf("inspect %s schema: %w", table, err)
	}
	switch {
	case columns["gone"]:
		if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE %s RENAME COLUMN gone TO disabled`, table)); err != nil {
			return fmt.Errorf("migrate %s schema: %w", table, err)
		}
	case !columns["disabled"]:
		if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0`, table)); err != nil {
			return fmt.Errorf("migrate %s schema: %w", table, err)
		}
	}
	return nil
}

// tableColumns returns one table's column names — the schema migrations'
// shared PRAGMA scan.
func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return nil, fmt.Errorf("inspect %s schema: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return nil, fmt.Errorf("inspect %s schema: %w", table, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect %s schema: %w", table, err)
	}
	return columns, nil
}

// migrateAddDeleting adds the roster's deleting column when a database
// predates the delete act (issue #59); a table already carrying it is
// untouched.
func migrateAddDeleting(db *sql.DB) error {
	columns, err := tableColumns(db, "roster")
	if err != nil {
		return fmt.Errorf("inspect roster schema: %w", err)
	}
	if !columns["deleting"] {
		if _, err := db.Exec(`ALTER TABLE roster ADD COLUMN deleting INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("migrate roster schema: %w", err)
		}
	}
	return nil
}

// ErrEmailTaken / ErrClientIDTaken are the roster's two uniqueness rules
// (user-management spec §5): email conflicts case-insensitively because the
// email IS the identity, Client IDs case-insensitively too because xray's
// auth index silently overwrites on same-UUID-different-email.
// ErrRosterNotFound marks a mutation naming an email the roster does not
// carry.
var (
	ErrEmailTaken     = errors.New("email taken")
	ErrClientIDTaken  = errors.New("client ID taken")
	ErrRosterNotFound = errors.New("roster record not found")
)

// NewRosterUser is one panel-added user to store. Protocol and Security are
// the table labels for the row until the next config parse resyncs them.
type NewRosterUser struct {
	Email    string
	ClientID string
	Inbounds []string
	Protocol string
	Security string
}

// RosterRecord is the stored roster row returned to the mutation API.
type RosterRecord struct {
	Email     string   `json:"email"`
	ClientID  string   `json:"client_id"`
	Inbounds  []string `json:"inbounds"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
}

// AddRosterUser stores one panel-added user: the roster row, plus a users
// row so the dashboard shows them immediately — labelled, live, totals
// at zero. A returning disabled user's email rejoins the existing row and its
// history (user-management spec §3); first_seen, totals, and last_seen are
// never touched. A removed user's flagged roster row is revived — shed of
// disabled, carrying the new credential. Conflicts write nothing.
func (s *Store) AddRosterUser(ctx context.Context, user NewRosterUser, now time.Time) (RosterRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RosterRecord{}, fmt.Errorf("begin add transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var taken string
	var existing struct {
		email    string
		disabled bool
		deleting bool
	}
	switch err := tx.QueryRowContext(ctx,
		`SELECT email, disabled, deleting FROM roster WHERE lower(email) = lower(?)`, user.Email,
	).Scan(&existing.email, &existing.disabled, &existing.deleting); {
	case err == nil && existing.deleting:
		// A delete in progress keeps its claims until the purge lands — a
		// failed apply may still have a live credential out there, so
		// re-adding the email waits (ADR-0007 two-phase delete, issue #59).
		return RosterRecord{}, fmt.Errorf("%w: %s (delete in progress)", ErrEmailTaken, existing.email)
	case err == nil && !existing.disabled:
		return RosterRecord{}, fmt.Errorf("%w: %s", ErrEmailTaken, existing.email)
	case !errors.Is(err, sql.ErrNoRows) && err != nil:
		return RosterRecord{}, fmt.Errorf("check email uniqueness: %w", err)
	}
	switch err := tx.QueryRowContext(ctx, `SELECT email FROM roster WHERE lower(client_id) = lower(?)`, user.ClientID).Scan(&taken); {
	case err == nil:
		// The user's own disabled row may be revived under its old credential;
		// any other holder — disabled or not — is a conflict.
		if !strings.EqualFold(taken, user.Email) || !existing.disabled {
			return RosterRecord{}, fmt.Errorf("%w: %s", ErrClientIDTaken, taken)
		}
	case !errors.Is(err, sql.ErrNoRows):
		return RosterRecord{}, fmt.Errorf("check client ID uniqueness: %w", err)
	}

	inbounds := user.Inbounds
	if inbounds == nil {
		inbounds = []string{}
	}
	encoded, err := json.Marshal(inbounds)
	if err != nil {
		return RosterRecord{}, fmt.Errorf("encode attachments: %w", err)
	}
	stamp := now.Unix()
	created := stamp
	if existing.disabled { // revive the flagged row, creation preserved
		if err := tx.QueryRowContext(ctx,
			`SELECT created_at FROM roster WHERE lower(email) = lower(?)`, user.Email).Scan(&created); err != nil {
			return RosterRecord{}, fmt.Errorf("read creation for revive: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE roster SET client_id = ?, inbounds = ?, disabled = 0, updated_at = ?
			WHERE lower(email) = lower(?)`,
			user.ClientID, string(encoded), stamp, user.Email); err != nil {
			return RosterRecord{}, fmt.Errorf("revive roster row: %w", err)
		}
	} else if _, err := tx.ExecContext(ctx, `
		INSERT INTO roster (email, client_id, inbounds, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`, user.Email, user.ClientID, string(encoded), stamp, stamp); err != nil {
		return RosterRecord{}, fmt.Errorf("insert roster row: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO users (email, protocol, security, disabled, first_seen)
		VALUES (?, ?, ?, 0, ?)
		ON CONFLICT(email) DO UPDATE SET
			protocol = excluded.protocol,
			security = excluded.security,
			disabled = 0`, user.Email, user.Protocol, user.Security, stamp); err != nil {
		return RosterRecord{}, fmt.Errorf("upsert user row: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RosterRecord{}, fmt.Errorf("commit add transaction: %w", err)
	}
	return RosterRecord{
		Email: user.Email, ClientID: user.ClientID, Inbounds: inbounds,
		CreatedAt: created, UpdatedAt: stamp,
	}, nil
}

// DisableRosterUser takes one user off the Roster (user-management spec
// §3–§4, ADR-0007, CONTEXT.md Disabled user): the roster row is flagged
// disabled — never erased — and the dashboard row keeps its history behind
// the disabled badge. Idempotent: an already-disabled (or never-known)
// email writes nothing.
func (s *Store) DisableRosterUser(ctx context.Context, email string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin disable transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		UPDATE roster SET disabled = 1, updated_at = ? WHERE lower(email) = lower(?) AND disabled = 0 AND deleting = 0`,
		now.Unix(), email); err != nil {
		return fmt.Errorf("flag roster row disabled: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE users SET disabled = 1 WHERE lower(email) = lower(?) AND disabled = 0`, email); err != nil {
		return fmt.Errorf("mark user row disabled: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit disable transaction: %w", err)
	}
	return nil
}

// MarkRosterDeleting flags one roster row deleting (ADR-0007 two-phase
// delete, phase one — issue #59): disabled at once — off the Roster, the
// dashboard row renders disabled — and marked so the purge follows once
// the removal applies. The email and its Client ID stay claimed until the
// purge erases the rows. Idempotent: an already-deleting (or never-known)
// email writes nothing.
func (s *Store) MarkRosterDeleting(ctx context.Context, email string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		UPDATE roster SET disabled = 1, deleting = 1, updated_at = ?
		WHERE lower(email) = lower(?) AND deleting = 0`,
		now.Unix(), email); err != nil {
		return fmt.Errorf("flag roster row deleting: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE users SET disabled = 1 WHERE lower(email) = lower(?) AND disabled = 0`, email); err != nil {
		return fmt.Errorf("mark user row disabled: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete transaction: %w", err)
	}
	return nil
}

// PurgeRosterUser erases every stored trace keyed by one email (ADR-0007
// two-phase delete, phase two — issue #59): the roster row and the users
// row with its durable totals, last seen, and presence. The xray-wide
// totals are panel state, not the user's — untouched. Deleting rows only:
// a live or disabled row is not a purge target. Idempotent: a purged (or
// never-known) email writes nothing.
func (s *Store) PurgeRosterUser(ctx context.Context, email string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin purge transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Deleting rows only, both tables: the users row goes first — gated on
	// the roster's deleting mark, so a live or disabled user is never a
	// purge target — and the roster row follows.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM users WHERE lower(email) = lower(?) AND EXISTS (
			SELECT 1 FROM roster r WHERE lower(r.email) = lower(users.email) AND r.deleting = 1)`, email); err != nil {
		return fmt.Errorf("purge user row: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM roster WHERE lower(email) = lower(?) AND deleting = 1`, email); err != nil {
		return fmt.Errorf("purge roster row: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit purge transaction: %w", err)
	}
	return nil
}

// DeletingRosterRecords returns every roster row awaiting its purge — the
// startup recovery read that re-queues a delete interrupted by a restart
// (issue #59). Ordered by email for deterministic writes.
func (s *Store) DeletingRosterRecords(ctx context.Context) ([]RosterRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT email, client_id, inbounds FROM roster WHERE deleting = 1 ORDER BY email`)
	if err != nil {
		return nil, fmt.Errorf("query deleting records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var records []RosterRecord
	for rows.Next() {
		var record RosterRecord
		var inbounds string
		if err := rows.Scan(&record.Email, &record.ClientID, &inbounds); err != nil {
			return nil, fmt.Errorf("scan deleting record: %w", err)
		}
		record.Inbounds = []string{}
		_ = json.Unmarshal([]byte(inbounds), &record.Inbounds) // tolerate malformed rows
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read deleting records: %w", err)
	}
	return records, nil
}

// rosterRecord reads one roster row by its flag state — the shared shape
// behind the live read (RosterRecord) and the disabled read
// (DisabledRosterRecord). Emails match case-insensitively; a row not
// matching the flag is ErrRosterNotFound.
func (s *Store) rosterRecord(ctx context.Context, email string, disabled bool) (RosterRecord, error) {
	var record RosterRecord
	var inbounds string
	err := s.db.QueryRowContext(ctx,
		`SELECT email, client_id, inbounds, created_at, updated_at
		 FROM roster WHERE lower(email) = lower(?) AND disabled = ? AND deleting = 0`, email, disabled,
	).Scan(&record.Email, &record.ClientID, &inbounds, &record.CreatedAt, &record.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RosterRecord{}, ErrRosterNotFound
	}
	if err != nil {
		return RosterRecord{}, fmt.Errorf("query roster record: %w", err)
	}
	record.Inbounds = []string{}
	_ = json.Unmarshal([]byte(inbounds), &record.Inbounds) // tolerate malformed rows
	return record, nil
}

// DisabledRosterRecord returns the stored roster record for a disabled
// email — the before-state the enable mutation re-applies. The credential
// and attachments survive the disable untouched. Emails match
// case-insensitively; a live or unknown email is ErrRosterNotFound.
func (s *Store) DisabledRosterRecord(ctx context.Context, email string) (RosterRecord, error) {
	return s.rosterRecord(ctx, email, true)
}

// EnableRosterUser revives one disabled user in place (ADR-0007): the
// roster and dashboard rows shed the disabled flag — credential,
// attachments, created_at, and history kept — and the record returns.
// revived reports whether the flag actually flipped: an already-live email
// is idempotent (its record returns, revived false). An unknown email is
// ErrRosterNotFound.
func (s *Store) EnableRosterUser(ctx context.Context, email string, now time.Time) (record RosterRecord, revived bool, err error) {
	if record, err = s.RosterRecord(ctx, email); err == nil {
		return record, false, nil // already live — idempotent
	} else if !errors.Is(err, ErrRosterNotFound) {
		return RosterRecord{}, false, err
	}
	if record, err = s.rosterRecord(ctx, email, true); err != nil {
		return RosterRecord{}, false, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RosterRecord{}, false, fmt.Errorf("begin enable transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stamp := now.Unix()
	if _, err := tx.ExecContext(ctx, `
		UPDATE roster SET disabled = 0, updated_at = ? WHERE lower(email) = lower(?)`,
		stamp, record.Email); err != nil {
		return RosterRecord{}, false, fmt.Errorf("revive roster row: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE users SET disabled = 0 WHERE lower(email) = lower(?)`, record.Email); err != nil {
		return RosterRecord{}, false, fmt.Errorf("revive user row: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RosterRecord{}, false, fmt.Errorf("commit enable transaction: %w", err)
	}
	record.UpdatedAt = stamp
	return record, true, nil
}

// RosterEdit is one edit mutation's stored fields (user-management spec
// §5): a nil ClientID keeps the stored credential; nil Inbounds keeps the
// stored attachment set while an empty (non-nil) set detaches every
// inbound. Protocol and Security relabel the dashboard row for the new
// attachment set.
type RosterEdit struct {
	ClientID *string
	Inbounds []string
	Protocol string
	Security string
}

// RosterRecord returns the stored roster record for email — the before
// state the edit path diffs against. A disabled user's row reads as
// absent: disabled users are history, not roster members. Emails match
// case-insensitively.
func (s *Store) RosterRecord(ctx context.Context, email string) (RosterRecord, error) {
	return s.rosterRecord(ctx, email, false)
}

// RosterRecords returns every live roster record, disabled rows excluded — the
// convergence pass re-applies exactly these against the config parse
// (user-management spec §4). Ordered by email for deterministic writes.
func (s *Store) RosterRecords(ctx context.Context) ([]RosterRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT email, client_id, inbounds FROM roster WHERE disabled = 0 AND deleting = 0 ORDER BY email`)
	if err != nil {
		return nil, fmt.Errorf("query roster records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var records []RosterRecord
	for rows.Next() {
		var record RosterRecord
		var inbounds string
		if err := rows.Scan(&record.Email, &record.ClientID, &inbounds); err != nil {
			return nil, fmt.Errorf("scan roster record: %w", err)
		}
		record.Inbounds = []string{}
		_ = json.Unmarshal([]byte(inbounds), &record.Inbounds) // tolerate malformed rows
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read roster records: %w", err)
	}
	return records, nil
}

// EditRosterUser stores one edit: the attachment set and — optionally — the
// Client ID change, with the row relabelled and kept live (a roster
// member edited to zero inbounds is profile-less, not disabled). Idempotent: an
// edit carrying the stored state — including the labels — writes nothing.
// Conflicts write nothing.
func (s *Store) EditRosterUser(ctx context.Context, email string, edit RosterEdit, now time.Time) (RosterRecord, error) {
	before, err := s.RosterRecord(ctx, email)
	if err != nil {
		return RosterRecord{}, err
	}

	clientID := before.ClientID
	if edit.ClientID != nil {
		var taken string
		switch err := s.db.QueryRowContext(ctx,
			`SELECT email FROM roster WHERE lower(client_id) = lower(?) AND lower(email) <> lower(?)`,
			*edit.ClientID, before.Email).Scan(&taken); {
		case err == nil:
			return RosterRecord{}, fmt.Errorf("%w: %s", ErrClientIDTaken, taken)
		case !errors.Is(err, sql.ErrNoRows):
			return RosterRecord{}, fmt.Errorf("check client ID uniqueness: %w", err)
		}
		// The stored spelling wins for the user's own credential — an
		// equivalent case-variant is the same ID, not a rotation.
		if !strings.EqualFold(clientID, *edit.ClientID) {
			clientID = *edit.ClientID
		}
	}
	inbounds := before.Inbounds
	if edit.Inbounds != nil {
		inbounds = edit.Inbounds
	}

	// The no-op probe: same credential, same attachment set, same labels.
	var protocol, security sql.NullString
	switch err := s.db.QueryRowContext(ctx,
		`SELECT protocol, security FROM users WHERE lower(email) = lower(?)`, before.Email,
	).Scan(&protocol, &security); {
	case errors.Is(err, sql.ErrNoRows): // no dashboard row yet — a label write is due
	case err != nil:
		return RosterRecord{}, fmt.Errorf("read stored labels: %w", err)
	default:
		if clientID == before.ClientID && slices.Equal(inbounds, before.Inbounds) &&
			edit.Protocol == protocol.String && edit.Security == security.String {
			return before, nil
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RosterRecord{}, fmt.Errorf("begin edit transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	encoded, err := json.Marshal(inbounds)
	if err != nil {
		return RosterRecord{}, fmt.Errorf("encode attachments: %w", err)
	}
	stamp := now.Unix()
	if _, err := tx.ExecContext(ctx, `
		UPDATE roster SET client_id = ?, inbounds = ?, updated_at = ? WHERE email = ?`,
		clientID, string(encoded), stamp, before.Email); err != nil {
		return RosterRecord{}, fmt.Errorf("update roster row: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO users (email, protocol, security, disabled, first_seen)
		VALUES (?, ?, ?, 0, ?)
		ON CONFLICT(email) DO UPDATE SET
			protocol = excluded.protocol,
			security = excluded.security,
			disabled = 0`,
		before.Email, edit.Protocol, edit.Security, stamp); err != nil {
		return RosterRecord{}, fmt.Errorf("relabel user row: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RosterRecord{}, fmt.Errorf("commit edit transaction: %w", err)
	}
	return RosterRecord{
		Email: before.Email, ClientID: clientID, Inbounds: inbounds,
		CreatedAt: before.CreatedAt, UpdatedAt: stamp,
	}, nil
}

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
// transaction: everyone not in the config becomes disabled (a no-op for
// rows already disabled), then roster emails are upserted — new users
// appear with zero totals, returning users lose the disabled flag, and
// labels follow the config.
func syncLabels(ctx context.Context, tx *sql.Tx, roster map[string]RosterUser, now time.Time) error {
	// Disabled-ness is the Roster's call, not the config's (CONTEXT.md): a
	// parse missing a roster member leaves them alone — convergence
	// re-applies — so only users with no live roster row can go disabled
	// here. And a parse may not revive a user the panel disabled: the
	// roster upsert keeps the disabled flag for rows flagged disabled in
	// the roster.
	if _, err := tx.ExecContext(ctx, `
		UPDATE users SET disabled = 1 WHERE disabled = 0
		AND NOT EXISTS (SELECT 1 FROM roster r WHERE lower(r.email) = lower(users.email) AND r.disabled = 0)`); err != nil {
		return fmt.Errorf("mark disabled users: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO users (email, protocol, security, disabled, first_seen)
		VALUES (?, ?, ?, 0, ?)
		ON CONFLICT(email) DO UPDATE SET
			protocol = excluded.protocol,
			security = excluded.security,
			disabled = CASE WHEN EXISTS (
				SELECT 1 FROM roster r WHERE lower(r.email) = lower(users.email) AND r.disabled = 1
			) THEN users.disabled ELSE 0 END`)
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
// re-applying them is convergence's job, not adoption's. A disabled user's
// flagged row is untouched — the panel decided they are off the Roster, and
// a stale config carrying them (the render has not landed yet) must not
// revive them.
//
// The schema deliberately carries no uniqueness constraint on client_id and
// no case folding on email: a misconfigured config (duplicate Client IDs,
// case-variant emails) must never fail the poll transaction that carries
// the traffic totals. Uniqueness is enforced where mutations enter — the
// mutation API (user-management spec §5), not the observer.
func adoptClients(ctx context.Context, tx *sql.Tx, clients map[string]RosterClient, now time.Time) error {
	type storedRow struct {
		tags     []string
		disabled bool
	}
	stored := map[string]storedRow{}
	rows, err := tx.QueryContext(ctx, `SELECT email, inbounds, disabled FROM roster`)
	if err != nil {
		return fmt.Errorf("read roster for adoption: %w", err)
	}
	for rows.Next() {
		var email, inbounds string
		var disabled bool
		if err := rows.Scan(&email, &inbounds, &disabled); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan roster row: %w", err)
		}
		// inbounds is a JSON array; tolerate malformed rows.
		var tags []string
		_ = json.Unmarshal([]byte(inbounds), &tags)
		stored[email] = storedRow{tags: tags, disabled: disabled}
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
		if row, known := stored[email]; known && row.disabled {
			continue // a disabled user stays disabled — the panel decided
		}
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
		merged := slices.Clone(tags.tags)
		for _, tag := range client.Inbounds {
			if !slices.Contains(merged, tag) {
				merged = append(merged, tag)
			}
		}
		if len(merged) == len(tags.tags) {
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
// (Client ID, inbounds) join from the roster store — null for users the
// config never adopted and for disabled users, whose flagged rows are
// history.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT u.email, u.protocol, u.security, u.up_bytes_total, u.down_bytes_total,
		       u.last_seen, u.last_ips, u.disabled, u.first_seen,
		       r.client_id, r.inbounds
		FROM users u
		LEFT JOIN roster r ON r.email = u.email AND r.disabled = 0
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
		var disabled bool
		var clientID sql.NullString
		var inbounds sql.NullString
		if err := rows.Scan(
			&user.Email, &user.Protocol, &user.Security,
			&user.UpBytesTotal, &user.DownBytesTotal,
			&lastSeen, &lastIPs, &disabled, &user.FirstSeen,
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
		user.Disabled = disabled
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
