package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"zkscan/internal/core"
)

const xss = "<script>alert(1)</script>"

// ---------------- fakes (embed interface: method ที่ไม่ได้ทำจะ panic) ----------------

type fakeStore struct {
	core.Store
	mu        sync.Mutex
	devices   []core.Device
	users     []core.User
	keys      []core.APIKey
	logs      []core.LogEntry
	att       []core.Attendance
	staff     []core.DeviceUser
	settings  core.Settings
	deleted   []int64
	revoked   []int64
	saved     []core.Settings
	listErr   error
	lastFilt  core.AttendanceFilter
	nextDevID int64
}

func (s *fakeStore) ListDevices(context.Context) ([]core.Device, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return append([]core.Device(nil), s.devices...), nil
}
func (s *fakeStore) GetDevice(_ context.Context, id int64) (core.Device, error) {
	for _, d := range s.devices {
		if d.ID == id {
			return d, nil
		}
	}
	return core.Device{}, core.ErrNotFound
}
func (s *fakeStore) CreateDevice(_ context.Context, d core.Device) (core.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.devices {
		if x.Name == d.Name {
			return core.Device{}, core.ErrConflict
		}
	}
	s.nextDevID++
	d.ID = 100 + s.nextDevID
	s.devices = append(s.devices, d)
	return d, nil
}
func (s *fakeStore) UpdateDevice(_ context.Context, d core.Device) error {
	for i := range s.devices {
		if s.devices[i].ID == d.ID {
			s.devices[i] = d
			return nil
		}
	}
	return core.ErrNotFound
}
func (s *fakeStore) DeleteDevice(_ context.Context, id int64) error {
	s.deleted = append(s.deleted, id)
	return nil
}
func (s *fakeStore) ListDeviceStatus(context.Context) (map[int64]core.DeviceStatus, error) {
	now := time.Now()
	m := map[int64]core.DeviceStatus{}
	for _, d := range s.devices {
		m[d.ID] = core.DeviceStatus{DeviceID: d.ID, LastPullAt: &now, LastOK: false, LastError: "ต่อเครื่องไม่ได้ " + xss, LastHint: "ตรวจสาย LAN " + xss}
	}
	return m, nil
}
func (s *fakeStore) ListAttendance(_ context.Context, f core.AttendanceFilter) ([]core.Attendance, int, error) {
	s.lastFilt = f
	rows := s.att
	total := len(rows)
	if f.Limit > 0 {
		end := f.Offset + f.Limit
		if f.Offset > len(rows) {
			return nil, total, nil
		}
		if end > len(rows) {
			end = len(rows)
		}
		rows = rows[f.Offset:end]
	}
	return rows, total, nil
}
func (s *fakeStore) ListDeviceUsers(context.Context, int64, string) ([]core.DeviceUser, error) {
	return s.staff, nil
}
func (s *fakeStore) CountAttendance(context.Context) (int, error)  { return len(s.att), nil }
func (s *fakeStore) CountDeviceUsers(context.Context) (int, error) { return len(s.staff), nil }
func (s *fakeStore) LastPullAt(context.Context) (*time.Time, error) {
	t := time.Now()
	return &t, nil
}
func (s *fakeStore) ListUsers(context.Context) ([]core.User, error) {
	return append([]core.User(nil), s.users...), nil
}
func (s *fakeStore) GetUserByID(_ context.Context, id int64) (core.User, error) {
	for _, u := range s.users {
		if u.ID == id {
			return u, nil
		}
	}
	return core.User{}, core.ErrNotFound
}
func (s *fakeStore) DeleteUser(_ context.Context, id int64) error {
	s.deleted = append(s.deleted, -id)
	return nil
}
func (s *fakeStore) ListAPIKeys(context.Context) ([]core.APIKey, error) { return s.keys, nil }
func (s *fakeStore) RevokeAPIKey(_ context.Context, id int64) error {
	s.revoked = append(s.revoked, id)
	return nil
}
func (s *fakeStore) AddLog(_ context.Context, e core.LogEntry) error {
	s.logs = append(s.logs, e)
	return nil
}
func (s *fakeStore) ListLogs(context.Context, int, string) ([]core.LogEntry, error) {
	return []core.LogEntry{{At: time.Now(), Level: "error", Source: "pull", Message: "ล้มเหลว " + xss}}, nil
}
func (s *fakeStore) GetSettings(context.Context) (core.Settings, error) { return s.settings, nil }
func (s *fakeStore) SaveSettings(_ context.Context, st core.Settings) error {
	s.saved = append(s.saved, st)
	return nil
}

