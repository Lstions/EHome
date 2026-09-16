#!/usr/bin/env bash
# =============================================================================
# Q4 —— 容器健康探针可达性（未鉴权）  [子代理 C]
#
# 守护的不变量（对齐 backend/simulation/catalog/dep.go:depRun005 / SIM-DEP-005）:
#   编排系统（Prometheus / k8s / docker healthcheck）**不经登录**就要能探活。
#   健康端点一旦要求 JWT，存活探测会被 401 挡死 —— 这是"服务看起来在、实际已死"
#   这类事故的最常见盲区。
#
# 本探针断言:
#   H1  /health              未鉴权可达 (200) 且响应体是 JSON 且含 "status"
#   H2  /metrics             未鉴权可达 (200) 且是 Prometheus 文本格式 (# HELP)
#   H3  反向对照: 未鉴权访问受保护端点 /api/v1/overview 必须 401
#       （若它 200，说明"未鉴权可达"是因为整个 API 都没鉴权 —— 那 H1/H2 就没有意义）
#   H4  /api/v1/health 陷阱: 该路径**不是**健康端点 —— SPA catch-all 对它返回 200 +
#       index.html。显式记录其真实形态，防止把"200 就算健康"写进判据（正是本题要防的假绿）。
#   H5  分母守卫: 断言本轮真的探测了 >= 2 个端点（防扫描器空转导致永远绿）
#   H6  冷启动回填日志 "Latest value cache warmed up: N rows" 必须出现（主控本轮接线）
#
# 环境契约（由 run.sh 注入；默认值 = CONTRACT 契约值）:
#   BB_PROJECT 默认 ehome-bb / BB_HOME_PORT 默认 18080 / BB_DB_NAME 默认 ehome_bb_<runid>
#
# 退出码: 0=全绿  1=有断言失败  77=主动 SKIP
# =============================================================================
set -uo pipefail

PROJECT="${BB_PROJECT:-ehome-bb}"
HOME_PORT="${BB_HOME_PORT:-18080}"
BASE="http://127.0.0.1:${HOME_PORT}"
WEB="${PROJECT}-ehome"
EVID="${BB_EVIDENCE_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/evidence/C-manual}"
export PATH=/snap/bin:/home/sun/.local/share/pnpm/bin:$PATH
export HOME="${HOME:-/home/sun}"
mkdir -p "$EVID"

PASS=0; FAIL=0; PROBED=0
declare -a RESULTS
ok()   { PASS=$((PASS+1)); RESULTS+=("PASS|$1|$2"); printf 'PASS  %s : %s\n' "$1" "$2"; }
bad()  { FAIL=$((FAIL+1)); RESULTS+=("FAIL|$1|$2"); printf 'FAIL  %s : %s\n' "$1" "$2"; }
note() { printf 'INFO  %s\n' "$*"; }

# ---- 秘密保护 ---------------------------------------------------------------
# 本探针会落盘启动日志, 而日志里含**一次性初始化凭据**(第 05 探针要用的那个)。
# 因此这里必须自带脱敏 —— 不能假定"只有 05 才碰凭据"。
# 实测教训(2026-09-16): 初版把整段启动日志原样写进 H6-startup-logs.txt,
# 被交付模式取证的秘密核查抓到 1 个凭据形状 token ⇒ 真泄漏, 已修。
# 秘密脱敏：统一用共享实现（原先本文件自带一份，与 05 分叉 —— 那正是缺陷温床）
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_redact.sh"
redact_file() {
  # 薄封装：委托给共享实现（见 _redact.sh），避免两份逻辑分叉。
  bb_redact_file "$1"
}

container_state() { docker inspect -f '{{.State.Status}}' "$WEB" 2>/dev/null || echo MISSING; }

{
  echo "probe=04-health.sh  project=$PROJECT  BB_PROJECT=$PROJECT  BB_HOME_PORT=$HOME_PORT  BB_DB_NAME=${BB_DB_NAME:-<unset>}"
  echo "base_url=$BASE  container=$WEB"
  echo "started_at=$(date -Is)"
} > "$EVID/context.txt"

note "BB_PROJECT=$PROJECT  BB_HOME_PORT=$HOME_PORT  BB_DB_NAME=${BB_DB_NAME:-<unset>}"

STATE="$(container_state)"

# 就绪等待: compose 的 ehome 服务**没有 healthcheck**, 故 up -d --wait 只保证"容器已运行",
# 不保证 HTTP 已监听 —— 直接探会得到 000, 并把它误判成"端点不可达"(假红)。
# 这里显式轮询 /health(最多 WAIT_READY 秒)。
WAIT_READY="${WAIT_READY:-90}"
wait_ready() {
  local i
  for i in $(seq 1 "$WAIT_READY"); do
    if curl -sS -m 3 -o /dev/null "$BASE/health" 2>/dev/null; then return 0; fi
    sleep 1
  done
  return 1
}

