package databus

// telemetry_sink_wiring_test.go — L2 双写 / L3 rollup 旁路 sink 的接线门禁
// （接线 patch: docs/设计/遥测存储-L2双写接线patch-2026-11-06.md §2；
//   docs/分析/遥测rollup-L3-接线patch-2026-11-06.md §3）。
//
// # 本文件守什么
//
// 两个 sink **必须挂在「解析成功路径」上**，而不是「写库成功」分支。这条如果写反：
//   · L2：L1 开启 write_on_change 后被跳过的帧不进帧表 ⇒ 增量链出现空洞，
//     60s 关键帧窗口内的变化值**永久丢失**（数据损失，不是压缩损失）；
//   · L3：被跳过的帧不进分钟桶 ⇒ rollup 的 avg/min/max 漏样本，与原始表口径漂移，
//     而且**没有任何告警**会暴露（图表看起来正常，只是数字偏了）。
//
// 两种错误接法在「L1 关着」时都**完全看不出来**（每帧都写库，谁接都一样）。
// 所以用例必须把 L1 打开、让一帧被跳过，再断言 sink 仍被调用 —— 这是唯一能区分
// 「接对了」与「接反了」的构造。
//
// 另外守三条共同不变量：nil 即跳过（默认关闭零成本）、传参口径正确
// （device.ID / 同一个 now / commandTemplateID）、不因旁路存在而改变落库行为。

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/pkg/parser"
)

// TestTelemetryRollupSink_RunsOnSkippedFrame 守 L3：L1 跳过入库的帧仍要进分钟桶。
func TestTelemetryRollupSink_RunsOnSkippedFrame(t *testing.T) {
	db := newWriteGateTestDB(t)
	node, device := seedWriteGateDevice(t, db, "node-sink-rollup")

	consumer, _ := newWriteGateConsumer(t, db, map[string]float64{"voltage": 12})
	consumer.SetTelemetryHeartbeat(time.Hour)

	var (
		calls      int
		gotDevices []uint
		gotFields  []int
		gotAts     []time.Time
	)
	consumer.SetTelemetryRollupSink(func(edgeDeviceID uint, fields []parser.Field, at time.Time) {
		calls++
		gotDevices = append(gotDevices, edgeDeviceID)
		gotFields = append(gotFields, len(fields))
		gotAts = append(gotAts, at)
	})

	consumer.Handle(frame(node.NodeID, device.ID)) // 首帧: 必写库
	consumer.Handle(frame(node.NodeID, device.ID)) // 同值: 被 L1 跳过
	consumer.Handle(frame(node.NodeID, device.ID)) // 同值: 被 L1 跳过

	if rows := countUnifiedRows(t, db, device.ID); rows != 1 {
		t.Fatalf("unified_data 行数 = %d, want 1（L1 应跳过 2 帧）", rows)
	}
	if calls != 3 {
		t.Errorf("rollup sink 调用次数 = %d, want 3 —— 被 L1 跳过的帧仍是真实采样，"+
			"必须进分钟桶（否则 rollup 的 avg/min/max 漏样本，与原始表口径漂移且无告警）", calls)
	}
	for i, id := range gotDevices {
		if id != device.ID {
			t.Errorf("第 %d 次调用 deviceID = %d, want %d（必须是 EdgeDevice.ID，"+
				"即 unified_data.device_id 口径）", i, id, device.ID)
		}
	}
	for i, n := range gotFields {
		if n != 1 {
			t.Errorf("第 %d 次调用 fields 数 = %d, want 1（应传本帧解析出的全部物理量）", i, n)
		}
	}
	for i, at := range gotAts {
		if at.IsZero() {
			t.Errorf("第 %d 次调用 at 为零值 —— 必须传 unified_data.timestamp 用的同一个 now", i)
		}
	}
}

// TestTelemetrySink_RunsOnSkippedFrame 守 L2：被 L1 跳过的帧仍要进帧表。
func TestTelemetrySink_RunsOnSkippedFrame(t *testing.T) {
	db := newWriteGateTestDB(t)
	node, device := seedWriteGateDevice(t, db, "node-sink-dual")

	consumer, _ := newWriteGateConsumer(t, db, map[string]float64{"voltage": 12})
	consumer.SetTelemetryHeartbeat(time.Hour)

	var (
		calls    int
		gotSeq   []int64
		gotNames [][]string
	)
	consumer.SetTelemetrySink(func(deviceID uint, at time.Time, commandTemplateID int64, readings []telemetry.Reading) {
		calls++
		if deviceID != device.ID {
			t.Errorf("telemetry sink deviceID = %d, want %d", deviceID, device.ID)
		}
		if at.IsZero() {
			t.Error("telemetry sink 收到零值 at —— 必须传 unified_data.timestamp 用的同一个 now")
		}
		gotSeq = append(gotSeq, commandTemplateID)
		names := make([]string, len(readings))
		for i := range readings {
			names[i] = readings[i].Name
		}
		gotNames = append(gotNames, names)
	})

	consumer.Handle(frame(node.NodeID, device.ID))
	consumer.Handle(frame(node.NodeID, device.ID))

	if rows := countUnifiedRows(t, db, device.ID); rows != 1 {
		t.Fatalf("unified_data 行数 = %d, want 1", rows)
	}
	if calls != 2 {
		t.Errorf("telemetry sink 调用次数 = %d, want 2 —— 被 L1 跳过的帧不进帧表会让增量链"+
			"出现空洞，60s 关键帧窗口内的变化值永久丢失", calls)
	}
	for i := range gotSeq {
		if gotSeq[i] != 0 {
			t.Errorf("第 %d 次 commandTemplateID = %d, want 0（本用例的 DataEvent 没带模板）", i, gotSeq[i])
		}
		if len(gotNames[i]) != 1 || gotNames[i][0] != "voltage" {
			t.Errorf("第 %d 次 readings = %v, want [voltage]", i, gotNames[i])
		}
	}
}