type fakeAuth struct {
	core.AuthService
	loginErr error
	loginOK  core.User
	chgErr   error
	keyErr   error
	ips      []string
}

func (a *fakeAuth) Login(_ context.Context, u, p, ip string) (string, core.User, error) {
	if a.loginErr != nil {
		return "", core.User{}, a.loginErr
	}
	return "tok123", a.loginOK, nil
}
func (a *fakeAuth) Logout(context.Context, string) error { return nil }
func (a *fakeAuth) ChangePassword(_ context.Context, _ int64, _, _, _ string) error {
	return a.chgErr
}
func (a *fakeAuth) CreateUser(_ context.Context, name string) (core.User, string, error) {
	return core.User{Username: name}, "TempPass1234", nil
}
func (a *fakeAuth) ResetPassword(context.Context, string) (string, error) { return "ResetPw5678", nil }
func (a *fakeAuth) CreateAPIKey(_ context.Context, name string, ips []string) (string, core.APIKey, error) {
	a.ips = ips
	if a.keyErr != nil {
		return "", core.APIKey{}, a.keyErr
	}
	return "zk_SECRETKEYVALUE", core.APIKey{Name: name}, nil
}

type fakePuller struct {
	core.Puller
	err error
}

func (p *fakePuller) Running() bool { return false }
func (p *fakePuller) PullAll(context.Context) ([]core.PullResult, error) {
	return []core.PullResult{{DeviceName: xss, OK: false, Error: "ต่อไม่ได้", Hint: "ตรวจสาย"}}, p.err
}
func (p *fakePuller) PullDevice(context.Context, int64) (core.PullResult, error) {
	return core.PullResult{DeviceName: "A", OK: true, NewRecords: 3, TotalUsers: 9}, p.err
}

type fakeClient struct {
	core.DeviceClient
	err error
}

func (c *fakeClient) TestConnection(context.Context, core.Device) (core.DeviceInfo, error) {
	return core.DeviceInfo{Serial: "SN1"}, c.err
}
func (c *fakeClient) SetTime(context.Context, core.Device, time.Time) error { return c.err }

// ---------------- harness ----------------

type env struct {
	app    *fiber.App
	store  *fakeStore
	auth   *fakeAuth
	puller *fakePuller
	client *fakeClient
}

var admin = core.User{ID: 1, Username: "admin"}

