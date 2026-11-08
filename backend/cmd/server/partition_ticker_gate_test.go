package main

// partition_ticker_gate_test.go — 「后台分区 ticker 不会重建被 DROP 的母表」的成对门禁。
//
// ============ 守的是什么（2026-11-08，DROP 前的最后一道）============
//
// unified_data 被 DROP 之后，「重启不重建」这个性质**不是靠某条显式门禁**保证的，
// 而是靠 main.go 的 ensurePartitions 里这个顺序：
//
//	for _, table := range append(timeseriesTableNames(), rollup) {
//	    if !datalifecycle.IsTablePartitioned(db, table) {
//	        continue                                        // ← 保护在这里
//	    }
//	    pm.EnsurePartitionsFor(table, 3)                    // ← 挪到 continue 之前 = 重建
//	}
//
// 而 unified_data **确实在** timeseriesTableNames() 的第一项（这是有意的：
// 它曾经是时序表，retention/purge 的清单里也还需要它）。所以：
//
//	· 保护完全依赖 IsTablePartitioned 对「表不存在」返回 false；
//	· 有人把这个 continue 挪到 EnsurePartitionsFor 之后（或换成别的判据），
//	  EnsurePartitionsFor 会进入 createPartitionIfNotExists ⇒ 对不存在的母表执行
//	  CREATE TABLE ... PARTITION OF ⇒ **报错**（不是静默建表），
//	  但这只在启动日志里，readyz 照样 200。
//
// ✅ 已实测（2026-11-08，scratch 库，新镜像 e7d71902）：DROP 后连续两次重启，
//    exists_after_restart=0，readyz=200。所以**当前**是安全的。
//
// ⚠ 但那个安全性是「语义巧合」不是「显式约束」：没有任何用例会在那个 continue
//   被挪走时变红。这个文件补上它。
//
// ============ 为什么必须两条（A 语义级 + B 源码级）============
//
//	门禁 A 锁「IsTablePartitioned 对缺失表返回 false」—— 这是保护的事实基础；
//	门禁 B 锁「那个 continue 必须在 EnsurePartitionsFor 之前」—— 这是两行的顺序问题，
//	       行为测试很难捕捉（要真起一个 ticker 轮次），只有源码级能挡。
//
// 缺 A：有人改 IsTablePartitioned 的判据（比如换成 HasTable，那样平表也返回 true）
//       ⇒ 保护对「平表」失效，而本仓的 DROP 目标恰好是分区表，A 仍然有意义。
// 缺 B：有人挪 continue ⇒ 直接变成重建入口。

import (
	"os"
	"strings"
	"testing"

	"ehome/backend/internal/datalifecycle"
)

// TestIsTablePartitioned_MissingTableReturnsFalse 是门禁 A。
//
// 它同时锁住两件事：
//
//	① 「表不存在」⇒ false（这是 ensurePartitions 那个 continue 的事实基础）
//	② 「非 PG 方言」⇒ false（SQLite 测试库没有 relkind 概念，必须早退）
//
// 为什么不测「表存在且已分区 ⇒ true」：那需要真 PG + 建分区表，属于
// partition_mgr_pg_test.go 的职责，本文件只锁「对缺失对象的行为」。
func TestIsTablePartitioned_MissingTableReturnsFalse(t *testing.T) {
	// 非 PG（nil db / SQLite）必须早退为 false，不 panic。
	// 这里用 nil 覆盖 isPostgres 的 nil 安全分支：main.go 的 ensurePartitions
	// 在 db 为 nil 时不该崩（启动早期接线顺序可能让它先跑）。
	if datalifecycle.IsTablePartitioned(nil, "unified_data") {
		t.Fatal("IsTablePartitioned(nil, ...) = true —— 非 PG / 无 db 时必须 false，" +
			"否则 ensurePartitions 会对不存在的表调 EnsurePartitionsFor")
	}
}

