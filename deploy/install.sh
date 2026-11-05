#!/usr/bin/env bash
# ติดตั้ง zkscan เป็นบริการ (systemd) บน Linux — รันซ้ำได้อย่างปลอดภัย (ใช้อัปเดตก็ได้)
# วิธีใช้:  sudo ./install.sh        (หรือ sudo ./deploy/install.sh)
set -euo pipefail

SERVICE_USER="zkscan"
DATA_DIR="/var/lib/zkscan"
CONF_DIR="/etc/zkscan"
ENV_FILE="$CONF_DIR/zkscan.env"
BIN_DEST="/usr/local/bin/zkscan"
UNIT_DEST="/etc/systemd/system/zkscan.service"
DEFAULT_PORT="6565"

# หาโฟลเดอร์ของโปรเจกต์จากตำแหน่งสคริปต์ (รันจากที่ไหนก็ได้)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd -P)"

TMP_DIR=""
cleanup() { [ -n "$TMP_DIR" ] && rm -rf "$TMP_DIR"; return 0; }
trap cleanup EXIT
on_error() {
  echo >&2
  echo "[ผิดพลาด] การติดตั้งหยุดกลางคัน (สคริปต์บรรทัดที่ $1)" >&2
  echo "          อ่านข้อความด้านบนเพื่อดูสาเหตุ แก้แล้วรันคำสั่งเดิมซ้ำได้อย่างปลอดภัย" >&2
}
trap 'on_error $LINENO' ERR

step() { echo; echo "==> $*"; }
info() { echo "    $*"; }
warn() { echo "[คำเตือน] $*" >&2; }
die()  { echo >&2; echo "[ผิดพลาด] $*" >&2; exit 1; }

# ---------- 0. ตรวจสิทธิ์และระบบ ----------
if [ "$(id -u)" -ne 0 ]; then
  die "ต้องรันด้วยสิทธิ์ผู้ดูแลระบบ (root)
          ให้พิมพ์ใหม่ว่า:  sudo ./install.sh"
fi

[ "$(uname -s)" = "Linux" ] || die "สคริปต์นี้ใช้ได้กับ Linux เท่านั้น (เครื่องนี้คือ $(uname -s))
          ถ้าต้องการรันบนเครื่องอื่น ให้ใช้ Docker:  docker compose up -d"

# ---------- 1. ตรวจ systemd และหาไฟล์โปรแกรม ----------
step "ขั้น 1/6  ตรวจระบบและหาไฟล์โปรแกรม"

if ! command -v systemctl >/dev/null 2>&1 || [ ! -d /run/systemd/system ]; then
  die "เครื่องนี้ไม่ได้ใช้ systemd (หรือยังไม่ได้บูตด้วย systemd) จึงติดตั้งเป็นบริการไม่ได้
          ทางเลือก: ใช้ Docker แทน โดยรัน  docker compose up -d  (ดูรายละเอียดใน README.md)"
fi

case "$(uname -m)" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) die "ไม่รองรับสถาปัตยกรรม CPU แบบ '$(uname -m)' (รองรับเฉพาะ amd64 และ arm64)
          ทางเลือก: ใช้ Docker แทน  docker compose up -d" ;;
esac
info "สถาปัตยกรรม CPU: $ARCH"

go_ok() {
  # คืน 0 ถ้ามีคำสั่ง go และเวอร์ชัน >= 1.24
  command -v go >/dev/null 2>&1 || return 1
  local v major minor
  v="$(go env GOVERSION 2>/dev/null || true)"   # เช่น go1.24.1
  v="${v#go}"
  major="${v%%.*}"
  minor="${v#*.}"; minor="${minor%%.*}"
  case "$major$minor" in ''|*[!0-9]*) return 1 ;; esac
  [ "$major" -gt 1 ] || { [ "$major" -eq 1 ] && [ "$minor" -ge 24 ]; }
}

TMP_DIR="$(mktemp -d)"
NEW_BIN="$TMP_DIR/zkscan"
DIST_BIN="$REPO_ROOT/dist/zkscan-linux-$ARCH"

if [ -f "$DIST_BIN" ]; then
  info "ใช้ไฟล์โปรแกรมสำเร็จรูป: $DIST_BIN"
  cp "$DIST_BIN" "$NEW_BIN"
