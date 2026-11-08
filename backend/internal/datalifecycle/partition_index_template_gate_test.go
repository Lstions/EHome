package datalifecycle

// partition_index_template_gate_test.go — task-20 索引瘦身的两条门禁。
//
// ============ 背景：一条差点被当成"遗留垃圾"删掉的索引 ============
//
// 2026-11-07 生产索引盘点（pg_stat_all_indexes 40 小时窗口）给出：
//
//	device_data_device_id_timestamp_idx   225 MB   0 次扫描   从未使用
//
// 按"0 次扫描 + 不在 partitionParentIndexes 模板里" ⇒ 判定为遗留重复品、可删。
// **这个判定是错的**，本文件把它变成可执行的门禁，避免下一个人重新踩。
//
// 两件事叠在一起造成了误判：
//
//  1. 它的读路径是**低频**的 —— data-summary（删除确认弹窗 / 合并历史）只在用户
//     点删除时才打一次，40 小时 0 次完全正常，不代表"没有读路径"。
//  2. 更关键：ensurePartitionParentIndexes 判"索引是否已存在"用的是**列清单包含**
//     （strings.Contains(normalizeIndexColumns(def), "(device_id, timestamp)")），
//     **不是索引名**。模板里那条叫 device_data_device_ts_idx，但列一模一样。
//     ⇒ 只要老名字那条在，模板那条**永远不会被创建**。
//     ⇒ 那条 225 MB 的"遗留品"其实是 (device_id,timestamp) 模板的**事实实现**。
//
// 删掉它的实测代价（隔篱 PG bench，30 万行 device_data，构造"只在分区末尾有数据"
// 的新设备 —— data-summary 的最坏情形）：
//
//	保留 (device_id,timestamp) : Index Only Scan              0.074 ms      7 buffer
//	删掉                        : Index Scan on timestamp_idx  17.77 ms  2394 buffer
//	                              Rows Removed by Filter: 216000
//
// 扫描量与**分区总行数**成正比，不是 O(log n)。千万行分区上就是秒级弹窗。
//
// ============ 本文件守什么 ============
//
//  1. TestNoTwoTemplatesShareColumnList —— 列清单在同一张表内必须唯一。
//     存在性检查按列，两条模板列相同 ⇒ "建了一条就认为两条都在" ⇒ 静默少建一条。
//  2. TestSingleColumnDeviceIDIndexIsPrefixCovered —— (device_id) 单列索引必须是
//     某条活模板的**严格前缀**，否则删它就是删掉唯一能服务 device_id 等值查询的索引。
//  3. TestTemplateColumnListsAllServed（PG）—— 迁移完成后，**每一条模板的列清单**
//     都必须真的在库上有一条等价索引（名字可以不同）。这条直接钉住"名字/列脱钩"。

import (
	"fmt"
	"strings"
	"testing"
)

// templateColumnLists 返回某表模板的列清单，归一成与 partitionIndexColumns **同一形态**。
//
// ⚠ 本仓存在**两套归一**，比对时必须用对：
//   - ensurePartitionParentIndexes 走 strings.Contains(normalizeIndexColumns(def), want)
//     ⇒ want 保留 DESC（模板 "(device_id, created_at DESC)" 归一成 "... created_at desc"）；
//   - partitionIndexColumns（退役与门禁用）走 parseIndexDef ⇒ **剥掉 DESC/ASC**，
//     只取每列第一个 token（"(device_id, created_at DESC)" → "device_id, created_at"）。
//
// 两套归一没有对错之分（一个是"模板字符串包含"、一个是"列清单语义"），但混用会让
// 门禁报假红 —— 第一版就在这里踩过（device_data 的 device_created_idx 被误判为缺失）。
// 这里用后者，因为门禁要与**退役逻辑**看到同一份事实。
func templateColumnLists(table string) []string {
	tmpls := effectiveParentIndexTemplatesForTest(table)
	out := make([]string, 0, len(tmpls))
	for _, tmpl := range tmpls {
		stmt := fmt.Sprintf(tmpl, table, table)
		cols, ok := indexColumnsOf(stmt)
		if !ok {
			continue
		}
		out = append(out, normalizeIndexColumns(strings.Join(firstTokensOfColumns(cols), ", ")))
	}
	return out
}

// firstTokensOfColumns 与 parseIndexDef 的列切分保持一致：归一后按逗号切、每段取首 token。
func firstTokensOfColumns(cols string) []string {
	parts := strings.Split(normalizeIndexColumns(cols), ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		out = append(out, fields[0])
	}
	return out
}

// effectiveParentIndexTemplatesForTest 取"通用模板 + PG 增强模板按名替换"后的清单。
//
// 为什么不直接读 partitionParentIndexes[table]：PG 上 device_sensor_ts_idx 会被
// partitionParentIndexesPG 的同名增强版（多 INCLUDE (value)）**替换**。两份清单
// 的列清单相同、只有 INCLUDE 不同，所以对本文件的判据无影响 —— 但走同一函数
// 能保证"门禁看到的清单"与"启动时真正执行的清单"是同一份，不会各抄一套后漂移。
func effectiveParentIndexTemplatesForTest(table string) []string {
	base := partitionParentIndexes[table]
	pg := partitionParentIndexesPG[table]
	overridden := make(map[string]struct{}, len(pg))
	for _, tmpl := range pg {
		overridden[indexNameOf(fmt.Sprintf(tmpl, table, table))] = struct{}{}
	}
	out := make([]string, 0, len(base)+len(pg))
	for _, tmpl := range base {
		if _, skip := overridden[indexNameOf(fmt.Sprintf(tmpl, table, table))]; skip {
			continue
		}
		out = append(out, tmpl)
	}
	return append(out, pg...)
}

