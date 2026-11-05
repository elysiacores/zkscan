package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"zkscan/internal/core"
)

type fakeStore struct {
	core.Store // unused methods panic
	devices    []core.Device
	status     map[int64]core.DeviceStatus
	rows       []core.Attendance
	total      int
	users      []core.DeviceUser
	lastFilter core.AttendanceFilter
	lastUserID int64
	lastPull   *time.Time
	err        error
}

func (s *fakeStore) ListDevices(context.Context) ([]core.Device, error) { return s.devices, s.err }
func (s *fakeStore) ListDeviceStatus(context.Context) (map[int64]core.DeviceStatus, error) {
	return s.status, s.err
}
func (s *fakeStore) ListAttendance(_ context.Context, f core.AttendanceFilter) ([]core.Attendance, int, error) {
	s.lastFilter = f
	return s.rows, s.total, s.err
}
func (s *fakeStore) ListDeviceUsers(_ context.Context, id int64, _ string) ([]core.DeviceUser, error) {
	s.lastUserID = id
	if s.err != nil {
		return nil, s.err
	}
	var out []core.DeviceUser
	for _, u := range s.users {
		if id == 0 || u.DeviceID == id {
			out = append(out, u)
		}
	}
	return out, nil
}
func (s *fakeStore) CountAttendance(context.Context) (int, error)  { return 12, s.err }
func (s *fakeStore) CountDeviceUsers(context.Context) (int, error) { return 3, s.err }
func (s *fakeStore) LastPullAt(context.Context) (*time.Time, error) {
	return s.lastPull, s.err
}

type fakePuller struct {
	core.Puller
	err     error
	gotID   int64
	allCall bool
}

func (p *fakePuller) PullAll(context.Context) ([]core.PullResult, error) {
	p.allCall = true
	if p.err != nil {
		return nil, p.err
	}
	return []core.PullResult{
		{DeviceID: 1, DeviceName: "A", OK: true, NewRecords: 4, TotalUsers: 2, Duration: 1500 * time.Millisecond, At: time.Date(2026, 11, 5, 8, 0, 0, 0, time.Local)},
		{DeviceID: 2, DeviceName: "B", Error: "หมดเวลา", Hint: "ตรวจสาย"},
	}, nil
}
func (p *fakePuller) PullDevice(_ context.Context, id int64) (core.PullResult, error) {
	p.gotID = id
	if p.err != nil {
		return core.PullResult{}, p.err
	}
	return core.PullResult{DeviceID: id, DeviceName: "A", OK: true}, nil
}

func newApp(s *fakeStore, p *fakePuller) *fiber.App {
	app := fiber.New()
	Register(app.Group("/api"), Deps{Store: s, Puller: p})
	return app
}

func do(t *testing.T, app *fiber.App, method, path string) (int, string, http.Header) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(method, path, nil))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func decode(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("bad json %q: %v", body, err)
	}
	return m
}

func keys(m map[string]any) string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	// sort for stable comparison
	for i := range ks {
		for j := i + 1; j < len(ks); j++ {
			if ks[j] < ks[i] {
				ks[i], ks[j] = ks[j], ks[i]
			}
		}
	}
	return strings.Join(ks, ",")
}

func TestHealth(t *testing.T) {
	lp := time.Date(2026, 11, 5, 9, 30, 1, 0, time.Local)
	app := newApp(&fakeStore{lastPull: &lp}, &fakePuller{})
	code, body, h := do(t, app, "GET", "/api/health")
	if code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "application/json") {
		t.Fatalf("code=%d ct=%s", code, h.Get("Content-Type"))
	}
	m := decode(t, body)
	if keys(m) != "last_pull,records,status,users" || m["status"] != "ok" || m["records"] != 12.0 || m["users"] != 3.0 || m["last_pull"] != "2026-11-05 09:30:01" {
		t.Errorf("body=%s", body)
	}
	_, body, _ = do(t, newApp(&fakeStore{}, &fakePuller{}), "GET", "/api/health")
	if decode(t, body)["last_pull"] != nil {
		t.Errorf("want null last_pull: %s", body)
	}
}