func newEnv(t *testing.T, cur core.User) *env {
	t.Helper()
	e := &env{
		store: &fakeStore{
			devices: []core.Device{{ID: 1, Name: xss, IP: "10.0.0.5", Port: 4370, CommKey: 424242, Transport: core.TransportTCP, TimeoutSec: 10, Enabled: true}},
			users:   []core.User{admin},
			keys:    []core.APIKey{{ID: 7, Name: xss, Prefix: "zk_ab12c", AllowedIPs: []string{"10.0.0.0/24"}, CreatedAt: time.Now()}},
			att: []core.Attendance{{DeviceID: 1, DeviceName: xss, UserID: "1001", Name: xss,
				Timestamp: time.Date(2026, 11, 4, 8, 23, 27, 0, time.Local), VerifyMode: 1, PunchState: 0}},
			staff:    []core.DeviceUser{{DeviceName: xss, UID: 1, UserID: "1001", Name: xss, Privilege: 14, Card: 55}},
			settings: core.DefaultSettings(),
		},
		auth: &fakeAuth{}, puller: &fakePuller{}, client: &fakeClient{},
	}
	w, err := New(Deps{Store: e.store, Auth: e.auth, Puller: e.puller, Client: e.client, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	e.app = fiber.New()
	w.RegisterPublic(e.app)
	g := e.app.Group("", func(c *fiber.Ctx) error {
		c.Locals(core.LocalsUser, cur)
		c.Locals(core.LocalsToken, "tok")
		return c.Next()
	})
	w.RegisterProtected(g)
	return e
}

func (e *env) do(t *testing.T, method, path string, form url.Values, cookies ...*http.Cookie) (*http.Response, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	req.Host = "zk.local:6565"
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := e.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func mustContain(t *testing.T, body string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(body, s) {
			t.Errorf("ไม่พบ %q ในหน้า\n%s", s, trunc(body))
		}
	}
}
func mustNotContain(t *testing.T, body string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(body, s) {
			t.Errorf("ต้องไม่มี %q ในหน้า", s)
		}
	}
}
func trunc(s string) string {
	if len(s) > 1500 {
		return s[:1500] + "…"
	}
	return s
}

// ---------------- tests ----------------

func TestAllPagesRender200(t *testing.T) {
	e := newEnv(t, admin)
	paths := []string{
		"/", "/devices", "/devices/new", "/devices/1/edit", "/records", "/records?from=2026-11-01&to=2026-11-05&q=x&device=1",
		"/staff", "/staff?q=a", "/api-keys", "/users", "/logs", "/logs?source=pull", "/settings", "/change-password", "/login",
		"/static/style.css", "/static/app.js", "/static/fonts/Sarabun-Regular.ttf",
	}
	for _, p := range paths {
		resp, body := e.do(t, "GET", p, nil)
		if resp.StatusCode != 200 {
			t.Errorf("%s -> %d\n%s", p, resp.StatusCode, trunc(body))
		}
	}
	_, body := e.do(t, "GET", "/", nil)
	mustContain(t, body, "ดึงข้อมูลทุกเครื่อง", "admin", "ออกจากระบบ", `action="/logout"`)
}

func TestNotFoundDevice(t *testing.T) {
	e := newEnv(t, admin)
	resp, body := e.do(t, "GET", "/devices/999/edit", nil)
	if resp.StatusCode != 404 {
		t.Fatalf("got %d", resp.StatusCode)
	}
	mustContain(t, body, "ไม่พบข้อมูล")
	resp, _ = e.do(t, "GET", "/devices/abc/edit", nil)
	if resp.StatusCode != 404 {
		t.Fatalf("bad id got %d", resp.StatusCode)
	}
}

func TestStoreErrorIs500ThaiNoDetails(t *testing.T) {
	e := newEnv(t, admin)
	e.store.listErr = errors.New("sqlite: disk I/O error SECRET_INTERNAL")
	resp, body := e.do(t, "GET", "/devices", nil)
	if resp.StatusCode != 500 {
		t.Fatalf("got %d", resp.StatusCode)
	}
	mustContain(t, body, "ระบบขัดข้อง")
	mustNotContain(t, body, "SECRET_INTERNAL", "sqlite")
}

func TestLogin(t *testing.T) {
	e := newEnv(t, admin)
	form := url.Values{"username": {"admin"}, "password": {"pw"}}

	e.auth.loginOK = core.User{ID: 1, Username: "admin"}
	resp, _ := e.do(t, "POST", "/login", form)
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/" {
		t.Fatalf("success: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	var ck *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "zkscan_session" {
			ck = c
		}
	}
	if ck == nil || ck.Value != "tok123" || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" || ck.Secure {
		t.Fatalf("cookie ผิด: %+v", ck)
	}
	if ck.MaxAge != 12*3600 {
		t.Errorf("max-age %d (ต้อง 12 ชั่วโมง)", ck.MaxAge)
	}

	e.auth.loginOK.MustChangePassword = true
	resp, _ = e.do(t, "POST", "/login", form)
	if resp.Header.Get("Location") != "/change-password" {
		t.Errorf("must change: %s", resp.Header.Get("Location"))
	}

	e.auth.loginErr = core.ErrBadCredentials
	resp, body := e.do(t, "POST", "/login", url.Values{"username": {xss}, "password": {"x"}})
	if resp.StatusCode != 401 {
		t.Errorf("bad creds %d", resp.StatusCode)
	}
	mustContain(t, body, "ชื่อผู้ใช้หรือรหัสผ่านไม่ถูกต้อง", "&lt;script&gt;")
	mustNotContain(t, body, xss)
	for _, c := range resp.Cookies() {
		if c.Name == "zkscan_session" && c.Value != "" {
			t.Error("ล็อกอินพลาดแต่ตั้ง cookie")
		}
	}

	e.auth.loginErr = core.ErrTooManyAttempts
	resp, body = e.do(t, "POST", "/login", form)
	if resp.StatusCode != 429 {
		t.Errorf("locked %d", resp.StatusCode)
	}
	mustContain(t, body, "ลองใหม่ใน 15 นาที")
}

func TestLogoutClearsCookie(t *testing.T) {
	e := newEnv(t, admin)
	resp, _ := e.do(t, "POST", "/logout", url.Values{})
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/login" {
		t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	found := false
	for _, c := range resp.Cookies() {
		if c.Name == "zkscan_session" && c.Value == "" && c.Expires.Before(time.Now()) {
			found = true
		}
	}
	if !found {
		t.Error("ไม่ได้ล้าง cookie")
	}
}

