// Package store is the SQLite implementation of core.Store.
//
// Concurrency: the pool is limited to one connection, so every statement and
// transaction is serialised inside the process. With WAL and busy_timeout this
// removes "database is locked" errors for any number of goroutines.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"zkscan/internal/core"

	"modernc.org/sqlite"
)

var _ core.Store = (*Store)(nil)

// Store is a SQLite-backed core.Store.
type Store struct {
	db *sql.DB
}

// migrations are applied in order; index+1 is the schema version.
// Add new migrations at the end only; never edit an existing one.
var migrations = []string{
	`
CREATE TABLE devices (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL UNIQUE,
	ip          TEXT NOT NULL,
	port        INTEGER NOT NULL,
	comm_key    INTEGER NOT NULL,
	transport   TEXT NOT NULL,
	timeout_sec INTEGER NOT NULL,
	demo        INTEGER NOT NULL,
	enabled     INTEGER NOT NULL,
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
);

CREATE TABLE device_status (
	device_id    INTEGER PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
	last_pull_at TEXT,
	last_ok_at   TEXT,
	last_ok      INTEGER NOT NULL DEFAULT 0,
	last_error   TEXT NOT NULL DEFAULT '',
	last_hint    TEXT NOT NULL DEFAULT '',
	last_new     INTEGER NOT NULL DEFAULT 0,
	info_serial       TEXT NOT NULL DEFAULT '',
	info_firmware     TEXT NOT NULL DEFAULT '',
	info_platform     TEXT NOT NULL DEFAULT '',
	info_device_name  TEXT NOT NULL DEFAULT '',
	info_user_count   INTEGER,
	info_record_count INTEGER,
	info_device_time  TEXT
);

CREATE TABLE device_users (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	device_id   INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	device_name TEXT NOT NULL,
	uid         INTEGER NOT NULL,
	user_id     TEXT NOT NULL,
	name        TEXT NOT NULL,
	privilege   INTEGER NOT NULL,
	card        INTEGER NOT NULL
);
CREATE INDEX idx_device_users_device ON device_users(device_id);
CREATE INDEX idx_device_users_user_id ON device_users(user_id);

-- device_id is deliberately NOT a foreign key: rows outlive the device.
CREATE TABLE attendance (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	device_id   INTEGER NOT NULL,
	device_name TEXT NOT NULL,
	user_id     TEXT NOT NULL,
	name        TEXT NOT NULL,
	ts          TEXT NOT NULL,
	verify_mode INTEGER NOT NULL,
	punch_state INTEGER NOT NULL,
	pulled_at   TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_attendance_unique ON attendance(device_id, user_id, ts);
CREATE INDEX idx_attendance_ts ON attendance(ts);
CREATE INDEX idx_attendance_user ON attendance(user_id);

CREATE TABLE users (
	id                   INTEGER PRIMARY KEY AUTOINCREMENT,
	username             TEXT NOT NULL UNIQUE,
	password_hash        TEXT NOT NULL,
	must_change_password INTEGER NOT NULL,
	created_at           TEXT NOT NULL,
	last_login_at        TEXT
);

CREATE TABLE sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	ip         TEXT NOT NULL,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL
);
CREATE INDEX idx_sessions_user ON sessions(user_id);

CREATE TABLE api_keys (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	name         TEXT NOT NULL,
	prefix       TEXT NOT NULL,
	key_hash     TEXT NOT NULL UNIQUE,
	allowed_ips  TEXT NOT NULL DEFAULT '[]',
	created_at   TEXT NOT NULL,
	last_used_at TEXT,
	revoked      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE logs (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	at        TEXT NOT NULL,
	level     TEXT NOT NULL,
	source    TEXT NOT NULL,
	device_id INTEGER,
	message   TEXT NOT NULL
);
CREATE INDEX idx_logs_source ON logs(source);

CREATE TABLE settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`,
}

// Open opens (creating if needed) the database at path and migrates it.
func Open(path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: q.Encode()}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var cur int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM schema_version LIMIT 1`).Scan(&cur)
	hasRow := true
	if errors.Is(err, sql.ErrNoRows) {
		hasRow = false
	} else if err != nil {
		return err
	}
	if cur > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this program (%d)", cur, len(migrations))
	}
	for v := cur; v < len(migrations); v++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if hasRow {
			_, err = tx.ExecContext(ctx, `UPDATE schema_version SET version = ?`, v+1)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO schema_version(version) VALUES (?)`, v+1)
			hasRow = true
		}
		if err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// ---------------- helpers ----------------

// isUnique reports whether err is a UNIQUE / PRIMARY KEY constraint violation.
func isUnique(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		c := se.Code()
		return c == 2067 || c == 1555 // SQLITE_CONSTRAINT_UNIQUE, _PRIMARYKEY
	}
	return false
}

// fmtWall formats t as-is (device wall-clock, no zone conversion).
func fmtWall(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(core.TimeLayout)
}

// fmtLocal formats t in the server's local time.
func fmtLocal(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(time.Local).Format(core.TimeLayout)
}

func fmtLocalPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtLocal(*t)
}

func parseT(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.ParseInLocation(core.TimeLayout, s, time.Local)
}

func parsePtr(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := parseT(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func intPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// likePattern builds a contains pattern with LIKE wildcards escaped (ESCAPE '\').
func likePattern(s string) string {
	r := make([]rune, 0, len(s)+2)
	r = append(r, '%')
	for _, c := range s {
		if c == '\\' || c == '%' || c == '_' {
			r = append(r, '\\')
		}
		r = append(r, c)
	}
	r = append(r, '%')
	return string(r)
}

// rowsAffected maps "0 rows" to core.ErrNotFound.
func rowsAffected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return core.ErrNotFound
	}
	return nil
}