// TestPartitionTickerGate_ContinueBeforeEnsure 是门禁 B（源码级）。
//
// 判据：剥注释后，在 ensurePartitions 的函数体里，
// `IsTablePartitioned(...)` 的出现位置必须在 `EnsurePartitionsFor(...)` **之前**。
//
// 为什么要在函数体内而不是整个文件：main.go 里还有别处提到这两个名字（注释/其它流程），
// 全文件查会出现「碰巧在前面」的假绿。
func TestPartitionTickerGate_ContinueBeforeEnsure(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("读 main.go: %v", err)
	}
	src := stripGoComments(string(raw))

	start := strings.Index(src, "ensurePartitions := func()")
	if start < 0 {
		t.Fatal("main.go 里找不到 ensurePartitions 闭包 —— 本门禁的前提不成立。" +
			"若它被重命名/移走，请同步更新本门禁，不要直接删它：那条 continue 是" +
			"「DROP 后不重建」的第一道保护")
	}
	body := src[start:]
	// 截到闭包结束：找下一个「\n\t}\n」形态（闭包体的右括号在顶层缩进）。
	if end := strings.Index(body, "\n\t}\n"); end > 0 {
		body = body[:end]
	}

	switch judgeTickerOrder(body) {
	case tickerOrderNoGuard:
		t.Fatal("ensurePartitions 里没有 IsTablePartitioned 判据 —— " +
			"unified_data 在 timeseriesTableNames() 里，没有这个判据，ticker 会对" +
			"不存在的母表调 EnsurePartitionsFor，readyz 仍然 200、错误只在启动日志里")
	case tickerOrderNoEnsure:
		t.Fatal("ensurePartitions 里没有 EnsurePartitionsFor —— 前提不成立，请更新本门禁")
	case tickerOrderBroken:
		t.Fatalf("IsTablePartitioned 判据出现在 EnsurePartitionsFor **之后** —— 那个 continue 被挪走了。\n\t"+
			"后果：ticker 会对被 DROP 的母表调 EnsurePartitionsFor，而 readyz 照样 200。\n\t"+
			"这是 2026-11-08 硬切换的唯一实际保护（scratch 实测 exists_after_restart=0 靠的就是它）。\n\t"+
			"函数体：%s", body)
	}
	t.Logf("门禁 B 成立：IsTablePartitioned 在 EnsurePartitionsFor 之前")
}

// TestPartitionTickerGate_ClassifierSelfCheck 让门禁 B 的判据**能变红**。
//
// 没有它，一个写坏的扫描器（比如正则永远匹配不到）会让门禁恒绿 ——
// 那是 vacuous pass，本仓已有过这个教训（TestUnifiedDataRecreateGate_ClassifierSelfCheck 同款）。
func TestPartitionTickerGate_ClassifierSelfCheck(t *testing.T) {
	// 反例：continue 被挪到 EnsurePartitionsFor 之后。
	bad := `ensurePartitions := func() {
		for _, table := range tables {
			if err := pm.EnsurePartitionsFor(table, 3); err != nil {
				logger.Errorf("x")
			}
			if !datalifecycle.IsTablePartitioned(db, table) {
				continue
			}
		}
	}`
	if judgeTickerOrder(bad) != tickerOrderBroken {
		t.Fatal("判据对「continue 在 Ensure 之后」的反例没有判红 —— 门禁 B 是 vacuous pass")
	}
	// 正例：当前形态。
	good := `ensurePartitions := func() {
		for _, table := range tables {
			if !datalifecycle.IsTablePartitioned(db, table) {
				continue
			}
			if err := pm.EnsurePartitionsFor(table, 3); err != nil {
				logger.Errorf("x")
			}
		}
	}`
	if judgeTickerOrder(good) != tickerOrderOK {
		t.Fatal("判据把当前正确形态判红了 —— 门禁 B 会误报")
	}
}

// tickerOrder 是门禁 B 的分类结果。
type tickerOrderDecision int

const (
	tickerOrderOK tickerOrderDecision = iota
	tickerOrderBroken
	tickerOrderNoGuard
	tickerOrderNoEnsure
)

// judgeTickerOrder 是门禁 B 的**纯函数判据**（拆出来才能自检）。
func judgeTickerOrder(body string) tickerOrderDecision {
	guardPos := strings.Index(body, "IsTablePartitioned(")
	ensurePos := strings.Index(body, "EnsurePartitionsFor(")
	switch {
	case guardPos < 0:
		return tickerOrderNoGuard
	case ensurePos < 0:
		return tickerOrderNoEnsure
	case guardPos > ensurePos:
		return tickerOrderBroken
	default:
		return tickerOrderOK
	}
}
