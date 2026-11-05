// Package web คือหน้าเว็บผู้ดูแลภาษาไทย (server-rendered ด้วย html/template)
package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"zkscan/internal/core"
)

//go:embed static templates
var assets embed.FS

const (
	sessionCookie = "zkscan_session"
	flashCookie   = "zkscan_flash"
	sessionTTL    = 12 * time.Hour
)

// Deps คือสิ่งที่หน้าเว็บต้องใช้ (ประกอบโดย cmd/zkscan)
type Deps struct {
	Store   core.Store
	Auth    core.AuthService
	Puller  core.Puller
	Client  core.DeviceClient
	Version string
}

// Web เก็บ template และ handler ทั้งหมด
type Web struct {
	d      Deps
	tpl    map[string]*template.Template
	static fs.FS
	hashes map[string]string // static file -> first 10 hex of sha256 (cache-busting)
}

type navItem struct{ Key, Href, Label string }

var navItems = []navItem{
	{"home", "/", "ภาพรวม"},
	{"devices", "/devices", "เครื่องสแกน"},
	{"records", "/records", "บันทึกเวลา"},
	{"staff", "/staff", "พนักงาน"},
	{"keys", "/api-keys", "กุญแจ API"},
	{"users", "/users", "ผู้ดูแล"},
	{"logs", "/logs", "บันทึกการทำงาน"},
	{"settings", "/settings", "ตั้งค่า"},
}

type flash struct{ Kind, Msg string }

// page คือข้อมูลที่ส่งเข้า layout ทุกหน้า
type page struct {
	Title      string
	Active     string
	User       string
	MustChange bool
	Flash      *flash
	Version    string
	Nav        []navItem
	Data       any
}

var pageNames = []string{
	"login", "password", "dashboard", "devices", "device_form", "result",
	"records", "staff", "apikeys", "users", "logs", "settings", "error",
}

// New สร้าง Web และ parse template ที่ฝังไว้
func New(d Deps) (*Web, error) {
	if d.Store == nil || d.Auth == nil || d.Puller == nil || d.Client == nil {
		return nil, errors.New("web: ต้องระบุ Store, Auth, Puller และ Client ครบ")
	}
	w := &Web{d: d, tpl: map[string]*template.Template{}, hashes: map[string]string{}}
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}
	w.static = sub
	for _, f := range []string{"style.css", "app.js"} {
		b, err := fs.ReadFile(sub, f)
		if err != nil {
			return nil, fmt.Errorf("web: static %s: %w", f, err)
		}
		sum := sha256.Sum256(b)
		w.hashes[f] = hex.EncodeToString(sum[:])[:10]
	}
	fm := template.FuncMap{}
	for k, v := range funcMap {
		fm[k] = v
	}
	// asset คืน URL ของไฟล์ static พร้อม ?v=<hash> เพื่อให้เบราว์เซอร์โหลดใหม่เมื่อไฟล์เปลี่ยน
	fm["asset"] = func(name string) string {
		if h, ok := w.hashes[name]; ok {
			return "/static/" + name + "?v=" + h
		}
		return "/static/" + name
	}
	for _, n := range pageNames {
		t, err := template.New("layout").Funcs(fm).ParseFS(assets, "templates/layout.html", "templates/"+n+".html")
		if err != nil {
			return nil, fmt.Errorf("web: template %s: %w", n, err)
		}
		w.tpl[n] = t
	}
	return w, nil
}

// RegisterPublic ลงทะเบียนเส้นทางที่ไม่ต้องล็อกอิน
func (w *Web) RegisterPublic(app *fiber.App) {
	app.Get("/static/*", w.serveStatic)
	app.Get("/login", w.loginGet)
	app.Post("/login", w.loginPost)
}

