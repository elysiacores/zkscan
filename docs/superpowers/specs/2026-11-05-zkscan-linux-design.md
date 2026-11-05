# zkscan — บริการดึงสแกนนิ้ว ZKTeco บน Linux Server (Go + Fiber)

วันที่: 2026-11-05 · สถานะ: อนุมัติให้สร้างโดยผู้ใช้ในแชต ("เริ่มเลย และใช้มดงาน")

## 1. ที่มาและเป้าหมาย

ระบบเดิมเป็น .NET ที่ต้องผูกกับ DLL ของ ZKTeco บน Windows ผู้ใช้อยากได้ **บริการบน Linux Server
ที่ดึงข้อมูลจากเครื่องสแกนเอง แล้วเปิด API ให้ระบบอื่น (เช่น HR) ดึงต่อ** ตั้งค่าทั้งหมดผ่านหน้าเว็บ

- ภาษา: Go 100% (Fiber v2) — ไม่มี Rust ในระบบใหม่
- โค้ด Rust เดิมใน `../app/src/` ใช้เป็น **ต้นแบบอ้างอิง** สำหรับโปรโตคอล ZKTeco และรูปแบบ JSON ของ API
  (เขียนแบบ clean-room เอง ไม่ได้มาจากโปรแกรมเก่า จึงแปลงได้อิสระ)

### เป้าหมาย
1. ไฟล์โปรแกรมเดียว `zkscan` รันเป็น systemd service บน Linux (amd64/arm64), `CGO_ENABLED=0`
2. ดึงข้อมูลจากเครื่องสแกนทุกเครื่องตามรอบ (ค่าเริ่มต้นทุก 5 นาที) และสั่ง "ดึงเดี๋ยวนี้" ได้
3. เก็บข้อมูลถาวรใน SQLite (pure Go: `modernc.org/sqlite`) ไม่บันทึกซ้ำ
4. หน้าเว็บภาษาไทยที่พอร์ต 6565 ต้องล็อกอิน
5. API ที่ `/api/...` พอร์ตเดียวกัน ต้องใช้กุญแจ API
6. ติดตั้งด้วย `git clone` + `sudo ./install.sh` หรือ `docker compose up -d`

### ไม่ทำในรอบนี้ (non-goals)
- กะงาน คำนวณมาสาย รายงาน
- แจ้งเตือน LINE/อีเมล
- สิทธิ์ผู้ใช้หลายระดับ (มีบทบาทเดียว: ผู้ดูแล)
- HTTPS ในตัว (ใช้ในวงแลน; ถ้าต้องการให้วางหลัง reverse proxy)
- ส่งข้อมูล (push) ไป URL ภายนอก, export ไป SQL Server

### สิ่งที่ถือว่าล้มเหลวแม้จะทำงานได้ (anti-goals)
- ใครในวงแลนก็ดึงข้อมูลพนักงานได้โดยไม่มีกุญแจ หรือเข้าหน้าตั้งค่าได้โดยไม่ล็อกอิน
- มีรหัสผ่านตายตัว (เช่น admin/admin)
- เครื่องสแกนเครื่องหนึ่งค้าง/ต่อไม่ได้ แล้วทำให้เครื่องอื่นไม่ถูกดึง หรือหน้าเว็บค้าง
- ข้อมูลหายเมื่อรีสตาร์ท หรือบันทึกเวลาซ้ำ
- ข้อความผิดพลาดที่ผู้ใช้เห็นเป็นภาษาอังกฤษล้วน/ศัพท์เทคนิค

## 2. การตัดสินใจ

| เรื่อง | การตัดสินใจ | ใครตัดสิน |
|---|---|---|
| ภาษา/เฟรมเวิร์ก | Go + Fiber v2 | ผู้ใช้ |
| หน้าเว็บตั้งค่า | `http://<server>:6565` ต้องล็อกอิน | ผู้ใช้ |
| ล็อกอินครั้งแรก | สร้าง `admin` + รหัสชั่วคราวสุ่ม แสดงตอนติดตั้ง บังคับเปลี่ยนทันทีหลังเข้า | ผู้ใช้ (รหัสสุ่มแทนรหัสตายตัว = ข้อเสนอของ lead ที่ผู้ใช้ไม่ค้าน) |
| กุญแจ API | บังคับเสมอ แยกกุญแจต่อระบบ จำกัด IP ได้ (ไม่บังคับ) | ผู้ใช้ |
| ฐานข้อมูล | SQLite ไฟล์เดียว | lead |
| บทบาทผู้ใช้ | ผู้ดูแลอย่างเดียว สร้างได้หลายบัญชี | lead |
| Go module path | `zkscan` (เปลี่ยนเป็น github path ได้ภายหลัง) | lead |
| หน้าเว็บ | server-rendered `html/template` + CSS ฝังในไฟล์โปรแกรม (`embed`), JS น้อยที่สุด, ฟอนต์ Sarabun (OFL) | lead |

## 3. โครงสร้างโปรเจกต์