if [[ "$STATE" != "running" ]]; then
  note "容器 $WEB 状态=$STATE —— 无栈可探, 主动 SKIP (77)"
  { echo "SKIP: $WEB 状态=$STATE"; echo "提示: 用 deploy/blackbox/run.sh --only 04 让编排层先起栈"; } > "$EVID/result.txt"
  exit 77
fi

if ! wait_ready; then
  echo "NOT_READY: /health 在 ${WAIT_READY}s 内未就绪" > "$EVID/not-ready.txt"
  docker logs --tail=40 "$WEB" >> "$EVID/not-ready.txt" 2>&1 || true
  note "/health 未在 ${WAIT_READY}s 内就绪 —— 见 $EVID/not-ready.txt"; exit 1
fi

# HTTP 辅助: 输出 "STATUS<TAB>CONTENT_TYPE<TAB>BODY_FILE"
http_get() {
  local path="$1" out; out="$(mktemp)"
  local meta; meta="$(curl -sS -m 20 -o "$out" -w '%{http_code}\t%{content_type}' "$BASE$path" 2>/dev/null)" || meta=$'000	-'
  printf '%s\t%s\n' "$meta" "$out"
}

# ---- H1 /health 未鉴权可达 + JSON + status -----------------------------------
note "探测 H1 GET /health (无 Authorization 头)"
IFS=$'\t' read -r st ct bodyf <<< "$(http_get /health)"
PROBED=$((PROBED+1))
{ echo "GET /health -> status=$st content_type=$ct"; echo "--- body (head -c 400) ---"; head -c 400 "$bodyf"; echo; } > "$EVID/H1-health.txt"
if [[ "$st" == "200" ]]; then
  if grep -q '"status"' "$bodyf"; then
    ok H1 "/health 未鉴权可达 200 且含 status: $(head -c 120 "$bodyf")"
  else
    bad H1 "/health 返回 200 但缺 \\"status\\" (status=$st ct=$ct body=$(head -c 200 "$bodyf"))"
  fi
else
  bad H1 "/health 未鉴权不可达: status=$st (期望 200) ct=$ct body=$(head -c 200 "$bodyf")"
fi
rm -f "$bodyf"

# ---- H2 /metrics 未鉴权可达 + Prometheus 文本格式 ----------------------------
note "探测 H2 GET /metrics (无 Authorization 头)"
IFS=$'\t' read -r st ct bodyf <<< "$(http_get /metrics)"
PROBED=$((PROBED+1))
BYTES="$(wc -c < "$bodyf" | tr -d ' ')"
HELPS="$(grep -c '^# HELP' "$bodyf" 2>/dev/null)"; HELPS="${HELPS:-0}"
{
  echo "GET /metrics -> status=$st content_type=$ct bytes=$BYTES help_lines=$HELPS"
  echo "--- 前 5 行 ---"; head -5 "$bodyf"
  echo "--- 前 64 字节 hexdump (诊断编码/gzip 双重帧问题) ---"; head -c 64 "$bodyf" | od -An -tx1 | head -4
} > "$EVID/H2-metrics.txt"
if [[ "$st" != "200" ]]; then
  bad H2 "/metrics 未鉴权不可达: status=$st (期望 200) ct=$ct body=$(head -c 200 "$bodyf")"
elif [[ "$HELPS" -lt 1 ]]; then
  bad H2 "/metrics 返回 200 但非 Prometheus 文本 (无 '# HELP'): bytes=$BYTES ct=$ct body=$(head -c 200 "$bodyf")"
elif [[ "$BYTES" -lt 100 ]]; then
  bad H2 "/metrics 返回 200 但体积异常小: bytes=$BYTES (疑似空/错误页)"
else
  ok H2 "/metrics 未鉴权可达 200, Prometheus 格式 (bytes=$BYTES help_lines=$HELPS)"
fi
rm -f "$bodyf"

# ---- H3 反向对照: 受保护端点未鉴权必须 401 ------------------------------------
note "探测 H3 GET /api/v1/overview (反向对照, 期望 401)"
IFS=$'\t' read -r st ct bodyf <<< "$(http_get /api/v1/overview)"
PROBED=$((PROBED+1))
{ echo "GET /api/v1/overview (无 token) -> status=$st content_type=$ct"; echo "--- body ---"; head -c 400 "$bodyf"; echo; } > "$EVID/H3-protected-control.txt"
if [[ "$st" == "401" ]]; then
  ok H3 "反向对照成立: /api/v1/overview 未鉴权被拒 401 ($(head -c 120 "$bodyf"))"
else
  bad H3 "反向对照失败: /api/v1/overview 未鉴权返回 $st (期望 401) —— H1/H2 的'未鉴权可达'不可信; body=$(head -c 200 "$bodyf")"
fi
rm -f "$bodyf"

