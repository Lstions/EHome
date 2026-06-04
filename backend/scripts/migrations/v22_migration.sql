-- v2.1 → v2.2 数据库迁移脚本
-- 执行顺序: 1) 创建新表 2) 数据迁移 3) 列重命名 4) 加 FK 约束 5) 创建兼容视图
-- 
-- 使用方法:
--   psql -h localhost -U postgres -d ehome -f v22_migration.sql
--   或: \i v22_migration.sql (在 pql 内)
--
-- 安全特性:
--   - 幂等性: 可重复执行 (IF NOT EXISTS / ON CONFLICT)
--   - 零数据丢失: 全量复制, 不删老表
--   - 兼容视图: 老代码可继续读 collectors / devices 视图
--
-- 版本: v2.2 (2026-06-04)
-- 作者: Phase 2A-2 DB Migration

BEGIN;

-- ============================================
-- Step 1: 创建新表 (如果不存在)
-- ============================================

-- 1.1 nodes 表 (从 collectors 迁移)
CREATE TABLE IF NOT EXISTS nodes (
  id                    SERIAL PRIMARY KEY,
  node_id               VARCHAR(32) UNIQUE NOT NULL,
  name                  VARCHAR(64) NOT NULL,
  model                 VARCHAR(20),
  firmware_version      VARCHAR(32),
  protocol_version      VARCHAR(16) DEFAULT '2.2',
  platform              VARCHAR(16),
  status                VARCHAR(20) DEFAULT 'offline',
  last_seen             TIMESTAMPTZ,
  last_ping_at          TIMESTAMPTZ,
  uptime_seconds        INTEGER DEFAULT 0,
  ping_latency_ms       INTEGER DEFAULT 0,
  mqtt_topic_up         VARCHAR(128),
  mqtt_topic_down       VARCHAR(128),
  wifi_ssid             VARCHAR(64),
  wifi_rssi             INTEGER,
  free_heap_bytes       INTEGER,
  capabilities          JSONB,
  hardware_info         JSONB,
  -- v2.1 同步机制字段 (v2.1 已加, v2.2 保留)
  config_epoch          BIGINT DEFAULT 0,
  last_manifest_id      VARCHAR(64),
  config_sync_state     VARCHAR(20) DEFAULT 'unknown',
  last_sync_at          TIMESTAMPTZ,
  last_sync_id          VARCHAR(64),
  -- v2.2 新增字段
  mqtt_topic_format     VARCHAR(16) DEFAULT 'v2',  -- 'v1' (devices/{id}) 或 'v2' (nodes/{id})
  created_at            TIMESTAMPTZ DEFAULT NOW(),
  updated_at            TIMESTAMPTZ DEFAULT NOW(),
  deleted_at            TIMESTAMPTZ
);

-- 1.2 edge_devices 表 (从 devices 迁移)
CREATE TABLE IF NOT EXISTS edge_devices (
  id                  SERIAL PRIMARY KEY,
  name                VARCHAR(64) NOT NULL,
  -- v2.2 新增/重命名字段
  node_id             INTEGER NOT NULL,        -- 显式 FK
  channel_id          INTEGER NOT NULL,        -- 改名 (原 channel_id, 不变)
  device_config_id    INTEGER NOT NULL,        -- v2.2 关键新增 FK
  hardware_id         INTEGER NOT NULL DEFAULT 0,
  interval_ms         INTEGER NOT NULL DEFAULT 5000,
  enabled             BOOLEAN NOT NULL DEFAULT true,
  status              VARCHAR(20) NOT NULL DEFAULT 'active',
  error_code          INTEGER NOT NULL DEFAULT 0,
  last_data_at        TIMESTAMPTZ,
  last_error          VARCHAR(256),
  config_version      VARCHAR(64),
  init_state          VARCHAR(20) NOT NULL DEFAULT 'pending',
  init_last_step      INTEGER NOT NULL DEFAULT 0,
  init_total_steps    INTEGER NOT NULL DEFAULT 0,
  -- 软删除
  created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  deleted_at          TIMESTAMPTZ
);

-- 1.3 edge_devices 唯一约束 (v2.2: node + channel + hardware_id)
CREATE UNIQUE INDEX IF NOT EXISTS idx_edge_devices_unique 
  ON edge_devices(node_id, channel_id, hardware_id) 
  WHERE deleted_at IS NULL;