```
server/
  go.mod                      module zkscan, go 1.24
  cmd/zkscan/main.go          CLI + ประกอบทุกส่วน           (ส่วน 8)
  internal/core/              ข้อตกลงกลาง: types + interfaces (ส่วน 0)
  internal/zk/                คุยกับเครื่อง ZKTeco + เครื่องจำลอง (ส่วน 1)
  internal/store/             SQLite                        (ส่วน 2)
  internal/auth/              รหัสผ่าน session กุญแจ API middleware (ส่วน 3)
  internal/api/               JSON API                      (ส่วน 4)
  internal/web/               หน้าเว็บ + handler + static    (ส่วน 5)
  internal/puller/            ดึงตามรอบ                      (ส่วน 7)
  deploy/                     install.sh, systemd, Docker   (ส่วน 6)
  Makefile, README.md, Dockerfile, docker-compose.yml       (ส่วน 6)
```

ทุกส่วน (ยกเว้น `core` และ `cmd`) **import ได้แค่ `internal/core`** ห้าม import แพ็กเกจของส่วนอื่น
ส่วน 8 (`cmd/zkscan`) เป็นที่เดียวที่ประกอบ implementation จริงเข้าด้วยกัน

## 4. ข้อตกลงกลาง `internal/core` (สัญญาที่ทุกส่วนต้องตรงกัน)

ไฟล์: `internal/core/types.go`, `internal/core/interfaces.go`, `internal/core/errors.go`, `internal/core/ctx.go`

