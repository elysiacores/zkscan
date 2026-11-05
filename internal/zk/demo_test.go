package zk

import (
	"context"
	"slices"
	"testing"
	"time"

	"zkscan/internal/core"
)

func demoDev(id int64) core.Device { return core.Device{ID: id, Demo: true} }

type recKey struct {
	userID string
	ts     time.Time
	vm, ps int
}

func keysOf(recs []core.Attendance) []recKey {
	out := make([]recKey, len(recs))
	for i, r := range recs {
		out[i] = recKey{r.UserID, r.Timestamp, r.VerifyMode, r.PunchState}
	}
	return out
}

func sameKeys(a, b []recKey) bool {
	return slices.EqualFunc(a, b, func(x, y recKey) bool {
		return x.userID == y.userID && x.ts.Equal(y.ts) && x.vm == y.vm && x.ps == y.ps
	})
}

func secOfDay(t time.Time) int { return t.Hour()*3600 + t.Minute()*60 + t.Second() }

// ---- tests (port of demo.rs tests) ----

func TestDemoUsersAndInfo(t *testing.T) {
	s := demoSnapshot(demoDev(3), time.Now())
	if len(s.Users) != 12 || s.Users[0].UserID != "1001" || s.Users[0].Privilege != 14 || s.Users[11].UserID != "1012" {
		t.Fatalf("users %+v", s.Users)
	}
	for i, u := range s.Users {
		if i > 0 && u.Privilege != 0 {
			t.Errorf("user %d privilege %d", i, u.Privilege)
		}
		if u.Card < 100_000 || u.Card > 99_999_999 {
			t.Errorf("card %d", u.Card)
		}
		if u.Name == "" || u.DeviceName != "" || u.DeviceID != 0 {
			t.Errorf("user %+v", u)
		}
	}
	in := s.Info
	if in.Serial != "DEMO-000003" || in.Firmware != "Ver 6.60 (สาธิต)" {
		t.Errorf("info %+v", in)
	}
	if in.UserCount == nil || *in.UserCount != 12 || in.RecordCount == nil || *in.RecordCount != len(s.Records) || in.DeviceTime == nil {
		t.Errorf("counts %+v", in)
	}
}

func TestDemoRecordsShape(t *testing.T) {
	s := demoSnapshot(demoDev(1), time.Now())
	nowAfter := time.Now()
	if len(s.Records) == 0 {
		t.Fatal("no records")
	}
	y, m, d := nowAfter.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	firstDay := time.Date(y, m, d-13, 0, 0, 0, 0, time.Local)
	for i, r := range s.Records {
		if i > 0 && r.Timestamp.Before(s.Records[i-1].Timestamp) {
			t.Fatal("not sorted")
		}
		if r.Timestamp.Before(firstDay) || r.Timestamp.After(nowAfter) {
			t.Errorf("out of range %v", r.Timestamp)
		}
		if r.Timestamp.Weekday() == time.Sunday {
			t.Errorf("sunday %v", r.Timestamp)
		}
		if r.VerifyMode != 1 && r.VerifyMode != 2 && r.VerifyMode != 15 {
			t.Errorf("verify %d", r.VerifyMode)
		}
		if r.Name == "" || r.DeviceID != 0 || r.DeviceName != "" {
			t.Errorf("record %+v", r)
		}
		lo, hi := 7*3600+40*60, 8*3600+25*60+59
		if r.PunchState != 0 {
			lo, hi = 17*3600, 17*3600+45*60+59
		}
		if sod := secOfDay(r.Timestamp); sod < lo || sod > hi {
			t.Errorf("time of day %v punch %d", r.Timestamp, r.PunchState)
		}
		if !r.Timestamp.Before(today) && r.PunchState != 0 {
			t.Errorf("today only has check-in: %v", r.Timestamp)
		}
	}
	if !slices.ContainsFunc(s.Records, func(r core.Attendance) bool { return r.PunchState == 1 }) ||
		!slices.ContainsFunc(s.Records, func(r core.Attendance) bool { return r.VerifyMode == 1 }) {
		t.Error("expected check-outs and fingerprint scans")
	}
}

func TestDemoDeterministicPerID(t *testing.T) {
	now := time.Now()
	a, b := demoSnapshot(demoDev(5), now), demoSnapshot(demoDev(5), now)
	if !sameKeys(keysOf(a.Records), keysOf(b.Records)) {
		t.Error("same id must give same records")
	}
	for i := range a.Users {
		if a.Users[i].Card != b.Users[i].Card {
			t.Error("cards differ")
		}
	}
	c := demoSnapshot(demoDev(6), now)
	if sameKeys(keysOf(a.Records), keysOf(c.Records)) {
		t.Error("different ids must differ")
	}
}

func TestDemoTodayHasNoFutureScans(t *testing.T) {
	users := demoUsers(1)
	day := func(h, m int) time.Time { return time.Date(2026, 11, 4, h, m, 0, 0, time.Local) } // a Wednesday
	dayStart := day(0, 0)
	onDay := func(recs []core.Attendance) int {
		n := 0
		for _, r := range recs {
			if !r.Timestamp.Before(dayStart) && r.Timestamp.Before(dayStart.AddDate(0, 0, 1)) {
				n++
			}
		}
		return n
	}
	early := demoRecords(1, users, day(7, 0))
	if onDay(early) != 0 {
		t.Errorf("early %d", onDay(early))
	}
	mid := demoRecords(1, users, day(8, 0))
	if onDay(mid) <= 0 || onDay(mid) >= 12 {
		t.Errorf("mid %d", onDay(mid))
	}
	for _, r := range mid {
		if r.Timestamp.After(day(8, 0)) {
			t.Errorf("future scan %v", r.Timestamp)
		}
	}
	late := demoRecords(1, users, day(23, 0))
	if onDay(late) < onDay(mid) {
		t.Error("late has fewer scans than mid")
	}
	for _, r := range late {
		if !r.Timestamp.Before(dayStart) && r.PunchState != 0 {
			t.Error("today only has check-in")
		}
	}
	// earlier days do not depend on the time of day
	before := func(v []core.Attendance) []recKey {
		var out []recKey
		for _, r := range v {
			if r.Timestamp.Before(dayStart) {
				out = append(out, recKey{r.UserID, r.Timestamp, 0, 0})
			}
		}
		return out
	}
	if !sameKeys(before(early), before(late)) {
		t.Error("earlier days changed with time of day")
	}
}

func TestDemoSomeAbsencesButMostlyPresent(t *testing.T) {
	s := demoSnapshot(demoDev(1), time.Now())
	n := 0
	for _, r := range s.Records {
		if r.PunchState == 0 {
			n++
		}
	}
	// 14 days minus Sundays = 12 or 13 working days incl. today, 12 staff
	if n <= 100 || n > 13*12 {
		t.Errorf("check-ins %d", n)
	}
}

func TestDemoEntryPoints(t *testing.T) {
	c := New()
	d := demoDev(1)
	info, err := c.TestConnection(context.Background(), d)
	mustNoErr(t, err)
	if info.Serial != "DEMO-000001" {
		t.Errorf("serial %q", info.Serial)
	}
	mustNoErr(t, c.SetTime(context.Background(), d, time.Now()))
	snap, err := c.Fetch(context.Background(), d)
	mustNoErr(t, err)
	if len(snap.Users) != 12 {
		t.Errorf("users %d", len(snap.Users))
	}
	// demo never touches the network: an unusable address is fine
	d.IP, d.Port = "256.256.256.256", -1
	if _, err := c.Fetch(context.Background(), d); err != nil {
		t.Errorf("demo must ignore the address: %v", err)
	}
}
