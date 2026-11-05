package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"zkscan/internal/core"
)

// ---------------- admin accounts ----------------

const userCols = `id, username, password_hash, must_change_password, created_at, last_login_at`

func scanUser(r scanner) (core.User, error) {
	var u core.User
	var must int
	var created string
	var last sql.NullString
	if err := r.Scan(&u.ID, &u.Username, &u.PasswordHash, &must, &created, &last); err != nil {
		return core.User{}, err
	}
	u.MustChangePassword = must != 0
	var err error
	if u.CreatedAt, err = parseT(created); err != nil {
		return core.User{}, err
	}
	if u.LastLoginAt, err = parsePtr(last); err != nil {
		return core.User{}, err
	}
	return u, nil
}

func (s *Store) getUser(ctx context.Context, where string, arg any) (core.User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return core.User{}, core.ErrNotFound
	}
	return u, err
}

func (s *Store) CountUsers(ctx context.Context) (int, error) { return s.count(ctx, "users") }

// CreateUser stores the username lowercased; ErrConflict if taken. CreatedAt is
// set to now when zero.
func (s *Store) CreateUser(ctx context.Context, u core.User) (core.User, error) {
	u.Username = strings.ToLower(u.Username)
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now()
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, must_change_password, created_at, last_login_at) VALUES (?,?,?,?,?)`,
		u.Username, u.PasswordHash, b2i(u.MustChangePassword), fmtLocal(u.CreatedAt), fmtLocalPtr(u.LastLoginAt))
	if err != nil {
		if isUnique(err) {
			return core.User{}, core.ErrConflict
		}
		return core.User{}, err
	}
	u.ID, err = res.LastInsertId()
	return u, err
}

func (s *Store) GetUserByID(ctx context.Context, id int64) (core.User, error) {
	return s.getUser(ctx, "id = ?", id)
}

// GetUserByUsername is case-insensitive (the name is lowercased first).
func (s *Store) GetUserByUsername(ctx context.Context, username string) (core.User, error) {
	return s.getUser(ctx, "username = ?", strings.ToLower(username))
}

func (s *Store) ListUsers(ctx context.Context) ([]core.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) UpdatePassword(ctx context.Context, userID int64, hash string, mustChange bool) error {
	return rowsAffected(s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, must_change_password = ? WHERE id = ?`, hash, b2i(mustChange), userID))
}

func (s *Store) TouchLogin(ctx context.Context, userID int64, at time.Time) error {
	return rowsAffected(s.db.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, fmtLocal(at), userID))
}

// DeleteUser deletes the user and all of that user's sessions; ErrNotFound if missing.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
		return err
	}
	if err := rowsAffected(tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------------- sessions ----------------

func (s *Store) CreateSession(ctx context.Context, se core.Session) error {
	if se.CreatedAt.IsZero() {
		se.CreatedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, ip, created_at, expires_at) VALUES (?,?,?,?,?)`,
		se.TokenHash, se.UserID, se.IP, fmtLocal(se.CreatedAt), fmtLocal(se.ExpiresAt))
	if err != nil && isUnique(err) {
		return core.ErrConflict
	}
	return err
}

// GetSession returns the session whether or not it has expired; callers check ExpiresAt.
func (s *Store) GetSession(ctx context.Context, tokenHash string) (core.Session, error) {
	var se core.Session
	var created, expires string
	err := s.db.QueryRowContext(ctx,
		`SELECT token_hash, user_id, ip, created_at, expires_at FROM sessions WHERE token_hash = ?`, tokenHash).
		Scan(&se.TokenHash, &se.UserID, &se.IP, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Session{}, core.ErrNotFound
	}
	if err != nil {
		return core.Session{}, err
	}
	if se.CreatedAt, err = parseT(created); err != nil {
		return core.Session{}, err
	}
	if se.ExpiresAt, err = parseT(expires); err != nil {
		return core.Session{}, err
	}
	return se, nil
}

// DeleteSession is idempotent.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// DeleteUserSessions removes all of a user's sessions except exceptTokenHash ("" = none kept).
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64, exceptTokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, userID, exceptTokenHash)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, fmtLocal(now))
	return err
}

// ---------------- API keys ----------------

const keyCols = `id, name, prefix, key_hash, allowed_ips, created_at, last_used_at, revoked`

func scanKey(r scanner) (core.APIKey, error) {
	var k core.APIKey
	var ips, created string
	var last sql.NullString
	var revoked int
	if err := r.Scan(&k.ID, &k.Name, &k.Prefix, &k.KeyHash, &ips, &created, &last, &revoked); err != nil {
		return core.APIKey{}, err
	}
	k.Revoked = revoked != 0
	if err := json.Unmarshal([]byte(ips), &k.AllowedIPs); err != nil {
		return core.APIKey{}, err
	}
	if len(k.AllowedIPs) == 0 {
		k.AllowedIPs = nil
	}
	var err error
	if k.CreatedAt, err = parseT(created); err != nil {
		return core.APIKey{}, err
	}
	if k.LastUsedAt, err = parsePtr(last); err != nil {
		return core.APIKey{}, err
	}
	return k, nil
}

// CreateAPIKey sets CreatedAt to now when zero. An empty AllowedIPs reads back as nil.
func (s *Store) CreateAPIKey(ctx context.Context, k core.APIKey) (core.APIKey, error) {
	if k.CreatedAt.IsZero() {
		k.CreatedAt = time.Now()
	}
	ips := k.AllowedIPs
	if ips == nil {
		ips = []string{}
	}
	b, err := json.Marshal(ips)
	if err != nil {
		return core.APIKey{}, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO api_keys (name, prefix, key_hash, allowed_ips, created_at, last_used_at, revoked) VALUES (?,?,?,?,?,?,?)`,
		k.Name, k.Prefix, k.KeyHash, string(b), fmtLocal(k.CreatedAt), fmtLocalPtr(k.LastUsedAt), b2i(k.Revoked))
	if err != nil {
		if isUnique(err) {
			return core.APIKey{}, core.ErrConflict
		}
		return core.APIKey{}, err
	}
	k.ID, err = res.LastInsertId()
	return k, err
}

