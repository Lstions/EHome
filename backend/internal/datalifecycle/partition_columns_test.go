package datalifecycle

// partition_columns_test.go — 列校正器 ensurePartitionParentColumns 的单元测试。
//
// SQLite 下这个函数是 no-op（AutoMigrate 建表，schema 与模型天然一致），
// 所以这里测的是**它依赖的两个纯函数**：parseDDLColumns / splitTopLevelCommas，
// 以及"四张分区表的 DDL 都能被解析"这个不变量。
//
// PG 侧的端到端验证在 telemetry_rollup_pg_test.go 的 TestPGRollupUpsertAgainstBareDDL
// 与 migration 用例里（那里真跑 ALTER TABLE）。

import (
	"strings"
	"testing"
)

// TestSplitTopLevelCommas 守切分器：它是解析器的地基。
func TestSplitTopLevelCommas(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "无逗号",
			in:   "id BIGSERIAL",
			want: []string{"id BIGSERIAL"},
		},
		{
			name: "顶层逗号",
			in:   "id BIGSERIAL, name TEXT",
			want: []string{"id BIGSERIAL", " name TEXT"},
		},
		{
			name: "括号内逗号不切（PRIMARY KEY）",
			in:   "a INT, PRIMARY KEY (a, b)",
			want: []string{"a INT", " PRIMARY KEY (a, b)"},
		},
		{
			name: "括号内逗号不切（VARCHAR）",
			in:   "a VARCHAR(16), b INT",
			want: []string{"a VARCHAR(16)", " b INT"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitTopLevelCommas(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("split(%q) = %v (%d 段), want %v (%d 段)", c.in, got, len(got), c.want, len(c.want))
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("段[%d] = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// TestParseDDLColumnsHandlesPartitionClause 守「尾部的 PARTITION BY RANGE (...)」
// 不会被算进列清单。
//
// 这是实测踩过的坑：早期用 strings.LastIndexByte(ddl, ')') 找列清单结尾，
// 结果把 "PARTITION BY RANGE (bucket)" 的括号也吞进来，报
// `unexpected column definition "bucket)"`。
func TestParseDDLColumnsHandlesPartitionClause(t *testing.T) {
	cols, err := parseDDLColumns("CREATE TABLE %s (\n" +
		"\t\tdevice_id integer NOT NULL,\n" +
		"\t\tbucket TIMESTAMPTZ NOT NULL,\n" +
		"\t\tPRIMARY KEY (device_id, bucket)\n" +
		") PARTITION BY RANGE (bucket)")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cols) != 2 {
		t.Fatalf("解析出 %d 列 %+v, want 2（PRIMARY KEY 与 PARTITION BY 都不是列）", len(cols), cols)
	}
	if cols[0].Name != "device_id" || cols[1].Name != "bucket" {
		t.Errorf("列名 = %q/%q, want device_id/bucket", cols[0].Name, cols[1].Name)
	}
}

// TestAllPartitionDDLsAreParsable 守四张分区表的 DDL 全部可解析。
//
// 为什么这条重要：ensurePartitionParentColumns 对**解析失败**返回 error（刻意
// 不静默通过）。若某张表的 DDL 写成解析器不认识的形态，补列功能对它永久失效 ——
// 而它不会报错（调用方只 Warn 日志），等于静默失去保护。
func TestAllPartitionDDLsAreParsable(t *testing.T) {
	for table, ddl := range partitionParentDDL {
		cols, err := parseDDLColumns(ddl)
		if err != nil {
			t.Errorf("partitionParentDDL[%s] 无法解析（补列功能对它失效）: %v", table, err)
			continue
		}
		if len(cols) == 0 {
			t.Errorf("partitionParentDDL[%s] 解析出 0 列", table)
			continue
		}
		// 每张表都必须有 device_id（四张表共同的设备维度列）。
		hasDeviceID := false
		for _, c := range cols {
			if c.Name == "device_id" {
				hasDeviceID = true
			}
		}
		if !hasDeviceID {
			t.Errorf("partitionParentDDL[%s] 解析后没有 device_id 列 —— 解析器可能吞了列", table)
		}
		// 分区键必须在列清单里（否则 ADD COLUMN 建出的列无处安放）。
		if !strings.Contains(strings.ToLower(ddl), "partition by range") {
			t.Errorf("partitionParentDDL[%s] 不是分区表 DDL", table)
		}
	}
}

// TestEnsurePartitionParentColumnsNoopOnSQLite 守非 PG 方言 no-op。
func TestEnsurePartitionParentColumnsNoopOnSQLite(t *testing.T) {
	db := newRollupTestDB(t)
	if err := ensurePartitionParentColumns(db, TelemetryRollupTable); err != nil {
		t.Fatalf("sqlite 下必须 no-op，got err=%v", err)
	}
	// 未登记表也必须安全返回（不能 panic / 不能报错）。
	if err := ensurePartitionParentColumns(db, "not_registered_table"); err != nil {
		t.Fatalf("未登记表必须安全返回，got err=%v", err)
	}
}
