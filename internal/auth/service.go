// Package auth: รหัสผ่าน, session, กุญแจ API และ Fiber middleware (ส่วน 3)
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"zkscan/internal/core"
)

const (
	DefaultSessionTTL = 12 * time.Hour
	bcryptCost        = 12
	touchInterval     = time.Minute
	maxKeyNameLen     = 100
	maxAllowedIPs     = 100
	logSource         = "auth"
	changePwIP        = "!change-password" // key แยกสำหรับกันเดารหัสเดิมตอนเปลี่ยนรหัส
)

// Options ตั้งค่า Service; ค่าศูนย์ใช้ค่าเริ่มต้น
type Options struct {
	SessionTTL time.Duration    // ค่าเริ่ม 12h
	Now        func() time.Time // ค่าเริ่ม time.Now

	// bcryptCost ให้เทสต์ใช้ cost ต่ำได้ (ค่าเริ่ม 12)
	bcryptCost int
}

// Service implement core.AuthService
type Service struct {
	st   core.Store
	ttl  time.Duration
	now  func() time.Time
	cost int

	throttle *throttle

	dummyOnce sync.Once
	dummyHash string

	touchMu sync.Mutex
	touched map[int64]time.Time

	// compareHook ใช้ในเทสต์ตรวจว่าทุกเส้นทางเทียบ bcrypt จริง
	compareHook func(hash string)
}

var _ core.AuthService = (*Service)(nil)

