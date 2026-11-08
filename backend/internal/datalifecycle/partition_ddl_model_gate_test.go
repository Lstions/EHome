package datalifecycle

// partition_ddl_model_gate_test.go — 分区母表 DDL 与 GORM model 的**列一致性**门禁。
//
// ============ 为什么需要这条门禁（2026-11-07 生产事故）============
//
// 生产 L3 rollup 每分钟报：
//
//	ERROR: column "updated_at" of relation "telemetry_rollup_1m" does not exist (SQLSTATE 42703)
//
// rollup 表 0 行 —— L3 等于完全没工作。根因：
//   · models.TelemetryRollup1m 有 UpdatedAt（AutoMigrate 会建该列）；
//   · partitionParentDDL[telemetry_rollup_1m] 的裸 DDL **没有**该列（PG 生产走这条）。
//
// **为什么所有单测都是绿的**：SQLite 用例走 AutoMigrate ⇒ 模型字段一定被建出；
// 而生产走裸 DDL。两条建表路径各自"自洽"，交叉点上没人验。
//
// ============ ⚠ 这条门禁不是给 rollup 单点打的补丁 ============
//
// 本仓有**四张**分区母表，每张都有"裸 DDL"和"GORM model"两份列定义：
//
//	unified_data           ↔ models.UnifiedData
//	device_data            ↔ models.DeviceData
//	telemetry_rollup_1m    ↔ models.TelemetryRollup1m
//	telemetry_frame        ↔ models.TelemetryFrame
//
// 这四对里**任何一对**发生列漂移，表现都完全相同且极难发现：
//   · 生产 PG 上写入/读取报 "column ... does not exist"，功能整段失效；
//   · 而 SQLite 单测全绿（AutoMigrate 从 model 反向建表，永远与 model 一致）。
//
// 也就是说：**这四张表里每一张都可能以同样方式炸**。今天炸的是 rollup，
// 明天给 telemetry_frame 加一列（比如 retry_count）而忘了改 DDL，就会在帧表上
// 重演同一场事故 —— 而帧表是 L2 主写入路径，后果更严重（每一帧都写不进去）。
//
// 因此门禁是**遍历全部四张表**的双向断言，而不是只盯 rollup。
//
// ============ 为什么是源码门禁而不是 PG 用例 ============
//
// PG 用例需要 EHOME_TEST_DB=postgres，绝大多数环境（含 CI 默认）会 Skip ——
// 那等于没有门禁。本用例只读源码 + 反射 model tag，**无任何环境依赖**，
// 常规 go test ./... 就会执行。真正的 PG 建表验证另有用例
// （telemetry_rollup_pg_test.go / telemetry_frame_pg_test.go），两者分工：
//   · 本门禁：列清单是否同步（开发期立刻拦住，不用等生产）；
//   · PG 用例：DDL 是否真能建出符合预期的表（含类型/主键/分区键）。
//
// 另外还有一道运行期兜底：ensurePartitionParentColumns（partition_columns.go）
// 会在启动时给**存量库**补缺失列。三者分工：门禁挡新漂移、PG 用例验建表、
// 校正器救存量库。

