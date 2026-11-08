package database

import (
	"os"
	"strings"
	"testing"

	"gorm.io/gorm"

	"ehome/backend/testutil"
)

// legacyRollupDDL 复刻已退役表的建表语句 (字段同被删的 models.UnifiedDataRollup1m)。
// 只用于测试: 证明"存量库里残留的旧表"确实会被 RetireLegacyRollup1m 幂等清掉。
// 两种方言 (SQLite / PG) 都接受这份 DDL 子集。
const legacyRollupDDL = `CREATE TABLE unified_data_rollup_1m (
	device_id   BIGINT NOT NULL,
	sensor_name VARCHAR(32) NOT NULL,
	bucket      TIMESTAMP NOT NULL,
	min_v       DOUBLE PRECISION,
	max_v       DOUBLE PRECISION,
	avg_v       DOUBLE PRECISION,
	last_v      DOUBLE PRECISION,
	last_id     BIGINT,
	cnt         INTEGER,
	PRIMARY KEY (device_id, sensor_name, bucket)
)`

// TestRetireLegacyRollup1mDropsLegacyTable 覆盖退役的核心行为:
// 存量库里真的存在这张旧表时, 退役步骤把它删掉, 且**重复调用零副作用** (幂等)。
//
// 方言: testutil.OpenTestDB 在 EHOME_TEST_DB=sqlite/空 下走内存 SQLite,
// 在 EHOME_TEST_DB=postgres 下走 PG 的隔离 schema (test_<pid>_<nano>)。
func TestRetireLegacyRollup1mDropsLegacyTable(t *testing.T) {
	db := testutil.OpenTestDB(t)

	// 1. 造出"存量旧表" (含一行数据, 确认 DROP 不因数据而失败)。
	if err := db.Exec(legacyRollupDDL).Error; err != nil {
		t.Fatalf("创建模拟遗留表 unified_data_rollup_1m: %v", err)
	}
	if err := db.Exec("INSERT INTO unified_data_rollup_1m (device_id, sensor_name, bucket, min_v, max_v, avg_v, last_v, last_id, cnt) VALUES (7, 'temp', ?, 10, 20, 15, 20, 2, 2)",
		"2026-08-21 10:00:00").Error; err != nil {
		t.Fatalf("写入模拟遗留行: %v", err)
	}
	if !db.Migrator().HasTable("unified_data_rollup_1m") {
		t.Fatal("前置条件失败: 模拟遗留表未被创建")
	}

	// 2. 首次退役: 删除, 并返回删除前行数 (留痕值)。
	rows, err := RetireLegacyRollup1m(db)
	if err != nil {
		t.Fatalf("首次退役: %v", err)
	}
	if rows != 1 {
		t.Errorf("退役返回的行数 = %d, want 1 (删除前该表有 1 行)", rows)
	}
	if db.Migrator().HasTable("unified_data_rollup_1m") {
		t.Fatal("退役后 unified_data_rollup_1m 仍然存在")
	}

	// 3. 二次退役: 幂等 —— 无表时零动作、零错误、行数 0。
	rows, err = RetireLegacyRollup1m(db)
	if err != nil {
		t.Fatalf("二次退役 (幂等性): %v", err)
	}
	if rows != 0 {
		t.Errorf("二次退役返回的行数 = %d, want 0 (表已不存在)", rows)
	}
}

