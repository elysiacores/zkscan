package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"

	"zkscan/internal/core"
)

// ---------------- ภาพรวม ----------------

type devCard struct {
	Device core.Device
	Status core.DeviceStatus
	Addr   string
	Kind   string // none | ok | err | off
	Text   string
}

type dashView struct {
	Cards    []devCard
	Records  int
	Users    int
	LastPull *time.Time
	Running  bool
}

func (w *Web) dashboard(c *fiber.Ctx) error {
	ctx := w.ctx(c)
	devs, err := w.d.Store.ListDevices(ctx)
	if err != nil {
		return w.fail(c, err)
	}
	sts, err := w.d.Store.ListDeviceStatus(ctx)
	if err != nil {
		return w.fail(c, err)
	}
	recs, err := w.d.Store.CountAttendance(ctx)
	if err != nil {
		return w.fail(c, err)
	}
	users, err := w.d.Store.CountDeviceUsers(ctx)
	if err != nil {
		return w.fail(c, err)
	}
	last, err := w.d.Store.LastPullAt(ctx)
	if err != nil {
		return w.fail(c, err)
	}
	v := dashView{Records: recs, Users: users, LastPull: last, Running: w.d.Puller.Running()}
	for _, d := range devs {
		st := sts[d.ID]
		card := devCard{Device: d, Status: st, Addr: deviceAddr(d)}
		switch {
		case st.LastPullAt == nil:
			card.Kind, card.Text = "none", "ยังไม่ได้ดึงข้อมูล"
		case st.LastOK:
			card.Kind = "ok"
			card.Text = fmt.Sprintf("สำเร็จ ได้รายการใหม่ %d รายการ", st.LastNew)
		default:
			card.Kind, card.Text = "err", "ดึงข้อมูลไม่สำเร็จ"
		}
		if !d.Enabled {
			card.Kind = "off"
			card.Text = "ปิดใช้งาน (ไม่ดึงตามรอบ)"
		}
		v.Cards = append(v.Cards, card)
	}
	return w.render(c, fiber.StatusOK, "dashboard", "ภาพรวม", "home", v)
}

func deviceAddr(d core.Device) string {
	if d.Demo {
		return "โหมดสาธิต (ไม่ต่อเครื่องจริง)"
	}
	return fmt.Sprintf("%s:%d · %s", d.IP, d.Port, strings.ToUpper(string(d.Transport)))
}

// ---------------- ผลการทำงาน ----------------

type kv struct{ K, V string }

type resultView struct {
	OK      bool
	Heading string
	Message string
	Hint    string
	Rows    []kv
	Results []core.PullResult
	Back    string
	BackTxt string
}

func (w *Web) result(c *fiber.Ctx, v resultView) error {
	if v.Back == "" {
		v.Back, v.BackTxt = "/", "กลับหน้าภาพรวม"
	}
	return w.render(c, fiber.StatusOK, "result", v.Heading, "", v)
}

// deviceFailure แปลง error ของเครื่องเป็นข้อความไทย (Message + Hint)
func deviceFailure(err error) (msg, hint string) {
	var de *core.DeviceError
	if errors.As(err, &de) {
		return de.Message, de.Hint
	}
	return "ทำงานกับเครื่องไม่สำเร็จ", "ลองใหม่อีกครั้ง ถ้ายังไม่ได้ให้แจ้งผู้ดูแลระบบ"
}

func optInt(p *int) string {
	if p == nil {
		return "-"
	}
	return fmtInt(*p)
}

func (w *Web) pullAll(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(w.ctx(c), 10*time.Minute)
	defer cancel()
	res, err := w.d.Puller.PullAll(ctx)
	if errors.Is(err, core.ErrBusy) {
		return w.redirectFlash(c, "/", "err", "กำลังดึงข้อมูลอยู่ รอสักครู่แล้วลองใหม่")
	}
	if err != nil {
		return w.fail(c, err)
	}
	if len(res) == 0 {
		return w.redirectFlash(c, "/", "err", "ยังไม่มีเครื่องที่เปิดใช้งาน เพิ่มเครื่องก่อนที่เมนู “เครื่องสแกน”")
	}
	ok, newN := 0, 0
	for _, r := range res {
		if r.OK {
			ok++
			newN += r.NewRecords
		}
	}
	v := resultView{
		OK:      ok == len(res),
		Heading: "ผลการดึงข้อมูลทุกเครื่อง",
		Message: fmt.Sprintf("สำเร็จ %d จาก %d เครื่อง ได้รายการใหม่ %s รายการ", ok, len(res), fmtInt(newN)),
		Results: res,
	}
	w.logf(c, "info", "สั่งดึงข้อมูลทุกเครื่อง (สำเร็จ %d จาก %d เครื่อง)", ok, len(res))
	return w.result(c, v)
}