else
  # sudo มักตัด PATH ทำให้หา go ที่ติดตั้งไว้ใน /usr/local/go ไม่เจอ จึงเติมให้
  for p in /usr/local/go/bin /usr/lib/go/bin "$HOME/go/bin"; do
    [ -d "$p" ] && PATH="$PATH:$p"
  done
  export PATH
  if go_ok; then
    info "ไม่พบ dist/zkscan-linux-$ARCH — กำลังคอมไพล์จากซอร์สด้วย Go $(go env GOVERSION) (ต้องต่ออินเทอร์เน็ตเพื่อโหลดไลบรารี อาจใช้เวลาสักครู่)"
    VERSION="$(cd "$REPO_ROOT" && git describe --tags --always --dirty 2>/dev/null || true)"
    [ -n "$VERSION" ] || VERSION="dev"
    ( cd "$REPO_ROOT" && CGO_ENABLED=0 go build -buildvcs=false -trimpath -tags timetzdata \
        -ldflags "-s -w -X main.version=$VERSION" -o "$NEW_BIN" ./cmd/zkscan )
    info "คอมไพล์เสร็จ (เวอร์ชัน $VERSION)"
  else
    die "ไม่พบไฟล์โปรแกรมสำเร็จรูป ($DIST_BIN) และเครื่องนี้ไม่มี Go เวอร์ชัน 1.24 ขึ้นไป
          เลือกวิธีใดวิธีหนึ่ง:
            1) ติดตั้ง Go 1.24+ จาก https://go.dev/dl/ แล้วรัน  sudo ./install.sh  ใหม่
            2) สร้างไฟล์โปรแกรมจากเครื่องอื่นที่มี Go ด้วย  make dist  แล้วคัดลอกโฟลเดอร์ dist/ มาไว้ที่ $REPO_ROOT
            3) ใช้ Docker แทนโดยไม่ต้องมี Go:  docker compose up -d"
  fi
fi

chmod 0755 "$NEW_BIN"
if ! NEW_VERSION="$("$NEW_BIN" version 2>&1)"; then
  die "ไฟล์โปรแกรมที่เตรียมไว้รันไม่ได้บนเครื่องนี้ (อาจเป็นไฟล์ผิดสถาปัตยกรรมหรือเสียหาย)
          ผลที่ได้: $NEW_VERSION
          ลองลบโฟลเดอร์ dist/ แล้วรันใหม่ หรือสร้างใหม่ด้วย  make dist"
fi
info "เวอร์ชันที่จะติดตั้ง: $NEW_VERSION"

# ---------- 2. ผู้ใช้ระบบ โฟลเดอร์ และไฟล์ตั้งค่า ----------
step "ขั้น 2/6  เตรียมผู้ใช้ระบบ โฟลเดอร์ข้อมูล และไฟล์ตั้งค่า"

if id -u "$SERVICE_USER" >/dev/null 2>&1; then
  info "ผู้ใช้ระบบ '$SERVICE_USER' มีอยู่แล้ว — ข้าม"
else
  NOLOGIN="$(command -v nologin || true)"
  [ -n "$NOLOGIN" ] || NOLOGIN="/usr/sbin/nologin"
  command -v useradd >/dev/null 2>&1 || die "ไม่พบคำสั่ง useradd จึงสร้างผู้ใช้ระบบให้อัตโนมัติไม่ได้
          ให้สร้างผู้ใช้ชื่อ '$SERVICE_USER' ด้วยตัวเองแล้วรันสคริปต์ใหม่"
  useradd --system --user-group --no-create-home --home-dir "$DATA_DIR" \
          --shell "$NOLOGIN" "$SERVICE_USER"
  info "สร้างผู้ใช้ระบบ '$SERVICE_USER' แล้ว (ล็อกอินไม่ได้)"
fi

install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" "$DATA_DIR"
info "โฟลเดอร์ข้อมูล: $DATA_DIR (ข้อมูลทั้งหมดอยู่ที่นี่ รวมถึงไฟล์ zkscan.db)"

install -d -m 0750 -o root -g "$SERVICE_USER" "$CONF_DIR"
if [ -f "$ENV_FILE" ]; then
  info "ไฟล์ตั้งค่า $ENV_FILE มีอยู่แล้ว — ไม่แตะต้อง (ค่าที่คุณแก้ไว้ยังอยู่)"
else
  umask 0137
  cat > "$ENV_FILE" <<ENVEOF
# ไฟล์ตั้งค่าของ zkscan (ไม่ต้องใส่เครื่องหมายคำพูดครอบค่า)
# แก้แล้วสั่ง  sudo systemctl restart zkscan  เพื่อให้มีผล

# ที่อยู่และพอร์ตที่เปิดให้เข้าเว็บ/API (:6565 = ทุกการ์ดเครือข่าย พอร์ต 6565)
ZKSCAN_LISTEN=:$DEFAULT_PORT

