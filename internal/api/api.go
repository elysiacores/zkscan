// Package api serves the JSON API used by other systems (HR etc.).
package api

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"zkscan/internal/core"
)

const (
	defaultLimit = 5000
	maxLimit     = 50000
)

// Deps are the collaborators the API needs.
type Deps struct {
	Store  core.Store
	Puller core.Puller
}

type handler struct{ d Deps }

// Register mounts the API routes on r (relative paths). The caller mounts r at
// /api and applies the API-key middleware before calling this.
func Register(r fiber.Router, d Deps) {
	h := &handler{d: d}
	r.Get("/health", h.health)
	r.Get("/devices", h.devices)
	r.Get("/attendance", h.attendance)
	r.Get("/users", h.users)
	r.Post("/pull", h.pull)
	r.All("/*", func(c *fiber.Ctx) error { return errJSON(c, fiber.StatusNotFound, "not found") })
}

func errJSON(c *fiber.Ctx, status int, msg string) error {
	return c.Status(status).JSON(fiber.Map{"error": msg})
}

func internalErr(c *fiber.Ctx) error {
	return errJSON(c, fiber.StatusInternalServerError, "internal error")
}

func fmtTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.Format(core.TimeLayout)
	return &s
}

func fmtTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	return fmtTime(*t)
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (h *handler) health(c *fiber.Ctx) error {
	ctx := c.UserContext()
	recs, err := h.d.Store.CountAttendance(ctx)
	if err != nil {
		return internalErr(c)
	}
	users, err := h.d.Store.CountDeviceUsers(ctx)
	if err != nil {
		return internalErr(c)
	}
	last, err := h.d.Store.LastPullAt(ctx)
	if err != nil {
		return internalErr(c)
	}
	return c.JSON(fiber.Map{
		"status":    "ok",
		"records":   recs,
		"users":     users,
		"last_pull": fmtTimePtr(last),
	})
}

type deviceJSON struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	IP         string  `json:"ip"`
	Port       int     `json:"port"`
	Transport  string  `json:"transport"`
	Demo       bool    `json:"demo"`
	Enabled    bool    `json:"enabled"`
	LastPullAt *string `json:"last_pull_at"`
	LastOK     *bool   `json:"last_ok"`
	LastError  *string `json:"last_error"`
}

func (h *handler) devices(c *fiber.Ctx) error {
	ctx := c.UserContext()
	devs, err := h.d.Store.ListDevices(ctx)
	if err != nil {
		return internalErr(c)
	}
	status, err := h.d.Store.ListDeviceStatus(ctx)
	if err != nil {
		return internalErr(c)
	}
	out := make([]deviceJSON, 0, len(devs))
	for _, d := range devs {
		j := deviceJSON{
			ID: d.ID, Name: d.Name, IP: d.IP, Port: d.Port,
			Transport: string(d.Transport), Demo: d.Demo, Enabled: d.Enabled,
		}
		if st, ok := status[d.ID]; ok {
			j.LastPullAt = fmtTimePtr(st.LastPullAt)
			if st.LastPullAt != nil {
				ok := st.LastOK
				j.LastOK = &ok
			}
			j.LastError = nonEmpty(st.LastError)
		}
		out = append(out, j)
	}
	return c.JSON(out)
}

// parseDate parses an optional YYYY-MM-DD query value.
func parseDate(c *fiber.Ctx, key string) (*time.Time, error) {
	v := strings.TrimSpace(c.Query(key))
	if v == "" {
		return nil, nil
	}
	t, err := time.ParseInLocation(core.DateLayout, v, time.Local)
	if err != nil {
		return nil, errors.New("invalid date for '" + key + "', expected YYYY-MM-DD")
	}
	return &t, nil
}

// parseInt parses an optional integer query value; absent returns def.
func parseInt(c *fiber.Ctx, key string, def int) (int, error) {
	v := strings.TrimSpace(c.Query(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, errors.New("invalid value for '" + key + "', expected an integer")
	}
	return n, nil
}

func parseID(c *fiber.Ctx, key string) (int64, error) {
	v := strings.TrimSpace(c.Query(key))
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.New("invalid value for '" + key + "', expected a positive integer")
	}
	return n, nil
}

type recordJSON struct {
	Device     string `json:"device"`
	UserID     string `json:"user_id"`
	Name       string `json:"name"`
	Timestamp  string `json:"timestamp"`
	VerifyMode int    `json:"verify_mode"`
	PunchState int    `json:"punch_state"`
}

