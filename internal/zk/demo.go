package zk

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"zkscan/internal/core"
)

var demoNames = [12]string{
	"สมชาย ใจดี",
	"สุดา รักงาน",
	"วิชัย มั่นคง",
	"มาลี ศรีสุข",
	"ประเสริฐ ทองดี",
	"อรุณี แสงทอง",
	"ธนา พูนผล",
	"กมล สายใจ",
	"นภา ฟ้าใส",
	"สมศักดิ์ ดีเลิศ",
	"พรทิพย์ ใจงาม",
	"อนันต์ สุขสันต์",
}

// lcg is a tiny deterministic generator (LCG with a splitmix-style seed mix).
type lcg uint64

func newLCG(seed uint64) *lcg {
	z := seed + 0x9E3779B97F4A7C15
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	g := lcg(z ^ (z >> 31))
	return &g
}

func (g *lcg) next() uint64 {
	*g = *g*6364136223846793005 + 1442695040888963407
	return uint64(*g) >> 33
}

// below is a uniform-ish value in 0..n.
func (g *lcg) below(n uint64) uint64 { return g.next() % n }

func demoUsers(id uint64) []core.DeviceUser {
	out := make([]core.DeviceUser, len(demoNames))
	for i, name := range demoNames {
		uid := i + 1
		rng := newLCG(id*1_000_003 + uint64(uid))
		priv := 0
		if i == 0 {
			priv = 14
		}
		out[i] = core.DeviceUser{
			UID:       uid,
			UserID:    fmt.Sprint(1001 + i),
			Name:      name,
			Privilege: priv,
			Card:      uint32(100_000 + rng.below(90_000_000)), // 6 to 8 digits
		}
	}
	return out
}

// numDaysFromCE is chrono's num_days_from_ce (0001-01-01 = day 1).
func numDaysFromCE(y int, m time.Month, d int) int64 {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix()/86400 + 719163
}

func demoRecords(id uint64, users []core.DeviceUser, now time.Time) []core.Attendance {
	ny, nm, nd := now.Date()
	var out []core.Attendance
	for back := 13; back >= 0; back-- {
		y, m, d := time.Date(ny, nm, nd-back, 0, 0, 0, 0, time.UTC).Date()
		isToday := back == 0
		if time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Weekday() == time.Sunday {
			continue
		}
		for _, u := range users {
			// seeded per (device, day, employee) so a given day never changes between runs
			seed := id*0x1_0000_01B3 + uint64(numDaysFromCE(y, m, d))*131 + uint64(u.UID)
			rng := newLCG(seed)
			if rng.below(12) == 0 {
				continue // absent that day
			}
			var verify int
			switch v := rng.below(20); {
			case v <= 13:
				verify = 1
			case v <= 16:
				verify = 2
			default:
				verify = 15
			}
			at := func(h, mi, s int) time.Time { return time.Date(y, m, d, h, mi, s, 0, time.Local) }
			// check-in 07:40..08:25
			inMin := 7*60 + 40 + int(rng.below(46))
			inTS := at(inMin/60, inMin%60, int(rng.below(60)))
			// today: no scans from the future
			if !isToday || !inTS.After(now) {
				out = append(out, core.Attendance{
					UserID: u.UserID, Name: u.Name, Timestamp: inTS, VerifyMode: verify, PunchState: 0,
				})
			}
			// check-out 17:00..17:45 (not for today)
			outMin := 17*60 + int(rng.below(46))
			outSec := int(rng.below(60))
			if !isToday {
				out = append(out, core.Attendance{
					UserID: u.UserID, Name: u.Name, Timestamp: at(outMin/60, outMin%60, outSec), VerifyMode: verify, PunchState: 1,
				})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b core.Attendance) int {
		if c := a.Timestamp.Compare(b.Timestamp); c != 0 {
			return c
		}
		return strings.Compare(a.UserID, b.UserID)
	})
	return out
}

// demoSnapshot is the simulated device data: 12 employees and 14 days of attendance, no network.
func demoSnapshot(d core.Device, now time.Time) core.Snapshot {
	now = now.In(time.Local)
	id := uint64(d.ID)
	users := demoUsers(id)
	records := demoRecords(id, users, now)
	if records == nil {
		records = []core.Attendance{}
	}
	uc, rc := len(users), len(records)
	devTime := now
	return core.Snapshot{
		Info: core.DeviceInfo{
			Serial:      fmt.Sprintf("DEMO-%06d", d.ID),
			Firmware:    "Ver 6.60 (สาธิต)",
			Platform:    "ZMM220_TFT",
			DeviceName:  "เครื่องสาธิต",
			UserCount:   &uc,
			RecordCount: &rc,
			DeviceTime:  &devTime,
		},
		Users:   users,
		Records: records,
	}
}
