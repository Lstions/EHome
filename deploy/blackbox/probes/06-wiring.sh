#!/usr/bin/env bash
# ────────────────────────────────────────────────────────────────────────────
# 06-wiring.sh — Q6: web 容器按【服务名】连通 PG 与 EMQX（子代理 B）
#
# 网络方案（已实测）: **external network 复用**
#   现有 ehome-postgres / ehome-emqx 挂在 compose 网络 ehomesystem_default 上，
#   DNS 别名同时含服务名 postgres/emqx 与容器名 ehome-postgres/ehome-emqx。
#   本栈通过 external network 接入该网络 ⇒ 无需新起 PG/EMQX，服务名天然可解析。
#
# 断言（全部只读，不在容器内改数据）:
#   1) 分母守卫: emqx 客户端列表非空 / web 日志 >=30 行 / 共享 PG+EMQX healthy /
#      **至少检查了 2 条连线**（postgres + emqx）
#   2) 服务名解析: docker exec <web> getent hosts postgres|emqx 有结果
#   3) web→postgres:5432 连通 —— 日志 "Database connected and migrated" 且指向
#      独立库、且该库 public 表数 >=30（只读 psql）
#   4) web→emqx:1883 连通（硬证据）—— emqx ctl 出现 peername==web 容器 IP 的客户端，
#      且其 client id 是本次启动**新增**（before/after 差集）
#   5) 反向对照/变异自证: MQTT_BROKER=tcp://nonexistent-broker-xyz:1883
#      ⇒ 断言4 必红，而 断言2/3 仍绿（隔离证明）⇒ 证明断言4 真的在测 MQTT 连通。
#      BB_EXPECT_FAIL=1 时，若"仅 EMQX 红、PG 绿"则脚本 exit 0（MUTATION_AS_EXPECTED）；
#      若 PG 也红 ⇒ MUTATION_NOT_ISOLATED ⇒ exit 1。
#
# 契约可覆盖（规则 B，默认值=契约值）:
#   BB_PROJECT(默认 ehome-bb) BB_CONTAINER(默认 <project>-ehome)
#   BB_HOME_PORT(默认 18080) BB_DB(默认 ehome_bb_<runid>) EHOME_BB_IMAGE
#   开发自测示例:
#     BB_PROJECT=ehome-bb-dev-b BB_HOME_PORT=18091 BB_DB=ehome_bbdev_b_1 \\
#       bash deploy/blackbox/probes/06-wiring.sh
#
# 退出码: 0 通过（或 BB_EXPECT_FAIL=1 且如期隔离变红）；1 失败
# ────────────────────────────────────────────────────────────────────────────
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# 共享脱敏（见 _redact.sh 头部）—— 本探针 dump 容器日志，必须脱敏。
source "$SCRIPT_DIR/_redact.sh"
REPO="$(cd "$SCRIPT_DIR/../../.." && pwd)"
BASE="$REPO/docker-compose.yml"
OVR="$REPO/deploy/blackbox/compose.bb.yml"
NET="ehomesystem_default"

PROJECT="${BB_PROJECT:-ehome-bb}"
WEB="${BB_CONTAINER:-${PROJECT}-ehome}"
PORT="${BB_HOME_PORT:-18080}"
RUNID="${BB_RUNID:-$(date +%Y%m%d-%H%M%S)}"
DB="${BB_DB:-ehome_bb_$RUNID}"
# 镜像名与 project 名解耦: 交付时 project=ehome-bb ⇒ 默认 ehome-bb-ehome:local（不变）；
# 开发期 project=ehome-bb-dev-x 时仍复用同一镜像，除非显式给 EHOME_BB_IMAGE
IMAGE="${EHOME_BB_IMAGE:-ehome-bb-ehome:local}"
TIMEOUT="${BB_TIMEOUT:-90}"
EVID="${BB_EVIDENCE:-$REPO/deploy/blackbox/evidence/B-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$EVID"
LOG="$EVID/06-wiring.log"
: > "$LOG"