func TestChangePassword(t *testing.T) {
	e := newEnv(t, admin)
	post := func(o, n, c string) (int, string) {
		resp, body := e.do(t, "POST", "/change-password", url.Values{"old_password": {o}, "new_password": {n}, "confirm_password": {c}})
		return resp.StatusCode, body
	}
	if code, body := post("a", "newpassword1", "different"); code != 400 {
		t.Errorf("mismatch %d", code)
	} else {
		mustContain(t, body, "ไม่ตรงกัน")
	}
	e.auth.chgErr = core.ErrWeakPassword
	if code, body := post("a", "short", "short"); code != 400 {
		t.Errorf("weak %d", code)
	} else {
		mustContain(t, body, "อย่างน้อย 8 ตัวอักษร")
	}
	e.auth.chgErr = core.ErrBadCredentials
	if _, body := post("wrong", "newpassword1", "newpassword1"); !strings.Contains(body, "รหัสผ่านเดิมไม่ถูกต้อง") {
		t.Error("old pw message")
	}
	e.auth.chgErr = nil
	resp, _ := e.do(t, "POST", "/change-password", url.Values{"old_password": {"a"}, "new_password": {"newpassword1"}, "confirm_password": {"newpassword1"}})
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/" {
		t.Errorf("ok %d", resp.StatusCode)
	}
}

func devForm(over map[string]string) url.Values {
	v := url.Values{"name": {"เครื่องหน้าบริษัท"}, "ip": {"192.168.1.201"}, "port": {"4370"}, "comm_key": {"0"}, "transport": {"tcp"}, "timeout": {"10"}, "enabled": {"1"}}
	for k, s := range over {
		v.Set(k, s)
	}
	return v
}

func TestDeviceCreateValidation(t *testing.T) {
	e := newEnv(t, admin)
	cases := []struct {
		over map[string]string
		msg  string
	}{
		{map[string]string{"name": " "}, "กรอกชื่อเครื่อง"},
		{map[string]string{"ip": ""}, "กรอก IP หรือชื่อโฮสต์"},
		{map[string]string{"ip": "bad host!"}, "IP หรือชื่อโฮสต์ไม่ถูกต้อง"},
		{map[string]string{"port": "0"}, "พอร์ตต้องเป็นตัวเลข 1 ถึง 65535"},
		{map[string]string{"port": "65536"}, "พอร์ตต้องเป็นตัวเลข"},
		{map[string]string{"port": "abc"}, "พอร์ตต้องเป็นตัวเลข"},
		{map[string]string{"comm_key": "1000000"}, "Comm Key ต้องเป็นตัวเลข 0 ถึง 999999"},
		{map[string]string{"comm_key": "-1"}, "Comm Key"},
		{map[string]string{"timeout": "0"}, "1 ถึง 120"},
		{map[string]string{"timeout": "121"}, "1 ถึง 120"},
		{map[string]string{"transport": "ftp"}, "TCP หรือ UDP"},
	}
	before := len(e.store.devices)
	for _, c := range cases {
		resp, body := e.do(t, "POST", "/devices", devForm(c.over))
		if resp.StatusCode != 422 {
			t.Errorf("%v: status %d", c.over, resp.StatusCode)
		}
		mustContain(t, body, c.msg)
	}
	if len(e.store.devices) != before {
		t.Error("สร้างเครื่องทั้งที่ข้อมูลผิด")
	}

	// ข้อมูลที่กรอกต้องไม่หาย
	_, body := e.do(t, "POST", "/devices", devForm(map[string]string{"ip": "", "name": "เครื่องของฉัน", "port": "5000"}))
	mustContain(t, body, `value="เครื่องของฉัน"`, `value="5000"`)

	// ชื่อซ้ำ
	resp, body := e.do(t, "POST", "/devices", devForm(map[string]string{"name": xss}))
	if resp.StatusCode != 422 {
		t.Errorf("dup %d", resp.StatusCode)
	}
	mustContain(t, body, "มีเครื่องชื่อนี้อยู่แล้ว")

	// สำเร็จ
	resp, _ = e.do(t, "POST", "/devices", devForm(nil))
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/devices" {
		t.Fatalf("ok %d", resp.StatusCode)
	}
	if len(e.store.devices) != before+1 {
		t.Fatal("ไม่ได้สร้าง")
	}
	d := e.store.devices[len(e.store.devices)-1]
	if d.Port != 4370 || d.Transport != core.TransportTCP || d.TimeoutSec != 10 || !d.Enabled {
		t.Errorf("%+v", d)
	}
}

func TestFlashAfterRedirectIsEscapedAndOneShot(t *testing.T) {
	e := newEnv(t, admin)
	resp, _ := e.do(t, "POST", "/devices", devForm(map[string]string{"name": xss + "2"}))
	var fl *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "zkscan_flash" {
			fl = c
		}
	}
	if fl == nil {
		t.Fatal("ไม่มี flash cookie")
	}
	resp, body := e.do(t, "GET", "/devices", nil, fl)
	mustContain(t, body, "เพิ่มเครื่อง", "&lt;script&gt;alert(1)&lt;/script&gt;2")
	mustNotContain(t, body, xss)
	cleared := false
	for _, c := range resp.Cookies() {
		if c.Name == "zkscan_flash" && c.Value == "" {
			cleared = true
		}
	}
	if !cleared {
		t.Error("flash ไม่ถูกล้างหลังแสดง")
	}
}