func TestDevices(t *testing.T) {
	lp := time.Date(2026, 11, 5, 8, 0, 0, 0, time.Local)
	s := &fakeStore{
		devices: []core.Device{
			{ID: 1, Name: "A", IP: "10.0.0.1", Port: 4370, CommKey: 123456, Transport: core.TransportTCP, Enabled: true},
			{ID: 2, Name: "B", IP: "10.0.0.2", Port: 4370, CommKey: 654321, Transport: core.TransportUDP, Demo: true},
			{ID: 3, Name: "C", IP: "10.0.0.3", Port: 4370, Transport: core.TransportTCP},
		},
		status: map[int64]core.DeviceStatus{
			1: {DeviceID: 1, LastPullAt: &lp, LastOK: true},
			2: {DeviceID: 2, LastPullAt: &lp, LastOK: false, LastError: "หมดเวลา"},
		},
	}
	code, body, h := do(t, newApp(s, &fakePuller{}), "GET", "/api/devices")
	if code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "application/json") {
		t.Fatalf("code=%d", code)
	}
	if strings.Contains(body, "comm_key") || strings.Contains(body, "123456") || strings.Contains(body, "654321") {
		t.Errorf("comm key leaked: %s", body)
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(body), &arr); err != nil || len(arr) != 3 {
		t.Fatalf("%v %s", err, body)
	}
	want := "demo,enabled,id,ip,last_error,last_ok,last_pull_at,name,port,transport"
	for i, d := range arr {
		if keys(d) != want {
			t.Errorf("device %d keys=%s", i, keys(d))
		}
	}
	if arr[0]["last_pull_at"] != "2026-11-05 08:00:00" || arr[0]["last_ok"] != true || arr[0]["last_error"] != nil || arr[0]["transport"] != "tcp" {
		t.Errorf("dev0 %v", arr[0])
	}
	if arr[1]["last_ok"] != false || arr[1]["last_error"] != "หมดเวลา" || arr[1]["demo"] != true || arr[1]["transport"] != "udp" || arr[1]["enabled"] != false {
		t.Errorf("dev1 %v", arr[1])
	}
	if arr[2]["last_pull_at"] != nil || arr[2]["last_ok"] != nil || arr[2]["last_error"] != nil {
		t.Errorf("dev2 %v", arr[2])
	}
	// empty list is [] not null
	_, body, _ = do(t, newApp(&fakeStore{}, &fakePuller{}), "GET", "/api/devices")
	if strings.TrimSpace(body) != "[]" {
		t.Errorf("empty=%s", body)
	}
}

func TestAttendance(t *testing.T) {
	s := &fakeStore{
		total: 42,
		rows: []core.Attendance{{
			ID: 9, DeviceID: 1, DeviceName: "A", UserID: "007", Name: "สมชาย",
			Timestamp:  time.Date(2026, 11, 4, 8, 1, 2, 0, time.Local),
			VerifyMode: 1, PunchState: 0, PulledAt: time.Now(),
		}},
	}
	app := newApp(s, &fakePuller{})
	code, body, h := do(t, app, "GET", "/api/attendance")
	if code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "application/json") {
		t.Fatalf("code=%d %s", code, body)
	}
	m := decode(t, body)
	if keys(m) != "count,records,total" || m["count"] != 1.0 || m["total"] != 42.0 {
		t.Errorf("body=%s", body)
	}
	rec := m["records"].([]any)[0].(map[string]any)
	if keys(rec) != "device,name,punch_state,timestamp,user_id,verify_mode" ||
		rec["device"] != "A" || rec["user_id"] != "007" || rec["name"] != "สมชาย" ||
		rec["timestamp"] != "2026-11-04 08:01:02" || rec["verify_mode"] != 1.0 || rec["punch_state"] != 0.0 {
		t.Errorf("rec=%v", rec)
	}
	if s.lastFilter.Limit != 5000 || s.lastFilter.Offset != 0 || s.lastFilter.From != nil || s.lastFilter.To != nil {
		t.Errorf("default filter %+v", s.lastFilter)
	}

	code, _, _ = do(t, app, "GET", "/api/attendance?from=2026-11-01&to=2026-11-30&user_id=007&device=A&device_id=3&limit=10&offset=20")
	f := s.lastFilter
	if code != 200 || f.From == nil || f.From.Format(core.DateLayout) != "2026-11-01" ||
		f.To == nil || f.To.Format(core.DateLayout) != "2026-11-30" ||
		f.UserID != "007" || f.DeviceName != "A" || f.DeviceID != 3 || f.Limit != 10 || f.Offset != 20 {
		t.Errorf("filter %+v", f)
	}

	// empty result is [] not null
	_, body, _ = do(t, newApp(&fakeStore{}, &fakePuller{}), "GET", "/api/attendance")
	if !strings.Contains(body, `"records":[]`) {
		t.Errorf("body=%s", body)
	}
}

