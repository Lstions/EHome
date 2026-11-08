#!/usr/bin/env bash
# ============================================================
# drill_unified_stop.sh — unified_data 停写「藏表演练」自动打勾版
#
# 依据: docs/验证/unified_data停写-藏表演练清单-2026-11-07.md（§2 逐接口回归表）
#
# 用法:
#   bash scripts/drill_unified_stop.sh --phase=space
#   bash scripts/drill_unified_stop.sh --phase=time
#   bash scripts/drill_unified_stop.sh --phase=cut
#   bash scripts/drill_unified_stop.sh --all
#   bash scripts/drill_unified_stop.sh --restore
#   bash scripts/drill_unified_stop.sh --verify-manifest
#
# 退出码（四个，必须区分）:
#   0  全部 25 项执行且通过（唯一可以当成"演练通过"的码）
#   1  有失败，或**有未执行且未加 --allow-skip**（没证据 = 不放过）
#   2  有未执行但加了 --allow-skip，或只跑了部分相位（整轮不完整）
#   3  恢复失败 —— 演练本身制造了事故，优先级最高
#
# 为什么 --allow-skip 只能把 1 降成 2、不能降成 0:
#   若它能返回 0，它就成了伪造全绿的工具。
#
# 环境变量（与 backup_pg.sh 同一套，不另起一套）:
#   POSTGRES_USER / POSTGRES_DB / COMPOSE_FILE
#   PG_CONTAINER      置空走 docker compose exec；设为容器名走 docker exec
#   TOKEN / DEVICE_ID / API_BASE   接口回归与恢复核验③需要
#
#   DRILL_PRECHECK_QUERY_SECONDS  预检阈值，默认 10（超过就拒绝演练）
#   DRILL_LOCK_TIMEOUT            演练 DDL 拿锁超时，默认 5s（快速失败）
#   DRILL_STMT_TIMEOUT            演练 DDL 语句超时，默认 60s
#   DRILL_RESTORE_LOCK_TIMEOUT    恢复 DDL 拿锁超时，默认 30s（宁可等）
#   DRILL_RESTORE_STMT_TIMEOUT    恢复 DDL 语句超时，默认 600s
# ============================================================
# 为什么不用 set -e:
#   单条失败必须继续跑后面的，否则第一个红了后面全变未执行，
#   而人只看红字会以为只有第一个有问题。跑完全部再汇总才有鉴别力。
# ============================================================
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
cd "$ROOT_DIR" || { echo "无法进入仓库根目录" >&2; exit 1; }

POSTGRES_USER="${POSTGRES_USER:-ehome}"
POSTGRES_DB="${POSTGRES_DB:-ehome}"
COMPOSE_FILE="${COMPOSE_FILE:-$ROOT_DIR/docker-compose.yml}"
COMPOSE="docker compose -f $COMPOSE_FILE"
# PG_CONTAINER: 置空则走 docker compose exec（生产假设）；设为容器名则走 docker exec。
# 为什么两种都要：生产是 compose 起的（backup_pg.sh 就是这么写的），
# 而本地/CI 常是裸容器（ehome-postgres）。只支持一种会让脚本在另一半环境里跑不起来，
# 然后人就会去改脚本 —— 那是「自己造的运维问题」。
PG_CONTAINER="${PG_CONTAINER:-}"
DOC="docs/验证/unified_data停写-藏表演练清单-2026-11-07.md"

EXIT_OK=0; EXIT_FAIL=1; EXIT_SKIP=2; EXIT_RESTORE_FAILED=3
die() { echo "[演练][致命] $*" >&2; exit 1; }
log() { echo "[演练] $*"; }

# ============================================================
# 锁超时 —— 本脚本最重要的一条护栏
#
# 2026-11-07 生产实测：DETACH PARTITION 需要 ACCESS EXCLUSIVE 锁，
# 而生产**永远有并发写**（每 5 秒一帧）。只要有一个慢查询在跑：
#
#   慢查询持 AccessShareLock  => DETACH 拿不到锁，无限等待
#   而**等锁的 DETACH 会挡住后面所有写**（RowExclusiveLock 也排队）
#   => 遥测写入被阻塞 —— 实测 15 分钟
#
# 注意后半句：真正伤人的不是「DETACH 慢」，是「DETACH 排队时把写者也拦住了」。
# 所以解决办法不是「耐心等」，是**别排队**：拿不到锁就立刻放弃。
#
#   lock_timeout   拿锁最多等多久 => 快速失败，不把写者拖下水
#   statement_timeout  单条语句总量上限 => 兜底（防 ATTACH 验证扫描超长）
#
# 演练脚本在生产上跑，失败模式必须是「快速放弃」而不是「挂住」。
# 挂住的成本 = 阻塞生产；快速放弃的成本 = 重试一次。
# ============================================================
LOCK_TIMEOUT="${DRILL_LOCK_TIMEOUT:-5s}"
STMT_TIMEOUT="${DRILL_STMT_TIMEOUT:-60s}"
# 恢复用的超时更长：恢复必须成功，宁可多等一会儿也不能让数据保持隐藏。
# ATTACH 会扫一遍分区验证约束（无法避免），所以 statement 给足时间。
RESTORE_LOCK_TIMEOUT="${DRILL_RESTORE_LOCK_TIMEOUT:-30s}"
RESTORE_STMT_TIMEOUT="${DRILL_RESTORE_STMT_TIMEOUT:-600s}"
# 预检阈值：多长的查询/多老的事务算「会挡路」
PRECHECK_QUERY_SECONDS="${DRILL_PRECHECK_QUERY_SECONDS:-10}"

# 基线采集的单接口超时。
# 为什么必须短：基线采集是「22 个接口串一遍」，其中 /metrics/summary 的
# points_stored 是全表 GROUP BY —— 生产实测 33.8 秒（17M 行），冷缓存时更长。
# 2026-11-07 实测：drill 采集基线触发那条查询跑了 5 分 37 秒，它持 AccessShareLock，
# **反而把后面的 DETACH 堵住了** —— 演练自己制造了自己要防的故障。
BASELINE_TIMEOUT="${DRILL_BASELINE_TIMEOUT:-8}"

# 回归阶段单接口超时。
# 为什么也要设：judge_response 用 curl -m，超时只是客户端不等了 —— 服务端
# 那条 SQL 还在跑并持锁。回归之后紧接着就是恢复 ATTACH（要 ACCESS EXCLUSIVE），
# 所以回归期间的慢查询同样会堵住恢复。
CHECK_TIMEOUT="${DRILL_CHECK_TIMEOUT:-20}"

# 长窗历史（#4）默认**不发请求**。
#
# 为什么默认关（2026-11-08 Lead 定的 P0）：
#   /devices/:id/history 的 max_points **只减响应体，不减解码堆** ——
#   frameHistoryRowsDownsampled 第一步就是全量 decodeHistoryByName。
#   168h => 解出约 1720 万行 ≈ 3 GB 堆；而生产主机总内存 4 GiB、当时可用 2.9 GiB，
#   容器又是 Memory=0（无限制）=> 一次请求就能 OOM，并可能连带杀掉同机的 PostgreSQL。
#
#   **一个有效 token 就能触发** => 这是活的 DoS，不是性能优化项。
#   解码帧数护栏上线前，演练**不应该**发这个请求。
#
# OBSERVE 只改「判定口径」，不改「请求行为」——
# 所以把 #4 改成观察项本身并不能降低风险，必须**默认不发**。
# 这条很容易搞混，写在这里。
# 护栏验证开关（#4）：发 hours=48 请求，断言被 413 拒。
# **这个是安全的** —— 护栏生效时立即返回、不解码。
LONG_HISTORY_ENABLED="${DRILL_LONG_HISTORY:-0}"
long_history_enabled() { [[ "$LONG_HISTORY_ENABLED" == "1" ]]; }

# 重观察项开关（#25）：真的发 hours=24 无 max_points，会下载 435 MB + 440 MB 服务端堆。
# **这个不安全**，所以和上面那个分成两个开关。
#
# 为什么必须分开（我第一版把它们合并成一个 DRILL_LONG_HISTORY，又是一个坑）：
#   #4 验护栏 = 发请求但**秒拒**（0 成本）
#   #25 测量级 = 发请求且**真的下载 435 MB**（高成本）
#   合并开关 => 想验护栏的人会意外触发 435 MB 下载。
#   这和「OBSERVE 不等于不发请求」是同族错误：
#   **一个开关控制两件后果不同的事。**
#
# #25 的量级已由 Lead 实测写进清单，通常不需要重新测。
OBSERVE_HEAVY_ENABLED="${DRILL_OBSERVE_HEAVY:-0}"
observe_heavy_enabled() { [[ "$OBSERVE_HEAVY_ENABLED" == "1" ]]; }

# 观察项（#25）的超时。
# #25 故意不传 max_points，会拉 435 MB —— 比普通检查慢得多，所以单独放宽。
# 要它跑得动必须给它足够时间，否则「观察失败」会替代掉真正的量级记录。
OBSERVE_TIMEOUT="${DRILL_OBSERVE_TIMEOUT:-120}"
# DETACH 前等待「基线采集引发的查询」真正结束的上限（秒）
SETTLE_MAX_WAIT="${DRILL_SETTLE_MAX_WAIT:-30}"

# 不采集基线的 id：这些接口的数值在本清单 §3 第 4 类里**明确不作正确性判据**
#   #9  overview 的 data_count_today     —— 读 unified_data 的**今日窗口**，
#        停写后恒为 0（不是「贵」，是「值会错」，而 number 0 仍能通过非空判定）
#   #10 metrics/summary 的 points_stored —— 原实现是全表 GROUP BY（生产 33.8s，
#        有竞争时 5 分 37 秒）。2026-11-08 已改从帧表计数（223ms）。
#
# 为什么 #10 已经变快了还继续排除：
#   drill 不该依赖「别人的性能修复有没有落地」。
#   若某天帧表计数退化成慢查询，这条排除让 drill 仍然不会自我阻塞。
#   —— 判据不变，成本就不该由演练承担。
#
# 它们仍做「状态码 + Content-Type + body.code」三关，只是不比形状。
NO_BASELINE_IDS=" 4 9 10 25 "

# ============================================================
# 接口必需的 query 参数（2026-11-07 生产实测逐个核对源码得出）
#
# 原来的脚本用的是「猜参数名」，结果 3 项被判 400，我一度当成端点坏了。
# 实际是参数错：参数名/必填项必须从 handler 源码读，不能猜。
#
#   #11 /sensor-fields          必填 edge_device_id（handler_sensor_fields.go:87
#                               parseEdgeDeviceIDQuery；**不是 device_pk**）
#   #22 /sensor-fields/resolve  必填 edge_device_id + selector（:247 selector 非空）
#   #18 /unified-data/historical   必填 device_pk + category + start_time + end_time
#                               （handler_data.go:253-263）
#   #19 /unified-data/historical-batch 必填 device_pk + categories + start_time + end_time
#                               （handler_data.go:343-360）
#   #21 /data-sources           **复数**。单数 /api/v1/data-source 不存在，
#                               会走 NoRoute 的 SPA 回退 => 200 + HTML（更快地骗过只看状态码）
# ============================================================
# selector 语法见 sensorfield/doc.go：@物理名 / family 名 / prefix_*:max
SENSOR_SELECTOR="${DRILL_SENSOR_SELECTOR:-@soc}"
# historical 需要一个真实存在的 category；默认取 rsoc（所有 BMS 都有）
HIST_CATEGORY="${DRILL_HIST_CATEGORY:-rsoc}"
_hist_from="$(date -u -d '24 hours ago' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v-24H +%Y-%m-%dT%H:%M:%SZ)"
_hist_to="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
# 用 ${DEVICE_ID:-} 而不是 ${DEVICE_ID}：脚本在 set -u 下运行，
# --verify-manifest 等不涉及接口的模式里 DEVICE_ID 根本没设，直接引用会 unbound 崩掉。
# （实测踩过：--verify-manifest 直接报 "DEVICE_ID: unbound variable"。）
_hist_q="device_pk=${DEVICE_ID:-}&category=${HIST_CATEGORY}&start_time=${_hist_from}&end_time=${_hist_to}&agg=raw"
_batch_q="device_pk=${DEVICE_ID:-}&categories=${HIST_CATEGORY}&start_time=${_hist_from}&end_time=${_hist_to}&agg=raw&max_points=0"
# change 类目：changeCategories = {protection_status, fet_status}（history_points.go:106）。
# 用 rsoc 传 agg=change 会被 resolveAggMode 落回 auto，测不到 change 分支。
HIST_CHANGE_CATEGORY="${DRILL_HIST_CHANGE_CATEGORY:-protection_status}"
# ⚠ 注意不要在 _hist_q 后面再拼 "&agg=change" —— _hist_q 已含 agg=raw，
# 拼出来是 agg=raw&agg=change，gin 的 c.Query("agg") 只取第一个 => 仍是 raw。
# 所以 change 版单独一条 query（实测踩过这个坑）。
_hist_q_change="device_pk=${DEVICE_ID:-}&category=${HIST_CHANGE_CATEGORY}&start_time=${_hist_from}&end_time=${_hist_to}&agg=change"

# DEVICE_ID 为空时这些 URL 会带空 device_pk => 400。这是预期的：
# 没有凭据的项会被 need_creds 拦下并登记「未执行」，不会走到这里。

# ============================================================
# MANIFEST —— 唯一真源。EXPECTED 由它派生，不靠执行计数自增。
#
# 格式: "id|phase|项|预期"
#
# 为什么必须硬编码:
#   自计式（CHECKS++）下被跳过/提前 return 的项不进计数 =>
#   「未执行 = 0」恒成立 => 「共 12 项，执行 12 项，0 失败」看起来完美，
#   而清单实际要求 25 项。零未执行 和 零检查 在自计式里同形。
#   这是《测试判据反模式》反模式 8 的第四层。
# ============================================================
MANIFEST=(
  "1~space~GET /nodes/:id/latest~200，values 非空（设备全软删时允许 null）~~0.8"
  "2~space~GET /devices/:id/history?hours=1~200，点数>=基线~.data~0.8"
  "3~space~GET /devices/:id/history?hours=24~200，点数>=基线~.data~0.8"
  "4~space~GET /devices/:id/history?hours=48~413 被内存护栏拒绝（P0 修复验证）~~observe"
  "5~space~GET /unified-data/historical?agg=change~200,points>0~.data~0"
  "6~space~GET /edge-devices/:id/latest-data~200，sensors>=1~json:.data.data_json:.sensors~0.8"
  "7~space~GET /edge-devices/:id/data~200，每行 sensors 非空~.data~0.8"
  "8~space~GET /edge-devices 列表~200，列表非空~.data.items~0.8"
  "9~space~GET /overview~200~.data.latest_data~0.8"
  "10~space~GET /metrics/summary~200~.data~0"
  "11~space~GET /sensor-fields~200，字段列表非空~.data.fields~0.8"
  "12~space~图表 30 组~全部 200 且点数与演练前一致~~0.8"
  "13~space~GET /latest-values~200，sensors>=1~.data~0.8"
  "13b~space~软删设备在 /nodes/:id/latest 消失~符合预期~~0"
  "14~manual~重启服务~能正常启动~~0"
  "15~space~GET /unified-data/categories~200，字段列表非空~.data~0.8"
  "16~space~GET /devices/:id/sensor-data~200，点数与演练前一致~.data~0.8"
  "17~space~GET /devices/:id/history?max_points=100~200，降采样后 <=102 ~.data~<=102"
  "25~space~GET /devices/:id/history?hours=24（不传 max_points）~只记录响应量级，不判定~~observe"
  "18~space~GET /unified-data/historical?raw=1~200，点数一致~.data~0.8"
  "19~space~GET /unified-data/historical-batch 非聚合~200，点数一致~.data~0.8"
  "20~space~GET /data-reports~200，点数一致~.data~0.8"
  "21~space~GET /data-sources~200，列表非空~.data~0.8"
  "22~space~GET /sensor-fields/resolve~200，样本非空~.data.status~0"
  "23~cut~三个 sink 存活 + 停写计数器~四信号 + skip 计数增长，unified 冻结（证据优先取容器 env，非自述）~~0"
  "24~time~coverage 推进判据~max(coverage.day) = UTC 昨天~~0"
)

declare -A RESULT=()
declare -A DETAIL=()
declare -A SKIPREASON=()
RESTORE_FAILED=0; RESTORE_DETAIL=""; RESTORE_DONE=0; DETACHED=0; ALLOW_SKIP=0; FORCE=0
ROWS_BEFORE=""; DETACHED_LIST=""
# 演练状态文件：DETACH 前把基线落盘。
#
# 为什么必须落盘而不是只放内存：
#   ① SIGKILL / 断电时 trap 不执行 => 分区留在游离状态，下次只能单独跑 --restore；
#      若基线只在内存里，--restore 的核验①（行数 == 演练前）必然失败 => 报 exit 3，
#      而数据其实好好的。**假报警和假绿一样有害**：它会训练人忽略 exit 3。
#   ② 重跑 --phase=space 时若分区已游离，count(*) = 0 会被当成基线 =>
#      「恢复后行数 0 == 0」=> 核验①通过，而数据其实还藏着。那是假绿。
STATE_DIR="${TMPDIR:-/tmp}/ehome-drill"
STATE_FILE="$STATE_DIR/${POSTGRES_DB}.state"

# pg_run 是所有 DB 访问的唯一出口：便于一处切换 compose / 裸容器
pg_run() {
  if [[ -n "$PG_CONTAINER" ]]; then
    docker exec -i "$PG_CONTAINER" "$@"
  else
    $COMPOSE exec -T postgres "$@"
  fi
}

