package database

// unified_data_no_recreate_e2e_test.go — 「DROP unified_data 后不会被重新建出来」
// 的**行为级**门禁（2026-11-08）。
//
// ============ 它守的终局 ============
//
// 生产镜像 afd66df1 实测：
//
//	DROP unified_data 后重启 => 表自动重建（exists_after_restart=1）
//	=> 17.7M 行没了、磁盘没省、readyz 照样 200，**没有任何错误信号**
//
// 这是「换用帧表」这件事要避免的终局。它有两个独立的成因，必须**分别**守住：
//
//	成因 ①：cmd/server/main.go 调用 datalifecycle.MigrateUnifiedDataToPartitioned
//	         => 由 cmd/server/unified_data_recreate_gate_test.go 的**源码级**门禁守
//	成因 ②：unified_data 回到 database.AutoMigrate 的模型清单
//	         => 由本文件守（行为级 + 源码级两条）
//
// ============ 为什么本文件不复制 main.go 的启动链 ============
//
// 任务约束：「如果 bootstrap.DefaultSteps() 能按步骤驱动就驱动它；不能就如实说
// 做不到。不要复制 main.go 的启动链 —— 复制出来必然漂移，漂移的判据比没有判据更糟
// （它给人虚假的安全感）。」
//
// 实测结论：**DefaultSteps() 不可驱动**。
//
//	Step 结构只有 ID/Label/Weight/State/Progress/Detail/Error/StartedAt/FinishedAt，
//	全是展示数据，**没有函数字段**；全仓也没有 map[string]func 形式的步骤分派
//	（grep 'map\[string\]func' internal/bootstrap internal/cmd 零命中）。
//	main.go 是**手写顺序**地调用各步（startupTracker.Begin(x); if err := f(); ...），
//	步骤列表只用于进度展示，与执行没有绑定关系。
//
// 但**仍有一条真实的行为级判据**可跑，且不需要复制任何启动链：
// 生产代码里能创建 unified_data 的入口只有三个（已全仓核实）：
//
//	① 显式 DDL      —— grep 'CREATE TABLE.*unified_data' 在生产代码里零命中
//	② 模型注册      —— database.AutoMigrate() 的清单（本文件守）
//	③ 迁移函数      —— datalifecycle.MigrateUnifiedDataToPartitioned（源码级门禁守）
//
// 也就是说：把一个**真实的单函数入口**当作被测对象，既拿到行为级证据，
// 又不需要复制启动链。这是「做不到全链驱动」时能拿到的最强证据，
// 而不是「做不到就什么都不做」。

import (
	"strings"
	"testing"
	"time"

	"ehome/backend/pkg/logger"
	"ehome/backend/testutil"
)

// unifiedDataTable 是被守的表名。
const unifiedDataTable = "unified_data"

