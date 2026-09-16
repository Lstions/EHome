#!/usr/bin/env bash
# =============================================================================
# EHomeSystem 部署黑盒验证 —— 编排 / 清理 / 汇总  (子代理 D)
# 方案: docs/分析/部署黑盒验证方案-2026-09-16.md (§3 执行设计 / §5 验收 / §6 边界)
#
# 职责边界: 本脚本【只编排、只清理、只汇总】, 不实现 Q1-Q7 的断言逻辑。
#           断言逻辑属于 probes/*.sh (子代理 A/B/C)。
#           唯一例外: 附加检查 SUP-1(冷启动回填日志), 系主控 2026-09-16 临时指派,
#           不属于原 Q1-Q7, 在报告中单列。
#
# 用法:
#   ./run.sh                       # 全量: 构建 -> compose 契约 -> 冷启动 -> 连通 -> 健康 -> 首次部署
#   ./run.sh --skip-build          # 置 BB_SKIP_BUILD=1 传给探针(由探针决定是否复用镜像)
#   ./run.sh --only 04             # 只跑指定探针(逗号分隔, 支持 id / name / id-name)
#   ./run.sh --only 04,05 --skip-build
#   ./run.sh --list                # 列出探针与依赖顺序
#   ./run.sh --probes-dir <dir>    # 覆盖探针目录(自测编排用)
#
# 探针契约(probes/*.sh 必须遵守):
#   * 以可执行脚本被调用, 无参数; 上下文经环境变量传入(见 run_probe 的 BB_* 导出清单)
#   * 断言失败必须以非 0 退出; 退出码 77 表示"主动 SKIP"
#   * 原始响应/日志片段必须写入 BB_EVIDENCE_DIR/ 供人工诊断
#   * 不得调用 docker compose down / docker rm / DROP DATABASE —— 生命周期由 run.sh 独占
#   * 不得读写 ehome / ehome_test / ehome_uiux / ehome_sim_pg, 不得动共享容器
#
# 硬性安全护栏(本脚本自行实现, 与断言无关):
#   1) 绝不实例化任何会创建 ehome-postgres/ehome-emqx/ehome-web/nginx 同名容器、
#      或占用 80/3080/8082/5432/1883/18083 宿主端口的 compose 配置(解析后校验, 不安全则拒跑)。
#   2) 清理只按 label com.docker.compose.project=ehome-bb 与 ehome-bb- 名字前缀,
#      并对启动时快照的共享容器 ID 做白名单保护。
#   3) DROP DATABASE 仅允许 ^ehome_bb_[A-Za-z0-9_]+$ 且不在保留名单内。
#   4) trap 保证异常路径也执行 down -v + 独立库清理 + 零残留核对。
# =============================================================================
set -uo pipefail

#  路径与常量 ───────────────────────────────────────────────────────────────
BB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$BB_DIR/../.." && pwd)"
PROBES_DIR="$BB_DIR/probes"
COMPOSE_BASE="$REPO_ROOT/docker-compose.yml"
COMPOSE_BB="$BB_DIR/compose.bb.yml"

PROJECT="ehome-bb"
HOME_PORT="${HOME_PORT:-18080}"

START_TS="$(date +%Y%m%d-%H%M%S)"
RUN_ID="D-${START_TS}"
EVID="$BB_DIR/evidence/${RUN_ID}"
BB_DB_NAME="ehome_bb_${START_TS//-/_}"
LOG_FILE="$EVID/run.log"

# 传给 compose: 黑盒独立库。compose.bb.yml 要求 EHOME_DB_NAME 必填且无默认值,
# 从机制上杜绝误连共享库 ehome/ehome_test/ehome_uiux。HOME_PORT 一并导出,
# 保证 compose 的端口插值确定(不依赖调用者是否 export)。
export EHOME_DB_NAME="$BB_DB_NAME"
export HOME_PORT
PROBE_TIMEOUT="${BB_PROBE_TIMEOUT:-900}"
UP_TIMEOUT="${BB_UP_TIMEOUT:-300}"

ONLY_FILTER=""
SKIP_BUILD=0
LIST_ONLY=0

# ── 安全常量 ─────────────────────────────────────────────────────────────────
SHARED_CONTAINERS=(ehome-postgres ehome-emqx nginx ddns-go)
PROTECTED_CONTAINER_NAMES=(ehome-postgres ehome-emqx ehome-web ehome-prometheus ehome-alertmanager nginx ddns-go)
PROTECTED_HOST_PORTS=(80 3080 8082 5432 1883 18083)
RESERVED_DBS=(ehome ehome_test ehome_uiux ehome_sim_pg postgres template0 template1)

#  探针清单: 依赖顺序 01,07 -> 02 -> 03 -> 06 -> 04 -> 05 ───────────────────
STEP_ORDER=(01 07 02 03 06 04 05)
step_name()   { case "$1" in 01) echo image-contract;; 07) echo reproducible;; 02) echo env-contract;; 03) echo coldstart;; 06) echo wiring;; 04) echo health;; 05) echo first-boot;; esac; }
step_q()      { case "$1" in 01) echo Q1;; 07) echo Q7;; 02) echo Q2;; 03) echo Q3;; 06) echo Q6;; 04) echo Q4;; 05) echo Q5;; esac; }
step_needs_stack() { case "$1" in 03|04|05|06) return 0;; *) return 1;; esac; }
step_coldstart()   { [[ "$1" == "03" ]]; }

