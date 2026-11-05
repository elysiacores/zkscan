package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"zkscan/internal/auth"
	"zkscan/internal/core"
	"zkscan/internal/puller"
	"zkscan/internal/store"
	"zkscan/internal/zk"
)

const testHost = "example.com"

// browser เป็นตัวจำลองเบราว์เซอร์แบบง่าย: เก็บ cookie และไม่ตาม redirect
type browser struct {
	t       *testing.T
	app     *fiber.App
	cookies map[string]string
}

type reply struct {
	Status int
	Body   string
	Header http.Header
}

func (b *browser) do(method, path string, form url.Values, hdr map[string]string) reply {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	req.Host = testHost
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	for k, v := range b.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	resp, err := b.app.Test(req, -1)
	if err != nil {
		b.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	for _, ck := range resp.Cookies() {
		if ck.Value == "" || ck.MaxAge < 0 {
			delete(b.cookies, ck.Name)
		} else {
			b.cookies[ck.Name] = ck.Value
		}
	}
	data, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, string(data), resp.Header}
}

func (b *browser) get(path string) reply { return b.do("GET", path, nil, nil) }

func (b *browser) post(path string, form url.Values) reply {
	return b.do("POST", path, form, map[string]string{"Origin": "http://" + testHost})
}

func (b *browser) apiGet(path, key string) reply {
	h := map[string]string{}
	if key != "" {
		h["Authorization"] = "Bearer " + key
	}
	return b.do("GET", path, nil, h)
}

func wantStatus(t *testing.T, what string, r reply, status int) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("%s: สถานะ %d ต้องการ %d; body=%.300s", what, r.Status, status, r.Body)
	}
}

func wantRedirect(t *testing.T, what string, r reply, to string) {
	t.Helper()
	wantStatus(t, what, r, fiber.StatusSeeOther)
	if loc := r.Header.Get("Location"); loc != to {
		t.Fatalf("%s: redirect ไป %q ต้องการ %q", what, loc, to)
	}
}

