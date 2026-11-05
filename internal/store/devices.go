package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"zkscan/internal/core"
)

const deviceCols = `id, name, ip, port, comm_key, transport, timeout_sec, demo, enabled, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanDevice(r scanner) (core.Device, error) {
	var d core.Device
	var tr, created, updated string
	var demo, enabled int
	if err := r.Scan(&d.ID, &d.Name, &d.IP, &d.Port, &d.CommKey, &tr, &d.TimeoutSec, &demo, &enabled, &created, &updated); err != nil {
		return core.Device{}, err
	}
	d.Transport = core.Transport(tr)
	d.Demo = demo != 0
	d.Enabled = enabled != 0
	var err error
	if d.CreatedAt, err = parseT(created); err != nil {
		return core.Device{}, err
	}
	if d.UpdatedAt, err = parseT(updated); err != nil {
		return core.Device{}, err
	}
	return d, nil
}

func (s *Store) ListDevices(ctx context.Context) ([]core.Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deviceCols+` FROM devices ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDevice(ctx context.Context, id int64) (core.Device, error) {
	d, err := scanDevice(s.db.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM devices WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return core.Device{}, core.ErrNotFound
	}
	return d, err
}

// CreateDevice sets CreatedAt/UpdatedAt itself (CreatedAt kept if already set).
func (s *Store) CreateDevice(ctx context.Context, d core.Device) (core.Device, error) {
	now := time.Now()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	d.UpdatedAt = now
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO devices (name, ip, port, comm_key, transport, timeout_sec, demo, enabled, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		d.Name, d.IP, d.Port, d.CommKey, string(d.Transport), d.TimeoutSec, b2i(d.Demo), b2i(d.Enabled),
		fmtLocal(d.CreatedAt), fmtLocal(d.UpdatedAt))
	if err != nil {
		if isUnique(err) {
			return core.Device{}, core.ErrConflict
		}
		return core.Device{}, err
	}
	d.ID, err = res.LastInsertId()
	return d, err
}

// UpdateDevice updates every field except CreatedAt and sets UpdatedAt.
// If the name changes, device_name on that device's attendance and users is
// renamed too so listings stay consistent.
func (s *Store) UpdateDevice(ctx context.Context, d core.Device) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE devices SET name=?, ip=?, port=?, comm_key=?, transport=?, timeout_sec=?, demo=?, enabled=?, updated_at=? WHERE id=?`,
		d.Name, d.IP, d.Port, d.CommKey, string(d.Transport), d.TimeoutSec, b2i(d.Demo), b2i(d.Enabled), fmtLocal(time.Now()), d.ID)
	if err != nil {
		if isUnique(err) {
			return core.ErrConflict
		}
		return err
	}
	if err := rowsAffected(res, nil); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE attendance SET device_name=? WHERE device_id=? AND device_name<>?`, d.Name, d.ID, d.Name); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE device_users SET device_name=? WHERE device_id=?`, d.Name, d.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteDevice removes the device, its status and its users; attendance stays.
func (s *Store) DeleteDevice(ctx context.Context, id int64) error {
	return rowsAffected(s.db.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id))
}

const statusCols = `device_id, last_pull_at, last_ok, last_error, last_hint, last_new,
	info_serial, info_firmware, info_platform, info_device_name, info_user_count, info_record_count, info_device_time`

func scanStatus(r scanner) (core.DeviceStatus, error) {
	var st core.DeviceStatus
	var pull, devTime sql.NullString
	var ok int
	var uc, rc sql.NullInt64
	if err := r.Scan(&st.DeviceID, &pull, &ok, &st.LastError, &st.LastHint, &st.LastNew,
		&st.Info.Serial, &st.Info.Firmware, &st.Info.Platform, &st.Info.DeviceName, &uc, &rc, &devTime); err != nil {
		return core.DeviceStatus{}, err
	}
	st.LastOK = ok != 0
	st.Info.UserCount = intPtr(uc)
	st.Info.RecordCount = intPtr(rc)
	var err error
	if st.LastPullAt, err = parsePtr(pull); err != nil {
		return core.DeviceStatus{}, err
	}
	if st.Info.DeviceTime, err = parsePtr(devTime); err != nil {
		return core.DeviceStatus{}, err
	}
	return st, nil
}

// GetDeviceStatus returns DeviceStatus{DeviceID:id} when never pulled.
func (s *Store) GetDeviceStatus(ctx context.Context, id int64) (core.DeviceStatus, error) {
	st, err := scanStatus(s.db.QueryRowContext(ctx, `SELECT `+statusCols+` FROM device_status WHERE device_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return core.DeviceStatus{DeviceID: id}, nil
	}
	return st, err
}

func (s *Store) ListDeviceStatus(ctx context.Context) (map[int64]core.DeviceStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+statusCols+` FROM device_status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]core.DeviceStatus{}
	for rows.Next() {
		st, err := scanStatus(rows)
		if err != nil {
			return nil, err
		}
		out[st.DeviceID] = st
	}
	return out, rows.Err()
}