```go
package core

import (
	"context"
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
)

// TimeLayout คือรูปแบบเวลาทุกที่ที่เป็นข้อความ (DB, JSON, CSV, หน้าเว็บ)
const TimeLayout = "2006-01-02 15:04:05"
const DateLayout = "2006-01-02"

type Transport string

const (
	TransportTCP Transport = "tcp"
	TransportUDP Transport = "udp"
)

// Device = เครื่องสแกนที่ผู้ดูแลตั้งค่าไว้
type Device struct {
	ID         int64
	Name       string    // ไม่ซ้ำกัน
	IP         string
	Port       int       // ค่าเริ่มต้น 4370
	CommKey    int       // 0..999999, ค่าเริ่มต้น 0
	Transport  Transport // ค่าเริ่มต้น tcp
	TimeoutSec int       // ค่าเริ่มต้น 10, 1..120
	Demo       bool      // true = เครื่องจำลอง ไม่ใช้เครือข่าย
	Enabled    bool      // false = ข้ามตอนดึงตามรอบ
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// DeviceInfo = ข้อมูลที่อ่านจากตัวเครื่อง
type DeviceInfo struct {
	Serial      string
	Firmware    string
	Platform    string
	DeviceName  string
	UserCount   *int
	RecordCount *int
	DeviceTime  *time.Time
}

// DeviceUser = พนักงานที่ลงทะเบียนในเครื่องสแกน
type DeviceUser struct {
	DeviceID   int64 // 0 ใน Snapshot; store ใส่ให้ตอนบันทึก
	DeviceName string
	UID        int    // ลำดับภายในเครื่อง
	UserID     string // รหัสพนักงาน (ข้อความ)
	Name       string
	Privilege  int    // 0 ผู้ใช้, 14 ผู้ดูแลเครื่อง
	Card       uint32
}

// Attendance = บันทึกเวลาเข้า-ออก 1 รายการ
// Timestamp เป็นเวลาตามนาฬิกาเครื่อง (ไม่มี timezone) สร้างด้วย time.Local
// และเก็บเป็นข้อความ TimeLayout เสมอ
type Attendance struct {
	ID         int64
	DeviceID   int64
	DeviceName string
	UserID     string
	Name       string // เติมจากรายชื่อพนักงานถ้ารู้ ไม่งั้น ""
	Timestamp  time.Time
	VerifyMode int // 0 รหัสผ่าน, 1 ลายนิ้วมือ, 2 บัตร, 15 ใบหน้า, อื่น ๆ
	PunchState int // 0 เข้างาน, 1 ออกงาน, 2 พักออก, 3 พักเข้า, 4 OT เข้า, 5 OT ออก
	PulledAt   time.Time
}

// Snapshot = ผลการดึงหนึ่งครั้งจากหนึ่งเครื่อง
type Snapshot struct {
	Info    DeviceInfo
	Users   []DeviceUser
	Records []Attendance // DeviceID/DeviceName/ID/PulledAt ว่างได้
}

// PullResult = ผลการดึงหนึ่งเครื่อง
type PullResult struct {
	DeviceID   int64
	DeviceName string
	OK         bool
	NewRecords int
	TotalUsers int
	Error      string // ข้อความไทย ว่างถ้า OK
	Hint       string // คำแนะนำไทย ว่างถ้า OK
	Duration   time.Duration
	At         time.Time
}

// DeviceStatus = สถานะล่าสุดของเครื่อง (เก็บใน DB)
type DeviceStatus struct {
	DeviceID     int64
	LastPullAt   *time.Time
	LastOK       bool
	LastError    string
	LastHint     string
	LastNew      int
	Info         DeviceInfo
}

// User = บัญชีผู้ดูแลหน้าเว็บ
type User struct {
	ID                 int64
	Username           string // a-z0-9._- 3..32 ตัว ไม่ซ้ำ (เก็บตัวเล็ก)
	PasswordHash       string // bcrypt
	MustChangePassword bool
	CreatedAt          time.Time
	LastLoginAt        *time.Time
}

type Session struct {
	TokenHash string // sha256 hex ของ token ใน cookie
	UserID    int64
	IP        string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type APIKey struct {
	ID         int64
	Name       string   // เช่น "HR"
	Prefix     string   // 8 ตัวแรกของกุญแจ ไว้แสดง เช่น "zk_ab12c"
	KeyHash    string   // sha256 hex ของกุญแจเต็ม
	AllowedIPs []string // ว่าง = ทุก IP; รับทั้ง IP เดี่ยวและ CIDR
	CreatedAt  time.Time
	LastUsedAt *time.Time
	Revoked    bool
}

type LogEntry struct {
	ID       int64
	At       time.Time
	Level    string // "info" | "warn" | "error"
	Source   string // "pull" | "auth" | "api" | "system" | "web"
	DeviceID *int64
	Message  string // ภาษาไทย ห้ามมีรหัสผ่าน/กุญแจ/Comm Key
}

type Settings struct {
	PullIntervalMin int // 0 = ปิดการดึงตามรอบ; ค่าเริ่มต้น 5; 0..1440
}

func DefaultSettings() Settings { return Settings{PullIntervalMin: 5} }

type AttendanceFilter struct {
	From       *time.Time // วันที่เริ่ม (รวมทั้งวัน) ใช้แค่ส่วนวันที่
	To         *time.Time // วันที่สิ้นสุด (รวมทั้งวัน)
	UserID     string     // ตรงทุกตัว
	DeviceID   int64      // 0 = ทุกเครื่อง
	DeviceName string     // "" = ทุกเครื่อง (ใช้กับ API ?device=)
	Search     string     // ค้นรหัสหรือชื่อ (contains, ไม่สนตัวเล็กใหญ่)
	Limit      int        // 0 = ไม่จำกัด
	Offset     int
}

// ---------------- interfaces ----------------

// DeviceClient คุยกับเครื่องสแกน (ส่วน 1)
// ทุก error ที่คืนต้องเป็น *DeviceError
type DeviceClient interface {
	Fetch(ctx context.Context, d Device) (Snapshot, error)
	TestConnection(ctx context.Context, d Device) (DeviceInfo, error)
	SetTime(ctx context.Context, d Device, t time.Time) error
}

// Store (ส่วน 2) — ทุก method ปลอดภัยต่อการเรียกพร้อมกันหลาย goroutine
type Store interface {
	// เครื่องสแกน
	ListDevices(ctx context.Context) ([]Device, error) // เรียงตาม Name
	GetDevice(ctx context.Context, id int64) (Device, error)                    // ErrNotFound
	CreateDevice(ctx context.Context, d Device) (Device, error)                 // ErrConflict ถ้าชื่อซ้ำ
	UpdateDevice(ctx context.Context, d Device) error                           // ErrNotFound, ErrConflict
	DeleteDevice(ctx context.Context, id int64) error                           // เก็บ attendance ไว้ (DeviceName ยังอยู่)
	GetDeviceStatus(ctx context.Context, id int64) (DeviceStatus, error)        // ไม่เคยดึง = DeviceStatus{DeviceID:id}
	ListDeviceStatus(ctx context.Context) (map[int64]DeviceStatus, error)

	// ข้อมูลที่ดึงมา
	// SaveSnapshot ในทรานแซกชันเดียว: แทนที่ users ของเครื่องนี้, เพิ่ม records ที่ยังไม่มี
	// (ไม่ซ้ำตาม device_id+user_id+timestamp), เติม Name จาก users ถ้าว่าง,
	// อัปเดต DeviceStatus (OK, info, เวลา) แล้วคืนจำนวน records ใหม่
	SaveSnapshot(ctx context.Context, deviceID int64, snap Snapshot, pulledAt time.Time) (int, error)
	// SavePullFailure อัปเดต DeviceStatus เมื่อดึงไม่สำเร็จ (ไม่แตะ users/records)
	SavePullFailure(ctx context.Context, deviceID int64, errMsg, hint string, at time.Time) error
	ListAttendance(ctx context.Context, f AttendanceFilter) (rows []Attendance, total int, err error) // ใหม่สุดก่อน
	ListDeviceUsers(ctx context.Context, deviceID int64, search string) ([]DeviceUser, error)        // deviceID 0 = ทุกเครื่อง
	CountAttendance(ctx context.Context) (int, error)
	CountDeviceUsers(ctx context.Context) (int, error)
	LastPullAt(ctx context.Context) (*time.Time, error) // เวลาที่ดึงสำเร็จล่าสุดของทุกเครื่อง

	// บัญชีผู้ดูแล
	CountUsers(ctx context.Context) (int, error)
	CreateUser(ctx context.Context, u User) (User, error) // ErrConflict
	GetUserByID(ctx context.Context, id int64) (User, error)
	GetUserByUsername(ctx context.Context, username string) (User, error)
	ListUsers(ctx context.Context) ([]User, error)
	UpdatePassword(ctx context.Context, userID int64, hash string, mustChange bool) error
	TouchLogin(ctx context.Context, userID int64, at time.Time) error
	DeleteUser(ctx context.Context, id int64) error // ลบ session ของผู้ใช้นั้นด้วย

	// session
	CreateSession(ctx context.Context, s Session) error
	GetSession(ctx context.Context, tokenHash string) (Session, error) // ErrNotFound
	DeleteSession(ctx context.Context, tokenHash string) error
	DeleteUserSessions(ctx context.Context, userID int64, exceptTokenHash string) error
	DeleteExpiredSessions(ctx context.Context, now time.Time) error

	// กุญแจ API
	CreateAPIKey(ctx context.Context, k APIKey) (APIKey, error)
	ListAPIKeys(ctx context.Context) ([]APIKey, error) // รวมที่ยกเลิกแล้ว
	GetAPIKeyByHash(ctx context.Context, keyHash string) (APIKey, error)
	RevokeAPIKey(ctx context.Context, id int64) error
	TouchAPIKey(ctx context.Context, id int64, at time.Time) error

	// บันทึกการทำงาน
	AddLog(ctx context.Context, e LogEntry) error // เก็บล่าสุด 5000 รายการ ตัดที่เก่ากว่าทิ้งเอง
	ListLogs(ctx context.Context, limit int, source string) ([]LogEntry, error) // ใหม่สุดก่อน, source "" = ทั้งหมด

	// การตั้งค่า
	GetSettings(ctx context.Context) (Settings, error) // ไม่เคยตั้ง = DefaultSettings()
	SaveSettings(ctx context.Context, s Settings) error

	Close() error
}

// Puller (ส่วน 7)
type Puller interface {
	// PullAll ดึงทุกเครื่องที่ Enabled ทีละเครื่อง; เครื่องที่ล้มเหลวไม่หยุดเครื่องอื่น
	// ถ้ากำลังดึงอยู่แล้ว คืน ErrBusy
	PullAll(ctx context.Context) ([]PullResult, error)
	// PullDevice ดึงเครื่องเดียว (แม้ Enabled=false); ErrNotFound, ErrBusy
	PullDevice(ctx context.Context, id int64) (PullResult, error)
	Running() bool
	LastResults() []PullResult // ผลรอบล่าสุด
}

// AuthService (ส่วน 3)
type AuthService interface {
	// Login: ตรวจรหัส + จำกัดการเดา คืน token (ใส่ใน cookie) ; ErrBadCredentials, ErrTooManyAttempts
	Login(ctx context.Context, username, password, ip string) (token string, u User, err error)
	Logout(ctx context.Context, token string) error
	// UserFromToken คืนผู้ใช้ของ session ที่ยังไม่หมดอายุ ; ErrNotFound
	UserFromToken(ctx context.Context, token string) (User, error)
	// ChangePassword ตรวจรหัสเดิม, ตรวจนโยบาย (ErrWeakPassword), ตั้ง MustChangePassword=false,
	// ลบ session อื่นของผู้ใช้ (เก็บ session ปัจจุบัน)
	ChangePassword(ctx context.Context, userID int64, currentToken, oldPw, newPw string) error
	// CreateUser สร้างผู้ดูแลใหม่ด้วยรหัสชั่วคราวสุ่ม MustChangePassword=true คืนรหัสชั่วคราว
	CreateUser(ctx context.Context, username string) (User, string, error)
	// ResetPassword ตั้งรหัสชั่วคราวใหม่ MustChangePassword=true ลบทุก session ของผู้ใช้นั้น
	ResetPassword(ctx context.Context, username string) (string, error)
	// Bootstrap: ถ้ายังไม่มีผู้ใช้เลย สร้าง "admin" + รหัสชั่วคราว คืน (temp, true)
	Bootstrap(ctx context.Context) (temp string, created bool, err error)
	// CreateAPIKey คืนกุญแจเต็ม (แสดงครั้งเดียว) ; ErrInvalidInput ถ้า IP/CIDR ผิดรูป
	CreateAPIKey(ctx context.Context, name string, allowedIPs []string) (plain string, k APIKey, err error)
	// CheckAPIKey ; ErrUnauthorized (ไม่มี/ผิด/ยกเลิกแล้ว), ErrForbidden (IP ไม่อยู่ในรายการ)
	CheckAPIKey(ctx context.Context, plain, ip string) (APIKey, error)
}

// ---------------- errors ----------------

var (
	ErrNotFound         = errors.New("not found")
	ErrConflict         = errors.New("conflict")
	ErrBusy             = errors.New("busy")
	ErrBadCredentials   = errors.New("bad credentials")
	ErrTooManyAttempts  = errors.New("too many attempts")
	ErrWeakPassword     = errors.New("weak password")
	ErrUnauthorized     = errors.New("unauthorized")
	ErrForbidden        = errors.New("forbidden")
	ErrInvalidInput     = errors.New("invalid input")
)

type DeviceErrorKind int

const (
	DevErrConnect DeviceErrorKind = iota // ต่อไม่ได้ / ปฏิเสธ / หาเครื่องไม่เจอ
	DevErrTimeout
	DevErrUnauthorized // Comm Key ผิด
	DevErrProtocol
)

// DeviceError: Message และ Hint เป็นภาษาไทยสำหรับผู้ใช้ทั่วไป
type DeviceError struct {
	Kind    DeviceErrorKind
	Message string
	Hint    string
	Err     error
}

func (e *DeviceError) Error() string { return e.Message }
func (e *DeviceError) Unwrap() error { return e.Err }

// ---------------- fiber context ----------------

const LocalsUser = "zkscan.user"     // core.User ที่ล็อกอินอยู่ (ใส่โดย auth middleware)
const LocalsToken = "zkscan.token"   // session token ปัจจุบัน (string)
const LocalsAPIKey = "zkscan.apikey" // core.APIKey (ใส่โดย API-key middleware)

func CurrentUser(c *fiber.Ctx) (User, bool) { u, ok := c.Locals(LocalsUser).(User); return u, ok }
func CurrentToken(c *fiber.Ctx) string       { t, _ := c.Locals(LocalsToken).(string); return t }

// ป้ายไทย
func VerifyModeLabel(m int) string // 0 รหัสผ่าน,1 ลายนิ้วมือ,2 บัตร,15 ใบหน้า, อื่น "อื่น ๆ"
func PunchStateLabel(p int) string // 0 เข้างาน,1 ออกงาน,2 พักออก,3 พักเข้า,4 OT เข้า,5 OT ออก, อื่น "ไม่ระบุ"
```