# โฟลเดอร์เก็บข้อมูล (ฐานข้อมูลอยู่ที่ \$ZKSCAN_DATA/zkscan.db)
ZKSCAN_DATA=$DATA_DIR
ENVEOF
  umask 0022
  chown root:"$SERVICE_USER" "$ENV_FILE"
  chmod 0640 "$ENV_FILE"
  info "สร้างไฟล์ตั้งค่า $ENV_FILE แล้ว"
fi

# อ่านไฟล์ตั้งค่าเป็นรายการ KEY=VALUE (ไม่ใช้ source เพื่อไม่ให้ค่าในไฟล์ถูกรันเป็นคำสั่ง)
# เริ่มจากค่าเริ่มต้นก่อน แล้วให้ค่าในไฟล์ทับ (ค่าท้ายสุดชนะ)
ENV_ARGS=("ZKSCAN_LISTEN=:$DEFAULT_PORT" "ZKSCAN_DATA=$DATA_DIR")
while IFS= read -r line || [ -n "$line" ]; do
  line="${line%$'\r'}"
  line="${line#"${line%%[![:space:]]*}"}"
  case "$line" in
    ''|'#'*) continue ;;
    [A-Za-z_]*=*) ENV_ARGS+=("$line") ;;
  esac
done < "$ENV_FILE"

env_get() {  # env_get KEY DEFAULT — ค่าสุดท้ายที่ซ้ำกันชนะ (เหมือน systemd)
  local key="$1" val="$2" kv
  for kv in "${ENV_ARGS[@]}"; do
    [ "${kv%%=*}" = "$key" ] && val="${kv#*=}"
  done
  printf '%s' "$val"
}

run_as_service_user() {
  # รันคำสั่งในนามผู้ใช้ zkscan พร้อมค่าจากไฟล์ตั้งค่า
  if command -v runuser >/dev/null 2>&1; then
    ( cd / && runuser -u "$SERVICE_USER" -- env "${ENV_ARGS[@]}" "$@" )
  elif command -v sudo >/dev/null 2>&1; then
    ( cd / && sudo -u "$SERVICE_USER" env "${ENV_ARGS[@]}" "$@" )
  else
    die "ไม่พบคำสั่ง runuser หรือ sudo สำหรับรันคำสั่งในนามผู้ใช้ $SERVICE_USER"
  fi
}

# ---------- 3. ติดตั้งไฟล์โปรแกรม ----------
step "ขั้น 3/6  ติดตั้งไฟล์โปรแกรมไปที่ $BIN_DEST"
if [ -f "$BIN_DEST" ]; then
  OLD_VERSION="$("$BIN_DEST" version 2>/dev/null || echo 'ไม่ทราบ')"
  info "เวอร์ชันเดิมที่ติดตั้งอยู่: $OLD_VERSION"
fi
install -m 0755 -o root -g root "$NEW_BIN" "$BIN_DEST"
info "ติดตั้งแล้ว ($("$BIN_DEST" version 2>/dev/null || echo "$NEW_VERSION"))"

# ---------- 4. สร้างบัญชีผู้ดูแลครั้งแรก ----------
step "ขั้น 4/6  เตรียมฐานข้อมูลและบัญชีผู้ดูแล (zkscan init)"
set +e
INIT_OUT="$(run_as_service_user "$BIN_DEST" init 2>&1)"
INIT_RC=$?
set -e
[ -z "$INIT_OUT" ] || printf '%s\n' "$INIT_OUT" | sed 's/^/    /'
if [ "$INIT_RC" -ne 0 ]; then
  die "คำสั่ง zkscan init ล้มเหลว (รหัส $INIT_RC) — ดูข้อความด้านบน
          สาเหตุที่พบบ่อย: พื้นที่ดิสก์เต็ม หรือสิทธิ์ของโฟลเดอร์ $DATA_DIR ไม่ถูกต้อง"
fi
TEMP_PASS_LINE="$(printf '%s\n' "$INIT_OUT" | grep 'รหัสผ่านชั่วคราว' || true)"

# ---------- 5. ติดตั้งและเริ่มบริการ ----------
step "ขั้น 5/6  ติดตั้งบริการ systemd และเริ่มทำงาน"
WAS_ACTIVE="no"
systemctl is-active --quiet zkscan && WAS_ACTIVE="yes"
install -m 0644 -o root -g root "$SCRIPT_DIR/zkscan.service" "$UNIT_DEST"
systemctl daemon-reload
if [ "$WAS_ACTIVE" = "yes" ]; then
  systemctl enable zkscan >/dev/null 2>&1
  systemctl restart zkscan
  info "บริการทำงานอยู่แล้ว — รีสตาร์ทเพื่อใช้โปรแกรมเวอร์ชันใหม่"