// TestAutoMigrateRetiresRollupOnProductionPath 走**生产入口本身**
// (database.AutoMigrate()) 验证退役: 存量库残留的旧表在同一次启动内被清掉,
// 且随后的启动**不再创建**它 (EnsureRollupTable 已随退役删除, main.go 调用点已摘除)。
// 这是最贴近生产的一条证据 —— 调用链与 cmd/server/main.go 完全一致。
func TestAutoMigrateRetiresRollupOnProductionPath(t *testing.T) {
	db := testutil.OpenTestDB(t)

	// 模拟"升级前就已存在该表"的存量库。
	if err := db.Exec(legacyRollupDDL).Error; err != nil {
		t.Fatalf("创建模拟遗留表: %v", err)
	}
	if !db.Migrator().HasTable("unified_data_rollup_1m") {
		t.Fatal("前置条件失败: 模拟遗留表未被创建")
	}

	// AutoMigrate() 用包级 DB; 包内测试不并行 (无 t.Parallel), 用后恢复。
	prev := DB
	DB = db
	t.Cleanup(func() { DB = prev })

	if err := AutoMigrate(); err != nil {
		t.Fatalf("生产 AutoMigrate 路径: %v", err)
	}
	if db.Migrator().HasTable("unified_data_rollup_1m") {
		t.Fatal("生产 AutoMigrate 路径结束后 unified_data_rollup_1m 仍然存在 —— 退役 DDL 未被调用")
	}
	// 再跑一次: 幂等 (表已不存在, 不得报错, 不得重建)。这一条同时覆盖
	// "启动后不再创建 rollup 表" (原 EnsureRollupTable 的建表路径已删)。
	if err := AutoMigrate(); err != nil {
		t.Fatalf("生产 AutoMigrate 路径二次执行 (幂等性): %v", err)
	}
	if db.Migrator().HasTable("unified_data_rollup_1m") {
		t.Fatal("二次执行后 unified_data_rollup_1m 被重建")
	}
}

// TestRetireLegacyRollup1mDoesNotTouchTelemetryRollup 是「只退役旧表、不误伤新表」
// 的**行为证据**（方案 §5.4 硬要求）。
//
// 为什么必须有这一条（源码门禁不够）: datalifecycle/rollup_retirement_gate_test.go
// 是源码扫描，它只能证明"退役文件里没出现新表名"；而真正会让新表消失的形态是
// **通配 DROP**（DROP TABLE IF EXISTS unified_data_rollup_1m_* / LIKE '..._rollup_1m%'）
// 或有人在退役步骤里顺手多删一张表 —— 这两种都可能在源码扫描下以"另一个写法"
// 混过去。这里用真实两张表 + 数据，断言退役前/后的行数与存在性。
//
// 注意: telemetry_rollup_1m 是 L3 新表（设计/遥测存储优化方案-2026-11-06.md §5.2），
// 与旧表**不是**同一条命：旧表退役、新表在场，是这个用例的初始状态。
func TestRetireLegacyRollup1mDoesNotTouchTelemetryRollup(t *testing.T) {
	db := testutil.OpenTestDB(t)

	// 1. 两表同时存在：旧表（待退役）+ 新表（L3，必须存活）。
	if err := db.Exec(legacyRollupDDL).Error; err != nil {
		t.Fatalf("创建旧表 unified_data_rollup_1m: %v", err)
	}
	if err := db.Exec(telemetryRollupDDLFor(db)).Error; err != nil {
		t.Fatalf("创建新表 telemetry_rollup_1m: %v", err)
	}
	if err := db.Exec("INSERT INTO unified_data_rollup_1m (device_id, sensor_name, bucket, min_v, max_v, avg_v, last_v, last_id, cnt) VALUES (7, 'temp', ?, 10, 20, 15, 20, 2, 2)",
		"2026-08-21 10:00:00").Error; err != nil {
		t.Fatalf("写旧表残留行: %v", err)
	}
	if err := db.Exec("INSERT INTO telemetry_rollup_1m (device_id, bucket, payload) VALUES (14, ?, ?)",
		"2026-11-04 10:00:00", []byte{1, 0, 1, 1, 'a'}).Error; err != nil {
		t.Fatalf("写新表行: %v", err)
	}

	// 2. 退役：只应删旧表。
	rows, err := RetireLegacyRollup1m(db)
	if err != nil {
		t.Fatalf("退役旧表: %v", err)
	}
	if rows != 1 {
		t.Errorf("退役返回行数 = %d, want 1", rows)
	}
	if db.Migrator().HasTable("unified_data_rollup_1m") {
		t.Fatal("旧表 unified_data_rollup_1m 应已被 DROP")
	}

	// 3. 新表必须原样存活（存在性 + 行数 + payload 字节）。
	if !db.Migrator().HasTable("telemetry_rollup_1m") {
		t.Fatal("退役步骤误删了 L3 新表 telemetry_rollup_1m —— 方案 §5.4 明令只退役旧表")
	}
	var count int64
	if err := db.Raw("SELECT count(*) FROM telemetry_rollup_1m").Scan(&count).Error; err != nil {
		t.Fatalf("读新表: %v", err)
	}
	if count != 1 {
		t.Fatalf("新表行数 = %d, want 1（退役不得动新表数据）", count)
	}
	var row struct{ Payload []byte }
	if err := db.Raw("SELECT payload FROM telemetry_rollup_1m LIMIT 1").Scan(&row).Error; err != nil {
		t.Fatalf("读新表 payload: %v", err)
	}
	if len(row.Payload) != 5 || row.Payload[0] != 1 {
		t.Errorf("新表 payload 被改动: %v", row.Payload)
	}
}