## 5. รายละเอียดแต่ละส่วน

### ส่วน 1 — `internal/zk` (คุยกับเครื่อง ZKTeco)
- แปลงจาก `../app/src/zk/protocol.rs`, `client.rs`, `mod.rs` และ `../app/src/demo.rs`
- `zk.New() core.DeviceClient` — ถ้า `d.Demo` คืนข้อมูลจำลองแบบ deterministic (12 พนักงานชื่อไทย 14 วัน ตาม demo.rs) ไม่แตะเครือข่าย
- TCP และ UDP, Comm Key auth, อ่าน info (serial, firmware, platform, ชื่อเครื่อง, จำนวนผู้ใช้/บันทึก, เวลาเครื่อง), อ่านผู้ใช้และบันทึกเวลาแบบ buffer/chunk, ตั้งเวลาเครื่อง, disable/enable device ระหว่างอ่าน, ปิดการเชื่อมต่อทุกกรณี
- เคารพ `ctx` และ `d.TimeoutSec`; ขีดจำกัดขนาด buffer เหมือน Rust (กันหน่วยความจำระเบิด)
- error ทุกตัวเป็น `*core.DeviceError` ข้อความและ hint ไทยตาม `zk/mod.rs` (`Display` และ `hint()`) แต่คำว่า "กด “ทดสอบการเชื่อมต่อ”" ยังใช้ได้เพราะหน้าเว็บมีปุ่มชื่อเดียวกัน
- ทดสอบ: แปลงชุดทดสอบ Rust ทั้งหมด (protocol 21, client 11 รวม fake device ทั้ง TCP/UDP, mod 7, demo 6) เป็น Go