# 只读查询的超时通过 PGOPTIONS 注入，**不用** -c "SET ...; SELECT ..."。
#
# 为什么必须带超时（同一类风险的延伸）：
#   SELECT 要 AccessShareLock。PG 锁队列是 FIFO —— 后到的 AccessShare 必须
#   排在前面**尚未授予**的 AccessExclusive 请求之后。所以别的会话有 DETACH
#   在等锁时，脚本自己的 SELECT 也会挂住；一挂住就排进队列，把写者也挡住。
#   实测：那个场景下无超时的 SELECT count(*) 挂住（timeout 124），带超时 2s 报错。
#
# 为什么不用 -c "SET ...; SELECT ..."：
#   那样 psql 会把 SET 的命令标签也打进 stdout（"SET\nSET\n1"），
#   而本函数的所有调用方都假定输出是**纯单列值**。
#   实测踩过：ROWS_BEFORE 变成 "SET\nSET\n100"，非数字 => 判「DB 不可达」。
#   PGOPTIONS 走 libpq 环境变量，不进 stdout，干净。
PGOPTIONS_DRILL="-c lock_timeout=3s -c statement_timeout=30s"
psql_q()  {
  pg_run env PGOPTIONS="$PGOPTIONS_DRILL" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -v ON_ERROR_STOP=1 -c "$1" 2>/dev/null
}
# 演练 DDL 的唯一出口 —— **所有 ALTER TABLE 必须走这里**。
#
# 为什么禁止裸 psql：
#   裸 ALTER 会无限等锁。等锁期间它排在锁队列里，**后面所有读写都被它挡住**
#   —— 2026-11-07 生产实测阻塞遥测写入 15 分钟的就是这个机制。
#   加 SET LOCAL lock_timeout 后，拿不到锁 5 秒就报错退出，队列立刻释放。
#
# SET LOCAL 而不是 SET：LOCAL 只在当前事务内生效，psql -c 每条是一个事务，
# 所以必须把 SET 和 DDL 放进同一条 -c（用分号连），不能分成两次调用。
#
# （这里原来有个裸的 psql_do()，本轮删掉了：它名字看起来像"标准写法"，
#   但没有任何超时。留着它就是给后来人一个看起来正确的坑。）
psql_ddl() {
  local lock_to="$1"; shift
  local stmt_to="$1"; shift
  pg_run psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 \
    -c "SET LOCAL lock_timeout = '$lock_to'; SET LOCAL statement_timeout = '$stmt_to'; $*"
}

# ============================================================
# 预检：报告会挡住 DDL 的会话，而不是盲目去抢锁
#
# DETACH 要 ACCESS EXCLUSIVE，它与**任何**其它锁模式都冲突。
# 所以只要库上有慢查询，DETACH 就必然等；而它一等，写者也跟着等。
#
# 这个函数只做只读查询，不改变任何东西 —— 可以在生产上放心跑。
# 返回 0 = 干净；返回 1 = 有阻塞者（已打印）。
# ============================================================
# 判据用 GREATEST(query_start, xact_start) 而不是只看 query_start。
#
# 为什么：query_start 是**当前这条语句**的开始时间。一个长事务里反复跑短语句时
# query_start 永远很新，但事务从头就持有锁 —— 只看 query_start 会漏掉它。
# xact_start 是事务开始时间。两者取大 = 「这个会话持锁最久有多久」。
#
# 排除 idle：idle 会话不持锁。但**必须包含** "idle in transaction" ——
# 它拿完锁就挂着不动，是 DDL 最经典的阻塞源。
SQL_BLOCKERS="SELECT a.pid,
       round(extract(epoch FROM GREATEST(now()-a.query_start, now()-coalesce(a.xact_start, a.query_start))))::bigint AS secs,
       a.state,
       left(regexp_replace(coalesce(a.query, ''), '\s+', ' ', 'g'), 90)
FROM pg_stat_activity a
WHERE a.datname = current_database()
  AND a.pid <> pg_backend_pid()
  AND a.state <> 'idle'
  AND (now() - coalesce(a.xact_start, a.query_start)) > interval '${PRECHECK_QUERY_SECONDS} seconds'
ORDER BY 2 DESC"

precheck_blockers() {
  local out
  out="$(psql_q "$SQL_BLOCKERS")"
  if [[ -z "$out" ]]; then
    echo "  [OK] 无 >${PRECHECK_QUERY_SECONDS}s 的活动会话"
    return 0
  fi
  echo "  [X] 发现会挡住 DETACH 的活动会话（DETACH 要 ACCESS EXCLUSIVE，与所有锁冲突）："
  echo "$out" | while IFS='|' read -r pid secs state q; do
    printf "        pid=%-8s %5ss  %-10s %s\n" "$pid" "$secs" "$state" "$q"
  done
  echo "        这些都是**只读观察**。要清理请人工确认后 pg_terminate_backend(pid)。"
  echo "        ⚠ 不要为了跑演练就随手杀查询 —— 先看清楚它是什么。"
  return 1
}

# 等锁者检测：如果已经有会话在等 unified_data 的锁，说明有人正在做大操作，
# 这时再去抢锁只会加剧排队。
SQL_LOCK_WAITERS="SELECT w.pid, w.mode, hold.pid AS holder_pid, hold.mode AS holder_mode,
       left(regexp_replace(hold.query, '\s+', ' ', 'g'), 60)
FROM pg_locks w
JOIN pg_locks hold ON hold.locktype = w.locktype AND hold.relation IS NOT DISTINCT FROM w.relation
     AND hold.granted AND NOT (hold.pid = w.pid)
JOIN pg_stat_activity hold ON hold.pid = hold.pid
WHERE NOT w.granted
  AND w.relation IN (SELECT c.oid FROM pg_class c WHERE c.relname IN ('unified_data'))"

mark_pass() { RESULT["$1"]="PASS"; DETAIL["$1"]="${2:-}"; }
mark_fail() { RESULT["$1"]="FAIL"; DETAIL["$1"]="${2:-}"; }
mark_skip() { RESULT["$1"]="SKIP"; SKIPREASON["$1"]="${2:-未执行}"; }
# CONFIG：功能本身没启用（配置项未开），所以**无法**判定它。
# 这不是 PASS（没验），也不是 FAIL（接口没坏）。
# 单列出来是为了不让它污染失败计数 —— 否则「13 个失败」里有几个其实是配置问题，
# 看报告的人会花时间去查根本不存在的 bug。
mark_config() { RESULT["$1"]="CONFIG"; SKIPREASON["$1"]="${2:-功能未启用（配置项未开）}"; }
# OBSERVE：**只记录，不判定**。
#
# 用于「这是已知的设计取舍，不是回归」的场景（比如 #25 的 435 MB 响应）。
# 它不进通过数、不进失败数、不进未执行数，也不影响退出码。
# 但必须在报告里显式标注「只记录不判定」—— 否则下次有人看到它就会当成失败来查。
mark_observe() { RESULT["$1"]="OBSERVE"; DETAIL["$1"]="${2:-（只记录）}"; }

# UNKNOWN：**证据不足，无法判定**。既不是通过，也不是失败。
#
# 与 SKIP（未执行）的区别：
#   SKIP    = 这项**没跑**（缺凭据 / 人工项 / 环境不满足）
#   UNKNOWN = 这项**跑了**，但拿到的观测不足以支持任何一个结论
#
# 为什么要单独一态：合成 SKIP 会让「跑了但没证据」混进「没跑」的堆里，
# 混成 FAIL 会让人去查不存在的缺陷。
# 处置与 SKIP 相同（不返回 0）—— 没有结论就不能上线。
mark_unknown() { RESULT["$1"]="UNKNOWN"; SKIPREASON["$1"]="${2:-证据不足}"; }


# ============================================================
# 恢复：必须幂等（trap 与 --restore 都会调）
# MSG 数组收集问题，最后统一打印 —— 避免在多层函数里拼接换行字符串
# ============================================================
RESTORE_MSGS=()
RESTORE_UNVERIFIED=0

# ⚠ SQL 里的字符串字面量必须用单引号。不要用 Postgres 的 $$ 美元引用 ——
#   在 bash 双引号里 $$ 会被替换成**进程号**，SQL 变成 LIKE 2unified_data_%2，
#   不报错、只是永远查不到（我第一次就踩了这个：演练报「没有找到分区」）。
#   也不要写 \$\$（可读性太差）。SQL 字符串就用 ' 括。
SQL_LOOSE_PARTITIONS="SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relname LIKE 'unified_data_%' AND c.relkind='r' AND NOT EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid=c.oid) ORDER BY c.relname"
SQL_LOOSE_COUNT="SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relname LIKE 'unified_data_%' AND c.relkind='r' AND NOT EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid=c.oid)"
# 仍挂在父表下的分区数（用 pg_inherits 而不是 relname 字符串比较，避免 search_path 问题）
SQL_ATTACHED_COUNT="SELECT count(*) FROM pg_inherits i JOIN pg_class p ON p.oid=i.inhparent WHERE p.relname='unified_data'"

