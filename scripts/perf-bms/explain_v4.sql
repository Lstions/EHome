-- v4（UNION ALL + covering 索引）实际 SQL 的 EXPLAIN (ANALYZE, BUFFERS).
-- SQL 形状与 backend/internal/api/scope_union.go 生成语句一致:
--   dataScopeBranches(device 14) = [A: logical_device_id IN (14)] + [B: logical_device_id IS NULL AND device_id IN (14)]
--   avg:   外拼 to_timestamp(floor(epoch/173)*173)+avg + GROUP BY 1
--   change: 每支 ORDER BY "timestamp", 外拼 lag() + prev IS DISTINCT FROM value
--   latest: 两段式 (A 分支命中即止, B 分支仅在 A 无行时执行)
\timing on
SET timezone = 'UTC';

SELECT '=== 0. 参数 ===' AS info;
SELECT :'end_ts' AS end_ts, :'start24' AS start24, :'start30d' AS start30d;

SELECT '=== 1. avg 单测点 24h：UNION ALL 两支 + covering 索引 ===' AS info;
EXPLAIN (ANALYZE, BUFFERS)
SELECT to_timestamp(floor(extract(epoch FROM "timestamp") / 173) * 173) AS "timestamp",
       avg("value") AS "value"
FROM (
  SELECT "timestamp", "value" FROM unified_data
  WHERE logical_device_id IN (14) AND sensor_name = 'cell_voltage_1'
    AND "timestamp" BETWEEN :'start24'::timestamptz AND :'end_ts'::timestamptz
  UNION ALL
  SELECT "timestamp", "value" FROM unified_data
  WHERE logical_device_id IS NULL AND device_id IN (14) AND sensor_name = 'cell_voltage_1'
    AND "timestamp" BETWEEN :'start24'::timestamptz AND :'end_ts'::timestamptz
) AS u
GROUP BY 1 ORDER BY 1;

SELECT '=== 2. change protection_status 30d：UNION ALL 两支各有 ORDER BY（Merge Append）===' AS info;
EXPLAIN (ANALYZE, BUFFERS)
SELECT s."timestamp", s."value" FROM (
  SELECT d."timestamp", d."value",
         lag(d."value") OVER (ORDER BY d."timestamp") AS prev
  FROM (
    (SELECT "timestamp", "value" FROM unified_data
     WHERE logical_device_id IN (14) AND sensor_name = 'protection_status'
       AND "timestamp" BETWEEN :'start30d'::timestamptz AND :'end_ts'::timestamptz
     ORDER BY "timestamp")
    UNION ALL
    (SELECT "timestamp", "value" FROM unified_data
     WHERE logical_device_id IS NULL AND device_id IN (14) AND sensor_name = 'protection_status'
       AND "timestamp" BETWEEN :'start30d'::timestamptz AND :'end_ts'::timestamptz
     ORDER BY "timestamp")
  ) AS d
) AS s
WHERE s.prev IS DISTINCT FROM s."value"
ORDER BY s."timestamp";

SELECT '=== 3a. latest-data A 分支（logical，命中即止）===' AS info;
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM device_data WHERE logical_device_id IN (14) ORDER BY created_at DESC LIMIT 1;

SELECT '=== 3b. latest-data B 分支（NULL-logical 部分索引）===' AS info;
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM device_data WHERE logical_device_id IS NULL AND device_id IN (14) ORDER BY created_at DESC LIMIT 1;

SELECT '=== 4. 行数/语义断言 ===' AS info;
SELECT count(*) AS change_points_protection_30d FROM (
  SELECT s."timestamp", s."value" FROM (
    SELECT d."timestamp", d."value",
           lag(d."value") OVER (ORDER BY d."timestamp") AS prev
    FROM (
      (SELECT "timestamp", "value" FROM unified_data
       WHERE logical_device_id IN (14) AND sensor_name = 'protection_status'
         AND "timestamp" BETWEEN :'start30d'::timestamptz AND :'end_ts'::timestamptz ORDER BY "timestamp")
      UNION ALL
      (SELECT "timestamp", "value" FROM unified_data
       WHERE logical_device_id IS NULL AND device_id IN (14) AND sensor_name = 'protection_status'
         AND "timestamp" BETWEEN :'start30d'::timestamptz AND :'end_ts'::timestamptz ORDER BY "timestamp")
    ) AS d
  ) AS s WHERE s.prev IS DISTINCT FROM s."value" ORDER BY s."timestamp"
) x;
SELECT count(*) AS avg_points_cell_voltage_1_24h FROM (
  SELECT to_timestamp(floor(extract(epoch FROM "timestamp") / 173) * 173) AS "timestamp",
         avg("value") AS "value"
  FROM (
    SELECT "timestamp", "value" FROM unified_data
    WHERE logical_device_id IN (14) AND sensor_name = 'cell_voltage_1'
      AND "timestamp" BETWEEN :'start24'::timestamptz AND :'end_ts'::timestamptz
    UNION ALL
    SELECT "timestamp", "value" FROM unified_data
    WHERE logical_device_id IS NULL AND device_id IN (14) AND sensor_name = 'cell_voltage_1'
      AND "timestamp" BETWEEN :'start24'::timestamptz AND :'end_ts'::timestamptz
  ) AS u GROUP BY 1 ORDER BY 1
) x;
