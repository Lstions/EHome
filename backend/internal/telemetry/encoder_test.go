package telemetry

import (
	"math"
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// 编码器状态机测试：关键帧/增量/变化抑制/心跳/NaN/版本切换。
//
// 这些是"压缩率从 300 倍变成 1 倍"的分界线 —— 任何一个失效都不会报错，
// 只会让 payload 悄悄变大或值悄悄错掉，所以必须逐条钉住。

func readingsOf(pairs map[string]float64, order []string) []Reading {
	out := make([]Reading, 0, len(order))
	for _, n := range order {
		out = append(out, Reading{Name: n, Value: pairs[n]})
	}
	return out
}

func baseReadings(cell1, current, rsoc float64) map[string]float64 {
	return map[string]float64{
		"cell_voltage_1": cell1,
		"current":        current,
		"rsoc":           rsoc,
	}
}

var order3 = []string{"cell_voltage_1", "current", "rsoc"}

// TestEncoderFirstFrameIsKeyFrame 钉住「首帧必然关键帧」（进程重启首帧全量写）。
func TestEncoderFirstFrameIsKeyFrame(t *testing.T) {
	dict := NewMemorySlots()
	enc := NewEncoder(EncoderConfig{}, dict)
	at := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)

	f, err := enc.EncodeFrame(1, at, 0, readingsOf(baseReadings(3.2, 10, 60), order3))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if f.FrameKind != models.TelemetryFrameKindKey {
		t.Fatalf("首帧 frame_kind = %d, want 关键帧", f.FrameKind)
	}
	// 关键帧里 3 个字段全带值 ⇒ payload = 1B flags + 1B mask + ceil((12+16+7)/8)=5B
	if want := 1 + 1 + 5; len(f.Payload) != want {
		t.Fatalf("关键帧 payload = %d B, want %d", len(f.Payload), want)
	}
}

// TestEncoderSuppressesUnchanged 钉住「值不变 ⇒ 该字段不占位宽」。
func TestEncoderSuppressesUnchanged(t *testing.T) {
	dict := NewMemorySlots()
	enc := NewEncoder(EncoderConfig{}, dict)
	at := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	vals := baseReadings(3.2, 10, 60)

	if _, err := enc.EncodeFrame(1, at, 0, readingsOf(vals, order3)); err != nil {
		t.Fatalf("encode keyframe: %v", err)
	}
	// 完全相同的一帧（1s 后）：零变化 ⇒ mask 全 0、位流 0 字节
	f, err := enc.EncodeFrame(1, at.Add(time.Second), 0, readingsOf(vals, order3))
	if err != nil {
		t.Fatalf("encode delta: %v", err)
	}
	if f.FrameKind != models.TelemetryFrameKindDelta {
		t.Fatalf("frame_kind = %d, want 增量", f.FrameKind)
	}
	if want := 1 + 1; len(f.Payload) != want {
		t.Fatalf("零变化帧 payload = %d B, want %d（只有 flags+mask）", len(f.Payload), want)
	}

	// 只改 rsoc（7 位）⇒ 多 1 字节
	vals["rsoc"] = 61
	f2, err := enc.EncodeFrame(1, at.Add(2*time.Second), 0, readingsOf(vals, order3))
	if err != nil {
		t.Fatalf("encode delta2: %v", err)
	}
	if want := 1 + 1 + 1; len(f2.Payload) != want {
		t.Fatalf("单字段变化 payload = %d B, want %d", len(f2.Payload), want)
	}
}

// TestEncoderHeartbeatKeyFrame 钉住 60 s 心跳关键帧（§8 增量链断裂的兜底）。
func TestEncoderHeartbeatKeyFrame(t *testing.T) {
	dict := NewMemorySlots()
	enc := NewEncoder(EncoderConfig{KeyFrameInterval: 60 * time.Second}, dict)
	at := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	vals := baseReadings(3.2, 10, 60)

	first, err := enc.EncodeFrame(1, at, 0, readingsOf(vals, order3))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if first.FrameKind != models.TelemetryFrameKindKey {
		t.Fatal("首帧必须关键帧")
	}
	// 30 s 后：仍是增量
	mid, err := enc.EncodeFrame(1, at.Add(30*time.Second), 0, readingsOf(vals, order3))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if mid.FrameKind != models.TelemetryFrameKindDelta {
		t.Errorf("30s 后应为增量帧，得到 %d", mid.FrameKind)
	}
	// 距**上一帧**（at+30s）超过 60 s：必须强制关键帧（值完全没变也要发）
	hb, err := enc.EncodeFrame(1, at.Add(30*time.Second+61*time.Second), 0, readingsOf(vals, order3))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if hb.FrameKind != models.TelemetryFrameKindKey {
		t.Errorf("超过 KeyFrameInterval 必须强制关键帧，得到 %d（否则丢一帧就永久错位）", hb.FrameKind)
	}
	if got := enc.Stats().ChainBreaks; got != 1 {
		t.Errorf("ChainBreaks = %d, want 1", got)
	}
}