# ─ 运行时状态 ───────────────────────────────────────────────────────────────
declare -a R_ID R_NAME R_Q R_STATUS R_REASON R_EVID R_PROBE
SHARED_IDS=()
BASE_3080="000"
STACK_UP=0
STACK_REASON=""
COMPOSE_ARGS=()
COMPOSE_RESOLVED=0
APP_IMAGE=""
MAIN_RC=0
SKIP_COUNT=0
FAIL_COUNT=0
RESIDUE_DIRTY=0
SHARED_DIRTY=0
CLEANUP_DONE=0
PG_USER=""
WARMUP_LINE=""
WARMUP_N=""

# ─ 基础工具 ────────────────────────────────────────────────────────────────
log() { printf '%s\n' "$*" | tee -a "$LOG_FILE" >&2; }
section() { log ""; log "── $* ────────────────────────────────────────────────"; }
record() { R_ID+=("$1"); R_NAME+=("$2"); R_Q+=("$3"); R_STATUS+=("$4"); R_REASON+=("$5"); R_EVID+=("$6"); R_PROBE+=("$7"); }

usage() { sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

# ── 参数解析 ─────────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --only)        ONLY_FILTER="${2:-}"; shift 2;;
    --only=*)      ONLY_FILTER="${1#*=}"; shift;;
    --skip-build)  SKIP_BUILD=1; shift;;
    --list)        LIST_ONLY=1; shift;;
    --probes-dir)  PROBES_DIR="${2:-}"; shift 2;;
    --probes-dir=*) PROBES_DIR="${1#*=}"; shift;;
    -h|--help)     usage; exit 0;;
    *) echo "未知参数: $1" >&2; usage >&2; exit 64;;
  esac
done

if [[ $LIST_ONLY == 1 ]]; then
  echo "执行顺序 (依赖顺序):"
  for id in "${STEP_ORDER[@]}"; do
    nm="$(step_name "$id")"; q="$(step_q "$id")"
    f="$(ls "$PROBES_DIR/${id}-"*.sh 2>/dev/null | head -1)"
    st="缺少(将 SKIP)"; [[ -n "$f" ]] && st="$f"
    ns="no"; step_needs_stack "$id" && ns="yes"
    printf '  %s  %-14s %-3s needs_stack=%-3s  %s\n' "$id" "$nm" "$q" "$ns" "$st"
  done
  exit 0
fi

mkdir -p "$EVID" || { echo "无法创建证据目录 $EVID" >&2; exit 66; }
: > "$LOG_FILE"
mkdir -p "$EVID/bin"

# =============================================================================
#  安全护栏
# =============================================================================
_is_protected_name() { local n="$1" p; for p in "${PROTECTED_CONTAINER_NAMES[@]}"; do [[ "$n" == "$p" ]] && return 0; done; return 1; }
_is_reserved_db()    { local n="$1" p; for p in "${RESERVED_DBS[@]}"; do [[ "$n" == "$p" ]] && return 0; done; return 1; }
_is_shared_id()      { local id="$1" p; for p in "${SHARED_IDS[@]}"; do [[ -n "$p" && "$id" == "$p" ]] && return 0; done; return 1; }

capture_baseline() {
  local d="$EVID/00-baseline"; mkdir -p "$d"
  SHARED_IDS=()
  for c in "${SHARED_CONTAINERS[@]}"; do
    local id; id="$(docker inspect -f '{{.Id}}' "$c" 2>/dev/null || true)"
    SHARED_IDS+=("$id")
    docker inspect -f '{{.Name}} {{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}nohealth{{end}}' "$c" \
      >> "$d/containers.txt" 2>&1 || echo "$c MISSING" >> "$d/containers.txt"
  done
  docker ps -a --format '{{.Names}}\t{{.Image}}\t{{.Status}}' > "$d/docker-ps-a.txt" 2>&1
  docker volume ls > "$d/volumes.txt" 2>&1
  docker network ls > "$d/networks.txt" 2>&1
  BASE_3080="$(curl -s -o /dev/null -m 8 -w '%{http_code}' http://127.0.0.1:3080/ 2>/dev/null || echo 000)"
  PG_USER="$(docker inspect ehome-postgres --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null | sed -n 's/^POSTGRES_USER=//p' | head -1)"
  [[ -z "$PG_USER" ]] && PG_USER="postgres"
  {
    echo "run_id=$RUN_ID"
    echo "bb_db_name=$BB_DB_NAME"
    echo "shared_ids=${SHARED_IDS[*]}"
    echo "pg_user=$PG_USER"
    echo "base_3080_http=$BASE_3080"
    echo "baseline_8082_health=$(curl -s -m 8 http://127.0.0.1:8082/health 2>/dev/null || echo ERR)"
    echo "git_head=$(git -C "$REPO_ROOT" rev-parse HEAD 2>/dev/null || echo unknown)"
    echo "docker=$(docker version --format '{{.Server.Version}}' 2>/dev/null)"
    echo "compose=$(docker compose version --short 2>/dev/null)"
  } > "$d/baseline.env"
  log "基线: :3080 -> $BASE_3080 ; :8082/health -> $(curl -s -m 8 http://127.0.0.1:8082/health 2>/dev/null || echo ERR)"
  log "基线: 共享容器 ID 快照 ${SHARED_IDS[*]}"
}

