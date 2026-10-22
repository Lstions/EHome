#!/usr/bin/env bash
# check_field_selectors.sh —— 门禁 11 / 代码回退前置盘点（只读）。
#
# 「无本脚本盘点结果，不得执行代码回退」（方案 §12.3#11 / §14.3）。
# 分类与 SQL 实现在 field_selectors_scan.py；本包装器只做参数转发与 python3 探测。
#
# 用法：
#   ./scripts/check_field_selectors.sh              # 扫描（docker exec ehome-postgres）
#   ./scripts/check_field_selectors.sh --json       # JSON 输出
#   ./scripts/check_field_selectors.sh --selftest   # 分类器自检（不连库）
#   EHOME_PG_CONTAINER/ EHOME_DB_USER/ EHOME_DB_NAME 可覆盖连接参数。
# 退出码：0=无非字面量（可回退）；1=存在（禁止回退）；77=DB 不可达（SKIP）；2=用法错。
set -uo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
PY=$(command -v python3 || true)
if [ -z "$PY" ]; then
  echo "SKIP: python3 不存在，无法执行盘点（门禁 11 不应被当作通过）" >&2
  echo "RESULT: SKIP(python3-missing)"
  exit 77
fi

exec "$PY" "$SCRIPT_DIR/field_selectors_scan.py" "$@"
