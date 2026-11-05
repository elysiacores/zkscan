package core

import (
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestVerifyModeLabel(t *testing.T) {
	cases := map[int]string{0: "รหัสผ่าน", 1: "ลายนิ้วมือ", 2: "บัตร", 15: "ใบหน้า", 7: "อื่น ๆ", -1: "อื่น ๆ"}
	for in, want := range cases {
		if got := VerifyModeLabel(in); got != want {
			t.Errorf("VerifyModeLabel(%d)=%q want %q", in, got, want)
		}
	}
}

func TestPunchStateLabel(t *testing.T) {
	cases := map[int]string{0: "เข้างาน", 1: "ออกงาน", 2: "พักออก", 3: "พักเข้า", 4: "OT เข้า", 5: "OT ออก", 6: "ไม่ระบุ", -1: "ไม่ระบุ"}
	for in, want := range cases {
		if got := PunchStateLabel(in); got != want {
			t.Errorf("PunchStateLabel(%d)=%q want %q", in, got, want)
		}
	}
}

func TestDefaultSettings(t *testing.T) {
	if got := DefaultSettings(); got.PullIntervalMin != 5 {
		t.Errorf("PullIntervalMin=%d want 5", got.PullIntervalMin)
	}
}

func TestCurrentUserAndToken(t *testing.T) {
	app := fiber.New()
	app.Get("/with", func(c *fiber.Ctx) error {
		c.Locals(LocalsUser, User{ID: 7, Username: "admin"})
		c.Locals(LocalsToken, "tok")
		u, ok := CurrentUser(c)
		return c.SendString(fmt.Sprintf("%v|%d|%s|%s", ok, u.ID, u.Username, CurrentToken(c)))
	})
	app.Get("/without", func(c *fiber.Ctx) error {
		u, ok := CurrentUser(c)
		return c.SendString(fmt.Sprintf("%v|%d|%q", ok, u.ID, CurrentToken(c)))
	})
	check := func(path, want string) {
		t.Helper()
		resp, err := app.Test(httptest.NewRequest("GET", path, nil))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		if string(b) != want {
			t.Errorf("%s: got %q want %q", path, b, want)
		}
	}
	check("/with", "true|7|admin|tok")
	check("/without", `false|0|""`)
}

func TestDeviceError(t *testing.T) {
	inner := io.ErrUnexpectedEOF
	var err error = fmt.Errorf("wrap: %w", &DeviceError{Kind: DevErrTimeout, Message: "หมดเวลา", Hint: "ตรวจสาย", Err: inner})
	var de *DeviceError
	if !errors.As(err, &de) {
		t.Fatal("errors.As failed")
	}
	if de.Kind != DevErrTimeout || de.Error() != "หมดเวลา" || de.Hint != "ตรวจสาย" {
		t.Errorf("unexpected %+v", de)
	}
	if errors.Unwrap(de) != inner || !errors.Is(err, inner) {
		t.Error("unwrap chain broken")
	}
}
