package auth

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"zkscan/internal/core"
)

const SessionCookieName = "zkscan_session"

func errJSON(c *fiber.Ctx, status int, msg string) error {
	return c.Status(status).JSON(fiber.Map{"error": msg})
}

// wantsJSON: HX-Request หรือ Accept ที่ชอบ JSON มากกว่า HTML
func wantsJSON(c *fiber.Ctx) bool {
	if c.Get("HX-Request") != "" {
		return true
	}
	a := strings.ToLower(c.Get(fiber.HeaderAccept))
	j := strings.Index(a, "application/json")
	if j < 0 {
		return false
	}
	h := strings.Index(a, "text/html")
	return h < 0 || j < h
}

// RequireLogin ตรวจ session จาก cookie
func RequireLogin(svc core.AuthService) fiber.Handler {
	return func(c *fiber.Ctx) error {
		deny := func() error {
			if wantsJSON(c) {
				c.Set("HX-Redirect", "/login")
				return errJSON(c, fiber.StatusUnauthorized, "ต้องเข้าสู่ระบบก่อน")
			}
			return c.Redirect("/login", fiber.StatusSeeOther)
		}
		token := c.Cookies(SessionCookieName)
		if token == "" {
			return deny()
		}
		u, err := svc.UserFromToken(c.UserContext(), token)
		if err != nil {
			if errors.Is(err, core.ErrNotFound) {
				ClearSessionCookie(c)
				return deny()
			}
			return errJSON(c, fiber.StatusInternalServerError, "ระบบขัดข้อง ลองใหม่อีกครั้ง")
		}
		c.Locals(core.LocalsUser, u)
		c.Locals(core.LocalsToken, token)
		c.Set("Cache-Control", "no-store") // handler ตั้งทับได้
		if u.MustChangePassword {
			p := strings.TrimRight(c.Path(), "/")
			if p != "/change-password" && p != "/logout" {
				if wantsJSON(c) {
					return errJSON(c, fiber.StatusForbidden, "ต้องเปลี่ยนรหัสผ่านก่อนใช้งาน")
				}
				return c.Redirect("/change-password", fiber.StatusSeeOther)
			}
		}
		return c.Next()
	}
}

func apiKeyFromRequest(c *fiber.Ctx) string {
	if h := strings.TrimSpace(c.Get(fiber.HeaderAuthorization)); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		if k := strings.TrimSpace(h[7:]); k != "" {
			return k
		}
	}
	return strings.TrimSpace(c.Get("X-API-Key"))
}

// RequireAPIKey: Authorization: Bearer <key> หรือ X-API-Key
func RequireAPIKey(svc core.AuthService) fiber.Handler {
	return func(c *fiber.Ctx) error {
		plain := apiKeyFromRequest(c)
		if plain == "" {
			c.Set(fiber.HeaderWWWAuthenticate, "Bearer")
			return errJSON(c, fiber.StatusUnauthorized, "ต้องแนบกุญแจ API (Authorization: Bearer <กุญแจ> หรือ X-API-Key)")
		}
		k, err := svc.CheckAPIKey(c.UserContext(), plain, c.IP())
		switch {
		case err == nil:
			c.Locals(core.LocalsAPIKey, k)
			return c.Next()
		case errors.Is(err, core.ErrUnauthorized):
			c.Set(fiber.HeaderWWWAuthenticate, "Bearer")
			return errJSON(c, fiber.StatusUnauthorized, "กุญแจ API ไม่ถูกต้องหรือถูกยกเลิกแล้ว")
		case errors.Is(err, core.ErrForbidden):
			return errJSON(c, fiber.StatusForbidden, "กุญแจนี้ไม่ได้รับอนุญาตให้ใช้จาก IP นี้")
		default:
			return errJSON(c, fiber.StatusInternalServerError, "ระบบขัดข้อง ลองใหม่อีกครั้ง")
		}
	}
}

// SameOrigin กัน CSRF: คำขอที่เปลี่ยนสถานะต้องมี Origin (หรือ Referer) ที่ host ตรงกับ Host
func SameOrigin() fiber.Handler {
	return func(c *fiber.Ctx) error {
		switch c.Method() {
		case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
			return c.Next()
		}
		src := c.Get(fiber.HeaderOrigin)
		if src == "" {
			src = c.Get(fiber.HeaderReferer)
		}
		host := c.Get(fiber.HeaderHost)
		if src != "" && host != "" {
			if u, err := url.Parse(src); err == nil && (u.Scheme == "http" || u.Scheme == "https") &&
				u.Host != "" && strings.EqualFold(u.Host, host) {
				return c.Next()
			}
		}
		return errJSON(c, fiber.StatusForbidden, "คำขอถูกปฏิเสธ (ไม่ได้มาจากหน้าเว็บนี้)")
	}
}

func SetSessionCookie(c *fiber.Ctx, token string, ttl time.Duration) {
	c.Cookie(&fiber.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		Expires:  time.Now().Add(ttl),
		HTTPOnly: true,
		SameSite: fiber.CookieSameSiteStrictMode,
		Secure:   c.Protocol() == "https",
	})
}

func ClearSessionCookie(c *fiber.Ctx) {
	c.Cookie(&fiber.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HTTPOnly: true,
		SameSite: fiber.CookieSameSiteStrictMode,
		Secure:   c.Protocol() == "https",
	})
}
