#!/usr/bin/env bash
# ────────────────────────────────────────────────────────────────────────────
# 03-coldstart.sh — Q3: 容器冷启动全序列（子代理 B）
#
# 断言：
#   1) 起栈后 web 容器在超时内进入 Up (running)，且非 Restarting、非 Exited
#   2) 日志出现启动序列成功迹象（"Database connected and migrated"、
#      "API server listening"），且**不出现** panic/FATAL
#   3) **独立库确实被创建**：只读查询 pg_database + 该库 public 表数 >= 30（分母守卫）
#   4) 分母守卫：拿到 >= 30 行日志、>= 2 条 migration 迹象
#   5) 应用确实连到**我们那个独立库**（启动日志含 DB=.../<DB>）—— 防误连共享库
#   6) 失败时 dump 最近 100 行容器日志（可诊断）
#
# 变异自证（把 EHOME_DB_HOST 改成不存在的名字）：
#   BB_MUTATE_DB_HOST=nonexistent-db-host-xyz bash 03-coldstart.sh
#     ⇒ 容器 Exited(1)，断言必红，脚本退出码 1
#   BB_MUTATE_DB_HOST=... BB_EXPECT_FAIL=1 bash 03-coldstart.sh
#     ⇒ 断言如期变红时脚本反转为 exit 0，并打印 MUTATION_AS_EXPECTED
#
# 用法: bash deploy/blackbox/probes/03-coldstart.sh
# 环境: BB_TIMEOUT(默认90) BB_RUNID BB_DB BB_EVIDENCE BB_MUTATE_DB_HOST BB_EXPECT_FAIL
# 退出码: 0 通过（或 BB_EXPECT_FAIL=1 且如期变红）；1 失败
# ────────────────────────────────────────────────────────────────────────────
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$SCRIPT_DIR/../../.." && pwd)"
BASE="$REPO/docker-compose.yml"
OVR="$REPO/deploy/blackbox/compose.bb.yml"
# project / 容器名 / 端口 / 库名**必须可覆盖**（见 deploy/blackbox/CONTRACT.md 规则 B）。
# 为什么：开发期与交付期若共用同一 project 名，一个代理的 `down -v` 会删掉另一个正在跑的栈 ——
# 上一轮就是这样发生了真实互踩（D 删了 C 的容器）。默认值 = 契约值，覆盖值用于开发自测。
PROJECT="${BB_PROJECT:-ehome-bb}"
WEB="${PROJECT}-ehome"
IMAGE="${EHOME_BB_IMAGE:-${PROJECT}-ehome:local}"
TIMEOUT="${BB_TIMEOUT:-90}"

# project 级排他锁：**同 project 并发会互相删容器**（实测：B 的交付取证连续 3 次被打断）。
# label 精确判据**解决不了** —— 两个调用方都用 -p ehome-bb 时 label 完全相同，
# 故只能串行化。拿不到锁立即失败（不等待），避免多代理互相饿死。
# shellcheck disable=SC1091
source "$SCRIPT_DIR/_lock.sh"
# 共享脱敏（见 _redact.sh 头部：一次真实泄漏事故的教训）—— 每个写日志文件的探针都必须用。
source "$SCRIPT_DIR/_redact.sh"
if ! bb_acquire_lock "$PROJECT"; then
  echo "[03] 无法获得 project '$PROJECT' 排他锁 —— 疑似同 project 并发，拒绝继续（避免互相删容器）" >&2
  exit 3
fi
trap 'bb_release_lock' EXIT

RUNID="${BB_RUNID:-$(date +%Y%m%d-%H%M%S)}"
DB="${BB_DB:-ehome_bb_$RUNID}"
EVID="${BB_EVIDENCE:-$REPO/deploy/blackbox/evidence/B-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$EVID"
LOG="$EVID/03-coldstart.log"
COMPOSE_LOG="$EVID/03-compose-up.log"
: > "$LOG"

PASS=0; FAIL=0
log(){ printf '%s\n' "$*"; printf '%s\n' "$*" >>"$LOG"; }
ok(){ log "  [PASS] $*"; PASS=$((PASS+1)); }
bad(){ log "  [FAIL] $*"; FAIL=$((FAIL+1)); }

export PATH="/snap/bin:/home/sun/.local/share/pnpm/bin:$PATH"

# ─ 载入共享 PG 凭据（只读 .env；不打印其值）────────────────────────────────
if [ -f "$REPO/.env" ]; then set -a; . "$REPO/.env" >/dev/null 2>&1; set +a; fi
PGUSER="${POSTGRES_USER:-ehome}"
if [ -z "${POSTGRES_PASSWORD:-}" ]; then
  echo "FATAL: POSTGRES_PASSWORD 未在 $REPO/.env 中提供 —— 无法连共享 PG（不猜凭据）" >&2
  exit 1
