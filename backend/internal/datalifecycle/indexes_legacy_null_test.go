package datalifecycle

// §1.6 UNION ALL B 分支 (NULL-logical 旧行) 的部分索引 —— SQLite 侧计划断言。
//
// B 分支 SQL 恒为
//   logical_device_id IS NULL AND device_id IN (...) [AND sensor_name=? AND timestamp BETWEEN ...]
// 没有部分索引时它在 22.5M 行上会退化为全窗口 timestamp/device_id 扫描, 把 A 分支
// 省下的成本吃回去。生产 NULL-logical 行 ≈0, 所以用 WHERE 部分索引 (体积可忽略)。
//
// PG 方言下的等价断言 (真实生产路径 SQL 的 EXPLAIN) 在 api 包的
// TestPostgresUnionPlanUsesCompositeIndexes; 本文件是 SQLite 侧的同构防线。
// PG 下本文件显式 skip (EXPLAIN QUERY PLAN 是 SQLite 语法)。

import (
	"context"
	"strings"
	"testing"
	"time"

	"ehome/backend/testutil"
)

// queryPlanRow / planUsesIndex / renderPlan 是 SQLite EXPLAIN QUERY PLAN 的
// 共享断言辅助 (原在 indexes_test.go; 该文件中途截断重写时移到这里)。
type queryPlanRow struct {
	Detail string `gorm:"column:detail"`
}

func planUsesIndex(plan []queryPlanRow, index string) bool {
	for _, row := range plan {
		if strings.Contains(row.Detail, index) {
			return true
		}
	}
	return false
}

func renderPlan(plan []queryPlanRow) string {
	lines := make([]string, 0, len(plan))
	for _, row := range plan {
		lines = append(lines, row.Detail)
	}
	return strings.Join(lines, " | ")
}

func TestEnsureLogicalDataIndexes_LegacyNullPartialIndexesUsedByPlanner(t *testing.T) {
	if testutil.IsPostgres() {
		t.Skip("SQLite-only planner assertion; PostgreSQL is covered by api TestPostgresUnionPlanUsesCompositeIndexes")
	}
	db := testutil.OpenTestDB(t)
	ctx := context.Background()
	if err := EnsureLogicalDataIndexes(ctx, db); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	// 造一些 NULL-logical 旧行与逻辑行, 让计划选择不依赖"空表碰巧"。
	for i := 0; i < 50; i++ {
		ts := start.Add(time.Duration(i) * time.Second)
		if err := db.Exec(
			"INSERT INTO unified_data (device_id, sensor_name, value, timestamp) VALUES (?, ?, ?, ?)",
			14, "cell_voltage_1", float64(i), ts).Error; err != nil {
			t.Fatalf("seed NULL-logical unified row: %v", err)
		}
		if err := db.Exec(
			"INSERT INTO device_data (device_id, node_id, data_json, timestamp, created_at) VALUES (?, ?, ?, ?, ?)",
			14, "N-LEGACY", "{}", ts, ts).Error; err != nil {
			t.Fatalf("seed NULL-logical device_data row: %v", err)
		}
	}

	// B 分支历史查询: (device_id, sensor_name, timestamp) 全部是索引条件。
	var plan []queryPlanRow
	if err := db.Raw(`EXPLAIN QUERY PLAN
SELECT "timestamp", "value" FROM unified_data
WHERE logical_device_id IS NULL AND device_id IN (?) AND sensor_name = ? AND "timestamp" BETWEEN ? AND ?`,
		14, "cell_voltage_1", start, end).Scan(&plan).Error; err != nil {
		t.Fatalf("explain NULL-logical history query: %v", err)
	}
	if !planUsesIndex(plan, IndexUnifiedLegacyNullTS) {
		t.Fatalf("NULL-logical history query plan does not use %s: %s",
			IndexUnifiedLegacyNullTS, renderPlan(plan))
	}

	// B 分支 latest-data: (device_id, created_at DESC) 部分索引。
	plan = nil
	if err := db.Raw(`EXPLAIN QUERY PLAN
SELECT * FROM device_data
WHERE logical_device_id IS NULL AND device_id IN (?)
ORDER BY created_at DESC LIMIT 1`, 14).Scan(&plan).Error; err != nil {
		t.Fatalf("explain NULL-logical latest-data query: %v", err)
	}
	if !planUsesIndex(plan, IndexDevDataLegacyNullCreat) {
		t.Fatalf("NULL-logical latest-data query plan does not use %s: %s",
			IndexDevDataLegacyNullCreat, renderPlan(plan))
	}
}