func TestXSSEscapedEverywhere(t *testing.T) {
	e := newEnv(t, admin)
	for _, p := range []string{"/", "/devices", "/devices/1/edit", "/records", "/staff", "/api-keys", "/logs"} {
		resp, body := e.do(t, "GET", p, nil)
		if resp.StatusCode != 200 {
			t.Errorf("%s %d", p, resp.StatusCode)
		}
		if strings.Contains(body, xss) {
			t.Errorf("%s: พบ <script> ที่ไม่ถูก escape", p)
		}
	}
	// ผลการดึง/ทดสอบ
	_, body := e.do(t, "POST", "/pull-all", url.Values{})
	mustNotContain(t, body, xss)
	mustContain(t, body, "&lt;script&gt;")
	// เกี่ยวกับ query ที่สะท้อนกลับ
	_, body = e.do(t, "GET", "/records?q="+url.QueryEscape(xss), nil)
	mustNotContain(t, body, xss)
	// ฟอร์มที่ส่งกลับพร้อมชื่อที่เป็น script
	_, body = e.do(t, "POST", "/devices", devForm(map[string]string{"name": xss, "ip": ""}))
	mustNotContain(t, body, xss)
	mustContain(t, body, "&lt;script&gt;")
}

func TestCommKeyOnlyInEditForm(t *testing.T) {
	e := newEnv(t, admin)
	for _, p := range []string{"/", "/devices", "/records", "/staff"} {
		_, body := e.do(t, "GET", p, nil)
		mustNotContain(t, body, "424242")
	}
	_, body := e.do(t, "GET", "/devices/1/edit", nil)
	mustNotContain(t, body, "424242")
	mustContain(t, body, `type="password"`, `autocomplete="new-password"`, "เว้นว่างไว้ = ใช้รหัสเดิม")
}

func TestCommKeyWriteOnly(t *testing.T) {
	e := newEnv(t, admin)
	key := func() int { return e.store.devices[0].CommKey }
	upd := func(over map[string]string) (*http.Response, string) {
		return e.do(t, "POST", "/devices/1", devForm(over))
	}
	if key() != 424242 {
		t.Fatal(key())
	}
	if resp, _ := upd(map[string]string{"comm_key": ""}); resp.StatusCode != 303 || key() != 424242 {
		t.Errorf("blank must keep: %d key=%d", resp.StatusCode, key())
	}
	if resp, _ := upd(map[string]string{"comm_key": "777"}); resp.StatusCode != 303 || key() != 777 {
		t.Errorf("value must change: %d key=%d", resp.StatusCode, key())
	}
	if resp, body := upd(map[string]string{"comm_key": "1234567"}); resp.StatusCode != 422 || key() != 777 {
		t.Errorf("bad key: %d key=%d", resp.StatusCode, key())
	} else {
		mustContain(t, body, "Comm Key ต้องเป็นตัวเลข 0 ถึง 999999")
		mustNotContain(t, body, "1234567")
	}
	// validation error elsewhere must not echo the typed key
	_, body := upd(map[string]string{"comm_key": "5551212", "ip": ""})
	mustNotContain(t, body, "5551212")
	// create: blank stores 0; error page does not echo
	resp, _ := e.do(t, "POST", "/devices", devForm(map[string]string{"name": "ใหม่", "comm_key": ""}))
	if resp.StatusCode != 303 {
		t.Fatalf("create %d", resp.StatusCode)
	}
	last := e.store.devices[len(e.store.devices)-1]
	if last.Name != "ใหม่" || last.CommKey != 0 {
		t.Errorf("%+v", last)
	}
	_, body = e.do(t, "POST", "/devices", devForm(map[string]string{"comm_key": "8675309", "ip": ""}))
	mustNotContain(t, body, "8675309")
}

