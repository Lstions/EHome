-- v22_seed_test_data.sql
-- 直接用 SQL 补全 seed 数据 (绕过 GORM AutoMigrate 死结)
-- 2026-06-04

DO $$
DECLARE
  v_node_id bigint;
  v_device_config_id bigint;
  v_channel_id bigint;
BEGIN
  -- 1. 取现有 node (nodes 表已有数据)
  SELECT id INTO v_node_id FROM nodes WHERE deleted_at IS NULL LIMIT 1;
  IF v_node_id IS NULL THEN
    INSERT INTO nodes (node_id, name, model, firmware_version, protocol_version, platform, status, config_version, config_status, mqtt_topic_up, mqtt_topic_down, config_epoch, config_sync_state, created_at, updated_at)
    VALUES ('10001', '测试节点-1号', 'ESP32-C6', '1.0.0', '2.2', 'esp32c6', 'offline', 'v1.0', 'synced', 'nodes/10001/up', 'nodes/10001/down', 1, 'synced', NOW(), NOW())
    RETURNING id INTO v_node_id;
  END IF;
  RAISE NOTICE 'node.id = %', v_node_id;

  -- 2. device_config
  SELECT id INTO v_device_config_id FROM device_configs WHERE deleted_at IS NULL LIMIT 1;
  IF v_device_config_id IS NULL THEN
    INSERT INTO device_configs (name, device_type, protocol, hardware_type, parser_id, connection, parser, init_flow, is_default, status, created_at, updated_at)
    VALUES (
      'BMP280-I2C-Seed配置',
      'bmp280',
      'i2c',
      'i2c',
      'bosch.bmp280',
      '{"protocol":"i2c","default_params":{"address":"0x76","bus":1}}'::jsonb,
      '{"id":"bosch.bmp280","options":{"oversampling":"x2"}}'::jsonb,
      '[{"step":1,"action":"write","data":"0xD0","description":"Read chip ID"}]'::jsonb,
      true, 'active', NOW(), NOW()
    )
    RETURNING id INTO v_device_config_id;
  END IF;
  RAISE NOTICE 'device_config.id = %', v_device_config_id;

  -- 3. channel (注意: channels 表没有 name/status 列, 有 bus_type/bus_config/enabled)
  SELECT id INTO v_channel_id FROM channels WHERE deleted_at IS NULL LIMIT 1;
  IF v_channel_id IS NULL THEN
    INSERT INTO channels (node_id, hardware_type, bus_type, bus_config, enabled, created_at, updated_at)
    VALUES (
      v_node_id, 'i2c', 'I2C',
      '{"bus":1,"sda":21,"scl":22,"frequency":400000}',
      true, NOW(), NOW()
    )
    RETURNING id INTO v_channel_id;
  END IF;
  RAISE NOTICE 'channel.id = %', v_channel_id;

  -- 4. edge_device (注意: edge_devices 没有 address 列, 有 type/enabled)
  IF NOT EXISTS (SELECT 1 FROM edge_devices WHERE deleted_at IS NULL) THEN
    INSERT INTO edge_devices (node_id, channel_id, device_config_id, name, type, status, enabled, created_at, updated_at)
    VALUES (
      v_node_id, v_channel_id, v_device_config_id,
      'BMP280-Seed', 'bmp280', 'active', true, NOW(), NOW()
    );
  END IF;

  RAISE NOTICE 'Seed data completed successfully';
END $$;