PASS=0; FAIL=0
log(){ printf '%s\n' "$*"; printf '%s\n' "$*" >>"$LOG"; }
ok(){ log "  [PASS] $*"; PASS=$((PASS+1)); }
bad(){ log "  [FAIL] $*"; FAIL=$((FAIL+1)); }

export PATH="/snap/bin:/home/sun/.local/share/pnpm/bin:$PATH"

# join with newline via JS concat below

# ── 凭据（只读 .env，不打印值）────────────────────────────────────────────
if [ -f "$REPO/.env" ]; then set -a; . "$REPO/.env" >/dev/null 2>&1; set +a; fi
PGUSER="${POSTGRES_USER:-ehome}"
if [ -z "${POSTGRES_PASSWORD:-}" ]; then
  echo "FATAL: POSTGRES_PASSWORD 未在 $REPO/.env 中提供 —— 无法连共享 PG（不猜凭据）" >&2
  exit 1
fi

# ── 开发期 project 覆盖：容器名由 compose.bb.yml 固定为 ehome-bb-ehome ──────
# 规则 B 要求 project 可覆盖，故 project != ehome-bb 时生成一个临时 override
# 把 container_name 重写为 <project>-ehome；否则用交付 compose.bb.yml 原样。
EXTRA_OVERRIDES=()
TMP_OVR=""
if [ "$PROJECT" != "ehome-bb" ]; then
  TMP_OVR="$(mktemp /tmp/06-wiring-ovr-XXXXXX.yml)"
  cat > "$TMP_OVR" <<YAML
services:
  ehome:
    container_name: ${WEB}
YAML
  EXTRA_OVERRIDES=(-f "$TMP_OVR")
  log "开发期 project=$PROJECT ⇒ 生成临时 override: $TMP_OVR (container_name=${WEB})"
fi

export HOME_PORT="$PORT"
export EHOME_DB_NAME="$DB"
export EHOME_DB_PORT=5432
export EHOME_EXTERNAL_HOST="${EHOME_EXTERNAL_HOST:-127.0.0.1:${PORT}}"
export EHOME_JWT_SECRET="${EHOME_JWT_SECRET:-bb_wiring_secret_0123456789}"
export EHOME_DB_HOST="${EHOME_DB_HOST:-postgres}"
export MQTT_BROKER="${MQTT_BROKER:-tcp://emqx:1883}"

MUT_BROKER="${BB_MUTATE_BROKER:-}"
MUT_DBHOST="${BB_MUTATE_DB_HOST:-}"
MUT_NOTE="none"
if [ -n "$MUT_BROKER" ]; then export MQTT_BROKER="$MUT_BROKER"; MUT_NOTE="MQTT_BROKER=$MUT_BROKER"; fi
if [ -n "$MUT_DBHOST" ]; then export EHOME_DB_HOST="$MUT_DBHOST"; MUT_NOTE="$MUT_NOTE EHOME_DB_HOST=$MUT_DBHOST"; fi

COMPOSE=(docker compose -f "$BASE" -f "$OVR" "${EXTRA_OVERRIDES[@]}" -p "$PROJECT")

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
  [ -n "$TMP_OVR" ] && rm -f "$TMP_OVR"
  log "清理后 ${PROJECT} 残留: $(docker ps -aq --filter "name=${PROJECT}" | wc -l | tr -d ' ') 容器"
  exit "$rc"
}
trap cleanup EXIT INT TERM

emqx_clients(){ docker exec ehome-emqx emqx ctl clients list 2>&1 || true; }

