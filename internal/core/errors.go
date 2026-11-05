package core

import "errors"

// ---------------- errors ----------------

var (
	ErrNotFound        = errors.New("not found")
	ErrConflict        = errors.New("conflict")
	ErrBusy            = errors.New("busy")
	ErrBadCredentials  = errors.New("bad credentials")
	ErrTooManyAttempts = errors.New("too many attempts")
	ErrWeakPassword    = errors.New("weak password")
	ErrUnauthorized    = errors.New("unauthorized")
	ErrForbidden       = errors.New("forbidden")
	ErrInvalidInput    = errors.New("invalid input")
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
