package auth

import (
	"sync"
	"time"
)

const (
	throttleMax    = 5                // ผิดครบจำนวนนี้แล้วล็อก
	throttleWindow = 15 * time.Minute // หน้าต่างนับ และระยะเวลาล็อก
	throttleSweep  = 1024             // เก็บกวาดรายการเก่าเมื่อ map โตเกินนี้
	throttleHard   = 100000           // เพดานแข็งกันหน่วยความจำบวม
	ipThrottleMax  = 21               // ผิดเกิน 20 ครั้งจาก IP เดียว (ทุกชื่อผู้ใช้) → ล็อก IP
)

type attempt struct {
	count       int
	first       time.Time
	lockedUntil time.Time
}

// throttle นับการเข้าสู่ระบบผิดต่อ key (username + IP) ในหน่วยความจำ
type throttle struct {
	mu  sync.Mutex
	m   map[string]*attempt
	max int // จำนวนครั้งที่ผิดแล้วล็อก
}

func newThrottle() *throttle { return newThrottleMax(throttleMax) }

// newThrottleMax สร้าง throttle ที่ล็อกเมื่อผิดครบ max ครั้ง
// ตัวนับต่อ IP (ข้ามทุกชื่อผู้ใช้) ใช้ instance แยกของตัวเอง จึงไม่มี key ชนกับ (username, IP)
func newThrottleMax(max int) *throttle {
	return &throttle{m: map[string]*attempt{}, max: max}
}

// ipKey คืน key ของตัวนับต่อ IP (ตัดความยาวเท่า throttleKey)
func ipKey(ip string) string {
	if len(ip) > 64 {
		ip = ip[:64]
	}
	return ip
}

func throttleKey(username, ip string) string {
	if len(username) > 64 {
		username = username[:64]
	}
	if len(ip) > 64 {
		ip = ip[:64]
	}
	return username + "\x00" + ip
}

// blocked คืน true ถ้า key ยังถูกล็อกอยู่
func (t *throttle) blocked(key string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.m[key]
	if !ok {
		return false
	}
	if now.Before(e.lockedUntil) {
		return true
	}
	if !now.Before(e.first.Add(throttleWindow)) {
		delete(t.m, key)
	}
	return false
}

// fail บันทึกความผิดพลาด 1 ครั้ง; ครบ throttleMax ภายในหน้าต่าง → ล็อก 15 นาที
// คืน true ถ้าครั้งนี้ทำให้ถูกล็อก
func (t *throttle) fail(key string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.m) >= throttleSweep {
		t.sweepLocked(now)
	}
	e, ok := t.m[key]
	if !ok || !now.Before(e.first.Add(throttleWindow)) {
		if !ok && len(t.m) >= throttleHard {
			return false // เต็มเพดาน ไม่เพิ่ม key ใหม่ (key ที่ล็อกอยู่ยังถูกรักษาไว้)
		}
		e = &attempt{first: now}
		t.m[key] = e
	}
	e.count++
	if e.count >= t.max && !now.Before(e.lockedUntil) {
		e.lockedUntil = now.Add(throttleWindow)
		return true
	}
	return false
}

func (t *throttle) reset(key string) {
	t.mu.Lock()
	delete(t.m, key)
	t.mu.Unlock()
}

func (t *throttle) sweepLocked(now time.Time) {
	for k, e := range t.m {
		if !now.Before(e.first.Add(throttleWindow)) && !now.Before(e.lockedUntil) {
			delete(t.m, k)
		}
	}
}
