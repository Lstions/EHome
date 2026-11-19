#!/usr/bin/env bash
# =============================================================================
# verify-release.sh — 平台发布对账 + release.json schema 校验（方案 §6.5 / T2.9）
#
# 为什么需要它：CI 绿 ≠ 线上跑的是这份代码。2026-10-21 的事故就是"镜像不是从 main
# 构建"，事后只能靠 strings 推断来源。commit 烙进二进制只是第一步 —— 还必须有人把
# 「发布清单说的 commit」与「运行中的两个端点说的 commit」逐字对上，否则来源依旧
# 是一句口头承诺。
#
# 判定（三态，与方案 §6.5 表格逐条对应）：
#   consistent  清单 commit == /health.commit == /api/v1/system/version.commit  → 退出 0
#   drifted     任一处不一致                                                  → 退出 1（提示回退镜像）
#   unknown     清单缺失/未填 commit，或端点不可达/响应不可解析                → 退出 2（人工确认）
#   （另：notes schema 不合法 → 退出 3；这是构建期门禁，见 --notes-only）
#
# 只对账 commit（§6.5）：改 notes 文案不算漂移，否则补一句说明就会触发告警。
# notes schema 另外校验：必须是数组、≤10 条、每条 ≤120 字、剔除空串后计数。
#
# 归属：仓库里的 deploy/release.json 是**模板** —— 人只维护 version 与 notes；
# commit/build_time 留空，由 CI 在构建时填入并写进 backend/internal/buildinfo/release.json，
# 再 go:embed 进二进制。本脚本校验的就是这份模板/CI 产物。
#
# 用法：
#   scripts/verify-release.sh                                   # 对账本机 :8080
#   scripts/verify-release.sh --base-url http://127.0.0.1:18080
#   scripts/verify-release.sh --from-file /tmp/release.json     # 指定清单（本地自测）
#   scripts/verify-release.sh --notes-only                      # 只校验 schema、不联网
#
# 环境变量：BASE_URL、RELEASE_FILE、CURL_TIMEOUT（秒，默认 5）
# 依赖：bash、curl、python3（只用于 JSON 解析；不引入 jq，避免部署机额外安装）
# =============================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFEST="${RELEASE_FILE:-$REPO_ROOT/deploy/release.json}"
BASE_URL="${BASE_URL:-http://127.0.0.1:8080}"
CURL_TIMEOUT="${CURL_TIMEOUT:-5}"
NOTES_ONLY=0

usage() {
  sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --from-file)   MANIFEST="${2:-}"; shift 2 ;;
    --from-file=*) MANIFEST="${1#*=}"; shift ;;
    --base-url)    BASE_URL="${2:-}"; shift 2 ;;
    --base-url=*)  BASE_URL="${1#*=}"; shift ;;
    --notes-only)  NOTES_ONLY=1; shift ;;
    -h|--help)     usage; exit 0 ;;
    *) echo "verify-release: 未知参数: $1" >&2; usage; exit 2 ;;
  esac
done

if ! command -v python3 >/dev/null 2>&1; then
  echo "verify-release: 需要 python3 解析 JSON（清单与端点响应都是 JSON）" >&2
  echo "RESULT unknown"
  exit 2
fi
if ! command -v curl >/dev/null 2>&1; then
  echo "verify-release: 需要 curl 访问运行时端点" >&2
  echo "RESULT unknown"
  exit 2
fi

# ── 1) 清单 schema（构建期门禁；--notes-only 到这一步就结束）──────────────────
manifest_fields=""
manifest_fields="$(python3 - "$MANIFEST" <<'PY'
import json
import sys

path = sys.argv[1]
MAX_NOTES = 10
MAX_NOTE_CHARS = 120

try:
    with open(path, encoding="utf-8") as fh:
        doc = json.load(fh)
except FileNotFoundError:
    print(f"verify-release: 清单不存在: {path}", file=sys.stderr)
    sys.exit(2)
except json.JSONDecodeError as exc:
    print(f"verify-release: 清单不是合法 JSON: {path}: {exc}", file=sys.stderr)
    sys.exit(2)

if not isinstance(doc, dict):
    print("verify-release: 清单顶层必须是 JSON 对象", file=sys.stderr)
    sys.exit(3)

errs = []

version = doc.get("version", "")
if not isinstance(version, str) or not version.strip():
    errs.append("version 必须是非空字符串")

commit = doc.get("commit", "")
if not isinstance(commit, str):
    errs.append("commit 必须是字符串（未发布时可留空，由 CI 填入）")

build_time = doc.get("build_time", "")
if not isinstance(build_time, str):
    errs.append("build_time 必须是字符串（未发布时可留空，由 CI 填入）")

notes = doc.get("notes", [])
if not isinstance(notes, list):
    errs.append(f"notes 必须是数组，实际是 {type(notes).__name__}")
    notes = []

