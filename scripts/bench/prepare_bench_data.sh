#!/usr/bin/env bash
# 准备压测数据: N 个模拟节点 + 每节点 1 个 lk_th01 边缘设备 (driver-backed)
# 用法: ./prepare_bench_data.sh <N>   (N=节点数, 默认 1000)
set -euo pipefail
N=${1:-1000}
PSQL="docker exec ehome-postgres psql -U ehome -d ehome -q -v ON_ERROR_STOP=1"

echo "==> 清理旧压测数据 (SIM*)"
$PSQL -c "DELETE FROM edge_devices WHERE node_id LIKE 'SIM%';"
$PSQL -c "DELETE FROM nodes WHERE node_id LIKE 'SIM%';"

echo "==> 插入 $N 个模拟节点 + 设备"
# 用 generate_series 批量插入, 比逐行快
$PSQL -c "
INSERT INTO nodes (node_id, name, status, created_at, updated_at)
SELECT 'SIM' || lpad(i::text, 4, '0'), '压测节点' || i, 'online', now(), now()
FROM generate_series(1, $N) AS i
ON CONFLICT (node_id) DO NOTHING;
"

$PSQL -c "
INSERT INTO edge_devices (type, parser_id, name, node_id, channel_id, device_config_id, hardware_id, interval_ms, enabled, status, error_code, init_state, init_total_steps, command_intervals, created_at, updated_at)
SELECT 'lk_th01', '', '压测温湿度' || i,
       'SIM' || lpad(i::text, 4, '0'),
       1, 0, '', 1000, true, 'active', 0, 'completed', 0, '{}', now(), now()
FROM generate_series(1, $N) AS i;
"

echo "==> 验证"
$PSQL -t -c "SELECT 'nodes=' || count(*) FROM nodes WHERE node_id LIKE 'SIM%';"
$PSQL -t -c "SELECT 'devices=' || count(*) FROM edge_devices WHERE node_id LIKE 'SIM%';"