func New(st core.Store, opts Options) *Service {
	s := &Service{
		st:       st,
		ttl:      opts.SessionTTL,
		now:      opts.Now,
		cost:     opts.bcryptCost,
		throttle: newThrottle(),
		touched:  map[int64]time.Time{},
	}
	if s.ttl <= 0 {
		s.ttl = DefaultSessionTTL
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.cost == 0 {
		s.cost = bcryptCost
	}
	return s
}

// SessionTTL ให้ handler ใช้ตั้งอายุ cookie ให้ตรงกับ session
func (s *Service) SessionTTL() time.Duration { return s.ttl }

func (s *Service) log(ctx context.Context, level, msg string) {
	_ = s.st.AddLog(ctx, core.LogEntry{At: s.now(), Level: level, Source: logSource, Message: msg})
}

func (s *Service) hashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), s.cost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// verify เทียบรหัสผ่านกับ hash (คืน true เมื่อตรง)
func (s *Service) verify(hash, pw string) bool {
	if s.compareHook != nil {
		s.compareHook(hash)
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

func (s *Service) getDummyHash() string {
	s.dummyOnce.Do(func() {
		b, err := randomBytes(16)
		if err != nil {
			b = []byte("zkscan-dummy-password")
		}
		h, err := s.hashPassword(fmt.Sprintf("%x", b))
		if err != nil {
			// ไม่ควรเกิดขึ้นกับ cost ที่ถูกต้อง; ใช้ hash cost 12 รูปแบบถูกต้องแทน
			h = "$2a$12$abcdefghijklmnopqrstuuK9hFVQ0lXKXpEoQ5xk3pT6YJf8mQe6a"
		}
		s.dummyHash = h
	})
	return s.dummyHash
}

func scrub(u core.User) core.User {
	u.PasswordHash = "" // ไม่ส่ง hash ออกนอกแพ็กเกจ
	return u
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ---------------- session ----------------

func (s *Service) Login(ctx context.Context, username, password, ip string) (string, core.User, error) {
	uname := lowerASCII(strings.TrimSpace(username))
	key := throttleKey(uname, ip)
	now := s.now()

	// ตรวจการล็อกก่อนเทียบรหัสเสมอ แม้รหัสถูกก็ไม่บอก (กัน oracle)
	if s.throttle.blocked(key, now) {
		s.log(ctx, "warn", fmt.Sprintf("ปฏิเสธการเข้าสู่ระบบ: ลองผิดบ่อยเกินไป (IP %s)", clip(ip, 64)))
		return "", core.User{}, core.ErrTooManyAttempts
	}

	u, err := s.st.GetUserByUsername(ctx, uname)
	known := err == nil
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return "", core.User{}, fmt.Errorf("auth: อ่านผู้ใช้ไม่ได้: %w", err)
	}
	hash := u.PasswordHash
	if !known {
		hash = s.getDummyHash() // เวลาเท่ากับผู้ใช้ที่มีอยู่จริง
	}
	ok := s.verify(hash, password) && known

	if !ok {
		locked := s.throttle.fail(key, now)
		// ไม่ใส่ชื่อผู้ใช้ที่พิมพ์มาลง log ถ้าไม่มีในระบบ (อาจเป็นรหัสผ่านที่พิมพ์ผิดช่อง)
		who := "ชื่อผู้ใช้ที่ไม่มีในระบบ"
		if known {
			who = "ผู้ใช้ " + u.Username
		}
		s.log(ctx, "warn", fmt.Sprintf("เข้าสู่ระบบไม่สำเร็จ: %s (IP %s)", who, clip(ip, 64)))
		if locked {
			s.log(ctx, "warn", fmt.Sprintf("ล็อกการเข้าสู่ระบบ 15 นาที: %s (IP %s)", who, clip(ip, 64)))
		}
		return "", core.User{}, core.ErrBadCredentials
	}

	token, err := newToken()
	if err != nil {
		return "", core.User{}, fmt.Errorf("auth: สุ่ม token ไม่ได้: %w", err)
	}
	sess := core.Session{
		TokenHash: hashHex(token),
		UserID:    u.ID,
		IP:        clip(ip, 64),
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
	}
	if err := s.st.CreateSession(ctx, sess); err != nil {
		return "", core.User{}, fmt.Errorf("auth: สร้าง session ไม่ได้: %w", err)
	}
	s.throttle.reset(key)
	if err := s.st.TouchLogin(ctx, u.ID, now); err != nil {
		s.log(ctx, "warn", "บันทึกเวลาเข้าสู่ระบบล่าสุดไม่สำเร็จ")
	} else {
		t := now
		u.LastLoginAt = &t
	}
	s.log(ctx, "info", fmt.Sprintf("เข้าสู่ระบบสำเร็จ: ผู้ใช้ %s (IP %s)", u.Username, clip(ip, 64)))
	// ล้าง session ที่หมดอายุเป็นครั้งคราว (ไม่ให้ล้มการล็อกอิน)
	_ = s.st.DeleteExpiredSessions(ctx, now)
	return token, scrub(u), nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	h := hashHex(token)
	var uname string
	if sess, err := s.st.GetSession(ctx, h); err == nil {
		if u, err := s.st.GetUserByID(ctx, sess.UserID); err == nil {
			uname = u.Username
		}
	}
	if err := s.st.DeleteSession(ctx, h); err != nil && !errors.Is(err, core.ErrNotFound) {
		return fmt.Errorf("auth: ลบ session ไม่ได้: %w", err)
	}
	if uname != "" {
		s.log(ctx, "info", "ออกจากระบบ: ผู้ใช้ "+uname)
	}
	return nil
}

func (s *Service) UserFromToken(ctx context.Context, token string) (core.User, error) {
	if token == "" {
		return core.User{}, core.ErrNotFound
	}
	h := hashHex(token)
	sess, err := s.st.GetSession(ctx, h)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return core.User{}, core.ErrNotFound
		}
		return core.User{}, fmt.Errorf("auth: อ่าน session ไม่ได้: %w", err)
	}
	if !s.now().Before(sess.ExpiresAt) {
		_ = s.st.DeleteSession(ctx, h)
		return core.User{}, core.ErrNotFound
	}
	u, err := s.st.GetUserByID(ctx, sess.UserID)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			_ = s.st.DeleteSession(ctx, h)
			return core.User{}, core.ErrNotFound
		}
		return core.User{}, fmt.Errorf("auth: อ่านผู้ใช้ไม่ได้: %w", err)
	}
	return scrub(u), nil
}

// ---------------- passwords & users ----------------

