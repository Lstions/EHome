-- 生产量级造数（固定时间窗；seed.sh 每次 DROP 重建库后执行）。
--
-- 数据口径（与生产根因报告 §3 R1 / 取证 §2.2 对齐）：
--   1) 历史 8 天（24h 窗口之前）× 1 Hz：28 类 × 691,200 = 19,353,600 行，
--      加 protection_status 611,268 行（30d 侧该测点生产只有约 8.09 天数据）
--      = 19,964,868 行。
--   2) 最近 24h × 29 类 × 86,251 点 = 2,501,279 行（与生产每测点 86,251 行一致）。
--      合计 22,466,147 行 / 当前月分区约 4.0 GB（生产 202610 分区 22.84M 行 / 4.42 GB）。
--   3) device_data 2,000,000 行（约 430 B/行），时间升序落在当前月分区，
--      复现 latest-data 的 ORDER BY created_at DESC LIMIT 1 全表 top-N 成本。
--
-- 关键：所有行按时间戳**升序物理写入**（每帧 29 行），与生产采集写入顺序一致。
-- 这决定 unified_data."timestamp" 的 pg_stats.correlation ≈ 1；若按 category 分组写入
-- （correlation ≈ 0.37），planner 会选 BitmapAnd + 并行 bitmap heap scan，既偏离生产
-- 的 Parallel Index Scan 计划，也会触发本机 docker /dev/shm=64MB 的动态共享内存耗尽。
--
-- 时间变量（psql -v 传入）：end_ts / start24 / start30d。
\timing on

BEGIN;

-- 1) 历史 8 天 × 1 Hz（时间升序写入；protection_status 只在其 30d 覆盖段内出现）
INSERT INTO unified_data (device_id, sensor_name, value, unit, "timestamp", created_at,
                          edge_device_id, logical_device_id)
SELECT 14,
       c.cat,
       CASE
         WHEN c.cat = 'protection_status'
           THEN CASE WHEN (extract(epoch FROM t.ts)::bigint % 7200) BETWEEN 100 AND 129
                     THEN 1 ELSE 0 END
         ELSE c.base + (extract(epoch FROM t.ts)::bigint % c.modn) * c.step
       END,
       c.unit,
       t.ts, t.ts, 14, 14
FROM generate_series(
  :'end_ts'::timestamptz - interval '86251 seconds' - interval '8 days',
  :'end_ts'::timestamptz - interval '86252 seconds',
  interval '1 second') AS t(ts)
CROSS JOIN (VALUES
  ('cell_voltage_1',   3.200,  0.002, 13, 'V'),
  ('cell_voltage_2',   3.201,  0.002, 13, 'V'),
  ('cell_voltage_3',   3.202,  0.002, 13, 'V'),
  ('cell_voltage_4',   3.203,  0.002, 13, 'V'),
  ('cell_voltage_5',   3.204,  0.002, 13, 'V'),
  ('cell_voltage_6',   3.205,  0.002, 13, 'V'),
  ('cell_voltage_7',   3.206,  0.002, 13, 'V'),
  ('cell_voltage_8',   3.207,  0.002, 13, 'V'),
  ('cell_voltage_9',   3.208,  0.002, 13, 'V'),
  ('cell_voltage_10',  3.209,  0.002, 13, 'V'),
  ('cell_voltage_11',  3.210,  0.002, 13, 'V'),
  ('cell_voltage_12',  3.211,  0.002, 13, 'V'),
  ('cell_voltage_13',  3.212,  0.002, 13, 'V'),
  ('cell_voltage_14',  3.213,  0.002, 13, 'V'),
  ('cell_voltage_15',  3.214,  0.002, 13, 'V'),
  ('cell_voltage_16',  3.215,  0.002, 13, 'V'),
  ('total_voltage',    51.70,  0.01,  10, 'V'),
  ('current',           2.60,  0.02,  100, 'A'),
  ('rsoc',              7.0,   0.5,   6,  '%'),
  ('remaining_capacity',7.13,  0.01,  50, 'Ah'),
  ('nominal_capacity',  99.0,  0.0,   1,  'Ah'),
  ('cycle_count',       123,   0.0,   1,  '次'),
  ('temperature_1',     28.10, 0.1,   20, '°C'),
  ('temperature_2',     26.50, 0.1,   20, '°C'),
  ('temperature_3',     27.80, 0.1,   20, '°C'),
  ('cell_voltage_max',  3.242, 0.001, 5,  'V'),
  ('cell_voltage_min',  3.231, 0.001, 5,  'V'),
  ('fet_status',        3,     0.0,   1,  'bitmask'),
  ('protection_status', 0,     0.0,   1,  'bitmask')
) AS c(cat, base, step, modn, unit)
WHERE NOT (c.cat = 'protection_status'
           AND t.ts < :'end_ts'::timestamptz - interval '697519 seconds');

-- 2) 最近 24h × 29 类 × 86,251 = 2,501,279 行（时间升序写入）
INSERT INTO unified_data (device_id, sensor_name, value, unit, "timestamp", created_at,
                          edge_device_id, logical_device_id)