func TestAttendanceBadParams(t *testing.T) {
	app := newApp(&fakeStore{}, &fakePuller{})
	cases := []struct{ path, msg string }{
		{"/api/attendance?from=2026-13-01", "invalid date for 'from', expected YYYY-MM-DD"},
		{"/api/attendance?from=nope", "invalid date for 'from', expected YYYY-MM-DD"},
		{"/api/attendance?to=01/02/2026", "invalid date for 'to', expected YYYY-MM-DD"},
		{"/api/attendance?limit=abc", ""},
		{"/api/attendance?limit=0", ""},
		{"/api/attendance?limit=-5", ""},
		{"/api/attendance?limit=50001", ""},
		{"/api/attendance?offset=-1", ""},
		{"/api/attendance?offset=x", ""},
		{"/api/attendance?device_id=abc", ""},
		{"/api/attendance?device_id=0", ""},
	}
	for _, c := range cases {
		code, body, h := do(t, app, "GET", c.path)
		if code != 400 || !strings.HasPrefix(h.Get("Content-Type"), "application/json") {
			t.Errorf("%s: code=%d", c.path, code)
			continue
		}
		e, _ := decode(t, body)["error"].(string)
		if e == "" || (c.msg != "" && e != c.msg) {
			t.Errorf("%s: error=%q", c.path, e)
		}
	}
	s := &fakeStore{}
	app = newApp(s, &fakePuller{})
	for _, p := range []string{"limit=1", "limit=50000", "offset=0"} {
		if code, _, _ := do(t, app, "GET", "/api/attendance?"+p); code != 200 {
			t.Errorf("%s: code=%d", p, code)
		}
	}
	if s.lastFilter.Offset != 0 {
		t.Errorf("filter %+v", s.lastFilter)
	}
}

func TestUsers(t *testing.T) {
	s := &fakeStore{
		devices: []core.Device{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}},
		users: []core.DeviceUser{
			{DeviceID: 1, DeviceName: "A", UID: 1, UserID: "007", Name: "สมชาย", Privilege: 14, Card: 99},
			{DeviceID: 2, DeviceName: "B", UID: 5, UserID: "008", Name: "สมหญิง"},
		},
	}
	app := newApp(s, &fakePuller{})

	code, body, h := do(t, app, "GET", "/api/users")
	m := decode(t, body)
	if code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "application/json") || m["count"] != 2.0 || keys(m) != "count,users" {
		t.Fatalf("%d %s", code, body)
	}
	u := m["users"].([]any)[0].(map[string]any)
	if keys(u) != "card,device,name,privilege,uid,user_id" || u["device"] != "A" || u["uid"] != 1.0 ||
		u["user_id"] != "007" || u["name"] != "สมชาย" || u["privilege"] != 14.0 || u["card"] != 99.0 {
		t.Errorf("user=%v", u)
	}

	_, body, _ = do(t, app, "GET", "/api/users?device=B")
	m = decode(t, body)
	if m["count"] != 1.0 || s.lastUserID != 2 || m["users"].([]any)[0].(map[string]any)["user_id"] != "008" {
		t.Errorf("by name: %s id=%d", body, s.lastUserID)
	}

	_, body, _ = do(t, app, "GET", "/api/users?device_id=1")
	m = decode(t, body)
	if m["count"] != 1.0 || s.lastUserID != 1 || m["users"].([]any)[0].(map[string]any)["user_id"] != "007" {
		t.Errorf("by id: %s", body)
	}

	_, body, _ = do(t, app, "GET", "/api/users?device=ZZZ")
	if !strings.Contains(body, `"count":0`) || !strings.Contains(body, `"users":[]`) {
		t.Errorf("unknown device: %s", body)
	}

	if code, _, _ = do(t, app, "GET", "/api/users?device_id=x"); code != 400 {
		t.Errorf("bad device_id code=%d", code)
	}
}

