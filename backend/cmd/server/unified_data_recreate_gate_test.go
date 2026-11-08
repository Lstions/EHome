package main

// unified_data_recreate_gate_test.go — 「DROP unified_data 后重启不重建」的**源码级门禁**。
//
// ============ 守什么 ============
//
// 生产镜像 afd66df1 实测（证据已落）：
//
//	main.go 仍调用 datalifecycle.MigrateUnifiedDataToPartitioned
//	=> DROP unified_data 后重启，表**自动重建**（exists_after_restart=1）
//	=> 两次启动 readyz 都是 200（**没有任何错误信号**）
//
// 也就是说：DROP 之后重启 = 17.7M 行没了、磁盘没省、而一切看起来正常。
// 这是「硬切换」这件事要避免的终局，所以必须有回归锁。
//
// ============ 为什么是源码级，而不是只靠行为级 ============
//
// 行为级门禁（跑两遍迁移链、断言表不存在）更贴近事实，但它慢（要 PG）、
// 且需要复制一段「启动链」—— 而复制出来的那份会和 main.go 漂移
// （见本任务的分工说明：漂移的判据比没有判据更糟）。
//
// 所以这里做**快的那一半**：断言 main.go 里**不存在**对该函数的调用。
// 它挡的是「某天有人把注释解开」这个具体回归。
//
// ============ 必须剥注释 ============
//
// main.go 里现在**留着**这段：
//
//	//	startupTracker.Begin(bootstrap.StepPartitionUnifiedData)
//	//	if err := datalifecycle.MigrateUnifiedDataToPartitioned(db); err != nil { ... }
//
// 那是解释「为什么删掉它」的证据，必须保留。而不剥注释的扫描器会命中它 ⇒
// 门禁**恒红**（然后有人会给它加豁免，而豁免即后门）。
//
// 反过来更危险：如果门禁写成「注释里也不能有」，那段说明就得删掉 ——
// 于是未来的人看不到「为什么这里少了一步」，然后「顺手补回来」。
//
// 所以：**剥注释后扫描**。剥注释复用 maintenance_gate_test.go 的 stripGoComments
// （同包，已处理字符串/raw string/rune/行注释/块注释）。
//
// ============ 扫描器必须能变红 ============
//
// 见 TestUnifiedDataRecreateGate_ClassifierSelfCheck：喂一段**含调用**的假源码，
// 断言分类器判红。否则一个坏掉的扫描器会让门禁永远绿（vacuous pass）——
// 本仓已有过这个教训（TestOperationLogsGate_ClassifierSelfCheck 同款）。

import (
	"os"
	"strings"
	"testing"
)

// recreateCallNeedle 是「会触发重建」的那次调用（剥注释后不允许出现）。
//
// 只锚这一个完整调用表达式，而不是函数名裸子串：
//
//	· 裸子串 MigrateUnifiedDataToPartitioned 会命中 requireMigrationHeldComment 里
//	  那段解释文字（剥注释后已消失，但**函数定义处**仍会出现）—— 那不是调用；
//	· 精确到 `= datalifecycle.MigrateUnifiedDataToPartitioned(` 只匹配调用形态。
const recreateCallNeedle = "datalifecycle.MigrateUnifiedDataToPartitioned("

// TestUnifiedDataRecreateGate_MainDoesNotCallMigration 是主门禁。
func TestUnifiedDataRecreateGate_MainDoesNotCallMigration(t *testing.T) {
	src := stripGoComments(readMainSourceRaw(t))

	if i := strings.Index(src, recreateCallNeedle); i >= 0 {
		line := 1 + strings.Count(src[:i], "\n")
		t.Fatalf("main.go 第 %d 行调用了 %s —— 这会让 DROP unified_data 后重启**自动重建**空表"+
			"（17.7M 行没了、磁盘没省，而 readyz 照样 200，没有任何错误信号）。\n\t"+
			"为什么必须删：MigrateTableToPartitioned 的判定链是"+
			"IsTablePartitioned(relkind='p') → 否；flatExists(relkind='r') → 否；"+
			"于是走「原表不存在（全新部署）→ createPartitionedParent」——"+
			"代码无法区分「全新部署」与「我们刚把表 DROP 了」。\n\t"+
			"2026-11-08 硬切换已移除这一步（原因写在 main.go 那段注释里），"+
			"保留 MigrateUnifiedDataToPartitioned 函数本身（device_data / telemetry_frame "+
			"仍走同一套机制），但**不再为 unified_data 调用**。", line, recreateCallNeedle)
	}
}

