package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"math/big"

	"zkscan/internal/core"
)

// ตัวอักษรของรหัสชั่วคราว: ตัด 0 O 1 l I ที่อ่านสับสน
const tempAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

const (
	tempPasswordLen = 12
	minPasswordLen  = 8
	// bcrypt อ่านแค่ 72 ไบต์แรก; ยาวกว่านี้ปฏิเสธเพื่อไม่ให้ส่วนท้ายถูกเมินเงียบ ๆ
	maxPasswordBytes = 72
)

// randomBytes อ่านจาก crypto/rand; ถ้าอ่านไม่ได้ถือว่าระบบเสียหนัก
func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

func newTempPassword() (string, error) {
	max := big.NewInt(int64(len(tempAlphabet)))
	out := make([]byte, tempPasswordLen)
	for i := range out {
		n, err := rand.Int(rand.Reader, max) // ไม่มี modulo bias
		if err != nil {
			return "", err
		}
		out[i] = tempAlphabet[n.Int64()]
	}
	return string(out), nil
}

func newToken() (string, error) {
	b, err := randomBytes(32)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// lowerASCII แปลงเฉพาะ A-Z (ไม่ใช้ strings.ToLower เพราะ Unicode บางตัว
// เช่น U+212A KELVIN SIGN จะกลายเป็น 'k' ได้)
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// normalizeUsername คืนชื่อผู้ใช้ตัวเล็ก และ ErrInvalidInput ถ้ารูปแบบผิด
func normalizeUsername(u string) (string, error) {
	u = lowerASCII(u)
	if len(u) < 3 || len(u) > 32 {
		return "", core.ErrInvalidInput
	}
	for i := 0; i < len(u); i++ {
		c := u[i]
		ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-'
		if !ok {
			return "", core.ErrInvalidInput
		}
	}
	return u, nil
}

// checkPolicy: อย่างน้อย 8 ตัว, ไม่เท่าชื่อผู้ใช้ (ไม่สนตัวเล็กใหญ่), ไม่เกิน 72 ไบต์
func checkPolicy(username, pw string) error {
	if len([]rune(pw)) < minPasswordLen || len(pw) > maxPasswordBytes {
		return core.ErrWeakPassword
	}
	if lowerASCII(pw) == lowerASCII(username) {
		return core.ErrWeakPassword
	}
	return nil
}

func base64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
