package core

import "time"

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
	Name       string // ไม่ซ้ำกัน
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
	Privilege  int // 0 ผู้ใช้, 14 ผู้ดูแลเครื่อง
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
	DeviceID   int64
	LastPullAt *time.Time
	LastOK     bool
	LastError  string
	LastHint   string
	LastNew    int
	Info       DeviceInfo
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