### ส่วน 2 — `internal/store` (SQLite)
- `store.Open(path string) (*Store, error)` สร้างไฟล์/ตาราง (migration แบบมีเลขเวอร์ชันใน `schema_version`), เปิด WAL, `busy_timeout`, foreign keys; `*Store` implement `core.Store`
- ไดรเวอร์ `modernc.org/sqlite` (ชื่อ driver `"sqlite"`)
- attendance: unique index `(device_id, user_id, ts)`, index ที่ `ts`; เก็บ `device_name` ไว้ด้วยเพื่อให้ข้อมูลยังอ่านได้หลังลบเครื่อง (`device_id` ไม่เป็น FK แบบ cascade)
- เวลา: เก็บเป็น TEXT ตาม `core.TimeLayout` (attendance ตามนาฬิกาเครื่อง, อื่น ๆ เวลาท้องถิ่นของ server)
- `AllowedIPs` เก็บเป็น JSON text
- ทดสอบด้วยไฟล์ DB ชั่วคราว (`t.TempDir()`) ครอบทุก method, กันซ้ำ, ตัวกรองวันที่รวมทั้งวัน, เรียงใหม่สุดก่อน, ลบเครื่องแล้ว attendance ยังอยู่, เรียกพร้อมกันหลาย goroutine (`-race`)

### ส่วน 3 — `internal/auth`
- `auth.New(st core.Store, opts Options) *Service` implement `core.AuthService`
  - `Options{SessionTTL time.Duration (ค่าเริ่ม 12h); Now func() time.Time}`
- รหัสผ่าน: bcrypt cost 12; นโยบาย: อย่างน้อย 8 ตัว, ไม่เท่ากับชื่อผู้ใช้, ไม่เท่ารหัสเดิม
- รหัสชั่วคราว: สุ่ม 12 ตัวจาก `crypto/rand` ตัดตัวที่สับสน (0 O 1 l I)
- token session: 32 ไบต์สุ่ม base64url; เก็บ sha256 hex
- กุญแจ API: `"zk_" + base64url(32 ไบต์)`; Prefix = 8 ตัวแรก; เก็บ sha256 hex; อัปเดต LastUsedAt (ไม่เกินนาทีละครั้ง)
- จำกัดการเดา: ผิดเกิน 5 ครั้งใน 15 นาที ต่อ (username, IP) → `ErrTooManyAttempts` 15 นาที; เก็บในหน่วยความจำ (map + mutex) — และใช้เวลาเท่ากันเมื่อไม่มีชื่อผู้ใช้ (เทียบกับ dummy hash)
- Middleware (ฟังก์ชันในแพ็กเกจ auth, ใช้ `core.AuthService`):
  - `RequireLogin(svc core.AuthService) fiber.Handler` — อ่าน cookie `zkscan_session`; ไม่มี/หมดอายุ → redirect `/login` (ถ้า header `HX-Request` หรือ Accept JSON → 401); ใส่ `LocalsUser`, `LocalsToken`; ถ้า `MustChangePassword` และ path ไม่ใช่ `/change-password` หรือ `/logout` → redirect `/change-password`
  - `RequireAPIKey(svc core.AuthService) fiber.Handler` — รับ `Authorization: Bearer <key>` หรือ `X-API-Key`; 401 `{"error":"..."}` / 403; ใส่ `LocalsAPIKey`
  - `SameOrigin() fiber.Handler` — สำหรับ POST/PUT/DELETE ที่มาจากหน้าเว็บ: ต้องมี `Origin` หรือ `Referer` ที่ host ตรงกับ `Host` ไม่งั้น 403 (กัน CSRF)
  - `SetSessionCookie(c, token, ttl)`, `ClearSessionCookie(c)` — HttpOnly, SameSite=Strict, Path=/, Secure เมื่อ `c.Protocol()=="https"`