SELECT 14,
       c.cat,
       CASE
         WHEN c.cat = 'protection_status'
           THEN CASE WHEN (extract(epoch FROM t.ts)::bigint % 7200) BETWEEN 100 AND 129
                     THEN 1 ELSE 0 END
         ELSE c.base + (extract(epoch FROM t.ts)::bigint % c.modn) * c.step
       END,
       c.unit,
       t.ts, t.ts, 14, 14
FROM generate_series(
  :'end_ts'::timestamptz - interval '86251 seconds',
  :'end_ts'::timestamptz - interval '1 second',
  interval '1 second') AS t(ts)
CROSS JOIN (VALUES
  ('cell_voltage_1',   3.200,  0.002, 13, 'V'),
  ('cell_voltage_2',   3.201,  0.002, 13, 'V'),
  ('cell_voltage_3',   3.202,  0.002, 13, 'V'),
  ('cell_voltage_4',   3.203,  0.002, 13, 'V'),
  ('cell_voltage_5',   3.204,  0.002, 13, 'V'),
  ('cell_voltage_6',   3.205,  0.002, 13, 'V'),
  ('cell_voltage_7',   3.206,  0.002, 13, 'V'),
  ('cell_voltage_8',   3.207,  0.002, 13, 'V'),
  ('cell_voltage_9',   3.208,  0.002, 13, 'V'),
  ('cell_voltage_10',  3.209,  0.002, 13, 'V'),
  ('cell_voltage_11',  3.210,  0.002, 13, 'V'),
  ('cell_voltage_12',  3.211,  0.002, 13, 'V'),
  ('cell_voltage_13',  3.212,  0.002, 13, 'V'),
  ('cell_voltage_14',  3.213,  0.002, 13, 'V'),
  ('cell_voltage_15',  3.214,  0.002, 13, 'V'),
  ('cell_voltage_16',  3.215,  0.002, 13, 'V'),
  ('total_voltage',    51.70,  0.01,  10, 'V'),
  ('current',           2.60,  0.02,  100, 'A'),
  ('rsoc',              7.0,   0.5,   6,  '%'),
  ('remaining_capacity',7.13,  0.01,  50, 'Ah'),
  ('nominal_capacity',  99.0,  0.0,   1,  'Ah'),
  ('cycle_count',       123,   0.0,   1,  '次'),
  ('temperature_1',     28.10, 0.1,   20, '°C'),
  ('temperature_2',     26.50, 0.1,   20, '°C'),
  ('temperature_3',     27.80, 0.1,   20, '°C'),
  ('cell_voltage_max',  3.242, 0.001, 5,  'V'),
  ('cell_voltage_min',  3.231, 0.001, 5,  'V'),
  ('fet_status',        3,     0.0,   1,  'bitmask'),
  ('protection_status', 0,     0.0,   1,  'bitmask')
) AS c(cat, base, step, modn, unit);

-- 3) device_data 2,000,000 行（约 430 B/行，时间升序写入；跨度约 19.6 天全部落在当前月分区）
INSERT INTO device_data (device_id, node_id, data_json, "timestamp", created_at,
                         edge_device_id, logical_device_id)
SELECT 14, 'bench-node',
       '{"total_voltage":51.76,"current":2.6,"rsoc":7,"remaining_capacity":7.13,"temperatures":[28.1,26.5,27.8],"cell_voltages":[3.236,3.242,3.234,3.233,3.235,3.234,3.237,3.239,3.232,3.233,3.231,3.234,3.235,3.233,3.238,3.234],"fet_status":3,"protection_status":0,"pad":"0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"}',
       :'end_ts'::timestamptz - interval '1690000 seconds' + i * interval '0.845 seconds',
       :'end_ts'::timestamptz - interval '1690000 seconds' + i * interval '0.845 seconds',
       14, 14
FROM generate_series(0, 1999999) AS i;

COMMIT;

ANALYZE unified_data;
ANALYZE device_data;

SELECT '=== seed verification ===' AS info;
SELECT 'unified_data total' AS metric, count(*)::text AS value FROM unified_data
UNION ALL
SELECT 'unified_data 24h window', count(*)::text FROM unified_data
 WHERE "timestamp" BETWEEN :'start24'::timestamptz AND :'end_ts'::timestamptz
UNION ALL
SELECT 'protection_status 30d', count(*)::text FROM unified_data
 WHERE sensor_name = 'protection_status'
   AND "timestamp" BETWEEN :'start30d'::timestamptz AND :'end_ts'::timestamptz
UNION ALL
SELECT 'protection_status value=0', count(*)::text FROM unified_data
 WHERE sensor_name = 'protection_status' AND value = 0
UNION ALL
SELECT 'protection_status value=1', count(*)::text FROM unified_data
 WHERE sensor_name = 'protection_status' AND value = 1
UNION ALL
SELECT 'device_data total', count(*)::text FROM device_data;