else
  systemctl enable --now zkscan >/dev/null 2>&1
  info "เปิดบริการแล้ว และจะเริ่มเองทุกครั้งที่เปิดเครื่อง"
fi

sleep 2
if ! systemctl is-active --quiet zkscan; then
  warn "บริการยังไม่ขึ้นสถานะทำงาน — ดูสาเหตุด้วยคำสั่ง:  sudo journalctl -u zkscan -n 50 --no-pager"
  warn "สาเหตุที่พบบ่อย: พอร์ตถูกใช้อยู่ (แก้ ZKSCAN_LISTEN ใน $ENV_FILE) แล้วสั่ง sudo systemctl restart zkscan"
  exit 1
fi

# ---------- 6. สรุป ----------
step "ขั้น 6/6  เสร็จเรียบร้อย"
LISTEN="$(env_get ZKSCAN_LISTEN ":$DEFAULT_PORT")"
PORT="${LISTEN##*:}"
case "$PORT" in ''|*[!0-9]*) PORT="$DEFAULT_PORT" ;; esac

IPS=""
if command -v ip >/dev/null 2>&1; then
  IPS="$(ip -4 -o addr show scope global 2>/dev/null | awk '{print $4}' | cut -d/ -f1 || true)"
fi
if [ -z "$IPS" ] && command -v hostname >/dev/null 2>&1; then
  IPS="$(hostname -I 2>/dev/null | tr ' ' '\n' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' || true)"
fi
IPS="$(printf '%s\n' "$IPS" | grep -v '^127\.' | grep -v '^$' || true)"

echo
echo "=============================================================="
echo " ติดตั้ง zkscan สำเร็จ ($("$BIN_DEST" version 2>/dev/null || echo "$NEW_VERSION"))"
echo "=============================================================="
echo
echo "เปิดเว็บเพื่อตั้งค่าได้ที่ (เปิดจากคอมพิวเตอร์เครื่องไหนก็ได้ในวงแลนเดียวกัน):"
if [ -n "$IPS" ]; then
  printf '%s\n' "$IPS" | while IFS= read -r ip; do echo "    http://$ip:$PORT"; done
else
  echo "    http://<IP ของเครื่องนี้>:$PORT   (หา IP ได้ด้วยคำสั่ง  hostname -I )"
fi
echo
if [ -n "$TEMP_PASS_LINE" ]; then
  echo "ล็อกอินครั้งแรก:  ชื่อผู้ใช้ admin"
  echo "  $TEMP_PASS_LINE"
  echo "  (จดไว้ แล้วระบบจะบังคับให้เปลี่ยนรหัสผ่านทันทีที่เข้าครั้งแรก)"
else
  echo "บัญชีผู้ดูแลมีอยู่แล้ว ใช้ชื่อผู้ใช้และรหัสผ่านเดิมของคุณ"
fi
echo
echo "คำสั่งที่ใช้บ่อย:"
echo "  ดูรหัสชั่วคราวอีกครั้ง (ใช้ได้จนกว่า admin จะเปลี่ยนรหัสผ่านแล้ว):"
echo "      sudo -u $SERVICE_USER env ZKSCAN_DATA=$DATA_DIR $BIN_DEST show-initial-password"
echo "  ลืมรหัสผ่าน (ออกรหัสชั่วคราวใหม่):"
echo "      sudo -u $SERVICE_USER env ZKSCAN_DATA=$DATA_DIR $BIN_DEST reset-password admin"
echo "  ดู log สด ๆ:          sudo journalctl -u zkscan -f"
echo "  ดูสถานะ:              sudo systemctl status zkscan"
echo "  รีสตาร์ท:             sudo systemctl restart zkscan"
echo "  อัปเดตเป็นเวอร์ชันใหม่:  cd $REPO_ROOT && git pull && sudo ./install.sh"
echo "  ถอนการติดตั้ง:         sudo $SCRIPT_DIR/uninstall.sh   (ข้อมูลยังอยู่ ถ้าไม่ใส่ --purge)"
echo
echo "ถ้าเปิดเว็บจากเครื่องอื่นไม่ได้ อาจเป็นเพราะไฟร์วอลล์ปิดพอร์ต $PORT — ดูวิธีเปิดใน README.md"
echo "หมายเหตุ: เครื่องนี้ต้องอยู่ในวงแลนเดียวกับเครื่องสแกน (หรือเข้าถึงพอร์ตเครื่องสแกน 4370 ได้)"