restore_all() {
  [[ "$RESTORE_DONE" -eq 1 ]] && return 0
  log "开始恢复"

  # 恢复前先结算：ATTACH 需要 ACCESS EXCLUSIVE，会被任何在跑的
  # unified_data 查询（AccessShareLock）挡住。回归阶段超时的接口，
  # 其服务端 SQL 可能还在跑 —— 不清干净就 ATTACH 会把恢复本身堵住，
  # 那是最坏情况（数据保持隐藏 + 恢复也卡住）。
  if ! wait_for_quiet >/dev/null 2>&1; then
    log "  [warn] 恢复前仍有 >5s 的 unified_data 查询在跑，ATTACH 可能撞锁（会重试 3 次）"
  fi

  local todo="$DETACHED_LIST"
  todo="$todo $(psql_q "$SQL_LOOSE_PARTITIONS")"
  local seen=""
  for part in $todo; do
    [[ -z "$part" ]] && continue
    case " $seen " in *" $part "*) continue;; esac
    seen="$seen $part"
    local ym="${part#unified_data_}"
    if [[ ! "$ym" =~ ^[0-9]{6}$ ]]; then
      log "  [warn] $part 不符合 unified_data_YYYYMM，需人工 ATTACH"
      continue
    fi
    local y="${ym:0:4}" m="${ym:4:2}"
    local nm ny to
    nm=$((10#$m + 1))
    if [[ "$nm" -gt 12 ]]; then nm=1; ny=$((10#$y + 1)); else ny=$((10#$y)); fi
    to="$(printf %04d-%02d-01 "$ny" "$nm")"
    # 恢复必须成功，所以超时给得比 DETACH 长；但仍然要**有**超时。
    # 没有超时的 ATTACH 同样会挂在锁队列里挡住生产写。
    # 失败重试 3 次（每次都有超时）：第一次撞上长事务，过一会儿可能就散了。
    local attempt=0 ok=0 err=""
    while [[ "$attempt" -lt 3 ]]; do
      attempt=$((attempt+1))
      if err="$(psql_ddl "$RESTORE_LOCK_TIMEOUT" "$RESTORE_STMT_TIMEOUT" \
           "ALTER TABLE unified_data ATTACH PARTITION $part FOR VALUES FROM ('$y-$m-01') TO ('$to')" 2>&1)"; then
        ok=1; break
      fi
      if grep -qi "already.*partition\|is already" <<< "$err"; then
        ok=1; break   # 已挂回父表，视为成功
      fi
      log "  [retry $attempt/3] ATTACH $part 失败: $(echo "$err" | tail -1)"
      sleep 2
    done
    if [[ "$ok" -ne 1 ]]; then
      log "  [X] ATTACH $part 重试 3 次仍失败"
    fi
  done

  # ---- 恢复核验三条，任一失败 => RESTORE_FAILED ----
  local failed=0

  # ① 行数 >= 演练前
  #
  # 为什么是 >= 而不是 ==（2026-11-07 生产实测发现的假报警）：
  #   演练期间应用一直在写。DETACH 后写入报「no partition of relation found」而失败、
  #   行数不涨；但 **ATTACH 完成后写入立刻恢复**，而核验① 是 ATTACH **之后**才跑的
  #   => 中间那几秒的新行被算成「不符」=> 报恢复失败。生产实测差 +22 行。
  #   数据其实完好，恢复也确实成功了。
  #
  #   **假报警和假绿一样有害**：会训练人「exit 3 是正常的，忽略它」。
  #
  # 那为什么不干脆只判 >0：那样就抓不到「真丢数据」。
  # 判据是 [演练前, ∞)：
  #   低于基线 => 真丢了东西 => 失败
  #   高于基线 => 正常写入 => 通过，但把增量打出来（可追溯，不是静默放行）
  if [[ -n "$ROWS_BEFORE" ]]; then
    local rows_after; rows_after="$(psql_q "SELECT count(*) FROM unified_data")"
    if [[ ! "${rows_after:-}" =~ ^[0-9]+$ ]]; then
      RESTORE_MSGS+=("① 读不到恢复后行数（查询失败）")
      failed=1
    elif [[ "$rows_after" -lt "$ROWS_BEFORE" ]]; then
      RESTORE_MSGS+=("① 行数**少于**演练前: 演练前=$ROWS_BEFORE 现在=$rows_after 差=$((ROWS_BEFORE - rows_after)) —— 真丢了数据")
      failed=1
    else
      local delta=$((rows_after - ROWS_BEFORE))
      if [[ "$delta" -gt 0 ]]; then
        log "  核验①：行数 $ROWS_BEFORE => $rows_after（+$delta，演练期间/恢复后的正常写入，判为通过）"
      else
        log "  核验①：行数 $ROWS_BEFORE => $rows_after（无变化）"
      fi
    fi
  else
    RESTORE_MSGS+=("① 没有演练前行数基线，无法核对（这不是通过）")
    failed=1
  fi

  # ② 无游离分区
  local loose_after; loose_after="$(psql_q "$SQL_LOOSE_COUNT")"
  if [[ "${loose_after:-QUERY_FAIL}" != "0" ]]; then
    RESTORE_MSGS+=("② 仍有游离分区: ${loose_after:-查询失败} 个")
    failed=1
  fi

  # ③ 应用侧能查到（比 pg 层更接近用户视角）
  if [[ -n "${TOKEN:-}" && -n "${DEVICE_ID:-}" ]]; then
    # 用 /data-sources：不依赖 unified_data（恢复后要验的是「应用能连上并拿到 JSON」），
    # 参数也简单。**不要**再用 historical-batch 缺参数那套 —— 那会永远 400，
    # 让核验③ 变成永久假报警（2026-11-07 生产实测就是这样）。
    # 同时它走四关判定，「返回 HTML 或空数组」也能被抓出来。
    if ! judge_response "restore" "/api/v1/data-sources" ".data" "0"; then
      RESTORE_MSGS+=("③ 应用侧核验失败: $JUDGE_REASON")
      failed=1
    fi
  else
    # 缺凭据 => ③ **未执行**，不是恢复失败。
    # 这两件事必须分开：混成一个「恢复失败 / exit 3」会训练人忽略 exit 3
    # （和 §3.1 一样：没跑 != 失败，但也不能算通过）。
    RESTORE_UNVERIFIED=1
    RESTORE_MSGS+=("③ 未提供 TOKEN/DEVICE_ID，应用侧核验**未执行**（不计通过，但也不是「恢复失败」）")
  fi

  RESTORE_DONE=1
  if [[ "$failed" -ne 0 ]]; then
    RESTORE_FAILED=1
    log "[warn] 恢复核验**失败**（数据可能没恢复），状态文件保留: $STATE_FILE"
  elif [[ "$RESTORE_UNVERIFIED" -eq 1 ]]; then
    # ①② 都过了（行数没少、无游离分区）=> 数据确实恢复回来了。
    # ③ 只是没凭据验，不影响「数据已恢复」这个事实。
    # 所以：清状态文件（数据确实好了），但**不宣称「核验通过」**，退出码见下。
    rm -f "$STATE_FILE"
    log "[warn] 恢复**已完成**（①②通过），但③应用侧未核验（缺 TOKEN/DEVICE_ID）"
    log "       数据已按库层核验恢复；应用侧是否正常未验证 —— 这不是「恢复失败」。"
  else
    # 只有核验通过才清状态文件。未通过还清掉 => 下次演练会以为环境是干净的。
    rm -f "$STATE_FILE"
    log "恢复核验通过（行数 / 无游离分区 / 应用侧 200），状态文件已清除"
  fi
  return 0
}

RESTORE_PRINTED=0
print_restore_failure() {
  # 幂等：主流程与 trap 都会调它，不防重会把同一份失败打印两遍 ——
  # 读者会以为发生了两次恢复失败。
  [[ "$RESTORE_PRINTED" -eq 1 ]] && return 0
  RESTORE_PRINTED=1
  echo "" >&2
  echo "################ 恢复失败 ################" >&2
  local m
  for m in "${RESTORE_MSGS[@]}"; do echo "  $m" >&2; done
  echo "##########################################" >&2
}

cleanup_on_exit() {
  local rc=$?
  if [[ "$DETACHED" -eq 1 && "$RESTORE_DONE" -eq 0 ]]; then
    log "检测到中途退出（rc=$rc），执行恢复"
    restore_all
  fi
  if [[ "$RESTORE_FAILED" -eq 1 ]]; then
    print_restore_failure
    exit $EXIT_RESTORE_FAILED
  fi
  exit "$rc"
}
trap cleanup_on_exit EXIT INT TERM

# ============================================================
# MANIFEST 自检：与清单文档 §2 的表逐条核对
# 为什么必须有：MANIFEST 是硬编码的，文档会改。两者脱钩时，
# 「脚本说 25 项全过」和「文档要求 27 项」会同时成立而没人发现。
# ============================================================
verify_manifest() {
  if [[ ! -f "$DOC" ]]; then
    echo "[manifest] 清单文档不存在: $DOC" >&2
    return 1
  fi
  # 从 §2 表格抽 id（形如 "| 1 |" / "| **15 🆕** |" / "| 13b |"）
  local doc_ids
  # 按 | 切列取第 2 列，剥掉 markdown 加粗与 emoji，只留数字/b
  doc_ids="$(awk '/^## 2\./{f=1;next} /^## 3\./{f=0} f && /^\|/{print}' "$DOC" \
    | awk -F"|" '{gsub(/^[ \t]+|[ \t]+$/, "", $2); print $2}' \
    | sed -E 's/\*//g; s/[^0-9b]//g' \
    | grep -E "^[0-9]+b?$" | sort -u)"
  local man_ids
  # 注意分隔符是 ~ 不是 | —— 形状字段里有 jq 表达式，里面含 | 管道符号。
  # 用 comm 之前必须两边都 sort -u（comm 要求输入有序，否则报 "not in sorted order" 并静默给错结果）
  man_ids="$(printf "%s\n" "${MANIFEST[@]}" | cut -d"~" -f1 | sort -u)"

  local only_doc only_man rc=0
  only_doc="$(comm -23 <(echo "$doc_ids") <(echo "$man_ids"))"
  only_man="$(comm -13 <(echo "$doc_ids") <(echo "$man_ids"))"

  echo "[manifest] 文档 §2 条目数: $(echo "$doc_ids" | grep -cE "^[0-9]+b?$")"
  echo "[manifest] MANIFEST 条目数 : $(echo "$man_ids" | grep -cE "^[0-9]+b?$")"
  if [[ -n "$only_doc" ]]; then
    echo "[manifest] [X] 文档有、MANIFEST 没有（会漏检）:"
    local x; for x in $only_doc; do echo "        - $x"; done
    rc=1
  fi
  if [[ -n "$only_man" ]]; then
    echo "[manifest] [X] MANIFEST 有、文档没有（会多检/编号已变）:"
    local x; for x in $only_man; do echo "        - $x"; done
    rc=1
  fi
  if [[ "$rc" -eq 0 ]]; then
    echo "[manifest] [OK] 两侧 id 集合一致"
  fi
  echo ""
  echo "[manifest] id -> 项 映射（人工核对用）:"
  local line
  while IFS= read -r line; do
    echo "  $(echo "$line" | cut -d"~" -f1)  $(echo "$line" | cut -d"~" -f2)  $(echo "$line" | cut -d"~" -f3)"
  done <<< "$(printf "%s\n" "${MANIFEST[@]}")"
  return "$rc"
}

# ============================================================
# 通用 HTTP 打点
# ============================================================

# ============================================================
# 响应判定：**状态码 + Content-Type + body.code + 形状**，四关全过才算通过
#
# 为什么不能只看状态码（2026-11-07 生产实测）：
#   演练报「16/18 通过」，但其中 8 项实际是 HTTP 200 + []（空图），
#   还有 1 项（/api/v1/data-source，路由名写错）返回的是 **SPA 前端 HTML，状态码 200**。
#
#   这三类都满足 code==200，所以只看状态码的判定把它们全放了：
#     · 空数组       => 用户看到空图，系统认为一切正常
#     · HTML 页面    => 连 API 都没打到，拿到一个网页当成功
#     · body.code 错误 => HTTP 200 但业务码失败
#
# 四关：
#   ① HTTP 状态 == 200
#   ② Content-Type 含 application/json   ← 这条直接抓住 SPA 回退
#   ③ body.code（若有）== 200            ← 抓住「HTTP 200 但业务码错」
#   ④ 声明路径的值「尺寸」>= 下界          ← 抓住空数组/半截数据
#
# 第 ④ 关的下界来自**演练前的基线**（见 capture_baseline）：
#   ratio=0   => 只要求非空
#   ratio=0.8 => 要求 >= 基线的 80%
# 后者才叫「点数与演练前一致」；只判 >0 的话 1 条也算过。
# ============================================================
JUDGE_REASON=""

# JSON 处理：优先 jq，其次 python3。两者都没有 => 致命（不能静默降级成只看状态码）
if command -v jq >/dev/null 2>&1; then
  JSON_TOOL="jq"
elif command -v python3 >/dev/null 2>&1; then
  JSON_TOOL="python3"
else
  JSON_TOOL=""
fi

# json_size <file> <形状声明>  => 打印该形状的「尺寸」
#
# 三种声明：
#   （空）                      不判形状
#   .data.values                路径 —— 取该路径的值算尺寸
#   json:.data.data_json:.sensors  嵌套 —— 外层是**JSON 字符串**，解析后取内层路径算尺寸
#
# 为什么要有 json: 这一种：
#   #6 /latest-data 的 sensors 藏在 .data.data_json（一段 JSON 字符串）里面。
#   只判「字符串非空」抓不到「串里一个传感器都没有」。
#
# ⚠⚠ 这个函数**必须两套后端（jq / python3）行为完全一致**。
#   2026-11-07 踩过：我加了 expr:（jq 表达式）只实现了 jq 分支，
#   python3 回退把它当路径名 -> 返回 -1 -> #6 在无 jq 的环境上必然失败。
#   而我的机器有 jq，所以本地全绿 —— **环境差异导致的假通过**。
#   所以现在只留**可移植的声明式形式**，不用表达式；并且开跑前有 json_tool_selftest。
json_size() {
  local file="$1" apath="$2"
  if [[ -z "$apath" ]]; then echo 0; return 0; fi

  if [[ "$apath" == json:* ]]; then
    # 嵌套：json:<外层路径>:<内层路径>
    local body="${apath#json:}"
    local outer="${body%%:*}" inner="${body#*:}"
    if [[ "$JSON_TOOL" == "jq" ]]; then
      jq -r --arg o "$outer" --arg i "$inner" '
        def sz: if type=="array" then length
                elif type=="object" then (keys|length)
                elif type=="string" then (if length>0 then 1 else 0 end)
                elif type=="number" or type=="boolean" then 1
                else 0 end;
        def gp($p): getpath($p|split(".")|map(select(length>0)|(tonumber? // .)));
        (gp($o)) as $s |
        if ($s|type) != "string" then -1
        else (($s|fromjson) | gp($i)) as $v | if $v==null then -1 else ($v|sz) end
        end
      ' "$file" 2>/dev/null || echo -2
    else
      python3 - "$file" "$outer" "$inner" <<'PYEOF' 2>/dev/null || echo -2
import json, sys
def gp(cur, path):
    for part in path.split("."):
        if part == "":
            continue
        if isinstance(cur, list):
            cur = cur[int(part)]
        elif isinstance(cur, dict):
            cur = cur.get(part)
        else:
            return None
        if cur is None:
            return None
    return cur
data = json.load(open(sys.argv[1], encoding="utf-8"))
s = gp(data, sys.argv[2])
if not isinstance(s, str):
    print(-1); sys.exit(0)
try:
    inner = json.loads(s)
except Exception:
    print(-2); sys.exit(0)
v = gp(inner, sys.argv[3])
if v is None:
    print(-1)
elif isinstance(v, (list, dict)):
    print(len(v))
elif isinstance(v, str):
    print(1 if v else 0)
elif isinstance(v, bool) or isinstance(v, (int, float)):
    print(1)
else:
    print(0)
PYEOF
    fi
    return 0
  fi

  # 普通路径
  if [[ "$JSON_TOOL" == "jq" ]]; then
    jq -r --arg p "$apath" '
      def sz: if type=="array" then length
              elif type=="object" then (keys|length)
              elif type=="string" then (if length>0 then 1 else 0 end)
              elif type=="number" or type=="boolean" then 1
              else 0 end;
      (getpath($p|split(".")|map(select(length>0)|(tonumber? // .))) // null) | if .==null then -1 else (sz) end
    ' "$file" 2>/dev/null || echo -2
  else
    python3 - "$file" "$apath" <<'PYEOF' 2>/dev/null || echo -2
import json, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
cur = data
for part in sys.argv[2].split("."):
    if part == "":
        continue
    if isinstance(cur, list):
        cur = cur[int(part)]
    elif isinstance(cur, dict):
        cur = cur.get(part)
    else:
        cur = None
        break
    if cur is None:
        break
if cur is None:
    print(-1)
elif isinstance(cur, (list, dict)):
    print(len(cur))
elif isinstance(cur, str):
    print(1 if cur else 0)
elif isinstance(cur, bool) or isinstance(cur, (int, float)):
    print(1)
else:
    print(0)
PYEOF
  fi
}

# json_code <file> => 打印 body.code（不存在打印空）
json_code() {
  local file="$1"
  if [[ "$JSON_TOOL" == "jq" ]]; then
    jq -r 'if has("code") then (.code|tostring) else "" end' "$file" 2>/dev/null || echo ""
  else
    python3 -c 'import json,sys; d=json.load(open(sys.argv[1],encoding="utf-8")); print(d["code"] if "code" in d else "")' "$file" 2>/dev/null || echo ""
  fi
}

# judge_response <id> <path> <assert_path> <ratio>
# 返回 0=通过 1=失败；失败原因放 JUDGE_REASON；观测到的尺寸放 JUDGE_SIZE
JUDGE_SIZE=0
judge_response() {
  local id="$1" path="$2" apath="$3" ratio="$4"
  JUDGE_REASON=""; JUDGE_SIZE=0
  local url="${API_BASE:-http://127.0.0.1:18090}${path}"
  local tmp; tmp="$(mktemp)"
  local meta
  meta="$(curl -s -o "$tmp" -w '%{http_code}|%{content_type}' -m "$CHECK_TIMEOUT" \
           -H "Authorization: Bearer ${TOKEN:-}" "$url" 2>/dev/null || echo "TIMEOUT|")"
  local code="${meta%%|*}" ctype="${meta#*|}"
  local body; body="$(head -c 4000 "$tmp")"

  # ① 状态码
  #
  # ⚠ curl -m 超时时 %{http_code} 输出的是 **000**（而不是空），
  #   而 '|| echo TIMEOUT|' 只在 curl 非 0 退出时才追加 —— 两者会拼成 "000|TIMEOUT|"。
  #   所以必须同时判 000 和 TIMEOUT，只判后者抓不到超时（实测踩过）。
  if [[ "$code" == "TIMEOUT" || "$code" == "000" ]]; then
    # 超时单独说，不与「HTTP 500」混在一起：它意味着**服务端那条 SQL 还在跑**，
    # 而它持着 AccessShareLock —— 紧接着的恢复 ATTACH 会被它挡住。
    JUDGE_REASON="接口 >${CHECK_TIMEOUT}s 未响应（客户端已放弃，但**服务端查询可能还在跑并持锁**）"
    rm -f "$tmp"; return 1
  fi
  if [[ "$code" != "200" ]]; then
    # 「功能未启用」不是「功能坏了」。
    # 2026-11-07 生产实测：#22 /sensor-fields/resolve 返 400，
    # body 是 'sensorfield: field resolver is disabled' —— 配置项没开，
    # 与 unified_data 退休毫无关系。记成失败会让人去查不存在的 bug；
    # 记成通过更是假绿。所以单列 CONFIG 状态，既不算通过也不计入失败。
    if [[ "$body" == *field_resolver_disabled* || "$body" == *"resolver is disabled"* ]]; then
      JUDGE_REASON="功能未启用（配置项未开）：$(echo "$body" | tr '
' ' ' | cut -c1-70)"
      rm -f "$tmp"; return 2
    fi
    JUDGE_REASON="HTTP $code（期望 200）$( [[ -n "$body" ]] && echo " body: $(echo "$body" | tr '\n' ' ' | cut -c1-80)" )"
    rm -f "$tmp"; return 1
  fi
  # ② Content-Type
  if [[ "$ctype" != *application/json* ]]; then
    JUDGE_REASON="HTTP 200 但 Content-Type=${ctype:-空}，不是 JSON ⇒ 很可能打到 SPA/静态页，API 根本没命中。body 前 80 字: $(echo "$body" | tr '\n' ' ' | cut -c1-80)"
    rm -f "$tmp"; return 1
  fi
  # ③ body.code
  local bcode; bcode="$(json_code "$tmp")"
  if [[ -n "$bcode" && "$bcode" != "200" ]]; then
    JUDGE_REASON="HTTP 200 但 body.code=$bcode（期望 200）"
    rm -f "$tmp"; return 1
  fi
  # ④ 形状 / 下界
  if [[ -n "$apath" ]]; then
    local size; size="$(json_size "$tmp" "$apath")"
    JUDGE_SIZE="$size"
    if [[ "$size" == "-1" ]]; then
      JUDGE_REASON="响应里没有 \`$apath\` 这个路径"
      rm -f "$tmp"; return 1
    elif [[ "$size" == "-2" ]]; then
      JUDGE_REASON="JSON 解析失败（不是合法 JSON？）"
      rm -f "$tmp"; return 1
    fi
    # 三种判据（ratio 字段）：
    #   数字     下界 = 基线 x ratio（比「少没少」）
    #   <=N      绝对上界（比「多没多」—— 降采样封顶的场景）
    #   0        只要求非空
    if [[ "$ratio" == "<="* ]]; then
      local cap="${ratio#<=}"
      if [[ "$size" -gt "$cap" ]]; then
        JUDGE_REASON="\`$apath\` 尺寸=$size > 上界 $cap（降采样应当封顶，返回更多说明 max_points 没生效）"
        rm -f "$tmp"; return 1
      fi
    else
      local baseline="${HTTP_BASE[$id]:-}"
      local lower=1
      if [[ -n "$baseline" && "$ratio" != "0" ]]; then
        lower="$(awk -v b="$baseline" -v r="$ratio" 'BEGIN{v=int(b*r); if(v<1)v=1; print v}')"
      fi
      if [[ "$size" -lt "$lower" ]]; then
        JUDGE_REASON="\`$apath\` 尺寸=$size < 下界 $lower$( [[ -n "$baseline" ]] && echo "（演练前基线=$baseline，要求>=${ratio}）" )"
        rm -f "$tmp"; return 1
      fi
    fi
  fi
  rm -f "$tmp"; return 0
}

# 兼容旧调用名（内部全部走 judge_response）
http_code() {
  curl -s -o /dev/null -w "%{http_code}" -m 25 -H "Authorization: Bearer ${TOKEN:-}" "$1" 2>/dev/null || echo "ERR"
}

# need_creds：接口回归需要凭据。缺了就统一登记「未执行」，绝不静默通过。
# check_token_alive：开跑前先验一次 token 有效
#
# 为什么要有：token 过期时 22 个接口会**全部** 401，
# 报告里是 22 条一模一样的失败 —— 那是噪声，会掩盖真正的接口问题。
# 而且演练本身要跑几分钟（基线采集 + DETACH + 回归），
# 期间 token 过期也是真实的失败模式。
#
# 判据用 /api/v1/data-sources（不读 unified_data、参数最简）。
check_token_alive() {
  local url="${API_BASE:-http://127.0.0.1:18090}/api/v1/data-sources"
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' -m "$CHECK_TIMEOUT" \
           -H "Authorization: Bearer ${TOKEN:-}" "$url" 2>/dev/null || echo "000")"
  if [[ "$code" == "401" ]]; then
    echo "  [X] token 无效或已过期（/api/v1/data-sources => 401）"
    echo "      22 个接口都会因此 401，不逐个跑 —— 那样只会得到 22 条一样的噪声。"
    return 1
  fi
  if [[ "$code" == "000" ]]; then
    echo "  [X] API 不可达（>${CHECK_TIMEOUT}s 无响应）：${API_BASE:-http://127.0.0.1:18090}"
    return 1
  fi
  echo "  [OK] token 有效，API 可达（/api/v1/data-sources => $code）"
  return 0
}

need_creds() {
  if [[ -z "${TOKEN:-}" || -z "${DEVICE_ID:-}" ]]; then
    echo "缺少 TOKEN / DEVICE_ID，接口回归未执行" >&2
    return 1
  fi
  return 0
}

# HTTP_BASE[id] = 演练前观测到的形状尺寸（capture_baseline 填）
declare -A HTTP_BASE=()

# ============================================================
# run_observe_check <id> <path>：**只记录，不判定**
#
# 用于「已知的设计取舍，不是回归」的场景。当前只有 #25：
#   /devices/:id/history?hours=24 不传 max_points
#   => 24h 的全类目全采样，服务端要物化约 440 MB，响应 435 MB
#
# 为什么不判定：
#   这不是 bug，是「调用方没传 max_points」的后果（前端根本不调这个接口）。
#   产品决策是「文档写明长窗必须传 max_points」而不是服务端静默 cap。
#   把它判成失败，会让人反复去查一个已经定案的取舍。
#
# 但也不能因此**不记录**：量级本身是信息 ——
#   如果哪天它从 435 MB 变成 4 GB，那才是真问题。
# 所以：记录 size_download 与 time_total，永不判失败。
#
# ⚠ 这个请求本身会拉 435 MB。基线采集**不采它**（NO_BASELINE_IDS 归一类处理），
#   而且它只在 space 相位跑一次。
# ============================================================
run_observe_check() {
  local id="$1" path="$2"
  local url="${API_BASE:-http://127.0.0.1:18090}${path}"
  local meta
  meta="$(curl -s -o /dev/null -w '%{http_code}|%{size_download}|%{time_total}' -m "${OBSERVE_TIMEOUT:-120}" \
           -H "Authorization: Bearer ${TOKEN:-}" "$url" 2>/dev/null || echo "ERR|0|0")"
  local code="${meta%%|*}" rest="${meta#*|}"
  local bytes="${rest%%|*}" secs="${rest#*|}"
  local mb; mb="$(awk -v b="${bytes:-0}" 'BEGIN{printf "%.1f", b/1048576}')"
  if [[ "$code" != "200" ]]; then
    # 连不上也不判失败 —— 但要说出来，否则「零未执行 + 零失败」会像通过
    mark_observe "$id" "$path -> 观察失败：HTTP ${code}（这项不参与判定）"
    return 0
  fi
  mark_observe "$id" "$path -> **只记录不判定**：响应 ${mb} MB / ${secs}s（已知取舍：不传 max_points 就是全采样）"
  return 0
}

# ============================================================
# run_guardrail_check <id> <path>：断言「被内存护栏拒绝」
#
# #4 的正确形态（2026-11-08 改）：
#
#   原来是「发 hours=168，看长窗能不能返回」。
#   但护栏上线后（maxHistoryRowsPerRequest = 300 万行），
#   29 字段设备的 48h 就是 501 万行 => **必然被 413 拒**。
#   「长窗分段解码能不能用」这条路在生产上已经走不通了：
#     3,000,000 / 29 / 3600 = 28h  => 29 字段设备最大只能查 28 小时。
#   所以再断言「长窗能返回数据」是断言一个不存在的功能。
#
#   改成断言**护栏生效** —— 这比记录耗时有用得多：
#     · 护栏是 P0 修复，需要演练验证；否则「护栏上线了」只是口头承诺；
#     · 断言被拒是**安全**的：护栏生效时立即返回（不解码），
#       万一护栏没生效，48h = 894 MB 堆，在 1.5 GiB 容器内可生还。
#
# ⚠ 这个断言依赖护栏已部署。护栏未上线时会**判失败**（返回 200 + 大量数据），
#   那是正确的 —— 「P0 修复没上线」本来就该让演练红。
#   护栏上线前请保持 DRILL_LONG_HISTORY=0（默认），这一项不会发请求。
# ============================================================
run_guardrail_check() {
  local id="$1" path="$2"
  local url="${API_BASE:-http://127.0.0.1:18090}${path}"
  local tmp; tmp="$(mktemp)"
  local meta code ctype
  meta="$(curl -s -o "$tmp" -w '%{http_code}|%{content_type}' -m "${OBSERVE_TIMEOUT:-120}" \
           -H "Authorization: Bearer ${TOKEN:-}" "$url" 2>/dev/null || echo "TIMEOUT|")"
  code="${meta%%|*}"; ctype="${meta#*|}"
  local body; body="$(head -c 400 "$tmp")"
  rm -f "$tmp"

  if [[ "$code" == "TIMEOUT" || "$code" == "000" ]]; then
    mark_fail "$id" "$path -> >${OBSERVE_TIMEOUT}s 未响应。**护栏可能没生效**（若生效应当立即 413 返回）"
    return 1
  fi
  if [[ "$code" == "413" ]]; then
    mark_pass "$id" "$path -> 413 拒绝（护栏生效）：$(echo "$body" | tr '\n' ' ' | cut -c1-90)"
    return 0
  fi
  if [[ "$code" == "200" ]]; then
    mark_fail "$id" "$path -> **200 而不是 413** —— 内存护栏没生效！这个请求本会物化约 900 MB 堆"
    return 1
  fi
  mark_fail "$id" "$path -> HTTP $code（期望 413 被护栏拒绝）：$(echo "$body" | tr '\n' ' ' | cut -c1-80)"
  return 1
}

# ============================================================
# run_manifest_check <id>：从 MANIFEST 读「形状 + 基线比」并执行判定
#
# 为什么要这个包装（2026-11-07 实测教训）：
#   原来 MANIFEST 声明了一份形状（.data.sensors），phase_space 的调用处
#   又硬编码了一份（.data.sensors）。**两份来源必然漂移** ——
#   我改了 MANIFEST 修 #6/#13，调用处没跟着改，结果形状仍然是错的。
#
# 现在形状只有一个来源：MANIFEST 第 5 字段。
# 调用处只说 id，其余全从 MANIFEST 取。
# ============================================================
manifest_field() {
  local want_id="$1" field="$2" line
  while IFS= read -r line; do
    [[ "$(echo "$line" | cut -d"~" -f1)" == "$want_id" ]] || continue
    echo "$line" | cut -d"~" -f"$field"
    return 0
  done <<< "$(printf "%s
" "${MANIFEST[@]}")"
  return 1
}

run_manifest_check() {
  local id="$1"
  local apath ratio
  apath="$(manifest_field "$id" 5)"
  ratio="$(manifest_field "$id" 6)"
  [[ -z "$apath" ]] && ratio="0"
  run_http_check "$id" "$(path_for_id "$id")" "$apath" "$ratio"
}

run_http_check() {
  local id="$1" path="$2" apath="${3:-}" ratio="${4:-0}"
  judge_response "$id" "$path" "$apath" "$ratio"
  local rc=$?
  if [[ "$rc" -eq 0 ]]; then
    local extra=""
    [[ -n "$apath" && "$JUDGE_SIZE" != "0" ]] && extra=" ${apath}=${JUDGE_SIZE}"
    mark_pass "$id" "$path -> 200 JSON${extra}"
    return 0
  fi
  if [[ "$rc" -eq 2 ]]; then
    # 配置未开 => 单列状态。既不算通过，也不算失败（详见 judge_response 里的注释）。
    mark_config "$id" "$path -> $JUDGE_REASON"
    return 2
  fi
  mark_fail "$id" "$path -> $JUDGE_REASON"
  return 1
}

# capture_baseline：在 DETACH **之前**把各项的形状尺寸记下来。
#
# 为什么必须演练前采样、而不是写死一个下界：
#   「点数与演练前一致」只有相对基线才有意义。写死 100 会在数据增长后失败，
#   写死 1 又退化成「非空即可」（1 条也算过）。基线是唯一既不脆又能抓半截数据的判据。
BASELINE_TIMEOUTS=""
capture_baseline() {
  local line id phase apath size
  echo ""
  echo "  采集基线（DETACH 之前，此时 unified_data 还在）："
  while IFS= read -r line; do
    id="$(echo "$line" | cut -d"~" -f1)"
    phase="$(echo "$line" | cut -d"~" -f2)"
    apath="$(echo "$line" | cut -d"~" -f5)"
    [[ "$phase" == "space" ]] || continue
    [[ -z "$apath" ]] && continue
    # 明确「数值不作正确性判据」的项不采基线（见 NO_BASELINE_IDS 注释）
    case "$NO_BASELINE_IDS" in *" $id "*) 
      log "  基线 #$id  跳过（该接口数值不作正确性判据，且可能触发全表 GROUP BY）"
      continue ;;
    esac
    local path; path="$(path_for_id "$id")"
    [[ -z "$path" ]] && continue
    size="$(probe_size "$id" "$path" "$apath")"
    if [[ "${size:--9}" -ge 0 ]]; then
      HTTP_BASE["$id"]="$size"
      log "  基线 #$id  ${apath} = $size"
    elif [[ "${size:--9}" == "-3" ]]; then
      # 超时必须单独说：它和「接口返空」是两件事，混成一个 -1 会让人误判
      log "  基线 #$id  **超时 >${BASELINE_TIMEOUT}s** —— 该项判定退回「非空」"
      BASELINE_TIMEOUTS+=" $id"
    else
      log "  基线 #$id  取不到 ${apath}（size=${size}）—— 该项判定退回「非空」"
    fi
  done <<< "$(printf "%s\n" "${MANIFEST[@]}")"
}

# probe_size：只读探针，拿某个 id 对应接口的形状尺寸。失败打印负数。
probe_size() {
  local id="$1" path="$2" apath="$3"
  local url="${API_BASE:-http://127.0.0.1:18090}${path}"
  local tmp; tmp="$(mktemp)"
  local meta code ctype
  meta="$(curl -s -o "$tmp" -w '%{http_code}|%{content_type}' -m "$BASELINE_TIMEOUT" \
           -H "Authorization: Bearer ${TOKEN:-}" "$url" 2>/dev/null || echo "TIMEOUT|")"
  code="${meta%%|*}"; ctype="${meta#*|}"
  local out=-1
  if [[ "$code" == "TIMEOUT" ]]; then
    out=-3   # 超时：与「响应里没这个路径」(-1) 区分开
  elif [[ "$code" == "200" && "$ctype" == *application/json* ]]; then
    out="$(json_size "$tmp" "$apath")"
  fi
  rm -f "$tmp"
  echo "$out"
}

# ============================================================
# json_tool_selftest：开跑前验证**当前后端**的 json_size 行为正确
#
# 为什么必须有（2026-11-07 踩过两次同类错）：
#   第一次：json_size 的 expr: 分支只写了 jq 那一半，python3 回退没写。
#           我的机器有 jq => 本地全绿；演练机没有 jq => #6 必然失败。
#           **环境差异导致的假通过** —— 这是最难自查的一类。
#   第二次（本轮）：同样的形态，只是换了个地方。
#
# 所以：不靠「我这边能跑」判断，靠**开跑前跑一遍固定用例**。
# 当前后端（jq 或 python3）对每个形状形式都要给出预期答案，
# 任何一个不符 => 判定能力不可信 => 中止演练。
# ============================================================
json_tool_selftest() {
  if [[ -z "$JSON_TOOL" ]]; then
    echo "  [X] 既没有 jq 也没有 python3 —— 无法做任何形状判定"
    return 1
  fi
  local d; d="$(mktemp -d)"
  # 夹具必须先在脚本里构造正确。
  # 第一次写的时候漏了一层转义，夹具本身不是合法 JSON，于是自检假失败 ——
  # 「测试自己的夹具坏了」和「被测对象坏了」长得一模一样，只能靠先单独验证夹具。
  printf %s '{"data":{"values":[1,2,3]}}' > "$d/arr.json"
  printf %s '{"data":[]}' > "$d/empty.json"
  printf %s '{"data":{"data_json":null}}' > "$d/nullinner.json"
  printf %s '{"data":{"data_json":"not json"}}' > "$d/badinner.json"
  printf %s '{"data":{"data_json":"{\"sensors\":[{\"n\":1},{\"n\":2}]}"}}' > "$d/nested.json"

  local bad=0
  expect() {
    local want="$1" file="$2" shape="$3" label="$4" got
    got="$(json_size "$d/$file" "$shape")"
    if [[ "$got" == "$want" ]]; then
      echo "    [OK] $label => $got"
    else
      echo "    [X]  $label => $got（期望 $want）"
      bad=$((bad+1))
    fi
  }
  echo "  json 工具自检（后端=$JSON_TOOL）:"
  expect 3  arr.json       ".data.values"                  "普通路径（数组长度）"
  expect 0  empty.json     ".data"                         "普通路径（空数组）"
  expect 2  nested.json    "json:.data.data_json:.sensors" "嵌套（字符串内 JSON）"
  expect -1 nullinner.json "json:.data.data_json:.sensors" "嵌套（外层为 null）"
  expect -2 badinner.json  "json:.data.data_json:.sensors" "嵌套（内层不是合法 JSON）"
  expect 0  arr.json       ""                              "空形状（不判）"
  rm -rf "$d"

  echo ""
  if [[ "$bad" -gt 0 ]]; then
    echo "  自检结论：$bad 项不符 => **当前后端的形状判定不可信**，演练中止"
    return 1
  fi
  echo "  自检结论：全部符合 ✓"
  return 0
}

# ============================================================
# wait_for_quiet：等「基线采集引发的查询」真正结束
#
# 为什么必须有这一步 —— 2026-11-07 生产实测：
#   采集基线会调 /metrics/summary，它的 points_stored 是全表 GROUP BY。
#   curl 有 -m 超时，**但超时只是客户端不等了，服务端那条 SQL 还在跑**。
#   实测它在 DETACH 期间仍持 AccessShareLock、跑了 5 分 37 秒
#   => 把 DETACH 堵住 => 演练自己制造了自己要防的故障。
#
#   所以「客户端超时」不等于「查询结束」。必须回头查 pg_stat_activity，
#   确认这些查询真的没了再往下走。
#
# 阈值 5s 比预检的 10s 松：这里只关心「还有没有在跑的长查询」，
# 短查询接受 —— 它们很快就散，不值得为它们等。
# ============================================================
SQL_HEAVY_ON_UNIFIED="SELECT a.pid,
       round(extract(epoch FROM now()-a.query_start))::bigint AS secs,
       left(regexp_replace(coalesce(a.query,''), '\\s+', ' ', 'g'), 80)
FROM pg_stat_activity a
WHERE a.datname = current_database()
  AND a.pid <> pg_backend_pid()
  AND a.state = 'active'
  AND coalesce(a.query, '') ILIKE '%unified_data%'
  AND (now() - a.query_start) > interval '5 seconds'
ORDER BY 2 DESC"

wait_for_quiet() {
  local waited=0 heavy
  echo ""
  echo "  结算：等基线采集引发的查询结束（最多 ${SETTLE_MAX_WAIT}s）"
  while true; do
    heavy="$(psql_q "$SQL_HEAVY_ON_UNIFIED")"
    if [[ -z "$heavy" ]]; then
      echo "  [OK] 无 >5s 的 unified_data 查询在跑（等了 ${waited}s）"
      return 0
    fi
    if [[ "$waited" -ge "$SETTLE_MAX_WAIT" ]]; then
      echo "  [X] 等了 ${SETTLE_MAX_WAIT}s 仍有长查询在跑："
      echo "$heavy" | while IFS="|" read -r pid secs q; do
        printf "        pid=%-8s %5ss  %s\n" "$pid" "$secs" "$q"
      done
      echo "        它们持 AccessShareLock，会让 DETACH 拿不到 ACCESS EXCLUSIVE。"
      return 1
    fi
    sleep 2; waited=$((waited+2))
  done
}
# ============================================================
# 判定逻辑自检（负向对照）
#
# 喂 4 个**明确应该被判失败**的输入。如果它们被判「通过」，
# 说明判定逻辑坏了 —— 那整个演练结论不可信，必须中止。
#
# 为什么需要：本轮就出过「只看状态码」把 8 项空数组 + 1 项 SPA HTML
# 全判成通过。**一个永远返回通过的判定函数，和没有判定函数是一样的。**
# ============================================================
judge_selftest() {
  echo ""
  echo "  判定逻辑自检（负向对照）：喂 4 个必须失败的输入"
  local failed=0 total=0
  local name code ctype body apath
  local tmp ok reason bcode size
  while IFS='|' read -r name code ctype body apath; do
    [[ -z "$name" ]] && continue
    total=$((total+1))
    tmp="$(mktemp)"; printf '%s' "$body" > "$tmp"
    ok=1; reason=""
    if [[ "$code" != "200" ]]; then
      ok=0; reason="status $code"
    elif [[ "$ctype" != *application/json* ]]; then
      ok=0; reason="content-type=$ctype"
    else
      bcode="$(json_code "$tmp")"
      if [[ -n "$bcode" && "$bcode" != "200" ]]; then ok=0; reason="body.code=$bcode"; fi
      if [[ "$ok" -eq 1 && -n "$apath" ]]; then
        size="$(json_size "$tmp" "$apath")"
        if [[ "$size" -lt 1 ]]; then ok=0; reason="shape size=$size"; fi
      fi
    fi
    rm -f "$tmp"
    if [[ "$ok" -eq 0 ]]; then
      echo "    [OK] 「$name」被判失败（$reason）"
    else
      echo "    [X]  「$name」**被判通过了**！它是错误输入 => 判定逻辑失效"
      failed=$((failed+1))
    fi
  done <<'CASES'
SPA 回退返回 HTML|200|text/html; charset=utf-8|<!doctype html><html></html>|
空数组（数据被藏）|200|application/json|{"code":200,"data":[]}|.data
HTTP 200 但 body.code=500|200|application/json|{"code":500,"data":[]}|
非 200 状态|503|application/json|{"code":503,"message":"maintenance"}|
CASES
  echo ""
  if [[ "$failed" -gt 0 ]]; then
    echo "    自检结论：$failed/$total 个错误输入被判通过 => 判定逻辑失效，演练结论不可信"
    return 1
  fi
  echo "    自检结论：$total/$total 个错误输入均被判失败 ✓"
  return 0
}

# ============================================================
# check_node_latest：item #1 的专用判定
#
# 为什么不能只用通用四关：
#   /nodes/:id/latest 在**节点下所有设备都被软删**时返回 values=null。
#   这不是 bug —— 新实现（task-19）刻意排除软删设备，这正是 #13b 要验的行为。
#   但对「有活跃设备的节点」，values=null 就是真失败。
#
# 所以判据是**条件式**的：
#   有数据              => 通过
#   null 且全软删        => 通过（并说明这是预期行为）
#   null 且有活跃设备    => 失败（这才是 bug）
#
# 只判「非空」会把第一种情况判成失败（假报警）；
# 只判「允许 null」会把第三种情况放过去（假绿）。必须查库分辨。
# ============================================================
check_node_latest() {
  local id="$1" path="$2"
  # 三关（状态码 / Content-Type / body.code）在本函数内联，不走 judge_response ——
  # 因为第四关（形状）是**条件式**的，judge_response 的通用规则表达不了。
  local url="${API_BASE:-http://127.0.0.1:18090}${path}"
  local tmp; tmp="$(mktemp)"
  local meta code ctype
  meta="$(curl -s -o "$tmp" -w '%{http_code}|%{content_type}' -m "$CHECK_TIMEOUT" \
           -H "Authorization: Bearer ${TOKEN:-}" "$url" 2>/dev/null || echo "TIMEOUT|")"
  code="${meta%%|*}"; ctype="${meta#*|}"
  if [[ "$code" == "TIMEOUT" || "$code" == "000" ]]; then
    mark_fail "$id" "$path -> 接口 >${CHECK_TIMEOUT}s 未响应"; rm -f "$tmp"; return 1
  fi
  if [[ "$code" != "200" ]]; then
    mark_fail "$id" "$path -> HTTP $code（期望 200）"; rm -f "$tmp"; return 1
  fi
  if [[ "$ctype" != *application/json* ]]; then
    mark_fail "$id" "$path -> HTTP 200 但 Content-Type=${ctype:-空}，不是 JSON（SPA 回退？）"
    rm -f "$tmp"; return 1
  fi
  local bcode; bcode="$(json_code "$tmp")"
  if [[ -n "$bcode" && "$bcode" != "200" ]]; then
    mark_fail "$id" "$path -> HTTP 200 但 body.code=$bcode"; rm -f "$tmp"; return 1
  fi

  # values 判定
  local nvals raw
  raw="$(jq -r '(.data.values // null) | if . == null then "NULL" else (length|tostring) end' "$tmp" 2>/dev/null)"
  rm -f "$tmp"
  if [[ "$raw" == "NULL" ]]; then
    # null：查库分辨「全软删」还是「有活跃设备却不返数据」
    # 取 node id 用 bash 参数展开，不用 sed 反向引用 ——
    # sed 的反向引用穿过多层引用（heredoc/python/JS）时会被吃成控制字符，
    # 结果 node_id 为空 => SQL 变成 node_id = 空串 => 恒返 0 => 假绿。
    # （实测踩到：报告打印「节点  下活跃设备数=0」，节点号是空的。）
    local node_id active
    local _n="${path#*/nodes/}"
    node_id="${_n%/latest}"
    active="$(psql_q "SELECT count(*) FROM edge_devices WHERE node_id = '$node_id' AND deleted_at IS NULL")"
    if [[ ! "$active" =~ ^[0-9]+$ ]]; then
      mark_fail "$id" "$path -> values=null，且查不到该节点活跃设备数（无法分辨是预期还是 bug）"
      return 1
    fi
    if [[ "$active" -eq 0 ]]; then
      mark_pass "$id" "$path -> values=null，但节点 $node_id 下活跃设备数=0（全软删）=> 预期行为"
      return 0
    fi
    mark_fail "$id" "$path -> values=null，但节点 $node_id 下还有 $active 台活跃设备 => 真丢数据"
    return 1
  fi
  if [[ "$raw" -ge 1 ]]; then
    mark_pass "$id" "$path -> 200 JSON .data.values=$raw"
    return 0
  fi
  # 空数组：有活跃设备就是失败
  local node_id2 active2
  local node_id2 active2
  local _n2="${path#*/nodes/}"
  node_id2="${_n2%/latest}"
  active2="$(psql_q "SELECT count(*) FROM edge_devices WHERE node_id = '$node_id2' AND deleted_at IS NULL")"
  if [[ "${active2:-x}" == "0" ]]; then
    mark_pass "$id" "$path -> values 为空，节点下活跃设备数=0 => 预期行为"
    return 0
  fi
  mark_fail "$id" "$path -> .data.values 为空，但节点下有 ${active2:-?} 台活跃设备"
  return 1
}

# path_for_id：演练项的接口路径（capture_baseline 与 run_http_check 共用，避免两处各写一份）
path_for_id() {
  case "$1" in
    1)   echo "/api/v1/nodes/1/latest" ;;
    2)   echo "/api/v1/devices/${DEVICE_ID}/history?hours=1" ;;
    # #3/#4 必须带 max_points：不带的话 24h 要下 435 MB、168h 要下 3 GB。
    # 演练要跑两遍（基线 + 回归）=> 合计 870 MB / 6 GB。
    # **演练不该为了验证接口而下 6 GB** —— 那是我这轮第三次发现的「drill 自己制造问题」
    # （前两次：基线采集触发全表 GROUP BY 堵住 DETACH、脚本自身挂进锁队列）。
    #
    # ⚠ 但 cap 会削弱判据：加了 cap 后点数恒为 cap+2，
    #   只能检测「完全没数据」，不能检测「少了一半」。
    #   「少一半」由 #2（hours=1，无 cap）和 #25（无 cap，观察）覆盖。
    #   这是有意的取舍，写在这里免得下次有人以为 #3/#4 能抓半截数据。
    #
    # ⚠⚠ max_points 只减**响应体**，不减**服务端解码堆**：
    #   frameHistoryRowsDownsampled 第一步就是全量 decodeHistoryByName。
    #   所以 #4 hours=168 仍会在服务端解出约 1720 万行 ≈ 3 GB 堆。
    #   这是产品侧问题（分段解码没有分段释放），已在报告里提出。
    3)   echo "/api/v1/devices/${DEVICE_ID}/history?hours=24&max_points=5000" ;;
    4)   echo "/api/v1/devices/${DEVICE_ID}/history?hours=48&max_points=5000" ;;
    5)   echo "/api/v1/unified-data/historical?${_hist_q_change}" ;;
    6)   echo "/api/v1/edge-devices/${DEVICE_ID}/latest-data" ;;
    7)   echo "/api/v1/edge-devices/${DEVICE_ID}/data" ;;
    8)   echo "/api/v1/edge-devices" ;;
    9)   echo "/api/v1/overview" ;;
    10)  echo "/api/v1/metrics/summary" ;;
    11)  echo "/api/v1/sensor-fields?edge_device_id=${DEVICE_ID}" ;;
    13)  echo "/api/v1/edge-devices/${DEVICE_ID}/latest-values" ;;
    15)  echo "/api/v1/unified-data/categories?device_pk=${DEVICE_ID}" ;;
    16)  echo "/api/v1/devices/${DEVICE_ID}/sensor-data?limit=50" ;;
    17)  echo "/api/v1/devices/${DEVICE_ID}/history?hours=24&max_points=100" ;;
    25)  echo "/api/v1/devices/${DEVICE_ID}/history?hours=24" ;;
    18)  echo "/api/v1/unified-data/historical?${_hist_q}" ;;
    19)  echo "/api/v1/unified-data/historical-batch?${_batch_q}" ;;
    20)  echo "/api/v1/data-reports" ;;
    21)  echo "/api/v1/data-sources" ;;
    22)  echo "/api/v1/sensor-fields/resolve?edge_device_id=${DEVICE_ID}&selector=${SENSOR_SELECTOR}" ;;
    *)   echo "" ;;
  esac
}