func newTestApp(t *testing.T) (*browser, core.Store, *auth.Service, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "zkscan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := auth.New(st, auth.Options{})
	client := zk.New()
	app, err := buildApp(appDeps{Store: st, Auth: svc, Puller: puller.New(st, client, nil), Client: client, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	pw, created, fileErr, err := bootstrap(context.Background(), svc, dir)
	if err != nil || !created || fileErr != nil {
		t.Fatalf("bootstrap: created=%v fileErr=%v err=%v", created, fileErr, err)
	}
	return &browser{t: t, app: app, cookies: map[string]string{}}, st, svc, pw
}

var keyRe = regexp.MustCompile(`id="new-key">([^<]+)<`)

func TestEndToEnd(t *testing.T) {
	b, st, svc, tempPw := newTestApp(t)
	ctx := context.Background()

	// ยังไม่ล็อกอิน
	wantRedirect(t, "GET / ไม่มี cookie", b.get("/"), "/login")
	wantRedirect(t, "GET /devices ไม่มี cookie", b.get("/devices"), "/login")
	wantStatus(t, "GET /login", b.get("/login"), 200)
	css := b.get("/static/style.css")
	wantStatus(t, "GET /static/style.css", css, 200)
	if !strings.Contains(css.Header.Get("Content-Type"), "css") {
		t.Fatalf("content-type ของ css: %q", css.Header.Get("Content-Type"))
	}
	if s := css.Header.Get("Server"); s != "" {
		t.Fatalf("ไม่ควรมี Server header: %q", s)
	}

	// CSRF: POST /login ไม่มี Origin/Referer ต้อง 403
	noOrigin := b.do("POST", "/login", url.Values{"username": {"admin"}, "password": {tempPw}}, nil)
	wantStatus(t, "POST /login ไม่มี Origin", noOrigin, 403)
	evil := b.do("POST", "/login", url.Values{"username": {"admin"}, "password": {tempPw}}, map[string]string{"Origin": "http://evil.example"})
	wantStatus(t, "POST /login Origin ต่างโฮสต์", evil, 403)

	// รหัสผิด
	wantStatus(t, "login รหัสผิด", b.post("/login", url.Values{"username": {"admin"}, "password": {"wrong-password"}}), 401)

	// ล็อกอินด้วยรหัสชั่วคราว → บังคับเปลี่ยนรหัส
	wantRedirect(t, "login", b.post("/login", url.Values{"username": {"admin"}, "password": {tempPw}}), "/change-password")
	if b.cookies[auth.SessionCookieName] == "" {
		t.Fatal("ไม่ได้ session cookie")
	}
	wantRedirect(t, "GET / ก่อนเปลี่ยนรหัส", b.get("/"), "/change-password")
	wantRedirect(t, "GET /devices ก่อนเปลี่ยนรหัส", b.get("/devices"), "/change-password")
	wantStatus(t, "GET /change-password", b.get("/change-password"), 200)

	// เปลี่ยนรหัส
	newPw := "รหัสใหม่-ปลอดภัย-2026"
	r := b.post("/change-password", url.Values{"old_password": {tempPw}, "new_password": {newPw}, "confirm_password": {newPw}})
	wantRedirect(t, "change-password", r, "/")
	wantStatus(t, "GET / หลังเปลี่ยนรหัส", b.get("/"), 200)
	admin, err := st.GetUserByUsername(ctx, "admin")
	if err != nil || admin.MustChangePassword {
		t.Fatalf("admin หลังเปลี่ยนรหัส: %+v %v", admin, err)
	}

	// เพิ่มเครื่องสาธิต
	r = b.post("/devices", url.Values{
		"name": {"เครื่องสาธิต"}, "ip": {"127.0.0.1"}, "port": {"4370"}, "comm_key": {"0"},
		"transport": {"tcp"}, "timeout": {"10"}, "demo": {"1"}, "enabled": {"1"},
	})
	wantRedirect(t, "POST /devices", r, "/devices")
	devs, err := st.ListDevices(ctx)
	if err != nil || len(devs) != 1 || !devs[0].Demo {
		t.Fatalf("devices: %+v %v", devs, err)
	}
	r = b.post("/devices/"+itoa(devs[0].ID)+"/pull", nil)
	wantStatus(t, "POST /devices/:id/pull", r, 200)

	r = b.get("/records")
	wantStatus(t, "GET /records", r, 200)
	if !strings.Contains(r.Body, "สมชาย") && !strings.Contains(r.Body, "สุดา") {
		t.Fatalf("/records ไม่พบชื่อภาษาไทยจากเครื่องสาธิต: %.500s", r.Body)
	}
	wantStatus(t, "GET /staff", b.get("/staff"), 200)

	// กุญแจ API ผ่านหน้าเว็บ
	r = b.post("/api-keys", url.Values{"name": {"ระบบ HR"}})
	wantStatus(t, "POST /api-keys", r, 200)
	m := keyRe.FindStringSubmatch(r.Body)
	if m == nil {
		t.Fatalf("ไม่พบกุญแจในหน้า: %.500s", r.Body)
	}
	key := strings.TrimSpace(m[1])

	r = b.apiGet("/api/attendance", key)
	wantStatus(t, "GET /api/attendance ด้วยกุญแจ", r, 200)
	var out struct {
		Count   int `json:"count"`
		Records []struct {
			Name string `json:"name"`
		} `json:"records"`
	}
	if err := json.Unmarshal([]byte(r.Body), &out); err != nil || out.Count == 0 || len(out.Records) == 0 {
		t.Fatalf("attendance json: %v %.300s", err, r.Body)
	}
	wantStatus(t, "GET /api/health ด้วยกุญแจ", b.apiGet("/api/health", key), 200)
	// /api ไม่ถูกบังคับล็อกอินผ่าน cookie และไม่ redirect
	nb := &browser{t: t, app: b.app, cookies: map[string]string{}}
	wantStatus(t, "GET /api/attendance ไม่มีกุญแจ", nb.apiGet("/api/attendance", ""), 401)
	wantStatus(t, "GET /api/attendance กุญแจมั่ว", nb.apiGet("/api/attendance", "zk_notakey"), 401)
	r = nb.apiGet("/api/nope", key)
	wantStatus(t, "GET /api/nope ด้วยกุญแจ", r, 404)
	if !strings.Contains(r.Header.Get("Content-Type"), "json") {
		t.Fatalf("404 ใต้ /api ต้องเป็น JSON: %q", r.Header.Get("Content-Type"))
	}
	// POST ใต้ /api ไม่ต้องมี Origin (ใช้กุญแจ)
	if r := nb.do("POST", "/api/pull", nil, map[string]string{"Authorization": "Bearer " + key}); r.Status == 403 || r.Status == 401 {
		t.Fatalf("POST /api/pull ด้วยกุญแจ: %d %.200s", r.Status, r.Body)
	}

	// 404 ภาษาไทยเมื่อล็อกอินแล้ว
	r = b.get("/ไม่มีหน้านี้")
	wantStatus(t, "GET path ไม่มี", r, 404)
	if !strings.Contains(r.Body, "ไม่พบหน้า") || !strings.Contains(r.Header.Get("Content-Type"), "html") {
		t.Fatalf("404 ต้องเป็น HTML ไทย: %.300s", r.Body)
	}

	// ยกเลิกกุญแจ → 401
	keys, err := st.ListAPIKeys(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys: %+v %v", keys, err)
	}
	wantRedirect(t, "revoke", b.post("/api-keys/"+itoa(keys[0].ID)+"/revoke", nil), "/api-keys")
	wantStatus(t, "กุญแจที่ยกเลิกแล้ว", nb.apiGet("/api/attendance", key), 401)

	// สร้างกุญแจตรงจาก service ก็ใช้ได้
	plain, _, err := svc.CreateAPIKey(ctx, "ตรง", nil)
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, "กุญแจจาก service", nb.apiGet("/api/health", plain), 200)

	// ออกจากระบบ
	wantRedirect(t, "logout", b.post("/logout", nil), "/login")
	wantRedirect(t, "GET / หลัง logout", b.get("/"), "/login")
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestUnknownPathAnonymous(t *testing.T) {
	b, _, _, _ := newTestApp(t)
	// ไม่ล็อกอิน: path ที่ไม่มีจริงถูกส่งไป /login (ไม่เผยโครงสร้างระบบ)
	wantRedirect(t, "unknown anon", b.get("/nope"), "/login")
	// /static ที่ไม่มีไฟล์ → 404 ไม่ใช่ redirect
	wantStatus(t, "static missing", b.get("/static/nothing.css"), 404)
}

func TestPasswordFileJanitor(t *testing.T) {
	b, st, _, tempPw := newTestApp(t)
	_ = b
	dir := t.TempDir()
	if err := writePasswordFile(dir, tempPw); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	removePasswordFileIfChanged(ctx, st, dir)
	if _, err := os.Stat(filepath.Join(dir, passwordFile)); err != nil {
		t.Fatal("ไฟล์ต้องอยู่ตราบที่ admin ยังไม่เปลี่ยนรหัส")
	}
	admin, _ := st.GetUserByUsername(ctx, "admin")
	if err := st.UpdatePassword(ctx, admin.ID, admin.PasswordHash, false); err != nil {
		t.Fatal(err)
	}
	removePasswordFileIfChanged(ctx, st, dir)
	if _, err := os.Stat(filepath.Join(dir, passwordFile)); !os.IsNotExist(err) {
		t.Fatalf("ไฟล์ต้องถูกลบ: %v", err)
	}
}

func runCLI(args ...string) (int, string, string) {
	var o, e bytes.Buffer
	code := run(args, &o, &e)
	return code, o.String(), e.String()
}

func TestCLIInit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	code, out, errOut := runCLI("init", "--data", dir)
	if code != 0 {
		t.Fatalf("init ครั้งแรก code=%d out=%q err=%q", code, out, errOut)
	}
	m := regexp.MustCompile(`(?m)^รหัสผ่านชั่วคราวของ admin: (\S+)$`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("ไม่พบบรรทัดรหัสผ่าน: %q", out)
	}
	p := filepath.Join(dir, passwordFile)
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("สิทธิ์ไฟล์ %v ต้องเป็น 0600", fi.Mode().Perm())
	}
	if di, _ := os.Stat(dir); di.Mode().Perm() != 0o750 {
		t.Fatalf("สิทธิ์โฟลเดอร์ %v ต้องเป็น 0750", di.Mode().Perm())
	}
	b, _ := os.ReadFile(p)
	if string(b) != m[1]+"\n" {
		t.Fatalf("เนื้อไฟล์ %q", b)
	}

	code, out, _ = runCLI("init", "--data", dir)
	if code != 0 || !strings.Contains(out, "มีบัญชีผู้ดูแลอยู่แล้ว") || strings.Contains(out, "รหัสผ่านชั่วคราว") {
		t.Fatalf("init ครั้งสอง code=%d out=%q", code, out)
	}

	code, out, _ = runCLI("show-initial-password", "--data", dir)
	if code != 0 || strings.TrimSpace(out) != "รหัสผ่านชั่วคราวของ admin: "+m[1] {
		t.Fatalf("show-initial-password code=%d out=%q", code, out)
	}

	// flag อยู่ท้ายสุด และ reset-password
	code, out, _ = runCLI("reset-password", "admin", "--data", dir)
	if code != 0 || !strings.HasPrefix(out, "รหัสผ่านชั่วคราวของ admin: ") {
		t.Fatalf("reset-password code=%d out=%q", code, out)
	}
	code, _, errOut = runCLI("reset-password", "nobody", "--data", dir)
	if code != 1 || !strings.Contains(errOut, "ไม่พบผู้ใช้") {
		t.Fatalf("reset ผู้ใช้ไม่มี code=%d err=%q", code, errOut)
	}

	os.Remove(p)
	code, _, errOut = runCLI("show-initial-password", "--data", dir)
	if code != 1 || !strings.Contains(errOut, "reset-password admin") {
		t.Fatalf("ไม่มีไฟล์ code=%d err=%q", code, errOut)
	}
}