// TestAutoMigrate_DoesNotRecreateDroppedUnifiedData 是本文件的核心断言。
//
// 场景照抄生产事实：
//
//	① 库里有 unified_data（testutil 的 allModels 会建它，模拟「生产里它存在」）
//	② DROP 它（模拟运维执行 DROP —— 这正是「硬切换」的收尾动作）
//	③ 走生产的建表入口 database.AutoMigrate()
//	④ 断言它**没有**回来
func TestAutoMigrate_DoesNotRecreateDroppedUnifiedData(t *testing.T) {
	logger.Init("error")
	db := testutil.OpenTestDB(t)

	// ① 前置：确认它此刻存在。
	//
	// 否则第 ④ 步的「没回来」证明不了任何事 —— 一张从来没存在过的表，
	// 「DROP 后不存在」是废命题。这条前置就是「判据必须排除废命题」的落地。
	if !db.Migrator().HasTable(unifiedDataTable) {
		t.Fatalf("前置失败：%s 不在测试库里。本用例需要它先存在，才能验证「DROP 后不重建」。"+
			"若 testutil 的 allModels 已移除它，请把本用例改成先手工建表再 DROP —— "+
			"不要删掉这条用例（它是「DROP 安全」的唯一行为级证据）", unifiedDataTable)
	}

	// 写一行数据：「重建」的后果因此可被观察，而不只是「表在不在」。
	// 重建出来的表是**空的** —— 那正是危害：结构在、数据没了。
	if err := db.Exec("INSERT INTO "+unifiedDataTable+
		" (device_id, sensor_name, value, timestamp) VALUES (1, 'e2e_probe', 1.5, ?)",
		time.Now()).Error; err != nil {
		t.Fatalf("写探针行: %v", err)
	}

	// ② DROP（模拟运维动作）
	if err := db.Migrator().DropTable(unifiedDataTable); err != nil {
		t.Fatalf("DROP %s: %v", unifiedDataTable, err)
	}
	if db.Migrator().HasTable(unifiedDataTable) {
		t.Fatalf("DROP 后 %s 仍存在 —— 判据失效", unifiedDataTable)
	}

	// ③ 走**生产同一个入口**（包级 DB + database.AutoMigrate()）。
	//
	// 刻意不复制 main.go 的 4 行迁移调用：那 4 行会漂移。
	// AutoMigrate 是一个真实的单函数入口，且它**确实**能建表（管着几十张表）——
	// 所以「它没把这张表建回来」是个有信息量的断言，不是废命题。
	prev := DB
	DB = db
	t.Cleanup(func() { DB = prev })
	if err := AutoMigrate(); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	// ④ 核心断言
	if db.Migrator().HasTable(unifiedDataTable) {
		var n int64
		_ = db.Raw("SELECT count(*) FROM " + unifiedDataTable).Scan(&n).Error
		t.Fatalf("DROP %s 之后 AutoMigrate 把它**重建**回来了（当前 %d 行）—— "+
			"这正是生产 afd66df1 的失效形态：表看起来在、数据没了、磁盘没省，"+
			"而 readyz 照样 200。\n\t"+
			"修复方向：从 database.AutoMigrate 的模型清单里移除 &models.UnifiedData{}。\n\t"+
			"注意：重建出来的表是**空的**，所以「行数为 0」不是无害的证据，"+
			"而是「数据已丢失」的证据。", unifiedDataTable, n)
	}
}

// TestAutoMigrate_UnifiedDataAbsentFromModelList 是上面的**源码级补充**。
//
// 为什么行为级之外还要这条：行为级断言依赖「先 DROP 再 AutoMigrate」这个序列。
// 若将来 AutoMigrate 的实现改成「只在首次建库时建表」，行为级断言会退化成废命题
// （DROP 之后本来就不会重建），而清单里的 &models.UnifiedData{} 仍在 ——
// 它在**全新部署**时照样把表建出来。
//
// 两条一起才完整：
//
//	行为级：DROP 之后不会回来（运维视角）
//	源码级：清单里没有它（全新部署视角）
//
// 复用 operation_logs_gate_test.go 的 extractAutoMigrateRegion / readGormSource /
// isCommentLine —— 同一套判据不要写两遍（第二遍必然漂移）。
func TestAutoMigrate_UnifiedDataAbsentFromModelList(t *testing.T) {
	src, err := readGormSource()
	if err != nil {
		t.Fatalf("read gorm.go: %v", err)
	}
	region := extractAutoMigrateRegion(t, "internal/database/gorm.go", src,
		"DB.AutoMigrate(", "\n\t);")
	for _, line := range strings.Split(region, "\n") {
		if isCommentLine(line) {
			continue // 注释里提到它是在**解释为什么移除**，那是要保留的证据
		}
		if strings.Contains(line, "&models.UnifiedData{}") {
			t.Fatalf("database.AutoMigrate 的模型清单里仍有 %s —— "+
				"这会让**全新部署**把这张已停写、待 DROP 的表建出来。"+
				"违规行: %q\n\t"+
				"（若确实要加回，必须同时删掉本门禁并说明理由 —— 但先想清楚："+
				"加回它意味着「DROP 后重启会重建空表」这个陷阱回来了。）",
				"&models.UnifiedData{}", strings.TrimSpace(line))
		}
	}
}
