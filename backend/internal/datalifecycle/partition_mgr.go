package datalifecycle

import (
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"ehome/backend/pkg/metrics"
)

// 数据层时序化 (方案 docs/设计/架构优化实施方案.md v0.4 §3.2.1)。
// unified_data 改 PG 声明式 RANGE 分区（月粒度，分区键 timestamp）。
// SQLite（单测环境）不分区：所有函数在非 postgres 方言下直接返回 no-op，
// 与 purge.go:300 / backfill.go:178 的方言分支模式一致。
//
// 分区机制已泛化为按表名参数化（*For 系列）；unified_data 仅作默认表，
// 既有 EnsurePartitions/DropPartitionsBefore/MigrateUnifiedDataToPartitioned
// 保留旧签名并委托，调用方零改动。

const (
	// partitionRollaheadMonths 启动/每日检查时确保未来 N 个月的分区存在。
	partitionRollaheadMonths = 3

	// partitionedTable 分区母表名；legacyTable 迁移后原表保留名。
	partitionedTable = "unified_data"
	legacyTable      = "unified_data_legacy"

	// 搬迁批次复用 migrate.go 的 migrateBatchSizePostgres（同包常量，PG 1 万行）。
)

// PartitionManager 管理时序表的月分区生命周期（默认 unified_data）。
type PartitionManager struct {
	db *gorm.DB
}

// NewPartitionManager creates a PartitionManager.
func NewPartitionManager(db *gorm.DB) *PartitionManager {
	return &PartitionManager{db: db}
}

func isPostgres(db *gorm.DB) bool {
	return db != nil && db.Dialector != nil && db.Dialector.Name() == "postgres"
}

// partitionName renders the monthly partition table name of table for t.
func partitionName(table string, t time.Time) string {
	return fmt.Sprintf("%s_%04d%02d", table, t.Year(), int(t.Month()))
}

// monthStart truncates t to the first day of its month (UTC).
func monthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// addMonths returns monthStart(t) advanced by n months.
func addMonths(t time.Time, n int) time.Time {
	return monthStart(t).AddDate(0, n, 0)
}

// EnsurePartitionsFor creates any missing monthly partitions of table covering
// [currentMonth-1, currentMonth+months]. Safe to call repeatedly.
// No-op on non-postgres dialects.
func (pm *PartitionManager) EnsurePartitionsFor(table string, months int) error {
	if !isPostgres(pm.db) {
		slog.Debug("partition_mgr: non-postgres dialect, skip")
		return nil
	}
	if months < 0 {
		months = 0
	}
	now := time.Now()
	// 从上月开始到未来 months 月（上月兜底：跨月边界晚到的数据仍可写入）。
	for i := -1; i <= months; i++ {
		start := addMonths(now, i)
		end := addMonths(start, 1)
		if err := pm.createPartitionIfNotExists(partitionName(table, start), table, start, end); err != nil {
			metrics.LifecycleTaskFailures.WithLabelValues("partition").Inc()
			return fmt.Errorf("ensure partition %s: %w", partitionName(table, start), err)
		}
	}
	return nil
}

// EnsurePartitions 保留旧签名，委托给 EnsurePartitionsFor("unified_data", ...)。
func (pm *PartitionManager) EnsurePartitions(months int) error {
	return pm.EnsurePartitionsFor(partitionedTable, months)
}

// tableExistsSQL 统计当前 schema 内指定 relkind 的同名表数量。
// schema 限定 (n.nspname = current_schema()) 必须: 集成测试每用例独立
// schema, 无限定则跨 schema 误判存在性 (生产单 schema 下同样更健壮)。
const tableExistsSQL = `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relname = ? AND c.relkind = ? AND n.nspname = current_schema()`

// createPartitionIfNotExists creates one RANGE partition under parent when absent.
// 存在性判断走 pg_class（PG 无 CREATE TABLE IF NOT EXISTS ... PARTITION OF）。
func (pm *PartitionManager) createPartitionIfNotExists(name, parent string, start, end time.Time) error {
	var count int64
	err := pm.db.Raw(tableExistsSQL, name, "r").Scan(&count).Error
	if err != nil {
		return fmt.Errorf("check pg_class: %w", err)
	}
	if count > 0 {
		return nil
	}
	ddl := fmt.Sprintf(
		"CREATE TABLE %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')",
		name, parent,
		start.Format("2006-01-02 15:04:05"), end.Format("2006-01-02 15:04:05"),
	)
	if err := pm.db.Exec(ddl).Error; err != nil {
		return err
	}
	slog.Info("partition_mgr: created partition", "partition", name, "parent", parent)
	return nil
}