func (h *handler) attendance(c *fiber.Ctx) error {
	var f core.AttendanceFilter
	var err error
	if f.From, err = parseDate(c, "from"); err != nil {
		return errJSON(c, fiber.StatusBadRequest, err.Error())
	}
	if f.To, err = parseDate(c, "to"); err != nil {
		return errJSON(c, fiber.StatusBadRequest, err.Error())
	}
	f.UserID = strings.TrimSpace(c.Query("user_id"))
	f.DeviceName = strings.TrimSpace(c.Query("device"))
	if f.DeviceID, err = parseID(c, "device_id"); err != nil {
		return errJSON(c, fiber.StatusBadRequest, err.Error())
	}
	if f.Limit, err = parseInt(c, "limit", defaultLimit); err != nil {
		return errJSON(c, fiber.StatusBadRequest, err.Error())
	}
	if f.Limit < 1 || f.Limit > maxLimit {
		return errJSON(c, fiber.StatusBadRequest, "invalid value for 'limit', expected 1.."+strconv.Itoa(maxLimit))
	}
	if f.Offset, err = parseInt(c, "offset", 0); err != nil {
		return errJSON(c, fiber.StatusBadRequest, err.Error())
	}
	if f.Offset < 0 {
		return errJSON(c, fiber.StatusBadRequest, "invalid value for 'offset', expected >= 0")
	}

	rows, total, err := h.d.Store.ListAttendance(c.UserContext(), f)
	if err != nil {
		return internalErr(c)
	}
	recs := make([]recordJSON, 0, len(rows))
	for _, r := range rows {
		recs = append(recs, recordJSON{
			Device: r.DeviceName, UserID: r.UserID, Name: r.Name,
			Timestamp:  r.Timestamp.Format(core.TimeLayout),
			VerifyMode: r.VerifyMode, PunchState: r.PunchState,
		})
	}
	return c.JSON(fiber.Map{"count": len(recs), "total": total, "records": recs})
}

type userJSON struct {
	Device    string `json:"device"`
	UID       int    `json:"uid"`
	UserID    string `json:"user_id"`
	Name      string `json:"name"`
	Privilege int    `json:"privilege"`
	Card      uint32 `json:"card"`
}

func (h *handler) users(c *fiber.Ctx) error {
	ctx := c.UserContext()
	name := strings.TrimSpace(c.Query("device"))
	id, err := parseID(c, "device_id")
	if err != nil {
		return errJSON(c, fiber.StatusBadRequest, err.Error())
	}
	// A device name resolves to an id; an unknown name matches nothing.
	if name != "" && id == 0 {
		devs, err := h.d.Store.ListDevices(ctx)
		if err != nil {
			return internalErr(c)
		}
		for _, d := range devs {
			if d.Name == name {
				id = d.ID
				break
			}
		}
		if id == 0 {
			return c.JSON(fiber.Map{"count": 0, "users": []userJSON{}})
		}
	}
	rows, err := h.d.Store.ListDeviceUsers(ctx, id, "")
	if err != nil {
		return internalErr(c)
	}
	out := make([]userJSON, 0, len(rows))
	for _, u := range rows {
		if name != "" && u.DeviceName != name {
			continue
		}
		out = append(out, userJSON{
			Device: u.DeviceName, UID: u.UID, UserID: u.UserID,
			Name: u.Name, Privilege: u.Privilege, Card: u.Card,
		})
	}
	return c.JSON(fiber.Map{"count": len(out), "users": out})
}

type pullJSON struct {
	DeviceID   int64   `json:"device_id"`
	Device     string  `json:"device"`
	OK         bool    `json:"ok"`
	NewRecords int     `json:"new_records"`
	TotalUsers int     `json:"total_users"`
	Error      *string `json:"error"`
	Hint       *string `json:"hint"`
	DurationMS int64   `json:"duration_ms"`
	At         *string `json:"at"`
}

func toPullJSON(r core.PullResult) pullJSON {
	return pullJSON{
		DeviceID: r.DeviceID, Device: r.DeviceName, OK: r.OK,
		NewRecords: r.NewRecords, TotalUsers: r.TotalUsers,
		Error: nonEmpty(r.Error), Hint: nonEmpty(r.Hint),
		DurationMS: r.Duration.Milliseconds(), At: fmtTime(r.At),
	}
}

func (h *handler) pull(c *fiber.Ctx) error {
	id, err := parseID(c, "device_id")
	if err != nil {
		return errJSON(c, fiber.StatusBadRequest, err.Error())
	}
	var results []core.PullResult
	if id == 0 {
		results, err = h.d.Puller.PullAll(c.UserContext())
	} else {
		var r core.PullResult
		r, err = h.d.Puller.PullDevice(c.UserContext(), id)
		results = []core.PullResult{r}
	}
	switch {
	case errors.Is(err, core.ErrBusy):
		return errJSON(c, fiber.StatusConflict, "กำลังดึงข้อมูลอยู่ ลองใหม่อีกครั้ง")
	case errors.Is(err, core.ErrNotFound):
		return errJSON(c, fiber.StatusNotFound, "not found")
	case err != nil:
		return internalErr(c)
	}
	out := make([]pullJSON, 0, len(results))
	for _, r := range results {
		out = append(out, toPullJSON(r))
	}
	return c.JSON(fiber.Map{"results": out})
}
