# v2.1 → v2.2 数据库迁移

## 概述

将 EHomeSystem 数据库从 v2.1 命名迁移到 v2.2 命名:

| v2.1 (旧) | v2.2 (新) | 说明 |
|-----------|----------|------|
| `collectors` 表 | `nodes` 表 | 采集器 → 节点 |
| `devices` 表 | `edge_devices` 表 | 设备 → 边缘设备 |
| `channels.collector_id` | `channels.node_id` | FK 列重命名 |
| - | `edge_devices.device_config_id` | 新增 FK 到 device_configs |

## 迁移特性

- ✅ **幂等性**: 可重复执行 (IF NOT EXISTS / ON CONFLICT)
- ✅ **零数据丢失**: 全量复制, 不删老表
- ✅ **兼容视图**: 老代码可继续读 `collectors` / `devices` 视图
- ✅ **dry-run 模式**: 预览 SQL, 不实际执行
- ✅ **回滚脚本**: 紧急回滚 (6 个月兼容窗口)

## 文件清单

```
scripts/
├── migrations/
│   ├── v22_migration.sql          # SQL 迁移脚本 (主脚本)
│   └── v22_rollback_to_v21.sql    # 回滚脚本
└── migrate_v21_to_v22/
    ├── main.go                    # Go 程序化迁移 (推荐)
    ├── verify.sql                 # 验证脚本
    └── README.md                  # 本文件
```

## 执行步骤

### 方式一: Go 程序化迁移 (推荐)

```bash
cd backend

# Step 1: 预览 SQL (dry-run)
go run scripts/migrate_v21_to_v22/main.go --dry-run

# Step 2: 执行迁移
go run scripts/migrate_v21_to_v22/main.go

# Step 3: 验证
psql -h localhost -U postgres -d ehome -f scripts/migrate_v21_to_v22/verify.sql
```

### 方式二: 纯 SQL 迁移

```bash
cd backend

# Step 1: 备份数据库 (重要!)
pg_dump -h localhost -U postgres ehome > backup_$(date +%Y%m%d_%H%M%S).sql

# Step 2: 执行迁移
psql -h localhost -U postgres -d ehome -f scripts/migrations/v22_migration.sql

# Step 3: 验证
psql -h localhost -U postgres -d ehome -f scripts/migrate_v21_to_v22/verify.sql
```

### 环境变量

Go 迁移工具支持以下环境变量:

| 变量 | 默认值 | 说明 |
|------|-------|------|
| `EHOME_DB_HOST` | `localhost` | 数据库主机 |
| `EHOME_DB_PORT` | `5432` | 数据库端口 |
| `EHOME_DB_USER` | `ehome` | 数据库用户 |
| `EHOME_DB_PASSWORD` | `ehome123` | 数据库密码 |
| `EHOME_DB_NAME` | `ehome` | 数据库名称 |
| `EHOME_DB_SSLMODE` | `disable` | SSL 模式 |

## 迁移后步骤

1. **验证数据完整性**: 运行 `verify.sql`
2. **补 device_config_id**: 如果有 `device_config_id=0` 的记录, 需人工补
3. **重启应用**: `systemctl restart ehome-server`
4. **监控日志**: `journalctl -u ehome-server -f`

## 回滚 (紧急)

```bash
# Step 1: 备份 v2.2 数据 (可选)
pg_dump -h localhost -U postgres -t nodes -t edge_devices ehome > v22_data_backup.sql

# Step 2: 执行回滚
psql -h localhost -U postgres -d ehome -f scripts/migrations/v22_rollback_to_v21.sql

# Step 3: 重启 v2.1 应用
systemctl restart ehome-server
```

## 兼容性说明

- **兼容视图** (`collectors`, `devices`, `device_templates`) 有效期 **6 个月**
- 6 个月后删除视图: `DROP VIEW collectors; DROP VIEW devices; DROP VIEW device_templates;`
- 老表 (`collectors`, `devices`) 不会被删除, 只是新建了 `nodes`, `edge_devices` 表

## 已知问题

1. **device_config_id 占位符**: 旧 `devices` 表没有 `device_config_id` 字段, 迁移时默认设为 0。脚本会尝试根据 `type` 字段猜测对应的 `device_config`, 但不一定准确。需要人工检查和修正。

2. **channels.collector_id 重命名**: 一旦 `collector_id` 被重命名为 `node_id`, 老代码中引用 `channels.collector_id` 的查询会报错。确保应用代码已同步更新。

3. **视图性能**: `collectors` 视图直接映射 `nodes` 表, 查询性能不受影响。`devices` 视图包含类型转换, 可能有微小开销。

4. **GORM AutoMigrate**: GORM AutoMigrate 只能添加新字段, 不能重命名字段或更改字段类型。字段重命名需要 SQL 脚本处理。

## 相关任务

- T-BE-DB-MIGRATE-01: 本任务 (DB 迁移)
- T-BE-RENAME-01: Go struct 改名 (并行执行, 不冲突)
- 命名迁移设计: `docs/设计/命名迁移设计.md` §3.2
