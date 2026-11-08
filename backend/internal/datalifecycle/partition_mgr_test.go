package datalifecycle

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// 单测（SQLite 环境）只验证纯逻辑: 命名/月份运算/方言 no-op/DDL 文本构造。
// PG 分支由集成测试覆盖（make test-integration）。

func newSQLiteDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.UnifiedData{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func TestPartitionName(t *testing.T) {
	cases := []struct {
		ts   time.Time
		want string
	}{
		{time.Date(2026, 8, 21, 15, 0, 0, 0, time.UTC), "unified_data_202608"},
		{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "unified_data_202601"},
		{time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC), "unified_data_202612"},
	}
	for _, c := range cases {
		if got := partitionName(partitionedTable, c.ts); got != c.want {
			t.Errorf("partitionName(%v) = %q, want %q", c.ts, got, c.want)
		}
	}
}

func TestMonthStartAndAddMonths(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 30, 0, 0, time.UTC)
	ms := monthStart(ts)
	if ms.Day() != 1 || ms.Hour() != 0 || ms.Month() != time.August {
		t.Errorf("monthStart = %v, want 2026-08-01T00:00Z", ms)
	}
	next := addMonths(ms, 1)
	if next.Month() != time.September || next.Day() != 1 {
		t.Errorf("addMonths(+1) = %v, want 2026-09-01", next)
	}
	// 跨年
	dec := monthStart(time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC))
	jan := addMonths(dec, 1)
	if jan.Year() != 2027 || jan.Month() != time.January {
		t.Errorf("cross-year addMonths = %v, want 2027-01-01", jan)
	}
}

// TestPartitionParentIndexesContract — 分区母表索引清单的契约门禁（SQLite 单测可跑）。
//
// 守四件事（2026-10-25 F-10）:
//  1. unified_data / device_data 都登记了 (device_id, timestamp) 复合索引 ——
//     data-summary 的 ORDER BY timestamp 边界查询依赖它；缺了就只能「扫时间序 + Filter
//     device_id」，对已停写的老设备代价随新数据量线性增长（见 partitionParentIndexes 注释）；
//  2. 同一张表的索引名互不重复 —— CREATE INDEX 按名字判重，重名会在建表/迁移时
//     duplicate index 报错；
//  3. 模板都能被 indexNameOf / indexColumnsOf 解析（建表路径与幂等补齐路径都依赖）；
//  4. PG 增强版（INCLUDE）必须是某个通用模板的**同名替换**且列清单一致 ——
//     否则会建出两条语义重复的索引（createPartitionedParent 的替换逻辑依赖这条）。
func TestPartitionParentIndexesContract(t *testing.T) {
	deviceTS := map[string]bool{"unified_data": false, "device_data": false}
	for table, templates := range partitionParentIndexes {
		seen := make(map[string]string, len(templates))
		for _, tmpl := range templates {
			stmt := fmt.Sprintf(tmpl, table, table)
			name := indexNameOf(stmt)
			if name == "" {
				t.Fatalf("%s: unparsable index template %q", table, tmpl)
			}
			if prev, dup := seen[name]; dup {
				t.Errorf("%s: duplicate index name %q (templates %q / %q)", table, name, prev, tmpl)
			}
			seen[name] = tmpl
			cols, ok := indexColumnsOf(stmt)
			if !ok {
				t.Fatalf("%s: cannot parse index columns from %q", table, tmpl)
			}
			if normalizeIndexColumns(cols) == "device_id, timestamp" {
				deviceTS[table] = true
			}
		}
		if ok, tracked := deviceTS[table]; tracked && !ok {
			t.Errorf("%s: missing (device_id, timestamp) index — data-summary 边界查询会退化为大范围扫描", table)
		}
	}

	for table, templates := range partitionParentIndexesPG {
		baseColumns := make(map[string]string, len(partitionParentIndexes[table]))
		for _, tmpl := range partitionParentIndexes[table] {
			stmt := fmt.Sprintf(tmpl, table, table)
			cols, ok := indexColumnsOf(stmt)
			if !ok {
				t.Fatalf("%s: cannot parse base index columns from %q", table, tmpl)
			}
			baseColumns[indexNameOf(stmt)] = normalizeIndexColumns(cols)
		}
		for _, tmpl := range templates {
			stmt := fmt.Sprintf(tmpl, table, table)
			name := indexNameOf(stmt)
			baseCols, ok := baseColumns[name]
			if !ok {
				t.Errorf("%s: PG-enhanced index %q has no same-name base template (替换逻辑会把它当追加索引)", table, name)
				continue
			}
			cols, _ := indexColumnsOf(stmt)
			if got := normalizeIndexColumns(cols); got != baseCols {
				t.Errorf("%s: PG-enhanced index %q columns = %q, base = %q (方言增强只能换 INCLUDE，不能改列)", table, name, got, baseCols)
			}
		}
	}
}

