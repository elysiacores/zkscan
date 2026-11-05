package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"zkscan/internal/core"
)

var ctx = context.Background()

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sub", "dir", "z.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mkDevice(t *testing.T, s *Store, name string) core.Device {
	t.Helper()
	d, err := s.CreateDevice(ctx, core.Device{Name: name, IP: "10.0.0.1", Port: 4370, Transport: core.TransportTCP, TimeoutSec: 10, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func wall(y int, m time.Month, d, hh, mm, ss int) time.Time {
	return time.Date(y, m, d, hh, mm, ss, 0, time.Local)
}

func rec(uid string, ts time.Time) core.Attendance {
	return core.Attendance{UserID: uid, Timestamp: ts, VerifyMode: 1, PunchState: 0}
}

func TestDevicesCRUD(t *testing.T) {
	s := openTemp(t)
	if _, err := s.GetDevice(ctx, 99); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("want ErrNotFound got %v", err)
	}
	b := mkDevice(t, s, "B")
	a, err := s.CreateDevice(ctx, core.Device{Name: "A", IP: "1.2.3.4", Port: 4370, CommKey: 123, Transport: core.TransportUDP, TimeoutSec: 5, Demo: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDevice(ctx, core.Device{Name: "A"}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("want ErrConflict got %v", err)
	}
	list, err := s.ListDevices(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "A" || list[1].Name != "B" {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	got, _ := s.GetDevice(ctx, a.ID)
	if got.CommKey != 123 || got.Transport != core.TransportUDP || !got.Demo || got.Enabled || got.CreatedAt.IsZero() {
		t.Errorf("got %+v", got)
	}
	a.Name = "A2"
	a.Enabled = true
	if err := s.UpdateDevice(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetDevice(ctx, a.ID)
	if got.Name != "A2" || !got.Enabled {
		t.Errorf("update not applied: %+v", got)
	}
	a.Name = "B"
	if err := s.UpdateDevice(ctx, a); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("want ErrConflict got %v", err)
	}
	if err := s.UpdateDevice(ctx, core.Device{ID: 999, Name: "zz"}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("want ErrNotFound got %v", err)
	}
	if err := s.DeleteDevice(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDevice(ctx, b.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("want ErrNotFound got %v", err)
	}
}

func TestSnapshotDedupAndNameFill(t *testing.T) {
	s := openTemp(t)
	d := mkDevice(t, s, "Front")
	t1, t2 := wall(2026, 11, 5, 8, 0, 0), wall(2026, 11, 5, 17, 30, 0)
	devTime := wall(2026, 11, 5, 18, 0, 0)
	uc, rc := 2, 2
	snap := core.Snapshot{
		Info:    core.DeviceInfo{Serial: "SN1", Firmware: "fw", Platform: "p", DeviceName: "dn", UserCount: &uc, RecordCount: &rc, DeviceTime: &devTime},
		Users:   []core.DeviceUser{{UID: 1, UserID: "001", Name: "สมชาย", Card: 4000000000}, {UID: 2, UserID: "002", Name: "Jane", Privilege: 14}},
		Records: []core.Attendance{rec("001", t1), rec("002", t2), rec("003", t1), {UserID: "001", Name: "Keep", Timestamp: t2}},
	}
	pulled := wall(2026, 11, 5, 18, 1, 0)
	n, err := s.SaveSnapshot(ctx, d.ID, snap, pulled)
	if err != nil || n != 4 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	// second pull: 2 old records + 1 new; users replaced (002 dropped, 003 added with name)
	snap2 := core.Snapshot{
		Users:   []core.DeviceUser{{UID: 1, UserID: "001", Name: "สมชาย"}, {UID: 3, UserID: "003", Name: "Late Name"}},
		Records: []core.Attendance{rec("001", t1), rec("002", t2), rec("001", wall(2026, 11, 6, 8, 0, 0))},
	}
	n, err = s.SaveSnapshot(ctx, d.ID, snap2, wall(2026, 11, 6, 9, 0, 0))
	if err != nil || n != 1 {
		t.Fatalf("second n=%d err=%v", n, err)
	}
	rows, total, err := s.ListAttendance(ctx, core.AttendanceFilter{})
	if err != nil || total != 5 || len(rows) != 5 {
		t.Fatalf("total=%d rows=%d err=%v", total, len(rows), err)
	}
	byKey := map[string]core.Attendance{}
	for _, r := range rows {
		byKey[r.UserID+"|"+r.Timestamp.Format(core.TimeLayout)] = r
	}
	if r := byKey["001|2026-11-05 08:00:00"]; r.Name != "สมชาย" || r.DeviceName != "Front" || r.DeviceID != d.ID || !r.PulledAt.Equal(pulled) {
		t.Errorf("name fill/device: %+v", r)
	}
	if r := byKey["001|2026-11-05 17:30:00"]; r.Name != "Keep" {
		t.Errorf("existing name overwritten: %+v", r)
	}
	if r := byKey["003|2026-11-05 08:00:00"]; r.Name != "Late Name" { // backfilled by 2nd pull
		t.Errorf("backfill: %+v", r)
	}
	if r := byKey["002|2026-11-05 17:30:00"]; r.Name != "Jane" {
		t.Errorf("name from first pull lost: %+v", r)
	}
	if !byKey["001|2026-11-05 08:00:00"].Timestamp.Equal(t1) {
		t.Error("timestamp not round-tripped in Local")
	}
	us, err := s.ListDeviceUsers(ctx, d.ID, "")
	if err != nil || len(us) != 2 || us[0].UserID != "001" || us[1].UserID != "003" || us[0].DeviceName != "Front" {
		t.Fatalf("users=%+v err=%v", us, err)
	}
	if us[0].Card != 0 {
		t.Errorf("card %d", us[0].Card)
	}
	if n, _ := s.CountDeviceUsers(ctx); n != 2 {
		t.Errorf("CountDeviceUsers=%d", n)
	}
	if n, _ := s.CountAttendance(ctx); n != 5 {
		t.Errorf("CountAttendance=%d", n)
	}
	// status
	st, _ := s.GetDeviceStatus(ctx, d.ID)
	if !st.LastOK || st.LastNew != 1 || st.LastPullAt == nil || !st.LastPullAt.Equal(wall(2026, 11, 6, 9, 0, 0)) {
		t.Errorf("status %+v", st)
	}
	if _, err := s.SaveSnapshot(ctx, 777, snap, pulled); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("want ErrNotFound got %v", err)
	}
}

func TestSnapshotInfoAndCardRoundTrip(t *testing.T) {
	s := openTemp(t)
	d := mkDevice(t, s, "X")
	devTime := wall(2026, 1, 2, 3, 4, 5)
	uc := 7
	_, err := s.SaveSnapshot(ctx, d.ID, core.Snapshot{
		Info:  core.DeviceInfo{Serial: "S", Firmware: "F", Platform: "P", DeviceName: "N", UserCount: &uc, DeviceTime: &devTime},
		Users: []core.DeviceUser{{UID: 1, UserID: "1", Card: 4294967295}},
	}, wall(2026, 1, 2, 3, 5, 0))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := s.GetDeviceStatus(ctx, d.ID)
	if st.Info.Serial != "S" || st.Info.Firmware != "F" || st.Info.Platform != "P" || st.Info.DeviceName != "N" ||
		st.Info.UserCount == nil || *st.Info.UserCount != 7 || st.Info.RecordCount != nil ||
		st.Info.DeviceTime == nil || !st.Info.DeviceTime.Equal(devTime) {
		t.Errorf("info %+v", st.Info)
	}
	us, _ := s.ListDeviceUsers(ctx, 0, "")
	if len(us) != 1 || us[0].Card != 4294967295 {
		t.Errorf("users %+v", us)
	}
}

func TestPullFailureAndStatus(t *testing.T) {
	s := openTemp(t)
	d := mkDevice(t, s, "D")
	if st, err := s.GetDeviceStatus(ctx, d.ID); err != nil || st.DeviceID != d.ID || st.LastPullAt != nil || st.LastOK {
		t.Fatalf("never pulled: %+v %v", st, err)
	}
	if lp, err := s.LastPullAt(ctx); err != nil || lp != nil {
		t.Fatalf("LastPullAt=%v err=%v", lp, err)
	}
	fail := wall(2026, 11, 5, 9, 0, 0)
	if err := s.SavePullFailure(ctx, d.ID, "หมดเวลา", "ตรวจสาย", fail); err != nil {
		t.Fatal(err)
	}
	st, _ := s.GetDeviceStatus(ctx, d.ID)
	if st.LastOK || st.LastError != "หมดเวลา" || st.LastHint != "ตรวจสาย" || st.LastPullAt == nil || !st.LastPullAt.Equal(fail) {
		t.Errorf("status %+v", st)
	}
	if lp, _ := s.LastPullAt(ctx); lp != nil {
		t.Errorf("failure must not count as successful pull: %v", lp)
	}
	ok := wall(2026, 11, 5, 10, 0, 0)
	if _, err := s.SaveSnapshot(ctx, d.ID, core.Snapshot{Users: []core.DeviceUser{{UID: 1, UserID: "1", Name: "A"}}, Records: []core.Attendance{rec("1", ok)}}, ok); err != nil {
		t.Fatal(err)
	}
	st, _ = s.GetDeviceStatus(ctx, d.ID)
	if !st.LastOK || st.LastError != "" || st.LastHint != "" || st.LastNew != 1 {
		t.Errorf("status after success %+v", st)
	}
	// failure keeps users, records, LastNew, and last successful time
	if err := s.SavePullFailure(ctx, d.ID, "x", "y", wall(2026, 11, 5, 11, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountAttendance(ctx); n != 1 {
		t.Errorf("records %d", n)
	}
	if n, _ := s.CountDeviceUsers(ctx); n != 1 {
		t.Errorf("users %d", n)
	}
	st, _ = s.GetDeviceStatus(ctx, d.ID)
	if st.LastOK || st.LastNew != 1 {
		t.Errorf("status %+v", st)
	}
	if lp, _ := s.LastPullAt(ctx); lp == nil || !lp.Equal(ok) {
		t.Errorf("LastPullAt=%v want %v", lp, ok)
	}
	// second device, later success wins
	d2 := mkDevice(t, s, "E")
	later := wall(2026, 11, 7, 1, 0, 0)
	if _, err := s.SaveSnapshot(ctx, d2.ID, core.Snapshot{}, later); err != nil {
		t.Fatal(err)
	}
	if lp, _ := s.LastPullAt(ctx); lp == nil || !lp.Equal(later) {
		t.Errorf("LastPullAt=%v", lp)
	}
	all, err := s.ListDeviceStatus(ctx)
	if err != nil || len(all) != 2 || !all[d2.ID].LastOK {
		t.Errorf("all=%+v err=%v", all, err)
	}
	if err := s.SavePullFailure(ctx, 555, "e", "h", fail); err != nil {
		t.Errorf("failure for unknown device should be a no-op, got %v", err)
	}
}

func TestListAttendanceFilters(t *testing.T) {
	s := openTemp(t)
	d1, d2 := mkDevice(t, s, "Front"), mkDevice(t, s, "Back")
	_, err := s.SaveSnapshot(ctx, d1.ID, core.Snapshot{
		Users: []core.DeviceUser{{UserID: "100", Name: "Alice Smith"}, {UserID: "200", Name: "Bob"}},
		Records: []core.Attendance{
			rec("100", wall(2026, 11, 3, 23, 59, 59)),
			rec("100", wall(2026, 11, 4, 0, 0, 0)),
			rec("200", wall(2026, 11, 4, 12, 0, 0)),
			rec("100", wall(2026, 11, 5, 23, 59, 59)),
			rec("200", wall(2026, 11, 6, 0, 0, 0)),
		}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveSnapshot(ctx, d2.ID, core.Snapshot{
		Users:   []core.DeviceUser{{UserID: "100", Name: "Alice Smith"}, {UserID: "50%_", Name: "Odd"}},
		Records: []core.Attendance{rec("100", wall(2026, 11, 4, 9, 0, 0)), rec("50%_", wall(2026, 11, 4, 10, 0, 0))}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	day := func(d int) *time.Time { x := wall(2026, 11, d, 13, 37, 0); return &x } // time-of-day must be ignored
	check := func(name string, f core.AttendanceFilter, wantTotal, wantRows int) []core.Attendance {
		t.Helper()
		rows, total, err := s.ListAttendance(ctx, f)
		if err != nil || total != wantTotal || len(rows) != wantRows {
			t.Fatalf("%s: total=%d rows=%d err=%v (want %d/%d)", name, total, len(rows), err, wantTotal, wantRows)
		}
		return rows
	}
	check("all", core.AttendanceFilter{}, 7, 7)
	rows := check("day 4..5", core.AttendanceFilter{From: day(4), To: day(5)}, 5, 5)
	for i := 1; i < len(rows); i++ {
		a, b := rows[i-1], rows[i]
		if a.Timestamp.Before(b.Timestamp) {
			t.Errorf("not newest first: %v then %v", a.Timestamp, b.Timestamp)
		}
	}
	if rows[0].Timestamp.Format(core.TimeLayout) != "2026-11-05 23:59:59" || rows[len(rows)-1].Timestamp.Format(core.TimeLayout) != "2026-11-04 00:00:00" {
		t.Errorf("bounds: first %v last %v", rows[0].Timestamp, rows[len(rows)-1].Timestamp)
	}
	check("from only", core.AttendanceFilter{From: day(5)}, 2, 2)
	check("to only", core.AttendanceFilter{To: day(3)}, 1, 1)
	check("one day", core.AttendanceFilter{From: day(4), To: day(4)}, 4, 4)
	check("user", core.AttendanceFilter{UserID: "100"}, 4, 4)
	check("user exact", core.AttendanceFilter{UserID: "10"}, 0, 0)
	check("device id", core.AttendanceFilter{DeviceID: d2.ID}, 2, 2)
	check("device name", core.AttendanceFilter{DeviceName: "Back"}, 2, 2)
	check("search name ci", core.AttendanceFilter{Search: "ALICE"}, 4, 4)
	check("search id", core.AttendanceFilter{Search: "20"}, 2, 2)
	check("search wildcard literal", core.AttendanceFilter{Search: "%"}, 1, 1)
	check("search underscore literal", core.AttendanceFilter{Search: "%_"}, 1, 1)
	check("search none", core.AttendanceFilter{Search: "zzz"}, 0, 0)
	check("combined", core.AttendanceFilter{Search: "alice", DeviceID: d1.ID, From: day(4), To: day(4)}, 1, 1)
	check("sql injection is data", core.AttendanceFilter{Search: "'; DROP TABLE attendance; --"}, 0, 0)

	// paging
	p1 := check("page1", core.AttendanceFilter{Limit: 3}, 7, 3)
	p2 := check("page2", core.AttendanceFilter{Limit: 3, Offset: 3}, 7, 3)
	p3 := check("page3", core.AttendanceFilter{Limit: 3, Offset: 6}, 7, 1)
	check("past end", core.AttendanceFilter{Limit: 3, Offset: 20}, 7, 0)
	check("offset no limit", core.AttendanceFilter{Offset: 5}, 7, 2)
	seen := map[int64]bool{}
	for _, r := range append(append(p1, p2...), p3...) {
		if seen[r.ID] {
			t.Errorf("row %d repeated across pages", r.ID)
		}
		seen[r.ID] = true
	}
}

func TestDeleteDeviceKeepsAttendance(t *testing.T) {
	s := openTemp(t)
	d := mkDevice(t, s, "Gone")
	if _, err := s.SaveSnapshot(ctx, d.ID, core.Snapshot{
		Users:   []core.DeviceUser{{UserID: "1", Name: "A"}},
		Records: []core.Attendance{rec("1", wall(2026, 11, 5, 8, 0, 0))}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDevice(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	rows, total, _ := s.ListAttendance(ctx, core.AttendanceFilter{})
	if total != 1 || rows[0].DeviceName != "Gone" || rows[0].DeviceID != d.ID {
		t.Errorf("rows=%+v", rows)
	}
	if n, _ := s.CountDeviceUsers(ctx); n != 0 {
		t.Errorf("device users should go with device, got %d", n)
	}
	if st, _ := s.GetDeviceStatus(ctx, d.ID); st.LastPullAt != nil {
		t.Errorf("status should be gone: %+v", st)
	}
	// name can be reused, and filter by old name still finds old rows
	d2 := mkDevice(t, s, "Gone")
	if d2.ID == d.ID {
		t.Error("ids must not be reused")
	}
	if _, total, _ := s.ListAttendance(ctx, core.AttendanceFilter{DeviceName: "Gone"}); total != 1 {
		t.Errorf("total %d", total)
	}
}

func TestRenameDeviceRenamesRows(t *testing.T) {
	s := openTemp(t)
	d := mkDevice(t, s, "Old")
	s.SaveSnapshot(ctx, d.ID, core.Snapshot{Users: []core.DeviceUser{{UserID: "1"}}, Records: []core.Attendance{rec("1", wall(2026, 11, 5, 8, 0, 0))}}, time.Now())
	d.Name = "New"
	if err := s.UpdateDevice(ctx, d); err != nil {
		t.Fatal(err)
	}
	rows, _, _ := s.ListAttendance(ctx, core.AttendanceFilter{})
	us, _ := s.ListDeviceUsers(ctx, 0, "")
	if rows[0].DeviceName != "New" || us[0].DeviceName != "New" {
		t.Errorf("rows=%+v users=%+v", rows, us)
	}
}

func TestListDeviceUsersSearch(t *testing.T) {
	s := openTemp(t)
	a, b := mkDevice(t, s, "A"), mkDevice(t, s, "B")
	s.SaveSnapshot(ctx, a.ID, core.Snapshot{Users: []core.DeviceUser{{UID: 2, UserID: "20", Name: "Zed"}, {UID: 1, UserID: "10", Name: "Amy"}}}, time.Now())
	s.SaveSnapshot(ctx, b.ID, core.Snapshot{Users: []core.DeviceUser{{UID: 1, UserID: "10", Name: "amy b"}}}, time.Now())
	all, _ := s.ListDeviceUsers(ctx, 0, "")
	if len(all) != 3 || all[0].DeviceName != "A" || all[0].UID != 1 || all[2].DeviceName != "B" {
		t.Errorf("all=%+v", all)
	}
	if got, _ := s.ListDeviceUsers(ctx, 0, "AMY"); len(got) != 2 {
		t.Errorf("search %+v", got)
	}
	if got, _ := s.ListDeviceUsers(ctx, b.ID, "amy"); len(got) != 1 || got[0].DeviceID != b.ID {
		t.Errorf("device search %+v", got)
	}
	if got, _ := s.ListDeviceUsers(ctx, a.ID, "20"); len(got) != 1 {
		t.Errorf("id search %+v", got)
	}
}

func TestUsersAndSessions(t *testing.T) {
	s := openTemp(t)
	if n, _ := s.CountUsers(ctx); n != 0 {
		t.Fatal(n)
	}
	u, err := s.CreateUser(ctx, core.User{Username: "Admin", PasswordHash: "h1", MustChangePassword: true})
	if err != nil || u.ID == 0 || u.Username != "admin" {
		t.Fatalf("%+v %v", u, err)
	}
	if _, err := s.CreateUser(ctx, core.User{Username: "ADMIN", PasswordHash: "x"}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("want conflict got %v", err)
	}
	u2, _ := s.CreateUser(ctx, core.User{Username: "bob", PasswordHash: "h2"})
	if n, _ := s.CountUsers(ctx); n != 2 {
		t.Error(n)
	}
	g, err := s.GetUserByUsername(ctx, "ADMIN")
	if err != nil || g.ID != u.ID || !g.MustChangePassword || g.LastLoginAt != nil || g.CreatedAt.IsZero() {
		t.Fatalf("%+v %v", g, err)
	}
	if _, err := s.GetUserByUsername(ctx, "nobody"); !errors.Is(err, core.ErrNotFound) {
		t.Error(err)
	}
	if _, err := s.GetUserByID(ctx, 999); !errors.Is(err, core.ErrNotFound) {
		t.Error(err)
	}
	if err := s.UpdatePassword(ctx, u.ID, "new", false); err != nil {
		t.Fatal(err)
	}
	at := wall(2026, 11, 5, 12, 0, 0)
	if err := s.TouchLogin(ctx, u.ID, at); err != nil {
		t.Fatal(err)
	}
	g, _ = s.GetUserByID(ctx, u.ID)
	if g.PasswordHash != "new" || g.MustChangePassword || g.LastLoginAt == nil || !g.LastLoginAt.Equal(at) {
		t.Errorf("%+v", g)
	}
	if err := s.UpdatePassword(ctx, 999, "x", false); !errors.Is(err, core.ErrNotFound) {
		t.Error(err)
	}
	if err := s.TouchLogin(ctx, 999, at); !errors.Is(err, core.ErrNotFound) {
		t.Error(err)
	}
	if l, _ := s.ListUsers(ctx); len(l) != 2 || l[0].Username != "admin" {
		t.Errorf("%+v", l)
	}

	// sessions
	exp := time.Now().Add(time.Hour)
	for _, h := range []string{"t1", "t2", "t3"} {
		if err := s.CreateSession(ctx, core.Session{TokenHash: h, UserID: u.ID, IP: "1.1.1.1", ExpiresAt: exp}); err != nil {
			t.Fatal(err)
		}
	}
	s.CreateSession(ctx, core.Session{TokenHash: "b1", UserID: u2.ID, ExpiresAt: exp})
	s.CreateSession(ctx, core.Session{TokenHash: "old", UserID: u2.ID, ExpiresAt: time.Now().Add(-time.Hour)})
	se, err := s.GetSession(ctx, "t1")
	if err != nil || se.UserID != u.ID || se.IP != "1.1.1.1" || se.CreatedAt.IsZero() || se.ExpiresAt.Sub(exp) > time.Second || exp.Sub(se.ExpiresAt) > time.Second {
		t.Fatalf("%+v %v", se, err)
	}
	if _, err := s.GetSession(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Error(err)
	}
	if err := s.DeleteSession(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, "t1"); !errors.Is(err, core.ErrNotFound) {
		t.Error(err)
	}
	if err := s.DeleteUserSessions(ctx, u.ID, "t2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, "t2"); err != nil {
		t.Errorf("exception kept: %v", err)
	}
	if _, err := s.GetSession(ctx, "t3"); !errors.Is(err, core.ErrNotFound) {
		t.Error("t3 should be gone")
	}
	if _, err := s.GetSession(ctx, "b1"); err != nil {
		t.Errorf("other user's session must stay: %v", err)
	}
	if err := s.DeleteExpiredSessions(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, "old"); !errors.Is(err, core.ErrNotFound) {
		t.Error("expired should be gone")
	}
	if _, err := s.GetSession(ctx, "b1"); err != nil {
		t.Error("unexpired removed")
	}
	// DeleteUserSessions with "" removes all
	if err := s.DeleteUserSessions(ctx, u.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, "t2"); !errors.Is(err, core.ErrNotFound) {
		t.Error("t2 should be gone")
	}
	// DeleteUser removes sessions
	s.CreateSession(ctx, core.Session{TokenHash: "b2", UserID: u2.ID, ExpiresAt: exp})
	if err := s.DeleteUser(ctx, u2.ID); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"b1", "b2"} {
		if _, err := s.GetSession(ctx, h); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("session %s survived user deletion", h)
		}
	}
	if err := s.DeleteUser(ctx, u2.ID); !errors.Is(err, core.ErrNotFound) {
		t.Error(err)
	}
	if err := s.CreateSession(ctx, core.Session{TokenHash: "orph", UserID: 12345, ExpiresAt: exp}); err == nil {
		t.Error("session for missing user should fail (foreign key)")
	}
}

func TestAPIKeys(t *testing.T) {
	s := openTemp(t)
	k, err := s.CreateAPIKey(ctx, core.APIKey{Name: "HR", Prefix: "zk_ab12c", KeyHash: "h1", AllowedIPs: []string{"10.0.0.1", "192.168.0.0/16"}})
	if err != nil || k.ID == 0 {
		t.Fatalf("%+v %v", k, err)
	}
	k2, _ := s.CreateAPIKey(ctx, core.APIKey{Name: "Any", Prefix: "zk_zz", KeyHash: "h2"})
	if _, err := s.CreateAPIKey(ctx, core.APIKey{Name: "dup", KeyHash: "h1"}); !errors.Is(err, core.ErrConflict) {
		t.Errorf("want conflict got %v", err)
	}
	g, err := s.GetAPIKeyByHash(ctx, "h1")
	if err != nil || g.Name != "HR" || len(g.AllowedIPs) != 2 || g.AllowedIPs[1] != "192.168.0.0/16" || g.Revoked || g.LastUsedAt != nil || g.CreatedAt.IsZero() {
		t.Fatalf("%+v %v", g, err)
	}
	if g2, _ := s.GetAPIKeyByHash(ctx, "h2"); len(g2.AllowedIPs) != 0 {
		t.Errorf("%+v", g2)
	}
	if _, err := s.GetAPIKeyByHash(ctx, "none"); !errors.Is(err, core.ErrNotFound) {
		t.Error(err)
	}
	at := wall(2026, 11, 5, 1, 2, 3)
	if err := s.TouchAPIKey(ctx, k.ID, at); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAPIKey(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListAPIKeys(ctx)
	if len(list) != 2 || list[0].ID != k.ID || !list[0].Revoked || list[0].LastUsedAt == nil || !list[0].LastUsedAt.Equal(at) || list[1].ID != k2.ID || list[1].Revoked {
		t.Errorf("%+v", list)
	}
	if g, err := s.GetAPIKeyByHash(ctx, "h1"); err != nil || !g.Revoked {
		t.Errorf("revoked key must still be returned: %+v %v", g, err)
	}
	if err := s.RevokeAPIKey(ctx, 999); !errors.Is(err, core.ErrNotFound) {
		t.Error(err)
	}
	if err := s.TouchAPIKey(ctx, 999, at); !errors.Is(err, core.ErrNotFound) {
		t.Error(err)
	}
}

func TestLogs(t *testing.T) {
	s := openTemp(t)
	dev := int64(3)
	base := wall(2026, 11, 5, 8, 0, 0)
	for i := 0; i < 5010; i++ {
		e := core.LogEntry{At: base.Add(time.Duration(i) * time.Second), Level: "info", Source: "pull", Message: fmt.Sprintf("m%d", i)}
		if i%2 == 1 {
			e.Source = "auth"
			e.DeviceID = &dev
		}
		if err := s.AddLog(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.ListLogs(ctx, 0, "")
	if err != nil || len(all) != 5000 {
		t.Fatalf("len=%d err=%v", len(all), err)
	}
	if all[0].Message != "m5009" || all[4999].Message != "m10" {
		t.Errorf("first %q last %q", all[0].Message, all[4999].Message)
	}
	top, _ := s.ListLogs(ctx, 3, "")
	if len(top) != 3 || top[0].Message != "m5009" || top[2].Message != "m5007" {
		t.Errorf("%+v", top)
	}
	if top[0].DeviceID == nil || *top[0].DeviceID != 3 || !top[0].At.Equal(base.Add(5009*time.Second)) {
		t.Errorf("%+v", top[0])
	}
	if top[1].DeviceID != nil {
		t.Errorf("%+v", top[1])
	}
	au, _ := s.ListLogs(ctx, 2, "auth")
	if len(au) != 2 || au[0].Source != "auth" || au[0].Message != "m5009" || au[1].Message != "m5007" {
		t.Errorf("%+v", au)
	}
	if none, _ := s.ListLogs(ctx, 10, "web"); len(none) != 0 {
		t.Error(none)
	}
}

func TestSettings(t *testing.T) {
	s := openTemp(t)
	got, err := s.GetSettings(ctx)
	if err != nil || got != core.DefaultSettings() {
		t.Fatalf("%+v %v", got, err)
	}
	if err := s.SaveSettings(ctx, core.Settings{PullIntervalMin: 0}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSettings(ctx)
	if got.PullIntervalMin != 0 {
		t.Errorf("0 (disabled) must be stored, got %+v", got)
	}
	s.SaveSettings(ctx, core.Settings{PullIntervalMin: 60})
	got, _ = s.GetSettings(ctx)
	if got.PullIntervalMin != 60 {
		t.Errorf("%+v", got)
	}
}

func TestReopenKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "re.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d := mkDevice(t, s, "Persist")
	s.SaveSnapshot(ctx, d.ID, core.Snapshot{Records: []core.Attendance{rec("1", wall(2026, 11, 5, 8, 0, 0))}}, time.Now())
	s.SaveSettings(ctx, core.Settings{PullIntervalMin: 9})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if l, _ := s2.ListDevices(ctx); len(l) != 1 || l[0].Name != "Persist" {
		t.Errorf("%+v", l)
	}
	if n, _ := s2.CountAttendance(ctx); n != 1 {
		t.Errorf("n=%d", n)
	}
	if st, _ := s2.GetSettings(ctx); st.PullIntervalMin != 9 {
		t.Errorf("%+v", st)
	}
	var v int
	if err := s2.db.QueryRow(`SELECT version FROM schema_version`).Scan(&v); err != nil || v != len(migrations) {
		t.Errorf("version=%d err=%v", v, err)
	}
	var fk int
	s2.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk)
	var jm string
	s2.db.QueryRow(`PRAGMA journal_mode`).Scan(&jm)
	if fk != 1 || jm != "wal" {
		t.Errorf("fk=%d journal=%s", fk, jm)
	}
}

func TestConcurrent(t *testing.T) {
	s := openTemp(t)
	const workers = 20
	devs := make([]core.Device, workers)
	for i := range devs {
		devs[i] = mkDevice(t, s, fmt.Sprintf("dev%02d", i))
	}
	var wg sync.WaitGroup
	errc := make(chan error, workers*100)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			d := devs[w]
			for round := 0; round < 5; round++ {
				var recs []core.Attendance
				for i := 0; i < 20; i++ {
					recs = append(recs, rec(fmt.Sprintf("%d", i%5), wall(2026, 11, 5, 8, round, i)))
				}
				n, err := s.SaveSnapshot(ctx, d.ID, core.Snapshot{
					Users: []core.DeviceUser{{UID: 1, UserID: "1", Name: "N"}}, Records: recs}, time.Now())
				if err != nil {
					errc <- fmt.Errorf("snapshot: %w", err)
					return
				}
				if n != 20 {
					errc <- fmt.Errorf("worker %d round %d new=%d", w, round, n)
				}
				if _, _, err := s.ListAttendance(ctx, core.AttendanceFilter{DeviceID: d.ID, Limit: 10}); err != nil {
					errc <- err
				}
				if _, err := s.ListDeviceStatus(ctx); err != nil {
					errc <- err
				}
				if err := s.AddLog(ctx, core.LogEntry{Level: "info", Source: "pull", Message: "x"}); err != nil {
					errc <- err
				}
				if _, err := s.CountAttendance(ctx); err != nil {
					errc <- err
				}
				if err := s.SavePullFailure(ctx, d.ID, "e", "h", time.Now()); err != nil {
					errc <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Error(err)
	}
	if n, _ := s.CountAttendance(ctx); n != workers*5*20 {
		t.Errorf("attendance=%d want %d", n, workers*5*20)
	}
}