// DropPartitionsBeforeFor drops all monthly partitions of table whose entire
// range is older than cutoff. Retention 的 O(1) 替代：整分区 DROP 替代逐行 DELETE。
// Returns the dropped partition names.
func (pm *PartitionManager) DropPartitionsBeforeFor(table string, cutoff time.Time) ([]string, error) {
	if !isPostgres(pm.db) {
		return nil, nil
	}
	var names []string
	rows, err := pm.db.Raw(
		`SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relname LIKE ? AND c.relkind = 'r' AND n.nspname = current_schema()`,
		table+"_%",
	).Rows()
	if err != nil {
		metrics.LifecycleTaskFailures.WithLabelValues("partition").Inc()
		return nil, fmt.Errorf("list partitions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			names = append(names, n)
		}
	}

	cutoffMonth := monthStart(cutoff)
	var dropped []string
	for _, n := range names {
		// 分区名形如 <table>_YYYYMM: 跳过母表名+下划线再解析月份。
		// (修复 off-by-one: 此前 n[len(partitionedTable):] 带下划线,
		// time.Parse 恒失败, 导致到期分区永不 DROP。)
		pt, parseErr := time.Parse("200601", n[len(table)+1:])
		if parseErr != nil {
			continue // 非 YYYYMM 后缀的表不碰
		}
		pStart := monthStart(pt)
		// 整分区早于 cutoff 才删：分区起点 < cutoff 月起点。
		if pStart.Before(cutoffMonth) {
			if err := pm.db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", n)).Error; err != nil {
				metrics.LifecycleTaskFailures.WithLabelValues("partition").Inc()
				return dropped, fmt.Errorf("drop %s: %w", n, err)
			}
			dropped = append(dropped, n)
			metrics.LifecycleDroppedPartitions.Inc()
			slog.Info("partition_mgr: dropped expired partition", "partition", n)
		}
	}
	return dropped, nil
}

// DropPartitionsBefore 保留旧签名，委托给 DropPartitionsBeforeFor("unified_data", ...)。
func (pm *PartitionManager) DropPartitionsBefore(cutoff time.Time) ([]string, error) {
	return pm.DropPartitionsBeforeFor(partitionedTable, cutoff)
}

// EnsureRollupTable idempotently creates the minute-level rollup table
// (方案 v3.4 §3.2.2 DDL). No-op on non-postgres dialects (SQLite 测试库
// 不建此表, RollupConsumer no-op)。
//
// 建表责任方裁决: rollup 表与分区母表同属数据层时序化迁移面, 由启动接线
// 调用 (main.go), 不放 AutoMigrate (与 UnifiedData 移出 AutoMigrate 同因:
// 时序化表结构由迁移面显式定义, 不交给 GORM tag 推导)。
func EnsureRollupTable(db *gorm.DB) error {
	if !isPostgres(db) {
		return nil
	}
	var count int64
	if err := db.Raw(tableExistsSQL, "unified_data_rollup_1m", "r").Scan(&count).Error; err != nil {
		metrics.LifecycleTaskFailures.WithLabelValues("rollup").Inc()
		return fmt.Errorf("check rollup table existence: %w", err)
	}
	if count > 0 {
		return nil
	}
	ddl := `CREATE TABLE unified_data_rollup_1m (
		device_id   BIGINT NOT NULL,
		sensor_name VARCHAR(32) NOT NULL,
		bucket      TIMESTAMP NOT NULL,
		min_v       DOUBLE PRECISION,
		max_v       DOUBLE PRECISION,
		avg_v       DOUBLE PRECISION,
		last_v      DOUBLE PRECISION,
		last_id     BIGINT,
		cnt         INTEGER,
		PRIMARY KEY (device_id, sensor_name, bucket)
	)`
	if err := db.Exec(ddl).Error; err != nil {
		metrics.LifecycleTaskFailures.WithLabelValues("rollup").Inc()
		return fmt.Errorf("create rollup table: %w", err)
	}
	slog.Info("partition_mgr: created rollup table unified_data_rollup_1m")
	return nil
}

