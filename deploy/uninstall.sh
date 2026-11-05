#!/usr/bin/env bash
# ถอนการติดตั้ง zkscan — ค่าเริ่มต้น: เก็บข้อมูลและไฟล์ตั้งค่าไว้
# วิธีใช้:  sudo ./deploy/uninstall.sh [--purge [--yes]]
#   --purge  ลบข้อมูล (ฐานข้อมูล), ไฟล์ตั้งค่า และผู้ใช้ระบบ zkscan ด้วย (ถามยืนยันก่อน)
#   --yes    ตอบยืนยันให้อัตโนมัติ (ใช้คู่กับ --purge)
set -euo pipefail

SERVICE_USER="zkscan"
DATA_DIR="/var/lib/zkscan"
CONF_DIR="/etc/zkscan"
BIN_DEST="/usr/local/bin/zkscan"
UNIT_DEST="/etc/systemd/system/zkscan.service"

PURGE="no"
ASSUME_YES="no"
for arg in "$@"; do
  case "$arg" in
    --purge) PURGE="yes" ;;
    --yes|-y) ASSUME_YES="yes" ;;
    -h|--help)
      sed -n '2,5p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "[ผิดพลาด] ไม่รู้จักตัวเลือก '$arg' (ใช้ได้: --purge, --yes)" >&2; exit 2 ;;
  esac
done

if [ "$(id -u)" -ne 0 ]; then
  echo "[ผิดพลาด] ต้องรันด้วยสิทธิ์ผู้ดูแลระบบ (root) — พิมพ์ใหม่ว่า:  sudo ./deploy/uninstall.sh" >&2
  exit 1
fi

if [ "$PURGE" = "yes" ] && [ "$ASSUME_YES" != "yes" ]; then
  echo "คำเตือน: --purge จะลบสิ่งต่อไปนี้ถาวร กู้คืนไม่ได้"
  echo "  - ข้อมูลทั้งหมด (บันทึกเวลา พนักงาน กุญแจ API ฯลฯ):  $DATA_DIR"
  echo "  - ไฟล์ตั้งค่า:  $CONF_DIR"
  echo "  - ผู้ใช้ระบบ:  $SERVICE_USER"
  echo "(ถ้ายังไม่แน่ใจ ให้สำรองไฟล์ $DATA_DIR/zkscan.db ไว้ก่อน)"
  if [ ! -t 0 ]; then
    echo "[ยกเลิก] ไม่มีหน้าจอให้ถามยืนยัน — ถ้าต้องการลบจริงให้ใส่ --yes ด้วย" >&2
    exit 1
  fi
  printf 'พิมพ์คำว่า "ใช่" (หรือ yes) แล้วกด Enter เพื่อยืนยัน: '
  read -r answer || answer=""
  case "$answer" in
    ใช่|yes|YES|Yes) ;;
    *) echo "ยกเลิกแล้ว ยังไม่มีอะไรถูกลบ"; exit 1 ;;
  esac
fi

echo "==> หยุดและปิดบริการ"
if command -v systemctl >/dev/null 2>&1; then
  systemctl disable --now zkscan >/dev/null 2>&1 || true
  rm -f "$UNIT_DEST"
  systemctl daemon-reload || true
  systemctl reset-failed zkscan >/dev/null 2>&1 || true
else
  rm -f "$UNIT_DEST"
fi

echo "==> ลบไฟล์โปรแกรม"
rm -f "$BIN_DEST"

if [ "$PURGE" = "yes" ]; then
  echo "==> ลบข้อมูล ไฟล์ตั้งค่า และผู้ใช้ระบบ"
  rm -rf "$DATA_DIR" "$CONF_DIR"
  if id -u "$SERVICE_USER" >/dev/null 2>&1; then
    userdel "$SERVICE_USER" >/dev/null 2>&1 || echo "[คำเตือน] ลบผู้ใช้ $SERVICE_USER ไม่สำเร็จ (ลบเองได้ด้วย: sudo userdel $SERVICE_USER)" >&2
  fi
  if getent group "$SERVICE_USER" >/dev/null 2>&1; then
    groupdel "$SERVICE_USER" >/dev/null 2>&1 || true
  fi
  echo "ถอนการติดตั้งและลบข้อมูลเรียบร้อยแล้ว"
else
  echo
  echo "ถอนการติดตั้งเรียบร้อยแล้ว — ข้อมูลและการตั้งค่ายังอยู่:"
  echo "  ข้อมูล:       $DATA_DIR"
  echo "  ไฟล์ตั้งค่า:   $CONF_DIR"
  echo "ติดตั้งใหม่ภายหลัง (sudo ./install.sh) จะใช้ข้อมูลเดิมต่อได้เลย"
  echo "ถ้าต้องการลบทั้งหมดด้วย ให้รัน:  sudo ./deploy/uninstall.sh --purge"
fi
