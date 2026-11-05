package web

import (
	"bytes"
	"encoding/csv"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"

	"zkscan/internal/core"
)

const (
	pageSize     = 100
	staffShowMax = 1000
)

type recRow struct {
	Device, UserID, Name, Date, Time, Verify, Punch string
	PunchClass                                      string
}

type quickLink struct{ Label, URL string }

type recordsView struct {
	From, To, Q string
	Device      string
	Devices     []core.Device
	Rows        []recRow
	Total       int
	Page, Pages int
	FirstN      int
	LastN       int
	PrevURL     string
	NextURL     string
	CSVURL      string
	Quick       []quickLink
	Warn        string
}

type recQuery struct {
	from, to, q string
	device      int64
	page        int
	warn        string
	filter      core.AttendanceFilter
}

func parseDay(s string) (*time.Time, bool) {
	t, err := time.ParseInLocation(core.DateLayout, s, time.Local)
	if err != nil {
		return nil, false
	}
	return &t, true
}

func parseRecQuery(c *fiber.Ctx) recQuery {
	q := recQuery{page: 1}
	var warns []string
	if s := strings.TrimSpace(c.Query("from")); s != "" {
		if t, ok := parseDay(s); ok {
			q.from, q.filter.From = s, t
		} else {
			warns = append(warns, "วันที่เริ่มต้นไม่ถูกต้อง (ต้องเป็น ปี-เดือน-วัน เช่น 2026-11-05)")
		}
	}
	if s := strings.TrimSpace(c.Query("to")); s != "" {
		if t, ok := parseDay(s); ok {
			q.to, q.filter.To = s, t
		} else {
			warns = append(warns, "วันที่สิ้นสุดไม่ถูกต้อง (ต้องเป็น ปี-เดือน-วัน เช่น 2026-11-05)")
		}
	}
	if s := strings.TrimSpace(c.Query("q")); s != "" {
		if utf8.RuneCountInString(s) > 100 {
			s = string([]rune(s)[:100])
		}
		q.q, q.filter.Search = s, s
	}
	if s := c.Query("device"); s != "" {
		if id, err := strconv.ParseInt(s, 10, 64); err == nil && id > 0 {
			q.device, q.filter.DeviceID = id, id
		}
	}
	if n, err := strconv.Atoi(c.Query("page")); err == nil && n > 1 {
		q.page = n
	}
	q.warn = strings.Join(warns, " · ")
	return q
}

func (q recQuery) values(withPage int) url.Values {
	v := url.Values{}
	if q.from != "" {
		v.Set("from", q.from)
	}
	if q.to != "" {
		v.Set("to", q.to)
	}
	if q.q != "" {
		v.Set("q", q.q)
	}
	if q.device > 0 {
		v.Set("device", strconv.FormatInt(q.device, 10))
	}
	if withPage > 1 {
		v.Set("page", strconv.Itoa(withPage))
	}
	return v
}

func withQuery(path string, v url.Values) string {
	if len(v) == 0 {
		return path
	}
	return path + "?" + v.Encode()
}

func (w *Web) records(c *fiber.Ctx) error {
	q := parseRecQuery(c)
	ctx := w.ctx(c)
	devs, err := w.d.Store.ListDevices(ctx)
	if err != nil {
		return w.fail(c, err)
	}
	f := q.filter
	f.Limit = pageSize
	var rows []core.Attendance
	var total int
	for attempt := 0; attempt < 2; attempt++ {
		f.Offset = (q.page - 1) * pageSize
		rows, total, err = w.d.Store.ListAttendance(ctx, f)
		if err != nil {
			return w.fail(c, err)
		}
		pages := (total + pageSize - 1) / pageSize
		if q.page > pages && pages > 0 {
			q.page = pages
			continue
		}
		break
	}
	pages := (total + pageSize - 1) / pageSize
	if pages < 1 {
		pages = 1
	}
	v := recordsView{
		From: q.from, To: q.to, Q: q.q, Devices: devs, Total: total, Page: q.page, Pages: pages,
		Warn: q.warn, CSVURL: withQuery("/records.csv", q.values(1)),
	}
	if q.device > 0 {
		v.Device = strconv.FormatInt(q.device, 10)
	}
	for _, a := range rows {
		cls := ""
		switch a.PunchState {
		case 0, 3, 4:
			cls = "in"
		case 1, 2, 5:
			cls = "out"
		}
		v.Rows = append(v.Rows, recRow{
			Device: a.DeviceName, UserID: a.UserID, Name: a.Name,
			Date: a.Timestamp.Format(core.DateLayout), Time: a.Timestamp.Format("15:04:05"),
			Verify: core.VerifyModeLabel(a.VerifyMode), Punch: core.PunchStateLabel(a.PunchState), PunchClass: cls,
		})
	}
	if len(rows) > 0 {
		v.FirstN = (q.page-1)*pageSize + 1
		v.LastN = v.FirstN + len(rows) - 1
	}
	if q.page > 1 {
		v.PrevURL = withQuery("/records", q.values(q.page-1))
	}
	if q.page < pages {
		v.NextURL = withQuery("/records", q.values(q.page+1))
	}
	// ปุ่มลัดช่วงวันที่ (เก็บคำค้นและเครื่องไว้)
	now := time.Now()
	day := func(t time.Time) string { return t.Format(core.DateLayout) }
	quick := func(label, from, to string) quickLink {
		qq := q
		qq.from, qq.to = from, to
		return quickLink{label, withQuery("/records", qq.values(1))}
	}
	v.Quick = []quickLink{
		quick("วันนี้", day(now), day(now)),
		quick("7 วันล่าสุด", day(now.AddDate(0, 0, -6)), day(now)),
		quick("เดือนนี้", day(time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)), day(now)),
	}
	return w.render(c, fiber.StatusOK, "records", "บันทึกเวลา", "records", v)
}

