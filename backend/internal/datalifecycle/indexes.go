package datalifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"
)

// 复合索引 (方案 §1.1 CRITICAL, v1-H2 闭环): 逻辑身份维度时序查询走
// (logical_device_id, timestamp DESC)。索引名与方案一致。
//
// 后两个是 BMS 页慢修复 P0-2 追加的索引 (BMS页慢修复-实施契约-2026-10-20 §1.4):
//   - idx_unified_logical_sensor_ts: 图表历史查询恒按
//     (logical_device_id, sensor_name, timestamp) 过滤, 旧索引只有前两列之一,
//     PG 只能扫整个时间窗再过滤 (单测点 24h 扫 2,808,951 行只返回 86,251 行)。
//   - idx_devdata_logical_created: latest-data 的
//     ORDER BY created_at DESC LIMIT 1 走索引, 不再并行 seq scan 214 万行。
//
// 最后两个是 §1.6 UNION ALL 重写配套的**部分索引** (2026-10-20 23:09 Lead 补充):
// UNION ALL 的 B 分支是 `logical_device_id IS NULL AND device_id IN (...)`
// (backfill 前的旧行), 若没有对应索引, 22.5M 行上它仍会退化为全窗口
// timestamp/device_id 扫描, 把 A 分支省下的成本吃回去。生产 NULL-logical 行
// 应为 0/极少, 因此用 WHERE 部分索引: 体积与建索引时间可忽略, 且 B 分支的
// device_id / sensor_name / timestamp 全部成为索引条件。
const (
	IndexUnifiedLogicalTS = "idx_unified_logical_ts"
	IndexDevDataLogicalTS = "idx_devdata_logical_ts"

	IndexUnifiedLogicalSensorTS = "idx_unified_logical_sensor_ts"
	IndexDevDataLogicalCreated  = "idx_devdata_logical_created"

	IndexUnifiedLegacyNullTS    = "idx_unified_legacy_null_ts"
	IndexDevDataLegacyNullCreat = "idx_devdata_legacy_null_created"
)

// logicalIndexSpecs maps index name → CREATE INDEX body (without the
// CREATE INDEX <name> prefix, which differs per dialect).
//
// PGDDL 是 PostgreSQL 专用覆盖: 覆盖索引用 INCLUDE ("value") 让 avg/change 的
// 查询变成 Index Only Scan (不回落 86,400 次随机 heap fetch, 代价模型才肯选它);
// SQLite 不支持 INCLUDE 语法, 因此 SQLite 用 DDL (普通复合索引)。
var logicalIndexSpecs = []struct {
	Name  string
	Table string
	DDL   string // column list (SQLite / 通用)
	PGDDL string // PostgreSQL override (INCLUDE 等); empty = use DDL
}{
	{IndexUnifiedLogicalTS, "unified_data", "(logical_device_id, timestamp DESC)", ""},
	{IndexDevDataLogicalTS, "device_data", "(logical_device_id, timestamp DESC)", ""},
	// BMS 页慢修复 P0-2 (契约 §1.4)：图表历史查询覆盖索引。
	// INCLUDE ("value"): 实测 bench 上只有 covering 索引才会被选中 ——
	// Index Only Scan 执行 67ms / Buffers 622 / Heap Fetches 0,
	// 而普通三列索引因 86,400 次随机 heap fetch 仍被 planner 放弃。
	{IndexUnifiedLogicalSensorTS, "unified_data",
		"(logical_device_id, sensor_name, timestamp)",
		`(logical_device_id, sensor_name, "timestamp") INCLUDE ("value")`},
	{IndexDevDataLogicalCreated, "device_data", "(logical_device_id, created_at DESC)", ""},
	// BMS 页慢修复 §1.6: UNION ALL B 分支 (NULL-logical 旧行) 的部分索引。
	// PG 侧同样 INCLUDE ("value") 走 Index Only Scan; SQLite 只建普通部分索引
	// (DDL 尾部直接带 WHERE, CREATE INDEX ... (cols) WHERE ...)。
	{IndexUnifiedLegacyNullTS, "unified_data",
		"(device_id, sensor_name, timestamp) WHERE logical_device_id IS NULL",
		`(device_id, sensor_name, "timestamp") INCLUDE ("value") WHERE logical_device_id IS NULL`},
	{IndexDevDataLegacyNullCreat, "device_data",
		"(device_id, created_at DESC) WHERE logical_device_id IS NULL", ""},
}

