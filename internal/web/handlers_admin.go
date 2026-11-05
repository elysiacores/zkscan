package web

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"zkscan/internal/core"
)

// ---------------- กุญแจ API ----------------

type keyRow struct {
	ID       int64
	Name     string
	Prefix   string
	IPs      string
	Created  time.Time
	LastUsed *time.Time
	Revoked  bool
}

type example struct{ Title, ID, Cmd string }

type keysView struct {
	Keys     []keyRow
	Name     string
	IPs      string
	Err      string
	NewKey   string
	NewName  string
	Examples []example
}

var hostHeaderRe = regexp.MustCompile(`^[A-Za-z0-9.\-:\[\]]{1,255}$`)

// baseURL ใช้ host ที่ผู้ใช้เปิดเข้ามา (กรองอักขระแปลก ๆ ก่อนใส่ในคำสั่ง)
func baseURL(c *fiber.Ctx) string {
	host := c.Get(fiber.HeaderHost)
	if !hostHeaderRe.MatchString(host) {
		host = "SERVER:6565"
	}
	return c.Protocol() + "://" + host
}

func curlExamples(base, key string) []example {
	if key == "" {
		key = "<กุญแจ API ของคุณ>"
	}
	now := time.Now()
	from, to := now.AddDate(0, 0, -6).Format(core.DateLayout), now.Format(core.DateLayout)
	h := `-H "Authorization: Bearer ` + key + `"`
	return []example{
		{"ตรวจสถานะระบบ", "ex-health", `curl ` + h + ` '` + base + `/api/health'`},
		{"รายชื่อเครื่องสแกน", "ex-devices", `curl ` + h + ` '` + base + `/api/devices'`},
		{"บันทึกเวลาเข้า-ออก (กรองตามวันที่)", "ex-att", `curl ` + h + ` '` + base + `/api/attendance?from=` + from + `&to=` + to + `'`},
		{"รายชื่อพนักงาน", "ex-users", `curl ` + h + ` '` + base + `/api/users'`},
		{"สั่งดึงข้อมูลจากเครื่องสแกนทันที", "ex-pull", `curl -X POST ` + h + ` '` + base + `/api/pull'`},
	}
}

func (w *Web) keysData(c *fiber.Ctx, v keysView) (keysView, error) {
	keys, err := w.d.Store.ListAPIKeys(w.ctx(c))
	if err != nil {
		return v, err
	}
	for _, k := range keys {
		ips := "ทุก IP"
		if len(k.AllowedIPs) > 0 {
			ips = strings.Join(k.AllowedIPs, ", ")
		}
		v.Keys = append(v.Keys, keyRow{ID: k.ID, Name: k.Name, Prefix: k.Prefix, IPs: ips, Created: k.CreatedAt, LastUsed: k.LastUsedAt, Revoked: k.Revoked})
	}
	v.Examples = curlExamples(baseURL(c), v.NewKey)
	return v, nil
}

func (w *Web) keysGet(c *fiber.Ctx) error {
	v, err := w.keysData(c, keysView{})
	if err != nil {
		return w.fail(c, err)
	}
	return w.render(c, fiber.StatusOK, "apikeys", "กุญแจ API", "keys", v)
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' }) {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (w *Web) keysCreate(c *fiber.Ctx) error {
	name := strings.TrimSpace(c.FormValue("name"))
	rawIPs := c.FormValue("allowed_ips")
	form := keysView{Name: name, IPs: rawIPs}
	reject := func(status int, msg string) error {
		form.Err = msg
		v, err := w.keysData(c, form)
		if err != nil {
			return w.fail(c, err)
		}
		return w.render(c, status, "apikeys", "กุญแจ API", "keys", v)
	}
	if name == "" {
		return reject(fiber.StatusUnprocessableEntity, "กรอกชื่อของกุญแจ เช่น ชื่อระบบที่จะใช้ (HR)")
	}
	if len([]rune(name)) > 100 {
		return reject(fiber.StatusUnprocessableEntity, "ชื่อยาวเกินไป (ไม่เกิน 100 ตัวอักษร)")
	}
	plain, _, err := w.d.Auth.CreateAPIKey(w.ctx(c), name, splitLines(rawIPs))
	if errors.Is(err, core.ErrInvalidInput) {
		return reject(fiber.StatusUnprocessableEntity, "รายการ IP ไม่ถูกต้อง ใส่ IP เช่น 192.168.1.10 หรือช่วง เช่น 192.168.1.0/24 บรรทัดละหนึ่งรายการ")
	}
	if err != nil {
		return w.fail(c, err)
	}
	v, err := w.keysData(c, keysView{NewKey: plain, NewName: name})
	if err != nil {
		return w.fail(c, err)
	}
	w.logf(c, "info", "สร้างกุญแจ API “%s”", name)
	return w.render(c, fiber.StatusOK, "apikeys", "กุญแจ API", "keys", v)
}