// ---------------- CSV ----------------

// csvSafe กันสูตรใน Excel/Sheets (CSV injection) จากข้อความที่มาจากเครื่องสแกน
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func (w *Web) sendCSV(c *fiber.Ctx, prefix string, header []string, rows [][]string) error {
	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF")
	cw := csv.NewWriter(&buf)
	cw.UseCRLF = true
	_ = cw.Write(header)
	for _, r := range rows {
		_ = cw.Write(r)
	}
	cw.Flush()
	c.Set(fiber.HeaderContentType, "text/csv; charset=utf-8")
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+prefix+"_"+time.Now().Format("20060102_150405")+`.csv"`)
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Set("X-Content-Type-Options", "nosniff")
	return c.Send(buf.Bytes())
}

func (w *Web) recordsCSV(c *fiber.Ctx) error {
	q := parseRecQuery(c)
	if q.warn != "" {
		return w.errorPage(c, fiber.StatusBadRequest, q.warn, "แก้วันที่ในตัวกรองแล้วลองบันทึกเป็น CSV อีกครั้ง")
	}
	f := q.filter
	f.Limit, f.Offset = 0, 0
	rows, _, err := w.d.Store.ListAttendance(w.ctx(c), f)
	if err != nil {
		return w.fail(c, err)
	}
	out := make([][]string, 0, len(rows))
	for _, a := range rows {
		out = append(out, []string{
			csvSafe(a.DeviceName), csvSafe(a.UserID), csvSafe(a.Name),
			a.Timestamp.Format(core.DateLayout), a.Timestamp.Format("15:04:05"),
			strconv.Itoa(a.VerifyMode), core.VerifyModeLabel(a.VerifyMode),
			strconv.Itoa(a.PunchState), core.PunchStateLabel(a.PunchState),
		})
	}
	return w.sendCSV(c, "attendance",
		[]string{"เครื่อง", "รหัสพนักงาน", "ชื่อ", "วันที่", "เวลา", "รหัสวิธียืนยัน", "วิธียืนยัน", "รหัสสถานะ", "สถานะ"}, out)
}

// ---------------- พนักงาน ----------------

type staffRow struct {
	Device, UID, UserID, Name, Role, Card string
}

type staffView struct {
	Q       string
	Device  string
	Devices []core.Device
	Rows    []staffRow
	Total   int
	Capped  bool
	CSVURL  string
}

func staffRole(p int) string {
	if p == 14 {
		return "ผู้ดูแลเครื่อง"
	}
	return "ผู้ใช้"
}

func (w *Web) staffQuery(c *fiber.Ctx) (q string, dev int64) {
	q = strings.TrimSpace(c.Query("q"))
	if utf8.RuneCountInString(q) > 100 {
		q = string([]rune(q)[:100])
	}
	if id, err := strconv.ParseInt(c.Query("device"), 10, 64); err == nil && id > 0 {
		dev = id
	}
	return q, dev
}

func (w *Web) staff(c *fiber.Ctx) error {
	q, dev := w.staffQuery(c)
	ctx := w.ctx(c)
	devs, err := w.d.Store.ListDevices(ctx)
	if err != nil {
		return w.fail(c, err)
	}
	users, err := w.d.Store.ListDeviceUsers(ctx, dev, q)
	if err != nil {
		return w.fail(c, err)
	}
	v := staffView{Q: q, Devices: devs, Total: len(users)}
	vals := url.Values{}
	if q != "" {
		vals.Set("q", q)
	}
	if dev > 0 {
		v.Device = strconv.FormatInt(dev, 10)
		vals.Set("device", v.Device)
	}
	v.CSVURL = withQuery("/staff.csv", vals)
	if len(users) > staffShowMax {
		users, v.Capped = users[:staffShowMax], true
	}
	for _, u := range users {
		v.Rows = append(v.Rows, staffRow{
			Device: u.DeviceName, UID: strconv.Itoa(u.UID), UserID: u.UserID, Name: u.Name,
			Role: staffRole(u.Privilege), Card: strconv.FormatUint(uint64(u.Card), 10),
		})
	}
	return w.render(c, fiber.StatusOK, "staff", "พนักงาน", "staff", v)
}

func (w *Web) staffCSV(c *fiber.Ctx) error {
	q, dev := w.staffQuery(c)
	users, err := w.d.Store.ListDeviceUsers(w.ctx(c), dev, q)
	if err != nil {
		return w.fail(c, err)
	}
	out := make([][]string, 0, len(users))
	for _, u := range users {
		out = append(out, []string{
			csvSafe(u.DeviceName), strconv.Itoa(u.UID), csvSafe(u.UserID), csvSafe(u.Name),
			staffRole(u.Privilege), strconv.FormatUint(uint64(u.Card), 10),
		})
	}
	return w.sendCSV(c, "staff", []string{"เครื่อง", "UID", "รหัสพนักงาน", "ชื่อ", "สิทธิ์", "เลขบัตร"}, out)
}