-- ============================================
-- Step 2: 数据迁移 (从老表)
-- ============================================

-- 2.1 迁移 collectors → nodes (只迁未删除的)
-- 注意: collectors 表可能不存在 (新部署), 用 DO 块保护
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'collectors') THEN
    INSERT INTO nodes (
      id, node_id, name, model, firmware_version, protocol_version,
      platform, status, last_seen, last_ping_at, uptime_seconds,
      ping_latency_ms, mqtt_topic_up, mqtt_topic_down,
      wifi_ssid, wifi_rssi, free_heap_bytes, capabilities, hardware_info,
      config_epoch, last_manifest_id, config_sync_state, last_sync_at, last_sync_id,
      mqtt_topic_format, created_at, updated_at, deleted_at
    )
    SELECT
      id, 
      device_id,  -- collectors.device_id → nodes.node_id
      COALESCE(name, 'node_' || device_id),  -- collectors 可能没有 name 字段
      model, 
      firmware_version, 
      COALESCE(protocol_version, '2.1'),  -- 老节点默认 v2.1
      platform, 
      status, 
      last_seen, 
      last_ping_at, 
      uptime_seconds,
      ping_latency_ms, 
      mqtt_topic_up, 
      mqtt_topic_down,
      wifi_ssid, 
      wifi_rssi, 
      free_heap_bytes, 
      capabilities, 
      hardware_info,
      COALESCE(config_epoch, 0), 
      last_manifest_id, 
      config_sync_state, 
      last_sync_at, 
      last_sync_id,
      'v1',  -- v2.2 新增: 标记老节点用 v1 topic
      created_at, 
      updated_at,
      deleted_at
    FROM collectors
    ON CONFLICT (node_id) DO NOTHING;
    
    RAISE NOTICE 'Migrated % rows from collectors to nodes', (SELECT COUNT(*) FROM nodes);
  ELSE
    RAISE NOTICE 'collectors table does not exist, skip migration';
  END IF;
END $$;

-- 2.2 同步 sequence (PostgreSQL)
SELECT setval(pg_get_serial_sequence('nodes', 'id'), 
              COALESCE((SELECT MAX(id) FROM nodes), 1));

-- 2.3 迁移 devices → edge_devices
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'devices') THEN
    -- 先检查 devices 表结构
    IF EXISTS (
      SELECT 1 FROM information_schema.columns 
      WHERE table_name = 'devices' AND column_name = 'channel_id'
    ) THEN
      INSERT INTO edge_devices (
        id, name, node_id, channel_id, device_config_id, hardware_id,
        interval_ms, enabled, status, error_code, last_data_at, last_error,
        config_version, init_state, init_last_step, init_total_steps,
        created_at, updated_at, deleted_at
      )
      SELECT
        d.id, 
        d.name, 
        COALESCE(c.collector_id, 1),  -- node_id: 从 channels.collector_id 获取
        d.channel_id, 
        0,  -- device_config_id 默认 0, 需人工补
        0,  -- hardware_id 默认 0
        COALESCE(d.interval_ms, 5000), 
        COALESCE(d.enabled, true), 
        COALESCE(d.status, 'active'), 
        0, 
        NULL, 
        '',
        '', 
        'pending', 
        0, 
        0,
        d.created_at, 
        d.updated_at,
        d.deleted_at
      FROM devices d
      LEFT JOIN channels c ON c.id = d.channel_id
      ON CONFLICT (id) DO NOTHING;
      
      RAISE NOTICE 'Migrated % rows from devices to edge_devices', (SELECT COUNT(*) FROM edge_devices);
    ELSE
      RAISE NOTICE 'devices table missing channel_id column, skip migration';
    END IF;
  ELSE
    RAISE NOTICE 'devices table does not exist, skip migration';
  END IF;
END $$;

SELECT setval(pg_get_serial_sequence('edge_devices', 'id'),
              COALESCE((SELECT MAX(id) FROM edge_devices), 1));

