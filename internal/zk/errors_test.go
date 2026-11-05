package zk

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
	"testing"

	"zkscan/internal/core"
)

// ---- tests (port of mod.rs tests) ----

func TestHintConnectionRefused(t *testing.T) {
	if got := connectErr(syscall.ECONNREFUSED).Hint; got != hintRefused {
		t.Errorf("got %q", got)
	}
	// also when wrapped by net.OpError like a real dial failure
	wrapped := &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
	if got := toDeviceError(context.Background(), wrapped).Hint; got != hintRefused {
		t.Errorf("wrapped %q", got)
	}
}

func TestHintHostNotFoundKinds(t *testing.T) {
	for _, e := range []error{syscall.EHOSTUNREACH, syscall.ENETUNREACH, syscall.EADDRNOTAVAIL, syscall.EINVAL,
		&net.DNSError{Err: "no such host", Name: "x"}} {
		if got := connectErr(e).Hint; got != hintNotFound {
			t.Errorf("%v: %q", e, got)
		}
	}
}

func TestHintTimeout(t *testing.T) {
	if !strings.HasPrefix(timeoutErr(nil).Hint, "เครื่องไม่ตอบ:") {
		t.Error("timeout hint")
	}
}

func TestHintUnauthorized(t *testing.T) {
	if !strings.HasPrefix(unauthorizedErr().Hint, "รหัสเชื่อมต่อไม่ตรง:") {
		t.Error("unauthorized hint")
	}
}

func TestHintProtocol(t *testing.T) {
	if !strings.HasPrefix(protoErr("x").Hint, "เครื่องตอบกลับในรูปแบบที่ไม่รู้จัก:") {
		t.Error("protocol hint")
	}
}

func TestHintOtherIO(t *testing.T) {
	for _, e := range []error{syscall.ECONNRESET, syscall.EACCES} {
		if got := connectErr(e).Hint; got != hintOtherIO {
			t.Errorf("%v: %q", e, got)
		}
	}
}

func TestFetchOnClosedLocalPortGivesRefusedHint(t *testing.T) {
	// bind to get a free port, then close the listener so nothing listens there
	l, err := net.Listen("tcp", "127.0.0.1:0")
	mustNoErr(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	d := core.Device{Name: "test", IP: "127.0.0.1", Port: port, TimeoutSec: 2}
	_, err = New().Fetch(context.Background(), d)
	var de *core.DeviceError
	if !errors.As(err, &de) {
		t.Fatalf("want *DeviceError, got %T %v", err, err)
	}
	if de.Hint != hintRefused || de.Kind != core.DevErrConnect {
		t.Errorf("kind=%v hint=%q err=%v", de.Kind, de.Hint, err)
	}
	if !strings.HasPrefix(de.Message, "เชื่อมต่อไม่ได้: ") {
		t.Errorf("message %q", de.Message)
	}
}

func TestErrorMessagesMatchRust(t *testing.T) {
	if timeoutErr(nil).Message != "เครื่องไม่ตอบสนอง (หมดเวลา)" ||
		unauthorizedErr().Message != "รหัสเชื่อมต่อ (Comm Key) ไม่ถูกต้อง" ||
		protoErr("x").Message != "ข้อมูลจากเครื่องผิดรูปแบบ: x" {
		t.Error("messages differ from the Rust Display texts")
	}
	if toDeviceError(context.Background(), &net.OpError{Err: timeoutNetErr{}}).Kind != core.DevErrTimeout {
		t.Error("net timeouts must map to DevErrTimeout")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if de := toDeviceError(ctx, errors.New("use of closed network connection")); de.Kind != core.DevErrTimeout || !errors.Is(de, context.Canceled) {
		t.Errorf("ctx error: %+v", de)
	}
}

type timeoutNetErr struct{}

func (timeoutNetErr) Error() string   { return "i/o timeout" }
func (timeoutNetErr) Timeout() bool   { return true }
func (timeoutNetErr) Temporary() bool { return true }