// IsTablePartitioned reports whether table is already a partitioned parent
// (relkind='p', migration done).
func IsTablePartitioned(db *gorm.DB, table string) bool {
	if !isPostgres(db) {
		return false
	}
	var count int64
	// 分区母表 relkind='p'（partitioned table），普通表为 'r'。
	db.Raw(tableExistsSQL, table, "p").Scan(&count)
	return count > 0
}

// IsUnifiedDataPartitioned 保留旧签名，委托给 IsTablePartitioned("unified_data")。
func IsUnifiedDataPartitioned(db *gorm.DB) bool {
	return IsTablePartitioned(db, partitionedTable)
}

// MigrateTableToPartitioned converts the flat table into a declarative
// RANGE-partitioned parent (monthly, key=timestamp), preserving:
//   - id 序列全局不变（业务零感知，id 单调排序语义不变）
//   - 全部既有索引（分区级重建）
//   - 原表数据（双表并存搬迁 + legacy RENAME 保留，不 DROP）
//
// legacyName 为迁移后原平表的保留名。幂等：母表已存在（已迁移过）直接返回。
// SQLite no-op。
// 步骤: 建 new 母表 → 按月分批 INSERT SELECT → 校验行数 → RENAME swap。
func MigrateTableToPartitioned(db *gorm.DB, table, legacyName string) error {
	if !isPostgres(db) {
		slog.Debug("migrate_partitioned: non-postgres dialect, skip")
		return nil
	}
	if IsTablePartitioned(db, table) {
		slog.Info("migrate_partitioned: already partitioned, skip")
		return nil
	}
	// 原表不存在（全新部署）→ 直接以最终名建分区母表。
	// 不走 _new 中转: fresh 路径无数据搬迁, 无需 RENAME swap。
	var flatExists int64
	db.Raw(tableExistsSQL, table, "r").Scan(&flatExists)
	if flatExists == 0 {
		slog.Info("migrate_partitioned: no legacy flat table, creating fresh partitioned parent", "table", table)
		// 幂等: 清理上次中断残留的 new 表。
		if err := db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", table+"_new")).Error; err != nil {
			return fmt.Errorf("drop stale %s: %w", table+"_new", err)
		}
		return createPartitionedParent(db, table, table, true)
	}

	newTable := table + "_new"
	// 幂等: 清理上次中断残留的 new 表。
	if err := db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", newTable)).Error; err != nil {
		return fmt.Errorf("drop stale %s: %w", newTable, err)
	}
	if err := createPartitionedParent(db, table, newTable, false); err != nil {
		return fmt.Errorf("create %s: %w", newTable, err)
	}

	// 按月分批搬迁（水位断点续跑: 失败重跑时 INSERT 已存在行会被 ON CONFLICT 跳过——
	// 主键 (id,timestamp) 天生幂等）。
	now := time.Now()
	oldest := oldestRowMonthFor(db, table)
	if oldest.IsZero() {
		oldest = now // 空表
	}
	pm := &PartitionManager{db: db}
	for m := monthStart(oldest); !m.After(monthStart(now)); m = addMonths(m, 1) {
		// 历史月份的分区必须先建好, 否则 INSERT INTO new 母表报
		// "no partition of relation found" (PG 分区表无兜底 default 分区)。
		// 分区名以最终母表 (table) 为前缀, 挂在 new 母表下; RENAME 后即
		// table_YYYYMM (与既有 unified_data 命名/retention 扫描一致)。
		if err := pm.createPartitionIfNotExists(partitionName(table, m), newTable, monthStart(m), addMonths(m, 1)); err != nil {
			return fmt.Errorf("create history partition %s: %w", partitionName(table, m), err)
		}
		if err := copyMonthBatched(db, newTable, table, m); err != nil {
			return fmt.Errorf("copy month %04d-%02d: %w", m.Year(), int(m.Month()), err)
		}
	}
	// 未来分区 (含上月兜底): 在 swap 前于 new 母表下建好, RENAME 后即刻可写。
	// 不能调 EnsurePartitionsFor(table) — 它以 table (此刻仍是旧平表) 为母表。
	for i := -1; i <= partitionRollaheadMonths; i++ {
		start := addMonths(now, i)
		if err := pm.createPartitionIfNotExists(partitionName(table, start), newTable, start, addMonths(start, 1)); err != nil {
			return fmt.Errorf("ensure partition %s: %w", partitionName(table, start), err)
		}
	}

	// 行数校验门禁（§8 风险表: 搬迁丢数据缓解）。
	var srcCount, dstCount int64
	db.Raw("SELECT count(*) FROM " + table).Scan(&srcCount)
	db.Raw("SELECT count(*) FROM " + newTable).Scan(&dstCount)
	if srcCount != dstCount {
		return fmt.Errorf("row count mismatch after migration: src=%d dst=%d (aborting, tables intact)", srcCount, dstCount)
	}

	// RENAME swap: 原表→legacy 保留不删（降险），new→table。
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(fmt.Sprintf("ALTER TABLE %s RENAME TO %s", table, legacyName)).Error; err != nil {
			return err
		}
		return tx.Exec(fmt.Sprintf("ALTER TABLE %s RENAME TO %s", newTable, table)).Error
	}); err != nil {
		return fmt.Errorf("rename swap: %w", err)
	}
	// 推进 id 序列至 MAX(id): BIGSERIAL 的 INSERT...SELECT 搬迁不触碰
	// sequence, 不推进则迁移后首条新数据 nextval 回到 1 (违反 "id 序列
	// 全局不变" 承诺, id 单调排序语义被破坏)。
	if err := db.Exec(fmt.Sprintf(
		"SELECT setval(pg_get_serial_sequence('%s', 'id'), COALESCE((SELECT MAX(id) FROM %s), 1))",
		table, table)).Error; err != nil {
		return fmt.Errorf("advance id sequence: %w", err)
	}
	slog.Info("migrate_partitioned: migration complete",
		"table", table, "rows", dstCount, "legacy_table", legacyName)
	return nil
}