// TestNoTwoTemplatesShareColumnList 守「列清单在同一张表内唯一」。
//
// 为什么必须唯一：ensurePartitionParentIndexes 判存在靠
// strings.Contains(normalizeIndexColumns(def), "(<列清单>)")。两条模板列清单相同
// ⇒ 建出第一条之后，第二条永远判为"已存在"⇒ 静默少建一条。
// 而"哪一条被建出来了"取决于模板顺序 —— 这种依赖顺序的静默行为是最难查的。
func TestNoTwoTemplatesShareColumnList(t *testing.T) {
	for _, table := range []string{"unified_data", "device_data", "telemetry_frame", "telemetry_rollup_1m"} {
		if _, registered := partitionParentIndexes[table]; !registered {
			continue
		}
		seen := make(map[string]string, 8)
		for _, tmpl := range effectiveParentIndexTemplatesForTest(table) {
			stmt := fmt.Sprintf(tmpl, table, table)
			name := indexNameOf(stmt)
			cols, ok := indexColumnsOf(stmt)
			if !ok {
				t.Fatalf("%s: 无法从模板解析列清单: %s", table, stmt)
			}
			norm := normalizeIndexColumns(cols)
			if prev, dup := seen[norm]; dup {
				t.Errorf("%s: 模板 %s 与 %s 的列清单相同 (%s) —— "+
					"按列判存在会让后一条永远建不出来", table, name, prev, norm)
				continue
			}
			seen[norm] = name
		}
	}
}

// TestSingleColumnDeviceIDIndexIsPrefixCovered 守 task-20 删 (device_id) 单列索引的安全性。
//
// 删它是安全的**唯一理由**：(device_id) 是某条活模板的**严格前缀**，
// 于是任何 WHERE device_id = ? 都能由那条复合索引以同样的 index-only scan 完成。
//
// 这条用例把"唯一理由"变成断言：若日后有人把那些 (device_id, ...) 复合索引也删了，
// 单列索引已经不在了 —— 这个用例会立刻红，而不是等到线上某某查询变 seq scan。
//
// 实测（PG bench，1,276 万行 unified_data，device_id=14）：
//
//	COUNT(*) WHERE device_id=14
//	  有单列索引 : Parallel Index Only Scan on unified_data_device_id_idx           372.9 ms
//	  无单列索引 : Parallel Index Only Scan on unified_data_device_id_timestamp_idx 378.4 ms  (1.5%，噪声内)
//
// device_data 侧（30 万行 bench，三条查询 latest-data / 分页 / data-summary）：
//
//	删前：COUNT 5.52 ms 用 device_id_idx；删后：4.93 ms 用 device_id_timestamp_idx —— 反而更快
//	latest-data 与分页始终用 (device_id, created_at DESC)，删前删后计划完全相同
//	data-summary 始终用 (device_id, timestamp)，删前删后计划完全相同
func TestSingleColumnDeviceIDIndexIsPrefixCovered(t *testing.T) {
	for _, table := range []string{"unified_data", "device_data"} {
		cols := templateColumns(table)
		if _, still := cols["device_id"]; still {
			t.Errorf("%s: (device_id) 单列索引仍在模板里 —— task-20 要求删除（它被复合索引前缀覆盖）", table)
		}

		// 必须具体存在 (device_id, timestamp) —— **不能只要求"任意 device_id 前缀的复合索引"**。
		//
		// 为什么必须具体到 timestamp：data-summary 的查询是
		//   SELECT timestamp FROM <table> WHERE device_id = ? ORDER BY timestamp ASC/DESC LIMIT 1
		// (device_id, created_at DESC) 前缀确实是 device_id，但它**给不了 timestamp 有序**，
		// 规划器只能扫 device_id 全部行再排序，等价于退化。
		//
		// 这一条是变异验证发现的：第一版只断言 "strings.HasPrefix(norm, "device_id, ")"，
		// 于是把 device_ts_idx 从模板里删掉后，device_created_idx 仍然满足前缀断言 ⇒
		// 用例没有变红。**断言写松了就等于没写**。
		covered := false
		var candidates []string
		for _, norm := range templateColumnLists(table) {
			candidates = append(candidates, norm)
			if norm == "device_id, timestamp" || strings.HasPrefix(norm, "device_id, timestamp, ") {
				covered = true
			}
		}
		if !covered {
			t.Errorf("%s: 没有 (device_id, timestamp) 开头的复合索引模板 —— "+
				"删掉 (device_id) 单列索引后，data-summary 的 ORDER BY timestamp 边界查询"+
				"会退化为扫描该设备全部行。现有模板=%v", table, candidates)
		}
	}
}
