-- v2.1 → v2.2 迁移验证脚本
-- 
-- 使用方法:
--   psql -h localhost -U postgres -d ehome -f verify.sql
--
-- 预期结果:
--   - nodes count = collectors count (数据完整迁移, 如果老表存在)
--   - edge_devices count = devices count (如果老表存在)
--   - channels with node_id IS NULL = 0 (FK 完整)
--   - edge_devices with device_config_id = 0 应该接近 0
--
-- 版本: v2.2 (2026-06-04)
-- 作者: Phase 2A-2 DB Migration

\echo '========================================'
\echo 'v2.1 → v2.2 Migration Verification'
\echo '========================================'
\echo ''

-- ============================================
-- 1. 表行数对比
-- ============================================

\echo '1. Table Row Counts'
\echo '-------------------'

SELECT 
  'nodes (v2.2)' AS table_name,
  COUNT(*) AS total_rows,
  COUNT(*) FILTER (WHERE deleted_at IS NULL) AS active_rows
FROM nodes

UNION ALL

SELECT 
  'edge_devices (v2.2)' AS table_name,
  COUNT(*) AS total_rows,
  COUNT(*) FILTER (WHERE deleted_at IS NULL) AS active_rows
FROM edge_devices

ORDER BY table_name;

\echo ''

-- ============================================
-- 2. 数据完整性检查
-- ============================================

\echo '2. Data Integrity Checks'
\echo '------------------------'

-- Check if old tables exist and compare counts
DO $$
DECLARE
  collectors_count INTEGER;
  nodes_count INTEGER;
  devices_count INTEGER;
  edge_devices_count INTEGER;
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'collectors') THEN
    SELECT COUNT(*) FILTER (WHERE deleted_at IS NULL) INTO collectors_count FROM collectors;
    SELECT COUNT(*) FILTER (WHERE deleted_at IS NULL) INTO nodes_count FROM nodes;
    IF collectors_count = nodes_count THEN
      RAISE NOTICE 'collectors vs nodes: PASS (% = %)', collectors_count, nodes_count;
    ELSE
      RAISE WARNING 'collectors vs nodes: FAIL (collectors=%, nodes=%)', collectors_count, nodes_count;
    END IF;
  ELSE
    RAISE NOTICE 'collectors table does not exist, skip comparison';
  END IF;

  IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'devices') THEN
    SELECT COUNT(*) FILTER (WHERE deleted_at IS NULL) INTO devices_count FROM devices;
    SELECT COUNT(*) FILTER (WHERE deleted_at IS NULL) INTO edge_devices_count FROM edge_devices;
    IF devices_count = edge_devices_count THEN
      RAISE NOTICE 'devices vs edge_devices: PASS (% = %)', devices_count, edge_devices_count;
    ELSE
      RAISE WARNING 'devices vs edge_devices: FAIL (devices=%, edge_devices=%)', devices_count, edge_devices_count;
    END IF;
  ELSE
    RAISE NOTICE 'devices table does not exist, skip comparison';
  END IF;
END $$;

\echo ''

-- ============================================
-- 3. FK 完整性检查
-- ============================================

\echo '3. Foreign Key Integrity'
\echo '------------------------'

-- channels.node_id IS NULL 检查
SELECT 
  'channels with node_id IS NULL' AS check_name,
  CASE 
    WHEN COUNT(*) = 0 THEN '✅ PASS'
    ELSE '❌ FAIL'
  END AS status,
  COUNT(*) AS count
FROM channels 
WHERE node_id IS NULL;

-- edge_devices.node_id FK 违反检查
SELECT 
  'edge_devices with invalid node_id' AS check_name,
  CASE 
    WHEN COUNT(*) = 0 THEN '✅ PASS'
    ELSE '❌ FAIL'
  END AS status,
  COUNT(*) AS count
FROM edge_devices ed
LEFT JOIN nodes n ON n.id = ed.node_id
WHERE n.id IS NULL;

