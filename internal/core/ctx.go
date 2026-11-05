package core

import "github.com/gofiber/fiber/v2"

const LocalsUser = "zkscan.user"     // core.User ที่ล็อกอินอยู่ (ใส่โดย auth middleware)
const LocalsToken = "zkscan.token"   // session token ปัจจุบัน (string)
const LocalsAPIKey = "zkscan.apikey" // core.APIKey (ใส่โดย API-key middleware)

func CurrentUser(c *fiber.Ctx) (User, bool) { u, ok := c.Locals(LocalsUser).(User); return u, ok }
func CurrentToken(c *fiber.Ctx) string      { t, _ := c.Locals(LocalsToken).(string); return t }

// VerifyModeLabel คืนป้ายไทยของวิธียืนยันตัวตน
func VerifyModeLabel(m int) string {
	switch m {
	case 0:
		return "รหัสผ่าน"
	case 1:
		return "ลายนิ้วมือ"
	case 2:
		return "บัตร"
	case 15:
		return "ใบหน้า"
	default:
		return "อื่น ๆ"
	}
}

// PunchStateLabel คืนป้ายไทยของสถานะการลงเวลา
func PunchStateLabel(p int) string {
	switch p {
	case 0:
		return "เข้างาน"
	case 1:
		return "ออกงาน"
	case 2:
		return "พักออก"
	case 3:
		return "พักเข้า"
	case 4:
		return "OT เข้า"
	case 5:
		return "OT ออก"
	default:
		return "ไม่ระบุ"
	}
}
