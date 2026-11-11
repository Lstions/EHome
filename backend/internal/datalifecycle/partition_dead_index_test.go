package datalifecycle

// partition_dead_index_test.go — 阶段0「死索引退役」的契约与解析单测
// （设计/遥测存储优化方案-2026-11-06.md §7 阶段0 / §1.3 第 2 条）。
//
// SQLite 单测只能覆盖**纯逻辑**（模板清单、pg_get_indexdef 解析、列清单比对）；
// 真实 DROP INDEX 需要 PG，由 partition_mgr_pg_test.go 一族的集成测试覆盖
// （make test-integration / EHOME_TEST_DB=postgres）。
//
// 本文件守四件事:
//  1. 4 条死索引**不得**再出现在 partitionParentIndexes 模板里 —— 否则下次建库/
//     建分区又会长回来（方案 §7 阶段0 的 ⚠ 明确点出这一点）；
//  2. 读路径仍在用的索引（device_id / timestamp / device_sensor_ts / device_ts /
//     device_created）**必须**在模板里 —— 删死索引绝不能顺手删掉活的；
//  3. 死索引清单与模板清单**不相交** —— 否则"先补建再删"会在每次启动之间来回
//     建/删同一条索引；
//  4. pg_get_indexdef 的解析（indexdef → 名字 + 列清单）与列清单比对逻辑正确 ——
//     判据是「列清单完全相等」，不是索引名（历史 _new_ 前缀导致按名匹配会漏）。

import (
	"fmt"
	"testing"
)

// templateColumns 返回某表模板里出现过的小写列清单集合（元素是 normalize 后的
// 列清单字符串，例如 "device_id, sensor_name, timestamp"）。
func templateColumns(table string) map[string]struct{} {
	out := make(map[string]struct{}, len(partitionParentIndexes[table]))
	for _, tmpl := range partitionParentIndexes[table] {
		stmt := fmt.Sprintf(tmpl, table, table)
		if cols, ok := indexColumnsOf(stmt); ok {
			out[normalizeIndexColumns(cols)] = struct{}{}
		}
	}
	return out
}

// TestDeadIndexTemplatesRemoved 守方案 §7 阶段0：4 条死索引不再被建回来。
func TestDeadIndexTemplatesRemoved(t *testing.T) {
	// 每条: 表 + 列清单（与 deadTimeseriesIndexes 同口径）。
	want := []struct {
		table string
		cols  string
		why   string
	}{
		{"unified_data", "sensor_name", "无读路径：所有 sensor_name 查询都以 device_id 为前导列，复合索引已覆盖"},
		{"unified_data", "edge_device_id", "该列生产上全 NULL，索引对任何查询都无选择性"},
		{"unified_data", "device_id", "task-20：被 (device_id, sensor_name, timestamp) / (device_id, timestamp) 前缀完全覆盖，生产 773 MB"},
		// 2026-11-11: device_data 的三条死索引条目已删除 —— 该表整表退役，没有模板可删。
	}
	for _, c := range want {
		cols := templateColumns(c.table)
		if _, still := cols[c.cols]; still {
			t.Errorf("%s 的索引模板里仍有 (%s) —— 方案 §7 阶段0 要求删除（理由: %s）",
				c.table, c.cols, c.why)
		}
	}
}

// TestLiveIndexTemplatesKept 守"删死索引不能顺手删活索引"。
//
// 每一条都在下面的读路径里被用到（行号为 2026-11-07 的代码事实）:
//   - (timestamp):                    /overview data_count_today、retention 范围扫描
//   - (device_id, sensor_name, timestamp): 历史图表 / historical-batch / history_points
//   - (device_id, timestamp):         /:id/data-summary 的 ORDER BY timestamp 边界查询
//   - (device_id, created_at DESC):   /:id/latest-data、/:id/data 分页
//
// ⚠ 2026-11-07 (task-20)：(device_id) 单列索引**已移出本清单**（改为必须删除）。
// 它不是"没有读路径"—— /unified-data/categories 与 /edge-devices/:id/data 确实按
// device_id 等值过滤；但它被上面任意一条 (device_id, ...) 复合索引**严格前缀覆盖**，
// 单独一条只多一份写入成本。EXPLAIN 前后对比见
// TestSingleColumnDeviceIDIndexIsPrefixCovered（本文件下半部分）。
func TestLiveIndexTemplatesKept(t *testing.T) {
	cases := []struct {
		table string
		cols  string
	}{
		{"unified_data", "timestamp"},
		{"unified_data", "device_id, sensor_name, timestamp"},
		{"unified_data", "device_id, timestamp"},
		// 2026-11-11: device_data 的三条活索引条目已删除（表整表退役）。
		// ⚠ 这些索引在**统一数据表**上的等价物仍在（见上面 unified_data 三条），
		// 所以"读路径有没有索引"这个语义没有被削弱。
	}
	for _, c := range cases {
		cols := templateColumns(c.table)
		if _, ok := cols[c.cols]; !ok {
			t.Errorf("%s 缺少 (%s) 的索引模板 —— 该列在既有读路径上，不能删", c.table, c.cols)
		}
	}
}

// TestDeadIndexListDisjointFromTemplates 守「先补建再删」不会自相打架：
// 死索引清单与模板清单必须**不相交**。
func TestDeadIndexListDisjointFromTemplates(t *testing.T) {
	for table, wants := range deadTimeseriesIndexes {
		live := templateColumns(table)
		for _, want := range wants {
			norm := normalizeIndexColumns(joinComma(want))
			if _, clash := live[norm]; clash {
				t.Errorf("%s: 死索引清单里的 (%s) 仍出现在建索引模板里 —— "+
					"finalizePartitionParentIndexes 会先建后删，每次启动来回抖动", table, norm)
			}
		}
	}
}

