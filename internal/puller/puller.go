// Package puller ดึงข้อมูลจากเครื่องสแกนทุกเครื่องตามรอบเวลาหรือเมื่อสั่ง (ส่วน 7)
package puller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"zkscan/internal/core"
)

const (
	defaultInitialDelay    = 5 * time.Second  // ดึงครั้งแรกหลังเริ่ม
	defaultDisabledRecheck = 30 * time.Second // ปิดการดึงตามรอบ: เช็กการตั้งค่าใหม่ทุกกี่วินาที
	defaultTimeoutSec      = 10
	// fetchTimeoutFactor: การ Fetch หนึ่งครั้งมีการคุยกับเครื่องหลายรอบ (เชื่อมต่อ, ข้อมูลเครื่อง,
	// รายชื่อ, บันทึกเวลา) แต่ TimeoutSec ของเครื่องเป็นเวลารอต่อหนึ่งรอบ จึงให้เวลารวมทั้งหมด
	// TimeoutSec*6 (เครื่อง 10 วินาที = 60 วินาทีต่อเครื่อง)
	fetchTimeoutFactor = 6
	// saveTimeout: เวลาสูงสุดของการเขียนผลลง store หลังดึงเสร็จ (ไม่ผูกกับ ctx ที่อาจถูกยกเลิก
	// เพื่อไม่ให้ข้อมูลที่ดึงมาแล้วหายตอนปิดบริการ)
	saveTimeout = 30 * time.Second
)

// Puller ดึงข้อมูลจากเครื่องสแกนทีละเครื่อง
type Puller struct {
	st  core.Store
	cl  core.DeviceClient
	now func() time.Time

	// ปรับได้ในเทสต์
	initialDelay    time.Duration
	disabledRecheck time.Duration
	intervalUnit    time.Duration // หน่วยของ PullIntervalMin (ปกติ 1 นาที)

	pullMu  sync.Mutex // TryLock: ดึงได้ทีละงาน
	running atomic.Bool

	resMu   sync.RWMutex
	results []core.PullResult
}

var _ core.Puller = (*Puller)(nil)

// New สร้าง Puller; now ใช้ประทับเวลาผลการดึง (nil = time.Now)
func New(st core.Store, cl core.DeviceClient, now func() time.Time) *Puller {
	if now == nil {
		now = time.Now
	}
	return &Puller{
		st: st, cl: cl, now: now,
		initialDelay:    defaultInitialDelay,
		disabledRecheck: defaultDisabledRecheck,
		intervalUnit:    time.Minute,
	}
}

// Run ดึงตามรอบจนกว่า ctx จะถูกยกเลิก: ดึงครั้งแรกหลังเริ่ม 5 วินาที แล้วอ่าน
// Settings.PullIntervalMin ใหม่ทุกครั้งก่อนรอ (0 = ปิด เช็กใหม่ทุก 30 วินาที)
func (p *Puller) Run(ctx context.Context) {
	if !p.wait(ctx, p.initialDelay) {
		return
	}
	last := core.DefaultSettings().PullIntervalMin
	readInterval := func() int {
		s, err := p.st.GetSettings(ctx)
		if err != nil {
			if ctx.Err() == nil {
				p.logf(ctx, "warn", nil, "อ่านการตั้งค่ารอบการดึงไม่สำเร็จ ใช้ค่าเดิม: %v", err)
			}
			return last
		}
		last = s.PullIntervalMin
		return last
	}
	for {
		if ctx.Err() != nil {
			return
		}
		if readInterval() > 0 {
			if _, err := p.PullAll(ctx); err != nil && !errors.Is(err, core.ErrBusy) && ctx.Err() == nil {
				p.logf(ctx, "error", nil, "ดึงข้อมูลตามรอบไม่สำเร็จ: %v", err)
			}
		}
		d := p.disabledRecheck
		if m := readInterval(); m > 0 {
			d = time.Duration(m) * p.intervalUnit
		}
		if !p.wait(ctx, d) {
			return
		}
	}
}

// wait รอ d หรือจน ctx ถูกยกเลิก; คืน false ถ้าถูกยกเลิก
func (p *Puller) wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return ctx.Err() == nil
	}
}

// PullAll ดึงทุกเครื่องที่ Enabled ทีละเครื่อง; ถ้ากำลังดึงอยู่คืน core.ErrBusy
func (p *Puller) PullAll(ctx context.Context) ([]core.PullResult, error) {
	if !p.pullMu.TryLock() {
		return nil, core.ErrBusy
	}
	defer p.pullMu.Unlock()
	p.running.Store(true)
	defer p.running.Store(false)

	devs, err := p.st.ListDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("อ่านรายการเครื่องไม่สำเร็จ: %w", err)
	}
	results := make([]core.PullResult, 0, len(devs))
	for _, d := range devs {
		if !d.Enabled {
			continue
		}
		results = append(results, p.pullOne(ctx, d))
	}
	p.resMu.Lock()
	p.results = append([]core.PullResult(nil), results...)
	p.resMu.Unlock()
	return results, nil
}

