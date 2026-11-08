package database

// unified_data_no_recreate_evidence_test.go — 给行为级门禁补一条**过程证据**。
//
// ============ 为什么要这条 ============
//
// TestAutoMigrate_DoesNotRecreateDroppedUnifiedData 的核心是一个**否定断言**：
// 「DROP 之后 AutoMigrate 没有把 unified_data 建回来」。
//
// 否定断言有个天然弱点：**它可能是空跑**。
// 如果 AutoMigrate() 什么都没做（提前 return / 当前没有任何表需要建），
// 「没建出 unified_data」就是废命题 —— 而用例照样绿。
//
// 这正是本仓已经踩过的坑（「验证器失效时不会喊，它会安静地继续返回成功」）。
//
// 所以本文件补一条**正向证据**：同一个 AutoMigrate() 调用之后，
// 必须确实建出了别的表（= 它真的在工作），同时 unified_data 不在其中。
//
// 两条合起来才是完整判据：
//
//	正向：AutoMigrate 确实执行并产生了副作用（不是空跑）
//	负向：副作用里没有 unified_data（这就是我们要的）
//
// 缺正向 ⇒ 负向可能是空跑的产物。
// 缺负向 ⇒ 只证明了 AutoMigrate 能建表，与 unified_data 无关。

import (
	"testing"

	"ehome/backend/pkg/logger"
	"ehome/backend/testutil"
)

// TestAutoMigrate_ReallyRanAndBuiltOtherTables 是「不是空跑」的证据。
func TestAutoMigrate_ReallyRanAndBuiltOtherTables(t *testing.T) {
	logger.Init("error")
	db := testutil.OpenTestDB(t)

	// 造一个「AutoMigrate 必然要处理」的初始条件：
	// 把几张**它管着的**表删掉（不是 unified_data，是别的），看它是否确实建回来。
	//
	// 选三个不同模块的表，避免「恰好某一张被别的初始化步骤建出来」这种巧合。
	witnessTables := []string{"device_configs", "alert_rules", "vendors"}
	for _, tbl := range witnessTables {
		if !db.Migrator().HasTable(tbl) {
			t.Fatalf("前置失败：%s 不在测试库里（testutil 的 allModels 应含它）", tbl)
		}
		if err := db.Exec("DROP TABLE IF EXISTS " + tbl + " CASCADE").Error; err != nil {
			t.Fatalf("DROP %s: %v", tbl, err)
		}
	}

	// 再删 unified_data（本次要守的那张）
	if err := db.Exec("DROP TABLE IF EXISTS " + unifiedDataTable + " CASCADE").Error; err != nil {
		t.Fatalf("DROP %s: %v", unifiedDataTable, err)
	}

	prev := DB
	DB = db
	t.Cleanup(func() { DB = prev })
	if err := AutoMigrate(); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	// ── 正向：见证表必须被建回来（证明 AutoMigrate 真的执行了）──
	rebuilt := 0
	for _, tbl := range witnessTables {
		if db.Migrator().HasTable(tbl) {
			rebuilt++
		}
	}
	if rebuilt == 0 {
		t.Fatalf("AutoMigrate() 跑完后，%d 张见证表**一张都没被建回来** —— "+
			"这说明它没有真正执行（空跑）。\n\t"+
			"在空跑的情况下，TestAutoMigrate_DoesNotRecreateDroppedUnifiedData 的"+
			"「没有重建 unified_data」是废命题 —— 它只证明了「什么都没发生」。\n\t"+
			"见证表：%v", len(witnessTables), witnessTables)
	}
	t.Logf("正向证据：AutoMigrate 把 %d/%d 张见证表建回来了 => 它确实在执行",
		rebuilt, len(witnessTables))

	// ── 负向：同一次调用里 unified_data 必须不在 ──
	if db.Migrator().HasTable(unifiedDataTable) {
		t.Fatalf("同一次 AutoMigrate 里 %s 被建出来了 —— 见 "+
			"TestAutoMigrate_DoesNotRecreateDroppedUnifiedData 的说明", unifiedDataTable)
	}
	t.Logf("负向证据：同一次调用未建出 %s", unifiedDataTable)
}
