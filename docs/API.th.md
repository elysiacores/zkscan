# คู่มือ API ของ zkscan (สำหรับนักพัฒนาระบบ HR)

เอกสารนี้อธิบายวิธีเรียก API ของ zkscan เพื่อดึงบันทึกเวลาเข้า-ออกงานและรายชื่อพนักงานไปใช้ในระบบอื่น
ทุกอย่างในเอกสารนี้ตรวจกับโค้ดของโปรแกรมแล้ว (`internal/api/api.go`, `internal/auth/middleware.go`, `internal/core`)

ผู้ดูแลระบบ (IT) จะเป็นคนสร้างกุญแจ API ให้คุณ แล้วส่ง **URL ของ server + กุญแจ** มา
วิธีสร้างกุญแจอยู่ใน [คู่มือหลัก](MANUAL.th.md) หัวข้อ "ให้ระบบ HR ดึงข้อมูล"

## สารบัญ

1. [ข้อมูลพื้นฐาน](#1-ข้อมูลพื้นฐาน)
2. [การยืนยันตัวตน (กุญแจ API)](#2-การยืนยันตัวตน-กุญแจ-api)
3. [รูปแบบข้อผิดพลาด](#3-รูปแบบข้อผิดพลาด)
4. [Endpoint ทั้งหมด](#4-endpoint-ทั้งหมด)
5. [ตารางรหัส verify_mode และ punch_state](#5-ตารางรหัส-verify_mode-และ-punch_state)
6. [การแบ่งหน้า (limit / offset / total)](#6-การแบ่งหน้า-limit--offset--total)
7. [แนวทางดึงข้อมูลซ้ำ ๆ (polling) ที่แนะนำ](#7-แนวทางดึงข้อมูลซ้ำ-ๆ-polling-ที่แนะนำ)
8. [ตัวอย่างโค้ด](#8-ตัวอย่างโค้ด)

---

## 1. ข้อมูลพื้นฐาน

| หัวข้อ | ค่า |
|---|---|
| Base URL | `http://<IP ของ server>:6565` (หรือ `https://...` ถ้าผู้ดูแลเปิด HTTPS) |
| เส้นทางของ API | ขึ้นต้นด้วย `/api/` เช่น `http://192.168.1.50:6565/api/health` |
| รูปแบบข้อมูล | JSON (UTF-8) ทุก endpoint |
| วิธีเรียก | `GET` สำหรับอ่านข้อมูล, `POST` สำหรับสั่งดึงข้อมูล (`/api/pull`) |
| รูปแบบวันเวลา | `YYYY-MM-DD HH:MM:SS` เช่น `2026-11-05 08:17:27` (ไม่มีโซนเวลาต่อท้าย) |
| รูปแบบวันที่ (ตัวกรอง) | `YYYY-MM-DD` เช่น `2026-11-05` |

ข้อควรรู้เรื่องเวลา:

- `timestamp` ของบันทึกเวลา คือ **เวลาตามนาฬิกาของเครื่องสแกน** (ไม่มีโซนเวลา) ถ้านาฬิกาเครื่องสแกนเดินผิด เวลาใน API ก็ผิดตามไปด้วย
  (ผู้ดูแลตั้งเวลาเครื่องให้ตรงกับ server ได้จากหน้าเว็บ)
- ค่าเวลาอื่น เช่น `last_pull`, `last_pull_at`, `at` เป็นเวลาตามเขตเวลาของ server
- ตัวกรอง `from` / `to` เทียบกับเวลาสแกน (`timestamp`) โดย `from` เริ่มที่ 00:00:00 ของวันนั้น และ `to` รวมถึง 23:59:59 ของวันนั้น (รวมทั้งวัน)
- ผลลัพธ์ `/api/attendance` เรียง **ใหม่สุดก่อน**

โปรแกรมไม่ได้ตั้ง CORS ไว้ API นี้ออกแบบให้ระบบหลังบ้าน (server ต่อ server) เรียก ไม่ใช่ให้เบราว์เซอร์ของผู้ใช้เรียกตรง ๆ

## 2. การยืนยันตัวตน (กุญแจ API)

ทุก endpoint ต้องแนบกุญแจ ส่งได้ 2 แบบ (เลือกแบบเดียว):

```
Authorization: Bearer zk_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

หรือ

```
X-API-Key: zk_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

- กุญแจขึ้นต้นด้วย `zk_`
- ถ้ามีทั้งสอง header โปรแกรมใช้ `Authorization: Bearer` ก่อน
- ใส่กุญแจใน header เท่านั้น ห้ามใส่ใน URL (จะไปติดใน log ของเครื่องต่าง ๆ) และห้ามเก็บกุญแจไว้ในโค้ดที่เผยแพร่

### รหัสตอบกลับที่เกี่ยวกับกุญแจ

| รหัส | ความหมาย | ต้องทำอย่างไร |
|---|---|---|
| `401` | ไม่ได้แนบกุญแจ, หรือกุญแจผิด/ถูกยกเลิกแล้ว (มี header `WWW-Authenticate: Bearer`) | ตรวจว่าส่ง header ถูก และกุญแจยังใช้ได้ ถ้าถูกยกเลิก ให้ขอกุญแจใหม่จากผู้ดูแล |
| `403` | กุญแจถูกต้อง แต่ **IP ของผู้เรียกไม่อยู่ในรายการ IP ที่อนุญาต** ของกุญแจนั้น | แจ้งผู้ดูแลให้เพิ่ม IP ของ server HR ในกุญแจ |

### เรื่อง IP ที่อนุญาต (allow-list)

- ผู้ดูแลจะจำกัดกุญแจให้ใช้ได้เฉพาะบาง IP ได้ (IP เดี่ยวหรือช่วง เช่น `192.168.1.10`, `192.168.1.0/24`) ถ้าไม่จำกัดจะใช้ได้จากทุก IP
- zkscan ตรวจจาก **IP ปลายทางของการเชื่อมต่อจริง (socket)** เท่านั้น **ไม่เชื่อ** header `X-Forwarded-For`
  ดังนั้นถ้าคุณเรียกผ่าน proxy / load balancer / NAT zkscan จะเห็นเป็น IP ของ proxy หรือ NAT ไม่ใช่ IP ของเครื่องคุณ
  ให้บอกผู้ดูแลว่า IP ที่ใช้เรียกจริงเป็นตัวไหน (ถ้าไม่แน่ใจ ให้ลองเรียกแล้วให้ผู้ดูแลเปิดเมนู "บันทึกการทำงาน" > "เข้าสู่ระบบ/ความปลอดภัย" ซึ่งจะมีบรรทัดเตือน "ถูกปฏิเสธ: IP ... ไม่อยู่ในรายการที่อนุญาต" บอก IP ที่ zkscan เห็นจริง)

## 3. รูปแบบข้อผิดพลาด

ข้อผิดพลาดทั้งหมดตอบเป็น JSON รูปแบบเดียวกัน:

```json
{"error": "ข้อความอธิบาย"}
```

| รหัส | เมื่อไหร่ |
|---|---|
| `400` | พารามิเตอร์ไม่ถูกต้อง (วันที่ผิดรูปแบบ, ตัวเลขผิด, `limit` นอกช่วง, `offset` ติดลบ, `device_id` ไม่ใช่เลขจำนวนเต็มบวก) |
| `401` | ไม่มีกุญแจ หรือกุญแจไม่ถูกต้อง/ถูกยกเลิก |
| `403` | IP ไม่อยู่ในรายการที่อนุญาต |
| `404` | ไม่มี endpoint นั้น (หรือใช้ method ผิด) หรือ `device_id` ที่ระบุใน `/api/pull` ไม่มีอยู่ |
| `409` | `/api/pull` ถูกเรียกขณะที่ระบบกำลังดึงข้อมูลอยู่ ให้รอสักครู่แล้วลองใหม่ |
| `500` | ระบบขัดข้องภายใน ลองใหม่ ถ้ายังเป็นอยู่ให้แจ้งผู้ดูแล |

ตัวอย่าง:

```json
{"error": "invalid date for 'from', expected YYYY-MM-DD"}
```

```json
{"error": "invalid value for 'limit', expected 1..50000"}
```

```json
{"error": "กุญแจ API ไม่ถูกต้องหรือถูกยกเลิกแล้ว"}
```

หมายเหตุ: ข้อความใน `error` บางข้อเป็นภาษาอังกฤษ บางข้อเป็นภาษาไทย และอาจเปลี่ยนตามเวอร์ชัน
**ให้ตัดสินใจจากรหัส HTTP (status code) ไม่ใช่จากข้อความ**

## 4. Endpoint ทั้งหมด

| Endpoint | ใช้ทำอะไร |
|---|---|
| `GET /api/health` | เช็กว่าระบบทำงาน + จำนวนข้อมูลรวม |
| `GET /api/devices` | รายชื่อเครื่องสแกนและสถานะการดึงล่าสุด |
| `GET /api/attendance` | บันทึกเวลาเข้า-ออก (กรองและแบ่งหน้าได้) |
| `GET /api/users` | พนักงานที่ลงทะเบียนในเครื่องสแกน |
| `POST /api/pull` | สั่งให้ดึงข้อมูลจากเครื่องสแกนเดี๋ยวนี้ |

ในตัวอย่าง curl ด้านล่างสมมติว่าตั้งตัวแปรไว้ก่อน:

```bash
export ZK=http://192.168.1.50:6565
export KEY=zk_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

### 4.1 `GET /api/health`

ไม่มีพารามิเตอร์ ใช้เช็กว่า zkscan ทำงานและกุญแจใช้ได้

```bash
curl -H "Authorization: Bearer $KEY" "$ZK/api/health"
```

```json
{
  "status": "ok",
  "records": 258,
  "users": 12,
  "last_pull": "2026-11-05 08:39:54"
}
```

| ฟิลด์ | ชนิด | ความหมาย |
|---|---|---|
| `status` | string | `"ok"` เสมอเมื่อระบบตอบได้ |
| `records` | number | จำนวนบันทึกเวลาทั้งหมดที่เก็บไว้ |
| `users` | number | จำนวนพนักงานในเครื่องสแกน (รวมทุกเครื่อง) |
| `last_pull` | string หรือ `null` | เวลาที่ดึงสำเร็จล่าสุด (`null` ถ้ายังไม่เคยดึงสำเร็จ) |

### 4.2 `GET /api/devices`

ไม่มีพารามิเตอร์ ตอบเป็น **อาร์เรย์** (ไม่ห่อด้วย object) **ไม่แสดง Comm Key**

```bash
curl -H "Authorization: Bearer $KEY" "$ZK/api/devices"
```

```json
[
  {
    "id": 1,
    "name": "เครื่องหน้าออฟฟิศ",
    "ip": "192.168.1.201",
    "port": 4370,
    "transport": "tcp",
    "demo": false,
    "enabled": true,
    "last_pull_at": "2026-11-05 08:39:54",
    "last_ok": true,
    "last_error": null
  },
  {
    "id": 2,
    "name": "เครื่องหลังโรงงาน",
    "ip": "192.168.1.250",
    "port": 4370,
    "transport": "tcp",
    "demo": false,
    "enabled": true,
    "last_pull_at": "2026-11-05 08:39:54",
    "last_ok": false,
    "last_error": "เชื่อมต่อไม่ได้: dial tcp 192.168.1.250:4370: connect: no route to host"
  }
]
```

| ฟิลด์ | ชนิด | ความหมาย |
|---|---|---|
| `id` | number | รหัสเครื่อง (ใช้เป็น `device_id` ใน endpoint อื่น) |
| `name` | string | ชื่อเครื่องที่ผู้ดูแลตั้ง (ไม่ซ้ำกัน) |
| `ip` | string | IP หรือชื่อโฮสต์ของเครื่องสแกน |
| `port` | number | พอร์ต (ปกติ 4370) |
| `transport` | string | `"tcp"` หรือ `"udp"` |
| `demo` | boolean | `true` = เครื่องสาธิต (ข้อมูลจำลอง) ไม่ใช่เครื่องจริง |
| `enabled` | boolean | `false` = ปิดใช้งาน ไม่ดึงตามรอบอัตโนมัติ |
| `last_pull_at` | string หรือ `null` | เวลาที่พยายามดึงล่าสุด (`null` ถ้ายังไม่เคยดึง) |
| `last_ok` | boolean หรือ `null` | ผลดึงล่าสุดสำเร็จหรือไม่ (`null` ถ้ายังไม่เคยดึง) |
| `last_error` | string หรือ `null` | ข้อความผิดพลาดของการดึงล่าสุด (`null` ถ้าไม่มี) |

### 4.3 `GET /api/attendance`

บันทึกเวลาเข้า-ออก ตัวกรองทุกตัว **ไม่บังคับ** และใช้ร่วมกันได้ (เงื่อนไข "และ")

| พารามิเตอร์ | ชนิด | ความหมาย |
|---|---|---|
| `from` | `YYYY-MM-DD` | เริ่มตั้งแต่วันที่นี้ (00:00:00 เป็นต้นไป) |
| `to` | `YYYY-MM-DD` | ถึงวันที่นี้ (รวมทั้งวัน ถึง 23:59:59) |
| `user_id` | string | รหัสพนักงาน **ตรงทั้งตัว** (เช่น `1001`) |
| `device` | string | ชื่อเครื่อง **ตรงทั้งตัว** (เช่น `เครื่องหน้าออฟฟิศ` ถ้าใส่ใน URL ต้อง URL-encode) |
| `device_id` | จำนวนเต็ม > 0 | รหัสเครื่อง (ดูจาก `/api/devices`) |
| `limit` | จำนวนเต็ม | จำนวนรายการต่อหน้า ค่าเริ่มต้น `5000` ต่ำสุด `1` สูงสุด `50000` |
| `offset` | จำนวนเต็ม >= 0 | ข้ามกี่รายการ ค่าเริ่มต้น `0` |

```bash
# ทุกเครื่อง ตั้งแต่ 1 ถึง 5 พ.ย. 2026
curl -H "Authorization: Bearer $KEY" "$ZK/api/attendance?from=2026-11-01&to=2026-11-05"

# เฉพาะพนักงานรหัส 1001 จากเครื่องรหัส 1 แบ่งหน้าทีละ 100
curl -H "X-API-Key: $KEY" "$ZK/api/attendance?user_id=1001&device_id=1&limit=100&offset=0"
```

```json
{
  "count": 3,
  "total": 258,
  "records": [
    {
      "device": "เครื่องหน้าออฟฟิศ",
      "user_id": "1007",
      "name": "ธนา พูนผล",
      "timestamp": "2026-11-05 08:17:27",
      "verify_mode": 1,
      "punch_state": 0
    },
    {
      "device": "เครื่องหน้าออฟฟิศ",
      "user_id": "1005",
      "name": "ประเสริฐ ทองดี",
      "timestamp": "2026-11-05 08:16:53",
      "verify_mode": 1,
      "punch_state": 0
    },
    {
      "device": "เครื่องหน้าออฟฟิศ",
      "user_id": "1012",
      "name": "อนันต์ สุขสันต์",
      "timestamp": "2026-11-05 08:10:59",
      "verify_mode": 1,
      "punch_state": 0
    }
  ]
}
```

| ฟิลด์ | ชนิด | ความหมาย |
|---|---|---|
| `count` | number | จำนวนรายการใน `records` ของหน้านี้ |
| `total` | number | จำนวนรายการทั้งหมดที่ตรงตัวกรอง (ไม่นับ `limit`/`offset`) |
| `records[].device` | string | ชื่อเครื่องที่สแกน |
| `records[].user_id` | string | รหัสพนักงาน (เป็น **string** เสมอ เช่น `"1007"` เพราะอาจมีเลขศูนย์นำหน้า) |
| `records[].name` | string | ชื่อพนักงานตามที่ลงทะเบียนในเครื่อง เป็น `""` (สตริงว่าง) ถ้าไม่ทราบชื่อ |
| `records[].timestamp` | string | เวลาสแกนตามนาฬิกาเครื่อง `YYYY-MM-DD HH:MM:SS` |
| `records[].verify_mode` | number | วิธียืนยันตัวตน ดูตารางหัวข้อ 5 |
| `records[].punch_state` | number | สถานะการลงเวลา ดูตารางหัวข้อ 5 |

ไม่มีรหัสประจำรายการ (id) ใน JSON ถ้าต้องการกันข้อมูลซ้ำ ให้ใช้ชุด `device` + `user_id` + `timestamp` เป็นกุญแจ (ดูหัวข้อ 7)

ถ้าไม่มีข้อมูลตรงตัวกรอง จะได้ `{"count":0,"total":0,"records":[]}`

### 4.4 `GET /api/users`

พนักงานที่ลงทะเบียนในเครื่องสแกน เรียงตามชื่อเครื่อง แล้วตาม `uid`

| พารามิเตอร์ | ชนิด | ความหมาย |
|---|---|---|
| `device` | string | ชื่อเครื่อง ตรงทั้งตัว (ถ้าไม่มีเครื่องชื่อนี้ จะได้รายการว่าง) |
| `device_id` | จำนวนเต็ม > 0 | รหัสเครื่อง |

```bash
curl -H "Authorization: Bearer $KEY" "$ZK/api/users"
curl -H "Authorization: Bearer $KEY" "$ZK/api/users?device_id=1"
```

```json
{
  "count": 2,
  "users": [
    {
      "device": "เครื่องหน้าออฟฟิศ",
      "uid": 1,
      "user_id": "1001",
      "name": "สมชาย ใจดี",
      "privilege": 14,
      "card": 84642880
    },
    {
      "device": "เครื่องหน้าออฟฟิศ",
      "uid": 2,
      "user_id": "1002",
      "name": "สุดา รักงาน",
      "privilege": 0,
      "card": 80653602
    }
  ]
}
```

| ฟิลด์ | ชนิด | ความหมาย |
|---|---|---|
| `count` | number | จำนวนรายการใน `users` |
| `users[].device` | string | ชื่อเครื่องที่พนักงานคนนี้ลงทะเบียนไว้ |
| `users[].uid` | number | ลำดับภายในเครื่อง (เลขภายในของเครื่องสแกน ไม่ใช่รหัสพนักงาน) |
| `users[].user_id` | string | รหัสพนักงาน (ตัวเดียวกับ `user_id` ใน `/api/attendance`) |
| `users[].name` | string | ชื่อ |
| `users[].privilege` | number | สิทธิ์ในเครื่อง: `0` = ผู้ใช้ทั่วไป, `14` = ผู้ดูแลเครื่อง |
| `users[].card` | number | เลขบัตร (`0` = ไม่มีบัตร) |

endpoint นี้ **ไม่แบ่งหน้า** ตอบทั้งหมดในครั้งเดียว
พนักงานคนเดียวกันที่ลงทะเบียนในหลายเครื่องจะปรากฏหลายแถว (หนึ่งแถวต่อเครื่อง)

### 4.5 `POST /api/pull`

สั่งให้ zkscan ดึงข้อมูลจากเครื่องสแกนเดี๋ยวนี้ (ไม่ต้องรอรอบอัตโนมัติ) และ **รอจนดึงเสร็จ** แล้วจึงตอบ
อาจใช้เวลาหลายวินาที ถึงหลายนาทีถ้าเครื่องสแกนไม่ตอบ จึงควรตั้ง timeout ฝั่งผู้เรียกให้พอ (เช่น 5 นาที)

| พารามิเตอร์ | ชนิด | ความหมาย |
|---|---|---|
| `device_id` | จำนวนเต็ม > 0 | ดึงเฉพาะเครื่องนี้ ถ้าไม่ใส่ = ดึงทุกเครื่อง |

ไม่ต้องส่ง body

```bash
curl -X POST -H "Authorization: Bearer $KEY" "$ZK/api/pull"
curl -X POST -H "Authorization: Bearer $KEY" "$ZK/api/pull?device_id=1"
```

```json
{
  "results": [
    {
      "device_id": 1,
      "device": "เครื่องหน้าออฟฟิศ",
      "ok": true,
      "new_records": 258,
      "total_users": 12,
      "error": null,
      "hint": null,
      "duration_ms": 412,
      "at": "2026-11-05 08:39:54"
    },
    {
      "device_id": 2,
      "device": "เครื่องหลังโรงงาน",
      "ok": false,
      "new_records": 0,
      "total_users": 0,
      "error": "เชื่อมต่อไม่ได้: dial tcp 192.168.1.250:4370: connect: no route to host",
      "hint": "หาเครื่องไม่เจอ: ตรวจเลข IP ในเมนูเครื่อง (การสื่อสาร › อีเทอร์เน็ต) และให้คอมพิวเตอร์อยู่ในวงแลนเดียวกับเครื่องสแกน",
      "duration_ms": 3,
      "at": "2026-11-05 08:39:54"
    }
  ]
}
```

| ฟิลด์ | ชนิด | ความหมาย |
|---|---|---|
| `results[].device_id` | number | รหัสเครื่อง |
| `results[].device` | string | ชื่อเครื่อง |
| `results[].ok` | boolean | ดึงสำเร็จหรือไม่ |
| `results[].new_records` | number | จำนวนบันทึกเวลา **ใหม่** ที่เพิ่งเก็บ (ไม่นับที่เคยเก็บแล้ว) |
| `results[].total_users` | number | จำนวนพนักงานที่อ่านได้จากเครื่อง |
| `results[].error` | string หรือ `null` | ข้อความผิดพลาด (ภาษาไทย) เมื่อ `ok` เป็น `false` |
| `results[].hint` | string หรือ `null` | คำแนะนำวิธีแก้ (ภาษาไทย) |
| `results[].duration_ms` | number | เวลาที่ใช้ (มิลลิวินาที) |
| `results[].at` | string | เวลาที่เริ่มดึง |

หมายเหตุสำคัญ:

- การที่บางเครื่องดึงไม่สำเร็จ (`ok: false`) **ไม่ได้ทำให้ HTTP status เปลี่ยน** ยังตอบ `200` ให้ตรวจ `ok` ในแต่ละรายการเอง
- ถ้ามีการดึงข้อมูลอยู่แล้ว (เช่น รอบอัตโนมัติกำลังทำงาน) จะตอบ `409` พร้อม `{"error":"กำลังดึงข้อมูลอยู่ ลองใหม่อีกครั้ง"}`
- `device_id` ที่ไม่มีอยู่จริง ตอบ `404`
- ไม่จำเป็นต้องเรียก endpoint นี้เป็นประจำ เพราะ zkscan ดึงเองตามรอบ (ค่าเริ่มต้นทุก 5 นาที ผู้ดูแลปรับได้) ใช้เมื่อต้องการข้อมูลล่าสุดทันทีเท่านั้น

## 5. ตารางรหัส verify_mode และ punch_state

ค่าตัวเลขมาจากเครื่องสแกนโดยตรง ตารางนี้คือความหมายที่ zkscan แสดงในหน้าเว็บและไฟล์ CSV

### `verify_mode` (วิธียืนยันตัวตน)

| รหัส | ความหมาย |
|---|---|
| `0` | รหัสผ่าน |
| `1` | ลายนิ้วมือ |
| `2` | บัตร |
| `15` | ใบหน้า |
| ค่าอื่น | อื่น ๆ (หน้าเว็บแสดงว่า "อื่น ๆ" แต่ API ส่งตัวเลขจริงจากเครื่องมาให้) |

### `punch_state` (สถานะการลงเวลา)

| รหัส | ความหมาย |
|---|---|
| `0` | เข้างาน |
| `1` | ออกงาน |
| `2` | พักออก |
| `3` | พักเข้า |
| `4` | OT เข้า |
| `5` | OT ออก |
| ค่าอื่น | ไม่ระบุ |

ข้อควรระวัง: สถานะ (เข้า/ออก) มาจากปุ่มที่พนักงานกดหรือการตั้งค่าของเครื่องสแกนเอง
เครื่องหลายเครื่องตั้งให้ส่ง `punch_state` เป็น `0` ทุกครั้ง การสรุปว่าเป็น "เข้า" หรือ "ออก" ในระบบ HR จึงควรดูเวลาประกอบด้วย ไม่ควรเชื่อ `punch_state` อย่างเดียว

## 6. การแบ่งหน้า (limit / offset / total)

`/api/attendance` แบ่งหน้าด้วย `limit` และ `offset` (ไม่มี cursor)

- `limit` ค่าเริ่มต้น 5000 สูงสุด 50000
- `total` ในคำตอบคือจำนวนทั้งหมดที่ตรงตัวกรอง (ไม่ขึ้นกับ `limit`/`offset`)
- ดึงหน้าถัดไปโดยเพิ่ม `offset` ทีละ `count` จนกว่า `offset >= total` หรือ `count` เป็น 0

ตัวอย่าง: มี 12,000 รายการ ใช้ `limit=5000`

| คำขอ | ได้ |
|---|---|
| `?limit=5000&offset=0` | `count=5000`, `total=12000` |
| `?limit=5000&offset=5000` | `count=5000`, `total=12000` |
| `?limit=5000&offset=10000` | `count=2000`, `total=12000` (หน้าสุดท้าย) |

ผลเรียงใหม่สุดก่อน ถ้ามีบันทึกใหม่เข้ามาระหว่างที่คุณกำลังไล่หน้า รายการจะเลื่อนลง ทำให้อาจเห็นรายการซ้ำข้ามหน้า
ซึ่งไม่เป็นปัญหาถ้าคุณกันข้อมูลซ้ำตามหัวข้อ 7 (และควรเลือกช่วงวันที่ให้เล็กพอที่จะดึงได้ในไม่กี่หน้า)

## 7. แนวทางดึงข้อมูลซ้ำ ๆ (polling) ที่แนะนำ

zkscan ดึงข้อมูลจากเครื่องสแกนเองทุก ๆ 5 นาที (ค่าเริ่มต้น) ระบบ HR จึงแค่ "มาเก็บ" จาก zkscan เป็นระยะ

1. **ทุก 5-15 นาที** เรียก `GET /api/attendance?from=<เมื่อวาน>` โดย `<เมื่อวาน>` เป็นวันที่รูปแบบ `YYYY-MM-DD`
   (ไม่ต้องใส่ `to`) และไล่หน้าด้วย `limit`/`offset` จนครบ `total`
2. **กันข้อมูลซ้ำ (dedupe)** ฝั่ง HR ด้วยชุด `device` + `user_id` + `timestamp`
   ในฐานข้อมูลของ zkscan ชุดนี้ไม่ซ้ำกันอยู่แล้ว ฝั่ง HR ใช้เป็นกุญแจเฉพาะ (unique key) ได้ และ insert แบบ "ถ้ามีแล้วให้ข้าม"
3. ทำไมต้องย้อนไป "เมื่อวาน" ไม่ใช่แค่วันนี้: ช่วงเปลี่ยนวัน, เวลาเครื่องสแกนไม่ตรง server เล็กน้อย, หรือเครื่องที่ดึงไม่สำเร็จชั่วคราวแล้วค่อยดึงได้ทีหลัง
   จะมีรายการที่มาถึง zkscan ช้ากว่าเวลาสแกน การย้อนวันกันไว้ทำให้ไม่ตกหล่น
4. **หลังระบบ HR หรือ zkscan หยุดไปนาน** ให้ย้อน `from` ไปไกลกว่าเดิม (เช่น ตั้งแต่วันสุดท้ายที่ดึงสำเร็จ) ครั้งหนึ่งก่อนกลับมาใช้ `from=เมื่อวาน`
5. **ตรวจสุขภาพของฝั่งที่ส่งข้อมูล**: เรียก `GET /api/devices` ดู `last_ok` / `last_error` ของแต่ละเครื่อง
   ถ้า `last_ok` เป็น `false` หรือ `last_pull_at` เก่ากว่าที่คาด แปลว่าเครื่องสแกนนั้นดึงไม่ได้ ควรแจ้งเตือนผู้ดูแล (ข้อมูลยังเก่าเท่ากับที่เคยดึงมา)
6. ไม่ต้องเรียกถี่กว่า 1 นาที ข้อมูลใหม่เข้ามาตามรอบดึงของ zkscan (ค่าเริ่มต้น 5 นาที) เท่านั้น ยกเว้นเรียก `POST /api/pull` เอง
7. ให้เก็บ `total` ไว้เทียบ ถ้าคำขอแต่ละหน้าได้ `count` ไม่ตรงที่คาด ให้ลองใหม่ อย่าเขียนข้อมูลครึ่ง ๆ กลาง ๆ (ใช้ transaction ฝั่ง HR)

## 8. ตัวอย่างโค้ด

ทุกตัวอย่างใช้ค่าสมมติ `BASE = http://192.168.1.50:6565` และ `KEY = zk_...` ให้เปลี่ยนเป็นของจริง
และเก็บกุญแจในที่ปลอดภัย (ตัวแปรสภาพแวดล้อม / ที่เก็บความลับ) ไม่ฝังในซอร์สโค้ด

### 8.1 curl

```bash
export ZK=http://192.168.1.50:6565
export KEY=zk_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx

# เช็กระบบ
curl -sS -H "Authorization: Bearer $KEY" "$ZK/api/health"

# บันทึกเวลาตั้งแต่เมื่อวาน (GNU date บน Linux)
curl -sS -H "Authorization: Bearer $KEY" "$ZK/api/attendance?from=$(date -d yesterday +%F)&limit=1000"

# ถ้าใช้ HTTPS ที่ใบรับรองเป็น self-signed ของผู้ดูแล และยังไม่ได้ติดตั้งใบรับรองบนเครื่องคุณ
# (ใช้ทดสอบเท่านั้น ใน production ควรติดตั้งใบรับรองให้เชื่อถือแทน)
curl -sS -k -H "Authorization: Bearer $KEY" "https://192.168.1.50:6565/api/health"
```

### 8.2 Python (ไลบรารี `requests`: `pip install requests`)

ดึงบันทึกเวลาตั้งแต่เมื่อวาน ไล่ทุกหน้า และกันข้อมูลซ้ำ

```python
import datetime as dt
import requests

BASE = "http://192.168.1.50:6565"
KEY = "zk_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"  # เก็บใน env/secret จริง ๆ

def fetch_attendance(from_day: str, page_size: int = 1000):
    s = requests.Session()
    s.headers["Authorization"] = f"Bearer {KEY}"
    offset = 0
    while True:
        r = s.get(
            f"{BASE}/api/attendance",
            params={"from": from_day, "limit": page_size, "offset": offset},
            timeout=30,
        )
        r.raise_for_status()          # 401/403/400 ฯลฯ จะ raise ตรงนี้
        data = r.json()
        yield from data["records"]
        offset += data["count"]
        if data["count"] == 0 or offset >= data["total"]:
            break

yesterday = (dt.date.today() - dt.timedelta(days=1)).isoformat()   # YYYY-MM-DD
seen = set()   # ในงานจริงใช้ unique key ในฐานข้อมูลแทน
for rec in fetch_attendance(yesterday):
    key = (rec["device"], rec["user_id"], rec["timestamp"])
    if key in seen:
        continue
    seen.add(key)
    print(rec["timestamp"], rec["user_id"], rec["name"], rec["punch_state"])
```

### 8.3 C# / .NET 8 (`HttpClient` + `System.Text.Json`)

```csharp
using System.Net.Http.Headers;
using System.Net.Http.Json;
using System.Text.Json.Serialization;

const string BaseUrl = "http://192.168.1.50:6565";
string key = Environment.GetEnvironmentVariable("ZKSCAN_KEY")!;   // zk_...

using var http = new HttpClient { BaseAddress = new Uri(BaseUrl), Timeout = TimeSpan.FromSeconds(30) };
http.DefaultRequestHeaders.Authorization = new AuthenticationHeaderValue("Bearer", key);

string yesterday = DateTime.Today.AddDays(-1).ToString("yyyy-MM-dd");
var seen = new HashSet<(string Device, string UserId, string Timestamp)>();
int offset = 0;

while (true)
{
    var page = await http.GetFromJsonAsync<AttendancePage>(
        $"/api/attendance?from={yesterday}&limit=1000&offset={offset}")
        ?? throw new InvalidOperationException("ตอบกลับว่าง");

    foreach (var r in page.Records)
    {
        if (!seen.Add((r.Device, r.UserId, r.Timestamp))) continue;   // ซ้ำ ข้าม
        Console.WriteLine($"{r.Timestamp} {r.UserId} {r.Name} punch={r.PunchState}");
    }

    offset += page.Count;
    if (page.Count == 0 || offset >= page.Total) break;
}

// GetFromJsonAsync จะโยน HttpRequestException เมื่อได้ 401/403/400 (ดู e.StatusCode)

record AttendancePage(
    [property: JsonPropertyName("count")] int Count,
    [property: JsonPropertyName("total")] int Total,
    [property: JsonPropertyName("records")] List<AttendanceRecord> Records);

record AttendanceRecord(
    [property: JsonPropertyName("device")] string Device,
    [property: JsonPropertyName("user_id")] string UserId,
    [property: JsonPropertyName("name")] string Name,
    [property: JsonPropertyName("timestamp")] string Timestamp,
    [property: JsonPropertyName("verify_mode")] int VerifyMode,
    [property: JsonPropertyName("punch_state")] int PunchState);
```

เวลา `Timestamp` ควรเก็บเป็นสตริงหรือแปลงด้วย `DateTime.ParseExact(s, "yyyy-MM-dd HH:mm:ss", CultureInfo.InvariantCulture)`
(เป็นเวลาท้องถิ่นของเครื่องสแกน ไม่มีโซนเวลา อย่าแปลงเป็น UTC เองโดยไม่ตั้งใจ)

---

มีปัญหาการเรียก API? ดูหัวข้อ "แก้ปัญหาที่พบบ่อย" ใน [คู่มือหลัก](MANUAL.th.md) หรือให้ผู้ดูแลเปิดเมนู "บันทึกการทำงาน" ในหน้าเว็บของ zkscan (ปุ่ม "เข้าสู่ระบบ/ความปลอดภัย" แสดงเหตุการณ์ของกุญแจ API เช่น ถูกปฏิเสธเพราะ IP)