// MigrateUnifiedDataToPartitioned 保留旧签名，委托给 MigrateTableToPartitioned。
func MigrateUnifiedDataToPartitioned(db *gorm.DB) error {
	return MigrateTableToPartitioned(db, partitionedTable, legacyTable)
}

// partitionParentDDL maps a logical table name to the CREATE TABLE template of
// its partitioned parent (%s = physical table name; <table> on the fresh path,
// <table>_new during legacy migration). Per-table switch keeps the existing
// unified_data column definition verbatim (防回归) while device_data aligns
// with models.DeviceData. Both use the composite PK (id, timestamp) required
// by PG partitioning (the partition key must be part of the primary key) and
// RANGE-partition on timestamp.
var partitionParentDDL = map[string]string{
	"unified_data": `CREATE TABLE %s (
		id BIGSERIAL,
		device_id BIGINT NOT NULL,
		sensor_name VARCHAR(32) NOT NULL,
		value DOUBLE PRECISION NOT NULL,
		unit VARCHAR(16),
		timestamp TIMESTAMPTZ NOT NULL,
		created_at TIMESTAMPTZ,
		edge_device_id BIGINT,
		logical_device_id BIGINT,
		PRIMARY KEY (id, timestamp)
	) PARTITION BY RANGE (timestamp)`,
	// device_data: data_json TEXT; device_id/node_id NOT NULL (models 对齐)。
	"device_data": `CREATE TABLE %s (
		id BIGSERIAL,
		device_id BIGINT NOT NULL,
		node_id VARCHAR(32) NOT NULL,
		data_json TEXT,
		timestamp TIMESTAMPTZ NOT NULL,
		created_at TIMESTAMPTZ,
		edge_device_id BIGINT,
		logical_device_id BIGINT,
		PRIMARY KEY (id, timestamp)
	) PARTITION BY RANGE (timestamp)`,
}