cat > "$EVID/bin/compose_safety.py" <<'PYEOF'
import json, sys
PROTECTED_NAMES = {"ehome-postgres","ehome-emqx","ehome-web","ehome-prometheus",
                   "ehome-alertmanager","nginx","ddns-go"}
PROTECTED_PORTS = {80, 3080, 8082, 5432, 1883, 18083}
def main():
    try:
        cfg = json.load(sys.stdin)
    except Exception as e:
        print("UNSAFE: cannot parse compose config JSON: %s" % e); return 3
    bad = []
    if cfg.get("name") != "ehome-bb":
        bad.append("project name=%r (expected ehome-bb)" % cfg.get("name"))
    svcs = cfg.get("services") or {}
    if not svcs:
        bad.append("no services after resolution")
    for s, v in svcs.items():
        cn = v.get("container_name")
        if cn in PROTECTED_NAMES:
            bad.append("service %s container_name=%s collides with shared container" % (s, cn))
        for p in (v.get("ports") or []):
            pub = p.get("published")
            try: pub = int(pub)
            except Exception: pub = None
            if pub in PROTECTED_PORTS:
                bad.append("service %s publishes protected host port %s" % (s, pub))
    SHARED_NETS = {"ehomesystem_default", "digital-family-tree_default"}
    for n, v in (cfg.get("networks") or {}).items():
        nm = v.get("name") or n
        if nm in SHARED_NETS and not v.get("external"):
            bad.append("network %s -> shared %s without external:true" % (n, nm))
    if bad:
        print("UNSAFE: " + " | ".join(bad)); return 3
    print("OK: services=%s" % ",".join(sorted(svcs)))
    return 0
sys.exit(main())
PYEOF

cat > "$EVID/bin/compose_image.py" <<'PYEOF'
import json, sys
cfg = json.load(sys.stdin)
proj = cfg.get("name") or "ehome-bb"
for s, v in (cfg.get("services") or {}).items():
    img = v.get("image")
    if not img and v.get("build") is not None:
        img = "%s-%s" % (proj, s)
    if img:
        print("%s\t%s" % (s, img))
PYEOF

_resolve_compose() {
  [[ -f "$COMPOSE_BB" ]] || { STACK_REASON="compose.bb.yml 缺失 (子代理 B 未产出)"; return 1; }
  [[ -f "$COMPOSE_BASE" ]] || { STACK_REASON="docker-compose.yml 基线缺失"; return 1; }
  local cand
  for cand in layered standalone; do
    local args=()
    if [[ "$cand" == "layered" ]]; then args=(-f "$COMPOSE_BASE" -f "$COMPOSE_BB"); else args=(-f "$COMPOSE_BB"); fi
    if ! docker compose "${args[@]}" -p "$PROJECT" config -q >"$EVID/00-compose-config-$cand.err" 2>&1; then
      log "compose 候选 [$cand] config 校验失败 (见 00-compose-config-$cand.err)"
      continue
    fi
    local json verdict rc
    json="$(docker compose "${args[@]}" -p "$PROJECT" config --format json 2>/dev/null)" || continue
    printf '%s' "$json" > "$EVID/00-compose-config-$cand.json"
    verdict="$(printf '%s' "$json" | python3 "$EVID/bin/compose_safety.py" 2>&1)"
    rc=$?
    echo "$verdict" > "$EVID/00-compose-config-$cand.safety"
    if [[ $rc -ne 0 ]]; then log "compose 候选 [$cand] 安全校验拒绝 -> $verdict"; continue; fi
    COMPOSE_ARGS=("${args[@]}")
    log "compose 候选 [$cand] 通过: $verdict"
    printf '%s' "$json" | python3 "$EVID/bin/compose_image.py" > "$EVID/00-compose-images.tsv" 2>/dev/null || true
    APP_IMAGE="$(awk -F'\t' -v p="$PROJECT" '$2 ~ ("^" p "-") {print $2; exit}' "$EVID/00-compose-images.tsv" 2>/dev/null || true)"
    return 0
  done
  STACK_REASON="compose.bb.yml 存在但无任何安全配置(见 evidence/00-compose-config-*.safety)"
  return 1
}

_bb_compose() { docker compose "${COMPOSE_ARGS[@]}" -p "$PROJECT" "$@"; }

cat > "$EVID/bin/bb-compose" <<EOF
#!/usr/bin/env bash
exec docker compose ${COMPOSE_ARGS[*]} -p ${PROJECT} "\$@"
EOF
chmod +x "$EVID/bin/bb-compose"

