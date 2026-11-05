package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"zkscan/internal/core"
)

var bg = context.Background()

func newSvc(t *testing.T) (*Service, *fakeStore, *fakeClock) {
	t.Helper()
	st, clk := newFakeStore(), newClock()
	s := New(st, Options{Now: clk.Now, bcryptCost: bcrypt.MinCost})
	return s, st, clk
}

// addUser สร้างผู้ใช้ผ่าน store ตรง ๆ ด้วยรหัสที่กำหนด
func addUser(t *testing.T, s *Service, st *fakeStore, name, pw string, must bool) core.User {
	t.Helper()
	h, err := s.hashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(bg, core.User{Username: name, PasswordHash: h, MustChangePassword: must})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestDefaults(t *testing.T) {
	s := New(newFakeStore(), Options{})
	if s.ttl != 12*time.Hour || s.cost != 12 || s.now == nil || s.SessionTTL() != 12*time.Hour {
		t.Fatalf("defaults wrong: ttl=%v cost=%d", s.ttl, s.cost)
	}
	if time.Since(s.now()) > time.Minute {
		t.Error("default Now is not time.Now")
	}
	s = New(newFakeStore(), Options{SessionTTL: time.Hour})
	if s.ttl != time.Hour {
		t.Error("custom TTL ignored")
	}
}

func TestNormalizeUsername(t *testing.T) {
	good := map[string]string{"abc": "abc", "Admin": "admin", "a.b_c-d9": "a.b_c-d9", strings.Repeat("x", 32): strings.Repeat("x", 32)}
	for in, want := range good {
		got, err := normalizeUsername(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q,%v want %q", in, got, err, want)
		}
	}
	bad := []string{"", "ab", strings.Repeat("x", 33), "a b c", "ab@c", "สมชาย", "a\x00bc", "a/b", "Kelvin", "abc\n"}
	for _, in := range bad {
		if _, err := normalizeUsername(in); !errors.Is(err, core.ErrInvalidInput) {
			t.Errorf("%q should be invalid, got %v", in, err)
		}
	}
}

func TestCheckPolicy(t *testing.T) {
	if err := checkPolicy("alice", "Str0ngPass"); err != nil {
		t.Error(err)
	}
	for _, pw := range []string{"short7!", "", "ALICE", "alice", "Alice", strings.Repeat("a", 73)} {
		if err := checkPolicy("alice", pw); !errors.Is(err, core.ErrWeakPassword) {
			t.Errorf("%q should be weak, got %v", pw, err)
		}
	}
	if err := checkPolicy("alice", "12345678"); err != nil { // นโยบายมีแค่นี้ตามสเปก
		t.Error(err)
	}
	if err := checkPolicy("alice", strings.Repeat("a", 72)); err != nil {
		t.Error(err)
	}
	// 8 อักขระไทย (หลายไบต์) ผ่านความยาว
	if err := checkPolicy("alice", "รหัสผ่านใหม่"); err != nil {
		t.Error(err)
	}
}

func TestTempPassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		p, err := newTempPassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(p) != 12 {
			t.Fatalf("len=%d", len(p))
		}
		if strings.ContainsAny(p, "0O1lI") {
			t.Fatalf("ambiguous char in %q", p)
		}
		for _, c := range p {
			if !strings.ContainsRune(tempAlphabet, c) {
				t.Fatalf("bad char %q", c)
			}
		}
		seen[p] = true
	}
	if len(seen) < 499 {
		t.Errorf("temp passwords repeat: %d unique", len(seen))
	}
	if len(tempAlphabet) != 57 {
		t.Errorf("alphabet size %d", len(tempAlphabet))
	}
}

func TestBootstrap(t *testing.T) {
	s, st, _ := newSvc(t)
	temp, created, err := s.Bootstrap(bg)
	if err != nil || !created || len(temp) != 12 {
		t.Fatalf("bootstrap: %q %v %v", temp, created, err)
	}
	u, err := st.GetUserByUsername(bg, "admin")
	if err != nil || !u.MustChangePassword {
		t.Fatalf("admin missing or no must-change: %+v %v", u, err)
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(temp)) != nil {
		t.Error("temp does not match hash")
	}
	if strings.Contains(st.logText(), temp) {
		t.Error("temp password leaked to log")
	}
	// ครั้งที่สอง ไม่สร้าง
	temp2, created, err := s.Bootstrap(bg)
	if err != nil || created || temp2 != "" {
		t.Fatalf("second bootstrap: %q %v %v", temp2, created, err)
	}
	if n, _ := st.CountUsers(bg); n != 1 {
		t.Errorf("users=%d", n)
	}
}

func TestBootstrapNotEmptyNoAdmin(t *testing.T) {
	s, st, _ := newSvc(t)
	addUser(t, s, st, "bob", "password1", false)
	_, created, err := s.Bootstrap(bg)
	if err != nil || created {
		t.Fatalf("should not bootstrap: %v %v", created, err)
	}
	if _, err := st.GetUserByUsername(bg, "admin"); err == nil {
		t.Error("admin created")
	}
}