func (s *Service) ChangePassword(ctx context.Context, userID int64, currentToken, oldPw, newPw string) error {
	u, err := s.st.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	now := s.now()
	key := throttleKey(u.Username, changePwIP)
	if s.throttle.blocked(key, now) {
		return core.ErrTooManyAttempts
	}
	if !s.verify(u.PasswordHash, oldPw) {
		s.throttle.fail(key, now)
		s.log(ctx, "warn", "เปลี่ยนรหัสผ่านไม่สำเร็จ: รหัสเดิมไม่ถูกต้อง ผู้ใช้ "+u.Username)
		return core.ErrBadCredentials
	}
	s.throttle.reset(key)
	if err := checkPolicy(u.Username, newPw); err != nil {
		return err
	}
	if newPw == oldPw {
		return core.ErrWeakPassword
	}
	hash, err := s.hashPassword(newPw)
	if err != nil {
		return fmt.Errorf("auth: เข้ารหัสผ่านไม่ได้: %w", err)
	}
	if err := s.st.UpdatePassword(ctx, u.ID, hash, false); err != nil {
		return err
	}
	// เก็บเฉพาะ session ปัจจุบัน และเฉพาะเมื่อมันเป็นของผู้ใช้นี้จริง
	except := ""
	if currentToken != "" {
		h := hashHex(currentToken)
		if sess, err := s.st.GetSession(ctx, h); err == nil && sess.UserID == u.ID {
			except = h
		}
	}
	if err := s.st.DeleteUserSessions(ctx, u.ID, except); err != nil {
		return fmt.Errorf("auth: ลบ session อื่นไม่ได้: %w", err)
	}
	s.log(ctx, "info", "เปลี่ยนรหัสผ่านสำเร็จ: ผู้ใช้ "+u.Username)
	return nil
}

func (s *Service) CreateUser(ctx context.Context, username string) (core.User, string, error) {
	uname, err := normalizeUsername(username)
	if err != nil {
		return core.User{}, "", err
	}
	u, temp, err := s.createWithTemp(ctx, uname)
	if err != nil {
		return core.User{}, "", err
	}
	s.log(ctx, "info", "สร้างผู้ดูแลใหม่: "+u.Username)
	return u, temp, nil
}

func (s *Service) createWithTemp(ctx context.Context, uname string) (core.User, string, error) {
	temp, err := newTempPassword()
	if err != nil {
		return core.User{}, "", fmt.Errorf("auth: สุ่มรหัสผ่านไม่ได้: %w", err)
	}
	hash, err := s.hashPassword(temp)
	if err != nil {
		return core.User{}, "", fmt.Errorf("auth: เข้ารหัสผ่านไม่ได้: %w", err)
	}
	u, err := s.st.CreateUser(ctx, core.User{
		Username:           uname,
		PasswordHash:       hash,
		MustChangePassword: true,
		CreatedAt:          s.now(),
	})
	if err != nil {
		return core.User{}, "", err
	}
	return scrub(u), temp, nil
}

func (s *Service) ResetPassword(ctx context.Context, username string) (string, error) {
	u, err := s.st.GetUserByUsername(ctx, lowerASCII(strings.TrimSpace(username)))
	if err != nil {
		return "", err
	}
	temp, err := newTempPassword()
	if err != nil {
		return "", fmt.Errorf("auth: สุ่มรหัสผ่านไม่ได้: %w", err)
	}
	hash, err := s.hashPassword(temp)
	if err != nil {
		return "", fmt.Errorf("auth: เข้ารหัสผ่านไม่ได้: %w", err)
	}
	if err := s.st.UpdatePassword(ctx, u.ID, hash, true); err != nil {
		return "", err
	}
	if err := s.st.DeleteUserSessions(ctx, u.ID, ""); err != nil {
		return "", fmt.Errorf("auth: ลบ session ไม่ได้: %w", err)
	}
	s.log(ctx, "info", "รีเซ็ตรหัสผ่าน: ผู้ใช้ "+u.Username)
	return temp, nil
}

func (s *Service) Bootstrap(ctx context.Context) (string, bool, error) {
	n, err := s.st.CountUsers(ctx)
	if err != nil {
		return "", false, err
	}
	if n != 0 {
		return "", false, nil
	}
	_, temp, err := s.createWithTemp(ctx, "admin")
	if err != nil {
		if errors.Is(err, core.ErrConflict) { // อีกโพรเซสสร้างตัดหน้า
			return "", false, nil
		}
		return "", false, err
	}
	s.log(ctx, "info", "สร้างบัญชี admin เริ่มต้น (ต้องเปลี่ยนรหัสผ่านเมื่อเข้าครั้งแรก)")
	return temp, true, nil
}

// ---------------- API keys ----------------

