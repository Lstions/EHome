package api

// 启动回填（WarmupLatestValues）的**接线与行为**回归锁。
//
// ===== 为什么需要这个文件（一个真实教训）=====
// `WarmupLatestValues` 自 2026-08-21 引入以来**从未被任何生产代码调用**，
// 而且**没有任何测试**。它就这样活了约 4 周，直到 2026-09-16 主控全仓 grep 才发现。
//
// 机械原因（已实测）：初版把参数声明成自定义窄接口
// `gormDB interface { Raw(string, ...interface{}) queryResult }`，
// 而 `*gorm.DB.Raw` 的真实签名返回 `*gorm.DB` —— **返回类型不同，接口不被满足**，
// 于是 `api.WarmupLatestValues(db)` 根本无法编译，接线的人一编译就会撞墙，
// 大概就此搁置（实施计划 §3.2.4 却明确要求「启动回填」，其验收项也一直未勾选）。
//
// ⇒ **本文件的教训性结论**：一个「导出但无调用者、也无测试」的函数，
//   在 grep 之前与「已接线」在编译期和测试期都**没有任何区别**。故必须有断言钉住「有人在调」。
//
// ===== 本文件守什么 =====
//   1. 回填**行为**正确：多物理量全部进缓存、last 取 created_at 最新者、DeviceID=0 跳过；
//   2. **接线**存在：源码里 `WarmupLatestValues()` 被调用，且数据来源已被注入
//      （这是「无调用者」缺陷的直接防线，纯行为测试抓不到）；
//   3. 未注入来源时为 no-op（不改坏既有行为）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// TestWarmupLatestValuesFrom_FillsAllSensorsAndNewestLast 守回填的行为。
func TestWarmupLatestValuesFrom_FillsAllSensorsAndNewestLast(t *testing.T) {
	resetLatestValueCacheForTest()
	t.Cleanup(resetLatestValueCacheForTest)

	old := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	newer := old.Add(time.Minute)

	WarmupLatestValuesFrom([]models.UnifiedData{
		{DeviceID: 1, SensorName: "voltage", Value: 51, CreatedAt: old},
		{DeviceID: 1, SensorName: "current", Value: 2, CreatedAt: old},
		{DeviceID: 1, SensorName: "rsoc", Value: 80, CreatedAt: newer},
		{DeviceID: 0, SensorName: "ignored", Value: 1, CreatedAt: newer}, // DeviceID=0 必须跳过
	})

	got := LatestValues(1)
	if len(got) != 3 {
		t.Fatalf("回填后应有 3 个物理量，实际 %d", len(got))
	}
	last, ok := LatestValue(1)
	if !ok {
		t.Fatal("回填后 LatestValue 应命中")
	}
	if last.SensorName != "rsoc" {
		t.Errorf("LatestValue 应返回 created_at 最新者（rsoc），实际 %s", last.SensorName)
	}
	if _, ok := LatestValue(0); ok {
		t.Error("DeviceID=0 的行不应进缓存")
	}
}

// TestWarmupLatestValues_NoSourceIsNoop 钉住「未注入来源 = 安全 no-op」。
func TestWarmupLatestValues_NoSourceIsNoop(t *testing.T) {
	resetLatestValueCacheForTest()
	t.Cleanup(resetLatestValueCacheForTest)
	oldSource := warmupSource
	SetLatestValuesSource(nil)
	t.Cleanup(func() { SetLatestValuesSource(oldSource) })

	if n := WarmupLatestValues(); n != 0 {
		t.Errorf("未注入来源时应回填 0 行，实际 %d", n)
	}
}

// TestWarmupLatestValues_UsesInjectedSource 证明来源真的被用上（不是摆设）。
func TestWarmupLatestValues_UsesInjectedSource(t *testing.T) {
	resetLatestValueCacheForTest()
	t.Cleanup(resetLatestValueCacheForTest)
	oldSource := warmupSource
	t.Cleanup(func() { SetLatestValuesSource(oldSource) })

	SetLatestValuesSource(func() ([]models.UnifiedData, error) {
		return []models.UnifiedData{
			{DeviceID: 42, SensorName: "a", Value: 1, CreatedAt: time.Now()},
			{DeviceID: 42, SensorName: "b", Value: 2, CreatedAt: time.Now()},
		}, nil
	})
	if n := WarmupLatestValues(); n != 2 {
		t.Fatalf("应回填 2 行，实际 %d", n)
	}
	if got := LatestValues(42); len(got) != 2 {
		t.Errorf("注入来源应被真正使用，缓存中有 %d 项，期望 2", len(got))
	}
}

// TestWarmupLatestValues_IsWiredInProduction 是**接线判据**（纯行为测试抓不到「无人调用」）。
//
// 为什么要这条：本函数的缺陷形态就是「实现了但没人调」，而那种状态在任何行为测试下都是绿的。
// 故必须直接断言「生产启动路径里确实调了它」。
func TestWarmupLatestValues_IsWiredInProduction(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "cmd", "server", "main.go"))
	if err != nil {
		t.Fatalf("读取 cmd/server/main.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "api.WarmupLatestValues()") {
		t.Error("cmd/server/main.go 未调用 api.WarmupLatestValues() —— " +
			"启动回填不会发生（这正是该函数自 2026-08-21 起的状态）")
	}
	if !strings.Contains(s, "api.SetLatestValuesSource(") {
		t.Error("cmd/server/main.go 未注入数据来源 —— WarmupLatestValues 会是 no-op")
	}
}
