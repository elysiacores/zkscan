package web

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"

	"zkscan/internal/core"
)

type loginView struct {
	Username string
	Err      string
}

func (w *Web) loginGet(c *fiber.Ctx) error {
	return w.render(c, fiber.StatusOK, "login", "เข้าสู่ระบบ", "", loginView{})
}

func (w *Web) loginPost(c *fiber.Ctx) error {
	username := strings.TrimSpace(c.FormValue("username"))
	password := c.FormValue("password")
	v := loginView{Username: username}
	if username == "" || password == "" {
		v.Err = "กรอกชื่อผู้ใช้และรหัสผ่านให้ครบ"
		return w.render(c, fiber.StatusBadRequest, "login", "เข้าสู่ระบบ", "", v)
	}
	token, u, err := w.d.Auth.Login(w.ctx(c), username, password, c.IP())
	switch {
	case err == nil:
		setSessionCookie(c, token, sessionTTL)
		if u.MustChangePassword {
			return c.Redirect("/change-password", fiber.StatusSeeOther)
		}
		return c.Redirect("/", fiber.StatusSeeOther)
	case errors.Is(err, core.ErrTooManyAttempts):
		v.Err = "ใส่รหัสผิดหลายครั้งเกินไป ลองใหม่ใน 15 นาที"
		return w.render(c, fiber.StatusTooManyRequests, "login", "เข้าสู่ระบบ", "", v)
	case errors.Is(err, core.ErrBadCredentials):
		v.Err = "ชื่อผู้ใช้หรือรหัสผ่านไม่ถูกต้อง"
		return w.render(c, fiber.StatusUnauthorized, "login", "เข้าสู่ระบบ", "", v)
	default:
		v.Err = "เข้าสู่ระบบไม่สำเร็จ ลองใหม่อีกครั้ง"
		return w.render(c, fiber.StatusInternalServerError, "login", "เข้าสู่ระบบ", "", v)
	}
}

func (w *Web) logout(c *fiber.Ctx) error {
	if tok := core.CurrentToken(c); tok != "" {
		_ = w.d.Auth.Logout(w.ctx(c), tok)
	}
	clearSessionCookie(c)
	return c.Redirect("/login", fiber.StatusSeeOther)
}

type passwordView struct {
	Err string
}

const weakPasswordMsg = "รหัสผ่านใหม่ยังไม่ปลอดภัยพอ: ต้องยาวอย่างน้อย 8 ตัวอักษร, ต้องไม่เหมือนชื่อผู้ใช้ และต้องไม่เหมือนรหัสผ่านเดิม"

func (w *Web) passwordGet(c *fiber.Ctx) error {
	return w.render(c, fiber.StatusOK, "password", "เปลี่ยนรหัสผ่าน", "", passwordView{})
}

func (w *Web) passwordPost(c *fiber.Ctx) error {
	u, ok := core.CurrentUser(c)
	if !ok {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	oldPw := c.FormValue("old_password")
	newPw := c.FormValue("new_password")
	confirm := c.FormValue("confirm_password")
	show := func(status int, msg string) error {
		return w.render(c, status, "password", "เปลี่ยนรหัสผ่าน", "", passwordView{Err: msg})
	}
	switch {
	case oldPw == "" || newPw == "" || confirm == "":
		return show(fiber.StatusBadRequest, "กรอกให้ครบทุกช่อง")
	case newPw != confirm:
		return show(fiber.StatusBadRequest, "รหัสผ่านใหม่ทั้งสองช่องไม่ตรงกัน")
	}
	err := w.d.Auth.ChangePassword(w.ctx(c), u.ID, core.CurrentToken(c), oldPw, newPw)
	switch {
	case err == nil:
		return w.redirectFlash(c, "/", "ok", "เปลี่ยนรหัสผ่านเรียบร้อยแล้ว")
	case errors.Is(err, core.ErrWeakPassword):
		return show(fiber.StatusBadRequest, weakPasswordMsg)
	case errors.Is(err, core.ErrBadCredentials), errors.Is(err, core.ErrUnauthorized):
		return show(fiber.StatusBadRequest, "รหัสผ่านเดิมไม่ถูกต้อง")
	case errors.Is(err, core.ErrTooManyAttempts):
		return show(fiber.StatusTooManyRequests, "ลองหลายครั้งเกินไป ลองใหม่ใน 15 นาที")
	default:
		return w.fail(c, err)
	}
}