# ============================================================
# phase=space：DETACH 全分区 + 逐接口打勾
# ============================================================
phase_space() {
  log "=== phase=space: DETACH 全分区 + 逐接口回归 ==="

  # 基线：行数 + 各分区名
  # 先看有没有上一次没恢复干净的残留状态
  if [[ -f "$STATE_FILE" ]]; then
    log "[拒绝] 发现未清除的演练状态文件: $STATE_FILE"
    log "        说明上一次演练没有正常收尾（SIGKILL/断电）。先跑 --restore："
    log "        bash $0 --restore"
    die "存在未完成的演练状态，拒绝叠加新的 DETACH"
  fi

  ROWS_BEFORE="$(psql_q "SELECT count(*) FROM unified_data")"
  [[ "$ROWS_BEFORE" =~ ^[0-9]+$ ]] || die "读不到 unified_data 行数（DB 不可达？）"
  log "演练前行数: $ROWS_BEFORE"

  # 预检（只读）—— 放在状态落盘之前：预检没过 = 环境一个字节都没动
  echo ""
  echo "  预检：查有没有会话会挡住 DETACH（DETACH 要 ACCESS EXCLUSIVE）"
  local blocker_snapshot=""
  local audit=""
  if ! precheck_blockers; then
    if [[ "$FORCE" -ne 1 ]]; then
      log ""
      log "########## 拒绝演练 ##########"
      log "有 >${PRECHECK_QUERY_SECONDS}s 的活动会话。此时抢锁会排在队列里，"
      log "把后面的写者也一起挡住（这是 2026-11-07 阻塞生产 15 分钟的机制）。"
      log "等它跑完再演练。**不要**为了跑演练去杀业务查询。"
      log "环境未改动：没有 DETACH、没有落盘状态。"
      log ""
      log "确实要带阻塞者硬上？用 --force。它会把阻塞者快照写进独立审计文件留痕，"
      log "且 DETACH 仍然受 ${LOCK_TIMEOUT} 超时保护（撞锁就退出，不会挂住）。"
      die "预检未通过：有长查询/长事务在跑（退出码 1，环境未改动）"
    fi
    blocker_snapshot="$(psql_q "$SQL_BLOCKERS")"
    mkdir -p "$STATE_DIR"
    audit="$STATE_DIR/forced-${POSTGRES_DB}-$(date -u +%Y%m%dT%H%M%SZ).log"
    {
      echo "# --force 演练（绕过了预检）"
      echo "# DB=$POSTGRES_DB  at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
      echo "# 阈值=${PRECHECK_QUERY_SECONDS}s  LOCK_TIMEOUT=$LOCK_TIMEOUT"
      echo "# 以下会话在 DETACH 发起时正在运行："
      echo "$blocker_snapshot"
    } > "$audit"
    log ""
    log "########## --force：带着阻塞者继续 ##########"
    log "阻塞者快照已写入审计文件（不会被删除）: $audit"
    echo "$blocker_snapshot" | sed "s/^/        /"
    log "DETACH 仍受 ${LOCK_TIMEOUT} 超时保护 —— 大概率会撞锁退出，这是预期。"
  fi

  # ---- 演练前基线采样（必须在 DETACH 之前，此时 unified_data 还在）----
  # 没有 creds 时跳过，后面所有接口项都会登记「未执行」。
  if need_creds 2>/dev/null; then
    # 判定工具自检：当前后端连形状都算不对的话，后面所有「通过」都是假的。
    if ! json_tool_selftest; then
      log ""
      log "########## 拒绝演练 ##########"
      log "形状判定工具自检未通过（jq/python3 行为不符）。"
      log "环境未改动：没有 DETACH、没有落盘状态。"
      die "json 工具自检失败（退出码 1，环境未改动）"
    fi
    echo ""
    if ! check_token_alive; then
      log ""
      log "########## 拒绝演练 ##########"
      log "凭据/连通性检查未通过。此时跑 22 个接口只会得到 22 条 401/超时，"
      log "它们会掩盖真正的接口问题。"
      log "环境未改动：没有 DETACH、没有落盘状态。"
      die "token 无效或 API 不可达（退出码 1，环境未改动）"
    fi
    capture_baseline
  else
    echo ""
    echo "  跳过基线采样：缺少 TOKEN / DEVICE_ID（接口项稍后会登记为未执行）"
  fi

  # ---- 关键：基线采集之后**必须**再结算 + 重跑预检 ----
  #
  # 2026-11-07 生产实测：基线采集调了 /metrics/summary，它触发全表 GROUP BY，
  # curl -m 超时后**服务端那条 SQL 还在跑**（跑了 5 分 37 秒），持 AccessShareLock，
  # 把紧随其后的 DETACH 堵死 —— 演练自己制造了自己要防的故障。
  #
  # 所以顺序必须是：预检 → 采基线 → **再结算** → **再预检** → DETACH。
  # 只在最前面预检一次是错的：它检查的是「采基线之前」的状态。
  if need_creds 2>/dev/null; then
    if ! wait_for_quiet; then
      log ""
      log "########## 拒绝演练 ##########"
      log "基线采集引发的长查询仍未结束。它们持 AccessShareLock，"
      log "此时 DETACH 必然撞锁（或被它们堵死）。"
      log "环境未改动：没有 DETACH、没有落盘状态。"
      log ""
      log "这些查询是**演练自己**触发的（基线采集），不是业务查询。"
      log "它们跑完就走了；如果反复出现，说明对应接口的重算成本需要治理"
      log "（/metrics/summary 的 points_stored 是全表 GROUP BY，见清单 §3 第 4 类）。"
      die "基线采集后的查询未结束（退出码 1，环境未改动）"
    fi
    echo ""
    echo "  重跑预检（采基线可能制造了新的长查询）"
    if ! precheck_blockers; then
      log ""
      log "########## 拒绝演练 ##########"
      log "基线采集之后出现 >${PRECHECK_QUERY_SECONDS}s 的活动会话。"
      log "可能是别的业务查询，也可能是基线采集留下的。等它们散掉再重跑。"
      log "环境未改动：没有 DETACH、没有落盘状态。"
      die "重跑预检未通过（退出码 1，环境未改动）"
    fi
  fi

  # 基线超时项要显式打出来：它们是「本次演练该项只能判非空」的依据
  if [[ -n "$BASELINE_TIMEOUTS" ]]; then
    log "⚠ 这些项基线采集超时：$BASELINE_TIMEOUTS"
    log "  它们的形状断言会退回「非空」（不判与演练前一致）。"
    log "  这些接口就是本次演练的**盲区** —— 时长短了（<${BASELINE_TIMEOUT}s）才采得到基线。"
  fi

  DETACHED_LIST="$(psql_q "SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid=i.inhrelid JOIN pg_class p ON p.oid=i.inhparent WHERE p.relname='unified_data' ORDER BY c.relname")"
  if [[ -z "$DETACHED_LIST" ]]; then
    log "没有找到 unified_data 的分区 —— 可能是非分区表或库不对"
    die "无法演练（需要分区表）"
  fi
  log "待 DETACH 分区: $(echo $DETACHED_LIST | tr "\n" " ")"

  # 基线落盘（DETACH **之前**，此时行数还是完整的）
  mkdir -p "$STATE_DIR"
  {
    echo "ROWS_BEFORE=$ROWS_BEFORE"
    echo "DETACHED_LIST=$DETACHED_LIST"
    echo "DB=$POSTGRES_DB"
    echo "STARTED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "LOCK_TIMEOUT=$LOCK_TIMEOUT"
    echo "FORCE=$FORCE"
    echo "AUDIT=$audit"
    local bid
    for bid in "${!HTTP_BASE[@]}"; do
      echo "HTTPBASE_${bid}=${HTTP_BASE[$bid]}"
    done
  } > "$STATE_FILE"
  log "基线已落盘: $STATE_FILE"

  DETACHED=1
  # 单事务执行全部 DETACH：一次拿锁，全成或全不成。
  local ddl_sql=""
  local p
  for p in $DETACHED_LIST; do
    ddl_sql="$ddl_sql ALTER TABLE unified_data DETACH PARTITION $p;"
  done

  # ---- DETACH 带重试 ----
  #
  # 为什么重试而不是一次失败就放弃（Lead 2026-11-07 建议）：
  #   锁是**瞬时资源**。撞上一次长查询不代表环境有问题，可能只是恰好。
  #   重试的成本 = 一次 LOCK_TIMEOUT（默认 5s）
  #   放弃的成本 = **整轮演练白跑**，包括已经采完的基线（那是几十秒的接口调用）
  #   => 重试的期望收益远大于成本。
  #
  # 但重试不能盲目重试同一个 DETACH：必须每次都
  #   ① 结算（等可能残留的查询结束）
  #   ② 重跑预检（确认此刻真的能拿锁）
  #   => 否则只是把「快速失败」变成「快速失败三次」。
  #
  # 次数默认 3：配合 5s lock_timeout，最坏代价约 15s + 结算时间。
  local attempt=0 detach_ok=0 detach_err=""
  local max_attempts="${DETACH_MAX_ATTEMPTS:-3}"
  while [[ "$attempt" -lt "$max_attempts" ]]; do
    attempt=$((attempt+1))
    if [[ "$attempt" -gt 1 ]]; then
      log ""
      log "---------- DETACH 重试 $attempt/$max_attempts ----------"
      # 每次重试前都重新结算 + 预检：锁是瞬时的，但「此刻能不能拿锁」要重新判
      wait_for_quiet >/dev/null 2>&1 || true
      if ! precheck_blockers >/dev/null 2>&1; then
        log "  预检仍不通过（有长查询在跑），再等一轮"
        sleep 2
        continue
      fi
      log "  预检通过，重新尝试 DETACH"
    fi

    if detach_err="$(psql_ddl "$LOCK_TIMEOUT" "$STMT_TIMEOUT" "$ddl_sql" 2>&1)"; then
      detach_ok=1
      break
    fi

    # 取 ERROR 行，不取 tail -1 —— psql 会先把 "SET" 打到 stdout，
    # 直接取最后一行有时只拿到 "SET"（实测踩过，看起来像成功）。
    log "  尝试 $attempt 失败：$(echo "$detach_err" | grep -m1 -E 'ERROR|错误' || echo "$detach_err" | tail -1)"
    if ! grep -q "lock timeout" <<< "$detach_err"; then
      # 不是锁超时就不重试 —— 那可能是语法/权限/分区不存在，重试没意义
      log "  失败原因不是 lock timeout，重试无意义，直接放弃"
      break
    fi
    [[ "$attempt" -lt "$max_attempts" ]] && sleep 1
  done

  if [[ "$detach_ok" -ne 1 ]]; then
    log ""
    log "########## DETACH 失败：分区**没有**被藏起来 ##########"
    echo "$detach_err" | tail -5 | sed "s/^/        /"
    if grep -q "lock timeout" <<< "$detach_err"; then
      log "原因：${max_attempts} 次尝试都在 ${LOCK_TIMEOUT} 内拿不到 ACCESS EXCLUSIVE 锁。"
      log "      每次都重新结算 + 重跑预检了，仍然撞锁 =>"
      log "      说明库上有**持续的**锁竞争，不是偶发。"
      log "      每次只等 ${LOCK_TIMEOUT} 是**故意的**：无限等锁会把生产写一起挡住。"
      log "      现在锁已释放，生产写入不受影响。"
      log ""
      log "      下一步：查清是谁在持续持锁（脚本已把每次的阻塞者打在预检里），"
      log "      处理掉再重跑。**不要**为了跑演练去杀业务查询。"
    fi
    local still_attached
    still_attached="$(psql_q "$SQL_ATTACHED_COUNT")"
    log "  仍挂在父表下的分区数: ${still_attached:-查询失败}（应为 $(echo $DETACHED_LIST | wc -w) = 全部）"
    log ""
    log "**回归项不执行** —— 分区没藏起来时它们测的是正常状态，会全绿，"
    log "然后被误当成「演练通过」。这比不跑危险得多。"
    DETACHED=0; rm -f "$STATE_FILE"
    die "DETACH 未成功（${attempt} 次尝试），演练中止（退出码 1，环境已回滚）"
  fi
  log "DETACH 完成（第 ${attempt} 次尝试，单事务，全部 $(echo $DETACHED_LIST | wc -w) 个分区）"

  # 双重核验：不信 SQL 的返回，直接查目录。
  local rows_detached attached_after
  rows_detached="$(psql_q "SELECT count(*) FROM unified_data")"
  attached_after="$(psql_q "$SQL_ATTACHED_COUNT")"
  log "DETACH 后：父表行数=${rows_detached:-查询失败}  仍挂分区=${attached_after:-查询失败}"
  if [[ "${rows_detached:-x}" != "0" || "${attached_after:-x}" != "0" ]]; then
    log "########## DETACH 声称成功但核验不通过 ##########"
    DETACHED=0; rm -f "$STATE_FILE"
    die "DETACH 后核验失败（行数=${rows_detached} 分区=${attached_after}），演练中止"
  fi

  # ---- 判定逻辑自检（负向对照）----
  # 必须在跑接口回归之前：如果判定函数永远返回通过，后面的「通过」全是假的。
  if need_creds 2>/dev/null; then
    if ! judge_selftest; then
      log "########## 判定逻辑失效，演练中止 ##########"
      restore_all
      die "判定自检失败：错误输入被判通过 => 后续「通过」不可信（退出码 1）"
    fi
  fi

  if need_creds; then
    # --- 逐接口（四项全过才算通过：状态码 / Content-Type / body.code / 形状下界）---
    check_node_latest "1" "$(path_for_id 1)"
    run_manifest_check "2"
    run_manifest_check "3"
    # #4 长窗：护栏上线后「长窗能返回数据」这条路走不通了
    #   （3M 行 / 29 字段 => 最大 28h），所以改断言**护栏生效**（413）。
    # 详见 run_guardrail_check 的注释。
    if long_history_enabled; then
      run_guardrail_check "4" "$(path_for_id 4)"
    else
      mark_observe "4" "**未发请求**：护栏未上线前不发 48h 请求（默认关闭，设 DRILL_LONG_HISTORY=1 才验护栏）"
    fi
    run_manifest_check "5"
    run_manifest_check "6"
    run_manifest_check "7"
    run_manifest_check "8"
    run_manifest_check "9"
    run_manifest_check "10"
    run_manifest_check "11"
    run_manifest_check "13"
    run_manifest_check "15"
    run_manifest_check "16"
    run_manifest_check "17"
    run_manifest_check "18"
    run_manifest_check "19"
    run_manifest_check "20"
    run_manifest_check "21"
    run_manifest_check "22"
    # #25 观察项：只记录，不判定（详见 run_observe_check 的文件头注释）
    # #25 是「不传 max_points 的 24h」——它本身**就是那个 P0 的形态**：
    # 下载 435 MB + 服务端物化约 440 MB 堆。和 #3 同量级（#3 只省了下载，堆一样）。
    #
    # 所以它也必须闸在后面：护栏未上线时，演练不该为了「记录一个已知数字」
    # 而在生产上制造一次 440 MB 堆分配。
    # 量级已经知道了（Lead 实测 435 MB / 439.7 MB 堆），写入清单即可；
    # 需要重新测量时显式开 DRILL_LONG_HISTORY=1。
    if observe_heavy_enabled; then
      run_observe_check "25" "$(path_for_id 25)"
    else
      mark_observe "25" "**未发请求**：24h 无 max_points 要下 435 MB（服务端 440 MB 堆），默认关；设 DRILL_OBSERVE_HEAVY=1 才测"
    fi
    mark_skip "13b" "需要 SOFTDEL_DEVICE_ID（已知软删设备）才能验，人工核对"
  else
    local id
    for id in 1 2 3 4 5 6 7 8 9 10 11 13 15 16 17 18 19 20 21 22 25 13b; do
      mark_skip "$id" "缺少 TOKEN/DEVICE_ID，未执行"
    done
  fi

  mark_skip "12" "图表 30 组需要固定字段清单，待接口清单稳定后补"
  mark_skip "14" "人工项：重启服务后需回填 STARTED_AT 证明重启发生过"

  restore_all
  return 0
}