// TestRetireLegacyRollup1mDoesNotUseWildcardDrop 钉死「退役 DROP 不得使用通配」。
//
// 为什么用源码判据: DROP 的目标表名是运行时拼出来的，行为断言只能覆盖"当下存在的
// 表"；通配形态一旦被写进来（例如为了清理旧表的历史分区而写成
// 'unified_data_rollup_1m%'），下一轮就会把 telemetry_rollup_1m 与
// telemetry_rollup_1m_YYYYMM 一起删掉 —— 而那种破坏在测试库里未必立刻可见
// （测试库通常只有一张表）。这条判据把"写法"本身钉死。
func TestRetireLegacyRollup1mDoesNotUseWildcardDrop(t *testing.T) {
	src, err := os.ReadFile("retire_rollup.go")
	if err != nil {
		t.Fatalf("读 retire_rollup.go: %v", err)
	}
	text := string(src)
	for _, banned := range []string{"LIKE '", "LIKE \"", "\\_rollup_1m%", "_rollup_1m%'"} {
		if strings.Contains(text, banned) {
			t.Errorf("退役步骤出现通配形态 %q —— 会连带删掉 telemetry_rollup_1m 与其月分区", banned)
		}
	}
	// 反向：表名字面量只应出现一次（集中在一个常量里），且 DROP 目标由它拼接。
	const tableLiteral = `"unified_data_rollup_1m"`
	if got := strings.Count(text, tableLiteral); got != 1 {
		t.Errorf("表名字面量 %s 出现次数 = %d, want 1（表名必须集中在一个常量里）", tableLiteral, got)
	}
	if !strings.Contains(text, "\"DROP TABLE IF EXISTS \" + rollupTable") {
		t.Error("DROP 目标必须由 rollupTable 常量拼接（不得内联别的表名）")
	}
}

// telemetryRollupDDL 复刻 L3 新表（模型 models.TelemetryRollup1m）的最小 DDL 子集。
// 只用于本文件的"不误伤"用例。**不要**在这里加分区语法（SQLite 不支持）。
//
// 方言差异（实测踩到）：SQLite 的 blob 类型名是 `BLOB`，PG 上必须写 `BYTEA` ——
// 直接复用同一份 DDL 会在 PG 隔离库上报 `type "blob" does not exist`。
// 两方言的 payload 列都可用 []byte 往返，只有类型名不同。
func telemetryRollupDDLFor(db *gorm.DB) string {
	payloadType := "BLOB"
	if db != nil && db.Dialector != nil && db.Dialector.Name() == "postgres" {
		payloadType = "BYTEA"
	}
	return `CREATE TABLE telemetry_rollup_1m (
	device_id integer NOT NULL,
	bucket    TIMESTAMP NOT NULL,
	payload   ` + payloadType + ` NOT NULL,
	PRIMARY KEY (device_id, bucket)
)`
}

// TestAutoMigrateDoesNotCreateRollup 断言: 走测试库的全量模型清单
// (testutil.allModels, 与生产 AutoMigrate 同源) 迁移后, 该表**不会被创建**。
//
// 这是"新表不再被创建"的库级证据 (源码级由
// datalifecycle/rollup_retirement_gate_test.go 覆盖); 在 EHOME_TEST_DB=postgres
// 下即 PG 生产路径的证据。
func TestAutoMigrateDoesNotCreateRollup(t *testing.T) {
	db := testutil.OpenTestDB(t)
	if db.Migrator().HasTable("unified_data_rollup_1m") {
		t.Fatal("AutoMigrate 迁移后仍创建了 unified_data_rollup_1m —— 死表复活")
	}
}