func TestDeviceActionsShowThaiMessageAndHint(t *testing.T) {
	e := newEnv(t, admin)
	e.client.err = fmt.Errorf("wrap: %w", &core.DeviceError{Kind: core.DevErrTimeout, Message: "เครื่องไม่ตอบสนอง", Hint: "ตรวจสาย LAN และ IP"})
	for _, p := range []string{"/devices/1/test", "/devices/1/settime"} {
		resp, body := e.do(t, "POST", p, url.Values{})
		if resp.StatusCode != 200 {
			t.Errorf("%s %d", p, resp.StatusCode)
		}
		mustContain(t, body, "เครื่องไม่ตอบสนอง", "ตรวจสาย LAN และ IP")
	}
	e.client.err = nil
	_, body := e.do(t, "POST", "/devices/1/test", url.Values{})
	mustContain(t, body, "เชื่อมต่อเครื่องได้สำเร็จ", "SN1")
	_, body = e.do(t, "POST", "/devices/1/settime", url.Values{})
	mustContain(t, body, "เรียบร้อยแล้ว")
	_, body = e.do(t, "POST", "/devices/1/pull", url.Values{})
	mustContain(t, body, "ได้รายการใหม่ 3 รายการ")

	e.puller.err = core.ErrBusy
	resp, _ := e.do(t, "POST", "/devices/1/pull", url.Values{})
	if resp.StatusCode != 303 {
		t.Errorf("busy %d", resp.StatusCode)
	}
}

func TestDeviceDelete(t *testing.T) {
	e := newEnv(t, admin)
	resp, _ := e.do(t, "POST", "/devices/1/delete", url.Values{})
	if resp.StatusCode != 303 || len(e.store.deleted) != 1 || e.store.deleted[0] != 1 {
		t.Errorf("%d %v", resp.StatusCode, e.store.deleted)
	}
	resp, _ = e.do(t, "POST", "/devices/55/delete", url.Values{})
	if resp.StatusCode != 404 {
		t.Errorf("missing %d", resp.StatusCode)
	}
}

func TestRecordsFiltersAndPaging(t *testing.T) {
	e := newEnv(t, admin)
	base := time.Date(2026, 11, 4, 8, 0, 0, 0, time.Local)
	e.store.att = nil
	for i := 0; i < 250; i++ {
		e.store.att = append(e.store.att, core.Attendance{DeviceName: "A", UserID: "1", Timestamp: base.Add(time.Duration(i) * time.Minute)})
	}
	_, body := e.do(t, "GET", "/records?from=2026-11-01&to=2026-11-05&q=สม&device=1&page=2", nil)
	f := e.store.lastFilt
	if f.Limit != 100 || f.Offset != 100 || f.Search != "สม" || f.DeviceID != 1 || f.From == nil || f.To == nil {
		t.Errorf("filter %+v", f)
	}
	mustContain(t, body, "101–200", "250", "หน้า 2 / 3", "บันทึกเป็น CSV", "วันนี้", "7 วันล่าสุด", "เดือนนี้", "ก่อนหน้า", "ถัดไป")
	mustContain(t, body, "/records.csv?")

	_, body = e.do(t, "GET", "/records?from=not-a-date", nil)
	mustContain(t, body, "วันที่เริ่มต้นไม่ถูกต้อง")
	if e.store.lastFilt.From != nil {
		t.Error("ต้องไม่ใช้วันที่ผิด")
	}
	// page เกินช่วง → ถอยมาหน้าสุดท้าย
	_, body = e.do(t, "GET", "/records?page=99", nil)
	mustContain(t, body, "หน้า 3 / 3")
}

func TestRecordsCSV(t *testing.T) {
	e := newEnv(t, admin)
	e.store.att = append(e.store.att, core.Attendance{DeviceName: "A", UserID: "2", Name: `=HYPERLINK("x")`, Timestamp: time.Date(2026, 11, 4, 9, 0, 0, 0, time.Local), PunchState: 1})
	resp, body := e.do(t, "GET", "/records.csv?from=2026-11-01&q=x", nil)
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	if !strings.HasPrefix(body, "\xEF\xBB\xBF") {
		t.Error("ไม่มี BOM")
	}
	if !strings.HasPrefix(strings.TrimPrefix(body, "\xEF\xBB\xBF"), "เครื่อง,รหัสพนักงาน,ชื่อ,วันที่,เวลา") {
		t.Errorf("หัวคอลัมน์: %s", trunc(body))
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/csv") || !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Errorf("headers %v", resp.Header)
	}
	mustContain(t, body, "2026-11-04,08:23:27", "ลายนิ้วมือ", "เข้างาน", "ออกงาน", "2026-11-04,09:00:00")
	mustNotContain(t, body, ",=HYPERLINK") // กันสูตร
	if e.store.lastFilt.Limit != 0 || e.store.lastFilt.Search != "x" || e.store.lastFilt.From == nil {
		t.Errorf("CSV ต้องใช้ตัวกรองเดียวกันและไม่จำกัดจำนวน: %+v", e.store.lastFilt)
	}
	resp, _ = e.do(t, "GET", "/records.csv?to=zzz", nil)
	if resp.StatusCode != 400 {
		t.Errorf("bad date csv %d", resp.StatusCode)
	}

	resp, body = e.do(t, "GET", "/staff.csv", nil)
	if resp.StatusCode != 200 || !strings.HasPrefix(body, "\xEF\xBB\xBFเครื่อง,UID,รหัสพนักงาน,ชื่อ,สิทธิ์,เลขบัตร") {
		t.Errorf("staff csv %d %s", resp.StatusCode, trunc(body))
	}
	mustContain(t, body, "ผู้ดูแลเครื่อง")
}