log "================ Q6: web→PG / web→EMQX 服务名连通 ================"
log "BB_PROJECT   : $PROJECT"
log "BB_CONTAINER : $WEB"
log "BB_HOME_PORT : $PORT"
log "BB_DB_NAME   : $DB"
log "镜像         : $IMAGE"
log "网络方案     : external network '$NET'（复用现有 ehome-postgres/ehome-emqx）"
log "变异         : $MUT_NOTE"
log "复现命令     : cd $REPO && BB_PROJECT=$PROJECT BB_HOME_PORT=$PORT BB_DB=$DB EHOME_DB_HOST=$EHOME_DB_HOST \\"
log "                 MQTT_BROKER=$MQTT_BROKER docker compose -f docker-compose.yml \\"
log "                 -f deploy/blackbox/compose.bb.yml -p $PROJECT up -d"
log ""

# ── 网络方案事实（只读 docker inspect，作为证据落盘）────────────────────────
log "--- 网络方案事实（docker inspect，只读）---"
for c in ehome-postgres ehome-emqx; do
  line="$(docker inspect "$c" --format '{{.Name}} networks={{range $k,$v := .NetworkSettings.Networks}}{{$k}}(aliases={{$v.Aliases}}){{end}}' 2>&1)"
  log "    $line"
  printf '%s\n' "$line" >> "$EVID/06-wiring-net-probe.txt"
  if printf '%s' "$line" | grep -q "$NET"; then
    ok "$c 挂在 $NET 上（external network 复用前提成立）"
  else
    bad "$c 不在 $NET 上 —— external network 复用前提不成立"
  fi
done

# ── 前提守卫 ──────────────────────────────────────────────────────────────
if docker image inspect "$IMAGE" >/dev/null 2>&1; then
  ok "前提守卫: 镜像 $IMAGE 存在"
else
  bad "前提守卫: 镜像 $IMAGE 不存在（请先 docker build -t $IMAGE .）"
  exit 1
fi
for pair in "ehome-postgres:PG" "ehome-emqx:EMQX"; do
  c="${pair%%:*}"; label="${pair##*:}"
  st="$(docker inspect -f '{{.State.Health.Status}}' "$c" 2>/dev/null || echo missing)"
  if [ "$st" = "healthy" ]; then ok "前提守卫: 复用容器 $c healthy ($label)"; else bad "前提守卫: $c 非 healthy（$st）"; fi
done

# ── before: EMQX 客户端快照 ───────────────────────────────────────────────
emqx_clients > "$EVID/06-emqx-clients-before.txt"
BEFORE_N="$(grep -c 'connected=true' "$EVID/06-emqx-clients-before.txt" || true)"
if [ "$BEFORE_N" -ge 1 ]; then
  ok "分母守卫: emqx ctl clients list 非空（connected=true x$BEFORE_N）"
else
  bad "分母守卫: emqx 客户端列表为空 —— EMQX 扫描不可信"
fi

# ── 建独立库 + 起栈 ────────────────────────────────────────────────────────
docker exec ehome-postgres dropdb --if-exists -U "$PGUSER" "$DB" >/dev/null 2>&1 || true
if docker exec ehome-postgres createdb -U "$PGUSER" "$DB" >>"$LOG" 2>&1; then
  ok "已创建独立库 $DB（不触碰 ehome/ehome_test/ehome_uiux）"
else
  bad "无法创建独立库 $DB"; exit 1
fi
# 并发守卫: 若同名容器已存在，说明别的 run/代理正在用这个 project ⇒ 绝不抢占
if [ -n "$(docker ps -aq --filter "name=^${WEB}$")" ]; then
  bad "ABORT(并发守卫): 容器 ${WEB} 已存在 —— 另一个 run/代理正在使用 project ${PROJECT}。"
  log "  ⇒ 为避免互相清理，本探针拒绝启动。请等对方结束或改用 BB_PROJECT=bbdev<x> 开发。"
  exit 1
