package main

import (
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"

	"zkscan/internal/api"
	"zkscan/internal/auth"
	"zkscan/internal/core"
	"zkscan/internal/web"
)

// appDeps คือสิ่งที่ buildApp ต้องใช้ (ทดสอบแทนด้วยของจริงบน DB ชั่วคราวได้)
type appDeps struct {
	Store   core.Store
	Auth    core.AuthService
	Puller  core.Puller
	Client  core.DeviceClient
	Version string
}

func isAPIPath(p string) bool { return p == "/api" || strings.HasPrefix(p, "/api/") }

// thaiStatusText คืนข้อความไทยตามรหัสสถานะ โดยไม่เปิดเผยรายละเอียดภายใน
func thaiStatusText(status int) string {
	switch status {
	case fiber.StatusBadRequest:
		return "คำขอไม่ถูกต้อง"
	case fiber.StatusForbidden:
		return "ไม่อนุญาตให้ทำรายการนี้"
	case fiber.StatusNotFound:
		return "ไม่พบหน้าที่ต้องการ"
	case fiber.StatusMethodNotAllowed:
		return "ไม่รองรับวิธีเรียกนี้"
	case fiber.StatusRequestEntityTooLarge:
		return "ข้อมูลที่ส่งมามีขนาดใหญ่เกินไป"
	case fiber.StatusRequestTimeout:
		return "คำขอใช้เวลานานเกินไป"
	default:
		return "ระบบขัดข้อง ลองใหม่อีกครั้ง"
	}
}

func errorHTML(status int) string {
	msg := html.EscapeString(thaiStatusText(status))
	return fmt.Sprintf(`<!doctype html>
<html lang="th"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>เกิดข้อผิดพลาด (%d)</title>
<style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 1rem;color:#222}a{color:#0a58ca}</style>
</head><body><h1>%d</h1><p>%s</p><p><a href="/">กลับหน้าแรก</a></p></body></html>
`, status, status, msg)
}

func respondError(c *fiber.Ctx, status int) error {
	if isAPIPath(c.Path()) {
		return c.Status(status).JSON(fiber.Map{"error": thaiStatusText(status)})
	}
	c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
	return c.Status(status).SendString(errorHTML(status))
}

func errorHandler(c *fiber.Ctx, err error) error {
	status := fiber.StatusInternalServerError
	var fe *fiber.Error
	if errors.As(err, &fe) {
		status = fe.Code
	}
	// รายละเอียดจริงลง stdout เท่านั้น ไม่ส่งให้ผู้ใช้
	fmt.Printf("ข้อผิดพลาดตอนตอบคำขอ %s %s (สถานะ %d): %v\n", c.Method(), c.Path(), status, err)
	return respondError(c, status)
}

// buildApp ประกอบ Fiber app ตามลำดับ:
//  1. recover  2. SameOrigin (เฉพาะที่ไม่ใช่ /api)  3. /api (RequireAPIKey → api.Register)
//  4. เส้นทางสาธารณะ (/static/*, /login)  5. กลุ่มที่ต้องล็อกอิน  6. 404 ท้ายสุด
//
// หมายเหตุ: Group("/", RequireLogin) ครอบทุกเส้นทางที่ลงทะเบียนหลังมัน (รวม 404)
// จึงต้องลงทะเบียน /api และเส้นทางสาธารณะก่อนเสมอ; คำขอไม่ล็อกอินที่ไปเส้นทางไม่มีจริงจะถูก
// ส่งไป /login (ไม่เผยว่ามีหน้าอะไรบ้าง)
func buildApp(d appDeps) (*fiber.App, error) {
	app := fiber.New(fiber.Config{
		AppName:      "zkscan",
		ServerHeader: "",
		BodyLimit:    1 << 20,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 10 * time.Minute, // ดึงทุกเครื่องผ่านหน้าเว็บอาจใช้เวลานาน
		IdleTimeout:  120 * time.Second,
		ErrorHandler: errorHandler,
		// ไม่ตั้ง ProxyHeader: ใช้ IP จริงของ socket เท่านั้น เพราะ IP ถูกใช้จำกัดกุญแจ API
		// และกันเดารหัส ถ้าเชื่อ X-Forwarded-For ผู้โจมตีปลอม IP ข้ามข้อจำกัดได้
		DisableStartupMessage: true,
	})

	app.Use(recover.New())
	same := auth.SameOrigin()
	app.Use(func(c *fiber.Ctx) error {
		if isAPIPath(c.Path()) {
			return c.Next()
		}
		return same(c)
	})

	// Fiber จับคู่ prefix ของ Group แบบไม่สนขอบเขตของ segment: "/api" จะจับ "/api-keys" ด้วย
	// จึงต้องเช็ก isAPIPath เองก่อนบังคับกุญแจ ไม่งั้นหน้า /api-keys ของผู้ดูแลจะโดนขอกุญแจ API
	requireKey := auth.RequireAPIKey(d.Auth)
	apiGroup := app.Group("/api", func(c *fiber.Ctx) error {
		if !isAPIPath(c.Path()) {
			return c.Next()
		}
		return requireKey(c)
	})
	api.Register(apiGroup, api.Deps{Store: d.Store, Puller: d.Puller})

	w, err := web.New(web.Deps{Store: d.Store, Auth: d.Auth, Puller: d.Puller, Client: d.Client, Version: d.Version})
	if err != nil {
		return nil, err
	}
	w.RegisterPublic(app)
	protected := app.Group("/", auth.RequireLogin(d.Auth))
	w.RegisterProtected(protected)

	app.Use(func(c *fiber.Ctx) error { return respondError(c, fiber.StatusNotFound) })
	return app, nil
}
