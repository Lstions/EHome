package datalifecycle

import (
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"
)

// 数据层时序化 (方案 docs/设计/架构优化实施方案.md v0.4 §3.2.1)。
// unified_data 改 PG 声明式 RANGE 分区（月粒度，分区键 timestamp）。
// SQLite（单测环境）不分区：所有函数在非 postgres 方言下直接返回 no-op，
// 与 purge.go:300 / backfill.go:178 的方言分支模式一致。

const (
	// partitionRollaheadMonths 启动/每日检查时确保未来 N 个月的分区存在。
	partitionRollaheadMonths = 3

	// partitionedTable 分区母表名；legacyTable 迁移后原表保留名。
	partitionedTable = "unified_data"
	legacyTable      = "unified_data_legacy"

	// 搬迁批次复用 migrate.go 的 migrateBatchSizePostgres（同包常量，PG 1 万行）。
)

// PartitionManager 管理 unified_data 的月分区生命周期。
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

// partitionName renders the monthly partition table name for t.
func partitionName(t time.Time) string {
	return fmt.Sprintf("%s_%04d%02d", partitionedTable, t.Year(), int(t.Month()))
}

// monthStart truncates t to the first day of its month (UTC).
func monthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// addMonths returns monthStart(t) advanced by n months.
func addMonths(t time.Time, n int) time.Time {
	return monthStart(t).AddDate(0, n, 0)
}

// EnsurePartitions creates any missing monthly partitions covering
// [currentMonth-1, currentMonth+months]. Safe to call repeatedly.
// No-op on non-postgres dialects.
func (pm *PartitionManager) EnsurePartitions(months int) error {
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
		if err := pm.createPartitionIfNotExists(partitionName(start), start, end); err != nil {
			return fmt.Errorf("ensure partition %s: %w", partitionName(start), err)
		}
	}
	return nil
}

// createPartitionIfNotExists creates one RANGE partition when absent.
// 存在性判断走 pg_class（PG 无 CREATE TABLE IF NOT EXISTS ... PARTITION OF）。
func (pm *PartitionManager) createPartitionIfNotExists(name string, start, end time.Time) error {
	var count int64
	err := pm.db.Raw(
		"SELECT count(*) FROM pg_class WHERE relname = ? AND relkind = 'r'",
		name,
	).Scan(&count).Error
	if err != nil {
		return fmt.Errorf("check pg_class: %w", err)
	}
	if count > 0 {
		return nil
	}
	ddl := fmt.Sprintf(
		"CREATE TABLE %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')",
		name, partitionedTable,
		start.Format("2006-01-02 15:04:05"), end.Format("2006-01-02 15:04:05"),
	)
	if err := pm.db.Exec(ddl).Error; err != nil {
		return err
	}
	slog.Info("partition_mgr: created partition", "partition", name)
	return nil
}

// DropPartitionsBefore drops all monthly partitions whose entire range is
// older than cutoff. Retention 的 O(1) 替代：整分区 DROP 替代逐行 DELETE。
// Returns the dropped partition names.
func (pm *PartitionManager) DropPartitionsBefore(cutoff time.Time) ([]string, error) {
	if !isPostgres(pm.db) {
		return nil, nil
	}
	var names []string
	rows, err := pm.db.Raw(
		"SELECT relname FROM pg_class WHERE relname LIKE ? AND relkind = 'r'",
		partitionedTable+"_%",
	).Rows()
	if err != nil {
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
		pt, parseErr := time.Parse("200601", n[len(partitionedTable):])
		if parseErr != nil {
			continue // 非 YYYYMM 后缀的表不碰
		}
		pStart := monthStart(pt)
		// 整分区早于 cutoff 才删：分区起点 < cutoff 月起点。
		if pStart.Before(cutoffMonth) {
			if err := pm.db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", n)).Error; err != nil {
				return dropped, fmt.Errorf("drop %s: %w", n, err)
			}
			dropped = append(dropped, n)
			slog.Info("partition_mgr: dropped expired partition", "partition", n)
		}
	}
	return dropped, nil
}

// IsUnifiedDataPartitioned reports whether the partitioned parent table is
// already in place (migration done).
func IsUnifiedDataPartitioned(db *gorm.DB) bool {
	if !isPostgres(db) {
		return false
	}
	var count int64
	// 分区母表 relkind='p'（partitioned table），普通表为 'r'。
	db.Raw(
		"SELECT count(*) FROM pg_class WHERE relname = ? AND relkind = 'p'",
		partitionedTable,
	).Scan(&count)
	return count > 0
}

