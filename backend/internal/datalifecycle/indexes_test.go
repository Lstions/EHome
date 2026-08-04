package datalifecycle

import (
	"context"
	"strings"
	"testing"

	"ehome/backend/testutil"
)

// ==================== 双方言索引语句 ====================

func TestRenderIndexDDL_Postgres(t *testing.T) {
	for _, idx := range []string{IndexUnifiedLogicalTS, IndexDevDataLogicalTS} {
		stmt, err := RenderIndexDDL("postgres", idx)
		if err != nil {
			t.Fatalf("render %s: %v", idx, err)
		}
		if !strings.HasPrefix(stmt, "CREATE INDEX CONCURRENTLY IF NOT EXISTS "+idx) {
			t.Errorf("postgres %s: %q missing CONCURRENTLY prefix", idx, stmt)
		}
		if !strings.Contains(stmt, "logical_device_id, timestamp DESC") {
			t.Errorf("postgres %s: %q missing composite columns", idx, stmt)
		}
	}
}

func TestRenderIndexDDL_SQLite(t *testing.T) {
	for _, idx := range []string{IndexUnifiedLogicalTS, IndexDevDataLogicalTS} {
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
	for _, idx := range []string{IndexUnifiedLogicalTS, IndexDevDataLogicalTS} {
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

// 索引可用: 插入数据后按 (logical_device_id, timestamp DESC) 查询正常。
func TestEnsureLogicalDataIndexes_UsableForQuery(t *testing.T) {
	db := testutil.OpenTestDB(t)
	ctx := context.Background()
	if err := EnsureLogicalDataIndexes(ctx, db); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	living := seedBackfillScenario(t, db, "idx:0x90", 1, 0, 2)
	_ = living
	var ldID uint
	if err := db.Raw("SELECT logical_device_id FROM edge_devices WHERE id = ?", living[0].ID).
		Scan(&ldID).Error; err != nil {
		t.Fatal(err)
	}
	var rows int64
	if err := db.Raw(
		"SELECT COUNT(*) FROM unified_data WHERE logical_device_id = ?", ldID).
		Scan(&rows).Error; err != nil {
		t.Fatalf("query on indexed columns: %v", err)
	}
	if rows != 0 { // 回填前: 行尚 NULL, 0 是预期
		t.Errorf("unexpected pre-backfill rows: %d", rows)
	}
}
