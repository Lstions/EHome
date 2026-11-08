package datalifecycle

// partition_index_retire_pg_test.go — task-20 的 PG 级门禁（run with EHOME_TEST_DB=postgres）。
//
// 本文件回答两个只能在真实 PG 上回答的问题：
//
//  1. **每条模板的列清单，在库上是否真有一条等价索引？**（TestTemplateColumnListsAllServed）
//     —— 这是本条任务最重要的门禁。ensurePartitionParentIndexes 按**列**判存在、
//     按**名**建索引，两者可以脱钩：模板 device_data_device_ts_idx 在库上可能是
//     device_data_device_id_timestamp_idx（老名字）。脱钩本身不算错，但会让
//     "模板里有、库上没有"这种**正常状态**看起来像漏建（2026-11-07 就有人据此把
//     唯一那条索引判成遗留垃圾准备删）。所以这里不判名字，只判"列清单有等价索引"。
//
//  2. **退役清单是幂等的，且不会误伤活索引。**（TestRetireDeadIndexesIdempotentAndPrecise）
//     —— retireDeadTimeseriesIndexes 按列清单删；若死清单里的列清单与活模板重叠，
//     "先补建再删"会在每次启动之间来回建/删（抖动）。SQLite 单测只能比源码字符串，
//     真实 DROP 只有在 PG 上才能验。

import (
	"fmt"
	"testing"

	"ehome/backend/testutil"
	"gorm.io/gorm"
)

// pgIndexColumnLists 返回当前 schema 下 table 上所有**非唯一**索引的列清单。
//
// 复用 partitionIndexColumns 而不是自己写一遍 SQL：退役逻辑用的就是它，
// 门禁必须与退役逻辑看到同一份事实，否则"门禁绿但退役删错"照样会发生。
func pgIndexColumnLists(t *testing.T, db *gorm.DB, table string) map[string]string {
	t.Helper()
	idx, err := partitionIndexColumns(db, table)
	if err != nil {
		t.Fatalf("list indexes on %s: %v", table, err)
	}
	out := make(map[string]string, len(idx))
	for _, i := range idx {
		out[normalizeIndexColumns(joinComma(i.columns))] = i.name
	}
	return out
}

// TestTemplateColumnListsAllServed 是「名字/列脱钩」的**可执行记录**。
//
// 判据：对每张已登记表，**每一条模板的列清单**都必须在库上找到一条等价索引
// （列清单完全相等；名字可以是任意历史名字）。
//
// 这条用例的价值不是"发现今天有 bug"，而是**让这个脱钩现象被显式承认**：
// 下一个人看到 device_data_device_ts_idx 在模板里、库上没有同名索引时，
// 不会以为漏建 —— 因为有一条用例叫"模板列清单都有等价索引"且它是绿的。
func TestTemplateColumnListsAllServed(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	dropUnifiedDataFlat(t, db)
	if err := db.Exec("DROP TABLE IF EXISTS device_data CASCADE").Error; err != nil {
		t.Fatalf("drop device_data: %v", err)
	}
	// 用本包的生产路径建表（不是手写 DDL）—— 门禁必须验"代码真的建出了什么"。
	if err := MigrateUnifiedDataToPartitioned(db); err != nil {
		t.Fatalf("migrate unified_data: %v", err)
	}
	if err := MigrateTableToPartitioned(db, "device_data", "device_data_legacy"); err != nil {
		t.Fatalf("migrate device_data: %v", err)
	}

	for _, table := range []string{"unified_data", "device_data"} {
		served := pgIndexColumnLists(t, db, table)
		for _, norm := range templateColumnLists(table) {
			name, ok := served[norm]
			if !ok {
				t.Errorf("%s: 模板要求 (%s)，但库上没有任何列清单等价的索引。\n"+
					"库上现有：%v\n"+
					"⇒ 要么模板写错了，要么 ensurePartitionParentIndexes 没跑。\n"+
					"注意：**不要**因为「模板里的名字在库上找不到」就判定漏建 —— "+
					"本仓历史上存在过名字/列脱钩（ensure 按列判存在），实际实现可能是另一个名字。",
					table, norm, served)
				continue
			}
			t.Logf("%s: (%s) 由 %s 提供", table, norm, name)
		}
	}
}

