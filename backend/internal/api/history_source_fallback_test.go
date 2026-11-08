package api

// history_source_fallback_test.go — 「帧表不可用」的四种形态在硬切换后怎么处置。
//
// ============ 这个文件整体重写过（2026-11-08）============
//
// 原文件守的是「帧表未覆盖 ⇒ 回落 unified_data」，那是**删老分区的安全前提**。
// 硬切换之后回落链整个消失，所以「回落」这件事不再存在 —— 但那些形态**仍然存在**，
// 只是处置不同了：
//
//	原形态                                   硬切换后
//	① 帧表完全没有该时段的帧                   => 返回 frameSource，点集为空（设备没数据）
//	② 帧表只覆盖窗口的一部分                   => **不再检查**（覆盖判据已删，见下）
//	③ 帧表存在但解码失败（字典落后 / payload 坏）=> **返回 error**（不再静默回落）
//	④ 完整覆盖                                => 返回 frameSource（唯一数据源）
//
// ============ 为什么 ② 是「删」而不是「改成相反断言」============
//
// ② 验的是「部分覆盖」这个**状态**，而那个状态在新架构里不再被检查：
// 覆盖判据（probeFrameCoverage/frameCovered）整个删掉了，因为它的第一性目的
// 是「不覆盖就回落」，而回落没了之后它只会把「超限」「头部未覆盖」都误判成
// 「返回空」。见 history_source.go 的 selectHistorySource 注释。
//
// 「把 FallsBack 改成 NotFallsBack」是错的 —— 那会留下一条**验一个不存在的机制**
// 的用例，而且它会一直是绿的（因为那个机制不在了，什么都不发生）。

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// setupFallbackDB 建帧表并清空，返回可用连接。
func setupFallbackDB(t *testing.T) *gorm.DB {
	t.Helper()
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	// ============ 必须重置解码缓存（2026-11-08，既有缺口）============
	//
	// frameDecodeKey 是 {DeviceID, AllowEmpty, From, To, Scope} —— **不含 dict**。
	// 于是「同一 (设备,窗口,scope,allowEmpty) 但不同字典」的两条用例会命中彼此的缓存：
	//
	//	TestSelectSourceAlwaysReturnsFrames  用真字典解出 240 点 => 写进缓存
	//	TestDecodeFailureReturnsError        用空字典（期望解码失败）=> key 相同 => 命中缓存
	//	                                    => 不走解码 => 没有 error => 用例红
	//
	// 这是**用例间污染**，不是断言错。history_source_share_test.go 早就单独重置过，
	// 只是没推广成基建 —— 这里补上。
	//
	// ⚠ 根治是把 dict 纳入 key（字典热重载时生产也会踩同一个坑），已登记待办；
	//   在根治之前，任何「同一窗口换字典」的用例都必须显式重置。
	ResetFrameDecodeCache()
	t.Cleanup(ResetFrameDecodeCache)
	return db
}

// TestSelectSourceWithNoFramesStillReturnsFrames 是原 TestFrameCoverageNotCoveredFallsBack 的替代。
//
// 原断言：「窗口内没有帧 ⇒ 不选帧表 ⇒ 回落 unified_data」。
// 新语义：「窗口内没有帧」= 设备那段时间没上报 = **正常**，返回空点集。
//
// 判据变了但目的没变：**不能把「没数据」变成「报错」** —— 那会把设备的正常静默
// 变成接口故障。所以这里断言「不报错 + 点集为空」，而不是「报错」。
func TestSelectSourceWithNoFramesStillReturnsFrames(t *testing.T) {
	db := setupFallbackDB(t)
	SetHistoryFrameDictionary(telemetry.NewDBSlots(db))
	defer SetHistoryFrameDictionary(nil)

	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	q := historyPointQuery{
		EdgeDeviceID: 14, Category: "current",
		Start: start, End: start.Add(time.Hour), MaxPoints: 500, Mode: AggModeAvg,
	}
	src, err := selectHistorySource(context.Background(), db, q)
	if err != nil {
		t.Fatalf("帧表无帧时选源报错: %v —— 设备静默是正常状态，不是故障", err)
	}
	if src.Name() != "telemetry_frame" {
		t.Fatalf("数据源 = %q, want telemetry_frame（硬切换后它是唯一来源）", src.Name())
	}
	points, err := src.Points(context.Background(), q)
	if err != nil {
		t.Fatalf("帧表路径报错: %v（无帧应当返回空点集，不是错误）", err)
	}
	if len(points) != 0 {
		t.Fatalf("无帧时点集 = %d 点，want 0", len(points))
	}
}