func TestAPIKeys(t *testing.T) {
	e := newEnv(t, admin)
	_, body := e.do(t, "GET", "/api-keys", nil)
	mustContain(t, body, "zk_ab12c", "10.0.0.0/24", "http://zk.local:6565/api/attendance", "/api/health", "/api/devices", "/api/users", "-X POST", "ใช้งานได้")
	mustNotContain(t, body, "SECRETKEYVALUE")

	resp, body := e.do(t, "POST", "/api-keys", url.Values{"name": {"HR"}, "allowed_ips": {"10.0.0.1\r\n 10.0.1.0/24 \n\n"}})
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	mustContain(t, body, "zk_SECRETKEYVALUE", "แสดงครั้งเดียว", "data-copy=\"new-key\"", "Bearer zk_SECRETKEYVALUE")
	if len(e.auth.ips) != 2 || e.auth.ips[0] != "10.0.0.1" || e.auth.ips[1] != "10.0.1.0/24" {
		t.Errorf("ips %v", e.auth.ips)
	}
	if !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
		t.Error("ต้อง no-store")
	}
	// GET ต่อมาต้องไม่เห็นกุญแจเต็ม
	_, body = e.do(t, "GET", "/api-keys", nil)
	mustNotContain(t, body, "SECRETKEYVALUE")

	resp, body = e.do(t, "POST", "/api-keys", url.Values{"name": {""}})
	if resp.StatusCode != 422 {
		t.Errorf("empty name %d", resp.StatusCode)
	}
	e.auth.keyErr = core.ErrInvalidInput
	resp, body = e.do(t, "POST", "/api-keys", url.Values{"name": {"HR"}, "allowed_ips": {"999.1.1.1"}})
	if resp.StatusCode != 422 {
		t.Errorf("bad ip %d", resp.StatusCode)
	}
	mustContain(t, body, "รายการ IP ไม่ถูกต้อง", "999.1.1.1")

	resp, _ = e.do(t, "POST", "/api-keys/7/revoke", url.Values{})
	if resp.StatusCode != 303 || len(e.store.revoked) != 1 || e.store.revoked[0] != 7 {
		t.Errorf("revoke %d %v", resp.StatusCode, e.store.revoked)
	}
}

func TestHostHeaderIsSanitizedInCurl(t *testing.T) {
	e := newEnv(t, admin)
	req := httptest.NewRequest("GET", "/api-keys", nil)
	req.Host = "evil.com';rm"
	resp, err := e.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	mustNotContain(t, string(b), "evil.com")
	mustContain(t, string(b), "SERVER:6565")
}

func TestUsers(t *testing.T) {
	// เหลือผู้ดูแลคนเดียว (ตัวเอง): ลบไม่ได้
	e := newEnv(t, admin)
	resp, _ := e.do(t, "POST", "/users/1/delete", url.Values{})
	if resp.StatusCode != 303 || len(e.store.deleted) != 0 {
		t.Fatalf("ลบคนสุดท้ายได้! %d %v", resp.StatusCode, e.store.deleted)
	}
	var fl *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "zkscan_flash" {
			fl = c
		}
	}
	_, body := e.do(t, "GET", "/users", nil, fl)
	mustContain(t, body, "ผู้ดูแลคนสุดท้าย")

	// มีสองคน: ลบตัวเองไม่ได้, ลบอีกคนได้
	e.store.users = append(e.store.users, core.User{ID: 2, Username: "somchai", CreatedAt: time.Now()})
	resp, _ = e.do(t, "POST", "/users/1/delete", url.Values{})
	if len(e.store.deleted) != 0 {
		t.Fatal("ลบตัวเองได้!")
	}
	for _, c := range resp.Cookies() {
		if c.Name == "zkscan_flash" {
			fl = c
		}
	}
	_, body = e.do(t, "GET", "/users", nil, fl)
	mustContain(t, body, "ตัวเองกำลังใช้อยู่")
	resp, _ = e.do(t, "POST", "/users/2/delete", url.Values{})
	if len(e.store.deleted) != 1 || e.store.deleted[0] != -2 {
		t.Errorf("deleted %v", e.store.deleted)
	}

	// เพิ่ม: รหัสชั่วคราวแสดงครั้งเดียว
	resp, body = e.do(t, "POST", "/users", url.Values{"username": {"Newuser"}})
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	mustContain(t, body, "TempPass1234", "newuser")
	_, body = e.do(t, "GET", "/users", nil)
	mustNotContain(t, body, "TempPass1234")
	resp, body = e.do(t, "POST", "/users", url.Values{"username": {"a b"}})
	if resp.StatusCode != 422 {
		t.Errorf("bad username %d", resp.StatusCode)
	}
	mustContain(t, body, "3-32 ตัว")

	// รีเซ็ต
	_, body = e.do(t, "POST", "/users/2/reset", url.Values{})
	mustContain(t, body, "ResetPw5678")
	resp, _ = e.do(t, "POST", "/users/1/reset", url.Values{})
	if resp.StatusCode != 303 {
		t.Errorf("reset self %d", resp.StatusCode)
	}
}