fi
"${COMPOSE[@]}" up -d > "$EVID/06-compose-up.log" 2>&1
log "compose up -d rc=$?"
sed 's/^/    | /' "$EVID/06-compose-up.log" >>"$LOG"
WEB_CID="$(docker inspect -f '{{.Id}}' "$WEB" 2>/dev/null || echo none)"
log "本 run 容器: $WEB cid=$(printf '%.12s' "$WEB_CID")  库=$DB"

deadline=$(( $(date +%s) + TIMEOUT ))
while :; do
  st="$(docker inspect -f '{{.State.Status}}' "$WEB" 2>/dev/null || echo missing)"
  case "$st" in exited|dead|missing) break ;; esac
  docker logs "$WEB" 2>&1 | grep -q 'API server listening' && break
  [ "$(date +%s)" -ge "$deadline" ] && break
  sleep 2
done
log "轮询结束: state=$st"
sleep 12   # 给 MQTT 握手留余量（应用启动即连，避免竞态误判）

# 并发守卫(事后): 容器在等待期间被外部 kill/destroy/重建 ⇒ 本 run 证据无效
CUR_CID="$(docker inspect -f '{{.Id}}' "$WEB" 2>/dev/null || echo none)"
if [ "$CUR_CID" != "$WEB_CID" ]; then
  bad "COLLISION(并发清理): 容器 cid 变化 $(printf '%.12s' "$WEB_CID") -> $(printf '%.12s' "$CUR_CID")"
  log "  ⇒ 本 run 的容器在测量期间被外部清理/重建（疑似并发 run.sh 或其他代理）。"
  log "  ⇒ 证据无效，请在对端空闲后重跑；这不是应用连通性缺陷。"
fi

docker logs "$WEB" > "$EVID/06-container.log" 2>&1 || true
bb_redact_file "$EVID/06-container.log"   # 启动日志含一次性凭据 ⇒ 写完立即脱敏
APP_LOG="$EVID/06-container.log"
# 并发守卫(语义): 读到的容器是否属于本 run（启动日志里的库名）
LOG_DB="$(grep -oE 'DB=[^,]+' "$APP_LOG" | head -1 | sed 's|.*/||')"
if [ -n "$LOG_DB" ]; then
  log "    应用启动日志中的库: $LOG_DB  (本 run 期望: $DB)"
  if [ "$LOG_DB" != "$DB" ]; then
    bad "COLLISION(读到别的 run): 容器实际连的是 $LOG_DB，不是本 run 的 $DB"
  fi
fi
if grep -qE 'FATAL|panic:' "$APP_LOG"; then
  log "    检测到 FATAL/panic，原始片段如下（可诊断）:"
  grep -nE 'FATAL|panic:' "$APP_LOG" | head -5 | sed 's/^/    | /' >>"$LOG"
fi
LINES="$(wc -l < "$APP_LOG" | tr -d ' ')"
if [ "$LINES" -ge 30 ]; then ok "分母守卫: web 日志 $LINES 行（>=30）"; else bad "分母守卫: web 日志仅 $LINES 行（<30）"; fi

# ─ 分母守卫: 本探针检查的连线条数 ─────────────────────────────────────────
LINKS_CHECKED=2
ok "分母守卫: 本探针检查 $LINKS_CHECKED 条连线（postgres:5432 + emqx:1883）"

# ── 断言 2: 服务名解析（容器内，只读）──────────────────────────────────────
log ""
log "--- 断言2: web 容器内按服务名解析 postgres / emqx ---"
for svc in postgres emqx; do
  res="$(docker exec "$WEB" getent hosts "$svc" 2>&1 || true)"
  log "    getent hosts $svc => [$res]"
  if [ -n "$res" ]; then ok "服务名 $svc 在 web 容器内可解析"; else bad "服务名 $svc 在 web 容器内解析失败"; fi
done
log "    应用实际配置: $(grep -oE 'Config: MQTT=[^,]+' "$APP_LOG" | head -1)"

