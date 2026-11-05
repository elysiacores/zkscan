package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"zkscan/internal/core"
)

// SaveSnapshot stores one pull in a single transaction:
//   - replaces the device's users with snap.Users;
//   - inserts records not yet present (device_id, user_id, timestamp), ignoring duplicates;
//   - fills an empty Name from the users list (also backfilling older rows of
//     this device whose name is still empty);
//   - sets device_status to OK with the info, the time and the new-record count.
//
// It returns the number of new records. core.ErrNotFound if the device is gone.
func (s *Store) SaveSnapshot(ctx context.Context, deviceID int64, snap core.Snapshot, pulledAt time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var devName string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM devices WHERE id = ?`, deviceID).Scan(&devName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, core.ErrNotFound
		}
		return 0, err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM device_users WHERE device_id = ?`, deviceID); err != nil {
		return 0, err
	}
	names := make(map[string]string, len(snap.Users))
	ustmt, err := tx.PrepareContext(ctx,
		`INSERT INTO device_users (device_id, device_name, uid, user_id, name, privilege, card) VALUES (?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer ustmt.Close()
	for _, u := range snap.Users {
		if _, err := ustmt.ExecContext(ctx, deviceID, devName, u.UID, u.UserID, u.Name, u.Privilege, int64(u.Card)); err != nil {
			return 0, err
		}
		if u.Name != "" {
			names[u.UserID] = u.Name
		}
	}

	pulled := fmtLocal(pulledAt)
	rstmt, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO attendance (device_id, device_name, user_id, name, ts, verify_mode, punch_state, pulled_at)
		 VALUES (?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer rstmt.Close()
	added := 0
	for _, r := range snap.Records {
		name := r.Name
		if name == "" {
			name = names[r.UserID]
		}
		res, err := rstmt.ExecContext(ctx, deviceID, devName, r.UserID, name, fmtWall(r.Timestamp), r.VerifyMode, r.PunchState, pulled)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		added += int(n)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE attendance SET name = (SELECT u.name FROM device_users u WHERE u.device_id = attendance.device_id
		    AND u.user_id = attendance.user_id AND u.name <> '' LIMIT 1)
		 WHERE device_id = ? AND name = '' AND EXISTS (SELECT 1 FROM device_users u WHERE u.device_id = attendance.device_id
		    AND u.user_id = attendance.user_id AND u.name <> '')`, deviceID); err != nil {
		return 0, err
	}

	var devTime any
	if snap.Info.DeviceTime != nil {
		devTime = fmtLocal(*snap.Info.DeviceTime)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO device_status (device_id, last_pull_at, last_ok_at, last_ok, last_error, last_hint, last_new,
		    info_serial, info_firmware, info_platform, info_device_name, info_user_count, info_record_count, info_device_time)
		 VALUES (?,?,?,1,'','',?,?,?,?,?,?,?,?)
		 ON CONFLICT(device_id) DO UPDATE SET
		    last_pull_at=excluded.last_pull_at, last_ok_at=excluded.last_ok_at, last_ok=1, last_error='', last_hint='',
		    last_new=excluded.last_new, info_serial=excluded.info_serial, info_firmware=excluded.info_firmware,
		    info_platform=excluded.info_platform, info_device_name=excluded.info_device_name,
		    info_user_count=excluded.info_user_count, info_record_count=excluded.info_record_count,
		    info_device_time=excluded.info_device_time`,
		deviceID, pulled, pulled, added,
		snap.Info.Serial, snap.Info.Firmware, snap.Info.Platform, snap.Info.DeviceName,
		nullInt(snap.Info.UserCount), nullInt(snap.Info.RecordCount), devTime); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return added, nil
}

// SavePullFailure records a failed pull. LastPullAt becomes the attempt time;
// users, records, info and LastNew are untouched. LastPullAt() (the store-wide
// "last successful pull") is not affected.
func (s *Store) SavePullFailure(ctx context.Context, deviceID int64, errMsg, hint string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO device_status (device_id, last_pull_at, last_ok, last_error, last_hint)
		 SELECT id, ?, 0, ?, ? FROM devices WHERE id = ?
		 ON CONFLICT(device_id) DO UPDATE SET last_pull_at=excluded.last_pull_at, last_ok=0,
		    last_error=excluded.last_error, last_hint=excluded.last_hint`,
		fmtLocal(at), errMsg, hint, deviceID)
	return err
}

