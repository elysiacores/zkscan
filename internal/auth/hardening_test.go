package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"zkscan/internal/core"
)

func (f *fakeStore) logCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.logs)
}

func (f *fakeStore) queries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.nameQueries...)
}

// ชื่อ Unicode/ไม่ถูกรูปแบบต้องไม่ถึง store, เทียบ dummy, และนับทั้งสอง throttle
func TestLoginInvalidUsernameNeverReachesStore(t *testing.T) {
	s, st, _ := newSvc(t)
	addUser(t, s, st, "admin", "correct-horse", false)
	var compares int32
	s.compareHook = func(string) { atomic.AddInt32(&compares, 1) }

	names := []string{"admİn", "\u212Aim", "ab", strings.Repeat("x", 33), "a b c", "ad/min", "admin\x00"}
	for i, n := range names {
		_, _, err := s.Login(bg, n, "correct-horse", "5.5.5.5")
		if !errors.Is(err, core.ErrBadCredentials) {
			t.Fatalf("%q: %v", n, err)
		}
		if got := atomic.LoadInt32(&compares); int(got) != i+1 {
			t.Fatalf("%q: compare calls=%d want %d", n, got, i+1)
		}
	}
	if q := st.queries(); len(q) != 0 {
		t.Fatalf("store queried with %q", q)
	}
	logs := st.logText()
	if strings.Count(logs, "ชื่อผู้ใช้ที่ไม่มีในระบบ") < len(names) || strings.Contains(logs, "İ") {
		t.Errorf("log: %s", logs)
	}
	// นับบน per-IP throttle (7 ครั้ง) และ per-(username,IP) throttle
	if e := s.ipThrottle.m[ipKey("5.5.5.5")]; e == nil || e.count != len(names) {
		t.Errorf("ip throttle entry: %+v", e)
	}
	if e := s.throttle.m[throttleKey(lowerASCII("admİn"), "5.5.5.5")]; e == nil || e.count != 1 {
		t.Errorf("user throttle entry: %+v", e)
	}
}

func TestLoginInvalidUsernameLocksPerUserKey(t *testing.T) {
	s, st, _ := newSvc(t)
	for i := 0; i < 5; i++ {
		_, _, _ = s.Login(bg, "\u212Aim", "x", "6.6.6.6")
	}
	if _, _, err := s.Login(bg, "\u212Aim", "x", "6.6.6.6"); !errors.Is(err, core.ErrTooManyAttempts) {
		t.Fatalf("not locked: %v", err)
	}
	if !strings.Contains(st.logText(), "ล็อกการเข้าสู่ระบบ") {
		t.Error("lock not logged")
	}
	if len(st.queries()) != 0 {
		t.Error("store queried")
	}
}

// "admİn" ต้องไม่ใช้ได้เพื่อเพิ่มจำนวนครั้งเดาบัญชี admin
func TestUnicodeVariantGivesNoExtraAttemptsOnAdmin(t *testing.T) {
	s, st, _ := newSvc(t)
	addUser(t, s, st, "admin", "correct-horse", false)
	ip := "4.4.4.4"
	for i := 0; i < 5; i++ {
		_, _, _ = s.Login(bg, "admin", "bad-guess", ip)
	}
	// admin ถูกล็อกแล้ว; ชื่อแปลงก็ต้องไม่ผ่านด้วยรหัสที่ถูก
	for _, n := range []string{"admİn", "ADMİN", "admin"} {
		_, _, err := s.Login(bg, n, "correct-horse", ip)
		if err == nil {
			t.Fatalf("%q logged in", n)
		}
	}
	// IP ใหม่: ชื่อแปลงไม่เข้าบัญชีจริงแม้รหัสถูก
	if _, _, err := s.Login(bg, "admİn", "correct-horse", "3.3.3.3"); !errors.Is(err, core.ErrBadCredentials) {
		t.Fatalf("variant reached account: %v", err)
	}
	for _, q := range st.queries() {
		if q != "admin" {
			t.Errorf("unexpected store query %q", q)
		}
	}
}

func TestResetPasswordInvalidUsernameNotQueried(t *testing.T) {
	s, st, _ := newSvc(t)
	addUser(t, s, st, "admin", "old-password", false)
	for _, n := range []string{"admİn", "\u212Aim", "ab", "a b c"} {
		if _, err := s.ResetPassword(bg, n); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("%q: %v", n, err)
		}
	}
	if q := st.queries(); len(q) != 0 {
		t.Errorf("store queried with %q", q)
	}
	if _, err := s.ResetPassword(bg, " Admin "); err != nil {
		t.Errorf("valid name: %v", err)
	}
}

