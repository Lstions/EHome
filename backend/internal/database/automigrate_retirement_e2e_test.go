package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"ehome/backend/pkg/logger"
	"ehome/backend/testutil"
)

// readGormSource 读取同包的 gorm.go 源码（顺序判据用）。
func readGormSource() (string, error) {
	b, err := os.ReadFile(filepath.Join(".", "gorm.go"))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// AutoMigrate 的「死表退役」端到端门禁（把「上线时观察首轮日志」变成本地可复现事实）。
//
// ===== 为什么需要它 =====
// 计划文档曾把这一项记为「生产库的表由下次启动 `AutoMigrate` 幂等清除；
// **上线时需观察首轮日志**」—— 即依赖人在生产上肉眼确认。
// 但「退役步骤本身能删表」（retire_operation_logs_test.go）与
// 「**AutoMigrate 真的调用了它、且顺序正确**」是**两件事**：
// 前者覆盖函数行为，后者才是上线时真正发生的事。本文件补上后者。
//
// ===== 覆盖的三件事 =====
//   1. 存量库里有残留的 operation_logs / unified_data_rollup_1m 时，
//      跑一次 AutoMigrate 后它们**都不存在**（端到端，不只看单个函数）；
//   2. **顺序正确**：退役在 AutoMigrate 建表**之后**执行 ——
//      源码注释写明「即便某次显式重加注册建出空表，也在同一次启动内被清掉」；
//   3. 再次跑 AutoMigrate 幂等（无表时不报错、不复活）。
//
// ===== 方言 =====
// 走 testutil.OpenTestDB：EHOME_TEST_DB=postgres 时用**真实 PG** 的隔离 schema，
// 空/sqlite 时用内存 SQLite。两种方言都验证（生产是 PG，故 PG 那次最关键）。
//
// ===== 本门禁查不了什么（诚实声明）=====
//   · 查不了**生产库的真实残留量**（那需要连生产库；本测试只证明「有残留必被清」）；
//   · 查不了「首轮日志是否被运维看到」（那是流程问题，不是代码问题）；
//   · 依赖 testutil.OpenTestDB 的方言分派 —— 若该分派被改坏，本测试会退化为只跑 SQLite
//     （testutil 侧已有 db_contract_test.go 专门守这一点）。

// 注：DDL 常量复用同包既有的 legacyOperationLogsDDL（retire_operation_logs_test.go）
// 与 legacyRollupDDL（retire_rollup_test.go）—— 不另起一份，避免两个副本漂移。

// seedLegacyDeadTables 在库中制造两张已退役表的残留，并确认它们确实存在。
func seedLegacyDeadTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(legacyOperationLogsDDL).Error; err != nil {
		t.Fatalf("制造 operation_logs 残留: %v", err)
	}
	if err := db.Exec(legacyRollupDDL).Error; err != nil {
		t.Fatalf("制造 unified_data_rollup_1m 残留: %v", err)
	}
	for _, tbl := range []string{"operation_logs", "unified_data_rollup_1m"} {
		if !db.Migrator().HasTable(tbl) {
			t.Fatalf("前置条件失败: %s 未被创建", tbl)
		}
	}
}

// TestAutoMigrate_RetiresDeadTablesEndToEnd 是本次补上的核心断言。
func TestAutoMigrate_RetiresDeadTablesEndToEnd(t *testing.T) {
	// 静音日志：退役步骤会 Warnf 一行（这正是「首轮日志」的内容），
	// 测试里不需要它刷屏，但**保留调用**以确保该分支被真实执行。
	logger.Init("error")

	db := testutil.OpenTestDB(t)

	// ① 制造存量残留
	seedLegacyDeadTables(t, db)

	// ② 走**生产同一条路径**：置包级 DB 后调 AutoMigrate
	prev := DB
	DB = db
	t.Cleanup(func() { DB = prev })
	if err := AutoMigrate(); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	// ③ 两张死表都必须已被清除
	for _, tbl := range []string{"operation_logs", "unified_data_rollup_1m"} {
		if db.Migrator().HasTable(tbl) {
			t.Errorf("AutoMigrate 后 %s 仍然存在 —— 退役步骤未被调用或顺序不对"+
				"（计划文档此前的说法是「上线时需观察首轮日志」，本断言把它变成本地可复现）", tbl)
		}
	}

	// ④ 幂等：再跑一次不应报错，也不应把表「复活」
	if err := AutoMigrate(); err != nil {
		t.Fatalf("二次 AutoMigrate（幂等性）: %v", err)
	}
	for _, tbl := range []string{"operation_logs", "unified_data_rollup_1m"} {
		if db.Migrator().HasTable(tbl) {
			t.Errorf("二次 AutoMigrate 后 %s 复活", tbl)
		}
	}
}

// TestAutoMigrate_RetirementRunsAfterMigration 钉住**顺序**：
// 退役调用必须在 AutoMigrate 建表之后（源码注释：即便有人把建表加回来，
// 也在同一次启动内被清掉）。这是纯源码判据 —— 顺序无法用行为断言区分，
// 因为两种顺序在「表已不存在」时的可观察结果相同。
func TestAutoMigrate_RetirementRunsAfterMigration(t *testing.T) {
	src, err := readGormSource()
	if err != nil {
		t.Fatalf("读取 gorm.go: %v", err)
	}
	// 取 AutoMigrate 函数体区域（到下一个顶层 func 为止）
	start := strings.Index(src, "func AutoMigrate(")
	if start < 0 {
		t.Fatal("gorm.go 找不到 func AutoMigrate")
	}
	rest := src[start:]
	if next := strings.Index(rest[1:], "\nfunc "); next >= 0 {
		rest = rest[:next+1]
	}

	migrateIdx := strings.Index(rest, "DB.AutoMigrate(")
	if migrateIdx < 0 {
		t.Fatal("AutoMigrate 函数体内找不到 DB.AutoMigrate( 调用")
	}
	for _, fn := range []string{"RetireLegacyOperationLogs(DB)", "RetireLegacyRollup1m(DB)"} {
		idx := strings.Index(rest, fn)
		if idx < 0 {
			t.Errorf("AutoMigrate 函数体内找不到 %s —— 死表退役不会在生产启动路径执行", fn)
			continue
		}
		if idx < migrateIdx {
			t.Errorf("%s 出现在 DB.AutoMigrate( **之前** —— "+
				"源码注释约定「退役放在建表之后，即便建表被加回来也在同一次启动内清掉」，顺序被改动", fn)
		}
	}
}

// TestAutoMigrate_RetirementIsIdempotentWithoutLegacyTables 覆盖「全新库」场景：
// 表本来就不存在时，AutoMigrate 不得因退役步骤报错（上线到干净环境也要成立）。
func TestAutoMigrate_RetirementIsIdempotentWithoutLegacyTables(t *testing.T) {
	logger.Init("error")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	prev := DB
	DB = db
	t.Cleanup(func() { DB = prev })

	if err := AutoMigrate(); err != nil {
		t.Fatalf("全新库跑 AutoMigrate 不应报错（退役步骤必须对「无表」宽容）: %v", err)
	}
}