// TestEnsurePartitionParentIndexesNoopOnSQLite — 非 PG 方言必须 no-op：
// SQLite 测试库没有分区表，补齐逻辑不能对普通表执行 PG 专用 DDL。
func TestEnsurePartitionParentIndexesNoopOnSQLite(t *testing.T) {
	db := newSQLiteDB(t)
	if err := ensurePartitionParentIndexes(db, "unified_data"); err != nil {
		t.Fatalf("ensurePartitionParentIndexes on sqlite should be no-op, got err=%v", err)
	}
}

func TestNoopOnNonPostgres(t *testing.T) {
	db := newSQLiteDB(t)
	pm := NewPartitionManager(db)
	if err := pm.EnsurePartitions(3); err != nil {
		t.Fatalf("EnsurePartitions on sqlite should be no-op, got err=%v", err)
	}
	if dropped, err := pm.DropPartitionsBefore(time.Now()); err != nil || dropped != nil {
		t.Errorf("DropPartitionsBefore on sqlite should be no-op, got %v %v", dropped, err)
	}
	if IsUnifiedDataPartitioned(db) {
		t.Error("IsUnifiedDataPartitioned must be false on sqlite")
	}
	if err := MigrateUnifiedDataToPartitioned(db); err != nil {
		t.Errorf("MigrateUnifiedDataToPartitioned on sqlite should be no-op, got %v", err)
	}
}

// TestGenericPartitionAPIs_NoopOnNonPostgres covers the table-parameterized
// (*For / IsTablePartitioned / MigrateTableToPartitioned) surface on SQLite.
func TestGenericPartitionAPIs_NoopOnNonPostgres(t *testing.T) {
	db := newSQLiteDB(t)
	pm := NewPartitionManager(db)
	if err := pm.EnsurePartitionsFor("device_data", 3); err != nil {
		t.Fatalf("EnsurePartitionsFor on sqlite should be no-op, got err=%v", err)
	}
	if dropped, err := pm.DropPartitionsBeforeFor("device_data", time.Now()); err != nil || dropped != nil {
		t.Errorf("DropPartitionsBeforeFor on sqlite should be no-op, got %v %v", dropped, err)
	}
	if IsTablePartitioned(db, "device_data") {
		t.Error("IsTablePartitioned must be false on sqlite")
	}
	if err := MigrateTableToPartitioned(db, "device_data", "device_data_legacy"); err != nil {
		t.Errorf("MigrateTableToPartitioned on sqlite should be no-op, got %v", err)
	}
}

func TestPartitionDDLSyntax(t *testing.T) {
	// 验证 DDL 模板的关键片段（防回归: 分区命名/RANGE 边界格式）。
	start := monthStart(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	end := addMonths(start, 1)
	ddl := fmt.Sprintf(
		"CREATE TABLE %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')",
		partitionName(partitionedTable, start), partitionedTable,
		start.Format("2006-01-02 15:04:05"), end.Format("2006-01-02 15:04:05"),
	)
	for _, want := range []string{
		"CREATE TABLE unified_data_202608 PARTITION OF unified_data",
		"FOR VALUES FROM ('2026-08-01 00:00:00') TO ('2026-09-01 00:00:00')",
	} {
		if !strings.Contains(ddl, want) {
			t.Errorf("DDL missing %q in:\n%s", want, ddl)
		}
	}
}