// TestSelectSourceAlwaysReturnsFrames 是原 TestFrameCoverageFullSelectsFrames 的替代。
//
// 保留它的理由和原用例一样：**没有正向见证，上面那条可以被「永远返回空」的
// 假实现满足**。这里造真实帧，断言能解出点。
func TestSelectSourceAlwaysReturnsFrames(t *testing.T) {
	db := setupFallbackDB(t)
	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("warm up: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)

	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	enc := telemetry.NewEncoder(telemetry.EncoderConfig{KeyFrameInterval: 60 * time.Second}, dict)
	var frames []models.TelemetryFrame
	for i := 0; i < 240; i++ {
		ts := start.Add(time.Duration(i) * 15 * time.Second)
		if !ts.Before(end) {
			break
		}
		f, err := enc.EncodeFrame(14, ts, 0, []telemetry.Reading{{Name: "current", Value: -12.5 + float64(i%7)*0.01}})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		frames = append(frames, models.TelemetryFrame{
			DeviceID: f.DeviceID, TS: f.TS, Seq: f.Seq,
			FrameKind: f.FrameKind, SlotID: f.SlotID, Payload: f.Payload,
		})
	}
	if err := db.Session(&gorm.Session{}).CreateInBatches(frames, 100).Error; err != nil {
		t.Fatalf("persist frames: %v", err)
	}

	q := historyPointQuery{
		EdgeDeviceID: 14, Category: "current",
		Start: start, End: end, MaxPoints: 500, Mode: AggModeAvg,
	}
	src, err := selectHistorySource(context.Background(), db, q)
	if err != nil {
		t.Fatalf("选源报错: %v", err)
	}
	if src.Name() != "telemetry_frame" {
		t.Fatalf("数据源 = %q, want telemetry_frame（否则读路径根本没接线）", src.Name())
	}
	points, err := src.Points(context.Background(), q)
	if err != nil {
		t.Fatalf("帧表路径取点失败: %v", err)
	}
	if len(points) == 0 {
		t.Fatal("帧表路径返回空点集（有帧却解不出数据）")
	}
}

// TestDecodeFailureReturnsError 是原 TestFrameSourceDecodeFailureFallsBack 的替代。
//
// ============ 这条是**行为变更**的显式证据 ============
//
// 原语义：选源成功但解码失败 => 回落 unified_data（「宁可慢也给对图」）。
// 新语义：**解码失败就失败，返回 error**（上层 500）。
//
// 为什么变：
//
//	· 回落目标不存在了（DROP unified_data）；
//	· 更根本的是**静默回落会把「存储故障」伪装成「没数据」** ——
//	  用户看到空图不会报障，运维不知道存储坏了。500 会立刻被发现。
//
// 构造：注入一份**空的**字典 ⇒ 帧的 slot_id 在它里面找不到 ⇒ 解码全跳过 ⇒
// 「有帧（FramesSeen>0）但 0 点解出」⇒ 触发 allowEmpty=false 的失败判据。
func TestDecodeFailureReturnsError(t *testing.T) {
	db := setupFallbackDB(t)

	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	writer := telemetry.NewDBSlots(db)
	if err := writer.WarmUp(); err != nil {
		t.Fatalf("warm up writer: %v", err)
	}
	enc := telemetry.NewEncoder(telemetry.EncoderConfig{KeyFrameInterval: 60 * time.Second}, writer)
	var frames []models.TelemetryFrame
	for i := 0; i < 240; i++ {
		ts := start.Add(time.Duration(i) * 15 * time.Second)
		if !ts.Before(end) {
			break
		}
		f, err := enc.EncodeFrame(14, ts, 0, []telemetry.Reading{{Name: "current", Value: -12.5}})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		frames = append(frames, models.TelemetryFrame{
			DeviceID: f.DeviceID, TS: f.TS, Seq: f.Seq,
			FrameKind: f.FrameKind, SlotID: f.SlotID, Payload: f.Payload,
		})
	}
	if err := db.Session(&gorm.Session{}).CreateInBatches(frames, 100).Error; err != nil {
		t.Fatalf("persist frames: %v", err)
	}
	// 空字典 ⇒ 所有帧的 slot 都找不到。
	SetHistoryFrameDictionary(telemetry.NewMemorySlots())
	defer SetHistoryFrameDictionary(nil)

	q := historyPointQuery{
		EdgeDeviceID: 14, Category: "current",
		Start: start, End: end, MaxPoints: 500, Mode: AggModeAvg,
	}
	points, err := queryHistoryPointsWithFallback(context.Background(), db, q)
	if err == nil {
		t.Fatalf("解码全跳过时返回了 %d 点且无错误 —— "+
			"硬切换后不再回落：静默返回空会把「存储/字典故障」伪装成「没数据」", len(points))
	}
	t.Logf("解码失败如实报错（不再回落）：%v", err)
}

// TestSelectSourceUnavailableErrorIsActionable 锁「不可用错误必须带用户能做的动作」。
//
// 判据（Lead 裁决）：任何面向用户的错误，必须包含「用户能做的动作」。
// 技术参数可以保留，但要放在动作之后。
func TestSelectSourceUnavailableErrorIsActionable(t *testing.T) {
	db := setupFallbackDB(t)

	cases := []struct {
		name string
		q    historyPointQuery
		set  func()
		want string
	}{
		{
			name: "开关被关",
			q: historyPointQuery{EdgeDeviceID: 14, Category: "current",
				Start: time.Now().Add(-time.Hour), End: time.Now(), MaxPoints: 100, Mode: AggModeAvg},
			set:  func() { SetFrameReadEnabled(false) },
			want: "打开该开关",
		},
		{
			name: "窗口超过解码上限",
			q: historyPointQuery{EdgeDeviceID: 14, Category: "current",
				Start: time.Now().Add(-48 * time.Hour), End: time.Now(), MaxPoints: 100, Mode: AggModeAvg},
			want: "请缩小到",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set != nil {
				tc.set()
				defer SetFrameReadEnabled(true)
			}
			SetHistoryFrameDictionary(telemetry.NewDBSlots(db))
			defer SetHistoryFrameDictionary(nil)

			_, err := selectHistorySource(context.Background(), db, tc.q)
			if err == nil {
				t.Fatal("期望返回不可用错误，实际 nil")
			}
			if !containsAction(err.Error(), tc.want) {
				t.Fatalf("错误信息缺少可执行动作 %q：%v", tc.want, err)
			}
			t.Logf("错误信息：%v", err)
		})
	}
}

// containsAction 是 strings.Contains 的薄封装（本文件只需这一处，不值得为一处 import strings）。
func containsAction(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