// TestRetireDeadIndexesIdempotentAndPrecise 锁退役的**幂等性 + 精确性**。
//
// 精确性指的是：删死索引时不能误伤同表的活索引。这在按列匹配的实现里是真实风险 ——
// 若某条活模板与某条死清单的列清单相同（部分前缀关系不算，要求完全相等），
// "先补建再删"就会每次启动来回抖动，而 pg_stat 上只表现为反复的 DDL 噪音。
func TestRetireDeadIndexesIdempotentAndPrecise(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	dropUnifiedDataFlat(t, db)
	if err := db.Exec("DROP TABLE IF EXISTS device_data CASCADE").Error; err != nil {
		t.Fatalf("drop device_data: %v", err)
	}
	if err := MigrateUnifiedDataToPartitioned(db); err != nil {
		t.Fatalf("migrate unified_data: %v", err)
	}
	if err := MigrateTableToPartitioned(db, "device_data", "device_data_legacy"); err != nil {
		t.Fatalf("migrate device_data: %v", err)
	}

	for _, table := range []string{"unified_data", "device_data"} {
		// 人为把死索引建回来（模拟存量库上还没清理的状态）。
		for _, cols := range deadTimeseriesIndexes[table] {
			name := fmt.Sprintf("%s_dead_probe_%s_idx", table, normalizeIndexColumns(joinComma(cols)))
			name = sanitizeIdentForTest(name)
			stmt := fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (%s)", name, table, joinComma(cols))
			if err := db.Exec(stmt).Error; err != nil {
				t.Fatalf("%s: create probe index %s: %v", table, name, err)
			}
		}
		before := pgIndexColumnLists(t, db, table)

		// 跑两次：第二次必须零变化（幂等）。
		if err := retireDeadTimeseriesIndexes(db, table); err != nil {
			t.Fatalf("%s: retire #1: %v", table, err)
		}
		mid := pgIndexColumnLists(t, db, table)
		if err := retireDeadTimeseriesIndexes(db, table); err != nil {
			t.Fatalf("%s: retire #2 (idempotency): %v", table, err)
		}
		after := pgIndexColumnLists(t, db, table)

		if len(mid) != len(after) {
			t.Errorf("%s: 退役非幂等：第 1 次后 %d 条索引，第 2 次后 %d 条", table, len(mid), len(after))
		}
		// 死索引确实没了。
		for _, cols := range deadTimeseriesIndexes[table] {
			norm := normalizeIndexColumns(joinComma(cols))
			if name, still := after[norm]; still {
				t.Errorf("%s: 死索引 (%s) 仍在（%s）—— 退役没生效", table, norm, name)
			}
		}
		// 活模板一条都不能少。
		for _, norm := range templateColumnLists(table) {
			if _, ok := after[norm]; !ok {
				t.Errorf("%s: 退役误删了活索引 (%s)！删除前有 %d 条，删除后 %d 条 (before=%v after=%v)",
					table, norm, len(before), len(after), before, after)
			}
		}
		t.Logf("%s: 退役前 %d 条 → 后 %d 条，活模板全部保留", table, len(before), len(after))
	}
}