func TestPullAll(t *testing.T) {
	p := &fakePuller{}
	code, body, h := do(t, newApp(&fakeStore{}, p), "POST", "/api/pull")
	if code != 200 || !p.allCall || !strings.HasPrefix(h.Get("Content-Type"), "application/json") {
		t.Fatalf("%d %s", code, body)
	}
	m := decode(t, body)
	rs := m["results"].([]any)
	if keys(m) != "results" || len(rs) != 2 {
		t.Fatalf("%s", body)
	}
	a := rs[0].(map[string]any)
	if keys(a) != "at,device,device_id,duration_ms,error,hint,new_records,ok,total_users" ||
		a["device_id"] != 1.0 || a["device"] != "A" || a["ok"] != true || a["new_records"] != 4.0 ||
		a["total_users"] != 2.0 || a["error"] != nil || a["hint"] != nil || a["duration_ms"] != 1500.0 ||
		a["at"] != "2026-11-05 08:00:00" {
		t.Errorf("a=%v", a)
	}
	b := rs[1].(map[string]any)
	if b["ok"] != false || b["error"] != "หมดเวลา" || b["hint"] != "ตรวจสาย" || b["at"] != nil {
		t.Errorf("b=%v", b)
	}
}

func TestPullOne(t *testing.T) {
	p := &fakePuller{}
	code, body, _ := do(t, newApp(&fakeStore{}, p), "POST", "/api/pull?device_id=7")
	if code != 200 || p.allCall || p.gotID != 7 {
		t.Fatalf("%d %s all=%v id=%d", code, body, p.allCall, p.gotID)
	}
	rs := decode(t, body)["results"].([]any)
	if len(rs) != 1 || rs[0].(map[string]any)["device_id"] != 7.0 {
		t.Errorf("%s", body)
	}
	if code, _, _ := do(t, newApp(&fakeStore{}, p), "POST", "/api/pull?device_id=abc"); code != 400 {
		t.Errorf("bad id code=%d", code)
	}
}

func TestPullErrors(t *testing.T) {
	for _, path := range []string{"/api/pull", "/api/pull?device_id=1"} {
		code, body, h := do(t, newApp(&fakeStore{}, &fakePuller{err: core.ErrBusy}), "POST", path)
		if code != 409 || !strings.HasPrefix(h.Get("Content-Type"), "application/json") ||
			decode(t, body)["error"] != "กำลังดึงข้อมูลอยู่ ลองใหม่อีกครั้ง" {
			t.Errorf("busy %s: %d %s", path, code, body)
		}
	}
	code, body, _ := do(t, newApp(&fakeStore{}, &fakePuller{err: core.ErrNotFound}), "POST", "/api/pull?device_id=99")
	if code != 404 || decode(t, body)["error"] == nil {
		t.Errorf("notfound: %d %s", code, body)
	}
	code, body, _ = do(t, newApp(&fakeStore{}, &fakePuller{err: errors.New("secret db path /x")}), "POST", "/api/pull")
	if code != 500 || strings.Contains(body, "secret") || decode(t, body)["error"] != "internal error" {
		t.Errorf("500: %d %s", code, body)
	}
}

func TestStoreErrors500(t *testing.T) {
	s := &fakeStore{err: errors.New("sqlite: disk /secret/path failed")}
	app := newApp(s, &fakePuller{})
	for _, path := range []string{"/api/health", "/api/devices", "/api/attendance", "/api/users", "/api/users?device=A"} {
		code, body, h := do(t, app, "GET", path)
		if code != 500 || !strings.HasPrefix(h.Get("Content-Type"), "application/json") ||
			strings.Contains(body, "secret") || decode(t, body)["error"] != "internal error" {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
}

func TestCatchAll404(t *testing.T) {
	app := newApp(&fakeStore{}, &fakePuller{})
	for _, c := range [][2]string{{"GET", "/api/nope"}, {"GET", "/api/a/b/c"}, {"POST", "/api/nope"}, {"GET", "/api/"}, {"DELETE", "/api/devices"}} {
		code, body, h := do(t, app, c[0], c[1])
		if code != 404 || !strings.HasPrefix(h.Get("Content-Type"), "application/json") || body != `{"error":"not found"}` {
			t.Errorf("%v: %d %s %s", c, code, body, h.Get("Content-Type"))
		}
	}
}
