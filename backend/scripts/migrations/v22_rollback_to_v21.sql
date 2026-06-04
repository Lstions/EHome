-- v2.2 → v2.1 回滚脚本 (紧急回滚)
-- 
-- ⚠️  警告: 此脚本会删除 v2.2 新表和视图
-- ⚠️  只在迁移后 6 个月内使用 (兼容窗口期)
-- 
-- 使用方法:
--   psql -h localhost -U postgres -d ehome -f v22_rollback_to_v21.sql
--
-- 前提条件:
--   - collectors / devices 老表仍然存在 (迁移脚本不删老表)
--   - 或者从备份恢复
--
-- 版本: v2.2 (2026-06-04)
-- 作者: Phase 2A-2 DB Migration

BEGIN;

-- ============================================
-- Step 1: 删除兼容视图
-- ============================================

DROP VIEW IF EXISTS collectors CASCADE;
DROP VIEW IF EXISTS devices CASCADE;
DROP VIEW IF EXISTS device_templates CASCADE;

RAISE NOTICE 'Dropped compatibility views';

-- ============================================
-- Step 2: 删除 FK 约束
-- ============================================

-- 2.1 删除 edge_devices FK
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.table_constraints
    WHERE constraint_name = 'fk_edge_devices_node' 
    AND table_name = 'edge_devices'
  ) THEN
    ALTER TABLE edge_devices DROP CONSTRAINT fk_edge_devices_node;
    RAISE NOTICE 'Dropped FK: fk_edge_devices_node';
  END IF;
END $$;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.table_constraints
    WHERE constraint_name = 'fk_edge_devices_device_config' 
    AND table_name = 'edge_devices'
  ) THEN
    ALTER TABLE edge_devices DROP CONSTRAINT fk_edge_devices_device_config;
    RAISE NOTICE 'Dropped FK: fk_edge_devices_device_config';
  END IF;
END $$;

-- 2.2 删除 channels FK
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.table_constraints
    WHERE constraint_name = 'fk_channels_node' 
    AND table_name = 'channels'
  ) THEN
    ALTER TABLE channels DROP CONSTRAINT fk_channels_node;
    RAISE NOTICE 'Dropped FK: fk_channels_node';
  END IF;
END $$;

-- ============================================
-- Step 3: 重命名 channels.node_id → channels.collector_id
-- ============================================

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns 
    WHERE table_name = 'channels' AND column_name = 'node_id'
  ) THEN
    ALTER TABLE channels RENAME COLUMN node_id TO collector_id;
    RAISE NOTICE 'Renamed channels.node_id → channels.collector_id';
  END IF;
END $$;

-- ============================================
-- Step 4: 删除 v2.2 索引
-- ============================================

DROP INDEX IF EXISTS idx_channels_node_id;
DROP INDEX IF EXISTS idx_edge_devices_node_id;
DROP INDEX IF EXISTS idx_edge_devices_channel_id;
DROP INDEX IF EXISTS idx_edge_devices_device_config_id;
DROP INDEX IF EXISTS idx_edge_devices_unique;

RAISE NOTICE 'Dropped v2.2 indexes';

-- ============================================
-- Step 5: 删除 v2.2 新表 (⚠️  数据丢失!)
-- ============================================

-- ⚠️  警告: 这会删除 nodes / edge_devices 表的所有数据
-- 如果需要保留数据, 请先备份:
--   pg_dump -t nodes -t edge_devices ehome > v22_data_backup.sql

DROP TABLE IF EXISTS edge_devices CASCADE;
DROP TABLE IF EXISTS nodes CASCADE;

RAISE NOTICE 'Dropped v2.2 tables (nodes, edge_devices)';

-- ============================================
-- Step 6: 验证老表存在
-- ============================================

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'collectors') THEN
    RAISE EXCEPTION 'collectors table does not exist! Cannot rollback without original table.';
  END IF;
  
  IF NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'devices') THEN
    RAISE EXCEPTION 'devices table does not exist! Cannot rollback without original table.';
  END IF;
  
  RAISE NOTICE 'Verified: collectors and devices tables exist';
END $$;

-- ============================================
-- Step 7: 数据完整性检查
-- ============================================

DO $$
DECLARE
  collectors_count INTEGER;
  devices_count INTEGER;
BEGIN
  SELECT COUNT(*) INTO collectors_count FROM collectors WHERE deleted_at IS NULL;
  SELECT COUNT(*) INTO devices_count FROM devices WHERE deleted_at IS NULL;
  
  RAISE NOTICE 'Rollback complete: collectors=%, devices=%', collectors_count, devices_count;
END $$;

COMMIT;

-- ============================================
-- 回滚完成提示
-- ============================================
-- 
-- ✅ 回滚成功! 下一步:
-- 
-- 1. 重启应用 (使用 v2.1 代码):
--    systemctl restart ehome-server
-- 
-- 2. 监控日志:
--    journalctl -u ehome-server -f
-- 
-- 3. 如果需要恢复 v2.2 数据:
--    psql -f v22_data_backup.sql
-- 
-- ============================================