clean = []
for i, item in enumerate(notes):
    if not isinstance(item, str):
        errs.append(f"notes[{i}] 必须是字符串，实际是 {type(item).__name__}")
        continue
    text = item.strip()
    if not text:
        continue  # 空串剔除：不计为一条，也不因此拒绝
    if len(text) > MAX_NOTE_CHARS:
        errs.append(f"notes[{i}] 有 {len(text)} 字（{text[:24]}…），超过上限 {MAX_NOTE_CHARS}")
    clean.append(text)

if len(clean) > MAX_NOTES:
    errs.append(f"notes 有效条目 {len(clean)} 条，超过上限 {MAX_NOTES} 条")

if errs:
    for e in errs:
        print(f"verify-release: 清单 schema 不合法: {e}", file=sys.stderr)
    sys.exit(3)

print(version.strip())
print(commit.strip())
print(len(clean))
PY
)"
manifest_rc=$?

case "$manifest_rc" in
  0) : ;;
  3) echo "RESULT notes-invalid"; exit 3 ;;
  *) echo "RESULT unknown"; exit 2 ;;
esac

# 逐行读而不是按 TAB 切：commit 未填时是空行，IFS 空白分隔符会把空字段吞掉，
# 导致 "notes 条数" 错位到 commit 上（隐藏的空值比报错更难查）。
{
  IFS= read -r manifest_version
  IFS= read -r manifest_commit
  IFS= read -r manifest_notes
} <<<"$manifest_fields"

echo "== release 对账 =="
echo "清单          : $MANIFEST"
echo "   version    : $manifest_version"
echo "   notes      : $manifest_notes 条（schema 合法：数组、≤10 条、每条 ≤120 字）"

if [ "$NOTES_ONLY" = 1 ]; then
  echo "RESULT notes-ok"
  exit 0
fi

# ── 2) 运行时端点 ────────────────────────────────────────────────────────────
json_field() {
  # $1 = 点分路径（如 data.commit）；JSON 从 stdin 读入
  python3 -c '
import json
import sys

path = sys.argv[1].split(".")
try:
    node = json.load(sys.stdin)
    for key in path:
        node = node[key]
    if not isinstance(node, str):
        raise TypeError("字段不是字符串")
    print(node.strip())
except Exception as exc:  # 不可解析一律按 unknown 处理，绝不猜
    print(f"verify-release: 解析 {sys.argv[1]} 失败: {exc}", file=sys.stderr)
    sys.exit(1)
' "$1"
}

health_body=""
version_body=""
if ! health_body="$(curl -fsS --max-time "$CURL_TIMEOUT" "$BASE_URL/health" 2>/dev/null)"; then
  echo "端点不可达        : $BASE_URL/health（超时 ${CURL_TIMEOUT}s）"
  echo "RESULT unknown"
  exit 2
fi
if ! version_body="$(curl -fsS --max-time "$CURL_TIMEOUT" "$BASE_URL/api/v1/system/version" 2>/dev/null)"; then
  echo "端点不可达        : $BASE_URL/api/v1/system/version（超时 ${CURL_TIMEOUT}s）"
  echo "RESULT unknown"
  exit 2
fi

health_commit=""
version_commit=""
version_build_id=""
version_build_time=""
if ! health_commit="$(printf '%s' "$health_body" | json_field commit)"; then
  echo "RESULT unknown"
  exit 2
fi
if ! version_commit="$(printf '%s' "$version_body" | json_field data.commit)"; then
  echo "RESULT unknown"
  exit 2
fi
version_build_id="$(printf '%s' "$version_body" | json_field data.build_id)" || version_build_id="?"
version_build_time="$(printf '%s' "$version_body" | json_field data.build_time)" || version_build_time="?"

echo "运行时端点    : $BASE_URL"
echo "   /health.commit              : $health_commit"
echo "   /system/version.commit      : $version_commit"
echo "   /system/version.build_id    : $version_build_id"
echo "   /system/version.build_time  : ${version_build_time:-（空）}"
echo "清单 commit   : ${manifest_commit:-（空）}"

# ── 3) 判定 ─────────────────────────────────────────────────────────────────
# 清单未填 commit（仓库模板状态）不算"一致"：没有期望值就没有对账。
if [ -z "$manifest_commit" ]; then
  echo "结论          : 清单未填 commit —— 这是模板状态，构建/部署时应由 CI 填入"
  echo "RESULT unknown"
  exit 2
fi

drift=0
if [ "$health_commit" != "$manifest_commit" ]; then
  echo "  [DRIFT] /health.commit != 清单 commit"
  drift=1
fi
if [ "$version_commit" != "$manifest_commit" ]; then
  echo "  [DRIFT] /system/version.commit != 清单 commit"
  drift=1
fi
if [ -z "$health_commit" ] || [ -z "$version_commit" ]; then
  echo "  [DRIFT] 运行端点的 commit 为空（未注入构建来源）"
  drift=1
fi

if [ "$drift" = 1 ]; then
  echo "结论          : drifted —— 线上镜像与发布清单不是同一份代码，回退镜像"
  echo "RESULT drifted"
  exit 1
fi

echo "结论          : consistent —— 清单、/health、/system/version 三处 commit 一致"
echo "RESULT consistent"
exit 0
