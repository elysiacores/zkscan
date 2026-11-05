package zk

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"

	"zkscan/internal/core"
)

// Thai texts (same as the Rust ZkError Display / hint()).
const (
	msgConnectPrefix = "เชื่อมต่อไม่ได้: "
	msgTimeout       = "เครื่องไม่ตอบสนอง (หมดเวลา)"
	msgUnauthorized  = "รหัสเชื่อมต่อ (Comm Key) ไม่ถูกต้อง"
	msgProtoPrefix   = "ข้อมูลจากเครื่องผิดรูปแบบ: "

	hintRefused      = "เครื่องปฏิเสธการเชื่อมต่อ: ตรวจว่าพอร์ตถูกต้อง (ปกติคือ 4370) และในเมนูเครื่องเปิดการเชื่อมต่อผ่านเครือข่ายไว้"
	hintNotFound     = "หาเครื่องไม่เจอ: ตรวจเลข IP ในเมนูเครื่อง (การสื่อสาร › อีเทอร์เน็ต) และให้คอมพิวเตอร์อยู่ในวงแลนเดียวกับเครื่องสแกน"
	hintOtherIO      = "เชื่อมต่อไม่ได้: ตรวจ IP, พอร์ต และสายแลน แล้วกด “ทดสอบการเชื่อมต่อ” อีกครั้ง"
	hintTimeout      = "เครื่องไม่ตอบ: ตรวจเลข IP, สายแลน และพอร์ต (ปกติ 4370) ถ้ายังไม่ได้ลองเปลี่ยนการเชื่อมต่อเป็น UDP หรือเพิ่มเวลาหมดเวลา"
	hintUnauthorized = "รหัสเชื่อมต่อไม่ตรง: ใส่ Comm Key ให้ตรงกับในเมนูเครื่อง (การสื่อสาร › ความปลอดภัย) ถ้าเครื่องไม่ได้ตั้งไว้ให้ใส่ 0"
	hintProtocol     = "เครื่องตอบกลับในรูปแบบที่ไม่รู้จัก: ลองเปลี่ยน TCP/UDP แล้วลองใหม่ ถ้ายังไม่ได้ ส่งข้อความผิดพลาดนี้และรุ่นเครื่องมาให้ผู้พัฒนา"
)

func protoErr(msg string) *core.DeviceError {
	return &core.DeviceError{Kind: core.DevErrProtocol, Message: msgProtoPrefix + msg, Hint: hintProtocol}
}

func unauthorizedErr() *core.DeviceError {
	return &core.DeviceError{Kind: core.DevErrUnauthorized, Message: msgUnauthorized, Hint: hintUnauthorized}
}

func timeoutErr(cause error) *core.DeviceError {
	return &core.DeviceError{Kind: core.DevErrTimeout, Message: msgTimeout, Hint: hintTimeout, Err: cause}
}

// connectErr is the Rust ZkError::Io: message "เชื่อมต่อไม่ได้: <cause>" with an io-kind-specific hint.
func connectErr(cause error) *core.DeviceError {
	return &core.DeviceError{Kind: core.DevErrConnect, Message: msgConnectPrefix + cause.Error(), Hint: ioHint(cause), Err: cause}
}

func ioHint(err error) string {
	var dns *net.DNSError
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return hintRefused
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH),
		errors.Is(err, syscall.EADDRNOTAVAIL), errors.Is(err, syscall.EINVAL),
		errors.As(err, &dns):
		return hintNotFound
	default:
		return hintOtherIO
	}
}

func isTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, syscall.ETIMEDOUT) || errors.Is(err, syscall.EWOULDBLOCK) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// toDeviceError converts any error to a *core.DeviceError (Rust From<io::Error> plus ctx handling).
// A cancelled / expired ctx always wins: the I/O error it caused is an artefact of the cancellation.
func toDeviceError(ctx context.Context, err error) *core.DeviceError {
	var de *core.DeviceError
	if errors.As(err, &de) {
		return de
	}
	if ctx != nil && ctx.Err() != nil {
		return timeoutErr(ctx.Err())
	}
	if isTimeout(err) {
		return timeoutErr(err)
	}
	return connectErr(err)
}