- บันทึก log (`Source "auth"`) เมื่อเข้าสำเร็จ/ผิด/ถูกล็อก/เปลี่ยนรหัส/สร้าง-ยกเลิกกุญแจ — ห้ามมีรหัสผ่านหรือกุญแจใน log
- ทดสอบ: ทุก method + middleware ด้วย `fiber.New()` + `app.Test()` และ fake store ใน `_test.go`

### ส่วน 4 — `internal/api` (JSON API)
- `api.Register(r fiber.Router, d Deps)`; `Deps{Store core.Store; Puller core.Puller}` — main ติด `RequireAPIKey` ไว้ที่ group `/api` ก่อนเรียก
- รูปแบบต้องตรงกับ API เดิม (`../app/src/api_server.rs`) และเพิ่มเติมดังนี้:

| Method/Path | คำตอบ |
|---|---|
| `GET /api/health` | `{"status":"ok","records":N,"users":N,"last_pull":"YYYY-MM-DD HH:MM:SS"\|null}` |
| `GET /api/devices` | `[{"id","name","ip","port","transport","demo","enabled","last_pull_at","last_ok","last_error"}]` (ไม่มี comm_key) |
| `GET /api/attendance?from=&to=&user_id=&device=&device_id=&limit=&offset=` | `{"count":n,"total":N,"records":[{"device","user_id","name","timestamp","verify_mode","punch_state"}]}` ; limit ค่าเริ่ม 5000 สูงสุด 50000 |
| `GET /api/users?device=&device_id=` | `{"count":n,"users":[{"device","uid","user_id","name","privilege","card"}]}` |
| `POST /api/pull?device_id=` | ไม่ใส่ = ทุกเครื่อง; `{"results":[{"device_id","device","ok","new_records","total_users","error","hint","duration_ms","at"}]}`; กำลังดึงอยู่ → 409 |

- error: `{"error":"<ข้อความ>"}` 400 (วันที่ผิดรูป: `invalid date for 'from', expected YYYY-MM-DD`), 404, 409; path ที่ไม่รู้จักใต้ `/api` → 404 JSON
- เวลาใน JSON ใช้ `core.TimeLayout`
- ทดสอบด้วย fake Store/Puller ใน `_test.go`

### ส่วน 5 — `internal/web` (หน้าเว็บภาษาไทย)
- `web.New(d Deps) (*Web, error)`; `Deps{Store core.Store; Auth core.AuthService; Puller core.Puller; Client core.DeviceClient; Version string}`
- `(*Web).RegisterPublic(app *fiber.App)` — `/static/*` (embed), `GET/POST /login`
- `(*Web).RegisterProtected(r fiber.Router)` — main ห่อด้วย `RequireLogin` + `SameOrigin` แล้วเรียก
- templates `html/template` (escape อัตโนมัติ) + CSS เดียว + ฟอนต์ Sarabun (คัดลอก `../app/assets/fonts/Sarabun-Regular.ttf` และ `Sarabun-OFL.txt` มาที่ `internal/web/static/fonts/`)
- หน้า (ข้อความไทยทั้งหมด, ใช้ได้ที่ความกว้าง 1280 และ 390):
  - `/login` — ฟอร์ม; ข้อความผิดแบบไม่บอกว่าชื่อหรือรหัสผิด; ถูกล็อก → "ลองใหม่ใน 15 นาที"
  - `/change-password` — บังคับเมื่อ MustChangePassword; ตรวจยืนยันรหัสใหม่ 2 ช่อง
  - `POST /logout`
  - `/` ภาพรวม — การ์ดเครื่องแต่ละเครื่อง (สถานะล่าสุด, เวลา, error+hint), จำนวนบันทึก/พนักงาน, เวลาดึงล่าสุด, ปุ่ม "ดึงข้อมูลทุกเครื่อง"
  - `/devices` รายการ + `/devices/new` + `/devices/:id/edit` (ชื่อ, IP, พอร์ต, Comm Key, TCP/UDP, หมดเวลา, โหมดสาธิต, เปิดใช้งาน) พร้อมคำอธิบาย Comm Key ตามโปรแกรมเดิม; POST สร้าง/แก้/ลบ (ยืนยันก่อนลบ); `POST /devices/:id/test` แสดงข้อมูลเครื่องหรือ error+hint; `POST /devices/:id/pull`; `POST /devices/:id/settime` (ตั้งเป็นเวลา server)
  - `/records` — ตัวกรอง ตั้งแต่/ถึง/ค้นหา/เครื่อง + ปุ่มลัด วันนี้/7 วัน/เดือนนี้, แบ่งหน้า 100 แถว, ปุ่ม "บันทึกเป็น CSV" → `/records.csv` (UTF-8 BOM, หัวคอลัมน์ไทย, ใช้ตัวกรองเดียวกัน, ตามรูปแบบ `../app/src/export.rs`)
  - `/staff` — พนักงานในเครื่อง ค้นหาได้ + `/staff.csv`
  - `/api-keys` — รายการ (ชื่อ, prefix, IP ที่อนุญาต, สร้างเมื่อ, ใช้ล่าสุด, สถานะ), สร้าง (แสดงกุญแจเต็มครั้งเดียวพร้อมปุ่มคัดลอก), ยกเลิก; ตัวอย่างการเรียก `curl -H "Authorization: Bearer ..." http://<host>/api/attendance?from=...`
  - `/users` — บัญชีผู้ดูแล, เพิ่ม (แสดงรหัสชั่วคราวครั้งเดียว), รีเซ็ตรหัส, ลบ (ห้ามลบตัวเองหรือคนสุดท้าย)
  - `/logs` — บันทึกการทำงาน 200 รายการล่าสุด กรองตาม source
  - `/settings` — รอบดึงอัตโนมัติ (นาที, 0 = ปิด)