// TestTelemetrySinks_NilSinksAreSafe 守「旁路未接线时不得 panic、主路径照常收窄」。
//
// 2026-11-07 起 L1 收窄**无条件生效**（契约 §1），所以本用例的落库断言从
// 「3 帧 3 行」改成「3 帧 1 行」—— 这不是回归，正是「删开关」应有的结果。
// 仍然要守的是两件事:
//  1. sink 为 nil 时 Handle 不 panic（两个旁路都在解析成功路径上被调用）；
//  2. latestSink 不受收窄影响，三帧都要更新（否则实时值会冻结）。
func TestTelemetrySinks_NilSinksAreSafe(t *testing.T) {
	db := newWriteGateTestDB(t)
	node, device := seedWriteGateDevice(t, db, "node-sink-nil")

	consumer, _ := newWriteGateConsumer(t, db, map[string]float64{"voltage": 12})
	consumer.SetTelemetryHeartbeat(time.Hour)
	// 刻意不调用 SetTelemetrySink / SetTelemetryRollupSink：两个旁路保持 nil。

	var latest int
	consumer.SetLatestSink(func(models.UnifiedData) { latest++ })

	for i := 0; i < 3; i++ {
		consumer.Handle(frame(node.NodeID, device.ID))
	}
	if rows := countUnifiedRows(t, db, device.ID); rows != 1 {
		t.Errorf("unified_data 行数 = %d, want 1（三帧同值 + 心跳 1h ⇒ 只有首帧落库；"+
			"L1 收窄已无条件生效，写 3 行说明开关没删干净）", rows)
	}
	if latest != 3 {
		t.Errorf("latestSink 调用次数 = %d, want 3（收窄只影响入库，实时值必须每帧更新）", latest)
	}
}

// TestTelemetrySinks_DoNotRescueFailedWrite 守「旁路不改变主写入语义」：
// 写库失败时旁路照常被调用（它记录的是设备真实采样），但不得因为旁路存在
// 而推进 L1 的收窄状态（否则该值会永久不落库）。
func TestTelemetrySinks_DoNotRescueFailedWrite(t *testing.T) {
	db := newWriteGateTestDB(t)
	node, device := seedWriteGateDevice(t, db, "node-sink-fail")

	consumer, driver := newWriteGateConsumer(t, db, map[string]float64{"voltage": 12})
	consumer.SetTelemetryHeartbeat(time.Hour)

	var rollupCalls int
	consumer.SetTelemetryRollupSink(func(uint, []parser.Field, time.Time) { rollupCalls++ })

	consumer.Handle(frame(node.NodeID, device.ID)) // 首帧写库成功（voltage=12 已入库）

	if err := db.Migrator().DropTable(&models.UnifiedData{}); err != nil {
		t.Fatalf("drop unified_data: %v", err)
	}
	// 值变了（12 → 42）⇒ L1 会尝试写库，但表已删 ⇒ Create 失败。
	// 旁路记录的是设备真实采样，与主写入成败无关，所以仍必须被调用。
	driver.values = map[string]float64{"voltage": 42}
	consumer.Handle(frame(node.NodeID, device.ID))

	if rollupCalls != 2 {
		t.Errorf("写库失败时 rollup sink 调用次数 = %d, want 2 —— "+
			"旁路记录的是设备真实采样，与主写入成败无关", rollupCalls)
	}
	// 关键断言：写库失败 ⇒ 收窄状态**不得**推进 ⇒ 同值 42 下一帧仍判 write_changed
	// （若被判成 skip，说明状态被错误推进，该值将永久不落库）。
	now := time.Now()
	_, stat := consumer.writeGate.filter(device.ID,
		[]models.UnifiedData{{DeviceID: device.ID, SensorName: "voltage", Value: 42, Timestamp: now}},
		now.Add(time.Second))
	if stat.Changed != 1 {
		t.Errorf("写库失败后收窄状态被推进了（%+v，want Changed=1）—— "+
			"新值被记成已落库，该值将永久不落库（方案 §3.3 缓存一致性）", stat)
	}
}