// ---------------- รายการเครื่อง ----------------

type devicesView struct {
	Cards []devCard
}

func (w *Web) devicesList(c *fiber.Ctx) error {
	ctx := w.ctx(c)
	devs, err := w.d.Store.ListDevices(ctx)
	if err != nil {
		return w.fail(c, err)
	}
	sts, err := w.d.Store.ListDeviceStatus(ctx)
	if err != nil {
		return w.fail(c, err)
	}
	var v devicesView
	for _, d := range devs {
		st := sts[d.ID]
		kind, text := "none", "ยังไม่ได้ดึง"
		if st.LastPullAt != nil {
			if st.LastOK {
				kind, text = "ok", "ปกติ"
			} else {
				kind, text = "err", "ผิดพลาด"
			}
		}
		if !d.Enabled {
			kind, text = "off", "ปิดใช้งาน"
		}
		// ไม่ส่ง CommKey ออกไปในรายการ
		d.CommKey = 0
		v.Cards = append(v.Cards, devCard{Device: d, Status: st, Addr: deviceAddr(d), Kind: kind, Text: text})
	}
	return w.render(c, fiber.StatusOK, "devices", "เครื่องสแกน", "devices", v)
}

// ---------------- ฟอร์มเครื่อง ----------------

type deviceForm struct {
	Name, IP, Port, CommKey, Transport, Timeout string
	Demo, Enabled                               bool
}