# ============================================================
# phase=time：coverage 推进判据
# ============================================================
phase_time() {
  log "=== phase=time: coverage 推进判据 ==="
  local cov_day rollup_bucket unified_rows
  cov_day="$(psql_q "SELECT max(day) FROM telemetry_rollup_coverage")"
  rollup_bucket="$(psql_q "SELECT max(bucket) FROM telemetry_rollup_1m")"
  unified_rows="$(psql_q "SELECT count(*) FROM unified_data")"
  local yesterday; yesterday="$(date -u -d "yesterday" +%Y-%m-%d)"
  echo "  coverage max(day) = ${cov_day:-查询失败}"
  echo "  rollup  max(bucket) = ${rollup_bucket:-查询失败}"
  echo "  unified_data 行数   = ${unified_rows:-查询失败}"
  echo "  期望 coverage >=    = $yesterday（UTC 昨天）"
  if [[ -z "$cov_day" ]]; then
    mark_fail "24" "读不到 coverage 表"
  elif [[ "$cov_day" < "$yesterday" ]]; then
    mark_fail "24" "coverage 停在 $cov_day，落后于 $yesterday —— 回填未推进"
  else
    mark_pass "24" "coverage=$cov_day >= $yesterday"
  fi

  # ── 帧数护栏（清单 §5.1，2026-11-07 实测更正后新增）──
  #
  # 为什么必须有这条：decodeRangeBounded 超限时**不返回 error**，且保留的是
  # **窗口最早**那一段（丢尾巴）。计数器也不管用 —— LIMIT maxFrames+1 让它
  # 恒报 1，真实丢弃 40 万帧也只报 1。所以只能**事前量**，不能事后看指标。
  #
  # 6h 段在 1 Hz 下约 21.6k 帧，离 32768 余量 1.52x。采样间隔到 0.65s 就超限。
  # 这里按 25000 报警（留 1.3x 余量），超过就说明「帧表重建 rollup」在这台设备上
  # 会静默丢后半段，必须先解决再停写。
  echo ""
  echo "  帧数护栏：任意设备任意 6h 段内帧数必须 < 25000（1.3x 余量）；"
  echo "            硬上限 MaxDecodeFrames=32768 超了会静默丢后半段且不报错"
  # 分桶用 epoch/21600（= 6h），不要用 date_trunc+extract(hour) 组合 ——
  # 那个写法在跨日时会把同一段拆到两个桶，量出来的数偏小（我第一次就写错了，
  # 21000 帧的 6h 段被量成 3600，护栏形同虚设）。
  local worst
  worst="$(psql_q "SELECT max(c) FROM (SELECT count(*) AS c FROM telemetry_frame GROUP BY device_id, floor(extract(epoch FROM ts) / 21600) ) t")"
  if [[ -z "$worst" ]]; then
    echo "  [X] 量不到帧数 —— 不能宣称「不存在静默截断」（未执行不是通过）"
    mark_fail "24" "帧数护栏量不到（查询失败/帧表为空）"
  elif [[ "$worst" -ge 32768 ]]; then
    echo "  [X] 最坏 6h 段 = $worst 帧，**已超过 MaxDecodeFrames=32768**"
    echo "      => 帧表重建 rollup 会静默丢后半段，停写前必须先解决"
    mark_fail "24" "最坏 6h 段 $worst 帧 >= 32768，会静默截断"
  elif [[ "$worst" -ge 25000 ]]; then
    echo "  [!] 最坏 6h 段 = $worst 帧，余量 < 1.3x（硬上限 32768）"
    echo "      暂时不失败，但采样频率一旦提高就会截断"
    mark_pass "24" "coverage 推进 OK；帧数 $worst 余量偏紧（<1.3x）"
  else
    echo "  [OK] 最坏 6h 段 = $worst 帧（余量 $(awk -v w="$worst" 'BEGIN{printf "%.2fx", 32768/w}')）"
  fi
  return 0
}


