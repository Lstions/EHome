-- 修复后新 SQL 的 EXPLAIN (ANALYZE, BUFFERS)（BMS 页慢修复-实施契约-2026-10-20 §1.2）。
-- avg/change SQL 与 backend/internal/api/history_points.go 生成的语句逐字一致；
-- latest-data 的 SQL 未改（handler_edge_device.go:1269-1285），靠新索引加速。
-- 用法: psql -v end_ts=... -v start24=... -v start30d=... < explain_after.sql
\timing on
SET timezone = 'UTC';

SELECT '=== 0. 参数 ===' AS info;
SELECT :'end_ts' AS end_ts, :'start24' AS start24, :'start30d' AS start30d;

SELECT '=== 1. avg 模式：单测点 24h（W=ceil(86400/500)=173s）===' AS info;
EXPLAIN (ANALYZE, BUFFERS)
SELECT to_timestamp(floor(extract(epoch FROM "timestamp") / 173) * 173) AS "timestamp",
       avg("value") AS "value"
FROM "unified_data"
WHERE (logical_device_id IN (14) OR (logical_device_id IS NULL AND device_id IN (14)))
  AND sensor_name = 'cell_voltage_1'
  AND "timestamp" BETWEEN :'start24'::timestamptz AND :'end_ts'::timestamptz
GROUP BY 1 ORDER BY 1;

SELECT '=== 2. change 模式：protection_status 30d ===' AS info;
EXPLAIN (ANALYZE, BUFFERS)
SELECT s."timestamp", s."value" FROM (
  SELECT "timestamp", "value",
         lag("value") OVER (ORDER BY "timestamp") AS prev
  FROM "unified_data"
  WHERE (logical_device_id IN (14) OR (logical_device_id IS NULL AND device_id IN (14)))
    AND sensor_name = 'protection_status'
    AND "timestamp" BETWEEN :'start30d'::timestamptz AND :'end_ts'::timestamptz
) s
WHERE s.prev IS DISTINCT FROM s."value"
ORDER BY s."timestamp";

SELECT '=== 3. latest-data：SQL 未改，靠 idx_devdata_logical_created ===' AS info;
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM "device_data"
WHERE (logical_device_id IN (14) OR (logical_device_id IS NULL AND device_id IN (14)))
ORDER BY created_at DESC
LIMIT 1;

SELECT '=== 4. 对照：旧 raw SQL 在已建新索引后的计划（旧客户端/agg=raw 仍走它）===' AS info;
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM "unified_data"
WHERE (logical_device_id IN (14) OR (logical_device_id IS NULL AND device_id IN (14)))
  AND sensor_name = 'cell_voltage_1'
  AND "timestamp" BETWEEN :'start24'::timestamptz AND :'end_ts'::timestamptz
ORDER BY "timestamp" ASC;

SELECT '=== 5. 结果行数断言口径 ===' AS info;
SELECT count(*) AS avg_points_cell_voltage_1_24h FROM (
  SELECT to_timestamp(floor(extract(epoch FROM "timestamp") / 173) * 173) AS "timestamp",
         avg("value") AS "value"
  FROM "unified_data"
  WHERE (logical_device_id IN (14) OR (logical_device_id IS NULL AND device_id IN (14)))
    AND sensor_name = 'cell_voltage_1'
    AND "timestamp" BETWEEN :'start24'::timestamptz AND :'end_ts'::timestamptz
  GROUP BY 1 ORDER BY 1
) x;
SELECT count(*) AS change_points_protection_30d FROM (
  SELECT s."timestamp", s."value" FROM (
    SELECT "timestamp", "value",
           lag("value") OVER (ORDER BY "timestamp") AS prev
    FROM "unified_data"
    WHERE (logical_device_id IN (14) OR (logical_device_id IS NULL AND device_id IN (14)))
      AND sensor_name = 'protection_status'
      AND "timestamp" BETWEEN :'start30d'::timestamptz AND :'end_ts'::timestamptz
  ) s
  WHERE s.prev IS DISTINCT FROM s."value"
  ORDER BY s."timestamp"
) x;