fi
PORT="${BB_HOME_PORT:-18080}"
export HOME_PORT="$PORT"
export EHOME_DB_NAME="$DB"
export EHOME_DB_PORT=5432
export EHOME_EXTERNAL_HOST="${EHOME_EXTERNAL_HOST:-127.0.0.1:$PORT}"
export EHOME_JWT_SECRET="${EHOME_JWT_SECRET:-bb_coldstart_secret_0123456789}"
export EHOME_DB_HOST="${EHOME_DB_HOST:-postgres}"
if [ -n "${BB_MUTATE_DB_HOST:-}" ]; then
  export EHOME_DB_HOST="$BB_MUTATE_DB_HOST"
fi

COMPOSE=(docker compose -f "$BASE" -f "$OVR" -p "$PROJECT")

cleanup() {
  local rc=$?
  log ""
  log "--- 清理 (trap) ---"
  "${COMPOSE[@]}" down --remove-orphans >>"$LOG" 2>&1 || true
  if docker exec ehome-postgres dropdb --if-exists -U "$PGUSER" "$DB" >>"$LOG" 2>&1; then
    log "已 DROP 独立库 $DB"
  else
    log "WARN: DROP 独立库 $DB 失败（需手工清理）"
  fi
  log "清理后 $PROJECT 残留: $(docker ps -aq --filter name=$PROJECT | wc -l | tr -d ' ') 容器"
  exit "$rc"
}
trap cleanup EXIT INT TERM

log "================ Q3: 容器冷启动全序列 ================"
log "时间戳/runid : $RUNID"
log "独立库       : $DB"
log "镜像         : $IMAGE"
log "超时         : ${TIMEOUT}s"
log "EHOME_DB_HOST: $EHOME_DB_HOST    <-- 变异自证关注点"
log "复现命令     : cd $REPO && HOME_PORT=$PORT EHOME_DB_NAME=$DB EHOME_DB_HOST=$EHOME_DB_HOST \\"
log "                 docker compose -f docker-compose.yml -f deploy/blackbox/compose.bb.yml -p $PROJECT up -d"
log ""

#  前提守卫 ──────────────────────────────────────────────────────────────
if docker image inspect "$IMAGE" >/dev/null 2>&1; then
  ok "前提守卫: 镜像 $IMAGE 存在"
else
  bad "前提守卫: 镜像 $IMAGE 不存在（构建属子代理 A；请先 docker build -t $IMAGE .）"
  exit 1
fi
state_shared_pg="$(docker inspect -f '{{.State.Health.Status}}' ehome-postgres 2>/dev/null || echo missing)"
state_shared_mq="$(docker inspect -f '{{.State.Health.Status}}' ehome-emqx 2>/dev/null || echo missing)"
if [ "$state_shared_pg" = "healthy" ]; then
  ok "前提守卫: 复用容器 ehome-postgres healthy"
else
  bad "前提守卫: ehome-postgres 非 healthy（实际 $state_shared_pg）"
fi
if [ "$state_shared_mq" = "healthy" ]; then
  ok "前提守卫: 复用容器 ehome-emqx healthy"
else
  bad "前提守卫: ehome-emqx 非 healthy（实际 $state_shared_mq）"
fi

# ── 建独立库（幂等）────────────────────────────────────────────────────────
docker exec ehome-postgres dropdb --if-exists -U "$PGUSER" "$DB" >/dev/null 2>&1 || true
if docker exec ehome-postgres createdb -U "$PGUSER" "$DB" >>"$LOG" 2>&1; then
  ok "已创建独立库 $DB（黑盒专用；不触碰 ehome/ehome_test/ehome_uiux）"
else
  bad "无法创建独立库 $DB"
  exit 1
fi

# ─ 起栈 ──────────────────────────────────────────────────────────────────
: > "$COMPOSE_LOG"
"${COMPOSE[@]}" up -d >>"$COMPOSE_LOG" 2>&1
log "compose up -d rc=$?  (完整输出: $COMPOSE_LOG)"
sed 's/^/    | /' "$COMPOSE_LOG" >>"$LOG"

# ─ 轮询等待 Up ────────────────────────────────────────────────────────────
deadline=$(( $(date +%s) + TIMEOUT ))
ready=0
st=missing
while :; do
  st="$(docker inspect -f '{{.State.Status}}' "$WEB" 2>/dev/null || echo missing)"
  if [ "$st" = "running" ] && docker logs "$WEB" 2>&1 | grep -q 'API server listening'; then
    ready=1; break
  fi
  case "$st" in exited|dead) break ;; esac
  [ "$(date +%s)" -ge "$deadline" ] && break
  sleep 2
done
log "轮询结束: state=$st ready=$ready"
log ""

# ── 断言 1: Up ────────────────────────────────────────────────────────────
log "--- 断言1: 容器进入 Up（非 Exited/Restarting）---"
st="$(docker inspect -f '{{.State.Status}}' "$WEB" 2>/dev/null || echo missing)"
restarting="$(docker inspect -f '{{.State.Restarting}}' "$WEB" 2>/dev/null || echo n/a)"
if [ "$st" = "running" ] && [ "$restarting" = "false" ]; then
  ok "容器状态 running（Restarting=false）—— 不是 Exited/Restarting"
else
  bad "容器状态=$st Restarting=$restarting —— 未进入 Up"
  log "  docker inspect: $(docker inspect "$WEB" --format 'State={{.State.Status}} ExitCode={{.State.ExitCode}} Error={{.State.Error}}' 2>&1)"