func (w *Web) keysRevoke(c *fiber.Ctx) error {
	id, ok := idParam(c)
	if !ok {
		return w.notFound(c)
	}
	if err := w.d.Store.RevokeAPIKey(w.ctx(c), id); err != nil {
		return w.fail(c, err)
	}
	w.logf(c, "warn", "ยกเลิกกุญแจ API (รหัส %d)", id)
	return w.redirectFlash(c, "/api-keys", "ok", "ยกเลิกกุญแจแล้ว ระบบที่ใช้กุญแจนี้จะเรียก API ไม่ได้อีก")
}

// ---------------- ผู้ดูแล ----------------

type userRow struct {
	ID        int64
	Username  string
	Created   time.Time
	LastLogin *time.Time
	Self      bool
	MustPw    bool
}

type usersView struct {
	Rows     []userRow
	NewName  string
	Err      string
	Temp     string
	TempUser string
	TempKind string // created | reset
}

var usernameRe = regexp.MustCompile(`^[a-z0-9._-]{3,32}$`)

func (w *Web) usersData(c *fiber.Ctx, v usersView) (usersView, error) {
	list, err := w.d.Store.ListUsers(w.ctx(c))
	if err != nil {
		return v, err
	}
	cur, _ := core.CurrentUser(c)
	for _, u := range list {
		v.Rows = append(v.Rows, userRow{ID: u.ID, Username: u.Username, Created: u.CreatedAt, LastLogin: u.LastLoginAt, Self: u.ID == cur.ID, MustPw: u.MustChangePassword})
	}
	return v, nil
}

func (w *Web) usersGet(c *fiber.Ctx) error {
	v, err := w.usersData(c, usersView{})
	if err != nil {
		return w.fail(c, err)
	}
	return w.render(c, fiber.StatusOK, "users", "ผู้ดูแลระบบ", "users", v)
}

func (w *Web) usersCreate(c *fiber.Ctx) error {
	name := strings.ToLower(strings.TrimSpace(c.FormValue("username")))
	reject := func(msg string) error {
		v, err := w.usersData(c, usersView{NewName: name, Err: msg})
		if err != nil {
			return w.fail(c, err)
		}
		return w.render(c, fiber.StatusUnprocessableEntity, "users", "ผู้ดูแลระบบ", "users", v)
	}
	if !usernameRe.MatchString(name) {
		return reject("ชื่อผู้ใช้ต้องยาว 3-32 ตัว ใช้ได้เฉพาะ a-z, 0-9, จุด (.), ขีดล่าง (_) และขีดกลาง (-)")
	}
	_, temp, err := w.d.Auth.CreateUser(w.ctx(c), name)
	switch {
	case errors.Is(err, core.ErrConflict):
		return reject("มีชื่อผู้ใช้นี้อยู่แล้ว")
	case errors.Is(err, core.ErrInvalidInput):
		return reject("ชื่อผู้ใช้ไม่ถูกต้อง")
	case err != nil:
		return w.fail(c, err)
	}
	v, err := w.usersData(c, usersView{Temp: temp, TempUser: name, TempKind: "created"})
	if err != nil {
		return w.fail(c, err)
	}
	w.logf(c, "info", "เพิ่มผู้ดูแล “%s”", name)
	return w.render(c, fiber.StatusOK, "users", "ผู้ดูแลระบบ", "users", v)
}

func (w *Web) usersReset(c *fiber.Ctx) error {
	id, ok := idParam(c)
	if !ok {
		return w.notFound(c)
	}
	target, err := w.d.Store.GetUserByID(w.ctx(c), id)
	if err != nil {
		return w.fail(c, err)
	}
	if cur, _ := core.CurrentUser(c); cur.ID == target.ID {
		return w.redirectFlash(c, "/users", "err", "รีเซ็ตรหัสของตัวเองไม่ได้ ให้ใช้เมนู “เปลี่ยนรหัสผ่าน” แทน")
	}
	temp, err := w.d.Auth.ResetPassword(w.ctx(c), target.Username)
	if err != nil {
		return w.fail(c, err)
	}
	v, err := w.usersData(c, usersView{Temp: temp, TempUser: target.Username, TempKind: "reset"})
	if err != nil {
		return w.fail(c, err)
	}
	w.logf(c, "warn", "รีเซ็ตรหัสผ่านของ “%s”", target.Username)
	return w.render(c, fiber.StatusOK, "users", "ผู้ดูแลระบบ", "users", v)
}