func (s *Store) ListAPIKeys(ctx context.Context) ([]core.APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+keyCols+` FROM api_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.APIKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// GetAPIKeyByHash also returns revoked keys; callers decide.
func (s *Store) GetAPIKeyByHash(ctx context.Context, keyHash string) (core.APIKey, error) {
	k, err := scanKey(s.db.QueryRowContext(ctx, `SELECT `+keyCols+` FROM api_keys WHERE key_hash = ?`, keyHash))
	if errors.Is(err, sql.ErrNoRows) {
		return core.APIKey{}, core.ErrNotFound
	}
	return k, err
}

func (s *Store) RevokeAPIKey(ctx context.Context, id int64) error {
	return rowsAffected(s.db.ExecContext(ctx, `UPDATE api_keys SET revoked = 1 WHERE id = ?`, id))
}

func (s *Store) TouchAPIKey(ctx context.Context, id int64, at time.Time) error {
	return rowsAffected(s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = ? WHERE id = ?`, fmtLocal(at), id))
}

// ---------------- logs ----------------

const maxLogs = 5000

// AddLog inserts the entry (At defaults to now) and keeps only the newest 5000.
func (s *Store) AddLog(ctx context.Context, e core.LogEntry) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var dev any
	if e.DeviceID != nil {
		dev = *e.DeviceID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO logs (at, level, source, device_id, message) VALUES (?,?,?,?,?)`,
		fmtLocal(e.At), e.Level, e.Source, dev, e.Message); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM logs WHERE id <= (SELECT MAX(id) FROM logs) - ?`, maxLogs); err != nil {
		return err
	}
	return tx.Commit()
}

// ListLogs returns newest first; limit <= 0 means no limit; source "" = all.
func (s *Store) ListLogs(ctx context.Context, limit int, source string) ([]core.LogEntry, error) {
	q := `SELECT id, at, level, source, device_id, message FROM logs`
	var args []any
	if source != "" {
		q += ` WHERE source = ?`
		args = append(args, source)
	}
	q += ` ORDER BY id DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.LogEntry
	for rows.Next() {
		var e core.LogEntry
		var at string
		var dev sql.NullInt64
		if err := rows.Scan(&e.ID, &at, &e.Level, &e.Source, &dev, &e.Message); err != nil {
			return nil, err
		}
		if e.At, err = parseT(at); err != nil {
			return nil, err
		}
		if dev.Valid {
			v := dev.Int64
			e.DeviceID = &v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------------- settings ----------------

const keyPullInterval = "pull_interval_min"

func (s *Store) GetSettings(ctx context.Context) (core.Settings, error) {
	st := core.DefaultSettings()
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, keyPullInterval).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return core.Settings{}, err
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return core.Settings{}, err
	}
	st.PullIntervalMin = n
	return st, nil
}

func (s *Store) SaveSettings(ctx context.Context, st core.Settings) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		keyPullInterval, strconv.Itoa(st.PullIntervalMin))
	return err
}