type conflictStore struct{ *fakeStore }

func (c conflictStore) CountUsers(context.Context) (int, error) { return 0, nil }
func (c conflictStore) CreateUser(context.Context, core.User) (core.User, error) {
	return core.User{}, core.ErrConflict
}

type countErrStore struct{ *fakeStore }

func (c countErrStore) CountUsers(context.Context) (int, error) { return 0, errBoom }

func TestBootstrapRaceAndErrors(t *testing.T) {
	st := newFakeStore()
	s := New(conflictStore{st}, Options{bcryptCost: 4})
	temp, created, err := s.Bootstrap(bg)
	if err != nil || created || temp != "" {
		t.Fatalf("conflict should be swallowed: %q %v %v", temp, created, err)
	}
	s = New(countErrStore{st}, Options{bcryptCost: 4})
	if _, _, err := s.Bootstrap(bg); !errors.Is(err, errBoom) {
		t.Errorf("want errBoom got %v", err)
	}
}

func TestCreateUser(t *testing.T) {
	s, st, _ := newSvc(t)
	u, temp, err := s.CreateUser(bg, "  Bob.Smith ")
	if !errors.Is(err, core.ErrInvalidInput) { // ช่องว่างไม่ถูก trim ในการสร้าง
		t.Fatalf("spaces should be invalid, got %v %v", u, err)
	}
	u, temp, err = s.CreateUser(bg, "Bob.Smith")
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "bob.smith" || !u.MustChangePassword || u.PasswordHash != "" || len(temp) != 12 {
		t.Errorf("unexpected: %+v %q", u, temp)
	}
	stored, _ := st.GetUserByUsername(bg, "bob.smith")
	if bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte(temp)) != nil {
		t.Error("stored hash mismatch")
	}
	if c, _ := bcrypt.Cost([]byte(stored.PasswordHash)); c != bcrypt.MinCost {
		t.Errorf("cost %d", c)
	}
	if _, _, err := s.CreateUser(bg, "BOB.smith"); !errors.Is(err, core.ErrConflict) {
		t.Errorf("duplicate: %v", err)
	}
	if _, _, err := s.CreateUser(bg, "x"); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("short: %v", err)
	}
	if strings.Contains(st.logText(), temp) {
		t.Error("temp leaked in log")
	}
}

func TestProductionCostIs12(t *testing.T) {
	s := New(newFakeStore(), Options{})
	h, err := s.hashPassword("whatever12")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := bcrypt.Cost([]byte(h)); c != 12 {
		t.Errorf("cost=%d want 12", c)
	}
}