- ข้อความแจ้งผลหลัง POST ใช้ redirect + flash (cookie สั้น ๆ หรือ query `?ok=`) ห้ามสะท้อนข้อความจากผู้ใช้โดยไม่ escape
- ห้ามแสดง Comm Key ในรายการ (แสดงได้ในฟอร์มแก้ไขเท่านั้น)
- ทดสอบด้วย fake deps: ทุกหน้า render 200, ฟอร์มสร้าง/แก้เครื่องตรวจค่าผิด (IP ว่าง, พอร์ตนอกช่วง), CSV มี BOM, XSS (ชื่อเครื่อง `<script>` ถูก escape)

### ส่วน 6 — `deploy/` และไฟล์ราก
- `Makefile`: `build`, `test` (`go test -race ./...`), `vet`, `dist` → `dist/zkscan-linux-amd64`, `dist/zkscan-linux-arm64` (`CGO_ENABLED=0 -trimpath -ldflags "-s -w -X main.version=$(VERSION)"`)
- `deploy/install.sh` (bash, idempotent, ต้องเป็น root):
  1. ตรวจ systemd; หาไฟล์โปรแกรม: `dist/zkscan-linux-<arch>` ถ้ามี, ไม่งั้น `go build` ถ้ามี Go ≥ 1.24, ไม่งั้นหยุดพร้อมคำแนะนำไทย (ติดตั้ง Go หรือใช้ Docker)
  2. สร้างผู้ใช้ระบบ `zkscan` (nologin), `/var/lib/zkscan` (0750), `/etc/zkscan/zkscan.env` (ถ้ายังไม่มี: `ZKSCAN_LISTEN=:6565`, `ZKSCAN_DATA=/var/lib/zkscan`)
  3. ติดตั้งไปที่ `/usr/local/bin/zkscan`
  4. รัน `zkscan init` ในนามผู้ใช้ `zkscan` (แสดงรหัสชั่วคราวถ้าเพิ่งสร้าง)
  5. ติดตั้ง `deploy/zkscan.service` → `systemctl daemon-reload && systemctl enable --now zkscan` (ถ้ารันอยู่ → restart)
  6. แสดงสรุปไทย: URL ที่เข้า (IP ของเครื่อง:6565), วิธีดูรหัสชั่วคราวอีกครั้ง, วิธีดู log
- `deploy/uninstall.sh` — หยุด/ลบ service และไฟล์โปรแกรม; **ไม่ลบข้อมูล** เว้นแต่ใส่ `--purge`
- `deploy/zkscan.service` — `User=zkscan`, `EnvironmentFile=/etc/zkscan/zkscan.env`, `ExecStart=/usr/local/bin/zkscan serve`, `Restart=on-failure`, hardening (`NoNewPrivileges`, `ProtectSystem=strict`, `ReadWritePaths=/var/lib/zkscan`, `ProtectHome`, `PrivateTmp`)
- `Dockerfile` multi-stage (golang:1.24 → distroless static nonroot หรือ alpine), `VOLUME /data`, `EXPOSE 6565`, entrypoint `zkscan serve` ด้วย `ZKSCAN_DATA=/data`; `docker-compose.yml` แมปพอร์ต 6565 + volume; บอกใน README ว่ารหัสชั่วคราวดูได้ด้วย `docker compose exec zkscan zkscan show-initial-password`
- `README.md` ภาษาไทย: ติดตั้ง (2 วิธี), เข้าครั้งแรก, เพิ่มเครื่อง/Comm Key, สร้างกุญแจ API + ตัวอย่าง curl ทุก endpoint, อัปเดต (`git pull && sudo ./deploy/install.sh`), สำรองข้อมูล (ไฟล์ `zkscan.db`), ลืมรหัส (`sudo -u zkscan zkscan reset-password admin`), ดู log (`journalctl -u zkscan -f`), ถอนการติดตั้ง
- `.gitignore`: `dist/`, `*.db`, `*.db-*`
- ทดสอบ: `bash -n` และ `shellcheck` (ถ้ามี) ทุกสคริปต์