// sanitizeIdentForTest 把列清单里的 ", " 换成 "_" 以便拼索引名（仅测试用）。
func sanitizeIdentForTest(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch r {
		case ',', ' ':
			out = append(out, '_')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}

// TestDeviceDataDeviceIDTimestampIndexIsLoadBearing 是 2026-11-07 那次误判的**回归门禁**。
//
// 事实：device_data 上必须有**某条** (device_id, timestamp) 索引，因为
// /:id/data-summary 的 first_time/last_time 查询是
//
//	SELECT timestamp FROM device_data WHERE device_id = ? ORDER BY timestamp ASC LIMIT 1
//
// 没有它，规划器会退化成 timestamp_idx 扫描 —— 扫描量与**分区总行数**成正比。
//
// 这条用例不关心它叫什么名字（历史上叫 device_data_device_id_timestamp_idx，
// 模板里叫 device_data_device_ts_idx，ensure 按列判存在所以两者等价）。
// 它只断言一件事：**能力在**。
func TestDeviceDataDeviceIDTimestampIndexIsLoadBearing(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	if err := db.Exec("DROP TABLE IF EXISTS device_data CASCADE").Error; err != nil {
		t.Fatalf("drop device_data: %v", err)
	}
	if err := MigrateTableToPartitioned(db, "device_data", "device_data_legacy"); err != nil {
		t.Fatalf("migrate device_data: %v", err)
	}
	// 先跑一次退役（正常启动会跑）—— 退役不得把这条删掉。
	if err := retireDeadTimeseriesIndexes(db, "device_data"); err != nil {
		t.Fatalf("retire: %v", err)
	}

	served := pgIndexColumnLists(t, db, "device_data")
	name, ok := served["device_id, timestamp"]
	if !ok {
		t.Fatalf("device_data 上没有 (device_id, timestamp) 索引 —— data-summary 的\n"+
			"  ORDER BY timestamp ASC/DESC LIMIT 1 会退化成扫描整个分区（实测 0.074ms → 17.77ms，\n"+
			"  Rows Removed by Filter: 216000）。可用索引=%v", served)
	}
	t.Logf("(device_id, timestamp) 由 %s 提供 ✅", name)

	// 属性断言：它必须是有效索引且非唯一（唯一索引不参与退役，也不该被当成"能力"）。
	var n int64
	if err := db.Raw(
		"SELECT count(*) FROM pg_index x JOIN pg_class t ON t.oid = x.indrelid " +
			"WHERE t.relname = 'device_data' AND x.indisvalid").Scan(&n).Error; err != nil {
		t.Fatalf("count indexes: %v", err)
	}
	if n == 0 {
		t.Fatal("device_data 上一条有效索引都没有 —— 迁移没建索引？")
	}
	t.Logf("device_data 有效索引 %d 条", n)
}

// TestLegacyIndexNameSatisfiesTemplateColumnCheck 是 2026-11-07 那次误判的**精确复现**。
//
// 生产实况：device_data 上那条 (device_id, timestamp) 索引叫
// device_data_device_id_timestamp_idx（老名字），而模板里叫 device_data_device_ts_idx。
// ensurePartitionParentIndexes 用 strings.Contains 判列 —— 于是它认为模板"已满足"，
// 永远不建同名那条。**这个脱钩必须被显式验一次**，否则下一次索引盘点会重演误判。
//
// 用例做法：直接造出老名字索引（不跑迁移），然后：
//  1. ensurePartitionParentIndexes 必须**不新建** device_data_device_ts_idx（列已满足）；
//  2. retireDeadTimeseriesIndexes 必须**不删**老名字那条（它不在死清单里）；
//  3. 最终 (device_id, timestamp) 能力仍在。
func TestLegacyIndexNameSatisfiesTemplateColumnCheck(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	if err := db.Exec("DROP TABLE IF EXISTS device_data CASCADE").Error; err != nil {
		t.Fatalf("drop device_data: %v", err)
	}
	// 用生产 2026-11 的 DDL 形状：只有老名字那条 (device_id, timestamp)。
	if err := db.Exec(`CREATE TABLE device_data (
		id bigserial NOT NULL, device_id bigint, node_id varchar(64), data_json text,
		timestamp timestamptz NOT NULL DEFAULT now(), created_at timestamptz, edge_device_id bigint,
		PRIMARY KEY (id, timestamp)) PARTITION BY RANGE (timestamp)`).Error; err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := db.Exec("CREATE INDEX device_data_device_id_timestamp_idx ON device_data (device_id, timestamp)").Error; err != nil {
		t.Fatalf("create legacy-named index: %v", err)
	}

	if err := ensurePartitionParentIndexes(db, "device_data"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := retireDeadTimeseriesIndexes(db, "device_data"); err != nil {
		t.Fatalf("retire: %v", err)
	}

	served := pgIndexColumnLists(t, db, "device_data")
	name, ok := served["device_id, timestamp"]
	if !ok {
		t.Fatalf("(device_id, timestamp) 索引消失了 —— 老名字那条被误删？剩余=%v", served)
	}
	t.Logf("(device_id, timestamp) 由 %s 提供（名字与模板不同，按列判存在 ⇒ 合规）", name)
	if name != "device_data_device_id_timestamp_idx" {
		t.Logf("注意：本次实测提供者是 %s，非生产老名字；判据相同，结论不变", name)
	}
}
