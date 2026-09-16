package api

// 最新值缓存**丢失同一设备的多物理量**（回归锁）。
//
// ===== 缺陷（2026-09-16 实测）=====
// 生产者 `SensorParserConsumer` 对**一帧**解析出的 N 个物理量逐条调用 `latestSink`
// （`consumers_heavy.go` 的 `for i := range records { c.latestSink(records[i]) }`），
// 但本缓存按 `DeviceID` 单键存储（`entries[rec.DeviceID] = rec`）——
// **后写覆盖先写**，于是一帧 14 个物理量最后只剩 1 个。
//
// 实测证据（运行中的 :8082 实例）：
//   · 走**回落 SQL** 的设备 7053 返回 **14** 个物理量；
//   · 而**缓存命中**时只返回 **1** 个。
//   即 `/overview` 的 `latest_data` 形状**随缓存冷热而变**，同一份数据两种结果。
//
// 为什么这是**数据完整性**问题而非仅「形状不一致」：
//   缓存 miss 时用户看到 14 个传感器，命中时只剩 1 个 —— 数据**看起来消失了**。
//   `docs/设计/场景仿真验证框架.md` §13.1 把它记为「产品语义裁决」（该表达什么），
//   但按代码事实，缓存本就**无法**表达「每设备多个物理量」，属于实现缺陷。
//
// ===== 本用例守什么 =====
// 只守**纯缓存层**的可表达性：同一设备写入多个物理量后，缓存必须能把它们**全部**取回。
// 这是修复的必要条件，且与 handler 如何消费无关（handler 侧由既有握手测试覆盖）。

import (
	"testing"

	"ehome/backend/internal/models"
)

// TestLatestValueCache_KeepsAllSensorsOfADevice 是本缺陷的核心断言。
func TestLatestValueCache_KeepsAllSensorsOfADevice(t *testing.T) {
	resetLatestValueCacheForTest()
	t.Cleanup(resetLatestValueCacheForTest)

	const devID = uint(4242)
	// 模拟一帧解析出的 3 个物理量（与真实 BMS 的 14 个同形，取 3 个便于断言）。
	for _, s := range []struct {
		name string
		val  float64
	}{{"total_voltage", 51.2}, {"current", 2.5}, {"rsoc", 80}} {
		SetLatestValue(models.UnifiedData{DeviceID: devID, SensorName: s.name, Value: s.val})
	}

	got := LatestValues(devID)
	if len(got) != 3 {
		t.Errorf("缓存只保留 %d 个物理量，期望 3 —— "+
			"一帧解析出的多物理量被后写覆盖，用户会看到数据\"消失\"（缓存命中 vs miss 形状不同）", len(got))
	}
	seen := map[string]float64{}
	for _, r := range got {
		seen[r.SensorName] = r.Value
	}
	for name, want := range map[string]float64{"total_voltage": 51.2, "current": 2.5, "rsoc": 80} {
		if v, ok := seen[name]; !ok {
			t.Errorf("物理量 %s 在缓存里丢失", name)
		} else if v != want {
			t.Errorf("物理量 %s = %v，期望 %v", name, v, want)
		}
	}
}

// TestLatestValueCache_SingleValueReadStillWorks 是**反向对照**：
// `LatestValue`（单条）必须仍返回**最后写入**的那条 —— 自动化引擎的 F4 条件复核依赖它。
// 只测「多值可表达」而不管单值语义，会破坏 `checkConditionsStillSatisfied` 的前提。
func TestLatestValueCache_SingleValueReadStillWorks(t *testing.T) {
	resetLatestValueCacheForTest()
	t.Cleanup(resetLatestValueCacheForTest)

	SetLatestValue(models.UnifiedData{DeviceID: 7, SensorName: "a", Value: 1})
	SetLatestValue(models.UnifiedData{DeviceID: 7, SensorName: "b", Value: 2})

	rec, ok := LatestValue(7)
	if !ok {
		t.Fatal("LatestValue 应命中")
	}
	if rec.SensorName != "b" || rec.Value != 2 {
		t.Errorf("LatestValue 应返回最后写入的那条（b=2），实际 %s=%v", rec.SensorName, rec.Value)
	}
}

// TestLatestValueCache_UnknownDeviceMisses 钉住 miss 语义不变。
func TestLatestValueCache_UnknownDeviceMisses(t *testing.T) {
	resetLatestValueCacheForTest()
	t.Cleanup(resetLatestValueCacheForTest)
	if _, ok := LatestValue(999); ok {
		t.Error("未写入过的设备应 miss")
	}
}