func TestLoginSuccessAndSession(t *testing.T) {
	s, st, clk := newSvc(t)
	u := addUser(t, s, st, "alice", "correct-horse", false)
	token, got, err := s.Login(bg, " Alice ", "correct-horse", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != u.ID || got.PasswordHash != "" || got.LastLoginAt == nil {
		t.Errorf("user: %+v", got)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		t.Fatalf("token not 32 bytes base64url: %q %v", token, err)
	}
	sum := sha256.Sum256([]byte(token))
	want := hex.EncodeToString(sum[:])
	sess, err := st.GetSession(bg, want)
	if err != nil {
		t.Fatal("session not stored under sha256 hex of token")
	}
	if sess.UserID != u.ID || sess.IP != "10.0.0.1" || !sess.ExpiresAt.Equal(clk.Now().Add(12*time.Hour)) {
		t.Errorf("session: %+v", sess)
	}
	for h := range st.sessions {
		if h == token {
			t.Error("raw token stored")
		}
	}
	if st.users[u.ID].LastLoginAt == nil {
		t.Error("TouchLogin not called")
	}
	if st.deleteExpiry == 0 {
		t.Error("expired sessions not purged")
	}
	logs := st.logText()
	if !strings.Contains(logs, "|auth|เข้าสู่ระบบสำเร็จ") || strings.Contains(logs, token) || strings.Contains(logs, "correct-horse") {
		t.Errorf("log: %s", logs)
	}
	// tokens are unique
	t2, _, _ := s.Login(bg, "alice", "correct-horse", "10.0.0.1")
	if t2 == token {
		t.Error("token reused")
	}
}

func TestLoginFailures(t *testing.T) {
	s, st, _ := newSvc(t)
	addUser(t, s, st, "alice", "correct-horse", false)
	if _, _, err := s.Login(bg, "alice", "wrong-pass", "1.1.1.1"); !errors.Is(err, core.ErrBadCredentials) {
		t.Errorf("wrong pw: %v", err)
	}
	if _, _, err := s.Login(bg, "nobody", "correct-horse", "1.1.1.1"); !errors.Is(err, core.ErrBadCredentials) {
		t.Errorf("unknown: %v", err)
	}
	if _, _, err := s.Login(bg, "alice", "", "1.1.1.1"); !errors.Is(err, core.ErrBadCredentials) {
		t.Errorf("empty pw: %v", err)
	}
	if _, _, err := s.Login(bg, "", "", "1.1.1.1"); !errors.Is(err, core.ErrBadCredentials) {
		t.Errorf("empty all: %v", err)
	}
	if len(st.sessions) != 0 {
		t.Error("session created on failure")
	}
	logs := st.logText()
	if !strings.Contains(logs, "เข้าสู่ระบบไม่สำเร็จ") || strings.Contains(logs, "wrong-pass") || strings.Contains(logs, "nobody") {
		t.Errorf("log: %s", logs)
	}
}

func TestLoginStoreError(t *testing.T) {
	s, st, _ := newSvc(t)
	st.failGetUser = errBoom
	_, _, err := s.Login(bg, "alice", "whatever12", "1.1.1.1")
	if !errors.Is(err, errBoom) || errors.Is(err, core.ErrBadCredentials) {
		t.Errorf("got %v", err)
	}
}

// เส้นทางผู้ใช้ไม่มีอยู่ต้องเทียบ bcrypt กับ dummy hash ที่ cost เท่ากัน
func TestUnknownUserComparesDummyHash(t *testing.T) {
	s, st, _ := newSvc(t)
	u := addUser(t, s, st, "alice", "correct-horse", false)
	var mu sync.Mutex
	var hashes []string
	s.compareHook = func(h string) { mu.Lock(); hashes = append(hashes, h); mu.Unlock() }

	_, _, _ = s.Login(bg, "alice", "wrong-pass", "1.1.1.1")
	_, _, err := s.Login(bg, "ghost", "wrong-pass", "1.1.1.1")
	if !errors.Is(err, core.ErrBadCredentials) {
		t.Fatal(err)
	}
	if len(hashes) != 2 {
		t.Fatalf("compare calls=%d, want 2", len(hashes))
	}
	if hashes[0] != u.PasswordHash {
		t.Error("known user compared against wrong hash")
	}
	dummy := hashes[1]
	if dummy == u.PasswordHash || dummy == "" {
		t.Error("unknown user did not use dummy hash")
	}
	cd, err := bcrypt.Cost([]byte(dummy))
	cu, _ := bcrypt.Cost([]byte(u.PasswordHash))
	if err != nil || cd != cu {
		t.Errorf("dummy cost %d != user cost %d (%v)", cd, cu, err)
	}
	// ใช้ dummy ตัวเดิมทุกครั้ง (ไม่ gen ใหม่)
	_, _, _ = s.Login(bg, "ghost2", "x", "1.1.1.1")
	if hashes[2] != dummy {
		t.Error("dummy hash regenerated")
	}
	// แม้ผู้ใช้ไม่มีอยู่ รหัสไหนก็ไม่ผ่าน
	if s.verify(dummy, "") {
		t.Error("dummy accepts empty")
	}
}

func TestDummyHashProductionCost(t *testing.T) {
	s := New(newFakeStore(), Options{})
	c, err := bcrypt.Cost([]byte(s.getDummyHash()))
	if err != nil || c != 12 {
		t.Errorf("dummy cost=%d err=%v", c, err)
	}
}

func TestThrottle(t *testing.T) {
	s, st, clk := newSvc(t)
	addUser(t, s, st, "alice", "correct-horse", false)
	ip := "9.9.9.9"
	for i := 0; i < 5; i++ {
		if _, _, err := s.Login(bg, "alice", "bad-guess", ip); !errors.Is(err, core.ErrBadCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// ถูกล็อก: แม้รหัสถูกก็ไม่ผ่าน
	if _, _, err := s.Login(bg, "alice", "correct-horse", ip); !errors.Is(err, core.ErrTooManyAttempts) {
		t.Fatalf("should be locked: %v", err)
	}
	// IP อื่น และผู้ใช้อื่น ไม่ถูกกระทบ
	if _, _, err := s.Login(bg, "alice", "correct-horse", "8.8.8.8"); err != nil {
		t.Errorf("other IP affected: %v", err)
	}
	// ล็อกต่อเนื่อง 14 นาที 59 วินาที
	clk.Add(14*time.Minute + 59*time.Second)
	if _, _, err := s.Login(bg, "alice", "correct-horse", ip); !errors.Is(err, core.ErrTooManyAttempts) {
		t.Fatalf("lock ended early: %v", err)
	}
	// ครบ 15 นาที ปลดล็อก
	clk.Add(2 * time.Second)
	if _, _, err := s.Login(bg, "alice", "correct-horse", ip); err != nil {
		t.Fatalf("still locked: %v", err)
	}
	if !strings.Contains(st.logText(), "ล็อกการเข้าสู่ระบบ") {
		t.Error("lock not logged")
	}
}

func TestThrottleAppliesToUnknownUsersAndCase(t *testing.T) {
	s, _, _ := newSvc(t)
	for i := 0; i < 5; i++ {
		_, _, _ = s.Login(bg, []string{"Ghost", "ghost", "GHOST", "ghost ", "gHost"}[i], "x", "7.7.7.7")
	}
	if _, _, err := s.Login(bg, "ghost", "x", "7.7.7.7"); !errors.Is(err, core.ErrTooManyAttempts) {
		t.Errorf("unknown user not throttled: %v", err)
	}
}

func TestThrottleWindowExpiryAndReset(t *testing.T) {
	s, st, clk := newSvc(t)
	addUser(t, s, st, "alice", "correct-horse", false)
	ip := "6.6.6.6"
	for i := 0; i < 4; i++ {
		_, _, _ = s.Login(bg, "alice", "bad-guess", ip)
	}
	// สำเร็จ → ล้างตัวนับ
	if _, _, err := s.Login(bg, "alice", "correct-horse", ip); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		_, _, _ = s.Login(bg, "alice", "bad-guess", ip)
	}
	if _, _, err := s.Login(bg, "alice", "bad-guess", ip); !errors.Is(err, core.ErrBadCredentials) {
		t.Fatalf("5th failure should still be bad creds: %v", err)
	}
	if _, _, err := s.Login(bg, "alice", "correct-horse", ip); !errors.Is(err, core.ErrTooManyAttempts) {
		t.Fatalf("expected lock: %v", err)
	}
	clk.Add(16 * time.Minute)
	// หน้าต่างเก่าหมดแล้ว: ผิดครั้งเดียวไม่ล็อก
	_, _, _ = s.Login(bg, "alice", "bad-guess", ip)
	if _, _, err := s.Login(bg, "alice", "correct-horse", ip); err != nil {
		t.Errorf("counter not reset after window: %v", err)
	}
	// ความผิดห่างกันเกิน 15 นาทีไม่สะสม
	for i := 0; i < 8; i++ {
		_, _, _ = s.Login(bg, "alice", "bad-guess", "5.5.5.5")
		clk.Add(4 * time.Minute)
		if _, _, err := s.Login(bg, "alice", "correct-horse", "4.4.4.4"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestThrottleUnit(t *testing.T) {
	th := newThrottle()
	now := time.Now()
	for i := 0; i < 4; i++ {
		if th.fail("k", now) {
			t.Fatal("locked too early")
		}
	}
	if !th.fail("k", now) {
		t.Fatal("5th should lock")
	}
	if th.fail("k", now) { // ไม่ขยายเวลาล็อก/ไม่ประกาศซ้ำ
		t.Error("lock re-announced")
	}
	if !th.blocked("k", now.Add(14*time.Minute)) || th.blocked("k", now.Add(15*time.Minute)) {
		t.Error("lock boundary wrong")
	}
	if len(th.m) != 0 {
		t.Error("expired entry not cleaned")
	}
	// sweep ทำงานเมื่อ map โต
	for i := 0; i < throttleSweep; i++ {
		th.fail(string(rune('a'))+hex.EncodeToString([]byte{byte(i), byte(i >> 8)}), now)
	}
	th.fail("trigger", now.Add(time.Hour))
	if len(th.m) > 2 {
		t.Errorf("sweep did not clear stale entries: %d", len(th.m))
	}
	// key ยาวถูกตัด
	if len(throttleKey(strings.Repeat("a", 500), strings.Repeat("b", 500))) > 140 {
		t.Error("key not clipped")
	}
}

func TestThrottleHardCap(t *testing.T) {
	th := newThrottle()
	now := time.Now()
	for i := 0; i < throttleHard; i++ {
		th.m[hex.EncodeToString([]byte{byte(i), byte(i >> 8), byte(i >> 16)})] = &attempt{count: 1, first: now}
	}
	th.fail("new-key", now)
	if _, ok := th.m["new-key"]; ok {
		t.Error("hard cap exceeded")
	}
}

func TestUserFromToken(t *testing.T) {
	s, st, clk := newSvc(t)
	u := addUser(t, s, st, "alice", "correct-horse", false)
	token, _, _ := s.Login(bg, "alice", "correct-horse", "1.1.1.1")

	got, err := s.UserFromToken(bg, token)
	if err != nil || got.ID != u.ID || got.PasswordHash != "" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, bad := range []string{"", "garbage", token + "x"} {
		if _, err := s.UserFromToken(bg, bad); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// ใกล้หมดอายุยังใช้ได้ ถึงเวลาแล้วใช้ไม่ได้และถูกลบ
	clk.Add(12*time.Hour - time.Second)
	if _, err := s.UserFromToken(bg, token); err != nil {
		t.Fatalf("expired early: %v", err)
	}
	clk.Add(time.Second)
	if _, err := s.UserFromToken(bg, token); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("expired session accepted: %v", err)
	}
	if st.sessionCount(u.ID) != 0 {
		t.Error("expired session not deleted")
	}
}

func TestUserFromTokenOrphanAndErrors(t *testing.T) {
	s, st, _ := newSvc(t)
	u := addUser(t, s, st, "alice", "correct-horse", false)
	token, _, _ := s.Login(bg, "alice", "correct-horse", "1.1.1.1")
	delete(st.users, u.ID)
	if _, err := s.UserFromToken(bg, token); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("orphan: %v", err)
	}
	if st.sessionCount(u.ID) != 0 {
		t.Error("orphan session kept")
	}
	st.failSession = errBoom
	if _, err := s.UserFromToken(bg, "abc"); !errors.Is(err, errBoom) {
		t.Errorf("session err: %v", err)
	}
	st.failSession = nil
	u2 := addUser(t, s, st, "carol", "correct-horse", false)
	tok2, _, _ := s.Login(bg, "carol", "correct-horse", "1.1.1.1")
	st.failGetUser = errBoom
	if _, err := s.UserFromToken(bg, tok2); !errors.Is(err, errBoom) || errors.Is(err, core.ErrNotFound) {
		t.Errorf("user err: %v", err)
	}
	if st.sessionCount(u2.ID) != 1 {
		t.Error("session deleted on transient store error")
	}
}

func TestLogout(t *testing.T) {
	s, st, _ := newSvc(t)
	u := addUser(t, s, st, "alice", "correct-horse", false)
	token, _, _ := s.Login(bg, "alice", "correct-horse", "1.1.1.1")
	if err := s.Logout(bg, token); err != nil {
		t.Fatal(err)
	}
	if st.sessionCount(u.ID) != 0 {
		t.Error("session not deleted")
	}
	if _, err := s.UserFromToken(bg, token); !errors.Is(err, core.ErrNotFound) {
		t.Error("token still valid")
	}
	if err := s.Logout(bg, token); err != nil { // ซ้ำ ไม่ error
		t.Errorf("idempotent: %v", err)
	}
	if err := s.Logout(bg, ""); err != nil {
		t.Error(err)
	}
	if !strings.Contains(st.logText(), "ออกจากระบบ") || strings.Contains(st.logText(), token) {
		t.Errorf("log: %s", st.logText())
	}
}

func TestChangePassword(t *testing.T) {
	s, st, _ := newSvc(t)
	u := addUser(t, s, st, "alice", "temp-passw0rd", true)
	cur, _, _ := s.Login(bg, "alice", "temp-passw0rd", "1.1.1.1")
	other, _, _ := s.Login(bg, "alice", "temp-passw0rd", "2.2.2.2")
	bobU := addUser(t, s, st, "bob", "bobs-password", false)
	bobTok, _, _ := s.Login(bg, "bob", "bobs-password", "3.3.3.3")

	if err := s.ChangePassword(bg, u.ID, cur, "temp-passw0rd", "brand-new-pw"); err != nil {
		t.Fatal(err)
	}
	got := st.users[u.ID]
	if got.MustChangePassword {
		t.Error("MustChangePassword still true")
	}
	if bcrypt.CompareHashAndPassword([]byte(got.PasswordHash), []byte("brand-new-pw")) != nil {
		t.Error("new password not stored")
	}
	if _, err := s.UserFromToken(bg, cur); err != nil {
		t.Errorf("current session lost: %v", err)
	}
	if _, err := s.UserFromToken(bg, other); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("other session kept: %v", err)
	}
	if _, err := s.UserFromToken(bg, bobTok); err != nil || st.sessionCount(bobU.ID) != 1 {
		t.Errorf("other user's session affected: %v", err)
	}
	if strings.Contains(st.logText(), "brand-new-pw") || strings.Contains(st.logText(), "temp-passw0rd") {
		t.Error("password in log")
	}
	if !strings.Contains(st.logText(), "เปลี่ยนรหัสผ่านสำเร็จ") {
		t.Error("not logged")
	}
}

func TestChangePasswordRejections(t *testing.T) {
	s, st, _ := newSvc(t)
	u := addUser(t, s, st, "alice", "old-password", false)
	cur, _, _ := s.Login(bg, "alice", "old-password", "1.1.1.1")
	before := st.users[u.ID].PasswordHash

	cases := []struct {
		name, old, new string
		want           error
	}{
		{"wrong old", "nope-nope", "brand-new-pw", core.ErrBadCredentials},
		{"short", "old-password", "short", core.ErrWeakPassword},
		{"same as old", "old-password", "old-password", core.ErrWeakPassword},
		{"same as username", "old-password", "Alice", core.ErrWeakPassword},
		{"too long", "old-password", strings.Repeat("z", 73), core.ErrWeakPassword},
	}
	for _, c := range cases {
		if err := s.ChangePassword(bg, u.ID, cur, c.old, c.new); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v want %v", c.name, err, c.want)
		}
	}
	if st.users[u.ID].PasswordHash != before {
		t.Error("hash changed on rejected change")
	}
	if _, err := s.UserFromToken(bg, cur); err != nil {
		t.Error("session lost on rejected change")
	}
	if err := s.ChangePassword(bg, 9999, cur, "a", "b"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
}

func TestChangePasswordForeignOrEmptyToken(t *testing.T) {
	s, st, _ := newSvc(t)
	u := addUser(t, s, st, "alice", "old-password", false)
	addUser(t, s, st, "bob", "bobs-password", false)
	own, _, _ := s.Login(bg, "alice", "old-password", "1.1.1.1")
	bobTok, _, _ := s.Login(bg, "bob", "bobs-password", "1.1.1.1")
	// token ของคนอื่นไม่ถูกใช้เป็นข้อยกเว้น → ลบทุก session ของ alice
	if err := s.ChangePassword(bg, u.ID, bobTok, "old-password", "brand-new-pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserFromToken(bg, own); !errors.Is(err, core.ErrNotFound) {
		t.Error("alice session kept")
	}
	if _, err := s.UserFromToken(bg, bobTok); err != nil {
		t.Error("bob session removed")
	}
}

func TestChangePasswordThrottled(t *testing.T) {
	s, st, clk := newSvc(t)
	u := addUser(t, s, st, "alice", "old-password", false)
	cur, _, _ := s.Login(bg, "alice", "old-password", "1.1.1.1")
	for i := 0; i < 5; i++ {
		_ = s.ChangePassword(bg, u.ID, cur, "wrong-guess", "brand-new-pw")
	}
	if err := s.ChangePassword(bg, u.ID, cur, "old-password", "brand-new-pw"); !errors.Is(err, core.ErrTooManyAttempts) {
		t.Errorf("got %v", err)
	}
	clk.Add(16 * time.Minute)
	if err := s.ChangePassword(bg, u.ID, cur, "old-password", "brand-new-pw"); err != nil {
		t.Errorf("after lock: %v", err)
	}
}

func TestResetPassword(t *testing.T) {
	s, st, _ := newSvc(t)
	u := addUser(t, s, st, "alice", "old-password", false)
	t1, _, _ := s.Login(bg, "alice", "old-password", "1.1.1.1")
	t2, _, _ := s.Login(bg, "alice", "old-password", "2.2.2.2")
	bobU := addUser(t, s, st, "bob", "bobs-password", false)
	bt, _, _ := s.Login(bg, "bob", "bobs-password", "3.3.3.3")

	temp, err := s.ResetPassword(bg, "ALICE")
	if err != nil || len(temp) != 12 {
		t.Fatalf("%q %v", temp, err)
	}
	got := st.users[u.ID]
	if !got.MustChangePassword || bcrypt.CompareHashAndPassword([]byte(got.PasswordHash), []byte(temp)) != nil {
		t.Error("reset state wrong")
	}
	for _, tok := range []string{t1, t2} {
		if _, err := s.UserFromToken(bg, tok); !errors.Is(err, core.ErrNotFound) {
			t.Error("session survived reset")
		}
	}
	if _, err := s.UserFromToken(bg, bt); err != nil || st.sessionCount(bobU.ID) != 1 {
		t.Error("bob affected")
	}
	if _, err := s.ResetPassword(bg, "ghost"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("ghost: %v", err)
	}
	if strings.Contains(st.logText(), temp) {
		t.Error("temp in log")
	}
	if _, _, err := s.Login(bg, "alice", temp, "1.1.1.1"); err != nil {
		t.Errorf("login with temp: %v", err)
	}
}

func TestCreateAPIKey(t *testing.T) {
	s, st, clk := newSvc(t)
	plain, k, err := s.CreateAPIKey(bg, " HR ", []string{"10.0.0.5", "192.168.0.0/24", "::1", "2001:db8::/32"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "zk_") {
		t.Fatal(plain)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(plain, "zk_"))
	if err != nil || len(raw) != 32 {
		t.Fatalf("key body: %v len=%d", err, len(raw))
	}
	if k.Name != "HR" || k.Prefix != plain[:8] || len(k.Prefix) != 8 || k.ID == 0 {
		t.Errorf("%+v", k)
	}
	if k.KeyHash != hashHex(plain) || k.KeyHash == plain {
		t.Error("hash wrong")
	}
	if !k.CreatedAt.Equal(clk.Now()) || len(k.AllowedIPs) != 4 {
		t.Errorf("%+v", k)
	}
	for _, v := range st.keys {
		if strings.Contains(v.KeyHash+v.Prefix+v.Name, plain) {
			t.Error("plain key stored")
		}
	}
	logs := st.logText()
	if strings.Contains(logs, plain) || strings.Contains(logs, k.KeyHash) {
		t.Error("key leaked to log")
	}
	if !strings.Contains(logs, "สร้างกุญแจ API") {
		t.Error("not logged")
	}
	p2, _, _ := s.CreateAPIKey(bg, "x", nil)
	if p2 == plain {
		t.Error("duplicate key")
	}
}

func TestCreateAPIKeyValidation(t *testing.T) {
	s, st, _ := newSvc(t)
	bad := [][]string{{"not-an-ip"}, {"10.0.0.0/33"}, {"1.2.3"}, {""}, {"  "}, {"10.0.0.1", "foo"}, {"1.2.3.4/"}, {"::g"}, make([]string, 101)}
	for _, b := range bad {
		if _, _, err := s.CreateAPIKey(bg, "n", b); !errors.Is(err, core.ErrInvalidInput) {
			t.Errorf("%q: %v", b, err)
		}
	}
	for _, n := range []string{"", "   ", strings.Repeat("n", 101)} {
		if _, _, err := s.CreateAPIKey(bg, n, nil); !errors.Is(err, core.ErrInvalidInput) {
			t.Errorf("name %q: %v", n, err)
		}
	}
	if len(st.keys) != 0 {
		t.Error("key stored despite validation failure")
	}
}

func TestCheckAPIKey(t *testing.T) {
	s, st, _ := newSvc(t)
	plain, k, _ := s.CreateAPIKey(bg, "open", nil)
	got, err := s.CheckAPIKey(bg, plain, "203.0.113.9")
	if err != nil || got.ID != k.ID {
		t.Fatalf("%+v %v", got, err)
	}
	for _, bad := range []string{"", "zk_nope", plain + "x", plain[:len(plain)-1], strings.ToUpper(plain), strings.Repeat("a", 300)} {
		if _, err := s.CheckAPIKey(bg, bad, "1.1.1.1"); !errors.Is(err, core.ErrUnauthorized) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// hash ของกุญแจใช้เป็นกุญแจไม่ได้
	if _, err := s.CheckAPIKey(bg, k.KeyHash, "1.1.1.1"); !errors.Is(err, core.ErrUnauthorized) {
		t.Error("hash accepted as key")
	}
	// ยกเลิกแล้ว
	if err := s.RevokeAPIKey(bg, k.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckAPIKey(bg, plain, "1.1.1.1"); !errors.Is(err, core.ErrUnauthorized) {
		t.Errorf("revoked: %v", err)
	}
	if err := s.RevokeAPIKey(bg, 12345); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("revoke missing: %v", err)
	}
	if !strings.Contains(st.logText(), "ยกเลิกกุญแจ API") || strings.Contains(st.logText(), plain) {
		t.Errorf("log: %s", st.logText())
	}
	st.failKey = errBoom
	if _, err := s.CheckAPIKey(bg, plain, "1.1.1.1"); !errors.Is(err, errBoom) || errors.Is(err, core.ErrUnauthorized) {
		t.Errorf("store err: %v", err)
	}
}

func TestCheckAPIKeyAllowedIPs(t *testing.T) {
	s, st, _ := newSvc(t)
	plain, _, _ := s.CreateAPIKey(bg, "hr", []string{"10.0.0.5", "192.168.1.0/24", "2001:db8::/32", "::1"})
	ok := []string{"10.0.0.5", "192.168.1.77", "192.168.1.0", "192.168.1.255", "2001:db8:1::1", "::1", "::ffff:10.0.0.5", " 10.0.0.5 "}
	for _, ip := range ok {
		if _, err := s.CheckAPIKey(bg, plain, ip); err != nil {
			t.Errorf("%q should pass: %v", ip, err)
		}
	}
	no := []string{"10.0.0.6", "192.168.2.1", "2001:db9::1", "", "garbage", "::2", "0.0.0.0", "10.0.0.5/32"}
	for _, ip := range no {
		if _, err := s.CheckAPIKey(bg, plain, ip); !errors.Is(err, core.ErrForbidden) {
			t.Errorf("%q should be forbidden: %v", ip, err)
		}
	}
	if !strings.Contains(st.logText(), "ไม่อยู่ในรายการที่อนุญาต") || strings.Contains(st.logText(), plain) {
		t.Errorf("log: %s", st.logText())
	}
	// IPv6-mapped prefix
	p2, _, _ := s.CreateAPIKey(bg, "m", []string{"::ffff:172.16.5.0/120"})
	if _, err := s.CheckAPIKey(bg, p2, "172.16.5.9"); err != nil {
		t.Errorf("mapped prefix: %v", err)
	}
	if _, err := s.CheckAPIKey(bg, p2, "172.16.6.1"); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("mapped prefix outside: %v", err)
	}
	// รายการว่าง = ทุก IP
	open, _, _ := s.CreateAPIKey(bg, "any", []string{})
	if _, err := s.CheckAPIKey(bg, open, "garbage"); err != nil {
		t.Errorf("empty list: %v", err)
	}
}

func TestIPAllowedMalformedStored(t *testing.T) {
	// ค่าเสียใน DB ต้องไม่ทำให้ผ่าน
	if ipAllowed([]string{"bogus", "1.2.3.4/99"}, "1.2.3.4") {
		t.Error("malformed entries matched")
	}
}

func TestTouchAPIKeyOncePerMinute(t *testing.T) {
	s, st, clk := newSvc(t)
	plain, k, _ := s.CreateAPIKey(bg, "k", nil)
	check := func() {
		t.Helper()
		if _, err := s.CheckAPIKey(bg, plain, "1.1.1.1"); err != nil {
			t.Fatal(err)
		}
	}
	check()
	check()
	clk.Add(59 * time.Second)
	check()
	if len(st.touchCalls) != 1 {
		t.Fatalf("touches=%d want 1", len(st.touchCalls))
	}
	clk.Add(time.Second)
	check()
	check()
	if len(st.touchCalls) != 2 {
		t.Fatalf("touches=%d want 2", len(st.touchCalls))
	}
	if st.keys[k.ID].LastUsedAt == nil {
		t.Error("LastUsedAt not set")
	}
	// กุญแจอื่นนับแยก
	p2, _, _ := s.CreateAPIKey(bg, "k2", nil)
	_, _ = s.CheckAPIKey(bg, p2, "1.1.1.1")
	if len(st.touchCalls) != 3 {
		t.Errorf("touches=%d want 3", len(st.touchCalls))
	}
}

func TestTouchRespectsPersistedLastUsed(t *testing.T) {
	s, st, clk := newSvc(t)
	plain, k, _ := s.CreateAPIKey(bg, "k", nil)
	recent := clk.Now().Add(-10 * time.Second)
	k.LastUsedAt = &recent
	st.keys[k.ID] = k
	_, _ = s.CheckAPIKey(bg, plain, "1.1.1.1") // เพิ่งใช้เมื่อ 10 วิก่อน (ก่อนรีสตาร์ท)
	if len(st.touchCalls) != 0 {
		t.Error("touched despite recent LastUsedAt")
	}
}

func TestConcurrentLoginsRace(t *testing.T) {
	s, st, _ := newSvc(t)
	addUser(t, s, st, "alice", "correct-horse", false)
	plain, _, _ := s.CreateAPIKey(bg, "k", nil)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pw := "correct-horse"
			if i%2 == 0 {
				pw = "wrong"
			}
			tok, _, err := s.Login(bg, "alice", pw, "1.1.1.1")
			if err == nil {
				_, _ = s.UserFromToken(bg, tok)
			}
			_, _ = s.CheckAPIKey(bg, plain, "1.1.1.1")
		}(i)
	}
	wg.Wait()
}

func TestIPThrottleSpraying(t *testing.T) {
	s, st, clk := newSvc(t)
	addUser(t, s, st, "alice", "correct-horse", false)
	ip := "3.3.3.3"
	for i := 0; i < 21; i++ {
		name := "ghost" + strings.Repeat("x", i)
		if i%2 == 0 {
			name = "user" + hex.EncodeToString([]byte{byte(i)})
		}
		if _, _, err := s.Login(bg, name, "bad", ip); !errors.Is(err, core.ErrBadCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, _, err := s.Login(bg, "alice", "correct-horse", ip); !errors.Is(err, core.ErrTooManyAttempts) {
		t.Fatalf("IP should be locked: %v", err)
	}
	if n := strings.Count(st.logText(), "ล็อก IP"); n != 1 {
		t.Errorf("IP lock log lines = %d, want 1", n)
	}
	if _, _, err := s.Login(bg, "alice", "correct-horse", "8.8.8.8"); err != nil {
		t.Errorf("other IP affected: %v", err)
	}
	clk.Add(14*time.Minute + 59*time.Second)
	if _, _, err := s.Login(bg, "alice", "correct-horse", ip); !errors.Is(err, core.ErrTooManyAttempts) {
		t.Fatalf("lock ended early: %v", err)
	}
	clk.Add(2 * time.Second)
	if _, _, err := s.Login(bg, "alice", "correct-horse", ip); err != nil {
		t.Fatalf("still locked after 15m: %v", err)
	}
}

func TestIPThrottleSuccessDoesNotReset(t *testing.T) {
	s, st, _ := newSvc(t)
	addUser(t, s, st, "alice", "correct-horse", false)
	ip := "2.2.2.2"
	for i := 0; i < 20; i++ {
		_, _, _ = s.Login(bg, "ghost"+strings.Repeat("x", i), "bad", ip)
	}
	if _, _, err := s.Login(bg, "alice", "correct-horse", ip); err != nil {
		t.Fatalf("20 failures must not lock: %v", err)
	}
	// สำเร็จไม่ล้างตัวนับ IP: ผิดอีกครั้งเดียว (รวม 21) → ล็อก
	if _, _, err := s.Login(bg, "ghost", "bad", ip); !errors.Is(err, core.ErrBadCredentials) {
		t.Fatal(err)
	}
	if _, _, err := s.Login(bg, "alice", "correct-horse", ip); !errors.Is(err, core.ErrTooManyAttempts) {
		t.Fatalf("success reset the IP counter: %v", err)
	}
}

func TestIPThrottleUnitSweepAndCap(t *testing.T) {
	th := newThrottleMax(ipThrottleMax)
	now := time.Now()
	for i := 0; i < ipThrottleMax-1; i++ {
		if th.fail("1.1.1.1", now) {
			t.Fatal("locked too early")
		}
	}
	if !th.fail("1.1.1.1", now) {
		t.Fatal("21st should lock")
	}
	for i := 0; i < throttleSweep; i++ {
		th.fail(hex.EncodeToString([]byte{byte(i), byte(i >> 8)}), now)
	}
	th.fail("trigger", now.Add(time.Hour))
	if len(th.m) > 2 {
		t.Errorf("sweep did not clear: %d", len(th.m))
	}
	for i := 0; i < throttleHard; i++ {
		th.m[hex.EncodeToString([]byte{byte(i), byte(i >> 8), byte(i >> 16)})] = &attempt{count: 1, first: now}
	}
	th.fail("new-ip", now)
	if _, ok := th.m["new-ip"]; ok {
		t.Error("hard cap exceeded")
	}
}
