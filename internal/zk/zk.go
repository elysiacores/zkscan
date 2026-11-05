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
		_ = s.enableDevice()
		_ = s.exit()
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