func TestBlockedAttemptWritesNoLogRow(t *testing.T) {
	s, st, _ := newSvc(t)
	addUser(t, s, st, "alice", "correct-horse", false)
	for i := 0; i < 5; i++ {
		_, _, _ = s.Login(bg, "alice", "bad-guess", "9.9.9.9")
	}
	before := st.logCount()
	for i := 0; i < 50; i++ {
		if _, _, err := s.Login(bg, "alice", "correct-horse", "9.9.9.9"); !errors.Is(err, core.ErrTooManyAttempts) {
			t.Fatal(err)
		}
	}
	if after := st.logCount(); after != before {
		t.Errorf("blocked attempts wrote %d rows", after-before)
	}
	if strings.Contains(st.logText(), "ลองผิดบ่อยเกินไป") {
		t.Error("old rejection line still logged")
	}
	if strings.Count(st.logText(), "ล็อกการเข้าสู่ระบบ") != 1 {
		t.Error("lock event should be logged exactly once")
	}
}

func TestIPDeniedLoggedOncePer10MinPerKey(t *testing.T) {
	s, st, clk := newSvc(t)
	p1, _, _ := s.CreateAPIKey(bg, "one", []string{"10.0.0.1"})
	p2, _, _ := s.CreateAPIKey(bg, "two", []string{"10.0.0.1"})
	count := func() int {
		return strings.Count(st.logText(), "ไม่อยู่ในรายการที่อนุญาต")
	}

	for i := 0; i < 20; i++ {
		if _, err := s.CheckAPIKey(bg, p1, "6.6.6.6"); !errors.Is(err, core.ErrForbidden) {
			t.Fatal(err)
		}
	}
	if count() != 1 {
		t.Fatalf("key one logged %d times", count())
	}
	_, _ = s.CheckAPIKey(bg, p2, "6.6.6.6") // กุญแจอื่นยัง log ได้
	if count() != 2 {
		t.Fatalf("key two should log: %d", count())
	}
	clk.Add(9*time.Minute + 59*time.Second)
	_, _ = s.CheckAPIKey(bg, p1, "6.6.6.6")
	if count() != 2 {
		t.Fatalf("logged again before 10 min: %d", count())
	}
	clk.Add(2 * time.Second)
	_, _ = s.CheckAPIKey(bg, p1, "6.6.6.6")
	if count() != 3 {
		t.Fatalf("not logged after 10 min: %d", count())
	}
}

func TestIPDeniedLogMapBounded(t *testing.T) {
	s, _, clk := newSvc(t)
	for i := int64(1); i <= ipDeniedLogMax+50; i++ {
		s.shouldLogIPDenied(i)
	}
	if len(s.ipDeniedLog) > ipDeniedLogMax {
		t.Fatalf("map grew to %d", len(s.ipDeniedLog))
	}
	clk.Add(11 * time.Minute)
	if !s.shouldLogIPDenied(99999) {
		t.Error("expired entries not swept")
	}
}

// bcrypt พร้อมกันได้ไม่เกิน 4
func TestBcryptConcurrencyLimit(t *testing.T) {
	s, _, _ := newSvc(t)
	hash, _ := s.hashPassword(bg, "pw-pw-pw-pw")
	var cur, peak int32
	release := make(chan struct{})
	s.bcryptHook = func() {
		n := atomic.AddInt32(&cur, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		<-release
		atomic.AddInt32(&cur, -1)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_, _ = s.verify(bg, hash, "x")
			} else {
				_, _ = s.hashPassword(bg, "x")
			}
		}(i)
	}
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&cur) < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // ให้ตัวที่เกินมีโอกาสแซง ถ้า semaphore พัง
	if got := atomic.LoadInt32(&cur); got != 4 {
		t.Errorf("in-flight=%d, want exactly 4", got)
	}
	close(release)
	wg.Wait()
	if p := atomic.LoadInt32(&peak); p != 4 {
		t.Errorf("peak=%d, want 4", p)
	}
}

func TestBcryptSemaphoreHonoursContext(t *testing.T) {
	s, st, _ := newSvc(t)
	addUser(t, s, st, "alice", "correct-horse", false)
	// เติมช่องจนเต็ม
	for i := 0; i < maxConcurrentBcrypt; i++ {
		s.sem <- struct{}{}
	}
	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() {
		_, _, err := s.Login(ctx, "alice", "bad-guess", "2.2.2.2")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("returned while sem full: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || errors.Is(err, core.ErrBadCredentials) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Login did not return after cancel")
	}
	// ไม่นับ throttle และไม่เขียน log ล้มเหลว
	if len(s.throttle.m) != 0 || len(s.ipThrottle.m) != 0 {
		t.Error("cancelled attempt counted")
	}
	if strings.Contains(st.logText(), "เข้าสู่ระบบไม่สำเร็จ") {
		t.Error("cancelled attempt logged as failure")
	}
	// hash ก็เคารพ ctx
	if _, err := s.hashPassword(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("hash with cancelled ctx: %v", err)
	}
	// ปล่อยช่อง → ใช้ได้ตามปกติ
	for i := 0; i < maxConcurrentBcrypt; i++ {
		<-s.sem
	}
	if _, _, err := s.Login(bg, "alice", "correct-horse", "2.2.2.2"); err != nil {
		t.Errorf("login after release: %v", err)
	}
}
