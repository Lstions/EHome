#!/usr/bin/env bash
# 实测两个新索引在 bench 库上的 CREATE INDEX 耗时与体积；默认建完即 DROP（恢复 before 基线态）。
# 注意：本脚本只测量索引 DDL 成本，不修改修复代码；修复代码里的索引由
#       datalifecycle.EnsureLogicalDataIndexes 创建（同一 DDL）。
# 用法: scripts/perf-bms/index_bench.sh [label]    # KEEP_INDEXES=1 可保留索引
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
source "$HERE/lib.sh"
LABEL=${1:-before}
OUT="$BENCH_DIR/index-create-$LABEL.txt"
KEEP_INDEXES=${KEEP_INDEXES:-0}
mkdir -p "$BENCH_DIR"

{
psql_bench <<'SQL'
\timing on
SELECT '=== pg_settings (建索引相关) ===' AS info;
SELECT name||' = '||setting||coalesce(' '||unit,'') AS setting
FROM pg_settings
WHERE name IN ('shared_buffers','work_mem','maintenance_work_mem',
               'max_parallel_maintenance_workers','max_parallel_workers',
               'max_parallel_workers_per_gather','effective_cache_size','track_io_timing')
ORDER BY name;

SELECT '=== 表/索引体积 BEFORE ===' AS info;
SELECT c.relname AS rel, c.reltuples::bigint AS est_rows,
       pg_size_pretty(pg_table_size(c.oid)) AS heap,
       pg_size_pretty(pg_indexes_size(c.oid)) AS indexes,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total
FROM pg_class c
WHERE c.relname IN ('unified_data','unified_data_202610','device_data','device_data_202610')
ORDER BY rel;

SELECT '=== 现有索引（父表 + 当前月分区）===' AS info;
SELECT tablename||' :: '||indexname AS index
FROM pg_indexes
WHERE tablename IN ('unified_data','device_data','unified_data_202610','device_data_202610')
ORDER BY tablename, indexname;

SELECT '=== CREATE INDEX idx_unified_logical_sensor_ts ON unified_data (logical_device_id, sensor_name, timestamp) ===' AS info;
SELECT '（分区母表普通 CREATE INDEX；PG 自动传播到全部月份分区；生产升级时首次启动执行）' AS note;
CREATE INDEX IF NOT EXISTS idx_unified_logical_sensor_ts ON unified_data (logical_device_id, sensor_name, "timestamp");

SELECT '=== CREATE INDEX idx_devdata_logical_created ON device_data (logical_device_id, created_at DESC) ===' AS info;
CREATE INDEX IF NOT EXISTS idx_devdata_logical_created ON device_data (logical_device_id, created_at DESC);

SELECT '=== 表/索引体积 AFTER ===' AS info;
SELECT c.relname AS rel, c.reltuples::bigint AS est_rows,
       pg_size_pretty(pg_table_size(c.oid)) AS heap,
       pg_size_pretty(pg_indexes_size(c.oid)) AS indexes,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total
FROM pg_class c
WHERE c.relname IN ('unified_data','unified_data_202610','device_data','device_data_202610')
ORDER BY rel;

SELECT '=== 新索引实际落盘体积（父 + 各分片子索引）===' AS info;
SELECT c.relname AS index, pg_size_pretty(pg_relation_size(c.oid)) AS size
FROM pg_class c
WHERE c.relname LIKE '%logical_device_id_sensor_name_timestamp%'
   OR c.relname LIKE '%logical_device_id_created_at%'
   OR c.relname IN ('idx_unified_logical_sensor_ts','idx_devdata_logical_created')
ORDER BY c.relname;
SQL
} | tee "$OUT"

if [ "$KEEP_INDEXES" != "1" ]; then
  {
    psql_bench <<'SQL'
SELECT '=== 回滚：DROP 两个新索引（恢复 before 基线态）===' AS info;
DROP INDEX IF EXISTS idx_unified_logical_sensor_ts;
DROP INDEX IF EXISTS idx_devdata_logical_created;
SELECT '=== 回滚后校验（应为 0）===' AS info;
SELECT count(*) AS remaining_new_indexes FROM pg_class
WHERE relname IN ('idx_unified_logical_sensor_ts','idx_devdata_logical_created');
SQL
  } | tee -a "$OUT"
fi

echo "[index_bench] wrote $OUT"
