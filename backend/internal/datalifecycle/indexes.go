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
const (
	IndexUnifiedLogicalTS = "idx_unified_logical_ts"
	IndexDevDataLogicalTS = "idx_devdata_logical_ts"
)

// logicalIndexSpecs maps index name → CREATE INDEX body (without the
// CREATE INDEX <name> prefix, which differs per dialect).
var logicalIndexSpecs = []struct {
	Name  string
	Table string
	DDL   string // column list
}{
	{IndexUnifiedLogicalTS, "unified_data", "(logical_device_id, timestamp DESC)"},
	{IndexDevDataLogicalTS, "device_data", "(logical_device_id, timestamp DESC)"},
}

// EnsureLogicalDataIndexes creates the §1.1 composite indexes idempotently.
//
// PostgreSQL: CREATE INDEX CONCURRENTLY IF NOT EXISTS — 不阻塞在线写入。
// CONCURRENTLY 不能在事务块内执行, 故用独立连接且不走 db.Transaction;
// 若上次 CONCURRENTLY 构建中途失败留下 INVALID 索引 (IF NOT EXISTS 会
// 静默跳过它), 先 DROP 再重建。
//
// SQLite: 普通 CREATE INDEX IF NOT EXISTS (无 CONCURRENTLY 概念)。
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
		// INVALID 索引检测: CONCURRENTLY 构建失败会留下 indisvalid=false
		// 的残骸, IF NOT EXISTS 对其生效 → 索引实际缺失却被静默跳过。
		var invalid bool
		err := db.WithContext(ctx).Raw(
			`SELECT COUNT(*) > 0 FROM pg_class c
JOIN pg_index i ON i.indexrelid = c.oid
WHERE c.relname = ? AND NOT i.indisvalid`, spec.Name).
			Scan(&invalid).Error
		if err != nil {
			return fmt.Errorf("check invalid index %s: %w", spec.Name, err)
		}
		if invalid {
			if err := db.WithContext(ctx).
				Exec(fmt.Sprintf("DROP INDEX IF EXISTS %s", spec.Name)).Error; err != nil {
				return fmt.Errorf("drop invalid index %s: %w", spec.Name, err)
			}
			slog.Warn("datalifecycle: dropped INVALID index before rebuild",
				"index", spec.Name)
		}

		stmt := fmt.Sprintf("CREATE INDEX CONCURRENTLY IF NOT EXISTS %s ON %s %s",
			spec.Name, spec.Table, spec.DDL)
		// 独立连接 + 无事务: CONCURRENTLY 不能在事务块内执行。GORM 顶层
		// Exec 不开事务; 无参数时 pgx 走 simple protocol, 不会被隐式事务
		// 包裹。
		if err := db.WithContext(ctx).Exec(stmt).Error; err != nil {
			return fmt.Errorf("create index concurrently %s: %w", spec.Name, err)
		}
		slog.Info("datalifecycle: index ensured (CONCURRENTLY)",
			"index", spec.Name, "table", spec.Table)
	}
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
			return fmt.Sprintf("CREATE INDEX CONCURRENTLY IF NOT EXISTS %s ON %s %s",
				spec.Name, spec.Table, spec.DDL), nil
		case "sqlite":
			return fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s %s",
				spec.Name, spec.Table, spec.DDL), nil
		default:
			return "", fmt.Errorf("unsupported dialect %q", dialect)
		}
	}
	return "", fmt.Errorf("unknown index %q", name)
}