// TestEncoderNaNAndInfAreInvalid 钉住 NaN/Inf 走 invalid 掩码而不是量化。
func TestEncoderNaNAndInfAreInvalid(t *testing.T) {
	dict := NewMemorySlots()
	enc := NewEncoder(EncoderConfig{}, dict)
	at := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	vals := baseReadings(3.2, math.NaN(), 60)
	vals["rsoc"] = math.Inf(1)

	f, err := enc.EncodeFrame(1, at, 0, readingsOf(vals, order3))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	slot, ok := dict.ByID(f.SlotID)
	if !ok {
		t.Fatalf("slot %d 不在字典里", f.SlotID)
	}
	fd, err := DecodeFrame(f.Payload, slot.Specs())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 字段顺序是字典序：cell_voltage_1, current, rsoc
	if fd.FieldInvalid(0) {
		t.Error("cell_voltage_1 是合法值，不该标缺失")
	}
	if !fd.FieldInvalid(1) || !fd.FieldInvalid(2) {
		t.Errorf("NaN/Inf 必须标缺失，得到 invalid=%v", fd.Invalid)
	}
	if fd.Changed[1] || fd.Changed[2] {
		t.Error("缺失字段不得同时标 changed")
	}
	if got := enc.Stats().InvalidValues; got != 2 {
		t.Errorf("InvalidValues = %d, want 2", got)
	}
}

// TestEncoderClampedCounted 钉住越界量化被计数（那是真实精度损失）。
func TestEncoderClampedCounted(t *testing.T) {
	dict := NewMemorySlots()
	enc := NewEncoder(EncoderConfig{}, dict)
	at := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	vals := baseReadings(9.9 /* cell_voltage 上限 6.095 V */, 10, 60)
	if _, err := enc.EncodeFrame(1, at, 0, readingsOf(vals, order3)); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if got := enc.Stats().ClampedValues; got != 1 {
		t.Fatalf("ClampedValues = %d, want 1（越界必须计数，验收要求真实数据上为 0）", got)
	}
}

// TestEncoderSlotVersionSwitchForcesKeyFrame 钉住「slot 变了必须重发关键帧」。
func TestEncoderSlotVersionSwitchForcesKeyFrame(t *testing.T) {
	dict := NewMemorySlots()
	enc := NewEncoder(EncoderConfig{}, dict)
	at := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)

	if _, err := enc.EncodeFrame(1, at, 100, readingsOf(baseReadings(3.2, 10, 60), order3)); err != nil {
		t.Fatalf("encode A: %v", err)
	}
	// 换成另一条指令（另一个 slot）：同字段集合但不同 command_template_id
	f, err := enc.EncodeFrame(1, at.Add(time.Second), 200, readingsOf(baseReadings(3.2, 10, 61), order3))
	if err != nil {
		t.Fatalf("encode B: %v", err)
	}
	if f.FrameKind != models.TelemetryFrameKindKey {
		t.Fatal("切换 slot 后必须发关键帧（旧 slot 的码点不能当新 slot 的增量基线）")
	}
}

// TestEncoderPerDeviceState 钉住「状态按设备隔离」。
func TestEncoderPerDeviceState(t *testing.T) {
	dict := NewMemorySlots()
	enc := NewEncoder(EncoderConfig{}, dict)
	at := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)

	if _, err := enc.EncodeFrame(1, at, 0, readingsOf(baseReadings(3.2, 10, 60), order3)); err != nil {
		t.Fatalf("encode dev1: %v", err)
	}
	f, err := enc.EncodeFrame(2, at, 0, readingsOf(baseReadings(3.2, 10, 60), order3))
	if err != nil {
		t.Fatalf("encode dev2: %v", err)
	}
	if f.FrameKind != models.TelemetryFrameKindKey {
		t.Fatal("另一台设备的首帧必须是关键帧（状态不能跨设备共用）")
	}
}