-- 2.4 ⚠️ 警告: device_config_id = 0 是占位符, 需要人工补!
-- 旧 devices 表没有 device_config_id 字段, 只能根据 Type 字段猜
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'devices') THEN
    IF EXISTS (
      SELECT 1 FROM information_schema.columns 
      WHERE table_name = 'devices' AND column_name = 'type'
    ) THEN
      UPDATE edge_devices ed
      SET device_config_id = (
        SELECT dc.id FROM device_configs dc
        WHERE dc.device_type = (
          SELECT d.type FROM devices d WHERE d.id = ed.id
        )
        AND dc.is_default = true
        LIMIT 1
      )
      WHERE device_config_id = 0
      AND EXISTS (
        SELECT 1 FROM devices d WHERE d.id = ed.id AND d.type IS NOT NULL
      );
      
      RAISE NOTICE 'Updated device_config_id for % edge_devices', 
        (SELECT COUNT(*) FROM edge_devices WHERE device_config_id > 0);
    END IF;
  END IF;
END $$;

-- ============================================
-- Step 3: 列重命名 (channels 表)
-- ============================================

-- 3.1 channels.collector_id → channels.node_id
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns 
    WHERE table_name = 'channels' AND column_name = 'collector_id'
  ) THEN
    ALTER TABLE channels RENAME COLUMN collector_id TO node_id;
    RAISE NOTICE 'Renamed channels.collector_id → channels.node_id';
  ELSE
    RAISE NOTICE 'channels.collector_id does not exist, skip rename';
  END IF;
END $$;

-- 3.2 加索引 (如果不存在)
CREATE INDEX IF NOT EXISTS idx_channels_node_id ON channels(node_id);
CREATE INDEX IF NOT EXISTS idx_edge_devices_node_id ON edge_devices(node_id);
CREATE INDEX IF NOT EXISTS idx_edge_devices_channel_id ON edge_devices(channel_id);
CREATE INDEX IF NOT EXISTS idx_edge_devices_device_config_id ON edge_devices(device_config_id);

-- ============================================
-- Step 4: 加 FK 约束 (v2.2 强化)
-- ============================================

-- 4.1 edge_devices.node_id → nodes.id (RESTRICT)
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.table_constraints
    WHERE constraint_name = 'fk_edge_devices_node' 
    AND table_name = 'edge_devices'
  ) THEN
    ALTER TABLE edge_devices
      ADD CONSTRAINT fk_edge_devices_node
      FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE RESTRICT;
    RAISE NOTICE 'Added FK: edge_devices.node_id → nodes.id';
  END IF;
END $$;

-- 4.2 edge_devices.device_config_id → device_configs.id (RESTRICT, 阻止删配置)
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.table_constraints
    WHERE constraint_name = 'fk_edge_devices_device_config' 
    AND table_name = 'edge_devices'
  ) THEN
    -- 允许 device_config_id = 0 (占位, 后续补)
    -- 所以 FK 加 NOT VALID, 现有数据可以违反
    ALTER TABLE edge_devices
      ADD CONSTRAINT fk_edge_devices_device_config
      FOREIGN KEY (device_config_id) REFERENCES device_configs(id) ON DELETE RESTRICT NOT VALID;
    RAISE NOTICE 'Added FK (NOT VALID): edge_devices.device_config_id → device_configs.id';
  END IF;
END $$;

-- 4.3 channels.node_id → nodes.id (如果 channels 表存在)
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'channels') THEN
    IF NOT EXISTS (
      SELECT 1 FROM information_schema.table_constraints
      WHERE constraint_name = 'fk_channels_node' 
      AND table_name = 'channels'
    ) THEN
      ALTER TABLE channels
        ADD CONSTRAINT fk_channels_node
        FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE RESTRICT;
      RAISE NOTICE 'Added FK: channels.node_id → nodes.id';
    END IF;
  END IF;
END $$;

-- ============================================
-- Step 5: 创建 v2.1 兼容视图 (6 个月后删除)
-- ============================================

-- 5.1 collectors 视图 (老代码读)
CREATE OR REPLACE VIEW collectors AS
  SELECT 
    id, 
    node_id AS device_id,  -- 兼容: node_id 映射回 device_id
    name,
    model, 
    firmware_version, 
    protocol_version,
    platform, 
    status, 
    last_seen, 
    last_ping_at, 
    uptime_seconds,
    ping_latency_ms, 
    mqtt_topic_up, 
    mqtt_topic_down,
    wifi_ssid, 
    wifi_rssi, 
    free_heap_bytes, 
    capabilities, 
    hardware_info,
    config_epoch, 
    last_manifest_id, 
    config_sync_state, 
    last_sync_at, 
    last_sync_id,
    created_at, 
    updated_at,
    deleted_at
  FROM nodes;