### ส่วน 7 — `internal/puller`
- `puller.New(st core.Store, cl core.DeviceClient, now func() time.Time) *Puller` implement `core.Puller`
- `(*Puller).Run(ctx context.Context)` — วนจนกว่า ctx ถูกยกเลิก: อ่าน `Settings.PullIntervalMin` ทุกรอบ (เปลี่ยนในหน้าเว็บแล้วมีผลรอบถัดไปโดยไม่ต้องรีสตาร์ท), 0 = รอเช็กใหม่ทุก 30 วินาที; ดึงครั้งแรกหลังเริ่ม 5 วินาที
- ดึงทีละเครื่อง (ไม่ขนาน) ใช้ timeout ของเครื่อง + ctx; มี mutex แบบ TryLock — ทับซ้อนกัน → `ErrBusy`
- สำเร็จ → `SaveSnapshot` + log info "ดึงข้อมูล <เครื่อง> สำเร็จ ได้รายการใหม่ N รายการ"; ล้มเหลว → `SavePullFailure` + log error พร้อม hint (ดึง Message/Hint จาก `*core.DeviceError` ด้วย `errors.As`; error อื่นใช้ข้อความทั่วไป)
- panic ในการดึงเครื่องหนึ่งต้องไม่ทำให้บริการล่ม (recover → นับเป็นล้มเหลว)
- ทดสอบด้วย fake store/client: เครื่องหนึ่งล้มเหลวไม่กระทบอีกเครื่อง, ErrBusy, Enabled=false ถูกข้าม, interval เปลี่ยนแล้วมีผล, ctx ยกเลิกแล้ว Run คืน

### ส่วน 8 — `cmd/zkscan` (ประกอบ) — รอบ 2
- คำสั่ง: `zkscan serve | init | reset-password <username> | show-initial-password | version`
- ค่าตั้ง: flag `--listen` (env `ZKSCAN_LISTEN`, ค่าเริ่ม `:6565`), `--data` (env `ZKSCAN_DATA`, ค่าเริ่ม `./data`) → DB `<data>/zkscan.db`
- `init`: เปิด store, `Bootstrap`; ถ้าสร้างใหม่ เขียนรหัสชั่วคราวลง `<data>/initial-admin-password.txt` (0600) และพิมพ์ `รหัสผ่านชั่วคราวของ admin: <รหัส>`; ถ้ามีผู้ใช้แล้วพิมพ์ "มีบัญชีผู้ดูแลอยู่แล้ว"
- `serve`: เหมือน init (ถ้ายังไม่มีผู้ใช้ สร้างและ log รหัสลง stdout ครั้งเดียว) แล้วเปิด Fiber + puller; ลบไฟล์รหัสชั่วคราวเมื่อ admin เปลี่ยนรหัสแล้ว (เช็กตอนเริ่มและทุกครั้งที่เปลี่ยนรหัส); ปิดอย่างนุ่มนวลเมื่อได้ SIGINT/SIGTERM; ลบ session หมดอายุทุกชั่วโมง; log ภาษาไทยลง stdout (journald)
- `show-initial-password`: พิมพ์จากไฟล์ ถ้าไม่มีแล้วบอกให้ใช้ `reset-password`
- `reset-password`: พิมพ์รหัสชั่วคราวใหม่
- Fiber: `ServerHeader` ว่าง, `BodyLimit` 1MB, ErrorHandler ที่ตอบ JSON ใต้ `/api` และหน้า error ไทยที่อื่น, `ProxyHeader` ปิด (ใช้ IP จริงของ socket)
- ทดสอบ integration (`cmd/zkscan/integration_test.go`): DB ชั่วคราว + เครื่องสาธิต — bootstrap → ล็อกอินด้วยรหัสชั่วคราว → ถูกบังคับเปลี่ยนรหัส → เพิ่มเครื่องสาธิต → ดึง → สร้างกุญแจ → `/api/attendance` ได้ 200 และข้อมูล; ไม่มีกุญแจ → 401; ไม่ล็อกอิน → redirect `/login`

## 6. การตรวจของ lead (ก่อนบอกว่าเสร็จ)
1. อ่าน diff ทุกบรรทัดเทียบกับเอกสารนี้
2. `gofmt -l .` ว่าง, `go vet ./...`, `go test -race ./...` ทั้งโปรเจกต์, `make dist`
3. รันจริงบน Mac ที่ `localhost:6565` และถ่ายภาพทุกหน้า (กว้าง 1280 และ 390)
4. ทดสอบ `install.sh` ในคอนเทนเนอร์ Linux ที่มี systemd (ถ้า Docker ใช้ได้) และ `docker compose up`
5. ผู้ตรวจความปลอดภัยอิสระ (Sonnet) อ่าน auth/api/web โดยไม่ได้รับข้อสรุปของ lead
6. ที่ยังตรวจไม่ได้: เครื่องสแกนจริง (ต้องให้ผู้ใช้ทดลองในวงแลน)

## 7. การใช้งานหลังสร้างเสร็จ
- ลองบน Mac: `go run ./cmd/zkscan serve --data ./data` แล้วเปิด `http://localhost:6565`
- ขึ้น GitHub: ถามผู้ใช้ชื่อ repo / private ก่อน push
- บน server: `git clone <repo> && cd zkscan && sudo ./deploy/install.sh`