// RegisterProtected ลงทะเบียนเส้นทางที่ต้องล็อกอิน (ผู้เรียกห่อด้วย auth middleware แล้ว)
func (w *Web) RegisterProtected(r fiber.Router) {
	r.Get("/", w.dashboard)
	r.Post("/pull-all", w.pullAll)
	r.Post("/logout", w.logout)
	r.Get("/change-password", w.passwordGet)
	r.Post("/change-password", w.passwordPost)

	r.Get("/devices", w.devicesList)
	r.Get("/devices/new", w.deviceNew)
	r.Post("/devices", w.deviceCreate)
	r.Get("/devices/:id/edit", w.deviceEdit)
	r.Post("/devices/:id", w.deviceUpdate)
	r.Post("/devices/:id/delete", w.deviceDelete)
	r.Post("/devices/:id/test", w.deviceTest)
	r.Post("/devices/:id/pull", w.devicePull)
	r.Post("/devices/:id/settime", w.deviceSetTime)

	r.Get("/records", w.records)
	r.Get("/records.csv", w.recordsCSV)
	r.Get("/staff", w.staff)
	r.Get("/staff.csv", w.staffCSV)

	r.Get("/api-keys", w.keysGet)
	r.Post("/api-keys", w.keysCreate)
	r.Post("/api-keys/:id/revoke", w.keysRevoke)

	r.Get("/users", w.usersGet)
	r.Post("/users", w.usersCreate)
	r.Post("/users/:id/reset", w.usersReset)
	r.Post("/users/:id/delete", w.usersDelete)

	r.Get("/logs", w.logsGet)
	r.Get("/settings", w.settingsGet)
	r.Post("/settings", w.settingsPost)
}

// ---------------- static ----------------

func (w *Web) serveStatic(c *fiber.Ctx) error {
	name := path.Clean(c.Params("*"))
	if name == "." || !fs.ValidPath(name) {
		return c.SendStatus(fiber.StatusNotFound)
	}
	b, err := fs.ReadFile(w.static, name)
	if err != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}
	switch path.Ext(name) {
	case ".css":
		c.Type("css", "utf-8")
	case ".js":
		c.Type("js", "utf-8")
	case ".ttf":
		c.Set(fiber.HeaderContentType, "font/ttf")
	case ".txt":
		c.Type("txt", "utf-8")
	default:
		c.Set(fiber.HeaderContentType, "application/octet-stream")
	}
	c.Set("X-Content-Type-Options", "nosniff")
	cc := "public, max-age=3600"
	if v := c.Query("v"); v != "" && v == w.hashes[name] {
		cc = "public, max-age=31536000, immutable" // URL มี hash ตรงกับไฟล์ จึงแคชได้ตลอด
	}
	c.Set(fiber.HeaderCacheControl, cc)
	return c.Send(b)
}

// ---------------- cookies ----------------

func setSessionCookie(c *fiber.Ctx, token string, ttl time.Duration) {
	c.Cookie(&fiber.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HTTPOnly: true,
		SameSite: fiber.CookieSameSiteStrictMode,
		Secure:   c.Protocol() == "https",
		Expires:  time.Now().Add(ttl),
		MaxAge:   int(ttl.Seconds()),
	})
}