# ============================================================
# phase=cut：停写切点判据 —— 三个 sink 必须都活着，只有 unified_data 该停
#
# 三条判据来自清单 §0.3（三种错法）：
#   A. 只跳 INSERT       => commit 永不调 => g.last 不推进 => 每帧判 First => 指标失真
#   B. 跳 gate + INSERT  => **推荐**（保留 telemetrySink / rollupSink / latestSink）
#   C. 跳过整段          => L2/L3 一起死 => 自毁，且症状像「停写生效了」
#
# 切法 C 的伪装度最高，所以判据**不能只看 unified 停没停** ——
# 必须同时证明三个 sink 还活着。四条全中才算通过。
#
# ============================================================
# 前置 A：必须人工确认开关已开
#
# 停写开关是 env（EHOME_TELEMETRY_UNIFIED_WRITE），改了要**重启服务**，
# 脚本无法自己打开、也无法从外部读到它的值（没有暴露端点）。
# 所以要求显式传 DRILL_CUT_CONFIRMED=1 声明「我已经设好 env 并重启了」。
# 不传就登记「未执行」——
#   **脚本无法验证的前提，不能靠猜；猜错了会产出「通过」，那是假绿。**
#
# ============================================================
# #23 的直接证据读取（2026-11-08，取代「人工自述」）
#
# 为什么要换掉 DRILL_CUT_CONFIRMED=1：
#   那是一句**自述** —— 它证明「操作者说他做了」，不证明「系统真的在停写」。
#   我们这一整天在查的正是「把自述当成观测」（反模式 8 同族）。
#
# 换成两个**直接可观测**的事实：
#
#   ① 运行中容器的 EHOME_TELEMETRY_UNIFIED_WRITE 值（.Config.Env）
#      开关在**构造时**读取（telemetry_write_gate.go resolveUnifiedWriteEnabled），
#      所以「容器当前的 env」== 「正在运行的进程实际生效的值」。
#
#   ② 容器的 .State.StartedAt
#      证明「确实重启过」，挡住「改了 compose/env 但忘了重建容器」这个最常见的手滑。
#
# 两者合起来是**充分的**：容器 N 分钟前启动 + 它的 env 是 false
# => 当前进程必然处于停写态。不需要任何人自述。
#
# 拿不到容器（无 docker 权限 / 演练机不在宿主机上）时回落到自述，
# 但**显式标注「未验证」** —— 让报告自己说出证据强度，而不是让人去猜。
# ============================================================

# app_container_env <VAR> => 该环境变量的值（取不到返回空）
app_container_env() {
	local name="${APP_CONTAINER:-}"
	[[ -z "$name" ]] && { echo ""; return 0; }
	docker inspect "$name" --format "{{range .Config.Env}}{{println .}}{{end}}" 2>/dev/null \
		| awk -F= -v k="$1" '$1==k {print substr($0, length(k)+2); exit}'
}

# app_container_started_epoch => 容器启动时刻的 epoch 秒（取不到返回空）
app_container_started_epoch() {
	local name="${APP_CONTAINER:-}"
	[[ -z "$name" ]] && { echo ""; return 0; }
	local s
	s="$(docker inspect "$name" --format "{{.State.StartedAt}}" 2>/dev/null)"
	[[ -z "$s" || "$s" == "0001-01-01T00:00:00Z" ]] && { echo ""; return 0; }
	# RFC3339 带纳秒（2026-11-08T01:26:24.851622596Z）—— GNU date 能直接吃，不必裁剪。
	date -u -d "$s" +%s 2>/dev/null || echo ""
}

# unified_skip_counter => ehome_unified_writes_skipped_total 的当前值（取不到返回空）
#
# 这是本项**最强的直接证据**：该计数器只在
#   「unifiedWriteEnabled == false」**且**「确实有记录到达」时才自增
# （consumers_heavy.go 的 `if !c.unifiedWriteEnabled { metrics...Add(len(records)) }`）。
# 所以它在 T0..T1 之间增长了，等于同时证明了两件事：开关生效 + 上游没断。
#
# 它从 /metrics 读，**不需要鉴权**（maintenance 路由的白名单，契约 §3.3），
# 而 /api/v1/metrics 需要 —— 演练里优先用前者，少一个凭据依赖。
unified_skip_counter() {
	local base="${API_BASE:-http://127.0.0.1:18090}"
	local v
	v="$(curl -s -m "$CHECK_TIMEOUT" "$base/metrics" 2>/dev/null \
		| awk '$1=="ehome_unified_writes_skipped_total" {print $2; exit}')"
	[[ "$v" =~ ^[0-9]+$ ]] && echo "$v" || echo ""
}

# ============================================================
# 前置 B：采样窗口必须 > 2x 心跳（60s）且 > 2x rollup flush（60s）
#
# 这是本轮最容易搞错的地方，写清楚：
#
#   · unified_data 不是每帧都写 —— L1 收窄后「值没变且未到心跳」就跳过，
#     心跳默认 60s（config.DefaultTelemetryHeartbeatSeconds = 60）。
#     => 采样 60s 时，「unified 不增长」**无法区分**「开关生效」和「正好没到心跳」。
#
#   · telemetry_rollup_1m 是内存累加 + 60s ticker flush，且**只落已结束的桶**
#     （FlushCompleted 里 k.bucket.Before(currentBucket) 才落）。
#     => 最坏滞后 120s，采样 60s 时「rollup 不增长」大概率是采样太短，不是 L3 死了。
#
# 所以默认 150s（2.5x）。想缩短就自己承担误判风险。
# ============================================================
# APP_CONTAINER: 应用容器名，用于**直接读取**停写开关的生效值（docker inspect）。
#
# 为什么要能读它：#23 的旧做法是要求操作者传 DRILL_CUT_CONFIRMED=1 —— 那是一句**自述**，
# 证明「他说他做了」，不证明「系统真的在停写」。而我们这一整天查的正是
# 「把自述当成观测」。有容器名时改读 .Config.Env（开关在构造时读取，
# 所以「容器当前 env」==「进程实际生效的值」）+ .State.StartedAt（证明真的重启过）。
#
# 置空表示「读不到」（演练机不在宿主机上 / 无 docker 权限）=> 回落到自述，
# 但报告会标明证据等级是「自述」，不会伪装成直接证据。
APP_CONTAINER="${APP_CONTAINER:-}"

# EnvUnifiedWrite: 与 backend/internal/databus/telemetry_write_gate.go 的常量保持一致。
# 写死在脚本里而不是从 Go 源码解析 —— 一致性由 #23 文档条目 + 代码注释双向登记保证。
EnvUnifiedWrite="EHOME_TELEMETRY_UNIFIED_WRITE"

CUT_SAMPLE_SECONDS="${DRILL_CUT_SAMPLE_SECONDS:-150}"

# ============================================================
# phase=cut：停写切点判据 —— 三个 sink 必须都活着，只有 unified_data 该停
#
# 依据清单 §0.3 的三种错法：
#   A. 只跳 INSERT        => commit 永不调 => g.last 不推进 => 每帧判 First => 指标失真
#   B. 跳 gate + INSERT   => **推荐**（保留 telemetrySink / rollupSink / latestSink）
#   C. 跳过整段           => L2/L3 一起死 => 自毁，且症状像「停写生效了」
#
# 切法 C 伪装度最高，所以判据**不能只看 unified 停没停** ——
# 必须同时证明三个 sink 还活着。四条全中才算通过。
#
# ---- 前置 A：必须人工确认开关已开 ----
#
# 开关是 env（EHOME_TELEMETRY_UNIFIED_WRITE），改了要**重启服务**；
# 脚本既打不开它、也读不到它的值（没有暴露端点）。
# 所以要求显式传 DRILL_CUT_CONFIRMED=1 声明「已设好 env 并重启」。
# 不传就登记「未执行」——
#   **无法验证的前提不能靠猜；猜错了会产出「通过」，那是假绿。**
#
# ---- 前置 B：采样窗口必须 > 2x 心跳 且 > 2x rollup flush ----
#
# 这是最容易搞错的地方：
#
#   · unified_data 不是每帧都写 —— L1 收窄后「值没变且未到心跳」就跳过，
#     心跳默认 60s（config.DefaultTelemetryHeartbeatSeconds = 60）。
#     => 采样 60s 时，「unified 不增长」**无法区分**「开关生效」和「正好没到心跳」。
#
#   · telemetry_rollup_1m 是内存累加 + 60s ticker flush，且**只落已结束的桶**
#     （FlushCompleted: k.bucket.Before(currentBucket)）。
#     => 最坏滞后 120s，采样 60s 时「rollup 不增长」大概率是采样太短，不是 L3 死了。
#
# 所以默认 150s（2.5x）。要缩短就自己承担误判风险。
# ============================================================
CUT_SAMPLE_SECONDS="${DRILL_CUT_SAMPLE_SECONDS:-150}"