# ─ 独立库(仅 ehome_bb_*) 管理 ──────────────────────────────────────────────
# 注意: 必须显式 < /dev/null。-i 会让 docker exec 继承并**消耗**外层 while 循环
# 的 stdin(here-string), 导致循环在第一轮后提前结束 —— 曾实测导致「只 DROP 了第一个库」。
_psql_rows() { docker exec -i ehome-postgres psql -U "$PG_USER" -d postgres -At -c "$1" </dev/null 2>/dev/null; }
_psql_exec() { docker exec -i ehome-postgres psql -U "$PG_USER" -d postgres -At -c "$1" </dev/null 2>&1; }
_create_bb_db() {
  [[ "$BB_DB_NAME" =~ ^ehome_bb_[A-Za-z0-9_]+$ ]] || { log "拒绝创建非法库名: $BB_DB_NAME"; return 1; }
  _is_reserved_db "$BB_DB_NAME" && { log "拒绝创建保留库名: $BB_DB_NAME"; return 1; }
  docker inspect ehome-postgres >/dev/null 2>&1 || { log "ehome-postgres 不可用, 跳过建库"; return 1; }
  local out; out="$(_psql_exec "CREATE DATABASE \"$BB_DB_NAME\"")"
  log "CREATE DATABASE $BB_DB_NAME -> $out"
  printf '%s\n' "$out" > "$EVID/00-db-create.log"
  _psql_rows "SELECT 1 FROM pg_database WHERE datname='$BB_DB_NAME'" | grep -q 1
}
_drop_bb_dbs() {
  docker inspect ehome-postgres >/dev/null 2>&1 || { log "ehome-postgres 不可用, 跳过独立库清理"; return 0; }
  local names d out dropped=0 listed=0
  names="$(_psql_rows "SELECT datname FROM pg_database WHERE datname LIKE 'ehome_bb_%' ORDER BY datname")"
  listed="$(printf '%s\n' "$names" | grep -c . || true)"
  while IFS= read -r d; do
    [[ -z "$d" ]] && continue
    [[ "$d" =~ ^ehome_bb_[A-Za-z0-9_]+$ ]] || { log "  DROP 跳过(名字不匹配护栏): $d"; continue; }
    _is_reserved_db "$d" && { log "  DROP 拒绝(保留名单): $d"; continue; }
    out="$(_psql_exec "DROP DATABASE IF EXISTS \"$d\" WITH (FORCE)")"
    log "  DROP DATABASE $d -> ${out:-ok}"
    dropped=$((dropped+1))
  done <<< "$names"
  log "独立库清理: 列出 $listed 个, 实际处理 $dropped 个"
  # 分母守卫: 列出与实际处理数不一致 => 清理可能不完整, 显式报警(不用静默默认)
  if [[ "$listed" != "$dropped" ]]; then log "!! 独立库清理不完整: 列出 $listed 个但只处理 $dropped 个 —— 见下方 check_residue 判定"; fi
}

# ── 栈生命周期 ───────────────────────────────────────────────────────────────
_stack_up() {
  local mode="$1" d="$2"
  mkdir -p "$d"
  if [[ "$mode" == "cold" ]]; then
    log "冷启动: 先 down -v 确保干净起点"
    _bb_compose down -v --remove-orphans > "$d/stack-down.log" 2>&1 || true
  fi
  log "启动栈 ($mode): docker compose ${COMPOSE_ARGS[*]} -p $PROJECT up -d --wait"
  _bb_compose up -d --wait --wait-timeout "$UP_TIMEOUT" > "$d/stack-up.log" 2>&1
  local rc=$?
  _bb_compose ps > "$d/stack-ps.log" 2>&1 || true
  docker ps -a --filter "label=com.docker.compose.project=$PROJECT" --format '{{.Names}}\t{{.Status}}\t{{.Ports}}' > "$d/stack-containers.log" 2>&1
  _bb_compose logs --no-color --timestamps --tail=3000 > "$d/stack-logs.txt" 2>&1 || true
  if [[ $rc -eq 0 ]]; then STACK_UP=1; log "栈已就绪(exit=0)"; else STACK_UP=0; log "栈启动失败(exit=$rc) — 原始输出:"; tail -25 "$d/stack-up.log" | sed 's/^/    /' >&2; fi
  return $rc
}

# ── 清理(异常路径也执行) ─────────────────────────────────────────────────────
_remove_bb_containers() {
  local ids id nm
  ids="$( { docker ps -aq --filter "label=com.docker.compose.project=$PROJECT" 2>/dev/null
            docker ps -aq --filter "name=^ehome-bb-" 2>/dev/null; } | sort -u )"
  while IFS= read -r id; do
    [[ -z "$id" ]] && continue
    nm="$(docker inspect -f '{{.Name}}' "$id" 2>/dev/null | sed 's|^/||')"
    if _is_shared_id "$id"; then log "  拒绝删除: $nm ($id) 是共享容器(ID 快照命中)"; continue; fi
    if _is_protected_name "$nm"; then log "  拒绝删除: 受保护容器名 $nm"; continue; fi
    log "  docker rm -f $nm"
    docker rm -f "$id" >/dev/null 2>&1 </dev/null || log "  (rm 失败: $nm)"
  done <<< "$ids"
}
_remove_bb_volumes() {
  local ids v
  ids="$( { docker volume ls -q --filter "label=com.docker.compose.project=$PROJECT" 2>/dev/null
            docker volume ls -q --filter "name=^${PROJECT}_" 2>/dev/null; } | sort -u )"
  while IFS= read -r v; do
    [[ -z "$v" ]] && continue
    case "$v" in *ehome-pgdata|*ehome-emqxdata|*ehome-redisdata) log "  拒绝删除共享卷: $v"; continue;; esac
    log "  docker volume rm $v"
    docker volume rm "$v" >/dev/null 2>&1 </dev/null || log "  (volume rm 失败/在用: $v)"
  done <<< "$ids"
}
_remove_bb_networks() {
  local ids n nm
  ids="$( { docker network ls -q --filter "label=com.docker.compose.project=$PROJECT" 2>/dev/null
            docker network ls -q --filter "name=^${PROJECT}_" 2>/dev/null; } | sort -u )"
  while IFS= read -r n; do
    [[ -z "$n" ]] && continue
    nm="$(docker network inspect -f '{{.Name}}' "$n" 2>/dev/null)"
    case "$nm" in bridge|host|none|ehomesystem_default|digital-family-tree_default) log "  拒绝删除共享网络: $nm"; continue;; esac
    log "  docker network rm $nm"
    docker network rm "$n" >/dev/null 2>&1 </dev/null || log "  (network rm 失败/在用: $nm)"
  done <<< "$ids"
}

