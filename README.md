# zkscan

บริการบน Linux Server ที่ **ดึงข้อมูลสแกนนิ้วจากเครื่อง ZKTeco เอง** เก็บไว้ในฐานข้อมูล แล้วเปิด **API** ให้ระบบอื่น (เช่น ระบบ HR) ดึงต่อ
ตั้งค่าทุกอย่างผ่านหน้าเว็บภาษาไทยที่ `http://<IP ของ server>:6565`

- ไฟล์โปรแกรมไฟล์เดียว ไม่ต้องติดตั้งฐานข้อมูลแยก (ใช้ SQLite ไฟล์เดียว)
- ดึงข้อมูลทุกเครื่องอัตโนมัติตามรอบ (ค่าเริ่มต้นทุก 5 นาที) และกด "ดึงข้อมูลเดี๋ยวนี้" ได้
- ไม่บันทึกซ้ำ ข้อมูลไม่หายเมื่อรีสตาร์ท
- หน้าเว็บต้องล็อกอิน, API ต้องใช้กุญแจ (API key) เสมอ

## ข้อควรรู้ก่อนเริ่ม: ต้องอยู่วงแลนเดียวกับเครื่องสแกน

Server ที่ติดตั้ง zkscan **ต้องเชื่อมต่อถึงเครื่องสแกนได้โดยตรงในเครือข่ายภายใน (LAN)** ผ่านพอร์ต **4370** (ค่าเริ่มต้นของ ZKTeco, TCP หรือ UDP)
ถ้า server อยู่บนคลาวด์ภายนอก จะมองไม่เห็นเครื่องสแกน ต้องต่อ VPN เข้าวงแลนของที่ทำงานก่อน

ลองเช็กจาก server ว่าถึงเครื่องสแกนไหม (แก้ IP ให้ตรงกับเครื่องของคุณ):

```bash
ping -c 3 192.168.1.201
nc -vz 192.168.1.201 4370      # ถ้าขึ้น "succeeded" แปลว่าต่อได้ (เครื่องที่ใช้ UDP อาจไม่ขึ้น ให้ลองกดทดสอบในหน้าเว็บแทน)
```

---

## ติดตั้ง

มี 2 วิธี เลือกวิธีใดวิธีหนึ่ง

### วิธีที่ 1: ติดตั้งเป็นบริการ (systemd) บน Linux

ต้องใช้ Linux ที่มี systemd (Ubuntu, Debian, Rocky, AlmaLinux ฯลฯ) สถาปัตยกรรม amd64 หรือ arm64 (เช่น Raspberry Pi 64 บิต)

```bash
git clone <ที่อยู่ repo ของคุณ> zkscan
cd zkscan
sudo ./install.sh
```

ตัวติดตั้งจะทำให้ทั้งหมดนี้ (รันซ้ำกี่ครั้งก็ปลอดภัย):

1. ตรวจ systemd และหาไฟล์โปรแกรม — ใช้ `dist/zkscan-linux-<arch>` ถ้ามี ไม่งั้นคอมไพล์จากซอร์สถ้าเครื่องมี Go 1.24 ขึ้นไป
   (ถ้าไม่มีทั้งสองอย่าง จะหยุดและบอกวิธีแก้ — ติดตั้ง Go หรือใช้ Docker แทน)
2. สร้างผู้ใช้ระบบ `zkscan` (ล็อกอินไม่ได้), โฟลเดอร์ข้อมูล `/var/lib/zkscan`, ไฟล์ตั้งค่า `/etc/zkscan/zkscan.env` (ถ้ายังไม่มี)
3. ติดตั้งโปรแกรมที่ `/usr/local/bin/zkscan`
4. สร้างบัญชีผู้ดูแล `admin` พร้อม **รหัสผ่านชั่วคราว** (แสดงบนหน้าจอครั้งเดียวตอนติดตั้ง)
5. ติดตั้งและเปิดบริการ `zkscan` (เริ่มเองทุกครั้งที่เปิดเครื่อง)
6. สรุปที่อยู่เว็บ เช่น `http://192.168.1.50:6565`