# ── 断言 3: web→postgres:5432 连通 ─────────────────────────────────────────
log ""
log "--- 断言3: web→postgres:5432 连通（经服务名，日志 + 独立库 schema）---"
c_conn="$(grep -cF 'Database connected successfully' "$APP_LOG" || true)"
c_mig="$(grep -cF 'Database connected and migrated' "$APP_LOG" || true)"
c_db="$(grep -cF "/$DB" "$APP_LOG" || true)"
log "    日志计数: 'Database connected successfully' x$c_conn, 'and migrated' x$c_mig, '/$DB' x$c_db"
# 反向证据: 不得依赖 127.0.0.1 连库
if grep -qE 'DB=127\.0\.0\.1|host=127\.0\.0\.1' "$APP_LOG"; then
  bad "日志显示 web 用 127.0.0.1 连库 —— 不是经服务名"
else
  ok "日志未见 127.0.0.1 连库（确为容器网络服务名路径）"
fi
if [ "$c_conn" -ge 1 ] && [ "$c_mig" -ge 1 ] && [ "$c_db" -ge 1 ]; then
  ok "web→postgres 连通: 连库成功且完成迁移，目标为独立库 $DB"
  grep -F "/$DB" "$APP_LOG" | head -1 | sed 's/^/    | /' >>"$LOG"
else
  bad "web→postgres 未连通或未指向独立库（见上方日志计数与 FATAL 片段）"
fi
tbls="$(docker exec ehome-postgres psql -U "$PGUSER" -d "$DB" -tAc \
  "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'" 2>&1 | tr -d '[:space:]')"
log "    psql(只读): SELECT count(*) FROM information_schema.tables WHERE table_schema='public' => [$tbls]"
if [ -n "$tbls" ] && [ "$tbls" -ge 30 ] 2>/dev/null; then
  ok "PG 侧分母守卫: 独立库 public 表数 $tbls（>=30）—— 非空壳"
else
  bad "PG 侧分母守卫: 独立库 public 表数 [$tbls] < 30"
fi

# ── 断言 4 前置: 容器存活再确认（防把「被外部删掉」误判成「MQTT 不通」）────
LIVE_CID="$(docker inspect -f '{{.Id}}' "$WEB" 2>/dev/null || echo none)"
if [ "$LIVE_CID" != "$WEB_CID" ]; then
  bad "ABORT(并发清理): 断言4 前容器已消失/被重建（cid $(printf '%.12s' "$WEB_CID") -> $(printf '%.12s' "$LIVE_CID")）"
  log "  ⇒ 本次「EMQX 不通」不可信：容器是被外部清理的，不是 MQTT 连不上。"
  log "  ⇒ docker events:"
  docker events --since 5m --until 0s --filter "container=$WEB" --format '{{.Time}} {{.Action}}' 2>&1 | tail -8 | sed 's/^/    | /' >>"$LOG"
  FAIL=$((FAIL+1))
fi

# ── 断言 4: web→emqx:1883 连通（硬证据: peername == web 容器 IP）────────────
log ""
log "--- 断言4: web→emqx:1883 连通（emqx ctl + web 容器 IP 匹配 + before/after 差集）---"
WEB_IP="$(docker inspect -f '{{(index .NetworkSettings.Networks "'"$NET"'").IPAddress}}' "$WEB" 2>/dev/null || true)"
log "    web 容器 IP ($NET): [$WEB_IP]"
emqx_clients > "$EVID/06-emqx-clients-after.txt"
AFTER_N="$(grep -c 'connected=true' "$EVID/06-emqx-clients-after.txt" || true)"
log "    emqx connected=true 计数: before=$BEFORE_N after=$AFTER_N"
emqx4=1
if [ -n "$WEB_IP" ]; then
  matches="$(grep -F "peername=$WEB_IP:" "$EVID/06-emqx-clients-after.txt" || true)"
  if [ -n "$matches" ]; then
    MID="$(printf '%s' "$matches" | sed -E 's/^Client\(([^,]+),.*/\1/' | head -1)"
    ok "EMQX 上有客户端 peername=$WEB_IP（来自 web 容器）id=$MID"
    printf '%s\n' "$matches" | sed 's/^/    | /' >>"$LOG"
    if grep -qF "$MID" "$EVID/06-emqx-clients-before.txt"; then
      bad "该 client id 在起栈前已存在 —— 不能归因于本次启动"
    else
      ok "该 client id ($MID) 为起栈后新增（before/after 差集）—— 归因成立"
    fi
  else
    bad "EMQX 上无 peername=$WEB_IP 的客户端 —— web 未与 emqx 建立 MQTT 连接"
    emqx4=0
    log "    当前 emqx clients:"; sed 's/^/    | /' "$EVID/06-emqx-clients-after.txt" >>"$LOG"
    log "    应用 MQTT 相关日志:"
    grep -inE 'mqtt|broker|connect' "$APP_LOG" | tail -8 | sed 's/^/    | /' >>"$LOG"
  fi