type deviceFormView struct {
	IsEdit bool
	ID     int64
	Form   deviceForm
	Errs   map[string]string
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`)

func defaultDeviceForm() deviceForm {
	return deviceForm{Port: "4370", CommKey: "0", Transport: "tcp", Timeout: "10", Enabled: true}
}

func formFromDevice(d core.Device) deviceForm {
	return deviceForm{
		// CommKey is write-only: never copied into the form.
		Name: d.Name, IP: d.IP, Port: strconv.Itoa(d.Port),
		Transport: string(d.Transport), Timeout: strconv.Itoa(d.TimeoutSec), Demo: d.Demo, Enabled: d.Enabled,
	}
}

func readDeviceForm(c *fiber.Ctx) deviceForm {
	return deviceForm{
		Name:      strings.TrimSpace(c.FormValue("name")),
		IP:        strings.TrimSpace(c.FormValue("ip")),
		Port:      strings.TrimSpace(c.FormValue("port")),
		CommKey:   strings.TrimSpace(c.FormValue("comm_key")),
		Transport: c.FormValue("transport"),
		Timeout:   strings.TrimSpace(c.FormValue("timeout")),
		Demo:      c.FormValue("demo") != "",
		Enabled:   c.FormValue("enabled") != "",
	}
}

func intIn(s string, lo, hi int) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < lo || n > hi {
		return 0, false
	}
	return n, true
}

// validate ตรวจค่าในฟอร์ม คืนค่าที่แปลงแล้วและข้อความผิดพลาดรายช่อง (ภาษาไทย)
func (f deviceForm) validate() (core.Device, map[string]string) {
	errs := map[string]string{}
	d := core.Device{Name: f.Name, IP: f.IP, Demo: f.Demo, Enabled: f.Enabled}
	switch {
	case f.Name == "":
		errs["name"] = "กรอกชื่อเครื่อง"
	case utf8.RuneCountInString(f.Name) > 100:
		errs["name"] = "ชื่อเครื่องยาวเกินไป (ไม่เกิน 100 ตัวอักษร)"
	}
	switch {
	case f.IP == "":
		errs["ip"] = "กรอก IP หรือชื่อโฮสต์ของเครื่อง เช่น 192.168.1.201"
	case len(f.IP) > 253 || (net.ParseIP(f.IP) == nil && !hostRe.MatchString(f.IP)):
		errs["ip"] = "IP หรือชื่อโฮสต์ไม่ถูกต้อง ใช้ได้เฉพาะตัวอักษรอังกฤษ ตัวเลข จุด และขีดกลาง"
	}
	if n, ok := intIn(f.Port, 1, 65535); ok {
		d.Port = n
	} else {
		errs["port"] = "พอร์ตต้องเป็นตัวเลข 1 ถึง 65535 (ปกติ 4370)"
	}
	if f.CommKey == "" {
		d.CommKey = 0 // create: blank = 0; update overrides with the stored key
	} else if n, ok := intIn(f.CommKey, 0, 999999); ok {
		d.CommKey = n
	} else {
		errs["comm_key"] = "Comm Key ต้องเป็นตัวเลข 0 ถึง 999999 (ถ้าไม่เคยตั้งให้ใส่ 0)"
	}
	switch f.Transport {
	case "tcp":
		d.Transport = core.TransportTCP
	case "udp":
		d.Transport = core.TransportUDP
	default:
		errs["transport"] = "เลือกวิธีเชื่อมต่อ TCP หรือ UDP"
	}
	if n, ok := intIn(f.Timeout, 1, 120); ok {
		d.TimeoutSec = n
	} else {
		errs["timeout"] = "เวลาหมดเวลาต้องเป็นตัวเลข 1 ถึง 120 วินาที"
	}
	return d, errs
}

func (w *Web) renderDeviceForm(c *fiber.Ctx, status int, v deviceFormView) error {
	title := "เพิ่มเครื่องสแกน"
	if v.IsEdit {
		title = "แก้ไขเครื่องสแกน"
	}
	return w.render(c, status, "device_form", title, "devices", v)
}

func (w *Web) deviceNew(c *fiber.Ctx) error {
	return w.renderDeviceForm(c, fiber.StatusOK, deviceFormView{Form: defaultDeviceForm()})
}

func (w *Web) deviceCreate(c *fiber.Ctx) error {
	f := readDeviceForm(c)
	d, errs := f.validate()
	if len(errs) > 0 {
		f.CommKey = ""
		return w.renderDeviceForm(c, fiber.StatusUnprocessableEntity, deviceFormView{Form: f, Errs: errs})
	}
	if _, err := w.d.Store.CreateDevice(w.ctx(c), d); err != nil {
		if errors.Is(err, core.ErrConflict) {
			errs["name"] = "มีเครื่องชื่อนี้อยู่แล้ว ใช้ชื่ออื่น"
			f.CommKey = ""
			return w.renderDeviceForm(c, fiber.StatusUnprocessableEntity, deviceFormView{Form: f, Errs: errs})
		}
		return w.fail(c, err)
	}
	w.logf(c, "info", "เพิ่มเครื่อง “%s”", d.Name)
	return w.redirectFlash(c, "/devices", "ok", "เพิ่มเครื่อง “"+d.Name+"” เรียบร้อยแล้ว")
}

func (w *Web) deviceEdit(c *fiber.Ctx) error {
	id, ok := idParam(c)
	if !ok {
		return w.notFound(c)
	}
	d, err := w.d.Store.GetDevice(w.ctx(c), id)
	if err != nil {
		return w.fail(c, err)
	}
	return w.renderDeviceForm(c, fiber.StatusOK, deviceFormView{IsEdit: true, ID: id, Form: formFromDevice(d)})
}

func (w *Web) deviceUpdate(c *fiber.Ctx) error {
	id, ok := idParam(c)
	if !ok {
		return w.notFound(c)
	}
	cur, err := w.d.Store.GetDevice(w.ctx(c), id)
	if err != nil {
		return w.fail(c, err)
	}
	f := readDeviceForm(c)
	d, errs := f.validate()
	if len(errs) > 0 {
		f.CommKey = ""
		return w.renderDeviceForm(c, fiber.StatusUnprocessableEntity, deviceFormView{IsEdit: true, ID: id, Form: f, Errs: errs})
	}
	if f.CommKey == "" {
		d.CommKey = cur.CommKey // blank = keep the stored key
	}
	d.ID, d.CreatedAt = cur.ID, cur.CreatedAt
	if err := w.d.Store.UpdateDevice(w.ctx(c), d); err != nil {
		if errors.Is(err, core.ErrConflict) {
			errs["name"] = "มีเครื่องชื่อนี้อยู่แล้ว ใช้ชื่ออื่น"
			f.CommKey = ""
			return w.renderDeviceForm(c, fiber.StatusUnprocessableEntity, deviceFormView{IsEdit: true, ID: id, Form: f, Errs: errs})
		}
		return w.fail(c, err)
	}
	w.logf(c, "info", "แก้ไขเครื่อง “%s”", d.Name)
	return w.redirectFlash(c, "/devices", "ok", "บันทึกการแก้ไขเครื่อง “"+d.Name+"” แล้ว")
}

func (w *Web) deviceDelete(c *fiber.Ctx) error {
	id, ok := idParam(c)
	if !ok {
		return w.notFound(c)
	}
	d, err := w.d.Store.GetDevice(w.ctx(c), id)
	if err != nil {
		return w.fail(c, err)
	}
	if err := w.d.Store.DeleteDevice(w.ctx(c), id); err != nil {
		return w.fail(c, err)
	}
	w.logf(c, "warn", "ลบเครื่อง “%s”", d.Name)
	return w.redirectFlash(c, "/devices", "ok", "ลบเครื่อง “"+d.Name+"” แล้ว (บันทึกเวลาที่ดึงไว้ก่อนหน้ายังอยู่)")
}

func (w *Web) deviceTest(c *fiber.Ctx) error {
	id, ok := idParam(c)
	if !ok {
		return w.notFound(c)
	}
	d, err := w.d.Store.GetDevice(w.ctx(c), id)
	if err != nil {
		return w.fail(c, err)
	}
	ctx, cancel := context.WithTimeout(w.ctx(c), 3*time.Minute)
	defer cancel()
	back := fmt.Sprintf("/devices/%d/edit", id)
	info, err := w.d.Client.TestConnection(ctx, d)
	if err != nil {
		msg, hint := deviceFailure(err)
		return w.result(c, resultView{Heading: "ทดสอบการเชื่อมต่อ “" + d.Name + "”", Message: msg, Hint: hint, Back: back, BackTxt: "กลับไปหน้าแก้ไขเครื่อง"})
	}
	v := resultView{
		OK:      true,
		Heading: "ทดสอบการเชื่อมต่อ “" + d.Name + "”",
		Message: "เชื่อมต่อเครื่องได้สำเร็จ",
		Back:    back, BackTxt: "กลับไปหน้าแก้ไขเครื่อง",
		Rows: []kv{
			{"หมายเลขเครื่อง", dashStr(info.Serial)},
			{"เฟิร์มแวร์", dashStr(info.Firmware)},
			{"รุ่น", dashStr(info.Platform)},
			{"ชื่อในตัวเครื่อง", dashStr(info.DeviceName)},
			{"จำนวนผู้ใช้", optInt(info.UserCount)},
			{"จำนวนบันทึก", optInt(info.RecordCount)},
		},
	}
	if info.DeviceTime != nil {
		v.Rows = append(v.Rows, kv{"เวลาในเครื่อง", info.DeviceTime.Format(core.TimeLayout)})
	}
	return w.result(c, v)
}

func dashStr(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func (w *Web) devicePull(c *fiber.Ctx) error {
	id, ok := idParam(c)
	if !ok {
		return w.notFound(c)
	}
	ctx, cancel := context.WithTimeout(w.ctx(c), 10*time.Minute)
	defer cancel()
	r, err := w.d.Puller.PullDevice(ctx, id)
	switch {
	case errors.Is(err, core.ErrBusy):
		return w.redirectFlash(c, "/", "err", "กำลังดึงข้อมูลอยู่ รอสักครู่แล้วลองใหม่")
	case err != nil:
		return w.fail(c, err)
	}
	v := resultView{Heading: "ผลการดึงข้อมูล “" + r.DeviceName + "”"}
	if r.OK {
		v.OK = true
		v.Message = fmt.Sprintf("ดึงข้อมูลสำเร็จ ได้รายการใหม่ %s รายการ", fmtInt(r.NewRecords))
		v.Rows = []kv{
			{"พนักงานในเครื่อง", fmtInt(r.TotalUsers) + " คน"},
			{"ใช้เวลา", fmt.Sprintf("%.1f วินาที", r.Duration.Seconds())},
		}
	} else {
		v.Message, v.Hint = r.Error, r.Hint
		if v.Message == "" {
			v.Message = "ดึงข้อมูลไม่สำเร็จ"
		}
	}
	w.logf(c, "info", "สั่งดึงข้อมูลเครื่อง “%s”", r.DeviceName)
	return w.result(c, v)
}

func (w *Web) deviceSetTime(c *fiber.Ctx) error {
	id, ok := idParam(c)
	if !ok {
		return w.notFound(c)
	}
	d, err := w.d.Store.GetDevice(w.ctx(c), id)
	if err != nil {
		return w.fail(c, err)
	}
	ctx, cancel := context.WithTimeout(w.ctx(c), 3*time.Minute)
	defer cancel()
	now := time.Now()
	back := fmt.Sprintf("/devices/%d/edit", id)
	v := resultView{Heading: "ตั้งเวลาเครื่อง “" + d.Name + "”", Back: back, BackTxt: "กลับไปหน้าแก้ไขเครื่อง"}
	if err := w.d.Client.SetTime(ctx, d, now); err != nil {
		v.Message, v.Hint = deviceFailure(err)
		return w.result(c, v)
	}
	v.OK = true
	v.Message = "ตั้งเวลาเครื่องเป็น " + now.Format(core.TimeLayout) + " (เวลาของเซิร์ฟเวอร์) เรียบร้อยแล้ว"
	w.logf(c, "info", "ตั้งเวลาเครื่อง “%s”", d.Name)
	return w.result(c, v)
}
