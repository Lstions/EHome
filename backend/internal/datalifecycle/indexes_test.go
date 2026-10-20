package datalifecycle

import (
	"context"
	"strings"
	"testing"
	"time"

	"ehome/backend/testutil"
)

// ==================== 双方言索引语句 ====================

func TestRenderIndexDDL_Postgres(t *testing.T) {
	cases := []struct {
		idx     string
		columns string
	}{
		{IndexUnifiedLogicalTS, "(logical_device_id, timestamp DESC)"},
		{IndexDevDataLogicalTS, "(logical_device_id, timestamp DESC)"},
		// BMS 页慢修复 P0-2 (契约 §1.4): 图表历史查询 / latest-data 复合索引。
		// PG covering 索引 (INCLUDE value): avg/change 变 Index Only Scan。
		{IndexUnifiedLogicalSensorTS,
			"(logical_device_id, sensor_name, \"timestamp\") INCLUDE (\"value\")"},
		{IndexDevDataLogicalCreated, "(logical_device_id, created_at DESC)"},
		// BMS 页慢修复 §1.6: UNION ALL B 分支 (NULL-logical 旧行) 部分索引。
		{IndexUnifiedLegacyNullTS,
			"(device_id, sensor_name, \"timestamp\") INCLUDE (\"value\") WHERE logical_device_id IS NULL"},
		{IndexDevDataLegacyNullCreat,
			"(device_id, created_at DESC) WHERE logical_device_id IS NULL"},
	}
	for _, tc := range cases {
		stmt, err := RenderIndexDDL("postgres", tc.idx)
		if err != nil {
			t.Fatalf("render %s: %v", tc.idx, err)
		}
		if !strings.HasPrefix(stmt, "CREATE INDEX CONCURRENTLY IF NOT EXISTS "+tc.idx) {
			t.Errorf("postgres %s: %q missing CONCURRENTLY prefix", tc.idx, stmt)
		}
		if !strings.Contains(stmt, tc.columns) {
			t.Errorf("postgres %s: %q missing composite columns %q", tc.idx, stmt, tc.columns)
		}
	}
}

func TestRenderIndexDDL_SQLite(t *testing.T) {
	for _, idx := range []string{
		IndexUnifiedLogicalTS, IndexDevDataLogicalTS,
		IndexUnifiedLogicalSensorTS, IndexDevDataLogicalCreated,
		IndexUnifiedLegacyNullTS, IndexDevDataLegacyNullCreat,
	} {
		stmt, err := RenderIndexDDL("sqlite", idx)
		if err != nil {
			t.Fatalf("render %s: %v", idx, err)
		}
		if strings.Contains(stmt, "CONCURRENTLY") {
			t.Errorf("sqlite %s: %q must not use CONCURRENTLY", idx, stmt)
		}
		if !strings.HasPrefix(stmt, "CREATE INDEX IF NOT EXISTS "+idx) {
			t.Errorf("sqlite %s: %q bad prefix", idx, stmt)
		}
	}
	if _, err := RenderIndexDDL("mysql", IndexUnifiedLogicalTS); err == nil {
		t.Error("unsupported dialect accepted")
	}
	if _, err := RenderIndexDDL("postgres", "idx_nonexistent"); err == nil {
		t.Error("unknown index accepted")
	}
}

// ==================== 端到端索引创建 ====================

// 当前测试方言下真实建索引并验证存在与幂等。
// SQLite (默认): CREATE INDEX IF NOT EXISTS 路径。
// PostgreSQL (EHOME_TEST_DB=postgres): CONCURRENTLY 路径 (独立连接, 无事务)。
func TestEnsureLogicalDataIndexes_CreateAndIdempotent(t *testing.T) {
	db := testutil.OpenTestDB(t)
	ctx := context.Background()

	if err := EnsureLogicalDataIndexes(ctx, db); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	for _, idx := range []string{
		IndexUnifiedLogicalTS, IndexDevDataLogicalTS,
		IndexUnifiedLogicalSensorTS, IndexDevDataLogicalCreated,
		IndexUnifiedLegacyNullTS, IndexDevDataLegacyNullCreat,
	} {
		exists, err := IndexExists(ctx, db, idx)
		if err != nil {
			t.Fatalf("IndexExists %s: %v", idx, err)
		}
		if !exists {
			t.Errorf("index %s not created", idx)
		}
	}

	// 幂等: 重复执行不报错 (IF NOT EXISTS)。
	if err := EnsureLogicalDataIndexes(ctx, db); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
}

// BMS 页慢修复 P0-2 (契约 §1.4): 新复合索引必须真的被查询计划选用 ——
// "建了但规划器不选"等于没建, 所以这里用 EXPLAIN QUERY PLAN 正向断言。
func TestEnsureLogicalDataIndexes_BMSHistoryIndexesUsedByPlanner(t *testing.T) {
	if testutil.IsPostgres() {
		// EXPLAIN QUERY PLAN 是 SQLite 语法 (PG 会报 syntax error at "QUERY");
		// PG 方言下的索引可用性由 api 包的
		// TestPostgresUnionPlanUsesCompositeIndexes (真实生产路径 SQL 的
		// EXPLAIN 断言) 覆盖。
		t.Skip("SQLite-only planner assertion; PostgreSQL planner is covered by api TestPostgresUnionPlanUsesCompositeIndexes")
	}
	db := testutil.OpenTestDB(t)
	ctx := context.Background()
	if err := EnsureLogicalDataIndexes(ctx, db); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	// 图表历史查询: 过滤列 = (logical_device_id, sensor_name, timestamp)。
	var plan []queryPlanRow
	if err := db.Raw(`EXPLAIN QUERY PLAN
SELECT "timestamp", "value" FROM unified_data
WHERE logical_device_id = ? AND sensor_name = ? AND "timestamp" BETWEEN ? AND ?
ORDER BY "timestamp"`, 14, "cell_voltage_1", start, end).Scan(&plan).Error; err != nil {
		t.Fatalf("explain unified history query: %v", err)
	}
	if !planUsesIndex(plan, IndexUnifiedLogicalSensorTS) {
		t.Fatalf("unified history query plan does not use %s:\n  %s",
			IndexUnifiedLogicalSensorTS, renderPlan(plan))
	}

	// latest-data: ORDER BY created_at DESC LIMIT 1。
	plan = nil
	if err := db.Raw(`EXPLAIN QUERY PLAN
SELECT * FROM device_data WHERE logical_device_id = ? ORDER BY created_at DESC LIMIT 1`, 14).
		Scan(&plan).Error; err != nil {
		t.Fatalf("explain latest-data query: %v", err)
	}
	if !planUsesIndex(plan, IndexDevDataLogicalCreated) {
		t.Fatalf("latest-data query plan does not use %s:\n  %s",
			IndexDevDataLogicalCreated, renderPlan(plan))
	}
}