# cut_max_epoch <表> <时间列> => 该列最大值转 epoch 秒（取不到返回空）
#
# 用 max() 不用 count(*)（Lead 2026-11-08 定的）：
#   · max 走索引/分区裁剪 => 毫秒级；count(*) 对 1700 万行要 3 秒+
#   · 「时间戳冻结」比「行数不变」更精确：并发写入时 count 有跨语句抖动
#
# 返回 **epoch 秒（数值）** 而不是文本：文本序在精度不同时
# （"09:11:00+00" vs "09:11:00.5+00"）容易踩坑，数值无歧义。
cut_max_epoch() {
  psql_q "SELECT coalesce(extract(epoch FROM max($2))::bigint::text, '') FROM $1"
}

# latest_values_max_epoch：探 /edge-devices/:id/latest-values，取其中最大的 timestamp（epoch 秒）
#
# 为什么必须查它（不能只看库）：
#   latestSink 挂在 writeGate.filter **之前**，且不受写库结果门控
#   （consumers_heavy.go:484 —— 被跳过的帧同样更新缓存）。
#   所以停写后 latest 缓存**仍应前进**。
#
# 它是**唯一能区分「sink 死了」和「设备安静」的独立证据**：
#   · latest 前进 + 帧表不动 => 数据在到达，L2 死了 => 切法 C
#   · latest 不动 + 帧表不动 => 设备安静（不是错，但这一轮验不出来）
#
# 返回 epoch 秒（数值），取不到返回空。
latest_values_max_epoch() {
  if ! need_creds 2>/dev/null; then echo ""; return 0; fi
  local url="${API_BASE:-http://127.0.0.1:18090}/api/v1/edge-devices/${DEVICE_ID}/latest-values"
  local tmp; tmp="$(mktemp)"
  local meta code ctype
  meta="$(curl -s -o "$tmp" -w '%{http_code}|%{content_type}' -m "$CHECK_TIMEOUT" \
           -H "Authorization: Bearer ${TOKEN:-}" "$url" 2>/dev/null || echo "TIMEOUT|")"
  code="${meta%%|*}"; ctype="${meta#*|}"
  local out=""
  if [[ "$code" == "200" && "$ctype" == *application/json* ]]; then
    if [[ "$JSON_TOOL" == "jq" ]]; then
      out="$(jq -r '[(.data // [])[] | .timestamp] | if length==0 then "" else (max | fromdateiso8601 | tostring) end' "$tmp" 2>/dev/null)"
      # fromdateiso8601 只吃 "YYYY-MM-DDTHH:MM:SSZ"，带纳秒/偏移会失败 => 回退到 python
      if [[ -z "$out" ]]; then
        out="$(jq -r '[(.data // [])[] | .timestamp] | max // ""' "$tmp" 2>/dev/null)"
      fi
    fi
    if [[ -z "$out" || ! "$out" =~ ^[0-9]+$ ]]; then
      out="$(python3 - "$tmp" <<'PYEOF' 2>/dev/null
import json, sys
from datetime import datetime
try:
    d = json.load(open(sys.argv[1], encoding="utf-8"))
    ts = [x.get("timestamp") for x in (d.get("data") or []) if x.get("timestamp")]
    if not ts:
        print(""); raise SystemExit
    best = max(datetime.fromisoformat(t.replace("Z", "+00:00")) for t in ts)
    print(int(best.timestamp()))
except Exception:
    print("")
PYEOF
)"
    fi
    [[ "$out" =~ ^[0-9]+$ ]] || out=""
  fi
  rm -f "$tmp"
  echo "$out"
}

# ============================================================
# cut_verdict：**纯函数**版的组合判定（不碰 DB、不碰网络）
#
# 为什么必须抽成纯函数：组合判定有 4 条判据 + 3 种错法的分支，
# 而真实演练每次要 150s 采样、还得手工开关停写 —— 用真实环境测这些边界
# 一轮就是几分钟，人很快就会「挑好走的路径跑」，边界永远没人验。
# 抽出来之后可以用假输入把每个分支都跑到，成本是毫秒级。
#
# 入参全部是「是/否 + 能否取值」，出参是 (verdict, message)：
#   verdict ∈ PASS | FAIL | UNKNOWN
#   UNKNOWN = 证据不足，**既不是通过也不是失败** —— 单独一个态，
#             否则「读不到」会被算成「验过了」。
# ============================================================
# cut_verdict l2 l3 latest frozen skip have_skip
#   l2/l3/latest/frozen/skip : 1=成立 0=不成立
#   have_skip                : 1=取到了 skip 计数器（0=取不到，不能判）
cut_verdict() {
	local l2="$1" l3="$2" latest="$3" frozen="$4" skip="$5" have_skip="$6"
	local detail="${7:-}"

	# ① 读不到 unified 的 max => 无法判定（不是「开关没生效」）
	if [[ "$frozen" == "2" ]]; then
		echo "UNKNOWN|读不到 unified_data 的 max(timestamp)（表空？分区已游离？）=> **无法判定**停写是否生效。这条不是「开关没生效」，是「这条判据本轮没证据」"
		return 0
	fi

	# ② unified 仍在增长 => 开关没生效
	if [[ "$frozen" == "0" ]]; then
		echo "FAIL|unified_data **仍在增长** => 停写开关没生效，切点未验证。${detail}"
		return 0
	fi

	# ③ 开关的否证：计数器 delta=0，而 L2 或 latest 在动
	#    => 数据确实在到达、sink 活着，但没一行被拦 => 开关大概率没生效。
	#    必须排在 ④ 之前：否则「unified 冻结（其实是心跳还没到）」+「L2 活着」会被判通过。
	if [[ "$have_skip" == "1" && "$skip" == "0" ]]; then
		if [[ "$l2" == "1" || "$latest" == "1" ]]; then
			echo "FAIL|**停写计数器 delta=0**，但 L2/latest 在前进（数据确实在到达）=> 停写开关没生效。unified 冻结很可能只是「心跳还没到」的假象。检查开关是否真的为 false 且容器**已重建**。${detail}"
			return 0
		fi
	fi

	# ④ 四个存活信号 + 开关证据全中 => 通过
	if [[ "$l2" == "1" && "$l3" == "1" && "$latest" == "1" && "$skip" == "1" ]]; then
		echo "PASS|五条全中：frame / rollup / latest / unified 冻结 / skip 计数器增长。${detail}"
		return 0
	fi

	# ⑤ 全都没动 => 分不清「设备安静」和「切法 C」，不能判通过也不能判失败
	if [[ "$l2" == "0" && "$l3" == "0" && "$latest" == "0" ]]; then
		echo "UNKNOWN|四个信号**全部无进展** => 无法区分「设备安静」和「切法 C（L2/L3 一起死）」。换一台**正在上报**的设备重跑，或加大采样窗口。**不能判通过**"
		return 0
	fi

	# ⑥ latest 前进但 L2/L3 有死的 => 切法 C（最危险：把 sink 一起切了）
	if [[ "$latest" == "1" && ( "$l2" == "0" || "$l3" == "0" ) ]]; then
		local dead=""
		[[ "$l2" == "0" ]] && dead="$dead L2帧表"
		[[ "$l3" == "0" ]] && dead="$dead L3rollup"
		echo "FAIL|**切法 C**：latest 缓存前进（数据确实在到达）但${dead} 无新增 => 切点切错了位置，把 sink 一起切了。${detail}"
		return 0
	fi

	# ⑦ 其余组合 => 判据不完整，既不能判通过也不能判失败
	echo "UNKNOWN|判据不完整：L2=$l2 L3=$l3 latest=$latest frozen=$frozen skip=$skip(取到=$have_skip)。**不判通过**"
}

# ============================================================
# cut_verdict_selftest：组合判定的**穷举自检**
#
# 为什么必须有它（不是可选）：
#   cut_verdict 有 4 条判据 + 6 个分支。真实演练里每验一次要采样 150s、
#   还得先手工改开关重建容器 —— 一轮几分钟。于是「挑好走的路径跑」必然发生，
#   而**边界分支永远没人验**。
#
#   这和 judge_selftest 是同一族：一个永远返回 PASS 的判定函数，
#   和没有判定函数是一样的。区别只是这里喂的是**纯函数入参**，毫秒级。
#
# 判据：10 个边界组合必须全部命中预期结论。任一不符 => 判定逻辑坏了，
# 整个 cut 相位结论不可信 => 中止（不让它继续产出 PASS）。
# ============================================================
cut_verdict_selftest() {
	local failed=0 total=0
	local l2 l3 latest frozen skip have want got name
	while IFS='|' read -r name l2 l3 latest frozen skip have want; do
		[[ -z "$name" ]] && continue
		total=$((total+1))
		got="$(cut_verdict "$l2" "$l3" "$latest" "$frozen" "$skip" "$have" "selftest")"
		got="${got%%|*}"
		if [[ "$got" != "$want" ]]; then
			echo "    [X]  $name：got=$got want=$want （l2=$l2 l3=$l3 latest=$latest frozen=$frozen skip=$skip have=$have）"
			failed=$((failed+1))
		fi
	done <<'CASES'
五条全中 => PASS|1|1|1|1|1|1|PASS
计数器不动但 L2 在动 => 开关没生效|1|1|1|1|0|1|FAIL
skip 取不到 => 不能判通过|1|1|1|1|0|0|UNKNOWN
全都没动 => 设备安静 or 切法C|0|0|0|1|0|1|UNKNOWN
unified 仍在增长 => 开关没生效|0|0|0|0|0|1|FAIL
读不到 unified max => 无法判定|0|0|0|2|0|1|UNKNOWN
切法C: latest 前进但 L2/L3 死|0|0|1|1|0|1|FAIL
切法C 变体: L3 活着 L2 死|1|0|1|1|0|1|FAIL
不完整组合 => 不判通过|1|1|0|1|0|1|FAIL
全静 + skip 取不到|0|0|0|1|0|0|UNKNOWN
CASES
	if [[ "$failed" -gt 0 ]]; then
		echo "    组合判定自检：$failed/$total 个边界组合结论错误 => 判定逻辑失效，cut 结论不可信"
		return 1
	fi
	echo "    组合判定自检：$total/$total 个边界组合结论正确 ✓"
	return 0
}

phase_cut() {
  log "=== phase=cut: 停写切点判据（三 sink 存活 + unified 冻结）==="

  if [[ "$(psql_q "SELECT 1")" != "1" ]]; then
    mark_fail "23" "DB 不可达，无法取样"
    return 0
  fi

  # ---- 前置 A：开关的直接证据（不是自述）----
  #
  # 这里**先取证据，再决定是否需要自述兜底**。顺序很重要：
  # 如果先看 DRILL_CUT_CONFIRMED，人就会习惯性传 1，直接证据永远没人看。
  local sw_env sw_started skip0
  sw_env="$(app_container_env "$EnvUnifiedWrite")"
  sw_started="$(app_container_started_epoch)"
  skip0="$(unified_skip_counter)"

  echo ""
  echo "  ── 开关证据（直接观测，不靠自述）──"
  printf "    容器 %-22s EHOME_TELEMETRY_UNIFIED_WRITE = %s\n" \
    "${APP_CONTAINER:-<未指定>}" "${sw_env:-<取不到>}"
  if [[ -n "$sw_started" ]]; then
    printf "    容器 StartedAt = %s（%s 前）\n" \
      "$(date -u -d "@$sw_started" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "$sw_started")" \
      "$(( $(date -u +%s) - sw_started ))s"
  else
    printf "    容器 StartedAt = %s\n" "<取不到>"
  fi
  printf "    skip 计数器 = %s\n" "${skip0:-<取不到>}"
  echo ""

  # 证据链判定：容器 env 明确为 false => 直接证据成立，不再需要自述。
  local cut_evidence="none"
  if [[ "$sw_env" == "false" ]]; then
    cut_evidence="env"
  fi

  if [[ "$cut_evidence" == "none" ]]; then
    # 证据不足：只接受显式自述，但**必须在报告里标注证据等级为「自述」**。
    if [[ "${DRILL_CUT_CONFIRMED:-0}" != "1" ]]; then
      echo "  停写开关**没有直接证据**，本项不执行（不是通过）。"
      echo ""
      echo "  推荐做法（有直接证据，不需要自述）："
      echo "    APP_CONTAINER=ehome-prod-web \\"
      echo "    docker inspect 能读到容器 env => 脚本自动判定，无需 DRILL_CUT_CONFIRMED"
      echo ""
      echo "  兜底做法（无 docker 权限时）："
      echo "    1) 设 EHOME_TELEMETRY_UNIFIED_WRITE=false 并**重建容器**"
      echo "    2) 等至少 30 秒（让在途事件走完）"
      echo "    3) DRILL_CUT_CONFIRMED=1 bash $0 --phase=cut"
      echo ""
      echo "  为什么读不到 env 时不自动放行：没有证据就不能产出「通过」，"
      echo "  那是假绿 —— 而停写没生效的后果是所有判据都指向「一切正常」。"
      mark_skip "23" "开关无直接证据且未自述（既没传 APP_CONTAINER，也没传 DRILL_CUT_CONFIRMED=1）=> 未执行"
      return 0
    fi
    cut_evidence="self-report"
  fi

  if [[ "$cut_evidence" == "self-report" ]]; then
    echo "  [证据等级：自述] DRILL_CUT_CONFIRMED=1，但读不到容器 env。"
    echo "  => 本项的「开关生效」这一半**只由操作者声明保证**，报告会如实标注。"
    echo ""
  fi

  # 重启证据：env 是 false 但容器是很久以前启动的 => 极可能是「改了 env 没重建」。
  # 这一条能挡住最常见的手滑，而它**无法**由自述挡住（自述说的是「我做了」）。
  if [[ "$cut_evidence" == "env" ]]; then
    local now_epoch uptime
    now_epoch="$(date -u +%s)"
    if [[ -n "$sw_started" ]]; then
      uptime=$(( now_epoch - sw_started ))
      local max_uptime=$(( CUT_SAMPLE_SECONDS * 4 ))
      if [[ "$uptime" -gt "$max_uptime" ]]; then
        echo "  [警告] 容器已运行 ${uptime}s（> ${max_uptime}s = 4x 采样窗口），"
        echo "         而 env 是 false。这不一定是错（可能装了就配了 false），"
        echo "         但请确认「这个 env 是本轮才改的」——若本轮刚改，容器**必须重建**才生效。"
        echo "         （开关在构造时读取，改 compose 文件而不重建 = 不生效。）"
        echo ""
      fi
    fi
  fi


  echo ""
  # 组合判定自检：先证明判定逻辑本身没坏，再看真实观测。
  # 顺序很重要 —— 先跑自检，坏了的判定逻辑不该有机会产出 PASS。
  if ! cut_verdict_selftest; then
    mark_fail "23" "组合判定自检未通过 => 判定逻辑失效，本项结论不可信"
    return 0
  fi

  echo "  采样窗口: ${CUT_SAMPLE_SECONDS}s"
  echo "  （必须 > 2x 心跳 60s 且 > 2x rollup flush 60s，否则会误判；默认 150s）"
  echo ""

  local W="$CUT_SAMPLE_SECONDS"

  # ---- T0 ----
  local f0 r0 u0 l0
  f0="$(psql_q "SELECT count(*) FROM telemetry_frame WHERE ts >= now() - interval '$W seconds'")"
  r0="$(cut_max_epoch telemetry_rollup_1m bucket)"
  u0="$(cut_max_epoch unified_data timestamp)"
  l0="$(latest_values_max_epoch)"
  # skip0 已在前置 A 取过一次；这里**重取**而不是复用 ——
  # 前置 A 到 T0 之间可能已经过去几秒，复用会让 delta 偏小（可能少算一批）。
  local skip_t0; skip_t0="$(unified_skip_counter)"
  echo "  T0: frame(recent)=${f0:-?}  rollup.max=${r0:-?}  unified.max=${u0:-?}  latest.max=${l0:-取不到}  skip=${skip_t0:-取不到}"

  echo "  等待 ${W}s ..."
  sleep "$W"

  # ---- T1 ----
  local f1 r1 u1 l1
  f1="$(psql_q "SELECT count(*) FROM telemetry_frame WHERE ts >= now() - interval '$W seconds'")"
  r1="$(cut_max_epoch telemetry_rollup_1m bucket)"
  u1="$(cut_max_epoch unified_data timestamp)"
  l1="$(latest_values_max_epoch)"
  local skip_t1; skip_t1="$(unified_skip_counter)"
  echo "  T1: frame(recent)=${f1:-?}  rollup.max=${r1:-?}  unified.max=${u1:-?}  latest.max=${l1:-取不到}  skip=${skip_t1:-取不到}"
  if [[ -n "${skip_t0:-}" && -n "${skip_t1:-}" ]]; then
    echo "      skip 计数器 delta = $(( skip_t1 - skip_t0 ))（>0 直接证明「开关生效且上游有数据」）"
  fi
  echo ""

  # ---- 四条判据 ----
  # ① L2 帧表：T1 的「最近 W 秒行数」> 0 就是 W 秒内有新帧。
  #    不用 max(ts) 判 —— 安静设备下 max(ts) 不前进，会被误判成 sink 死了。
  local l2_alive=0 l3_alive=0 latest_alive=0 unified_frozen=0
  [[ "${f1:-0}" =~ ^[0-9]+$ && "${f1:-0}" -gt 0 ]] && l2_alive=1
  [[ "${r1:-}" =~ ^[0-9]+$ && "${r0:-}" =~ ^[0-9]+$ && "$r1" -gt "$r0" ]] && l3_alive=1
  [[ "${l1:-}" =~ ^[0-9]+$ && "${l0:-}" =~ ^[0-9]+$ && "$l1" -gt "$l0" ]] && latest_alive=1
  [[ -n "${u1:-}" && "${u0:-}" == "${u1:-}" ]] && unified_frozen=1

  # ⑤ 停写开关计数器增长 —— 这是**唯一一条同时证明两件事**的判据：
  #    「开关生效」+「上游确实有数据到达」。
  # 仅仅 unified 冻结不够：上游断了它也会冻结。
  # 仅仅 L2/L3 增长也不够：那证明数据在来，但没证明 unified **为什么**不写。
  local skip_alive=0 skip_delta=""
  if [[ "${skip_t0:-}" =~ ^[0-9]+$ && "${skip_t1:-}" =~ ^[0-9]+$ ]]; then
    skip_delta=$(( skip_t1 - skip_t0 ))
    [[ "$skip_delta" -gt 0 ]] && skip_alive=1
  fi

  echo "  判定："
  printf "    ① L2 帧表 最近 %ss 有新增:  %s\n" "$W" "$([[ $l2_alive -eq 1 ]] && echo 是 || echo 否)"
  printf "    ② L3 rollup 前进:          %s\n" "$([[ $l3_alive -eq 1 ]] && echo 是 || echo 否)"
  printf "    ③ latest 缓存前进:         %s\n" "$([[ $latest_alive -eq 1 ]] && echo 是 || echo 否)"
  printf "    ④ unified_data 冻结:       %s\n" "$([[ $unified_frozen -eq 1 ]] && echo 是 || echo 否)"
  if [[ -n "$skip_delta" ]]; then
    printf "    ⑤ 停写计数器增长:          %s（delta=%s）\n" \
      "$([[ $skip_alive -eq 1 ]] && echo 是 || echo 否)" "$skip_delta"
  else
    printf "    ⑤ 停写计数器增长:          %s\n" "取不到（/metrics 不可达？）"
  fi
  echo ""

  # ---- 组合判定：调用纯函数（分支逻辑本身可被单测穷举）----
  #
  # 把 frozen 编码成三态：2=读不到 / 1=冻结 / 0=仍在增长。
  # 「读不到」必须与「在增长」区分 —— 前者是**没有证据**，后者是**开关没生效**，
  # 混成一句会把「本轮没取证」误报成「开关坏了」，排查方向直接跑偏。
  local frozen_tri
  if [[ -z "${u0:-}" || -z "${u1:-}" ]]; then
    frozen_tri=2
  elif [[ "${u0:-}" == "${u1:-}" ]]; then
    frozen_tri=1
  else
    frozen_tri=0
  fi

  local have_skip=0
  [[ -n "$skip_delta" ]] && have_skip=1

  local verdict raw
  raw="$(cut_verdict "$l2_alive" "$l3_alive" "$latest_alive" "$frozen_tri" "$skip_alive" "$have_skip"     "frame(recent)=${f1:-?} rollup=${r0:-?}=>${r1:-?} unified.max=${u1:-?} skip_delta=${skip_delta:-?} 证据等级=${cut_evidence}")"
  verdict="${raw%%|*}"; detail="${raw#*|}"

  case "$verdict" in
    PASS)    mark_pass "23" "$detail" ;;
    FAIL)    mark_fail "23" "$detail" ;;
    UNKNOWN) mark_unknown "23" "$detail" ;;
    *)       mark_fail "23" "cut_verdict 返回了未知结论 "$verdict"（脚本 bug）" ;;
  esac
  return 0
}