> ถ้าเครื่องไม่มี Go และไม่มีโฟลเดอร์ `dist/` ให้สร้างไฟล์โปรแกรมจากคอมพิวเตอร์ที่มี Go 1.24+ ด้วย `make dist`
> แล้วคัดลอกโฟลเดอร์ `dist/` ไปไว้ในโฟลเดอร์ `zkscan` บน server ก่อนรัน `sudo ./install.sh`

### วิธีที่ 2: Docker

```bash
git clone <ที่อยู่ repo ของคุณ> zkscan
cd zkscan
docker compose up -d --build
docker compose exec zkscan zkscan show-initial-password
```

บรรทัดสุดท้ายจะแสดงรหัสผ่านชั่วคราวของ `admin` ข้อมูลเก็บใน volume ชื่อ `zkscan-data` (ไม่หายเมื่อลบหรือสร้างคอนเทนเนอร์ใหม่)

**เรื่องเขตเวลา:** คอนเทนเนอร์ตั้ง `TZ=Asia/Bangkok` ไว้แล้วใน `docker-compose.yml` (โปรแกรมฝังข้อมูลเขตเวลาไว้ในตัว จึงใช้ได้แม้ image ไม่มี tzdata)
ถ้าต้องการเขตเวลาอื่น: `ZKSCAN_TZ=Asia/Tokyo docker compose up -d`
เวลาลงสแกนในรายการบันทึก *ใช้ตามนาฬิกาของเครื่องสแกน* ส่วนเวลาที่ server เป็นคนบันทึก (เช่น เวลาดึงล่าสุด, log) ใช้ตามเขตเวลานี้

> เครื่องสแกนอยู่ในวงแลนที่ Docker host ต่ออยู่ คอนเทนเนอร์จะเรียกออกไปหาเครื่องสแกนได้ตามปกติ (ผ่านเครือข่าย bridge ของ Docker)

---

## เข้าใช้งานครั้งแรก

1. เปิดเบราว์เซอร์ไปที่ `http://<IP ของ server>:6565`
2. ล็อกอินด้วยชื่อผู้ใช้ `admin` และ **รหัสผ่านชั่วคราว** ที่แสดงตอนติดตั้ง
3. ระบบจะบังคับให้ **ตั้งรหัสผ่านใหม่ทันที** (อย่างน้อย 8 ตัว) แล้วจึงเข้าหน้าหลักได้

ลืมจดรหัสชั่วคราว? ดูอีกครั้งได้ (ใช้ได้จนกว่าจะเปลี่ยนรหัสผ่านแล้ว):

```bash
# ติดตั้งแบบ systemd
sudo -u zkscan env ZKSCAN_DATA=/var/lib/zkscan /usr/local/bin/zkscan show-initial-password

# ติดตั้งแบบ Docker
docker compose exec zkscan zkscan show-initial-password
```

## เพิ่มเครื่องสแกน และ Comm Key

1. เมนู **เครื่องสแกน** > **เพิ่มเครื่อง**
2. กรอก ชื่อ (ตั้งเองได้ ห้ามซ้ำ), **IP** ของเครื่องสแกน, พอร์ต (ปกติ `4370`), **Comm Key**, ประเภทการเชื่อมต่อ TCP/UDP (ปกติ TCP)
3. กด **ทดสอบการเชื่อมต่อ** — ถ้าสำเร็จจะเห็นรุ่น เฟิร์มแวร์ จำนวนผู้ใช้ ถ้าไม่สำเร็จจะมีคำอธิบายและวิธีแก้เป็นภาษาไทย
4. กด **ดึงข้อมูล** หรือรอรอบอัตโนมัติ

**Comm Key** คือ "รหัสสื่อสาร" ของเครื่องสแกน ตัวเลข 0-999999 ตั้งที่เมนูของตัวเครื่อง (Menu > Comm. > Comm Key) ถ้าไม่เคยตั้งให้ใส่ `0`
ถ้าใส่ไม่ตรง เครื่องจะปฏิเสธการเชื่อมต่อ

อยากลองระบบก่อนต่อเครื่องจริง: ติ๊ก **โหมดสาธิต** จะได้ข้อมูลจำลอง (ไม่ใช้เครือข่าย)