// EnsureLogicalDataIndexes creates the §1.1 composite indexes idempotently.
//
// PostgreSQL: CREATE INDEX CONCURRENTLY IF NOT EXISTS — 不阻塞在线写入。
// CONCURRENTLY 不能在事务块内执行, 故用独立连接且不走 db.Transaction;
// 若上次 CONCURRENTLY 构建中途失败留下 INVALID 索引 (IF NOT EXISTS 会
// 静默跳过它), 先 DROP 再重建。
//
// SQLite: 普通 CREATE INDEX IF NOT EXISTS (无 CONCURRENTLY 概念)。
//
// # 生产升级注意 (BMS 页慢修复-实施契约-2026-10-20 §1.4)
//
// 分区母表分支用**非并发** CREATE INDEX —— PostgreSQL 禁止对分区母表执行
// CREATE INDEX CONCURRENTLY。unified_data 生产约 4.4 GB / 2400 万行, 因此
// 升级后**首次**建 idx_unified_logical_sensor_ts / idx_devdata_logical_created
// 会持有 SHARE 锁, 期间该表写入阻塞 (可能数十秒到数分钟)。部署流程必须走
// 维护窗口: 停旧容器 → `ehomectl datalifecycle ensure-indexes` 建索引 →
// 起新容器。索引建成后 IF NOT EXISTS 幂等跳过, 后续启动是 O(1) 检查;
// 之后由 PG 新建的分区会自动继承父索引, 不需要再全量建索引。
//
// 入口说明: 显式运维入口是 `ehomectl datalifecycle ensure-indexes`;
// 服务端启动时 cmd/server 的 datalifecycle.NewBackfiller(db).Start() 也会在
// 后台 goroutine 内调用本函数 (backfill.go:97, 非阻塞启动), 两条路径都幂等。
func EnsureLogicalDataIndexes(ctx context.Context, db *gorm.DB) error {
	if db.Dialector != nil && db.Dialector.Name() == "postgres" {
		return ensurePostgresIndexes(ctx, db)
	}
	return ensureSQLiteIndexes(ctx, db)
}

func ensureSQLiteIndexes(ctx context.Context, db *gorm.DB) error {
	for _, spec := range logicalIndexSpecs {
		stmt := fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s %s",
			spec.Name, spec.Table, spec.DDL)
		if err := db.WithContext(ctx).Exec(stmt).Error; err != nil {
			return fmt.Errorf("create index %s: %w", spec.Name, err)
		}
		slog.Info("datalifecycle: index ensured", "index", spec.Name, "table", spec.Table)
	}
	return nil
}

func ensurePostgresIndexes(ctx context.Context, db *gorm.DB) error {
	for _, spec := range logicalIndexSpecs {
		// 分区母表禁止 CONCURRENTLY (PG 0A000): 改用普通 CREATE INDEX, PG
		// 自动把索引传播到各分区; 普通表仍走 CONCURRENTLY 不阻塞在线写入。
		partitioned := IsTablePartitioned(db, spec.Table)
		concurrently := !partitioned
		// INVALID 索引检测 (两种分支都做): 构建中途失败会留下 indisvalid=false
		// 的残骸, 而 CREATE INDEX IF NOT EXISTS 对其静默生效 → 索引实际缺失却
		// 永远补不上。评审 2026-10-21 指出：原实现只在非分区(CONCURRENTLY)分支
		// 做这件事，而生产要建的两个新索引恰好都在**分区母表**上（非并发建，
		// 失败概率更高），漏检会静默少索引。分区母表索引被 DROP 时子索引级联删除。
		if err := dropInvalidIndexIfAny(ctx, db, spec.Name); err != nil {
			return err
		}
		if !concurrently {
			// 分区母表分支: IF NOT EXISTS 以 schema 全局关系名为准 (非按表)。
			// 分区迁移把原平表保留为 <table>_legacy, 其上的同名复合索引仍占用
			// 规范名, 不移走则 CREATE INDEX IF NOT EXISTS 被静默跳过, 分区母表
			// 永久缺失该复合索引。先释放规范名 (幂等)。
			if err := releaseIndexNameFromNonTarget(ctx, db, spec.Name, spec.Table); err != nil {
				return err
			}
		}

		ddl := spec.DDL // SQLite 不支持 INCLUDE; PG 用 PGDDL 覆盖 (covering 索引)
		if spec.PGDDL != "" {
			ddl = spec.PGDDL
		}
		stmt := fmt.Sprintf("CREATE INDEX %sIF NOT EXISTS %s ON %s %s",
			map[bool]string{true: "CONCURRENTLY ", false: ""}[concurrently],
			spec.Name, spec.Table, ddl)
		// 独立连接 + 无事务: CONCURRENTLY 不能在事务块内执行。GORM 顶层
		// Exec 不开事务; 无参数时 pgx 走 simple protocol, 不会被隐式事务
		// 包裹。
		if err := db.WithContext(ctx).Exec(stmt).Error; err != nil {
			return fmt.Errorf("create index %s: %w", spec.Name, err)
		}
		slog.Info("datalifecycle: index ensured",
			"index", spec.Name, "table", spec.Table, "concurrently", concurrently)
	}
	return nil
}

