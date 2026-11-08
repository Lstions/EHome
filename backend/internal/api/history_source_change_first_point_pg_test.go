package api

// history_source_change_first_point_pg_test.go — change 语义的**首点契约**
// 独立断言（2026-11-08，第三方复核产物）。
//
// ============ 这条断言守的是什么 ============
//
// change 模式的契约是「首点必出」—— 它是窗口内第一个采样值，
// 没有前驱可比，所以必然被视为「发生了变化」。
//
// SQL 侧靠 lag() 表达：首行 lag() 为 NULL，而 `NULL IS DISTINCT FROM 0` 恒为 true
// ⇒ 首点必出。Go 侧靠 `i == 0` 短路。两条路**实现机制完全不同**。
//
// ============ 为什么必须单独钉住 ============
//
// 因为**现有用例一条都不会为它变红**：所有对拍用例都是「两边相等」，
// 而两边一起错、或两边都恰好对，都照样绿。
//
// 先说清一件事：两边的**机制不同，但结构上都保证首点必出** ——
//
//	Go : if i == 0 || raw[i].Value != prev
//	     i==0 短路**优先**，所以 prev 的零值初值永远不会被读到。
//	     首点必出；之后「值未变」的点不出。
//
//	SQL: lag() 首行 NULL => NULL IS DISTINCT FROM v 恒 true => 首点必出；
//	     次行起 lag = 前值 => 值未变时 IS DISTINCT FROM 为 false => 不出。
//
// 所以这不是「碰巧一致」，而是两个不同机制在语义上都正确。
// 本用例的价值不是「怀疑它们不一致」，而是**把这条契约独立钉住**：
// 断言不经过 SQL 路径 ⇒ 将来任一侧改坏了，这里会红，而不会因为
// 「两边一起改坏」或「对拍用例只比相等」而漏掉。
//
// 所以本文件的断言**不依赖任何 SQL 路径**，直接验契约：
//
//	· 首点必须存在，且时间戳 == 窗口内第一个采样的时间戳；
//	· 全零序列（首点 0、后继全 0）在 change 模式下必须**恰好 1 个点**。
//
// 第二条同时封死两种失效：
//	· 丢了首点      => 0 个点 => 红
//	· 没做 change 过滤 => N 个点 => 红
// 缺了「恰好」这个量词，两种失效中的一种就会漏掉。

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

// TestFrameSource_ChangeKeepsFirstPointWhenZero 直接用 frameSource，不碰 unified_data。
//
// 为什么连 SQL 路径都不建：本用例要验的是**帧表侧自己的契约**。
// 若它依赖对拍，那它就是「两边相等」的又一个副本 —— 而两边一起错照样绿。
func TestFrameSource_ChangeKeepsFirstPointWhenZero(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")

	// rsoc 的 spec 是 Min=0 Step=1（internal/telemetry/spec.go），
	// 所以 0 是它的**精确网格点**，量化无损 —— 这一点很重要：
	// 若用不可表达的精度，帧表侧解码回来的值就不是 0，「首点为 0」这个前提根本不成立。
	const dev = 9811
	start := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	const n = 40

	// 全零序列：首点 0、后继全 0。
	// 这是最能暴露「首点是否靠 i==0 短路保留」的构造 ——
	// 因为 prev 的零值恰好等于所有值，任何「靠 prev 初值区分首点」的实现都会丢首点。
	var frames []models.TelemetryFrame
	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("warm up slots: %v", err)
	}
	enc := telemetry.NewEncoder(telemetry.EncoderConfig{KeyFrameInterval: 60 * time.Second}, dict)
	for i := 0; i < n; i++ {
		ts := start.Add(time.Duration(i) * time.Second)
		frame, err := enc.EncodeFrame(dev, ts.UTC(), 0, []telemetry.Reading{{Name: "rsoc", Value: 0}})
		if err != nil {
			t.Fatalf("encode frame %d: %v", i, err)
		}
		frames = append(frames, models.TelemetryFrame{
			DeviceID: frame.DeviceID, TS: frame.TS, Seq: frame.Seq,
			FrameKind: frame.FrameKind, SlotID: frame.SlotID, Payload: frame.Payload,
		})
	}
	if err := db.Session(&gorm.Session{}).CreateInBatches(frames, 200).Error; err != nil {
		t.Fatalf("persist frames: %v", err)
	}

	src := frameSource{db: db, dict: dict}
	got, err := src.Points(context.Background(), historyPointQuery{
		EdgeDeviceID: dev, Category: "rsoc",
		Start: start, End: start.Add(n * time.Second), MaxPoints: 100, Mode: AggModeChange,
	})
	if err != nil {
		t.Fatalf("frameSource.Points: %v", err)
	}

	// ── 断言 1：首点必须存在，且是窗口内第一个采样的时间戳 ──
	//
	// 这条**不依赖 SQL 路径**，所以它验的是「结构上必然」而非「碰巧一致」。
	if len(got) == 0 {
		t.Fatal("change 路径返回 0 点 —— 丢了首点。" +
			"契约：change 保留首点 + 每个值变化的点；首点没有前驱，必然算变化")
	}
	if !got[0].Timestamp.Equal(start) {
		t.Fatalf("change 路径首点时间戳 = %v，want %v —— 首点不是窗口内第一个采样"+
			"（可能被错当成后续点跳过，或顺序被打乱）", got[0].Timestamp, start)
	}
	if got[0].Value != 0 {
		t.Fatalf("首点值 = %v，want 0（量化应无损：rsoc spec Min=0 Step=1）", got[0].Value)
	}

	// ── 断言 2：全零序列在 change 模式下**恰好 1 个点** ──
	//
	// 「恰好」这两个字是判据的核心：
	//   丢了首点         => 0 点 => 红
	//   没做 change 过滤  => 40 点 => 红
	// 只断言「至少 1 个点」会漏掉后者，只断言「首点存在」会漏掉前者。
	if len(got) != 1 {
		t.Fatalf("全零序列在 change 模式下返回 %d 点，want 恰好 1 点。"+
			"\n\t若为 0 点：丢了首点（prev 零值 == 所有值，靠 prev 初值区分首点的实现会丢）；"+
			"\n\t若为 %d 点：没做 change 过滤（每个点都被当成变化）", len(got), n)
	}
	t.Logf("change 首点契约成立：全零序列 %d 行 => 恰好 1 点 @ %v", n, got[0].Timestamp)
}
