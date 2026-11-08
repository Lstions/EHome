package api

// sensor_fields_preview_frames_pg_test.go — /sensor-fields/preview 换源的验收（2026-11-08）。
//
// ============ 守什么 ============
//
// 停写之后 preview 从 unified_data 读会**恒空**，而响应形状不变（samples_considered=0,
// frames=[]）。前端据此显示「规则不匹配 / 没有数据」—— 用户会去改 selector，
// 而真正的原因是存储换源了。**这是把系统变更伪装成用户配置错误**，必须守住。
//
// 变异验证：删掉 previewRowsFromFrames 的帧表分支 => 用例 1 红。

import (
	"testing"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// previewRowsFromFrames 的单元级验收：不经过 HTTP，直接验「帧表能给出行」。
//
// 为什么分两层：HTTP 层要注入 resolver（selector 编译）才能跑，耦合重；
// 而「数据源能不能给出行」这件事不需要 resolver。分层后这条断言不受
// resolver 的可用性影响 —— 那正是本仓「契约在哪一层，就在哪一层测」的作法。
func TestPreviewRowsFromFrames_ServesWhenUnifiedIsEmpty(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	db.Exec("DELETE FROM unified_data")
	ResetDeviceUnitCache()
	defer ResetDeviceUnitCache()

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)

	const dev = 9961
	fields := []string{"current", "rsoc"}
	start := time.Now().UTC().Add(-30 * time.Minute)
	seedFramesOnlyFields(t, db, dev, start, start.Add(2*time.Minute), dict, fields, 10*time.Second)

	rows, ok := previewRowsFromFrames(db, dev, start.Add(-time.Minute), fields)
	if !ok {
		t.Fatal("帧表有数据时 previewRowsFromFrames 必须 ok=true（否则 preview 会回落到" +
			"已停写的 unified_data，用户看到 samples_considered=0 而以为 selector 配错了）")
	}
	if len(rows) == 0 {
		t.Fatal("previewRowsFromFrames 返回 0 行，但帧表刚造了 12 帧")
	}
	for _, r := range rows {
		if r.Timestamp.Before(start.Add(-time.Second)) || r.Timestamp.After(time.Now().Add(time.Second)) {
			t.Fatalf("行时间戳 %s 不在造数区间内", r.Timestamp.Format(time.RFC3339))
		}
		if r.DeviceID != dev {
			t.Fatalf("行 device_id = %d, want %d", r.DeviceID, dev)
		}
	}
}

// 帧表无数据的设备：必须 ok=false，让调用方回落，而不是返回空正文。
func TestPreviewRowsFromFrames_UnavailableWhenNoFrames(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	ResetDeviceUnitCache()
	defer ResetDeviceUnitCache()

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)

	rows, ok := previewRowsFromFrames(db, 9971, time.Now().Add(-time.Hour), []string{"current"})
	if ok {
		t.Fatalf("帧表无该设备时必须 ok=false（调用方要回落 unified_data），got %d 行", len(rows))
	}
}