func clearSessionCookie(c *fiber.Ctx) {
	c.Cookie(&fiber.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HTTPOnly: true,
		SameSite: fiber.CookieSameSiteStrictMode,
		Secure:   c.Protocol() == "https",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}

// ---------------- flash ----------------

// redirectFlash ตั้งข้อความแจ้งผล (อายุสั้น) แล้ว redirect (303)
// kind = "ok" หรือ "err"; ข้อความถูก escape ตอนแสดงโดย html/template
func (w *Web) redirectFlash(c *fiber.Ctx, to, kind, msg string) error {
	c.Cookie(&fiber.Cookie{
		Name:     flashCookie,
		Value:    kind + ":" + url.QueryEscape(msg),
		Path:     "/",
		HTTPOnly: true,
		SameSite: fiber.CookieSameSiteStrictMode,
		Secure:   c.Protocol() == "https",
		MaxAge:   60,
		Expires:  time.Now().Add(time.Minute),
	})
	return c.Redirect(to, fiber.StatusSeeOther)
}

func (w *Web) takeFlash(c *fiber.Ctx) *flash {
	raw := c.Cookies(flashCookie)
	if raw == "" {
		return nil
	}
	c.Cookie(&fiber.Cookie{Name: flashCookie, Value: "", Path: "/", Expires: time.Unix(0, 0), MaxAge: -1, HTTPOnly: true})
	kind, enc, ok := strings.Cut(raw, ":")
	if !ok || (kind != "ok" && kind != "err") {
		return nil
	}
	msg, err := url.QueryUnescape(enc)
	if err != nil || msg == "" {
		return nil
	}
	if r := []rune(msg); len(r) > 400 {
		msg = string(r[:400])
	}
	return &flash{Kind: kind, Msg: msg}
}

// ---------------- render ----------------

func (w *Web) render(c *fiber.Ctx, status int, name, title, active string, data any) error {
	t, ok := w.tpl[name]
	if !ok {
		return fmt.Errorf("web: ไม่มี template %s", name)
	}
	p := page{Title: title, Active: active, Version: w.d.Version, Nav: navItems, Data: data}
	if u, ok := core.CurrentUser(c); ok {
		p.User = u.Username
		p.MustChange = u.MustChangePassword
	}
	p.Flash = w.takeFlash(c)
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", p); err != nil {
		return err
	}
	c.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set("Referrer-Policy", "same-origin")
	c.Set("X-Frame-Options", "DENY")
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Status(status)
	c.Type("html", "utf-8")
	return c.Send(buf.Bytes())
}

type errorView struct {
	Status  int
	Message string
	Hint    string
}

// fail แสดงหน้าข้อผิดพลาดภาษาไทย ไม่เปิดเผยรายละเอียดภายใน
func (w *Web) fail(c *fiber.Ctx, err error) error {
	if errors.Is(err, core.ErrNotFound) {
		return w.notFound(c)
	}
	return w.errorPage(c, fiber.StatusInternalServerError,
		"ระบบขัดข้อง ทำรายการไม่สำเร็จ",
		"ลองใหม่อีกครั้ง ถ้ายังเป็นอยู่ให้แจ้งผู้ดูแลระบบ และดู “บันทึกการทำงาน” ประกอบ")
}

func (w *Web) notFound(c *fiber.Ctx) error {
	return w.errorPage(c, fiber.StatusNotFound, "ไม่พบข้อมูลที่ต้องการ", "รายการนี้อาจถูกลบไปแล้ว ลองกลับไปที่หน้าก่อนหน้า")
}

func (w *Web) errorPage(c *fiber.Ctx, status int, msg, hint string) error {
	return w.render(c, status, "error", "เกิดข้อผิดพลาด", "", errorView{Status: status, Message: msg, Hint: hint})
}

// ---------------- helpers ----------------

func (w *Web) ctx(c *fiber.Ctx) context.Context { return c.UserContext() }

// logf บันทึกการทำงานจากหน้าเว็บ (ไม่ล้มถ้าบันทึกไม่ได้; ห้ามใส่รหัสผ่าน/กุญแจ)
func (w *Web) logf(c *fiber.Ctx, level, format string, args ...any) {
	who := "ผู้ดูแล"
	if u, ok := core.CurrentUser(c); ok {
		who = "ผู้ใช้ " + u.Username
	}
	_ = w.d.Store.AddLog(w.ctx(c), core.LogEntry{
		At: time.Now(), Level: level, Source: "web",
		Message: who + " " + fmt.Sprintf(format, args...),
	})
}

func idParam(c *fiber.Ctx) (int64, bool) {
	var id int64
	s := c.Params("id")
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' || id > 1<<50 {
			return 0, false
		}
		id = id*10 + int64(r-'0')
	}
	return id, id > 0
}

var funcMap = template.FuncMap{
	"ts": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.Format(core.TimeLayout)
	},
	"tsp": func(t *time.Time) string {
		if t == nil || t.IsZero() {
			return "-"
		}
		return t.Format(core.TimeLayout)
	},
	"dash": func(s string) string {
		if strings.TrimSpace(s) == "" {
			return "-"
		}
		return s
	},
	"num":    fmtInt,
	"verify": core.VerifyModeLabel,
	"punch":  core.PunchStateLabel,
}

func fmtInt(n int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprint(n)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
