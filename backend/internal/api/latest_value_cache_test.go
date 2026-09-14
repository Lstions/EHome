package api

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// 数据层时序化 (v3.4 §3.2.4) 单测: latestValueCache 命中/miss + precision 决策。

func TestLatestValueCacheSetAndHit(t *testing.T) {
	edgeID := uint(4242)
	rec := models.UnifiedData{
		DeviceID:     edgeID,
		SensorName:   "temperature",
		Value:        25.5,
		Timestamp:    time.Now(),
		EdgeDeviceID: &edgeID,
	}
	SetLatestValue(rec)

	got, ok := LatestValue(edgeID)
	if !ok {
		t.Fatal("expected cache hit after SetLatestValue")
	}
	if got.SensorName != "temperature" || got.Value != 25.5 {
		t.Errorf("got %+v, want sensor=temperature value=25.5", got)
	}
}

func TestLatestValueCacheMiss(t *testing.T) {
	if _, ok := LatestValue(999999); ok {
		t.Error("expected cache miss for unknown device")
	}
}

func TestLatestValueCacheNilEdgeDeviceSkipped(t *testing.T) {
	// EdgeDeviceID 为 nil 的记录不应 panic 也不入缓存。
	SetLatestValue(models.UnifiedData{DeviceID: 1, SensorName: "x"})
	if _, ok := LatestValue(0); ok {
		t.Error("nil edge_device_id record must not enter cache")
	}
}

func TestLatestValueCacheOverwrite(t *testing.T) {
	edgeID := uint(4243)
	first := models.UnifiedData{DeviceID: edgeID, SensorName: "v", Value: 1, EdgeDeviceID: &edgeID}
	SetLatestValue(first)
	second := models.UnifiedData{DeviceID: edgeID, SensorName: "v", Value: 2, EdgeDeviceID: &edgeID}
	SetLatestValue(second)

	got, ok := LatestValue(edgeID)
	if !ok || got.Value != 2 {
		t.Errorf("expected overwrite to value=2, got %+v ok=%v", got, ok)
	}
}

// TestPrecisionFor 已随 precisionFor 一并删除 (2026-09-14 裁决):
// 该路由函数零生产调用者, 且其 rollup 分支在生产查询协议下不可达。
// 详见 docs/分析/rollup-读取路径裁决-2026-09-14.md §1.1。
