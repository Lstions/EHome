#!/usr/bin/env bash
# 实机稳定性门禁：从生产环境读取"设备是否在偷偷重启"的硬证据。
#
# 为什么需要它（2026-10-04 教训）：
# 当天 S3 以每 8.8s 一次的频率擦 NVS 并重启，连续 10 分钟 67 次；而当时**所有**
# 自动化门禁都是绿的 —— 后端单测、140 个仿真场景、固件 host 测试、前端类型检查
# 无一失败。因为那些门禁断言的全是"服务端看到的 API 行为"，从来没有一条去看
# 设备的真实运行状态。是用户看到红灯/蓝灯/紫灯交替闪烁才发现的。
#
# 本脚本补上这个层次：直接查库，断言
#   1. 节点 uptime 在观测窗口内**单调上升**（任何回退都意味着重启）；
#   2. 窗口内没有新增的 CRASH/PANIC 诊断上报；
#   3. 节点状态为 online 且 last_seen 新鲜。
#
# 用法：
#   tools/device-stability-gate.sh [观测秒数] [容器名]
# 退出码：0 = 稳定；1 = 检测到重启/panic/掉线；2 = 环境不可用（无法判定）。
#
# 注意：这是**实机**门禁，需要能访问生产库；不适合放进 GitHub CI，
# 应在部署后与压力测试后手动/由发布流程调用。
set -uo pipefail

WINDOW_SEC="${1:-90}"
WEB_CONTAINER="${2:-ehome-prod-web}"
PSQL_CONTAINER="${3:-ehome-prod-postgres}"
DB_USER="${DB_USER:-ehome}"
DB_NAME="${DB_NAME:-ehome}"

if ! docker ps --format "{{.Names}}" | grep -qx "$PSQL_CONTAINER"; then
  echo "环境不可用：找不到容器 $PSQL_CONTAINER" >&2
  exit 2
fi

q() { docker exec "$PSQL_CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -At -c "$1"; }

echo "== 实机稳定性门禁：观测 ${WINDOW_SEC}s =="
echo "-- 观测前快照 --"
BEFORE="$(q "SELECT node_id||'|'||uptime_seconds||'|'||COALESCE(firmware_version,'') FROM nodes ORDER BY node_id;")"
echo "$BEFORE"
BEFORE_PANIC="$(q "SELECT count(*) FROM device_diag_reports WHERE report_type=2 AND reset_reason=4;")"
echo "panic 上报累计：$BEFORE_PANIC"

sleep "$WINDOW_SEC"

echo "-- 观测后快照 --"
AFTER="$(q "SELECT node_id||'|'||uptime_seconds||'|'||COALESCE(firmware_version,'') FROM nodes ORDER BY node_id;")"
echo "$AFTER"
AFTER_PANIC="$(q "SELECT count(*) FROM device_diag_reports WHERE report_type=2 AND reset_reason=4;")"
echo "panic 上报累计：$AFTER_PANIC"

RC=0

# 断言 1：uptime 必须单调上升。注意 uptime_seconds 是固件自报的"本次运行秒数"，
# 一旦设备重启就会归零/变小 —— 这是最直接、最难伪造的重启证据。
while IFS="|" read -r node_id up_before fw; do
  [ -z "$node_id" ] && continue
  up_after="$(printf '%s\n' "$AFTER" | awk -F'|' -v n="$node_id" '$1==n {print $2}')"
  if [ -z "$up_after" ]; then
    echo "FAIL [$node_id] 观测后查不到该节点（可能被删除或库异常）"
    RC=1; continue
  fi
  if [ "$up_after" -lt "$up_before" ]; then
    echo "FAIL [$node_id] uptime 回退 ${up_before}s -> ${up_after}s：设备在观测窗口内重启了"
    RC=1
  elif [ "$up_after" -eq "$up_before" ]; then
    echo "FAIL [$node_id] uptime 未增长（${up_before}s -> ${up_after}s）：设备可能已停止上报"
    RC=1
  else
    echo "ok   [$node_id] uptime ${up_before}s -> ${up_after}s（+$((up_after - up_before))s），未重启"
  fi
done <<< "$BEFORE"

# 断言 2：窗口内不得新增 PANIC（reset_reason=4）。
if [ "$AFTER_PANIC" -gt "$BEFORE_PANIC" ]; then
  echo "FAIL 观测窗口内新增 $((AFTER_PANIC - BEFORE_PANIC)) 条 PANIC 上报"
  RC=1
else
  echo "ok   窗口内无新增 PANIC 上报"
fi

# 断言 3：节点必须 online 且 last_seen 新鲜（否则"没重启"只是因为它已经掉线了）。
STALE="$(q "SELECT count(*) FROM nodes WHERE status<>'online' OR last_seen IS NULL OR now()-last_seen > interval '30 seconds';")"
if [ "${STALE:-0}" -gt 0 ]; then
  echo "FAIL 有 $STALE 个节点离线或 last_seen 超过 30s 未更新"
  q "SELECT node_id||' status='||status||' seen_age='||COALESCE(EXTRACT(EPOCH FROM (now()-last_seen))::int::text,'null') FROM nodes;"
  RC=1
else
  echo "ok   所有节点 online 且 last_seen 新鲜"
fi

if [ "$RC" -eq 0 ]; then
  echo "== 结论：稳定（观测 ${WINDOW_SEC}s，无重启 / 无 panic / 无掉线）=="
else
  echo "== 结论：未通过 —— 见上面的 FAIL ==" >&2
fi
exit "$RC"