func TestCLIMisc(t *testing.T) {
	if code, out, _ := runCLI("version"); code != 0 || out != "zkscan "+version+"\n" {
		t.Fatalf("version: %d %q", code, out)
	}
	if code, _, errOut := runCLI(); code != 2 || !strings.Contains(errOut, "วิธีใช้") {
		t.Fatalf("no args: %d %q", code, errOut)
	}
	if code, out, _ := runCLI("help"); code != 0 || !strings.Contains(out, "serve") {
		t.Fatalf("help: %d", code)
	}
	if code, _, _ := runCLI("bogus"); code != 2 {
		t.Fatalf("bogus: %d", code)
	}
	if code, _, _ := runCLI("reset-password"); code != 2 {
		t.Fatalf("reset-password ไม่มีชื่อ: %d", code)
	}
	if code, _, _ := runCLI("init", "--nope"); code != 2 {
		t.Fatalf("flag ผิด: %d", code)
	}
}

func TestOptionPrecedence(t *testing.T) {
	t.Setenv("ZKSCAN_DATA", "/env/data")
	t.Setenv("ZKSCAN_LISTEN", "127.0.0.1:1")
	o, _, err := parseOpts("serve", nil, io.Discard)
	if err != nil || o.Data != "/env/data" || o.Listen != "127.0.0.1:1" {
		t.Fatalf("env: %+v %v", o, err)
	}
	o, _, err = parseOpts("serve", []string{"--data", "/flag", "--listen=:9"}, io.Discard)
	if err != nil || o.Data != "/flag" || o.Listen != ":9" {
		t.Fatalf("flag: %+v %v", o, err)
	}
	t.Setenv("ZKSCAN_DATA", "")
	o, _, _ = parseOpts("serve", nil, io.Discard)
	if o.Data != defaultData {
		t.Fatalf("default: %+v", o)
	}
}