func TestSettingsBounds(t *testing.T) {
	e := newEnv(t, admin)
	for _, bad := range []string{"-1", "1441", "abc", "", "5.5"} {
		resp, body := e.do(t, "POST", "/settings", url.Values{"pull_interval": {bad}})
		if resp.StatusCode != 422 {
			t.Errorf("%q -> %d", bad, resp.StatusCode)
		}
		mustContain(t, body, "0 ถึง 1440")
	}
	if len(e.store.saved) != 0 {
		t.Fatal("บันทึกค่าผิด")
	}
	for _, good := range []string{"0", "5", "1440"} {
		resp, _ := e.do(t, "POST", "/settings", url.Values{"pull_interval": {good}})
		if resp.StatusCode != 303 {
			t.Errorf("%q -> %d", good, resp.StatusCode)
		}
	}
	if len(e.store.saved) != 3 || e.store.saved[0].PullIntervalMin != 0 || e.store.saved[2].PullIntervalMin != 1440 {
		t.Errorf("saved %+v", e.store.saved)
	}
}

func TestMustChangeHidesNav(t *testing.T) {
	e := newEnv(t, core.User{ID: 1, Username: "admin", MustChangePassword: true})
	_, body := e.do(t, "GET", "/change-password", nil)
	mustContain(t, body, "ตั้งรหัสผ่านใหม่ก่อนใช้งานต่อ")
	mustNotContain(t, body, `href="/devices"`)
}

func TestNewRequiresDeps(t *testing.T) {
	if _, err := New(Deps{}); err == nil {
		t.Error("ควร error เมื่อ deps ไม่ครบ")
	}
}

func TestDeviceFormPlaceholderAndCommKeyField(t *testing.T) {
	e := newEnv(t, admin)
	_, body := e.do(t, "GET", "/devices/new", nil)
	mustContain(t, body, `placeholder="เช่น 192.168.1.201"`, `placeholder="0 (ถ้าไม่เคยตั้ง)"`)
	re := regexp.MustCompile(`<input[^>]*name="comm_key"[^>]*>`)
	in := re.FindString(body)
	if in == "" {
		t.Fatal("no comm_key input on create form")
	}
	if !strings.Contains(in, `type="text"`) || !strings.Contains(in, `value=""`) || strings.Contains(in, "required") || strings.Contains(in, "password") {
		t.Fatalf("create comm_key input wrong: %s", in)
	}
	_, body = e.do(t, "GET", "/devices/1/edit", nil)
	in = re.FindString(body)
	if !strings.Contains(in, `type="password"`) || !strings.Contains(in, `value=""`) || strings.Contains(body, "424242") {
		t.Fatalf("edit comm_key input wrong: %s", in)
	}
}

func TestStaticCacheBusting(t *testing.T) {
	e := newEnv(t, admin)
	_, body := e.do(t, "GET", "/login", nil)
	for _, f := range []string{"style.css", "app.js"} {
		b, err := fs.ReadFile(assets, "static/"+f)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		h := hex.EncodeToString(sum[:])[:10]
		mustContain(t, body, "/static/"+f+"?v="+h)
		resp, _ := e.do(t, "GET", "/static/"+f+"?v="+h, nil)
		if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
			t.Errorf("%s matching v: Cache-Control=%q", f, cc)
		}
		for _, q := range []string{"", "?v=deadbeef00"} {
			resp, _ = e.do(t, "GET", "/static/"+f+q, nil)
			if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=3600" {
				t.Errorf("%s%s: Cache-Control=%q", f, q, cc)
			}
		}
	}
}