func (w *Web) usersDelete(c *fiber.Ctx) error {
	id, ok := idParam(c)
	if !ok {
		return w.notFound(c)
	}
	list, err := w.d.Store.ListUsers(w.ctx(c))
	if err != nil {
		return w.fail(c, err)
	}
	var target *core.User
	for i := range list {
		if list[i].ID == id {
			target = &list[i]
		}
	}
	if target == nil {
		return w.notFound(c)
	}
	if len(list) <= 1 {
		return w.redirectFlash(c, "/users", "err", "ลบไม่ได้ เพราะเป็นผู้ดูแลคนสุดท้าย ต้องมีผู้ดูแลอย่างน้อย 1 คน")
	}
	if cur, _ := core.CurrentUser(c); cur.ID == id {
		return w.redirectFlash(c, "/users", "err", "ลบบัญชีที่ตัวเองกำลังใช้อยู่ไม่ได้")
	}
	if err := w.d.Store.DeleteUser(w.ctx(c), id); err != nil {
		return w.fail(c, err)
	}
	w.logf(c, "warn", "ลบผู้ดูแล “%s”", target.Username)
	return w.redirectFlash(c, "/users", "ok", "ลบผู้ดูแล “"+target.Username+"” แล้ว")
}

// ---------------- บันทึกการทำงาน ----------------

type logRow struct {
	At      time.Time
	Level   string
	Class   string
	Source  string
	Message string
}

type logsView struct {
	Source  string
	Sources []quickLink // Label=ป้าย, URL=ค่า source
	Rows    []logRow
}

var logSources = []quickLink{
	{"ทั้งหมด", ""}, {"ดึงข้อมูล", "pull"}, {"เข้าสู่ระบบ/ความปลอดภัย", "auth"},
	{"API", "api"}, {"หน้าเว็บ", "web"}, {"ระบบ", "system"},
}

func sourceLabel(s string) string {
	for _, l := range logSources {
		if l.URL == s && s != "" {
			return l.Label
		}
	}
	return s
}

func (w *Web) logsGet(c *fiber.Ctx) error {
	src := c.Query("source")
	valid := false
	for _, l := range logSources {
		if l.URL == src {
			valid = true
		}
	}
	if !valid {
		src = ""
	}
	entries, err := w.d.Store.ListLogs(w.ctx(c), 200, src)
	if err != nil {
		return w.fail(c, err)
	}
	v := logsView{Source: src, Sources: logSources}
	for _, e := range entries {
		lvl, cls := "ข้อมูล", "info"
		switch e.Level {
		case "warn":
			lvl, cls = "เตือน", "warn"
		case "error":
			lvl, cls = "ผิดพลาด", "err"
		}
		v.Rows = append(v.Rows, logRow{At: e.At, Level: lvl, Class: cls, Source: sourceLabel(e.Source), Message: e.Message})
	}
	return w.render(c, fiber.StatusOK, "logs", "บันทึกการทำงาน", "logs", v)
}

// ---------------- ตั้งค่า ----------------

type settingsView struct {
	Interval string
	Err      string
}

func (w *Web) settingsGet(c *fiber.Ctx) error {
	s, err := w.d.Store.GetSettings(w.ctx(c))
	if err != nil {
		return w.fail(c, err)
	}
	return w.render(c, fiber.StatusOK, "settings", "ตั้งค่า", "settings", settingsView{Interval: strconv.Itoa(s.PullIntervalMin)})
}

func (w *Web) settingsPost(c *fiber.Ctx) error {
	raw := strings.TrimSpace(c.FormValue("pull_interval"))
	n, ok := intIn(raw, 0, 1440)
	if !ok {
		return w.render(c, fiber.StatusUnprocessableEntity, "settings", "ตั้งค่า", "settings",
			settingsView{Interval: raw, Err: "รอบดึงข้อมูลต้องเป็นตัวเลข 0 ถึง 1440 นาที (0 = ปิดการดึงอัตโนมัติ)"})
	}
	if err := w.d.Store.SaveSettings(w.ctx(c), core.Settings{PullIntervalMin: n}); err != nil {
		return w.fail(c, err)
	}
	msg := "ปิดการดึงข้อมูลอัตโนมัติแล้ว"
	if n > 0 {
		msg = fmt.Sprintf("ตั้งให้ดึงข้อมูลอัตโนมัติทุก %d นาทีแล้ว", n)
	}
	w.logf(c, "info", "แก้ไขการตั้งค่า: %s", msg)
	return w.redirectFlash(c, "/settings", "ok", msg)
}