-- edge_devices.device_config_id FK 违反检查 (允许 0)
SELECT 
  'edge_devices with invalid device_config_id (excluding 0)' AS check_name,
  CASE 
    WHEN COUNT(*) = 0 THEN '✅ PASS'
    ELSE '❌ FAIL'
  END AS status,
  COUNT(*) AS count
FROM edge_devices ed
LEFT JOIN device_configs dc ON dc.id = ed.device_config_id
WHERE dc.id IS NULL
AND ed.device_config_id != 0;

\echo ''

-- ============================================
-- 4. device_config_id 占位符检查
-- ============================================

\echo '4. device_config_id Placeholder Check'
\echo '-------------------------------------'

SELECT 
  'edge_devices with device_config_id = 0' AS check_name,
  CASE 
    WHEN COUNT(*) = 0 THEN '✅ PASS (all configured)'
    WHEN COUNT(*) < 10 THEN '⚠️  WARNING (minor)'
    ELSE '❌ FAIL (needs manual fix)'
  END AS status,
  COUNT(*) AS count,
  ROUND(100.0 * COUNT(*) / NULLIF((SELECT COUNT(*) FROM edge_devices), 0), 2) AS percentage
FROM edge_devices 
WHERE device_config_id = 0;

\echo ''

-- ============================================
-- 5. 索引检查
-- ============================================

\echo '5. Index Checks'
\echo '---------------'

SELECT 
  indexname,
  tablename
FROM pg_indexes
WHERE tablename IN ('nodes', 'edge_devices', 'channels')
AND indexname LIKE 'idx_%'
ORDER BY tablename, indexname;

\echo ''

-- ============================================
-- 6. FK 约束检查
-- ============================================

\echo '6. FK Constraint Checks'
\echo '----------------------'

SELECT 
  tc.constraint_name,
  tc.table_name,
  kcu.column_name,
  ccu.table_name AS foreign_table_name,
  ccu.column_name AS foreign_column_name
FROM information_schema.table_constraints AS tc
JOIN information_schema.key_column_usage AS kcu
  ON tc.constraint_name = kcu.constraint_name
  AND tc.table_schema = kcu.table_schema
JOIN information_schema.constraint_column_usage AS ccu
  ON ccu.constraint_name = tc.constraint_name
  AND ccu.table_schema = tc.table_schema
WHERE tc.constraint_type = 'FOREIGN KEY'
  AND tc.table_name IN ('edge_devices', 'channels')
ORDER BY tc.table_name, tc.constraint_name;

\echo ''

-- ============================================
-- 7. 数据样本检查
-- ============================================

\echo '7. Data Sample (first 3 nodes)'
\echo '-----------------------------'

SELECT 
  id,
  node_id,
  name,
  status,
  mqtt_topic_format,
  created_at
FROM nodes
ORDER BY id
LIMIT 3;

\echo ''

\echo '8. Data Sample (first 3 edge_devices)'
\echo '------------------------------------'

SELECT 
  id,
  name,
  node_id,
  channel_id,
  device_config_id,
  status,
  created_at
FROM edge_devices
ORDER BY id
LIMIT 3;

\echo ''

-- ============================================
-- 总结
-- ============================================

\echo '========================================'
\echo 'Verification Complete'
\echo '========================================'
\echo ''
\echo 'Expected results:'
\echo '  ✅ nodes count = collectors count (if old table exists)'
\echo '  ✅ edge_devices count = devices count (if old table exists)'
\echo '  ✅ channels with node_id IS NULL = 0'
\echo '  ✅ edge_devices with invalid FK = 0'
\echo '  ⚠️  edge_devices with device_config_id = 0 should be minimal'
\echo '  ✅ All indexes created'
\echo '  ✅ All FK constraints added'
\echo ''
\echo 'If any checks fail, review the migration logs and fix manually.'
\echo ''