// MigrateUnifiedDataToPartitioned converts the flat unified_data table into a
// declarative RANGE-partitioned parent (monthly, key=timestamp), preserving:
//   - id 序列全局不变（业务零感知，id 单调排序语义不变）
//   - 全部既有索引（分区级重建）
//   - 原表数据（双表并存搬迁 + legacy RENAME 保留，不 DROP）
//
// 幂等：母表已存在（已迁移过）直接返回。SQLite no-op。
// 步骤: 建 new 母表 → 按月分批 INSERT SELECT → 校验行数 → RENAME swap。
func MigrateUnifiedDataToPartitioned(db *gorm.DB) error {
	if !isPostgres(db) {
		slog.Debug("migrate_partitioned: non-postgres dialect, skip")
		return nil
	}
	if IsUnifiedDataPartitioned(db) {
		slog.Info("migrate_partitioned: already partitioned, skip")
		return nil
	}
	// 原表不存在（全新部署）→ 直接建分区母表后返回。
	var flatExists int64
	db.Raw("SELECT count(*) FROM pg_class WHERE relname = ? AND relkind = 'r'", partitionedTable).Scan(&flatExists)
	if flatExists == 0 {
		slog.Info("migrate_partitioned: no legacy flat table, creating fresh partitioned parent")
		return createPartitionedParent(db, partitionedTable+"_new", true)
	}

	newTable := partitionedTable + "_new"
	// 幂等: 清理上次中断残留的 new 表。
	if err := db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", newTable)).Error; err != nil {
		return fmt.Errorf("drop stale %s: %w", newTable, err)
	}
	if err := createPartitionedParent(db, newTable, false); err != nil {
		return fmt.Errorf("create %s: %w", newTable, err)
	}

	// 按月分批搬迁（水位断点续跑: 失败重跑时 INSERT 已存在行会被 ON CONFLICT 跳过——
	// 主键 (id,timestamp) 天生幂等）。
	now := time.Now()
	oldest := oldestRowMonth(db)
	if oldest.IsZero() {
		oldest = now // 空表
	}
	for m := monthStart(oldest); !m.After(monthStart(now)); m = addMonths(m, 1) {
		if err := copyMonthBatched(db, newTable, m); err != nil {
			return fmt.Errorf("copy month %04d-%02d: %w", m.Year(), int(m.Month()), err)
		}
	}

	// 行数校验门禁（§8 风险表: 搬迁丢数据缓解）。
	var srcCount, dstCount int64
	db.Raw("SELECT count(*) FROM " + partitionedTable).Scan(&srcCount)
	db.Raw("SELECT count(*) FROM " + newTable).Scan(&dstCount)
	if srcCount != dstCount {
		return fmt.Errorf("row count mismatch after migration: src=%d dst=%d (aborting, tables intact)", srcCount, dstCount)
	}

	// RENAME swap: 原表→legacy 保留不删（降险），new→unified_data。
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(fmt.Sprintf("ALTER TABLE %s RENAME TO %s", partitionedTable, legacyTable)).Error; err != nil {
			return err
		}
		return tx.Exec(fmt.Sprintf("ALTER TABLE %s RENAME TO %s", newTable, partitionedTable)).Error
	}); err != nil {
		return fmt.Errorf("rename swap: %w", err)
	}
	slog.Info("migrate_partitioned: migration complete",
		"rows", dstCount, "legacy_table", legacyTable)
	return nil
}

// createPartitionedParent creates the partitioned parent table with the
// composite PK (id, timestamp) and per-partition indexes inherited via the
// parent definition. When fresh=true it also creates the first partitions.
func createPartitionedParent(db *gorm.DB, name string, fresh bool) error {
	ddl := fmt.Sprintf(`CREATE TABLE %s (
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
	) PARTITION BY RANGE (timestamp)`, name)
	if err := db.Exec(ddl).Error; err != nil {
		return err
	}
	// 既有 b-tree 索引在分区级重建（与 models.go 原 index 定义一致）。
	indexes := []string{
		fmt.Sprintf("CREATE INDEX %s_device_id_idx ON %s (device_id)", name, name),
		fmt.Sprintf("CREATE INDEX %s_sensor_name_idx ON %s (sensor_name)", name, name),
		fmt.Sprintf("CREATE INDEX %s_timestamp_idx ON %s (timestamp)", name, name),
		fmt.Sprintf("CREATE INDEX %s_edge_device_id_idx ON %s (edge_device_id)", name, name),
	}
	for _, idx := range indexes {
		if err := db.Exec(idx).Error; err != nil {
			return err
		}
	}
	if fresh {
		pm := &PartitionManager{db: db}
		if err := pm.EnsurePartitions(partitionRollaheadMonths); err != nil {
			return err
		}
	}
	return nil
}

// oldestRowMonth returns the month of the earliest row, or zero time if empty.
func oldestRowMonth(db *gorm.DB) time.Time {
	var ts *time.Time
	db.Raw("SELECT min(timestamp) FROM " + partitionedTable).Scan(&ts)
	if ts == nil {
		return time.Time{}
	}
	return monthStart(*ts)
}

// copyMonthBatched copies one calendar month in batches of
// migrateBatchSizePostgres using an id-watermark loop (断点续跑: 重跑幂等).
func copyMonthBatched(db *gorm.DB, dst string, month time.Time) error {
	start := monthStart(month)
	end := addMonths(month, 1)
	watermark := int64(0)
	for {
		res := db.Exec(fmt.Sprintf(
			`INSERT INTO %s (id, device_id, sensor_name, value, unit, timestamp, created_at, edge_device_id, logical_device_id)
			 SELECT id, device_id, sensor_name, value, unit, timestamp, created_at, edge_device_id, logical_device_id
			 FROM %s
			 WHERE id > ? AND timestamp >= ? AND timestamp < ?
			 ORDER BY id LIMIT %d
			 ON CONFLICT (id, timestamp) DO NOTHING`,
			dst, partitionedTable, migrateBatchSizePostgres),
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
			partitionedTable),
			watermark, watermark, start, end,
		).Scan(&maxID)
		if maxID <= watermark {
			return nil
		}
		watermark = maxID
	}
}