// ListAttendance filters (all optional) and returns newest first plus the total
// number of matching rows ignoring Limit/Offset.
func (s *Store) ListAttendance(ctx context.Context, f core.AttendanceFilter) ([]core.Attendance, int, error) {
	var where []string
	var args []any
	if f.From != nil {
		where = append(where, "ts >= ?")
		args = append(args, f.From.Format(core.DateLayout)+" 00:00:00")
	}
	if f.To != nil {
		where = append(where, "ts <= ?")
		args = append(args, f.To.Format(core.DateLayout)+" 23:59:59")
	}
	if f.UserID != "" {
		where = append(where, "user_id = ?")
		args = append(args, f.UserID)
	}
	if f.DeviceID != 0 {
		where = append(where, "device_id = ?")
		args = append(args, f.DeviceID)
	}
	if f.DeviceName != "" {
		where = append(where, "device_name = ?")
		args = append(args, f.DeviceName)
	}
	if f.Search != "" {
		p := likePattern(strings.ToLower(f.Search))
		where = append(where, `(lower(user_id) LIKE ? ESCAPE '\' OR lower(name) LIKE ? ESCAPE '\')`)
		args = append(args, p, p)
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()

	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attendance`+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT id, device_id, device_name, user_id, name, ts, verify_mode, punch_state, pulled_at FROM attendance` +
		cond + ` ORDER BY ts DESC, id DESC`
	qargs := append([]any{}, args...)
	if f.Limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		qargs = append(qargs, f.Limit, max(f.Offset, 0))
	} else if f.Offset > 0 {
		q += ` LIMIT -1 OFFSET ?`
		qargs = append(qargs, f.Offset)
	}
	rows, err := tx.QueryContext(ctx, q, qargs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []core.Attendance
	for rows.Next() {
		var a core.Attendance
		var ts, pulled string
		if err := rows.Scan(&a.ID, &a.DeviceID, &a.DeviceName, &a.UserID, &a.Name, &ts, &a.VerifyMode, &a.PunchState, &pulled); err != nil {
			return nil, 0, err
		}
		if a.Timestamp, err = parseT(ts); err != nil {
			return nil, 0, err
		}
		if a.PulledAt, err = parseT(pulled); err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// ListDeviceUsers lists users of a device (deviceID 0 = all devices), ordered by
// device name then uid; search matches user_id or name (contains, case-insensitive).
func (s *Store) ListDeviceUsers(ctx context.Context, deviceID int64, search string) ([]core.DeviceUser, error) {
	q := `SELECT device_id, device_name, uid, user_id, name, privilege, card FROM device_users`
	var where []string
	var args []any
	if deviceID != 0 {
		where = append(where, "device_id = ?")
		args = append(args, deviceID)
	}
	if search != "" {
		p := likePattern(strings.ToLower(search))
		where = append(where, `(lower(user_id) LIKE ? ESCAPE '\' OR lower(name) LIKE ? ESCAPE '\')`)
		args = append(args, p, p)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += ` ORDER BY device_name, uid, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.DeviceUser
	for rows.Next() {
		var u core.DeviceUser
		var card int64
		if err := rows.Scan(&u.DeviceID, &u.DeviceName, &u.UID, &u.UserID, &u.Name, &u.Privilege, &card); err != nil {
			return nil, err
		}
		u.Card = uint32(card)
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) count(ctx context.Context, table string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n) // table is a package constant
	return n, err
}

func (s *Store) CountAttendance(ctx context.Context) (int, error) { return s.count(ctx, "attendance") }
func (s *Store) CountDeviceUsers(ctx context.Context) (int, error) {
	return s.count(ctx, "device_users")
}

// LastPullAt is the latest successful pull time over all devices (nil if none).
func (s *Store) LastPullAt(ctx context.Context) (*time.Time, error) {
	var ns sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(last_ok_at) FROM device_status`).Scan(&ns); err != nil {
		return nil, err
	}
	return parsePtr(ns)
}