## สร้างกุญแจ API

API ทุกเส้นต้องมีกุญแจ ไม่มีกุญแจ = ใช้ไม่ได้

1. เมนู **กุญแจ API** > **สร้างกุญแจใหม่** ตั้งชื่อตามระบบที่จะใช้ (เช่น "ระบบ HR")
2. (ไม่บังคับ) ใส่ IP ที่อนุญาตให้ใช้กุญแจนี้ได้
3. กุญแจเต็ม (ขึ้นต้นด้วย `zk_`) **แสดงครั้งเดียว** ให้กดคัดลอกเก็บไว้ทันที ถ้าหายต้องสร้างใหม่ (ยกเลิกกุญแจเก่าได้ในหน้าเดียวกัน)

ส่งกุญแจได้ 2 แบบ: `Authorization: Bearer <กุญแจ>` หรือ `X-API-Key: <กุญแจ>`

## ตัวอย่างเรียก API (curl)

ตั้งตัวแปรไว้ก่อนจะได้คัดลอกสะดวก:

```bash
export ZK=http://192.168.1.50:6565
export KEY=zk_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

| Endpoint | ใช้ทำอะไร |
|---|---|
| `GET /api/health` | เช็กว่าระบบทำงาน + จำนวนข้อมูล |
| `GET /api/devices` | รายชื่อเครื่องสแกนและสถานะดึงล่าสุด |
| `GET /api/attendance` | บันทึกเวลาเข้า-ออก (กรองได้) |
| `GET /api/users` | พนักงานที่ลงทะเบียนในเครื่อง |
| `POST /api/pull` | สั่งดึงข้อมูลเดี๋ยวนี้ |

**1. `GET /api/health`** — ตอบ `{"status":"ok","records":N,"users":N,"last_pull":"2026-11-05 09:30:00"}` (`last_pull` เป็น `null` ถ้ายังไม่เคยดึง)

```bash
curl -H "Authorization: Bearer $KEY" "$ZK/api/health"
```

**2. `GET /api/devices`** — ตอบรายการ `[{"id","name","ip","port","transport","demo","enabled","last_pull_at","last_ok","last_error"}]` (ไม่แสดง Comm Key)

```bash
curl -H "Authorization: Bearer $KEY" "$ZK/api/devices"
```

**3. `GET /api/attendance`** — ตัวกรอง (ใส่หรือไม่ใส่ก็ได้): `from`, `to` (รูปแบบ `YYYY-MM-DD` รวมทั้งวัน), `user_id`, `device` (ชื่อเครื่อง), `device_id`, `limit` (ค่าเริ่มต้น 5000, สูงสุด 50000), `offset`
ตอบ `{"count":n,"total":N,"records":[{"device","user_id","name","timestamp","verify_mode","punch_state"}]}`

```bash
# ทั้งหมดของวันที่ 1-5 พ.ย. 2026
curl -H "Authorization: Bearer $KEY" "$ZK/api/attendance?from=2026-11-01&to=2026-11-05"