# ---- H4 /api/v1/health 是 SPA catch-all 陷阱 ----------------------------------
note "探测 H4 GET /api/v1/health (陷阱: SPA catch-all)"
IFS=$'\t' read -r st ct bodyf <<< "$(http_get /api/v1/health)"
PROBED=$((PROBED+1))
{ echo "GET /api/v1/health -> status=$st content_type=$ct"; echo "--- body (head -c 300) ---"; head -c 300 "$bodyf"; echo; } > "$EVID/H4-apiv1-health-trap.txt"
if grep -qi '<!doctype html' "$bodyf"; then
  ok H4 "/api/v1/health 实测为 SPA HTML 回退 (status=$st ct=$ct) —— 健康端点只有 /health; 判据不得写成'该路径 200 即健康'"
else
  note "H4 信息: /api/v1/health 非 HTML (status=$st ct=$ct body=$(head -c 160 "$bodyf"))"
  ok H4 "/api/v1/health 非 SPA HTML (status=$st ct=$ct) —— 已记录实际形态"
fi
rm -f "$bodyf"

# ---- H4b 负控: 证明"200"本身不是判据, 内容判别才可信 ---------------------------
# 实测(2026-09-16): 本服务的 SPA catch-all 对**任意不存在的路径**都返回 200 + index.html。
# 因此"status==200 就算端点存在/健康"是**天生的假绿发生器**。
# 负控据此设计: 用一个必然不存在的路径探测, 断言它虽然可能是 200, 但**通不过**
# H1/H2 的内容判据(不含 "status" JSON 字段、不含 Prometheus 的 "# HELP")。
NEG_PATH="/__bb_nonexistent_probe_$$"
IFS=$'	' read -r st ct bodyf <<< "$(http_get "$NEG_PATH")"
NEG_HAS_STATUS=0; grep -q '"status"' "$bodyf" && NEG_HAS_STATUS=1
NEG_HAS_HELP=0;   grep -q '^# HELP'   "$bodyf" && NEG_HAS_HELP=1
{ echo "GET $NEG_PATH (负控, 期望: 内容判别失败) -> status=$st content_type=$ct"
  echo "contains_status_json=$NEG_HAS_STATUS contains_prometheus_help=$NEG_HAS_HELP"
  echo "--- body (head -c 200) ---"; head -c 200 "$bodyf"; echo; } > "$EVID/H4b-negative-control.txt"
if [[ "$NEG_HAS_STATUS" -eq 0 && "$NEG_HAS_HELP" -eq 0 ]]; then
  ok H4b "负控成立: 不存在的路径 $NEG_PATH 返回 status=$st 但**通不过**内容判据 (无 status 字段/无 # HELP) —— 证明 H1/H2 靠内容判别, 不靠裸 200"
else
  bad H4b "负控失败: 不存在的路径 $NEG_PATH 竟通过了内容判据 (status_json=$NEG_HAS_STATUS prometheus_help=$NEG_HAS_HELP) —— catch-all 足以伪造健康响应, H1/H2 不可信"
fi
rm -f "$bodyf"

# ---- H5 分母守卫 --------------------------------------------------------------
if [[ "$PROBED" -ge 2 ]]; then
  ok H5 "分母守卫: 本轮实际探测 $PROBED 个端点 (>=2)"
else
  bad H5 "分母守卫失败: 只探测了 $PROBED 个端点 (<2) —— 扫描器可能空转"
fi

# ---- H6 冷启动回填日志 --------------------------------------------------------
LOGS="$(docker logs --tail=4000 "$WEB" 2>&1 || true)"
printf '%s' "$LOGS" > "$EVID/H6-startup-logs.txt"
# 立刻脱敏: 启动日志含一次性凭据, 不能原样留档。
redact_file "$EVID/H6-startup-logs.txt"
WARM="$(printf '%s' "$LOGS" | grep -oE 'Latest value cache warmed up: [0-9]+ rows' | head -1)"
if [[ -n "$WARM" ]]; then
  ok H6 "冷启动回填日志出现: [$WARM]"
else
  bad H6 "冷启动日志未见 'Latest value cache warmed up: N rows'(接线未生效/日志被截断); warm|cache 关键词命中=$(printf '%s' "$LOGS" | grep -ciE 'warm|cache' || true)"
fi

# ---- 汇总 ---------------------------------------------------------------------
{
  echo "probe=04-health.sh"
  echo "project=$PROJECT port=$HOME_PORT db=${BB_DB_NAME:-<unset>}"
  echo "endpoints_probed=$PROBED"
  echo "PASS=$PASS FAIL=$FAIL"
  echo "finished_at=$(date -Is)"
  echo "--- 明细 ---"
  printf '%s\n' "${RESULTS[@]}"
} > "$EVID/result.txt"

# 兜底脱敏: 对证据目录做一次全局扫描(防将来新增的日志落盘又忘记脱敏)。
find "$EVID" -type f \( -name '*.txt' -o -name '*.json' -o -name '*.raw' \) 2>/dev/null | while IFS= read -r f; do redact_file "$f"; done

printf '\n==== 04-health: PASS=%d FAIL=%d endpoints_probed=%d ====\n' "$PASS" "$FAIL" "$PROBED"
[[ "$FAIL" -eq 0 ]] && exit 0 || exit 1