fi
if [ "$ready" = "1" ]; then
  ok "在 ${TIMEOUT}s 超时内观察到 'API server listening'"
else
  bad "超时内未观察到启动完成迹象"
fi

# ─ 断言 2: 启动序列 + 无 panic/FATAL ──────────────────────────────────────
log ""
log "--- 断言2: 启动序列成功迹象；无 panic/FATAL ---"
APP_LOG="$EVID/03-container.log"
docker logs "$WEB" > "$APP_LOG" 2>&1 || true
bb_redact_file "$APP_LOG"   # 启动日志含一次性凭据 ⇒ 写完立即脱敏
LINES="$(wc -l < "$APP_LOG" | tr -d ' ')"
if [ "$LINES" -ge 30 ]; then
  ok "分母守卫: 拿到 $LINES 行容器日志（>=30）"
else
  bad "分母守卫: 只有 $LINES 行容器日志（<30）—— 日志扫描不可信"
fi
for needle in 'Database connected successfully' 'Database connected and migrated' 'API server listening'; do
  n="$(grep -cF "$needle" "$APP_LOG" || true)"
  if [ "$n" -ge 1 ]; then ok "启动序列: 出现 '$needle' (x$n)"; else bad "启动序列: 未出现 '$needle'"; fi
done
mig_lines="$(grep -icE 'migrat' "$APP_LOG" || true)"
if [ "$mig_lines" -ge 2 ]; then
  ok "分母守卫: migration 相关日志 $mig_lines 条（>=2）"
  grep -iE 'migrat' "$APP_LOG" | head -6 | sed 's/^/    | /' >>"$LOG"
else
  bad "分母守卫: migration 相关日志仅 $mig_lines 条（<2）—— AutoMigrate 可能没跑"
fi
bad_lines="$(grep -cE 'FATAL|panic:|runtime error|fatal error' "$APP_LOG" || true)"
if [ "$bad_lines" -eq 0 ]; then
  ok "日志无 panic/FATAL"
else
  bad "日志出现 $bad_lines 行 panic/FATAL —— 冷启动未干净成功"
  grep -nE 'FATAL|panic:|runtime error|fatal error' "$APP_LOG" | head -10 | sed 's/^/    | /' >>"$LOG"
fi

# ── 断言 5: 应用连的确实是我们那个独立库 ───────────────────────────────────
log ""
log "--- 断言5: 应用确实连到独立库（防误连共享库）---"
if grep -qF "/$DB" "$APP_LOG"; then
  ok "启动日志含 '/$DB'（应用连的是本 run 的独立库）"
  grep -F "/$DB" "$APP_LOG" | head -2 | sed 's/^/    | /' >>"$LOG"
else
  bad "启动日志未含 '/$DB' —— 可能连到了别的库"
  grep -iE 'Config: MQTT' "$APP_LOG" | head -2 | sed 's/^/    | /' >>"$LOG"
fi

# ── 断言 3: 独立库确实被创建（只读查询）────────────────────────────────────
log ""
log "--- 断言3: 独立库被创建（docker exec psql 只读查询）---"
exists="$(docker exec ehome-postgres psql -U "$PGUSER" -d postgres -tAc \
  "SELECT count(*) FROM pg_database WHERE datname='$DB'" 2>&1 | tr -d '[:space:]')"
log "    psql: SELECT count(*) FROM pg_database WHERE datname='$DB'  => [$exists]"
if [ "$exists" = "1" ]; then ok "pg_database 中存在 $DB"; else bad "pg_database 中不存在 $DB（实际=[$exists]）"; fi
tbls="$(docker exec ehome-postgres psql -U "$PGUSER" -d "$DB" -tAc \
  "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'" 2>&1 | tr -d '[:space:]')"
log "    psql: SELECT count(*) FROM information_schema.tables WHERE table_schema='public'  => [$tbls]"
if [ -n "$tbls" ] && [ "$tbls" -ge 30 ] 2>/dev/null; then
  ok "分母守卫: 独立库 public 表数 $tbls（>=30）—— 确实建了 schema（非空壳）"
else
  bad "分母守卫: 独立库 public 表数 [$tbls] < 30 —— 可能是空壳/迁移未跑"
fi

log ""
log "================ 汇总: PASS=$PASS FAIL=$FAIL ================"
log "证据目录: $EVID"

if [ "$FAIL" -ne 0 ]; then
  log ""
  log "--- 失败诊断: 最近 100 行容器日志 ---"
  docker logs --tail 100 "$WEB" 2>&1 | sed 's/^/    > /' | tee -a "$LOG"
fi
if [ "${BB_EXPECT_FAIL:-0}" = "1" ]; then
  if [ "$FAIL" -ne 0 ]; then
    log ""
    log "MUTATION_AS_EXPECTED: 变异配置下断言如期变红（FAIL=$FAIL），脚本反转为 exit 0"
    exit 0
  fi
  log "MUTATION_NOT_DETECTED: 变异配置下断言竟然全绿 —— 变异未被抓住！"
  exit 1
fi
[ "$FAIL" -eq 0 ] && exit 0 || exit 1