import (
	"reflect"
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

// partitionDDLModelPairs 是「分区母表 DDL ↔ GORM model」的全量对照表。
//
// 新增任何分区表时**必须**在这里登记，否则
// TestPartitionDDLModelPairsCoversAllRegisteredTables 会红
// （它检查 partitionParentDDL 的每个键都在本表里）。
var partitionDDLModelPairs = []struct {
	table string
	model interface{}
}{
	{"unified_data", &models.UnifiedData{}},
	{"device_data", &models.DeviceData{}},
	{"telemetry_rollup_1m", &models.TelemetryRollup1m{}},
	{"telemetry_frame", &models.TelemetryFrame{}},
}

// modelColumnNames 反射读出 model 的列名集合（小写）。
//
// 优先用 gorm 的 column tag；无 tag 时按 GORM 默认命名（snake_case）推导。
func modelColumnNames(m interface{}) map[string]bool {
	rt := reflect.TypeOf(m)
	if rt.Kind() == reflect.Ptr {
		rt = rt.Elem()
	}
	out := make(map[string]bool, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("gorm")
		if strings.Contains(tag, "column:") {
			name := tag[strings.Index(tag, "column:")+len("column:"):]
			if j := strings.IndexAny(name, ";"); j >= 0 {
				name = name[:j]
			}
			if name != "" {
				out[strings.ToLower(name)] = true
				continue
			}
		}
		out[gormDefaultColumnName(f.Name)] = true
	}
	return out
}

// gormDefaultColumnName 复刻 GORM 的默认列名转换（CamelCase → snake_case）。
//
// 只覆盖本仓 models 的命名形态（HTTPCode → http_code, DeviceID → device_id）。
// 刻意不引 gorm.io/gorm/schema 的 NamingStrategy：那会把被测对象的默认值也拉进来，
// 万一将来改了全局 NamingStrategy，门禁会跟着"自动适应"而失去鉴别力。
func gormDefaultColumnName(field string) string {
	// 必须处理**连续大写**（缩写）：DeviceID → device_id（不是 device_i_d），
	// DataJSON → data_json（不是 data_j_s_o_n）。实测踩过：简单地在每个大写字母前
	// 插下划线会把 DeviceID 变成 device_i_d，于是门禁对 unified_data/device_data
	// 报一堆假红 —— 那比没有门禁更糟，因为维护者会去改 model 迎合错误的门禁。
	//
	// 规则（GORM NamingStrategy 的实际行为）：
	//   · 大写字母前插下划线，当且仅当「前一个字符是小写/数字」
	//     或「前一个是大写、下一个是小写」（后者是缩写结尾，如 DataJSON 的 J）；
	//   · 首字符不插。
	runes := []rune(field)
	var b strings.Builder
	for i, r := range runes {
		isUpper := r >= 65 && r <= 90
		if !isUpper {
			b.WriteRune(r)
			continue
		}
		if i > 0 {
			prevUpper := runes[i-1] >= 65 && runes[i-1] <= 90
			nextLower := i+1 < len(runes) && runes[i+1] >= 97 && runes[i+1] <= 122
			if !prevUpper || nextLower {
				b.WriteByte(95)
			}
		}
		b.WriteRune(r - 65 + 97)
	}
	return b.String()
}

// TestPartitionDDLMatchesModels 是核心门禁：双向断言 DDL 与 model 的列集合一致。
//
// 两个方向都查，因为它们对应不同的故障：
//
//	· model 有、DDL 没有 ⇒ 生产写入/读取报 column does not exist（**本次事故**）；
//	· DDL 有、model 没有 ⇒ 该列永远为 NULL 或走默认值，读回来对不上（无声的错）。
func TestPartitionDDLMatchesModels(t *testing.T) {
	for _, pair := range partitionDDLModelPairs {
		t.Run(pair.table, func(t *testing.T) {
			ddl, ok := partitionParentDDL[pair.table]
			if !ok {
				t.Fatalf("partitionParentDDL 未登记 %q —— 没有建表路径", pair.table)
			}
			ddlCols, err := parseDDLColumns(ddl)
			if err != nil {
				t.Fatalf("解析 partitionParentDDL[%s]: %v", pair.table, err)
			}
			if len(ddlCols) == 0 {
				t.Fatalf("partitionParentDDL[%s] 解析出 0 列", pair.table)
			}
			ddlSet := make(map[string]bool, len(ddlCols))
			for _, c := range ddlCols {
				ddlSet[c.Name] = true
			}
			modelSet := modelColumnNames(pair.model)

			var missingInDDL, missingInModel []string
			for name := range modelSet {
				if !ddlSet[name] {
					missingInDDL = append(missingInDDL, name)
				}
			}
			for name := range ddlSet {
				if !modelSet[name] {
					missingInModel = append(missingInModel, name)
				}
			}
			if len(missingInDDL) > 0 {
				t.Errorf("model 有而 partitionParentDDL[%s] 没有的列: %v\n"+
					"这是 2026-11-07 生产事故的形状：SQLite 走 AutoMigrate（有该列）⇒ 单测全绿，\n"+
					"PG 走裸 DDL（没该列）⇒ 生产报 column does not exist、功能整段失效。\n"+
					"修复：把该列补进 partitionParentDDL[%s]（存量库由 ensurePartitionParentColumns 自动补列）。",
					pair.table, missingInDDL, pair.table)
			}
			if len(missingInModel) > 0 {
				t.Errorf("partitionParentDDL[%s] 有而 model 没有的列: %v\n"+
					"该列在生产上永远为 NULL 或走 DDL 默认值，读回来与 model 对不上（无声的错）。\n"+
					"修复：要么从 DDL 删掉，要么在 model 里补上对应字段。",
					pair.table, missingInModel)
			}
		})
	}
}

// TestPartitionDDLModelPairsCoversAllRegisteredTables 守对照表本身的完整性：
// partitionParentDDL 里的每张表都必须在门禁的对照表里登记。
//
// 没有这条，加一张新分区表时"忘了往门禁里加一行"会让新表**静默豁免**——
// 而恰恰是新表最容易列漂移（还没经过任何生产验证）。
func TestPartitionDDLModelPairsCoversAllRegisteredTables(t *testing.T) {
	covered := make(map[string]bool, len(partitionDDLModelPairs))
	for _, p := range partitionDDLModelPairs {
		covered[p.table] = true
	}
	for table := range partitionParentDDL {
		if !covered[table] {
			t.Errorf("partitionParentDDL 登记了 %q，但 partitionDDLModelPairs 没有对应的 model 对照"+
				"（该表会静默豁免列一致性检查）", table)
		}
	}
}

// TestParseDDLColumns 守解析器自身：它是门禁的判据来源，解析错了门禁就是摆设。
func TestParseDDLColumns(t *testing.T) {
	cols, err := parseDDLColumns("CREATE TABLE %s (\n" +
		"\t\tid BIGSERIAL,\n" +
		"\t\tdevice_id BIGINT NOT NULL,\n" +
		"\t\tunit VARCHAR(16),\n" +
		"\t\tPRIMARY KEY (id, timestamp)\n" +
		") PARTITION BY RANGE (timestamp)")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := make(map[string]string, len(cols))
	for _, c := range cols {
		got[c.Name] = c.Def
	}
	if len(got) != 3 {
		t.Fatalf("解析出 %d 列 %v, want 3（PRIMARY KEY 是表级约束，不应算列）", len(got), got)
	}
	if !strings.Contains(got["unit"], "VARCHAR(16)") {
		t.Errorf("unit 类型 = %q, want 含 VARCHAR(16)", got["unit"])
	}
	if !strings.Contains(got["device_id"], "NOT NULL") {
		t.Errorf("device_id 定义 = %q, 丢了 NOT NULL（ADD COLUMN 会建出可空列）", got["device_id"])
	}
	if _, err := parseDDLColumns("not a create table"); err == nil {
		t.Error("非 CREATE TABLE 文本必须报错（静默通过会让门禁变成安全假象）")
	}
}
