#!/usr/bin/env bash
# ตัวช่วยสั้น ๆ: ให้พิมพ์ `sudo ./install.sh` ได้เลย (ตัวติดตั้งจริงอยู่ที่ deploy/install.sh)
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
exec bash "$here/deploy/install.sh" "$@"