# ============================================================
# 汇总：三态单列，未执行单独一列
# ============================================================
REPORT_TOTAL=0; REPORT_EXECUTED=0; REPORT_UNEXEC=0; REPORT_FAILED=0; REPORT_NOTREQ=0
# 第五态(CONFIG)与第六态(OBSERVE)也要初始化 ——
# 未初始化时 set -u 会炸；而用 ${VAR:-0} 兜底又会掩盖「忘了赋值」这种 bug。
REPORT_CFG=0; REPORT_CFG_IDS=""
REPORT_UNKNOWN=0; REPORT_UNKNOWN_IDS=""
REPORT_OBS=0; REPORT_OBS_IDS=""
REPORT_UNEXEC_IDS=""

# REQUESTED_PHASES: 本次请求的相位集合（前后带空格，便于 *" x "* 匹配）。
# 用来区分两种「没结果」：
#   未执行 = 请求了但没跑成        => 事故
#   未请求 = 本次压根没要求这个相位 => 整轮不完整，但不是事故
# 混成一个数字会让 --phase=time 永远显示「未执行 24 项」，人很快就开始忽略那一列。
REQUESTED_PHASES=" "

report() {
  local phase_filter="${1:-all}"
  echo ""
  echo "################ 演练结果 ################"
  printf "  %-5s %-7s %-6s %s\n" id phase 状态 说明
  local executed=0 unexecuted=0 failed=0 total=0 notreq=0 cfg=0 obs=0 unknown=0
  local unexec_ids="" notreq_ids="" cfg_ids="" obs_ids="" unknown_ids=""
  local line id phase what status note
  while IFS= read -r line; do
    id="$(echo "$line" | cut -d"~" -f1)"
    phase="$(echo "$line" | cut -d"~" -f2)"
    what="$(echo "$line" | cut -d"~" -f3)"
    if [[ "$phase_filter" != "all" && "$phase" != "$phase_filter" ]]; then continue; fi
    total=$((total+1))
    status="${RESULT[$id]:-}"
    if [[ -z "$status" && "$REQUESTED_PHASES" != *" $phase "* ]]; then
      notreq=$((notreq+1)); notreq_ids="$notreq_ids $id"
      printf "  %-5s %-7s %-6s %s\n" "$id" "$phase" 未请求 "本次未请求 phase=$phase"
      continue
    fi
    status="${status:-SKIP}"
    if [[ "$status" == "SKIP" ]]; then
      note="${SKIPREASON[$id]:-未执行}"
      unexecuted=$((unexecuted+1))
      unexec_ids="$unexec_ids $id"
      printf "  %-5s %-7s %-6s %s\n" "$id" "$phase" 未执行 "$note"
    elif [[ "$status" == "UNKNOWN" ]]; then
      # 证据不足：跑了但拿不到结论。不并入未执行（否则看不出「跑了」），
      # 也不并入失败（否则让人去查不存在的缺陷）。
      note="${SKIPREASON[$id]:-证据不足}"
      unknown=$((unknown+1))
      unknown_ids="$unknown_ids $id"
      printf "  %-5s %-7s %-6s %s\n" "$id" "$phase" 无结论 "$note"
    elif [[ "$status" == "CONFIG" ]]; then
      # 配置未开：既不是通过，也不是失败。单独计数。
      note="${SKIPREASON[$id]:-功能未启用}"
      cfg=$((cfg+1))
      cfg_ids="$cfg_ids $id"
      printf "  %-5s %-7s %-6s %s\n" "$id" "$phase" 配置未开 "$note"
    elif [[ "$status" == "OBSERVE" ]]; then
      # 观察项：只记录，不判定。既不算通过也不算失败，不影响退出码。
      obs=$((obs+1))
      obs_ids="$obs_ids $id"
      printf "  %-5s %-7s %-6s %s\n" "$id" "$phase" 观察 "${DETAIL[$id]:-$what}"
    elif [[ "$status" == "PASS" ]]; then
      executed=$((executed+1))
      printf "  %-5s %-7s %-6s %s\n" "$id" "$phase" 通过 "${DETAIL[$id]:-$what}"
    else
      executed=$((executed+1)); failed=$((failed+1))
      printf "  %-5s %-7s %-6s %s\n" "$id" "$phase" 失败 "${DETAIL[$id]:-$what}"
    fi
  done <<< "$(printf "%s\n" "${MANIFEST[@]}")"
  echo ""
  echo "  共 $total 项，执行 $executed 项，未执行 $unexecuted 项，无结论 $unknown 项，配置未开 $cfg 项，观察 $obs 项，未请求 $notreq 项，失败 $failed 项"
  if [[ "$obs" -gt 0 ]]; then
    echo "  观察 id:$obs_ids"
    echo "  「观察」**只记录、不判定** —— 这些是已定案的设计取舍，不是回归。"
    echo "  它们的数值仅供参考；不要因为它们的存在认为演练没跑完，也不要因为数值大就当失败。"
  fi
  if [[ "$cfg" -gt 0 ]]; then
    echo "  配置未开 id:$cfg_ids"
    echo "  「配置未开」不是通过也不是失败 —— 功能没启用，这项**没被验证**。"
    echo "  要么打开配置重跑，要么在结论里显式排除它（不能默认它没问题）。"
  fi
  if [[ "$unknown" -gt 0 ]]; then
    echo "  无结论 id:$unknown_ids"
    echo "  「无结论」不是通过 —— 这项**跑了**，但拿到的观测不足以支持任何结论。"
    echo "  典型原因：设备安静（所有 sink 都没动）、读不到判据值、组合不完整。"
    echo "  处置同「未执行」：没有结论就不能上线。"
  fi
  if [[ "$unexecuted" -gt 0 ]]; then
    echo "  未执行 id:$unexec_ids"
    echo "  「未执行」不是通过 —— 请求了却没有对系统做过任何检验。"
  fi
  if [[ "$notreq" -gt 0 ]]; then
    echo "  未请求 id:$notreq_ids"
    echo "  「未请求」表示本次没跑那个相位 —— 整轮演练不完整，不等于全过。"
  fi
  REPORT_TOTAL=$total; REPORT_EXECUTED=$executed
  REPORT_UNEXEC=$unexecuted; REPORT_FAILED=$failed; REPORT_NOTREQ=$notreq
  REPORT_CFG=$cfg; REPORT_CFG_IDS="$cfg_ids"
  REPORT_UNKNOWN=$unknown; REPORT_UNKNOWN_IDS="$unknown_ids"
  REPORT_OBS=$obs; REPORT_OBS_IDS="$obs_ids"
  REPORT_UNEXEC_IDS="$unexec_ids"
  return 0
}

# ============================================================
# main
# ============================================================
PHASES=(); MODE="run"; PHASE_FILTER="all"
for arg in "$@"; do
  case "$arg" in
    --phase=*) PHASE_FILTER="${arg#--phase=}"; PHASES+=("$PHASE_FILTER"); REQUESTED_PHASES="$REQUESTED_PHASES$PHASE_FILTER ";;
    --all) PHASES=(space time cut); REQUESTED_PHASES=" space time cut manual ";;
    --restore) MODE="restore";;
    --verify-manifest) MODE="manifest";;
    --allow-skip) ALLOW_SKIP=1;;
    --force) FORCE=1;;
    *) die "未知参数: $arg";;
  esac
done

if [[ "$MODE" == "manifest" ]]; then verify_manifest; exit $?; fi
if [[ "$MODE" == "restore" ]]; then
  # 先读回落盘的基线：把分区挂回去却不知道「该有多少行」，核验①就是废的。
  if [[ -f "$STATE_FILE" ]]; then
    # shellcheck disable=SC1090
    source "$STATE_FILE"
    log "读回演练状态: ROWS_BEFORE=$ROWS_BEFORE  DB=$DB  STARTED_AT=$STARTED_AT"
    if [[ -n "$DB" && "$DB" != "$POSTGRES_DB" ]]; then
      die "状态文件属于数据库 $DB，当前 POSTGRES_DB=$POSTGRES_DB —— 拒绝跨库恢复"
    fi
  else
    log "[warn] 没有找到演练状态文件 ($STATE_FILE)"
    log "       基线未知 => 核验①（行数 == 演练前）无法通过，会报 exit 3。"
    log "       这不是数据问题，是缺基线。原始行数只能人工核对。"
  fi
  restore_all
  exit $EXIT_OK
fi

if ! verify_manifest >/tmp/drill_manifest.log 2>&1; then
  echo "[演练][拒绝] MANIFEST 与清单文档不一致，详见 /tmp/drill_manifest.log" >&2
  cat /tmp/drill_manifest.log >&2
  exit $EXIT_FAIL
fi

if ! psql_q "SELECT 1" >/dev/null 2>&1; then
  die "无法连接 postgres 容器（docker compose exec 失败）"
fi

if [[ ${#PHASES[@]} -eq 0 ]]; then
  die "请指定 --phase=space|time|cut 或 --all 或 --restore 或 --verify-manifest"
fi

for ph in "${PHASES[@]}"; do
  case "$ph" in
    space) phase_space;;
    time)  phase_time;;
    cut)   phase_cut;;
    # manual 相位没有自动检查，只有人工插点（phase_space 里已登记为未执行）。
    # 单独 --phase=manual 时不应报「未知 phase」。
    manual) log "=== phase=manual: 无自动检查，逐项人工完成（清单 §0.2 / §0.3）===";;
  esac
done

report all

if [[ "$RESTORE_FAILED" -eq 1 ]]; then print_restore_failure; exit $EXIT_RESTORE_FAILED; fi
if [[ "$REPORT_FAILED" -gt 0 ]]; then
  echo "  [X] 有 $REPORT_FAILED 项失败"
  exit $EXIT_FAIL
fi
# 未执行（请求了但没跑成）的三档语义：
#   不加 --allow-skip => 退出码 1（当成失败：判据没跑过 = 没拿到证据）
#   加 --allow-skip   => 退出码 2（显式接受，但**绝不返回 0**）
# 为什么 --allow-skip 不能变 0：那它就成了伪造全绿的工具。
# 为什么默认是 1 而不是 2：默认情况下「没验过」和「验了没过」在决策上等价 ——
#   都不能上线。把默认设成 2（更"轻"的码）会诱导人忽略它。
# 配置未开（CONFIG）：**功能没启用，这项没被验证**。
# 处置与「未执行」相同 —— 没有证据就不能返回 0。
# 为什么不同「失败」：接口本身没坏，把它算进失败会让人去查不存在的 bug。
# 为什么不同「通过」：它确实什么都没验。
if [[ "$REPORT_CFG" -gt 0 ]]; then
  if [[ "$ALLOW_SKIP" -eq 1 ]]; then
    echo "  [!] 有 $REPORT_CFG 项「配置未开」（--allow-skip 已显式放行）=> 退出码 2，不是 0"
    exit $EXIT_SKIP
  fi
  echo "  [X] 有 $REPORT_CFG 项「配置未开」—— 功能没启用 = 没验过，不是通过 => 退出码 1"
  echo "      要么打开配置重跑，要么用 --allow-skip 显式放行（那时退出码 2）"
  exit $EXIT_FAIL
fi

# 无结论（跑了但证据不足）：与「未执行」同档 —— 没有结论就不能返回 0。
# 为什么不同「失败」：没发现缺陷，只是没拿到足以判定的观测。
if [[ "$REPORT_UNKNOWN" -gt 0 ]]; then
  if [[ "$ALLOW_SKIP" -eq 1 ]]; then
    echo "  [!] 有 $REPORT_UNKNOWN 项无结论（--allow-skip 已显式放行）=> 退出码 2，不是 0"
    exit $EXIT_SKIP
  fi
  echo "  [X] 有 $REPORT_UNKNOWN 项无结论 —— 跑了但证据不足，不等于通过 => 退出码 1"
  exit $EXIT_FAIL
fi
if [[ "$REPORT_UNEXEC" -gt 0 ]]; then
  # 不再重复打印 id 列表 —— report() 已经打过一次。
  # 同一份清单印两遍会让人以为有两组不同的未执行项（实测输出里就是这样）。
  if [[ "$ALLOW_SKIP" -eq 1 ]]; then
    echo "  [!] 有 $REPORT_UNEXEC 项未执行（--allow-skip 已显式放行）=> 退出码 2，不是 0"
    exit $EXIT_SKIP
  fi
  echo "  [X] 有 $REPORT_UNEXEC 项未执行 —— 未执行不是通过（没有证据）=> 退出码 1"
  echo "      确实无法执行时用 --allow-skip 显式放行（那时退出码 2）"
  exit $EXIT_FAIL
fi

# 未请求（本次没跑那个相位）=> 整轮不完整。
# 若只跑了部分相位就返回 0，人会把「跑了一个相位全绿」当成「整轮通过」。
# => 必须 2；除非 --all 全跑了。
if [[ "$REPORT_NOTREQ" -gt 0 ]]; then
  echo "  [!] 有 $REPORT_NOTREQ 项未请求（本次只跑了部分相位）=> 整轮不完整，退出码 2"
  echo "      要一轮完整演练请用 --all"
  exit $EXIT_SKIP
fi

# 观察项不计入「通过」，所以终态文案要分开说 ——
# 否则「全部 26 项执行且通过」会把 1 项「只记录不判定」也算成通过（假绿）。
if [[ "${REPORT_OBS:-0}" -gt 0 ]]; then
  echo "  [OK] $((REPORT_TOTAL - REPORT_OBS)) 项执行且通过，另有 $REPORT_OBS 项为观察项（只记录、不判定）"
else
  echo "  [OK] 全部 $REPORT_TOTAL 项执行且通过"
fi
exit $EXIT_OK