// PullDevice ดึงเครื่องเดียว (แม้ Enabled=false); core.ErrNotFound, core.ErrBusy
func (p *Puller) PullDevice(ctx context.Context, id int64) (core.PullResult, error) {
	if !p.pullMu.TryLock() {
		return core.PullResult{}, core.ErrBusy
	}
	defer p.pullMu.Unlock()
	p.running.Store(true)
	defer p.running.Store(false)

	d, err := p.st.GetDevice(ctx, id)
	if err != nil {
		return core.PullResult{}, err
	}
	r := p.pullOne(ctx, d)
	p.resMu.Lock()
	replaced := false
	for i := range p.results {
		if p.results[i].DeviceID == r.DeviceID {
			p.results[i] = r
			replaced = true
			break
		}
	}
	if !replaced {
		p.results = append(p.results, r)
	}
	p.resMu.Unlock()
	return r, nil
}

// Running บอกว่ากำลังดึงอยู่หรือไม่
func (p *Puller) Running() bool { return p.running.Load() }

// LastResults คืนสำเนาผลรอบล่าสุด
func (p *Puller) LastResults() []core.PullResult {
	p.resMu.RLock()
	defer p.resMu.RUnlock()
	return append([]core.PullResult(nil), p.results...)
}

// pullOne ดึงเครื่องเดียว ไม่ panic และไม่คืน error (ความล้มเหลวอยู่ใน PullResult)
func (p *Puller) pullOne(ctx context.Context, d core.Device) core.PullResult {
	start := p.now()
	res := core.PullResult{DeviceID: d.ID, DeviceName: d.Name, At: start}
	id := d.ID

	timeout := d.TimeoutSec
	if timeout <= 0 {
		timeout = defaultTimeoutSec
	}
	fctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*fetchTimeoutFactor*time.Second)
	var snap core.Snapshot
	err := guard(func() (e error) { snap, e = p.cl.Fetch(fctx, d); return })
	cancel()

	// เขียนผลลง store ด้วย ctx ที่ไม่ถูกยกเลิกตาม ctx หลัก
	sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), saveTimeout)
	defer scancel()

	if err == nil {
		var n int
		saveErr := guard(func() (e error) { n, e = p.st.SaveSnapshot(sctx, d.ID, snap, start); return })
		if saveErr == nil {
			res.OK = true
			res.NewRecords = n
			res.TotalUsers = len(snap.Users)
			res.Duration = p.now().Sub(start)
			p.logf(sctx, "info", &id, "ดึงข้อมูล %s สำเร็จ ได้รายการใหม่ %d รายการ", d.Name, n)
			return res
		}
		// ดึงมาได้แต่บันทึกไม่สำเร็จ
		res.Error = "บันทึกข้อมูลที่ดึงมาไม่สำเร็จ: " + saveErr.Error()
		res.Hint = "ตรวจพื้นที่ดิสก์และสิทธิ์ของไฟล์ฐานข้อมูล"
	} else {
		res.Error, res.Hint = describe(err)
	}

	res.Duration = p.now().Sub(start)
	if pfErr := guard(func() error { return p.st.SavePullFailure(sctx, d.ID, res.Error, res.Hint, start) }); pfErr != nil {
		res.Error += " (และบันทึกสถานะไม่สำเร็จ: " + pfErr.Error() + ")"
		p.logf(sctx, "error", &id, "บันทึกสถานะการดึง %s ไม่สำเร็จ: %v", d.Name, pfErr)
	}
	msg := fmt.Sprintf("ดึงข้อมูล %s ไม่สำเร็จ: %s", d.Name, res.Error)
	if res.Hint != "" {
		msg += " (คำแนะนำ: " + res.Hint + ")"
	}
	p.logf(sctx, "error", &id, "%s", msg)
	return res
}

// describe แปลง error เป็นข้อความไทย + คำแนะนำ; *core.DeviceError ใช้ Message/Hint ของมัน
func describe(err error) (msg, hint string) {
	var de *core.DeviceError
	if errors.As(err, &de) {
		return de.Message, de.Hint
	}
	return "ดึงข้อมูลไม่สำเร็จ: " + err.Error(), ""
}

// guard รัน f และแปลง panic เป็น error
func guard(f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("เกิดข้อผิดพลาดภายในโปรแกรม (panic): %v", r)
		}
	}()
	return f()
}

// logf เขียนบันทึกแหล่ง "pull" (ล้มเหลวเงียบ ๆ ถ้า store ใช้ไม่ได้)
func (p *Puller) logf(ctx context.Context, level string, deviceID *int64, format string, args ...any) {
	e := core.LogEntry{At: p.now(), Level: level, Source: "pull", DeviceID: deviceID, Message: fmt.Sprintf(format, args...)}
	_ = guard(func() error { return p.st.AddLog(context.WithoutCancel(ctx), e) })
}
