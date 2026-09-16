#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# 02-env-contract.sh — Q2: compose 必填 env 契约的黑盒验证（子代理 B）
#
# 断言：
#   1) 缺 EHOME_EXTERNAL_HOST ⇒ docker compose config 非零退出，且 stderr 点明该变量
#   2) 缺 EHOME_JWT_SECRET    ⇒ 同上
#   3) 两者都给（+ 其余必填给全）⇒ config --quiet 通过，且渲染出我们的 override
#   4) 分母/前提守卫：repo/.env 确实存在且含被测量；.env.empty 确实存在且为空
#   5) 变异自证（漏检）：不抑制 .env 自动加载时，断言 1/2 会【假绿】(rc=0)
#      —— 证明「必须用 --env-file <空文件> 抑制 .env」不是多余的
#
# 关键事实（已实测）：compose 的 project directory = 第一个 -f 文件的目录，
#   因此 -f <repo>/docker-compose.yml 会自动加载 <repo>/.env。
#   要测「变量缺失」必须用 --env-file deploy/blackbox/probes/.env.empty 抑制它。
#
# 用法：bash deploy/blackbox/probes/02-env-contract.sh
# 退出码：0 = 全部断言通过；1 = 有断言失败
# 证据：$BB_EVIDENCE 或 deploy/blackbox/evidence/B-<ts>/02-env-contract.log
# ─────────────────────────────────────────────────────────────────────────────
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$SCRIPT_DIR/../../.." && pwd)"
ENV_EMPTY="$SCRIPT_DIR/.env.empty"
# 可注入基线文件，便于「破坏基线 ⇒ 断言必红」的自证；默认即仓库真实文件
BASE="${BB_BASE:-$REPO/docker-compose.yml}"
OVR="${BB_OVR:-$REPO/deploy/blackbox/compose.bb.yml}"
PROJECT="ehome-bb"
EVID="${BB_EVIDENCE:-$REPO/deploy/blackbox/evidence/B-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$EVID"
LOG="$EVID/02-env-contract.log"
: > "$LOG"

PASS=0
FAIL=0
log() { printf '%s\n' "$*"; printf '%s\n' "$*" >>"$LOG"; }
ok()  { log "  [PASS] $*"; PASS=$((PASS + 1)); }
bad() { log "  [FAIL] $*"; FAIL=$((FAIL + 1)); }
warn(){ log "  [WARN] $*"; }

export PATH="/snap/bin:/home/sun/.local/share/pnpm/bin:$PATH"

# 所有必填项（POSTGRES_USER/PASSWORD 来自 base；EHOME_DB_NAME 来自我们的 override）
V_USER="POSTGRES_USER=ehome"
V_PASS="POSTGRES_PASSWORD=bb_probe_pw"
V_DB="EHOME_DB_NAME=ehome_bb_envprobe"
V_HOST="EHOME_EXTERNAL_HOST=127.0.0.1:18080"
V_JWT="EHOME_JWT_SECRET=bb_probe_secret_0123456789"

# 复现「变量缺失」= --env-file 空文件（抑制 .env 自动加载）+ env -u（清掉调用方 shell）
UNSET_ARGS=(-u EHOME_EXTERNAL_HOST -u EHOME_JWT_SECRET -u EHOME_DB_NAME
            -u POSTGRES_USER -u POSTGRES_PASSWORD -u HOME_PORT)

compose_cfg() {
  # 用法: compose_cfg [--leak] [--quiet] KEY=VAL...
  local leak=0 quiet=0
  while :; do
    case "${1:-}" in
      --leak)  leak=1; shift ;;
      --quiet) quiet=1; shift ;;
      *) break ;;
    esac
  done
  local envargs=() cfgargs=()
  if [ "$leak" = "1" ]; then envargs=(); else envargs=(--env-file "$ENV_EMPTY"); fi
  if [ "$quiet" = "1" ]; then cfgargs+=("--quiet"); fi
  ( cd "$REPO" && env "${UNSET_ARGS[@]}" "$@" docker compose \
      -f "$BASE" -f "$OVR" -p "$PROJECT" "${envargs[@]}" config "${cfgargs[@]}" 2>&1 )
}

log "================ Q2: compose 必填 env 契约 ================"
log "仓库根     : $REPO"
log "project    : $PROJECT"
log "复现命令(抑制 .env): cd $REPO && docker compose -f docker-compose.yml \\"
log "  -f deploy/blackbox/compose.bb.yml -p ehome-bb --env-file deploy/blackbox/probes/.env.empty config"
log ""

# ── 前提/分母守卫 ───────────────────────────────────────────────────────────
if [ -f "$REPO/.env" ] && grep -q '^EHOME_EXTERNAL_HOST=' "$REPO/.env" \
   && grep -q '^EHOME_JWT_SECRET=' "$REPO/.env"; then
  ok "前提守卫: $REPO/.env 存在且含 EHOME_EXTERNAL_HOST + EHOME_JWT_SECRET（故「不抑制」必然假绿）"