// TestUnifiedDataRecreateGate_ExplainsWhy 断言 main.go **保留了**那段解释。
//
// 为什么这条也要有：门禁只断言「不许调用」的话，最省事的合规方式是**把整段注释删掉**
// —— 于是未来的人看不到「为什么这里少了一步」，然后「顺手补回来」。
// 所以「不许调用」必须和「必须留下理由」成对存在。
func TestUnifiedDataRecreateGate_ExplainsWhy(t *testing.T) {
	raw := readMainSourceRaw(t)
	// 注意：这条**要在原文里查**（不剥注释）—— 我们要的正是那段注释。
	for _, want := range []string{
		"unified_data 不再迁移",
		"MigrateTableToPartitioned 的",
		"createPartitionedParent",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("main.go 缺少说明片段 %q —— 删掉迁移那一步时**必须留下理由**，"+
				"否则未来的人看不到「为什么这里少了一步」，然后会顺手补回来"+
				"（而那会让 DROP 后的表被重建）", want)
		}
	}
}

// TestUnifiedDataRecreateGate_ClassifierSelfCheck 是**扫描器自检**。
//
// 一个只会说「没找到问题」的扫描器与没有扫描器等价。这里喂它两段假源码：
//
//	① 含调用的 ⇒ 必须判红
//	② 只有注释里含调用的 ⇒ 必须判绿（证明剥注释真的生效）
//
// 缺了 ②，扫描器可能是「连注释一起扫」的实现 —— 那种实现在真实 main.go 上恒红，
// 然后就会有人给它加豁免。
func TestUnifiedDataRecreateGate_ClassifierSelfCheck(t *testing.T) {
	withCall := "func f() {\n\tif err := " + recreateCallNeedle + "db); err != nil {\n\t}\n}\n"
	if !recreateGateFindsCall(withCall) {
		t.Error("自检失败：含调用的源码被判为「没有调用」—— 扫描器失效，主门禁是假绿")
	}

	commentOnly := "func f() {\n\t// 原实现：" + recreateCallNeedle + "db)\n\t// 已删除\n}\n"
	if recreateGateFindsCall(commentOnly) {
		t.Error("自检失败：只需注释里出现就被判红 —— 扫描器没剥注释。" +
			"后果是真实 main.go（保留了解释性注释）恒红，然后有人会加豁免，而豁免即后门")
	}

	// ③ 函数**定义**（不是调用）不得误判
	definition := "func MigrateUnifiedDataToPartitioned(db *gorm.DB) error {\n\treturn nil\n}\n"
	if recreateGateFindsCall(definition) {
		t.Error("自检失败：函数定义被误判为调用 —— 判据必须是调用形态，不是函数名裸子串")
	}

	// ④ 参数形态变化不得逃过：只要是真调用，参数叫什么都要抓到。
	//
	// 这条防的是「门禁只认某个实参写法」—— 有人改成 tx / database.GetDB() /
	// 换行写，门禁就漏了。锚的必须是调用形态本身（函数名 + 左括号）。
	for _, variant := range []string{
		"if err := " + recreateCallNeedle + "db); err != nil { }",
		"if err := " + recreateCallNeedle + "tx); err != nil { }",
		"if err := " + recreateCallNeedle + "database.GetDB()); err != nil { }",
		"err := " + recreateCallNeedle + "db)",
	} {
		if !recreateGateFindsCall(variant) {
			t.Errorf("自检失败：调用 %q 未被识别 —— 门禁只认某种实参写法，改个名字就绕过了", variant)
		}
	}
}

// recreateGateFindsCall 是门禁的**判定核心**（抽出来才能被自检覆盖）。
//
// 剥注释后查调用形态。抽成独立函数而不是内联在测试里，是为了让
// TestUnifiedDataRecreateGate_ClassifierSelfCheck 能喂它假输入 ——
// 这是本仓「判据必须能被行为验证」的一贯做法。
func recreateGateFindsCall(src string) bool {
	// 剥注释后，只要出现 "datalifecycle.MigrateUnifiedDataToPartitioned(" 就是一次调用：
	// 函数**定义**在 partition_mgr.go，不在 main.go，所以这里不存在误判成定义的可能；
	// 而参数名无论叫 db / tx / database.GetDB() 都无所谓 —— 锚的是调用形态本身。
	return strings.Contains(stripGoComments(src), recreateCallNeedle)
}

// readMainSourceRaw 读取 main.go 的**原文**（不剥注释）。
//
// 与 maintenance_gate_test.go 的 readStartupMainSource 区分：那个已经剥过注释，
// 而本文件的 TestUnifiedDataRecreateGate_ExplainsWhy 需要看注释原文。
func readMainSourceRaw(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(maintenanceGateMainFile)
	if err != nil {
		t.Fatalf("read %s: %v（门禁不能静默失效）", maintenanceGateMainFile, err)
	}
	return string(raw)
}