func validateAllowed(list []string) ([]string, error) {
	if len(list) > maxAllowedIPs {
		return nil, core.ErrInvalidInput
	}
	out := make([]string, 0, len(list))
	for _, raw := range list {
		e := strings.TrimSpace(raw)
		if e == "" {
			return nil, core.ErrInvalidInput
		}
		if strings.Contains(e, "/") {
			if _, err := netip.ParsePrefix(e); err != nil {
				return nil, core.ErrInvalidInput
			}
		} else if _, err := netip.ParseAddr(e); err != nil {
			return nil, core.ErrInvalidInput
		}
		out = append(out, e)
	}
	return out, nil
}

func ipAllowed(list []string, ip string) bool {
	if len(list) == 0 {
		return true
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return false
	}
	addr = addr.Unmap().WithZone("")
	for _, e := range list {
		if strings.Contains(e, "/") {
			p, err := netip.ParsePrefix(e)
			if err != nil {
				continue
			}
			pa := p.Addr().Unmap()
			bits := p.Bits()
			if p.Addr().Is4In6() && bits >= 96 {
				bits -= 96
			}
			if pp, err := pa.Prefix(bits); err == nil && pp.Contains(addr) {
				return true
			}
			continue
		}
		a, err := netip.ParseAddr(e)
		if err != nil {
			continue
		}
		if a.Unmap().WithZone("") == addr {
			return true
		}
	}
	return false
}

func (s *Service) CreateAPIKey(ctx context.Context, name string, allowedIPs []string) (string, core.APIKey, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxKeyNameLen {
		return "", core.APIKey{}, core.ErrInvalidInput
	}
	allowed, err := validateAllowed(allowedIPs)
	if err != nil {
		return "", core.APIKey{}, err
	}
	b, err := randomBytes(32)
	if err != nil {
		return "", core.APIKey{}, fmt.Errorf("auth: สุ่มกุญแจไม่ได้: %w", err)
	}
	plain := "zk_" + base64URL(b)
	k, err := s.st.CreateAPIKey(ctx, core.APIKey{
		Name:       name,
		Prefix:     plain[:8],
		KeyHash:    hashHex(plain),
		AllowedIPs: allowed,
		CreatedAt:  s.now(),
	})
	if err != nil {
		return "", core.APIKey{}, err
	}
	s.log(ctx, "info", fmt.Sprintf("สร้างกุญแจ API \"%s\" (%s…)", k.Name, k.Prefix))
	return plain, k, nil
}

// RevokeAPIKey ยกเลิกกุญแจและบันทึก log (ไม่อยู่ใน core.AuthService; ให้ส่วนหน้าเว็บเรียก)
func (s *Service) RevokeAPIKey(ctx context.Context, id int64) error {
	if err := s.st.RevokeAPIKey(ctx, id); err != nil {
		return err
	}
	s.log(ctx, "info", fmt.Sprintf("ยกเลิกกุญแจ API หมายเลข %d", id))
	return nil
}

func (s *Service) CheckAPIKey(ctx context.Context, plain, ip string) (core.APIKey, error) {
	if plain == "" || len(plain) > 256 {
		return core.APIKey{}, core.ErrUnauthorized
	}
	k, err := s.st.GetAPIKeyByHash(ctx, hashHex(plain))
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return core.APIKey{}, core.ErrUnauthorized
		}
		return core.APIKey{}, fmt.Errorf("auth: อ่านกุญแจไม่ได้: %w", err)
	}
	if k.Revoked {
		return core.APIKey{}, core.ErrUnauthorized
	}
	if !ipAllowed(k.AllowedIPs, ip) {
		s.log(ctx, "warn", fmt.Sprintf("กุญแจ API \"%s\" (%s…) ถูกปฏิเสธ: IP %s ไม่อยู่ในรายการที่อนุญาต", k.Name, k.Prefix, clip(ip, 64)))
		return core.APIKey{}, core.ErrForbidden
	}
	s.touchKey(ctx, k)
	return k, nil
}

// touchKey อัปเดต LastUsedAt ไม่เกินนาทีละครั้งต่อกุญแจ
func (s *Service) touchKey(ctx context.Context, k core.APIKey) {
	now := s.now()
	s.touchMu.Lock()
	last, seen := s.touched[k.ID]
	if !seen && k.LastUsedAt != nil {
		last, seen = *k.LastUsedAt, true
	}
	if seen && now.Sub(last) < touchInterval && !now.Before(last) {
		s.touchMu.Unlock()
		return
	}
	s.touched[k.ID] = now
	s.touchMu.Unlock()
	_ = s.st.TouchAPIKey(ctx, k.ID, now)
}
