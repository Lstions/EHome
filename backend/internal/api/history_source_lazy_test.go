package api

// history_source_lazy_test.go — 未注入字典时的**惰性装载**（生产实际路径）。
//
// ============ 为什么这条必须有 ============
//
// `SetHistoryFrameDictionary` 需要 main.go 接线，而 main.go 不在本任务的写范围内。
// 如果只依赖注入，生产上就是「代码在、开关开、但字典为 nil ⇒ 帧表读路径永远
// 不可用」—— 接了个不通电的线，而且不会有任何报错，最难发现的那种。
//
// 所以 frameDictionaryFor 实现了惰性装载兜底。本条用例守的是：**不注入也能用**。
//
// 同时守第二条：注入**优先于**惰性（production 共用 main.go 那份字典，避免
// "解码用一套 slot 规格、编码用另一套"的静默错数）。

import (
	"testing"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// TestFrameDictionaryLazyLoadsWithoutInjection 守「没注入也能解码」。
func TestFrameDictionaryLazyLoadsWithoutInjection(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	SetHistoryFrameDictionary(nil)
	ResetLazyFrameDictionary()
	defer func() {
		SetHistoryFrameDictionary(nil)
		ResetLazyFrameDictionary()
	}()

	if dict := frameDictionaryFor(db); dict == nil {
		t.Fatal("未注入时 frameDictionaryFor 返回 nil —— 生产上帧表读路径会永远不可用")
	}
	// 幂等：第二次必须返回同一实例（不重复 WarmUp / 不重复占内存）。
	first := frameDictionaryFor(db)
	second := frameDictionaryFor(db)
	if first != second {
		t.Fatal("惰性字典不是单例 —— 每次请求都全量 WarmUp 会把 telemetry_slot 扫爆")
	}
}

// TestFrameDictionaryInjectionWins 守「注入优先于惰性」。
func TestFrameDictionaryInjectionWins(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	ResetLazyFrameDictionary()
	injected := telemetry.NewMemorySlots()
	SetHistoryFrameDictionary(injected)
	defer func() {
		SetHistoryFrameDictionary(nil)
		ResetLazyFrameDictionary()
	}()

	if got := frameDictionaryFor(db); got != injected {
		t.Fatal("注入的字典未被优先使用 —— 会导致解码规格与编码侧不一致（静默错数）")
	}
}