# เฉพาะพนักงานรหัส 1001 จากเครื่องหนึ่ง แบ่งหน้าทีละ 100
curl -H "X-API-Key: $KEY" "$ZK/api/attendance?user_id=1001&device_id=1&limit=100&offset=0"
```

**4. `GET /api/users`** — ตัวกรอง: `device`, `device_id` ตอบ `{"count":n,"users":[{"device","uid","user_id","name","privilege","card"}]}`

```bash
curl -H "Authorization: Bearer $KEY" "$ZK/api/users"
curl -H "Authorization: Bearer $KEY" "$ZK/api/users?device_id=1"
```

**5. `POST /api/pull`** — ไม่ใส่ `device_id` = ดึงทุกเครื่อง ตอบ `{"results":[{"device_id","device","ok","new_records","total_users","error","hint","duration_ms","at"}]}`
ถ้ากำลังดึงอยู่ จะตอบ 409

```bash
curl -X POST -H "Authorization: Bearer $KEY" "$ZK/api/pull"
curl -X POST -H "Authorization: Bearer $KEY" "$ZK/api/pull?device_id=1"
```

ข้อผิดพลาดตอบเป็น JSON `{"error":"..."}` รหัส 400 (พารามิเตอร์ผิด), 401 (ไม่มี/กุญแจผิด), 403 (IP ไม่อยู่ในรายการที่อนุญาต), 404, 409
เวลาทั้งหมดอยู่ในรูปแบบ `YYYY-MM-DD HH:MM:SS`

---

## เปิดพอร์ต 6565 ในไฟร์วอลล์

ถ้าเปิดเว็บจากเครื่องอื่นไม่ได้ ส่วนใหญ่เพราะไฟร์วอลล์ของ server ปิดพอร์ตไว้

**Ubuntu / Debian (ufw):**

```bash
sudo ufw allow 6565/tcp
# หรือเปิดเฉพาะวงแลนของคุณ (ปลอดภัยกว่า):
sudo ufw allow from 192.168.1.0/24 to any port 6565 proto tcp
sudo ufw status
```

**Rocky / AlmaLinux / CentOS / Fedora (firewalld):**

```bash
sudo firewall-cmd --permanent --add-port=6565/tcp
sudo firewall-cmd --reload
sudo firewall-cmd --list-ports
```

zkscan ออกแบบมาให้ใช้ในวงแลน **อย่าเปิดพอร์ต 6565 ออกอินเทอร์เน็ตโดยตรง** ถ้าต้องการเข้าจากภายนอก ให้วางหลัง reverse proxy ที่มี HTTPS (เช่น nginx, Caddy) และควรเปิด HTTPS ของ zkscan เอง ดูหัวข้อถัดไป

## เปิดใช้ HTTPS (แนะนำ)

ถ้าไม่เปิด HTTPS รหัสผ่านของผู้ดูแลและ API key จะวิ่งในวงแลนแบบไม่เข้ารหัส ใครที่ดักแพ็กเก็ตในเครือข่ายได้ก็อ่านได้ จึงแนะนำให้เปิด

1. วางไฟล์ใบรับรองและกุญแจ (PEM) ไว้ใน `/etc/zkscan` ให้เจ้าของเป็น `root:zkscan` และกุญแจมีสิทธิ์ `0640`
2. ตั้งค่าสองตัวนี้ใน `/etc/zkscan/zkscan.env` (ต้องตั้งทั้งคู่ ตัวติดตั้งใส่ตัวอย่างที่ถูกคอมเมนต์ไว้ให้แล้วในไฟล์ใหม่):

   ```
   ZKSCAN_TLS_CERT=/etc/zkscan/server.crt
   ZKSCAN_TLS_KEY=/etc/zkscan/server.key
   ```

3. รีสตาร์ท: `sudo systemctl restart zkscan` แล้วเข้าที่ `https://<IP>:6565`

เมื่อตั้งครบทั้งสองตัว โปรแกรมจะเปิด HTTPS บนพอร์ต `ZKSCAN_LISTEN` เดิม และคุกกี้ session จะมีแฟล็ก `Secure`

**ตัวอย่างสร้างใบรับรองแบบ self-signed** (อายุ 825 วัน เปลี่ยน `192.168.1.10` เป็น IP ของ server):

```bash
sudo openssl req -x509 -newkey rsa:2048 -nodes -days 825 -subj "/CN=zkscan" -addext "subjectAltName=IP:192.168.1.10" -keyout /etc/zkscan/server.key -out /etc/zkscan/server.crt && sudo chown root:zkscan /etc/zkscan/server.key /etc/zkscan/server.crt && sudo chmod 0640 /etc/zkscan/server.key && sudo chmod 0644 /etc/zkscan/server.crt
```

เบราว์เซอร์จะเตือนว่าไม่น่าเชื่อถือจนกว่าจะติดตั้งไฟล์ `server.crt` เป็นใบรับรองที่เชื่อถือบนเครื่องผู้ใช้ (หรือใช้ใบรับรองจาก CA ภายในองค์กร)