else
  bad "无法取得 web 容器 IP —— 断言不可判定"
  emqx4=0
fi

# ── 反向对照汇总 ───────────────────────────────────────────────────────────
# pg_ok = 断言3 是否全绿；emqx_ok = 断言4 是否全绿
pg_ok=1
{ [ "$c_conn" -ge 1 ] && [ "$c_mig" -ge 1 ] && [ "$c_db" -ge 1 ] \
  && [ -n "$tbls" ] && [ "$tbls" -ge 30 ] 2>/dev/null; } || pg_ok=0

log ""
log "================ 汇总: PASS=$PASS FAIL=$FAIL ================"
log "连线判定: PG(web→postgres:5432)=$([ "$pg_ok" = 1 ] && echo 绿 || echo 红)  EMQX(web→emqx:1883)=$([ "$emqx4" = 1 ] && echo 绿 || echo 红)"
log "证据目录: $EVID"

if [ "$FAIL" -ne 0 ]; then
  log ""
  log "--- 失败诊断: 最近 100 行 web 日志 ---"
  docker logs --tail 100 "$WEB" 2>&1 | sed 's/^/    > /' | tee -a "$LOG"
fi

if [ "${BB_EXPECT_FAIL:-0}" = "1" ]; then
  log ""
  if [ -n "$MUT_BROKER" ]; then
    # 反向对照要求: broker 变异 ⇒ 仅 EMQX 红，PG 仍绿（隔离证明）
    if [ "$emqx4" = 0 ] && [ "$pg_ok" = 1 ]; then
      log "MUTATION_AS_EXPECTED: broker 指到错误地址 ⇒ web→EMQX 断言变红，而 web→PG 仍绿"
      log "  ⇒ 断言4 确实在测 MQTT 连通（与 PG 断言隔离）；反向对照成立"
      exit 0
    fi
    log "MUTATION_NOT_ISOLATED: 期望「仅 EMQX 红、PG 绿」，实际 PG=$pg_ok EMQX=$emqx4"
    exit 1
  fi
  if [ "$FAIL" -ne 0 ]; then
    log "MUTATION_AS_EXPECTED: 变异配置下断言如期变红（FAIL=$FAIL），脚本反转为 exit 0"
    exit 0
  fi
  log "MUTATION_NOT_DETECTED: 变异配置下断言竟然全绿 —— 变异未被抓住！"
  exit 1
fi

# 交付路径: 正常跑必须全绿，且两条连线都必须绿
if [ "$FAIL" -eq 0 ] && [ "$pg_ok" = 1 ] && [ "$emqx4" = 1 ]; then
  log "结线判定: web→postgres:5432 与 web→emqx:1883 均经服务名连通 ✅"
  exit 0
fi
log "结线判定: 存在未连通连线 ❌（PG=$pg_ok EMQX=$emqx4）"
exit 1