// dropInvalidIndexIfAny drops an INVALID (indisvalid=false) index left behind
// by an interrupted build, so the following CREATE INDEX IF NOT EXISTS actually
// runs. Idempotent: absent/valid index → no-op.
func dropInvalidIndexIfAny(ctx context.Context, db *gorm.DB, indexName string) error {
	var invalid bool
	err := db.WithContext(ctx).Raw(
		`SELECT COUNT(*) > 0 FROM pg_class c
JOIN pg_index i ON i.indexrelid = c.oid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relname = ? AND n.nspname = current_schema() AND NOT i.indisvalid`, indexName).
		Scan(&invalid).Error
	if err != nil {
		return fmt.Errorf("check invalid index %s: %w", indexName, err)
	}
	if !invalid {
		return nil
	}
	if err := db.WithContext(ctx).
		Exec(fmt.Sprintf("DROP INDEX IF EXISTS %s", indexName)).Error; err != nil {
		return fmt.Errorf("drop invalid index %s: %w", indexName, err)
	}
	slog.Warn("datalifecycle: dropped INVALID index before rebuild", "index", indexName)
	return nil
}

// releaseIndexNameFromNonTarget frees indexName for target before a
// non-CONCURRENTLY CREATE INDEX IF NOT EXISTS on a partitioned parent.
//
// PG's IF NOT EXISTS is keyed on the schema-global relation name, so a
// same-named index retained by a non-target relation (the <table>_legacy
// snapshot kept by the partition migration) turns the CREATE into a silent
// no-op. When the name is held by another relation we rename it to
// <index>_legacy; if that name is already taken (previous interrupted run)
// the stale index is dropped instead. Both paths are idempotent.
func releaseIndexNameFromNonTarget(ctx context.Context, db *gorm.DB, indexName, target string) error {
	var owner string
	if err := db.WithContext(ctx).Raw(
		`SELECT t.relname FROM pg_class i
JOIN pg_index x ON x.indexrelid = i.oid
JOIN pg_class t ON t.oid = x.indrelid
JOIN pg_namespace n ON n.oid = i.relnamespace
WHERE i.relname = ? AND n.nspname = current_schema()`, indexName).
		Scan(&owner).Error; err != nil {
		return fmt.Errorf("resolve owner of index %s: %w", indexName, err)
	}
	if owner == "" || owner == target {
		return nil
	}
	legacyName := indexName + "_legacy"
	var legacyExists int64
	if err := db.WithContext(ctx).Raw(tableExistsSQL, legacyName, "i").Scan(&legacyExists).Error; err != nil {
		return fmt.Errorf("check index %s: %w", legacyName, err)
	}
	if legacyExists > 0 {
		if err := db.WithContext(ctx).Exec(fmt.Sprintf("DROP INDEX IF EXISTS %s", indexName)).Error; err != nil {
			return fmt.Errorf("drop stale index %s: %w", indexName, err)
		}
		slog.Warn("datalifecycle: dropped stale duplicate index off target table",
			"index", indexName, "owner", owner)
		return nil
	}
	if err := db.WithContext(ctx).Exec(fmt.Sprintf("ALTER INDEX %s RENAME TO %s", indexName, legacyName)).Error; err != nil {
		return fmt.Errorf("rename stale index %s: %w", indexName, err)
	}
	slog.Warn("datalifecycle: renamed stale index off target table",
		"index", indexName, "owner", owner, "renamed_to", legacyName)
	return nil
}

// IndexExists reports whether an index exists and (on PostgreSQL) is valid.
// 测试与运维自检用。
func IndexExists(ctx context.Context, db *gorm.DB, name string) (bool, error) {
	if db.Dialector != nil && db.Dialector.Name() == "postgres" {
		var count int64
		err := db.WithContext(ctx).Raw(
			`SELECT COUNT(*) FROM pg_class c
JOIN pg_index i ON i.indexrelid = c.oid
WHERE c.relname = ? AND i.indisvalid`, name).
			Scan(&count).Error
		return count > 0, err
	}
	var count int64
	err := db.WithContext(ctx).Raw(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).
		Scan(&count).Error
	return count > 0, err
}

// RenderIndexDDL returns the CREATE INDEX statement for a dialect and index
// name — 供测试断言双方言语句形态 (PG 含 CONCURRENTLY, SQLite 不含)。
func RenderIndexDDL(dialect, name string) (string, error) {
	for _, spec := range logicalIndexSpecs {
		if spec.Name != name {
			continue
		}
		switch strings.ToLower(dialect) {
		case "postgres":
			ddl := spec.DDL
			if spec.PGDDL != "" {
				ddl = spec.PGDDL
			}
			return fmt.Sprintf("CREATE INDEX CONCURRENTLY IF NOT EXISTS %s ON %s %s",
				spec.Name, spec.Table, ddl), nil
		case "sqlite":
			return fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s %s",
				spec.Name, spec.Table, spec.DDL), nil
		default:
			return "", fmt.Errorf("unsupported dialect %q", dialect)
		}
	}
	return "", fmt.Errorf("unknown index %q", name)
}
