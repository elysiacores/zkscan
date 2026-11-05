package core

import (
	"context"
	"time"
)

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
	ListDevices(ctx context.Context) ([]Device, error)                   // เรียงตาม Name
	GetDevice(ctx context.Context, id int64) (Device, error)             // ErrNotFound
	CreateDevice(ctx context.Context, d Device) (Device, error)          // ErrConflict ถ้าชื่อซ้ำ
	UpdateDevice(ctx context.Context, d Device) error                    // ErrNotFound, ErrConflict
	DeleteDevice(ctx context.Context, id int64) error                    // เก็บ attendance ไว้ (DeviceName ยังอยู่)
	GetDeviceStatus(ctx context.Context, id int64) (DeviceStatus, error) // ไม่เคยดึง = DeviceStatus{DeviceID:id}
	ListDeviceStatus(ctx context.Context) (map[int64]DeviceStatus, error)

	// ข้อมูลที่ดึงมา
	// SaveSnapshot ในทรานแซกชันเดียว: แทนที่ users ของเครื่องนี้, เพิ่ม records ที่ยังไม่มี
	// (ไม่ซ้ำตาม device_id+user_id+timestamp), เติม Name จาก users ถ้าว่าง,
	// อัปเดต DeviceStatus (OK, info, เวลา) แล้วคืนจำนวน records ใหม่
	SaveSnapshot(ctx context.Context, deviceID int64, snap Snapshot, pulledAt time.Time) (int, error)
	// SavePullFailure อัปเดต DeviceStatus เมื่อดึงไม่สำเร็จ (ไม่แตะ users/records)
	SavePullFailure(ctx context.Context, deviceID int64, errMsg, hint string, at time.Time) error
	ListAttendance(ctx context.Context, f AttendanceFilter) (rows []Attendance, total int, err error) // ใหม่สุดก่อน
	ListDeviceUsers(ctx context.Context, deviceID int64, search string) ([]DeviceUser, error)         // deviceID 0 = ทุกเครื่อง
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
	AddLog(ctx context.Context, e LogEntry) error                               // เก็บล่าสุด 5000 รายการ ตัดที่เก่ากว่าทิ้งเอง
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