else
  bad "前提守卫不成立: $REPO/.env 缺失或不含被测量 —— 第 5 条漏检自证失去意义"
fi
if [ -f "$ENV_EMPTY" ] && [ ! -s "$ENV_EMPTY" ]; then
  ok "前提守卫: $ENV_EMPTY 存在且为空（$(wc -c <"$ENV_EMPTY" | tr -d ' ') 字节）—— 可抑制 .env"
else
  bad "前提守卫不成立: $ENV_EMPTY 不存在或非空 —— 无法抑制 .env 自动加载"
fi

# ── 断言 1: 缺 EHOME_EXTERNAL_HOST ─────────────────────────────────────────
log ""
log "--- 断言1: 缺 EHOME_EXTERNAL_HOST（期望 rc!=0 且 stderr 点明该变量）---"
out="$(compose_cfg "$V_USER" "$V_PASS" "$V_DB" "$V_JWT")"
rc=$?
log "$out"
if [ "$rc" -ne 0 ]; then
  ok "缺失 EHOME_EXTERNAL_HOST ⇒ compose config rc=$rc（非 0）"
else
  bad "缺失 EHOME_EXTERNAL_HOST 竟然 rc=0 —— compose 契约未生效"
fi
if printf '%s' "$out" | grep -q 'EHOME_EXTERNAL_HOST'; then
  ok "stderr 点明变量名 EHOME_EXTERNAL_HOST（错误可诊断）"
else
  bad "错误信息未点明 EHOME_EXTERNAL_HOST（不可诊断）"
fi
if printf '%s' "$out" | grep -qiE 'required variable|missing a value'; then
  ok "stderr 含 compose 的 required-variable 语义"
else
  bad "stderr 未含 required-variable 语义"
fi

# ── 断言 2: 缺 EHOME_JWT_SECRET ────────────────────────────────────────────
log ""
log "--- 断言2: 缺 EHOME_JWT_SECRET（期望 rc!=0 且 stderr 点明该变量）---"
out="$(compose_cfg "$V_USER" "$V_PASS" "$V_DB" "$V_HOST")"
rc=$?
log "$out"
if [ "$rc" -ne 0 ]; then
  ok "缺失 EHOME_JWT_SECRET ⇒ compose config rc=$rc（非 0）"
else
  bad "缺失 EHOME_JWT_SECRET 竟然 rc=0 —— compose 契约未生效"
fi
if printf '%s' "$out" | grep -q 'EHOME_JWT_SECRET'; then
  ok "stderr 点明变量名 EHOME_JWT_SECRET（错误可诊断）"
else
  bad "错误信息未点明 EHOME_JWT_SECRET（不可诊断）"
fi

# ── 断言 3: 全给 ⇒ config --quiet 通过 ─────────────────────────────────────
log ""
log "--- 断言3: 必填全给 ⇒ config --quiet 通过 ---"
out="$(compose_cfg --quiet "$V_USER" "$V_PASS" "$V_DB" "$V_HOST" "$V_JWT")"
rc=$?
log "(quiet 输出为空属正常) stderr/stdout:[$out]"
if [ "$rc" -eq 0 ]; then
  ok "必填全给 ⇒ config --quiet rc=0"
else
  bad "必填全给却 rc=$rc —— compose 契约过严或配置有误"
  log "$out"
fi
# 分母守卫：渲染出的确实是我们的 override，而不是别的栈
rendered="$(compose_cfg "$V_USER" "$V_PASS" "$V_DB" "$V_HOST" "$V_JWT")"
for needle in "ehome-bb-ehome" "EHOME_DB_NAME: ehome_bb_envprobe" "published: \"18080\"" "ehomesystem_default"; do
  if printf '%s' "$rendered" | grep -qF "$needle"; then
    ok "渲染守卫: config 含 '$needle'"
  else
    bad "渲染守卫: config 未含 '$needle' —— 扫描的不是预期产物"
  fi
done

# ── 断言 5: 变异自证·漏检（不抑制 .env）────────────────────────────────────
log ""
log "--- 断言5: 变异自证 —— 不抑制 .env 时，断言1 变成【假绿】---"
out="$(compose_cfg --leak --quiet "$V_USER" "$V_PASS" "$V_DB" "$V_JWT")"
rc=$?
log "(不抑制 .env) 缺 EHOME_EXTERNAL_HOST 下的 rc=$rc  stderr/stdout:[$out]"
if [ "$rc" -eq 0 ]; then
  ok "漏检复现成功: 不抑制 .env ⇒ rc=0（假绿）。故 02 必须 --env-file 空文件抑制 .env"
  log "  ⇒ 这正是「为什么必须抑制」的证据：默认调用方式下缺变量测不出来"
else
  warn "未复现漏检（rc=$rc）：本机 repo/.env 可能不含这些变量，或 compose 行为已变"
fi

log ""
log "================ 汇总: PASS=$PASS FAIL=$FAIL ================"
log "证据: $LOG"
[ "$FAIL" -eq 0 ] && exit 0 || exit 1