// TestEncoderSortsFieldsStably 钉住「reading 顺序无关」。
//
// 为什么重要：驱动的字段发射顺序会随固件演进变，如果 slot 的位流顺序跟着变，
// 同一设备升级固件后历史帧就解错了。
func TestEncoderSortsFieldsStably(t *testing.T) {
	dict := NewMemorySlots()
	enc := NewEncoder(EncoderConfig{}, dict)
	at := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)

	vals := baseReadings(3.2, 10, 60)
	rev := []string{"rsoc", "current", "cell_voltage_1"}
	f1, err := enc.EncodeFrame(1, at, 0, readingsOf(vals, order3))
	if err != nil {
		t.Fatalf("encode forward: %v", err)
	}
	f2, err := enc.EncodeFrame(1, at.Add(time.Second), 0, readingsOf(vals, rev))
	if err != nil {
		t.Fatalf("encode reverse: %v", err)
	}
	if f1.SlotID != f2.SlotID {
		t.Fatalf("字段顺序不同导致 slot 不同（%d vs %d）—— slot 必须按字典序", f1.SlotID, f2.SlotID)
	}
	// 值也没变 ⇒ 第二帧零变化
	if want := 2; len(f2.Payload) != want {
		t.Fatalf("第二帧 payload = %d B, want %d（字段重排不应被当成变化）", len(f2.Payload), want)
	}
}

// TestEncoderResetForcesKeyFrames 钉住 Reset（进程重启语义）。
func TestEncoderResetForcesKeyFrames(t *testing.T) {
	dict := NewMemorySlots()
	enc := NewEncoder(EncoderConfig{}, dict)
	at := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	vals := baseReadings(3.2, 10, 60)

	if _, err := enc.EncodeFrame(1, at, 0, readingsOf(vals, order3)); err != nil {
		t.Fatalf("encode: %v", err)
	}
	enc.Reset()
	f, err := enc.EncodeFrame(1, at.Add(time.Second), 0, readingsOf(vals, order3))
	if err != nil {
		t.Fatalf("encode after reset: %v", err)
	}
	if f.FrameKind != models.TelemetryFrameKindKey {
		t.Fatal("Reset 后首帧必须是关键帧")
	}
}

// ── slot 字典 ──────────────────────────────────────────────────────

// TestSlotSignatureDistinguishesCommandTemplate 钉住 §4.4：
// 同一字段集合但不同指令模板 ⇒ 不同 slot。
func TestSlotSignatureDistinguishesCommandTemplate(t *testing.T) {
	names := []string{"pv1_voltage", "pv1_current", "pv1_power"}
	a := SignatureOf(11, SpecVersion, names)
	b := SignatureOf(12, SpecVersion, names)
	if a == b {
		t.Fatal("不同 command_template_id 必须得到不同签名（否则 device15 7 种帧写串）")
	}
	// 字段集合不同 ⇒ 不同签名
	c := SignatureOf(11, SpecVersion, []string{"pv1_voltage"})
	if a == c {
		t.Fatal("不同字段集合必须得到不同签名")
	}
	// 顺序无关
	d := SignatureOf(11, SpecVersion, []string{"pv1_power", "pv1_voltage", "pv1_current"})
	if a != d {
		t.Fatal("签名必须与字段顺序无关")
	}
}

// TestDBSlotsPersistAndReload 钉住 slot 落库 + 重启后仍是同一 ID。
func TestDBSlotsPersistAndReload(t *testing.T) {
	db := openTelemetryTestDB(t)
	names := []string{"rsoc", "current", "total_voltage"}

	d1 := NewDBSlots(db)
	s1, err := d1.Resolve(0, names)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if s1.ID == 0 {
		t.Fatal("落库后 slot_id 不应为 0")
	}
	// 同一个字典再取：命中缓存，ID 不变
	s1b, err := d1.Resolve(0, names)
	if err != nil {
		t.Fatalf("resolve again: %v", err)
	}
	if s1b.ID != s1.ID {
		t.Fatalf("同一签名得到两个 slot_id（%d vs %d）", s1.ID, s1b.ID)
	}

	// 模拟进程重启：新字典从表里装载
	d2 := NewDBSlots(db)
	s2, err := d2.Resolve(0, names)
	if err != nil {
		t.Fatalf("resolve after restart: %v", err)
	}
	if s2.ID != s1.ID {
		t.Fatalf("重启后 slot_id 变了（%d → %d）—— 历史帧会解不出来", s1.ID, s2.ID)
	}
	// 字段快照必须逐字段一致（规格真值来自 slot，不是当前代码）
	if len(s2.Fields) != len(s1.Fields) {
		t.Fatalf("字段数不一致")
	}
	for i := range s1.Fields {
		if s1.Fields[i] != s2.Fields[i] {
			t.Fatalf("字段 %d 规格漂移：%+v vs %+v", i, s1.Fields[i], s2.Fields[i])
		}
	}
}

