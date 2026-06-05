-- v2.2 FK 修复 + config_templates 列重命名
-- 修复内容:
--   1. config_templates.collector_id → config_templates.node_id (列重命名 + FK)
--   2. 确认 channels.node_id FK 指向 nodes(id) (已修复, 此处幂等检查)
--
-- 版本: v2.2.1 (2026-06-04)
-- 幂等: 可重复执行

BEGIN;

-- ============================================
-- 1. config_templates: collector_id → node_id
-- ============================================

-- 1.1 重命名列
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'config_templates' AND column_name = 'collector_id'
  ) THEN
    ALTER TABLE config_templates RENAME COLUMN collector_id TO node_id;
    RAISE NOTICE 'Renamed config_templates.collector_id → config_templates.node_id';
  ELSE
    RAISE NOTICE 'config_templates.collector_id already renamed, skip';
  END IF;
END $$;

-- 1.2 重命名索引
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_indexes
    WHERE indexname = 'idx_config_templates_collector_id'
  ) THEN
    ALTER INDEX idx_config_templates_collector_id RENAME TO idx_config_templates_node_id;
    RAISE NOTICE 'Renamed index idx_config_templates_collector_id → idx_config_templates_node_id';
  END IF;
END $$;

-- 1.3 添加 FK: config_templates.node_id → nodes(id)
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.table_constraints
    WHERE constraint_name = 'fk_nodes_config_templates'
    AND table_name = 'config_templates'
  ) THEN
    ALTER TABLE config_templates
      ADD CONSTRAINT fk_nodes_config_templates
      FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE RESTRICT;
    RAISE NOTICE 'Added FK: config_templates.node_id → nodes.id';
  END IF;
END $$;

-- ============================================
-- 2. 幂等检查: channels.node_id FK 指向 nodes
-- ============================================

-- 2.1 如果 channels.node_id FK 指向 collectors (旧 bug), 修复为 nodes
DO $$
DECLARE
  fk_ref_table text;
BEGIN
  SELECT confrelid::regclass::text INTO fk_ref_table
  FROM pg_constraint c
  JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
  WHERE c.conname = 'fk_nodes_channels' AND c.contype = 'f';

  IF fk_ref_table = 'collectors' THEN
    -- 删除旧 FK, 添加新 FK
    ALTER TABLE channels DROP CONSTRAINT fk_nodes_channels;
    ALTER TABLE channels ADD CONSTRAINT fk_nodes_channels
      FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE RESTRICT;
    RAISE NOTICE 'Fixed channels.node_id FK: collectors → nodes';
  ELSIF fk_ref_table = 'nodes' THEN
    RAISE NOTICE 'channels.node_id FK already points to nodes, OK';
  ELSE
    RAISE NOTICE 'channels.node_id FK state: % (no action needed)', COALESCE(fk_ref_table, 'NOT FOUND');
  END IF;
END $$;

COMMIT;
