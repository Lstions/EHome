-- 旧代码（main 分支）实际 SQL 的 PG 成本：与 handler_data.go:271-329 / :333-408
-- 生成的语句逐字一致，仅把时间参数换成 bench 固定窗口（psql -v 传入）。
-- 用法: scripts/perf-bms/explain.sh [out.txt]
\timing on
SET timezone = 'UTC';

SELECT '=== 0. 参数 ===' AS info;
SELECT :'end_ts' AS end_ts, :'start24' AS start24, :'start30d' AS start30d;

SELECT '=== 1. 单测点 24h：旧 SQL（OR scope 形态 + SELECT * + ORDER BY timestamp ASC）===' AS info;
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM "unified_data"
WHERE (logical_device_id IN (14) OR (logical_device_id IS NULL AND device_id IN (14)))
  AND sensor_name = 'cell_voltage_1'
  AND "timestamp" BETWEEN :'start24'::timestamptz AND :'end_ts'::timestamptz
ORDER BY "timestamp" ASC;

SELECT '=== 2. protection_status 30d：旧 SQL ===' AS info;
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM "unified_data"
WHERE (logical_device_id IN (14) OR (logical_device_id IS NULL AND device_id IN (14)))
  AND sensor_name = 'protection_status'
  AND "timestamp" BETWEEN :'start30d'::timestamptz AND :'end_ts'::timestamptz
ORDER BY "timestamp" ASC;

SELECT '=== 3. 命中行数口径（真实行数，不含被过滤行）===' AS info;
SELECT count(*) AS rows_cell_voltage_1_24h FROM unified_data
WHERE (logical_device_id IN (14) OR (logical_device_id IS NULL AND device_id IN (14)))
  AND sensor_name = 'cell_voltage_1'
  AND "timestamp" BETWEEN :'start24'::timestamptz AND :'end_ts'::timestamptz;
SELECT count(*) AS rows_protection_status_30d FROM unified_data
WHERE (logical_device_id IN (14) OR (logical_device_id IS NULL AND device_id IN (14)))
  AND sensor_name = 'protection_status'
  AND "timestamp" BETWEEN :'start30d'::timestamptz AND :'end_ts'::timestamptz;

SELECT '=== 4. latest-data：旧 SQL（device_data ORDER BY created_at DESC LIMIT 1）===' AS info;
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM "device_data"
WHERE (logical_device_id IN (14) OR (logical_device_id IS NULL AND device_id IN (14)))
ORDER BY created_at DESC
LIMIT 1;
