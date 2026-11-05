package zk

import (
	"context"
	"slices"
	"strings"
	"time"

	"zkscan/internal/core"
)

type client struct{}

// New returns the ZKTeco device client. Devices with Demo=true use the deterministic demo data
// and never touch the network.
func New() core.DeviceClient { return client{} }

// openAndConnect opens the socket and does the CONNECT / AUTH handshake.
func openAndConnect(ctx context.Context, d core.Device) (*session, error) {
	s, err := openSession(ctx, d)
	if err != nil {
		return nil, err
	}
	if err := s.connect(uint32(d.CommKey)); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

// pullSession reads info, users and attendance (the device is already disabled by the caller's flow).
func (s *session) pullSession() (core.Snapshot, error) {
	info := s.readInfo()
	if err := s.ctx.Err(); err != nil {
		return core.Snapshot{}, s.fail(err)
	}
	// set before sending: if the reply is lost the device may still have been disabled
	s.disabled = true
	if err := s.disableDevice(); err != nil {
		return core.Snapshot{}, err
	}
	// like pyzk: an empty table is not read at all (some devices answer ACK_ERROR for it)
	users := []core.DeviceUser{}
	if info.UserCount == nil || *info.UserCount != 0 {
		u, err := s.readUsers(info.UserCount)
		if err != nil {
			return core.Snapshot{}, err
		}
		users = u
	}
	records := []core.Attendance{}
	if info.RecordCount == nil || *info.RecordCount != 0 {
		r, err := s.readAttendance(users, info.RecordCount)
		if err != nil {
			return core.Snapshot{}, err
		}
		records = r
	}
	slices.SortStableFunc(records, func(a, b core.Attendance) int {
		if c := a.Timestamp.Compare(b.Timestamp); c != 0 {
			return c
		}
		return strings.Compare(a.UserID, b.UserID)
	})
	return core.Snapshot{Info: info, Users: users, Records: records}, nil
}

// Fetch connects, reads device info, users and attendance, and disconnects.
func (client) Fetch(ctx context.Context, d core.Device) (core.Snapshot, error) {
	if d.Demo {
		return demoSnapshot(d, time.Now()), nil
	}
	s, err := openAndConnect(ctx, d)
	if err != nil {
		return core.Snapshot{}, err
	}
	defer s.close()
	snap, err := s.pullSession()
	// best effort: always give the device back to the user and end the session
	// (skipped when ctx is cancelled: the connection is already closed and the caller wants out)
	if ctx.Err() == nil {
		s.timeout = min(cleanupTimeout, deviceTimeout(d))
		if s.disabled && s.enableDevice() == nil {
			s.disabled = false
		}
		_ = s.exit()
	}
	if s.disabled {
		// ENABLEDEVICE did not get through (ctx cancelled / connection broken): the scanner would stay
		// locked and staff could not clock in, so try once more on a fresh connection.
		s.close()
		recoverEnable(ctx, d)
	}
	if err != nil {
		return core.Snapshot{}, err
	}
	return snap, nil
}

// TestConnection connects and reads only the device info.
func (client) TestConnection(ctx context.Context, d core.Device) (core.DeviceInfo, error) {
	if d.Demo {
		return demoSnapshot(d, time.Now()).Info, nil
	}
	s, err := openAndConnect(ctx, d)
	if err != nil {
		return core.DeviceInfo{}, err
	}
	defer s.close()
	info := s.readInfo()
	if err := ctx.Err(); err != nil {
		return core.DeviceInfo{}, toDeviceError(ctx, err)
	}
	_ = s.exit()
	return info, nil
}

// SetTime sets the device clock (wall-clock fields of t in time.Local). Demo: no-op success.
func (client) SetTime(ctx context.Context, d core.Device, t time.Time) error {
	if d.Demo {
		return nil
	}
	s, err := openAndConnect(ctx, d)
	if err != nil {
		return err
	}
	defer s.close()
	err = s.setTime(t)
	_ = s.exit()
	return err
}

const (
	// recoverTimeout bounds the whole ENABLEDEVICE recovery on a new connection.
	recoverTimeout = 5 * time.Second
	// recoverIOTimeout caps the per-step I/O timeout of the recovery connection.
	recoverIOTimeout = 3 * time.Second
)

// recoverEnable is the single best-effort attempt to ENABLEDEVICE on a new connection after a Fetch
// left the scanner disabled. It is detached from ctx cancellation (ctx is usually already dead) and
// all its errors are ignored: the caller returns the original error.
func recoverEnable(ctx context.Context, d core.Device) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recoverTimeout)
	defer cancel()
	d.TimeoutSec = int(min(deviceTimeout(d), recoverIOTimeout) / time.Second)
	s, err := openAndConnect(rctx, d)
	if err != nil {
		return
	}
	defer s.close()
	_ = s.enableDevice()
	_ = s.exit()
}
