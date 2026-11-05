package auth

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"zkscan/internal/core"
)

func do(t *testing.T, app *fiber.App, req *http.Request) (*http.Response, string) {
	t.Helper()
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func req(method, path string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

func cookieHdr(tok string) map[string]string {
	return map[string]string{"Cookie": SessionCookieName + "=" + tok}
}

func loginApp(s *Service) *fiber.App {
	app := fiber.New()
	app.Use(RequireLogin(s))
	h := func(c *fiber.Ctx) error {
		u, ok := core.CurrentUser(c)
		return c.SendString(c.Path() + "|" + u.Username + "|" + core.CurrentToken(c) + "|" + map[bool]string{true: "ok", false: "nouser"}[ok] + "|" + u.PasswordHash)
	}
	app.Get("/", h)
	app.Get("/change-password", h)
	app.Get("/change-password/", h)
	app.Post("/change-password", h)
	app.Get("/logout", h)
	app.Post("/logout", h)
	app.Get("/api/x", h)
	return app
}

func TestRequireLogin(t *testing.T) {
	s, st, clk := newSvc(t)
	addUser(t, s, st, "alice", "correct-horse", false)
	tok, _, _ := s.Login(bg, "alice", "correct-horse", "1.1.1.1")
	app := loginApp(s)

	resp, body := do(t, app, req("GET", "/", cookieHdr(tok)))
	if resp.StatusCode != 200 || body != "/|alice|"+tok+"|ok|" {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	if !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
		t.Error("authenticated pages should be no-store")
	}

	// ไม่มี cookie → redirect
	resp, _ = do(t, app, req("GET", "/", nil))
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/login" {
		t.Errorf("no cookie: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	// token ผิด → redirect + ล้าง cookie
	resp, _ = do(t, app, req("GET", "/", cookieHdr("bogus")))
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/login" {
		t.Errorf("bad token: %d", resp.StatusCode)
	}
	if sc := resp.Header.Get("Set-Cookie"); !strings.Contains(sc, SessionCookieName+"=;") && !strings.Contains(strings.ToLower(sc), "max-age=0") {
		t.Errorf("cookie not cleared: %q", sc)
	}
	// JSON / HTMX → 401
	for _, h := range []map[string]string{
		{"Accept": "application/json"},
		{"Accept": "application/json, text/plain, */*"},
		{"HX-Request": "true"},
	} {
		resp, body := do(t, app, req("GET", "/api/x", h))
		if resp.StatusCode != 401 || !strings.Contains(body, `"error"`) || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			t.Errorf("%v: %d %q", h, resp.StatusCode, body)
		}
	}
	// เบราว์เซอร์ Accept html ก่อน json → redirect
	resp, _ = do(t, app, req("GET", "/", map[string]string{"Accept": "text/html,application/xhtml+xml,application/json;q=0.9"}))
	if resp.StatusCode != 303 {
		t.Errorf("html-first accept: %d", resp.StatusCode)
	}
	// หมดอายุ
	clk.Add(13 * time.Hour)
	resp, _ = do(t, app, req("GET", "/", cookieHdr(tok)))
	if resp.StatusCode != 303 {
		t.Errorf("expired: %d", resp.StatusCode)
	}
}

func TestRequireLoginMustChange(t *testing.T) {
	s, st, _ := newSvc(t)
	addUser(t, s, st, "alice", "temp-passw0rd", true)
	tok, _, _ := s.Login(bg, "alice", "temp-passw0rd", "1.1.1.1")
	app := loginApp(s)

	for _, p := range []string{"/", "/api/x"} {
		resp, _ := do(t, app, req("GET", p, cookieHdr(tok)))
		if resp.StatusCode != 303 || resp.Header.Get("Location") != "/change-password" {
			t.Errorf("%s: %d %s", p, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	resp, body := do(t, app, req("GET", "/api/x", map[string]string{"Cookie": cookieHdr(tok)["Cookie"], "Accept": "application/json"}))
	if resp.StatusCode != 403 || !strings.Contains(body, "error") {
		t.Errorf("json must-change: %d %q", resp.StatusCode, body)
	}
	for _, c := range [][2]string{{"GET", "/change-password"}, {"POST", "/change-password"}, {"GET", "/logout"}, {"POST", "/logout"}, {"GET", "/change-password/"}} {
		resp, _ := do(t, app, req(c[0], c[1], cookieHdr(tok)))
		if resp.StatusCode != 200 {
			t.Errorf("%v should pass through: %d", c, resp.StatusCode)
		}
	}
}

func TestRequireLoginServiceError(t *testing.T) {
	s, st, _ := newSvc(t)
	addUser(t, s, st, "alice", "correct-horse", false)
	tok, _, _ := s.Login(bg, "alice", "correct-horse", "1.1.1.1")
	st.failSession = errBoom
	app := loginApp(s)
	resp, body := do(t, app, req("GET", "/", cookieHdr(tok)))
	if resp.StatusCode != 500 || strings.Contains(body, "boom") {
		t.Errorf("%d %q (must not leak internal error)", resp.StatusCode, body)
	}
}

func apiApp(s *Service) *fiber.App {
	app := fiber.New()
	app.Use(RequireAPIKey(s))
	app.Get("/api/ping", func(c *fiber.Ctx) error {
		k, _ := c.Locals(core.LocalsAPIKey).(core.APIKey)
		return c.SendString("pong:" + k.Name)
	})
	return app
}

func TestRequireAPIKey(t *testing.T) {
	s, st, _ := newSvc(t)
	plain, _, _ := s.CreateAPIKey(bg, "HR", nil)
	app := apiApp(s)

	ok := []map[string]string{
		{"Authorization": "Bearer " + plain},
		{"Authorization": "bearer " + plain},
		{"Authorization": "BEARER   " + plain},
		{"X-API-Key": plain},
		{"X-API-Key": " " + plain + " "},
		{"Authorization": "Basic abc", "X-API-Key": plain},
	}
	for _, h := range ok {
		resp, body := do(t, app, req("GET", "/api/ping", h))
		if resp.StatusCode != 200 || body != "pong:HR" {
			t.Errorf("%v: %d %q", h, resp.StatusCode, body)
		}
	}
	bad := []map[string]string{
		nil,
		{"Authorization": "Bearer "},
		{"Authorization": "Bearer"},
		{"Authorization": plain},
		{"Authorization": "Bearer zk_wrong"},
		{"X-API-Key": "zk_wrong"},
		{"Authorization": "Basic " + plain},
	}
	for _, h := range bad {
		resp, body := do(t, app, req("GET", "/api/ping", h))
		if resp.StatusCode != 401 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") || !strings.Contains(body, `{"error":`) {
			t.Errorf("%v: %d %q", h, resp.StatusCode, body)
		}
		if resp.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("%v: no WWW-Authenticate", h)
		}
	}
	// ยกเลิกแล้ว
	st.mu.Lock()
	for id, k := range st.keys {
		k.Revoked = true
		st.keys[id] = k
	}
	st.mu.Unlock()
	if resp, _ := do(t, app, req("GET", "/api/ping", map[string]string{"X-API-Key": plain})); resp.StatusCode != 401 {
		t.Errorf("revoked: %d", resp.StatusCode)
	}
}

func TestRequireAPIKeyIPAndErrors(t *testing.T) {
	s, st, _ := newSvc(t)
	// httptest.NewRequest ใช้ RemoteAddr 0.0.0.0:0 ใน app.Test → c.IP() = 0.0.0.0
	plain, _, _ := s.CreateAPIKey(bg, "locked", []string{"10.1.2.3"})
	app := apiApp(s)
	resp, body := do(t, app, req("GET", "/api/ping", map[string]string{"X-API-Key": plain}))
	if resp.StatusCode != 403 || !strings.Contains(body, `{"error":`) {
		t.Errorf("ip denied: %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("WWW-Authenticate") != "" {
		t.Error("403 should not challenge")
	}
	// ใช้ IP จริงของ c.IP(): ตั้ง allowlist เป็นค่าที่ fiber เห็น
	var seen string
	probe := fiber.New()
	probe.Get("/", func(c *fiber.Ctx) error { seen = c.IP(); return nil })
	do(t, probe, req("GET", "/", nil))
	okKey, _, _ := s.CreateAPIKey(bg, "ok", []string{seen})
	if resp, _ := do(t, app, req("GET", "/api/ping", map[string]string{"X-API-Key": okKey})); resp.StatusCode != 200 {
		t.Errorf("allowed ip %q: %d", seen, resp.StatusCode)
	}
	// X-Forwarded-For ต้องไม่ถูกเชื่อ (ไม่ได้ตั้ง ProxyHeader)
	resp, _ = do(t, app, req("GET", "/api/ping", map[string]string{"X-API-Key": plain, "X-Forwarded-For": "10.1.2.3"}))
	if resp.StatusCode != 403 {
		t.Errorf("XFF spoof accepted: %d", resp.StatusCode)
	}
	st.failKey = errBoom
	resp, body = do(t, app, req("GET", "/api/ping", map[string]string{"X-API-Key": plain}))
	if resp.StatusCode != 500 || strings.Contains(body, "boom") {
		t.Errorf("store err: %d %q", resp.StatusCode, body)
	}
}

func originApp() *fiber.App {
	app := fiber.New()
	app.Use(SameOrigin())
	h := func(c *fiber.Ctx) error { return c.SendString("ok") }
	app.All("/x", h)
	return app
}

func TestSameOrigin(t *testing.T) {
	app := originApp()
	const host = "zk.local:6565"
	mk := func(method string, hdr map[string]string) int {
		r := req(method, "/x", hdr)
		r.Host = host
		resp, _ := do(t, app, r)
		return resp.StatusCode
	}
	// ปลอดภัย: ไม่ตรวจ
	for _, m := range []string{"GET", "HEAD", "OPTIONS"} {
		if c := mk(m, nil); c != 200 {
			t.Errorf("%s: %d", m, c)
		}
	}
	allowed := []map[string]string{
		{"Origin": "http://zk.local:6565"},
		{"Origin": "https://zk.local:6565"},
		{"Origin": "http://ZK.LOCAL:6565"},
		{"Referer": "http://zk.local:6565/settings?x=1"},
		{"Origin": "http://zk.local:6565", "Referer": "http://evil.com/"}, // Origin เป็นหลัก
	}
	for _, h := range allowed {
		for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
			if c := mk(m, h); c != 200 {
				t.Errorf("%s %v: %d", m, h, c)
			}
		}
	}
	denied := []map[string]string{
		nil,
		{"Origin": "http://evil.com"},
		{"Origin": "http://zk.local"}, // พอร์ตต่าง
		{"Origin": "http://zk.local:6565.evil.com"},
		{"Origin": "http://evil.com/zk.local:6565"},
		{"Origin": "null"},
		{"Origin": ""},
		{"Origin": "zk.local:6565"},
		{"Origin": "ftp://zk.local:6565"},
		{"Origin": "http://zk.local:6565@evil.com"},
		{"Origin": "http://evil.com", "Referer": "http://zk.local:6565/"}, // Origin ผิดห้ามถอยไปใช้ Referer
		{"Referer": "http://evil.com/zk.local:6565"},
		{"Referer": "/relative"},
		{"Referer": "://bad"},
	}
	for _, h := range denied {
		r := req("POST", "/x", h)
		r.Host = host
		resp, body := do(t, app, r)
		if resp.StatusCode != 403 || !strings.Contains(body, `"error"`) {
			t.Errorf("%v: %d %q", h, resp.StatusCode, body)
		}
	}
}

func cookieApp(ttl time.Duration) *fiber.App {
	app := fiber.New()
	app.Get("/set", func(c *fiber.Ctx) error { SetSessionCookie(c, "tok123", ttl); return nil })
	app.Get("/clear", func(c *fiber.Ctx) error { ClearSessionCookie(c); return nil })
	return app
}

func TestSessionCookieAttributes(t *testing.T) {
	app := cookieApp(12 * time.Hour)

	resp, _ := do(t, app, req("GET", "/set", nil))
	sc := resp.Header.Get("Set-Cookie")
	for _, want := range []string{"zkscan_session=tok123", "path=/", "httponly", "samesite=strict", "max-age=43200"} {
		if !strings.Contains(strings.ToLower(sc), want) {
			t.Errorf("missing %q in %q", want, sc)
		}
	}
	if strings.Contains(strings.ToLower(sc), "secure") {
		t.Errorf("Secure on http: %q", sc)
	}

	// https (TLS) → Secure
	httpsApp := fiber.New()
	httpsApp.Get("/set", func(c *fiber.Ctx) error {
		SetSessionCookie(c, "tok123", time.Hour)
		return nil
	})
	resp, _ = do(t, httpsApp, req("GET", "/set", map[string]string{"X-Forwarded-Proto": "https"}))
	sc = strings.ToLower(resp.Header.Get("Set-Cookie"))
	if !strings.Contains(sc, "secure") || !strings.Contains(sc, "httponly") || !strings.Contains(sc, "samesite=strict") {
		t.Errorf("https cookie: %q", sc)
	}

	// clear (https)
	resp, _ = do(t, httpsApp2(), req("GET", "/clear", map[string]string{"X-Forwarded-Proto": "https"}))
	if !strings.Contains(strings.ToLower(resp.Header.Get("Set-Cookie")), "secure") {
		t.Errorf("clear on https should be Secure: %q", resp.Header.Get("Set-Cookie"))
	}
	// clear
	resp, _ = do(t, app, req("GET", "/clear", nil))
	sc = resp.Header.Get("Set-Cookie")
	for _, want := range []string{"zkscan_session=;", "path=/", "httponly", "samesite=strict"} {
		if !strings.Contains(strings.ToLower(sc), want) {
			t.Errorf("clear missing %q in %q", want, sc)
		}
	}
	if !strings.Contains(sc, "1970") {
		t.Errorf("clear must expire: %q", sc)
	}
}

func TestWantsJSON(t *testing.T) {
	app := fiber.New()
	app.Get("/", func(c *fiber.Ctx) error { return c.SendString(map[bool]string{true: "j", false: "h"}[wantsJSON(c)]) })
	cases := map[string]string{
		"":                                  "h",
		"*/*":                               "h",
		"text/html":                         "h",
		"application/json":                  "j",
		"APPLICATION/JSON":                  "j",
		"text/html, application/json":       "h",
		"application/json, text/html":       "j",
		"application/json, text/plain, */*": "j",
	}
	for accept, want := range cases {
		_, body := do(t, app, req("GET", "/", map[string]string{"Accept": accept}))
		if body != want {
			t.Errorf("Accept %q: got %s want %s", accept, body, want)
		}
	}
}

func httpsApp2() *fiber.App {
	app := fiber.New()
	app.Get("/clear", func(c *fiber.Ctx) error { ClearSessionCookie(c); return nil })
	return app
}