**Docker:** วางไฟล์ไว้ในโฟลเดอร์ `certs/` ข้าง `docker-compose.yml` (ไฟล์ต้องอ่านได้โดยผู้ใช้ 65532) แล้วเอาเครื่องหมาย `#` ออกจากบรรทัด `ZKSCAN_TLS_CERT`, `ZKSCAN_TLS_KEY` และ `./certs:/certs:ro` ใน `docker-compose.yml` จากนั้น `docker compose up -d`

**ทางเลือก: วางหลัง reverse proxy ที่ทำ HTTPS อยู่แล้ว** (nginx, Caddy ฯลฯ) ให้ proxy ทำ TLS แล้วส่งต่อมาที่ zkscan แต่ **โปรแกรมไม่เชื่อ header `X-Forwarded-*`** ดังนั้นรายการ IP ที่อนุญาตของ API key จะเห็นเป็น IP ของ proxy ไม่ใช่ IP ของเครื่อง HR ที่แท้จริง (ควรจำกัดการเข้าถึงที่ proxy เองด้วย และให้ zkscan รับการเชื่อมต่อจาก proxy เท่านั้น เช่นตั้ง `ZKSCAN_LISTEN=127.0.0.1:6565`)

## ความปลอดภัย

- ใช้ในวงแลนภายในเท่านั้น และเปิดพอร์ต 6565 ให้เฉพาะเครือข่ายสำนักงาน (ดูตัวอย่าง ufw ในหัวข้อ "เปิดพอร์ต 6565 ในไฟร์วอลล์" ด้านบน)
- เปิด HTTPS ตามหัวข้อด้านบน
- กุญแจ API ทุกอันต้องจำกัด IP ให้เหลือเฉพาะ IP ของ server HR
- ผู้ดูแลทุกคนมีสิทธิ์เท่ากัน (บทบาทเดียว) จัดการบัญชีผู้ดูแลคนอื่นได้ จึงควรมีผู้ดูแลเท่าที่จำเป็น
- สำรอง `zkscan.db` สม่ำเสมอ ไฟล์นี้มีข้อมูลส่วนบุคคลของพนักงาน เก็บสำเนาไว้ในที่ที่เข้าถึงได้เฉพาะผู้เกี่ยวข้อง
- ไฟล์รหัสผ่านชั่วคราวจะถูกลบอัตโนมัติหลังจากที่ admin เปลี่ยนรหัสผ่านแล้ว

## เปลี่ยนพอร์ตหรือการตั้งค่า

แก้ไฟล์ `/etc/zkscan/zkscan.env` (ไม่ต้องใส่เครื่องหมายคำพูด) แล้วรีสตาร์ท:

```bash
sudoedit /etc/zkscan/zkscan.env      # เช่น ZKSCAN_LISTEN=:8080
sudo systemctl restart zkscan
```

ใช้พอร์ตตั้งแต่ 1024 ขึ้นไป (บริการรันแบบไม่มีสิทธิ์พิเศษ จึงเปิดพอร์ต 80/443 โดยตรงไม่ได้ ให้ใช้ reverse proxy)
ถ้าใช้ Docker: เปลี่ยนด้านซ้ายของ `ports:` ใน `docker-compose.yml` เช่น `"8080:6565"`

## อัปเดตเป็นเวอร์ชันใหม่

```bash
cd zkscan
git pull
sudo ./deploy/install.sh        # หรือ sudo ./install.sh
```

ข้อมูลและการตั้งค่าเดิมไม่ถูกแตะต้อง (ถ้ามีโฟลเดอร์ `dist/` เก่าค้างอยู่ ตัวติดตั้งจะใช้ไฟล์ในนั้น ให้รัน `make dist` ใหม่หรือลบ `dist/` ก่อน)

Docker: `git pull && docker compose up -d --build`

## สำรองข้อมูล

ข้อมูลทั้งหมดอยู่ในไฟล์ฐานข้อมูลเดียว ชื่อ `zkscan.db`

```bash
# แบบ systemd — หยุดสั้น ๆ เพื่อให้ไฟล์สมบูรณ์ แล้วคัดลอก
sudo systemctl stop zkscan
mkdir -p ~/zkscan-backup-$(date +%F)
sudo cp /var/lib/zkscan/zkscan.db* ~/zkscan-backup-$(date +%F)/
sudo systemctl start zkscan

# แบบ Docker
docker compose stop zkscan
docker run --rm -v zkscan_zkscan-data:/data -v "$PWD":/backup alpine cp /data/zkscan.db /backup/
docker compose start zkscan
```