do_cleanup() {
  [[ $CLEANUP_DONE == 1 ]] && return 0
  CLEANUP_DONE=1
  local d="$EVID/99-cleanup"; mkdir -p "$d"
  section "清理 (project=$PROJECT)"
  {
    echo "== docker compose down -v =="
    if [[ ${#COMPOSE_ARGS[@]} -gt 0 ]]; then _bb_compose down -v --remove-orphans 2>&1
    else echo "(无安全 compose 配置可用, 改为按 label/名字精确清理)"; fi
    echo "== 按 label/名字精确清理 =="
  } > "$d/cleanup.log" 2>&1
  _remove_bb_containers 2>&1 | tee -a "$d/cleanup.log"
  _remove_bb_volumes    2>&1 | tee -a "$d/cleanup.log"
  _remove_bb_networks   2>&1 | tee -a "$d/cleanup.log"
  _drop_bb_dbs          2>&1 | tee -a "$d/cleanup.log"
  log "清理完成 -> $d/cleanup.log"
}

#  零残留核对 ──────────────────────────────────────────────────────────────
check_residue() {
  local d="$EVID/98-zero-residue"; mkdir -p "$d"
  section "零残留核对"
  local c v net db
  c="$(docker ps -a --format '{{.Names}}\t{{.Label "com.docker.compose.project"}}' | awk -F'\t' -v p="$PROJECT" '$1 ~ ("^" p "-") || $2 == p' | wc -l)"
  v="$( { docker volume ls -q --filter "label=com.docker.compose.project=$PROJECT"; docker volume ls -q --filter "name=^${PROJECT}_"; } | sort -u | grep -c . || true)"
  net="$(docker network ls --format '{{.Name}} {{.Label "com.docker.compose.project"}}' | awk -v p="$PROJECT" '$1 ~ ("^" p "_") || $2 == p' | wc -l)"
  db="$(docker inspect ehome-postgres >/dev/null 2>&1 && _psql_rows "SELECT count(*) FROM pg_database WHERE datname LIKE 'ehome_bb_%'" || echo 0)"
  db="${db:-0}"
  {
    echo "== 残留容器 (label 或 名字前缀) =="
    docker ps -a --format '{{.Names}}\t{{.Status}}\t{{.Label "com.docker.compose.project"}}' | awk -F'\t' -v p="$PROJECT" '$1 ~ ("^" p "-") || $3 == p'
    echo "== 残留卷 =="
    { docker volume ls -q --filter "label=com.docker.compose.project=$PROJECT"; docker volume ls -q --filter "name=^${PROJECT}_"; } | sort -u
    echo "== 残留网络 =="
    docker network ls --format '{{.Name}}\t{{.Label "com.docker.compose.project"}}' | awk -F'\t' -v p="$PROJECT" '$1 ~ ("^" p "_") || $2 == p'
    echo "== 残留独立库 ehome_bb_* =="
    docker inspect ehome-postgres >/dev/null 2>&1 && _psql_rows "SELECT datname FROM pg_database WHERE datname LIKE 'ehome_bb_%' ORDER BY 1" || echo "(PG 不可用, 跳过)"
    echo ""
    echo "RESIDUE containers=$c volumes=$v networks=$net bb_databases=$db"
  } > "$d/residue.txt" 2>&1
  log "残留计数: 容器=$c 卷=$v 网络=$net 独立库=$db"
  if [[ "$c" != 0 || "$v" != 0 || "$net" != 0 || "$db" != 0 ]]; then
    RESIDUE_DIRTY=1
    log "!! 发现残留, 详见 $d/residue.txt"
    tail -30 "$d/residue.txt" | sed 's/^/    /' >&2
  else
    log "零残留: 无 ehome-bb 容器/卷/网络/独立库"
  fi
}

#  共享资源未受影响核对 ─────────────────────────────────────────────────────
check_shared() {
  local d="$EVID/97-shared-health"; mkdir -p "$d"
  section "共享资源未受影响核对"
  local dirty=0 c st hs c3080 body8082
  for c in "${SHARED_CONTAINERS[@]}"; do
    st="$(docker inspect -f '{{.State.Status}}' "$c" 2>/dev/null)"
    hs="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}nohealth{{end}}' "$c" 2>/dev/null)"
    if [[ "$st" != "running" ]]; then dirty=1; log "!! 共享容器 $c 状态异常: ${st:-MISSING}"; fi
    if [[ "$hs" == "unhealthy" ]]; then dirty=1; log "!! 共享容器 $c unhealthy"; fi
  done
  c3080="$(curl -s -o /dev/null -m 8 -w '%{http_code}' http://127.0.0.1:3080/ 2>/dev/null || echo 000)"
  if [[ "$c3080" == "000" ]]; then dirty=1; log "!! :3080 不可达 (基线 $BASE_3080)"; fi
  if [[ "$c3080" != "$BASE_3080" ]]; then log "!! :3080 状态码由 $BASE_3080 变为 $c3080 (已记录)"; fi
  body8082="$(curl -s -m 8 http://127.0.0.1:8082/health 2>/dev/null || echo ERR)"
  if [[ "$body8082" != *'"status":"ok"'* ]]; then dirty=1; log "!! :8082/health 异常: $body8082"; fi
  {
    echo "== 共享容器状态 =="
    for c in "${SHARED_CONTAINERS[@]}"; do
      docker inspect -f '{{.Name}} {{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}nohealth{{end}}' "$c" 2>&1 || echo "$c MISSING"
    done
    echo "== :3080 (DSH GUI) 基线=$BASE_3080 现在=$c3080 =="
    echo "== :8082 (审计后端) =="
    curl -s -m 8 -w '\nHTTP=%{http_code}\n' http://127.0.0.1:8082/health 2>&1 || echo "ERR"
    echo "== 保留库存在性(只读: 仅列出名字, 不读写内容) =="
    docker inspect ehome-postgres >/dev/null 2>&1 && _psql_rows "SELECT datname FROM pg_database WHERE datname IN ('ehome','ehome_test','ehome_uiux','ehome_sim_pg') ORDER BY 1" || echo "(PG 不可用)"
  } > "$d/shared.txt" 2>&1
  [[ $dirty == 0 ]] && log "共享资源均正常: 4 容器 running, :3080=$c3080, :8082/health=ok"
  SHARED_DIRTY=$dirty
}

# =============================================================================
#  探针编排
# =============================================================================
_only_match() {
  local id="$1" name="$2" f
  [[ -z "$ONLY_FILTER" ]] && return 0
  local IFS=','
  for f in $ONLY_FILTER; do
    f="${f// /}"
    [[ "$f" == "$id" || "$f" == "$name" || "$f" == "$id-$name" ]] && return 0
  done
  return 1
}

run_probe() {
  local id="$1" name="$2" q="$3" probe="$4"
  local d="$EVID/${id}-${name}"; mkdir -p "$d"
  section "$id / $name ($q)"
  log "执行: $probe"
  log "证据: $d"
  local envs=(
    "BB_REPO_ROOT=$REPO_ROOT"
    "BB_DIR=$BB_DIR"
    "BB_EVIDENCE_DIR=$d"
    "BB_EVIDENCE=$d"        # 兼容别名: 部分探针(如 02)读 BB_EVIDENCE
    "BB_RUN_ID=$RUN_ID"
    "BB_PROJECT=$PROJECT"
    "BB_HOME_PORT=$HOME_PORT"
    "BB_DB_NAME=$BB_DB_NAME"
    "BB_COMPOSE_BASE=$COMPOSE_BASE"
    "BB_COMPOSE_FILE=$COMPOSE_BB"
    "BB_IMAGE=$APP_IMAGE"
    "BB_APP_IMAGE=$APP_IMAGE"
    "BB_SKIP_BUILD=$SKIP_BUILD"
    "BB_STACK_UP=$STACK_UP"
    "BB_COMPOSE=$EVID/bin/bb-compose"
    "BB_PG_CONTAINER=ehome-postgres"
    "BB_PG_USER=$PG_USER"
    "BB_PG_MAINT_DB=postgres"
    "BB_BASE_3080=$BASE_3080"
  )
  { echo "probe=$probe"; echo "argv=(none; env contract)"; printf '%s\n' "${envs[@]}" | sed 's/^/env /'; echo "cwd=$REPO_ROOT"; } > "$d/meta.txt"
  ( cd "$REPO_ROOT" && env "${envs[@]}" GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" \
      timeout --signal=TERM --kill-after=30 "$PROBE_TIMEOUT" "$probe" ) > "$d/stdout.log" 2> "$d/stderr.log"
  local rc=$?
  echo "$rc" > "$d/rc"
  if [[ -s "$d/stdout.log" ]]; then log "--- $id stdout (tail 40) ---"; tail -40 "$d/stdout.log" | sed 's/^/  | /' >&2; fi
  if [[ -s "$d/stderr.log" ]]; then log "--- $id stderr (tail 40) ---"; tail -40 "$d/stderr.log" | sed 's/^/  ! /' >&2; fi
  return $rc
}

# =============================================================================
#  附加检查 SUP-1 (主控 2026-09-16 指派; 非 Q1-Q7)
#  容器冷启动日志必须出现 "Latest value cache warmed up: N rows"
# =============================================================================
check_cache_warmup() {
  local d="$EVID/S1-cache-warmup"; mkdir -p "$d"
  section "SUP-1 / cache-warmup (附加检查, 非 Q1-Q7)"
  if [[ $STACK_UP != 1 ]]; then
    printf '结论: SKIP (栈未起来, 无冷启动日志可查)\n' > "$d/result.txt"
    record "S1" "cache-warmup" "SUP" "SKIP" "栈未起来, 无冷启动日志" "$d" "-"
    log "[S1] SKIP: 栈未起来"; SKIP_COUNT=$((SKIP_COUNT+1)); return 0
  fi
  local names n hits=0
  names="$(docker ps -a --filter "label=com.docker.compose.project=$PROJECT" --format '{{.Names}}')"
  : > "$d/logs-excerpt.txt"
  while IFS= read -r n; do
    [[ -z "$n" ]] && continue
    local lg line
    lg="$(docker logs --tail=3000 "$n" 2>&1 || true)"
    printf '%s' "$lg" >> "$d/logs-excerpt.txt"
    line="$(printf '%s' "$lg" | grep -oE 'Latest value cache warmed up: [0-9]+ rows' | head -1)"
    if [[ -n "$line" ]]; then
      hits=$((hits+1)); WARMUP_LINE="$line"
      WARMUP_N="$(printf '%s' "$line" | grep -oE '[0-9]+')"
      printf 'container=%s line=%s\n' "$n" "$line" >> "$d/result.txt"
    fi
  done <<< "$names"
  if [[ $hits -gt 0 ]]; then
    record "S1" "cache-warmup" "SUP" "PASS" "观察到 [$WARMUP_LINE] (N=$WARMUP_N)" "$d" "-"
    log "[S1] PASS: 冷启动日志含 [$WARMUP_LINE] (N=$WARMUP_N)"
  else
    {
      echo "结论: FAIL — 未在任何 ehome-bb 容器日志中找到 'Latest value cache warmed up: N rows'"
      echo "容器列表: $names"
      echo "--- 日志片段(含 cache/warm/migrat/error 关键字) ---"
      grep -iE 'cache|warm|migrat|error|panic' "$d/logs-excerpt.txt" | tail -40 || echo "(无匹配关键字)"
    } > "$d/result.txt"
    record "S1" "cache-warmup" "SUP" "FAIL" "冷启动日志未见回填行(见 result.txt/logs-excerpt.txt)" "$d" "-"
    log "[S1] FAIL: 冷启动日志未见 'Latest value cache warmed up: N rows'"
    grep -iE 'cache|warm' "$d/logs-excerpt.txt" | tail -10 | sed 's/^/  ! /' >&2 || true
    FAIL_COUNT=$((FAIL_COUNT+1))
  fi
}

# =============================================================================
#  main
# =============================================================================
main() {
  section "运行开始 run_id=$RUN_ID"
  log "仓库=$REPO_ROOT  探针目录=$PROBES_DIR"
  log "选项: only='${ONLY_FILTER:-<全部>}' skip_build=$SKIP_BUILD"
  log "独立库=$BB_DB_NAME"

  capture_baseline
  if _resolve_compose; then COMPOSE_RESOLVED=1; else log "栈不可用原因: $STACK_REASON"; fi
  if [[ $COMPOSE_RESOLVED == 1 ]]; then _create_bb_db || log "建库未成功(探针可能自行处理)"; fi

  local first_stack_step=1 id name q probe
  for id in "${STEP_ORDER[@]}"; do
    name="$(step_name "$id")"; q="$(step_q "$id")"
    probe="$(ls "$PROBES_DIR/${id}-"*.sh 2>/dev/null | head -1)"
    if ! _only_match "$id" "$name"; then record "$id" "$name" "$q" "SKIP" "--only 未选中" "-" "-"; continue; fi
    if [[ -z "$probe" ]]; then
      record "$id" "$name" "$q" "SKIP" "探针文件缺失: $PROBES_DIR/${id}-*.sh" "-" "缺失"
      log "[$id/$name] SKIP: 探针缺失 (子代理尚未产出)"
      SKIP_COUNT=$((SKIP_COUNT+1)); continue
    fi
    if step_needs_stack "$id"; then
      if [[ $COMPOSE_RESOLVED != 1 ]]; then
        record "$id" "$name" "$q" "SKIP" "$STACK_REASON" "-" "$probe"
        log "[$id/$name] SKIP: $STACK_REASON"; SKIP_COUNT=$((SKIP_COUNT+1)); continue
      fi
      local mode="warm" sd="$EVID/${id}-${name}"
      if step_coldstart "$id" && [[ $first_stack_step == 1 ]]; then mode="cold"; fi
      if [[ $STACK_UP != 1 || "$mode" == "cold" ]]; then
        if ! _stack_up "$mode" "$sd"; then
          record "$id" "$name" "$q" "ERROR" "栈启动失败(见 ${id}-${name}/stack-up.log)" "$sd" "$probe"
          log "[$id/$name] ERROR: 栈启动失败"; FAIL_COUNT=$((FAIL_COUNT+1)); continue
        fi
      fi
      first_stack_step=0
    fi
    run_probe "$id" "$name" "$q" "$probe"; local rc=$?
    if [[ $rc -eq 0 ]]; then
      record "$id" "$name" "$q" "PASS" "exit=0" "$EVID/${id}-${name}" "$probe"; log "[$id/$name] PASS"
    elif [[ $rc -eq 77 ]]; then
      record "$id" "$name" "$q" "SKIP" "探针主动 SKIP (exit=77)" "$EVID/${id}-${name}" "$probe"; log "[$id/$name] SKIP (77)"; SKIP_COUNT=$((SKIP_COUNT+1))
    elif [[ $rc -eq 124 || $rc -eq 137 ]]; then
      record "$id" "$name" "$q" "ERROR" "超时(timeout ${PROBE_TIMEOUT}s, exit=$rc)" "$EVID/${id}-${name}" "$probe"; log "[$id/$name] ERROR: 超时"; FAIL_COUNT=$((FAIL_COUNT+1))
    else
      record "$id" "$name" "$q" "FAIL" "exit=$rc (见 ${id}-${name}/stdout.log, stderr.log)" "$EVID/${id}-${name}" "$probe"; log "[$id/$name] FAIL: exit=$rc"; FAIL_COUNT=$((FAIL_COUNT+1))
    fi
  done

  check_cache_warmup

  section "编排结束: FAIL=$FAIL_COUNT SKIP=$SKIP_COUNT"
  return 0
}

write_report() {
  local final="$1" md="$EVID/SUMMARY.md"
  {
    echo "# 部署黑盒验证 — 编排结果 ($RUN_ID)"
    echo ""
    echo "- 时间: $START_TS"
    echo "- 仓库: $REPO_ROOT @ $(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
    echo "- project: $PROJECT  端口: $HOME_PORT  独立库: $BB_DB_NAME"
    echo "- 选项: --only ${ONLY_FILTER:-全部} / --skip-build=$SKIP_BUILD"
    echo "- 退出码: **$final**  (0=全绿 1=失败/残留/共享异常 2=有 SKIP 未完成)"
    echo ""
    echo "## 命题结果"
    echo ""
    echo "| id | 命题 | 探针 | 结论 | 说明 | 证据 |"
    echo "|---|---|---|---|---|---|"
    local i
    for i in "${!R_ID[@]}"; do
      echo "| ${R_ID[$i]} | ${R_Q[$i]} ${R_NAME[$i]} | $(basename "${R_PROBE[$i]}") | **${R_STATUS[$i]}** | ${R_REASON[$i]} | ${R_EVID[$i]} |"
    done
    echo ""
    echo "## 环境护栏"
    echo ""
    echo "| 检查 | 结果 |"
    echo "|---|---|"
    if [[ $RESIDUE_DIRTY == 0 ]]; then echo "| 零残留 (容器/卷/网络/独立库) | PASS |"; else echo "| 零残留 | DIRTY — 见 98-zero-residue/residue.txt |"; fi
    if [[ $SHARED_DIRTY == 0 ]]; then echo "| 共享资源未受影响 (:3080 / :8082 / 4 容器) | PASS |"; else echo "| 共享资源未受影响 | FAIL — 见 97-shared-health/shared.txt |"; fi
    echo ""
    echo "## 未覆盖 (方案 §6 照抄)"
    echo ""
    echo "- 不做真实 ESP32 硬件验证"
    echo "- 不做生产环境验证 (生产域名/证书/反代不在范围)"
    echo "- 不做压力/容量测试"
    echo "- 不做镜像安全扫描 (只查无密钥泄漏)"
    echo "- 不覆盖 nginx 反代路径 (80 端口被占用, 本方案不经 nginx)"
    echo ""
    echo "## 复现命令"
    echo ""
    echo "    cd $REPO_ROOT"
    echo "    ./deploy/blackbox/run.sh              # 全量"
    echo "    ./deploy/blackbox/run.sh --only 04    # 单条"
    echo "    cat $EVID/run.log                     # 完整编排日志"
  } > "$md"
  cp "$md" "$BB_DIR/evidence/latest-SUMMARY.md" 2>/dev/null || true
  ln -sfn "$RUN_ID" "$BB_DIR/evidence/latest" 2>/dev/null || true
  log "汇总报告: $md"
}

on_exit() {
  local rc=$?
  trap - EXIT INT TERM
  if [[ $MAIN_RC == 0 ]]; then MAIN_RC=$rc; fi
  section "收尾 (main rc=$MAIN_RC)"
  do_cleanup
  check_residue
  check_shared
  local final="$MAIN_RC"
  if [[ $FAIL_COUNT -gt 0 ]]; then final=1; fi
  if [[ $RESIDUE_DIRTY != 0 || $SHARED_DIRTY != 0 ]]; then final=1; fi
  if [[ $final == 0 && -z "$ONLY_FILTER" && $SKIP_COUNT -gt 0 ]]; then final=2; fi
  write_report "$final"
  section "最终退出码: $final  (0=全绿 1=失败/残留/共享异常 2=有 SKIP 未完成)"
  log "证据目录: $EVID"
  exit "$final"
}

trap on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

main
MAIN_RC=$?
exit "$MAIN_RC"