// ── 解码链断裂（§8）─────────────────────────────────────────────────

// TestDecodeBrokenChainSkipsToKeyFrame 钉住「坏帧后的增量全部跳过，到关键帧恢复」。
func TestDecodeBrokenChainSkipsToKeyFrame(t *testing.T) {
	dict := NewMemorySlots()
	enc := NewEncoder(EncoderConfig{KeyFrameInterval: time.Hour}, dict)
	at := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	vals := baseReadings(3.2, 10, 60)

	frames := make([]models.TelemetryFrame, 0, 6)
	for i := 0; i < 3; i++ {
		vals["rsoc"] = float64(60 + i)
		f, err := enc.EncodeFrame(1, at.Add(time.Duration(i)*time.Second), 0, readingsOf(vals, order3))
		if err != nil {
			t.Fatalf("encode %d: %v", i, err)
		}
		frames = append(frames, models.TelemetryFrame{
			DeviceID: f.DeviceID, TS: f.TS, Seq: f.Seq,
			FrameKind: f.FrameKind, SlotID: f.SlotID, Payload: f.Payload,
		})
	}
	// 第 3 帧人为损坏（截断）
	vals["rsoc"] = 70
	f, err := enc.EncodeFrame(1, at.Add(3*time.Second), 0, readingsOf(vals, order3))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	frames = append(frames, models.TelemetryFrame{
		DeviceID: f.DeviceID, TS: f.TS, Seq: f.Seq,
		FrameKind: f.FrameKind, SlotID: f.SlotID, Payload: []byte{0x00, 0x01}, // 截断
	})
	// 第 4/5 帧是合法增量（但链已断，必须跳过）
	vals["rsoc"] = 72
	for i := 4; i < 6; i++ {
		f, err := enc.EncodeFrame(1, at.Add(time.Duration(i)*time.Second), 0, readingsOf(vals, order3))
		if err != nil {
			t.Fatalf("encode %d: %v", i, err)
		}
		frames = append(frames, models.TelemetryFrame{
			DeviceID: f.DeviceID, TS: f.TS, Seq: f.Seq,
			FrameKind: f.FrameKind, SlotID: f.SlotID, Payload: f.Payload,
		})
	}
	// 第 6 帧强制关键帧（模拟 60s 后心跳/或 Reset）
	enc.Reset()
	vals["rsoc"] = 80
	f, err = enc.EncodeFrame(1, at.Add(6*time.Second), 0, readingsOf(vals, order3))
	if err != nil {
		t.Fatalf("encode keyframe: %v", err)
	}
	frames = append(frames, models.TelemetryFrame{
		DeviceID: f.DeviceID, TS: f.TS, Seq: f.Seq,
		FrameKind: f.FrameKind, SlotID: f.SlotID, Payload: f.Payload,
	})

	points, stats, err := DecodeFrames(frames, dict, DecodeOptions{Fields: []string{"rsoc"}})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if stats.FramesSkippedCorrupt != 1 {
		t.Errorf("坏帧计数 = %d, want 1", stats.FramesSkippedCorrupt)
	}
	if stats.ChainInvalidations != 1 {
		t.Errorf("链失效次数 = %d, want 1", stats.ChainInvalidations)
	}
	if stats.FramesSkippedBrokenChain != 2 {
		t.Errorf("断链跳过帧数 = %d, want 2（坏帧后的两个合法增量必须跳）", stats.FramesSkippedBrokenChain)
	}
	// 恢复后必须能拿到最后一帧的值（不错位、不丢）
	if len(points) == 0 {
		t.Fatal("断链恢复后必须产出读数")
	}
	last := points[len(points)-1]
	if math.Abs(last.Value-80) > 0.5 {
		t.Fatalf("恢复后 rsoc = %v, want 80（跳到关键帧后必须完全正确）", last.Value)
	}
	// 跳过的那两帧**不得**产出任何 rsoc=72 的读数
	for _, p := range points {
		if math.Abs(p.Value-72) < 0.5 {
			t.Fatal("断链期间的增量帧不得产出读数（否则就是错位数据）")
		}
	}
}