(ชื่อ volume ของ Docker ขึ้นกับชื่อโฟลเดอร์ ดูได้จาก `docker volume ls`) กู้คืนโดยหยุดบริการ คัดลอกไฟล์กลับไปที่เดิม และเปลี่ยนเจ้าของ `sudo chown zkscan:zkscan /var/lib/zkscan/zkscan.db*`

## ลืมรหัสผ่าน

ออกรหัสผ่านชั่วคราวใหม่ให้บัญชีผู้ดูแล (ระบบจะบังคับให้เปลี่ยนตอนเข้าครั้งต่อไป):

```bash
# systemd
sudo -u zkscan env ZKSCAN_DATA=/var/lib/zkscan /usr/local/bin/zkscan reset-password admin

# Docker
docker compose exec zkscan zkscan reset-password admin
```

## ดู log และสถานะ

```bash
sudo journalctl -u zkscan -f           # ดู log สด ๆ (กด Ctrl+C เพื่อออก)
sudo journalctl -u zkscan -n 100       # 100 บรรทัดล่าสุด
sudo systemctl status zkscan           # สถานะบริการ
docker compose logs -f zkscan          # (Docker)
```

ในหน้าเว็บ เมนู **บันทึกการทำงาน** ก็แสดงเหตุการณ์ล่าสุดและสาเหตุที่ดึงเครื่องไหนไม่สำเร็จ

## ถอนการติดตั้ง

```bash
sudo ./deploy/uninstall.sh            # ลบโปรแกรมและบริการ แต่ "เก็บข้อมูลและการตั้งค่าไว้"
sudo ./deploy/uninstall.sh --purge    # ลบทุกอย่างรวมข้อมูล (ถามยืนยันก่อน; ใส่ --yes เพื่อข้ามคำถาม)
```

Docker: `docker compose down` (เก็บข้อมูลไว้) หรือ `docker compose down -v` (ลบ volume ข้อมูลด้วย)

## ปัญหาที่พบบ่อย

| อาการ | วิธีแก้ |
|---|---|
| เปิดเว็บจากเครื่องอื่นไม่ได้ | เปิดพอร์ต 6565 ในไฟร์วอลล์ (ดูหัวข้อด้านบน) และเช็ก `sudo systemctl status zkscan` |
| บริการไม่ขึ้นหลังติดตั้ง | `sudo journalctl -u zkscan -n 50` — มักเพราะพอร์ตถูกใช้อยู่ ให้เปลี่ยน `ZKSCAN_LISTEN` |
| ทดสอบเครื่องสแกนไม่ผ่าน | เช็ก IP/พอร์ต/Comm Key, ลองสลับ TCP/UDP, เช็กว่า server ต่อถึงเครื่องสแกนได้ (`ping`, `nc -vz <IP> 4370`) |
| ล็อกอินผิดหลายครั้งแล้วเข้าไม่ได้ | ระบบล็อกชั่วคราว 15 นาที แล้วลองใหม่ |
| เวลาใน log ไม่ตรงเวลาไทย | `sudo timedatectl set-timezone Asia/Bangkok` |

## สำหรับนักพัฒนา

```bash
make test      # go test -race ./...
make vet       # go vet ./...
make build     # ไฟล์ dist/zkscan สำหรับเครื่องที่ใช้อยู่
make dist      # dist/zkscan-linux-amd64 และ dist/zkscan-linux-arm64 (CGO_ENABLED=0)
go run ./cmd/zkscan serve --data ./data   # ลองรันบนเครื่องตัวเอง แล้วเปิด http://localhost:6565
```

คำสั่งของโปรแกรม: `zkscan serve | init | reset-password <username> | show-initial-password | version`
ตัวเลือก `--listen` (หรือ env `ZKSCAN_LISTEN`, ค่าเริ่มต้น `:6565`) และ `--data` (หรือ env `ZKSCAN_DATA`, ค่าเริ่มต้น `./data`)
