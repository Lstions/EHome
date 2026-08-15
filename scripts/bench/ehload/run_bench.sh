#!/usr/bin/env bash
# 分级压测编排: 每档跑 duration 秒, 档间休息 gap 秒
# 每档前后做 DB 快照, 精确计算 unified_data/device_data 增量 vs 发送数 → 丢率
# 用法: ./run_bench.sh
set -uo pipefail
cd /tmp/ehload

PSQL() { docker exec ehome-postgres psql -U ehome -d ehome -t -A -c "$1"; }

# 动态解析设备 id 起点: DELETE 不重置序列, 每次 prepare 后起点变化(3→1003→2003...)。
# 固定 --device-base 3 会导致压测帧携带不存在的设备 id → 后端 record not found 整档丢弃。
BASE=$(PSQL "SELECT min(id) FROM edge_devices WHERE node_id LIKE 'SIM%';" | tr -d ' ')
if [ -z "$BASE" ] || [ "$BASE" = "NULL" ]; then
  echo "ERROR: 未找到 SIM 设备, 请先运行 scripts/bench/prepare_bench_data.sh <N>"
  exit 1
fi
MAX=$((BASE + 999))
echo "==> 设备 id 范围: $BASE..$MAX (动态解析)"

# 快照: unified/device 中 SIM 设备相关行数
snapshot() {
  echo "$(PSQL "SELECT count(*) FROM unified_data WHERE device_id BETWEEN $BASE AND $MAX;")|$(PSQL "SELECT count(*) FROM device_data WHERE node_id LIKE 'SIM%';")|$(PSQL "SELECT count(*) FROM edge_devices WHERE node_id LIKE 'SIM%' AND status='active';")"
}

LOG=/home/sun/workspace/EHomeSystem/.logs/backend.log
logcount() { wc -l < "$LOG"; }

run_stage() {
  local name=$1 nodes=$2 rate=$3 dur=$4 gap=$5 qos=$6
  echo ""
  echo "############ 档位 $name: ${nodes}节点 x ${rate}Hz = $((nodes*rate)) msg/s (${dur}s) ############"
  local s0 l0
  s0=$(snapshot)
  l0=$(logcount)
  ./ehload --nodes "$nodes" --rate "$rate" --duration "$dur" --qos "$qos" --device-base "$BASE" 2>&1 | tail -2
  echo "  [等待 $gap s 清空积压...]"
  sleep "$gap"
  local s1 l1
  s1=$(snapshot)
  l1=$(logcount)
  local u0 d0 a0 u1 d1 a1
  IFS='|' read -r u0 d0 a0 <<<"$s0"
  IFS='|' read -r u1 d1 a1 <<<"$s1"
  local sent
  sent=$((nodes*rate*dur))
  local unified_delta=$((u1-u0)) device_delta=$((d1-d0))
  # sensor_parser 处理数 = unified_delta/2 (每事件2字段), db_persist 处理数 = device_delta - unified_delta (sensor_parser 也写 device_data)
  local parsed=$((unified_delta/2))
  local dbpersist=$((device_delta-unified_delta))
  local drop=$((sent-parsed))
  echo "  == 对账 == 发送=$sent  parser处理=$parsed (unified+$unified_delta 行)  db_persist=$dbpersist (device_data+$device_delta 行)"
  echo "  丢弃=$drop ($(awk "BEGIN{printf \"%.1f\", $drop*100/$sent}")%)  活跃设备=$a1"
  echo "  日志增量=$((l1-l0)) 行"
  echo "RESULT|$name|$sent|$parsed|$dbpersist|$drop|$((l1-l0))"
}

echo "=== 压测开始 $(date +%T) ==="
echo "后端 PID: $(lsof -ti :8082)"

# 采样器 300s 后台 (覆盖全部档位)
(python3 sampler.py /tmp/ehload/bench_sample.csv 360 > /tmp/ehload/bench_sampler.log 2>&1 &)

sleep 5
run_stage "S1-400"  200  2  60  15 1
run_stage "S2-2000" 500  4  60  15 1
run_stage "S3-5000" 1000 5  60  15 1
run_stage "S4-10000" 1000 10 60  20 1

echo ""
echo "=== 压测结束 $(date +%T) ==="