-- 5.2 devices 视图 (老代码读)
CREATE OR REPLACE VIEW devices AS
  SELECT
    id, 
    name, 
    channel_id, 
    interval_ms, 
    enabled, 
    status,
    created_at, 
    updated_at, 
    deleted_at,
    -- 兼容性: 推导 Type 和 ParserID (从 device_config)
    -- 注意: 这些字段在 v2.2 已移到 device_configs 表
    ''::varchar AS type,
    ''::varchar AS parser_id
  FROM edge_devices;

-- 5.3 device_templates 视图 (如果需要)
CREATE OR REPLACE VIEW device_templates AS
  SELECT * FROM device_configs;

-- ============================================
-- Step 6: 更新序列 (确保新插入不冲突)
-- ============================================

-- 确保 nodes.id 序列正确
SELECT setval(pg_get_serial_sequence('nodes', 'id'), 
              COALESCE((SELECT MAX(id) FROM nodes), 0) + 1, false);

-- 确保 edge_devices.id 序列正确
SELECT setval(pg_get_serial_sequence('edge_devices', 'id'), 
              COALESCE((SELECT MAX(id) FROM edge_devices), 0) + 1, false);

-- ============================================
-- Step 7: 数据完整性检查 (警告, 不阻止提交)
-- ============================================

DO $$
DECLARE
  collectors_count INTEGER;
  nodes_count INTEGER;
  devices_count INTEGER;
  edge_devices_count INTEGER;
  missing_config_count INTEGER;
BEGIN
  -- 检查数据迁移完整性
  IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'collectors') THEN
    SELECT COUNT(*) INTO collectors_count FROM collectors WHERE deleted_at IS NULL;
    SELECT COUNT(*) INTO nodes_count FROM nodes WHERE deleted_at IS NULL;
    
    IF collectors_count > 0 AND nodes_count < collectors_count THEN
      RAISE WARNING 'Data migration incomplete: collectors=%, nodes=%', collectors_count, nodes_count;
    ELSIF collectors_count > 0 THEN
      RAISE NOTICE 'Data migration OK: collectors=%, nodes=%', collectors_count, nodes_count;
    END IF;
  END IF;
  
  IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'devices') THEN
    SELECT COUNT(*) INTO devices_count FROM devices WHERE deleted_at IS NULL;
    SELECT COUNT(*) INTO edge_devices_count FROM edge_devices WHERE deleted_at IS NULL;
    
    IF devices_count > 0 AND edge_devices_count < devices_count THEN
      RAISE WARNING 'Data migration incomplete: devices=%, edge_devices=%', devices_count, edge_devices_count;
    ELSIF devices_count > 0 THEN
      RAISE NOTICE 'Data migration OK: devices=%, edge_devices=%', devices_count, edge_devices_count;
    END IF;
  END IF;
  
  -- 检查 device_config_id 占位符
  SELECT COUNT(*) INTO missing_config_count 
  FROM edge_devices 
  WHERE device_config_id = 0 OR device_config_id IS NULL;
  
  IF missing_config_count > 0 THEN
    RAISE WARNING '% edge_devices have device_config_id=0 (need manual fix)', missing_config_count;
  END IF;
END $$;

COMMIT;

-- ============================================
-- 迁移完成提示
-- ============================================
-- 
-- ✅ 迁移成功! 下一步:
-- 
-- 1. 验证数据完整性:
--    psql -f verify.sql
-- 
-- 2. 如果 device_config_id=0 的记录较多, 需要人工补:
--    UPDATE edge_devices SET device_config_id = <正确的ID> WHERE id = <...>;
-- 
-- 3. 重启应用:
--    systemctl restart ehome-server
-- 
-- 4. 监控日志:
--    journalctl -u ehome-server -f
-- 
-- 5. 6 个月后删除兼容视图:
--    DROP VIEW collectors;
--    DROP VIEW devices;
--    DROP VIEW device_templates;
-- 
-- 回滚 (如果需要):
--    psql -f v22_rollback_to_v21.sql
-- 
-- ============================================