// partitionParentIndexes maps a logical table name to the CREATE INDEX
// templates (%s、%s = physical table name) rebuilding the flat table's b-tree
// indexes at partition level. The (logical_device_id, timestamp DESC) composite
// index is intentionally NOT created here — indexes.go owns it
// (EnsureLogicalDataIndexes). 索引名沿用既有 <name>_<column>_idx 约定。
var partitionParentIndexes = map[string][]string{
	"unified_data": {
		"CREATE INDEX %s_device_id_idx ON %s (device_id)",
		"CREATE INDEX %s_sensor_name_idx ON %s (sensor_name)",
		"CREATE INDEX %s_timestamp_idx ON %s (timestamp)",
		"CREATE INDEX %s_edge_device_id_idx ON %s (edge_device_id)",
	},
	"device_data": {
		"CREATE INDEX %s_device_id_idx ON %s (device_id)",
		"CREATE INDEX %s_node_id_idx ON %s (node_id)",
		"CREATE INDEX %s_timestamp_idx ON %s (timestamp)",
		"CREATE INDEX %s_edge_device_id_idx ON %s (edge_device_id)",
	},
}

// partitionCopyColumns maps a logical table name to the column list the
// migration INSERT...SELECT copies. 两张表列集不同, 必须按表生成 (否则列不存在
// 或列数不匹配)。
var partitionCopyColumns = map[string]string{
	"unified_data": "id, device_id, sensor_name, value, unit, timestamp, created_at, edge_device_id, logical_device_id",
	"device_data":  "id, device_id, node_id, data_json, timestamp, created_at, edge_device_id, logical_device_id",
}

// createPartitionedParent creates the partitioned parent for logical table
// table, physically named name (table on the fresh path, table+"_new" during
// legacy migration), with the composite PK (id, timestamp) and per-partition
// indexes inherited via the parent definition. When fresh=true it also creates
// the first partitions.
func createPartitionedParent(db *gorm.DB, table, name string, fresh bool) error {
	ddlTemplate, indexTemplates := partitionParentDDL[table], partitionParentIndexes[table]
	if ddlTemplate == "" {
		// 未登记表沿用 unified_data 列结构 (与泛化探针测试及历史硬编码行为向后兼容);
		// 登记表 (unified_data / device_data) 按上表各自定义。
		ddlTemplate, indexTemplates = partitionParentDDL[partitionedTable], partitionParentIndexes[partitionedTable]
	}
	if err := db.Exec(fmt.Sprintf(ddlTemplate, name)).Error; err != nil {
		return err
	}
	// 既有 b-tree 索引在分区级重建（与 models.go 原 index 定义一致）。
	for _, tmpl := range indexTemplates {
		if err := db.Exec(fmt.Sprintf(tmpl, name, name)).Error; err != nil {
			return err
		}
	}
	if fresh {
		pm := &PartitionManager{db: db}
		if err := pm.EnsurePartitionsFor(name, partitionRollaheadMonths); err != nil {
			return err
		}
	}
	return nil
}

// oldestRowMonthFor returns the month of the earliest row in table, or zero
// time if empty.
func oldestRowMonthFor(db *gorm.DB, table string) time.Time {
	var ts *time.Time
	db.Raw("SELECT min(timestamp) FROM " + table).Scan(&ts)
	if ts == nil {
		return time.Time{}
	}
	return monthStart(*ts)
}

// copyMonthBatched copies one calendar month from src into dst in batches of
// migrateBatchSizePostgres using an id-watermark loop (断点续跑: 重跑幂等).
func copyMonthBatched(db *gorm.DB, dst, src string, month time.Time) error {
	cols := partitionCopyColumns[src]
	if cols == "" {
		// 未登记表沿用 unified_data 列清单 (见 createPartitionedParent 注记)。
		cols = partitionCopyColumns[partitionedTable]
	}
	start := monthStart(month)
	end := addMonths(month, 1)
	watermark := int64(0)
	for {
		res := db.Exec(fmt.Sprintf(
			`INSERT INTO %s (%s)
			 SELECT %s
			 FROM %s
			 WHERE id > ? AND timestamp >= ? AND timestamp < ?
			 ORDER BY id LIMIT %d
			 ON CONFLICT (id, timestamp) DO NOTHING`,
			dst, cols, cols, src, migrateBatchSizePostgres),
			watermark, start, end,
		)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected < int64(migrateBatchSizePostgres) {
			return nil // 本月搬完
		}
		// 推进水位: 取本批最大 id。
		var maxID int64
		db.Raw(fmt.Sprintf(
			"SELECT COALESCE(max(id), ?) FROM %s WHERE id > ? AND timestamp >= ? AND timestamp < ?",
			src),
			watermark, watermark, start, end,
		).Scan(&maxID)
		if maxID <= watermark {
			return nil
		}
		watermark = maxID
	}
}
