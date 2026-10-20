-- bench 库鉴权/设备元数据（本地基准专用）。
-- 与生产对齐的 id：edge_devices.id=14（jiabaida_bms）、logical_devices.id=14。
-- 为什么要写这些行：/api/v1 全部路由挂 JWTAuthWithDB，会逐请求校验
-- auth_states.state='initialized' + users.subject_key='system_admin' +
-- users.session_version == JWT.sv；空库直接 mint JWT 必 401。
BEGIN;

INSERT INTO auth_states (key, state, security_version, initialized_at, created_at, updated_at)
VALUES ('system_auth', 'initialized', 1, now(), now(), now())
ON CONFLICT (key) DO UPDATE
  SET state = 'initialized', initialized_at = now(), updated_at = now();

INSERT INTO users (id, username, password_hash, role, enabled, subject_key,
                   session_version, initialized_at, created_at, updated_at)
VALUES (1, 'bench-admin', 'x', 'admin', true, 'system_admin', 2, now(), now(), now())
ON CONFLICT (id) DO UPDATE
  SET subject_key = 'system_admin', enabled = true, session_version = 2,
      retired_at = NULL, updated_at = now();
SELECT setval('users_id_seq', GREATEST((SELECT COALESCE(MAX(id), 0) FROM users), 1));

INSERT INTO logical_devices (id, identity_key, name, device_type, retention_days,
                             merge_status, purge_requested, created_at, updated_at)
VALUES (14, 'bench-bms-14', 'BMS-bench', 'jiabaida_bms', 90, NULL, false, now(), now())
ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name;
SELECT setval('logical_devices_id_seq', GREATEST((SELECT COALESCE(MAX(id), 0) FROM logical_devices), 1));

INSERT INTO edge_devices (id, name, type, node_id, channel_id, device_config_id,
                          interval_ms, enabled, status, logical_device_id, init_state,
                          config_version, command_intervals, created_at, updated_at)
VALUES (14, 'BMS-bench', 'jiabaida_bms', 'bench-node', 1, 0,
        5000, true, 'active', 14, 'done',
        '', '{"read_comprehensive": 1000}'::jsonb, now(), now())
ON CONFLICT (id) DO UPDATE
  SET logical_device_id = 14, enabled = true, interval_ms = 5000, updated_at = now();
SELECT setval('edge_devices_id_seq', GREATEST((SELECT COALESCE(MAX(id), 0) FROM edge_devices), 1));

COMMIT;

SELECT '=== bench metadata ===' AS info;
SELECT id, name, type, logical_device_id, interval_ms FROM edge_devices WHERE id = 14;
SELECT id, name, device_type, retention_days FROM logical_devices WHERE id = 14;
SELECT id, subject_key, session_version, enabled FROM users WHERE id = 1;
SELECT key, state, security_version FROM auth_states;