// joinComma 用 ", " 连接列名（与 normalizeIndexColumns 的归一格式对齐）。
func joinComma(cols []string) string {
	out := ""
	for i, c := range cols {
		if i > 0 {
			out += ", "
		}
		out += c
	}
	return out
}

// TestParseIndexDef 守 pg_get_indexdef 的解析（判据按列清单，不按索引名）。
func TestParseIndexDef(t *testing.T) {
	cases := []struct {
		def     string
		name    string
		columns []string
		ok      bool
	}{
		// 普通单列索引（生产真实形态）。
		{
			def:     "CREATE INDEX unified_data_sensor_name_idx ON public.unified_data USING btree (sensor_name)",
			name:    "unified_data_sensor_name_idx",
			columns: []string{"sensor_name"},
			ok:      true,
		},
		// 历史迁移路径: 名字带 _new_ 前缀（PG RENAME 不改索引名）—— 按名匹配会漏。
		{
			def:     "CREATE INDEX unified_data_new_node_id_idx ON public.unified_data USING btree (node_id)",
			name:    "unified_data_new_node_id_idx",
			columns: []string{"node_id"},
			ok:      true,
		},
		// 复合索引 + DESC 排序: 列清单必须取自第一组括号，不能被 DESC 干扰。
		{
			def:     "CREATE INDEX device_data_device_created_idx ON public.device_data USING btree (device_id, created_at DESC)",
			name:    "device_data_device_created_idx",
			columns: []string{"device_id", "created_at"},
			ok:      true,
		},
		// INCLUDE 包含列不属于索引列: 第一组括号规则天然把它排除。
		{
			def:     "CREATE INDEX unified_data_device_sensor_ts_idx ON public.unified_data USING btree (device_id, sensor_name, timestamp) INCLUDE (value)",
			name:    "unified_data_device_sensor_ts_idx",
			columns: []string{"device_id", "sensor_name", "timestamp"},
			ok:      true,
		},
		// 唯一索引 / 主键索引: 承载的是**约束**，不是查询加速路径 ⇒ 一律不参与退役。
		// 删掉唯一索引会让重复数据静默变得可插入（data_sources 的 uniq_ds_source
		// 踩过同类事故），因此解析层直接把它们排除，而不是靠"清单里别写错"。
		{def: "CREATE UNIQUE INDEX uniq_ds_source ON public.data_sources USING btree (edge_device_id, category)", ok: false},
		{def: "CREATE UNIQUE INDEX unified_data_pkey ON public.unified_data USING btree (id, timestamp)", ok: false},
		{def: "ALTER TABLE ONLY public.unified_data ADD CONSTRAINT x PRIMARY KEY (id)", ok: false},
		{def: "", ok: false},
	}
	for _, c := range cases {
		name, cols, ok := parseIndexDef(c.def)
		if ok != c.ok {
			t.Errorf("parseIndexDef(%q) ok = %v, want %v", c.def, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if name != c.name {
			t.Errorf("parseIndexDef(%q) name = %q, want %q", c.def, name, c.name)
		}
		if len(cols) != len(c.columns) {
			t.Errorf("parseIndexDef(%q) columns = %v, want %v", c.def, cols, c.columns)
			continue
		}
		for i := range cols {
			if cols[i] != c.columns[i] {
				t.Errorf("parseIndexDef(%q) columns[%d] = %q, want %q", c.def, i, cols[i], c.columns[i])
			}
		}
	}
}

// TestMatchesAnyColumnList 守「列清单完全相等」判据: 前缀不算命中。
//
// 这条是死索引退役的安全边界: (device_id) 是 (device_id, timestamp) 的前缀，
// 但绝不能被当成命中 —— 否则会把仍在读路径上的索引删掉。
func TestMatchesAnyColumnList(t *testing.T) {
	wants := [][]string{{"sensor_name"}, {"node_id"}}
	cases := []struct {
		columns []string
		want    bool
		why     string
	}{
		{[]string{"sensor_name"}, true, "完全相等"},
		{[]string{"node_id"}, true, "完全相等"},
		{[]string{"device_id", "sensor_name", "timestamp"}, false, "复合索引 != 单列死索引"},
		{[]string{"device_id", "timestamp"}, false, "device_id 前缀不算命中"},
		{[]string{"device_id"}, false, "活索引绝不能被判成死索引"},
		{[]string{"edge_device_id"}, false, "不在清单里"},
		{[]string{"sensor_name", "device_id"}, false, "顺序不同不算相等"},
		{nil, false, "空清单"},
	}
	for _, c := range cases {
		if got := matchesAnyColumnList(c.columns, wants); got != c.want {
			t.Errorf("matchesAnyColumnList(%v) = %v, want %v（%s）", c.columns, got, c.want, c.why)
		}
	}
}

// TestRetireDeadTimeseriesIndexesNoopOnSQLite 非 PG 方言必须 no-op:
// SQLite 测试库没有分区表，也绝不能对普通表执行 PG 专用目录查询。
func TestRetireDeadTimeseriesIndexesNoopOnSQLite(t *testing.T) {
	db := newSQLiteDB(t)
	for table := range deadTimeseriesIndexes {
		if err := retireDeadTimeseriesIndexes(db, table); err != nil {
			t.Errorf("retireDeadTimeseriesIndexes(%q) on sqlite = %v, want nil", table, err)
		}
	}
	// 未登记表同样 no-op（不查目录、不报错）。
	if err := retireDeadTimeseriesIndexes(db, "not_registered_table"); err != nil {
		t.Errorf("unregistered table = %v, want nil", err)
	}
}
