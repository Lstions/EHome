package telemetry

import (
	"math"
	"math/rand"
	"testing"
)

// ── 量化规格（§4.3）────────────────────────────────────────────────────

// TestSpecFor_RealFieldNames 钉住真实字段名都能拿到**精确**规格。
//
// 分母守卫：字段清单来自生产实测（device14 的 29 个 + device15 的 7 种帧类型），
// 少一个就说明 spec.go 的登记被改坏了 —— 那会让该字段静默走 FallbackSpec
// （步长 1e-6、31 位），payload 反而变大但精度"看起来"更高，属于最难发现的退化。
func TestSpecFor_RealFieldNames(t *testing.T) {
	real := []string{
		// device14（BMS）29 字段
		"cell_voltage_1", "cell_voltage_2", "cell_voltage_3", "cell_voltage_4",
		"cell_voltage_5", "cell_voltage_6", "cell_voltage_7", "cell_voltage_8",
		"cell_voltage_9", "cell_voltage_10", "cell_voltage_11", "cell_voltage_12",
		"cell_voltage_13", "cell_voltage_14", "cell_voltage_15", "cell_voltage_16",
		"cell_voltage_max", "cell_voltage_min",
		"current", "cycle_count", "fet_status", "nominal_capacity", "protection_status",
		"remaining_capacity", "rsoc", "temperature_1", "temperature_2", "temperature_3",
		"total_voltage",
		// device15（逆变器）7 种帧类型的并集
		"pv1_voltage", "pv1_current", "pv1_power",
		"work_mode", "fault_code",
		"alarm_pv_to_load", "alarm_pv_low", "alarm_overtemp", "alarm_overload",
		"alarm_output", "alarm_input_overvoltage", "alarm_fan_error",
		"alarm_eeprom_rw", "alarm_eeprom_data", "alarm_battery_overvoltage",
		"alarm_battery_missing", "alarm_battery_low",
		"grid_voltage", "grid_frequency",
		"output_voltage", "output_frequency", "output_active_power",
		"output_apparent_power", "output_load_percent",
		"battery_voltage", "battery_charge_current", "battery_discharge_current",
		"battery_capacity", "bus_voltage",
		"boost_temp", "inverter_temp", "max_temp", "pv_temp", "transformer_temp",
		"fan1_status", "fan2_status", "fan1_speed", "fan2_speed",
		"daily_energy", "monthly_energy", "total_energy", "yearly_energy",
	}
	if len(real) < 60 {
		t.Fatalf("字段清单只有 %d 个（期望 >=60），门禁形同虚设", len(real))
	}
	for _, name := range real {
		sp, known := SpecFor(name)
		if !known {
			t.Errorf("字段 %q 未在 spec.go 登记（会走 FallbackSpec，精度不可控）", name)
			continue
		}
		if !sp.Valid() {
			t.Errorf("字段 %q 规格非法: %+v", name, sp)
		}
	}
}

// TestSpecQuantizeMaxError 钉住「量化误差 <= 步长/2」这一条验收红线（§9.1）。
func TestSpecQuantizeMaxError(t *testing.T) {
	names := []string{"cell_voltage_1", "current", "total_voltage", "temperature_1",
		"rsoc", "output_active_power", "grid_voltage", "daily_energy"}
	for _, name := range names {
		sp, _ := SpecFor(name)
		rng := rand.New(rand.NewSource(42))
		for i := 0; i < 5000; i++ {
			// 在量程内取随机物理量
			v := sp.Min + rng.Float64()*(sp.Max()-sp.Min)
			code, clamped := sp.Quantize(v)
			if clamped {
				t.Fatalf("%s: value %v within range was clamped", name, v)
			}
			got := sp.Dequantize(code)
			if err := math.Abs(got - v); err > sp.MaxError()+1e-12 {
				t.Fatalf("%s: |%v - %v| = %v > maxError %v", name, got, v, err, sp.MaxError())
			}
		}
	}
}

// TestSpecQuantizeClamps 钉住越界必须**报告** clamped（而不是静默截断）。
func TestSpecQuantizeClamps(t *testing.T) {
	sp, _ := SpecFor("cell_voltage_1") // 2.0 起步长 0.001 共 12 位 ⇒ 上限 6.095 V
	if _, clamped := sp.Quantize(2.0 - 0.5); !clamped {
		t.Error("below-range value must report clamped")
	}
	if _, clamped := sp.Quantize(sp.Max() + 1); !clamped {
		t.Error("above-range value must report clamped")
	}
	if code, clamped := sp.Quantize(3.254); clamped || code != 1254 {
		t.Errorf("3.254 V ⇒ code=%d clamped=%v, want 1254/false", code, clamped)
	}
	if got := sp.Dequantize(1254); math.Abs(got-3.254) > 1e-9 {
		t.Errorf("dequantize(1254) = %v, want 3.254", got)
	}
}

// TestSpecForVersionRejectsUnknown 钉住「未知规格版本必须显式失败」。
func TestSpecForVersionRejectsUnknown(t *testing.T) {
	if _, _, err := SpecForVersion(SpecVersion, "rsoc"); err != nil {
		t.Fatalf("current version must be accepted: %v", err)
	}
	if _, _, err := SpecForVersion(SpecVersion+1, "rsoc"); err == nil {
		t.Fatal("unknown spec version must be rejected (静默按当前版本解 = 静默错值)")
	}
}

// ── payload 编解码（§4.2）─────────────────────────────────────────────

func testSlotSpecs(t *testing.T, names []string) []FieldSpec {
	t.Helper()
	out := make([]FieldSpec, len(names))
	for i, n := range names {
		sp, _ := SpecFor(n)
		out[i] = sp
	}
	return out
}

// TestEncodeDecodeRoundTrip 是编解码的**主对拍**：随机掩码 + 随机码点往返。
func TestEncodeDecodeRoundTrip(t *testing.T) {
	names := []string{"cell_voltage_1", "current", "rsoc", "temperature_1", "alarm_pv_low", "daily_energy"}
	specs := testSlotSpecs(t, names)
	rng := rand.New(rand.NewSource(7))
	for iter := 0; iter < 2000; iter++ {
		n := len(names)
		f := &FrameData{
			KeyFrame: iter%17 == 0,
			Changed:  make([]bool, n),
			Codes:    make([]uint64, n),
		}
		for i := 0; i < n; i++ {
			f.Changed[i] = rng.Intn(2) == 0
			f.Codes[i] = uint64(rng.Int63n(int64(specs[i].MaxCode()) + 1))
		}
		payload, err := EncodeFrame(f, specs)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		got, err := DecodeFrame(payload, specs)
		if err != nil {
			t.Fatalf("decode: %v (payload %x)", err, payload)
		}
		if got.KeyFrame != f.KeyFrame {
			t.Fatalf("keyframe mismatch: got %v want %v", got.KeyFrame, f.KeyFrame)
		}
		for i := 0; i < n; i++ {
			if got.Changed[i] != f.Changed[i] {
				t.Fatalf("iter %d field %d changed mismatch", iter, i)
			}
			if f.Changed[i] && got.Codes[i] != f.Codes[i] {
				t.Fatalf("iter %d field %d code mismatch: got %d want %d", iter, i, got.Codes[i], f.Codes[i])
			}
		}
		if len(got.Invalid) != 0 {
			t.Fatalf("unexpected invalid mask on frame without NaN")
		}
	}
}

// TestPayloadSizeMatchesEncode 钉住「估算与实际编码字节数一致」。
//
// 为什么要这条：验收里"平均 payload"这个数字来自 PayloadSize（写入前估算）
// 或 len(payload)（实际）；两者一旦漂移，所有体积结论都不可信。
func TestPayloadSizeMatchesEncode(t *testing.T) {
	names := []string{"cell_voltage_1", "current", "rsoc", "temperature_1", "alarm_pv_low", "daily_energy"}
	specs := testSlotSpecs(t, names)
	rng := rand.New(rand.NewSource(11))
	for iter := 0; iter < 500; iter++ {
		n := len(names)
		f := &FrameData{Changed: make([]bool, n), Codes: make([]uint64, n)}
		for i := 0; i < n; i++ {
			f.Changed[i] = rng.Intn(2) == 0
			f.Codes[i] = rng.Uint64() % (specs[i].MaxCode() + 1)
		}
		payload, err := EncodeFrame(f, specs)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if got, want := len(payload), PayloadSize(f, specs); got != want {
			t.Fatalf("iter %d: encoded %d bytes, PayloadSize said %d", iter, got, want)
		}
	}
}

// TestPayloadLayoutFixed 钉住 §4.2 的**字节布局**（flags/mask 在前，位流在后）。
//
// 用一组人工构造的码点直接断言十六进制，任何人改动布局都会在这里红。
func TestPayloadLayoutFixed(t *testing.T) {
	// 4 个字段：bits = 12, 16, 7, 1（cell_voltage_1, current, rsoc, alarm_pv_low）
	names := []string{"cell_voltage_1", "current", "rsoc", "alarm_pv_low"}
	specs := testSlotSpecs(t, names)
	if specs[0].Bits != 12 || specs[1].Bits != 16 || specs[2].Bits != 7 || specs[3].Bits != 1 {
		t.Fatalf("unexpected bit widths: %d/%d/%d/%d", specs[0].Bits, specs[1].Bits, specs[2].Bits, specs[3].Bits)
	}
	f := &FrameData{
		Changed: []bool{true, false, true, true}, // 字段 0/2/3 变化 ⇒ mask = 0b1101 = 0x0D
		Codes:   []uint64{0x0ABC, 0, 0x55, 1},
	}
	payload, err := EncodeFrame(f, specs)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// flags=0x00, mask=0x0D, 位流 = 0xABC(12b) | 0x55(7b) | 1(1b) = 20 位 ⇒ 3 字节
	// LSB-first: 0xABC 占低 12 位, 0x55 接第 12..18 位, 1 接第 19 位
	//   bits: 1100 1011 1010 (0xABC) 很低 12 位
	//   x = 0xABC | (0x55 << 12) | (1 << 19) = 0xABC + 0x55000 + 0x80000 = 0xD5ABC
	//   字节序(小端) = BC 5A 0D
	want := "000dbc5a0d"
	if got := hexOf(payload); got != want {
		t.Fatalf("payload = %s, want %s (layout: 1B flags + 1B mask + LSB-first bit stream)", got, want)
	}
}

// TestDecodeRejectsCorrupt 钉住坏 payload 必须**报错**而不是解出垃圾。
func TestDecodeRejectsCorrupt(t *testing.T) {
	names := []string{"cell_voltage_1", "current", "rsoc"}
	specs := testSlotSpecs(t, names)
	cases := []struct {
		name    string
		payload []byte
		wantErr error
	}{
		{"empty", nil, ErrEmptyPayload},
		{"truncated-mask", []byte{0x00}, ErrPayloadTruncated},
		{"truncated-bits", []byte{0x00, 0x07}, ErrPayloadTruncated},
		{"reserved-flags", []byte{0x04, 0x00}, ErrReservedFlags},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := DecodeFrame(c.payload, specs); err == nil {
				t.Fatalf("expected error, got nil")
			} else if !isErr(err, c.wantErr) {
				t.Fatalf("error = %v, want %v", err, c.wantErr)
			}
		})
	}
	// 尾部多余字节（slot 版本不一致的典型症状）
	payload := []byte{0x00, 0x00, 0xFF}
	if _, err := DecodeFrame(payload, specs); err == nil {
		t.Fatal("trailing bytes must be rejected (slot 不一致会静默写串值)")
	}
}

// TestEncodeRejectsInconsistentMasks 钉住「同一字段既 changed 又 invalid」被拒。
func TestEncodeRejectsInconsistentMasks(t *testing.T) {
	names := []string{"rsoc", "current"}
	specs := testSlotSpecs(t, names)
	f := &FrameData{Changed: []bool{true, false}, Invalid: []bool{true, false}, Codes: []uint64{1, 2}}
	if _, err := EncodeFrame(f, specs); err == nil || !isErr(err, ErrInvalidMaskChanged) {
		t.Fatalf("expected ErrInvalidMaskChanged, got %v", err)
	}
}

// TestInvalidMaskRoundTrip 钉住 NaN 缺失走的扩展掩码（§4.2 的 bit1 扩展）。
func TestInvalidMaskRoundTrip(t *testing.T) {
	names := []string{"cell_voltage_1", "current", "rsoc"}
	specs := testSlotSpecs(t, names)
	f := &FrameData{
		Changed: []bool{true, false, true},
		Invalid: []bool{false, true, false},
		Codes:   []uint64{1254, 0, 55},
	}
	payload, err := EncodeFrame(f, specs)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeFrame(payload, specs)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.HasInvalid() || !got.Invalid[1] {
		t.Fatalf("invalid mask lost: %+v", got.Invalid)
	}
	if got.Changed[1] {
		t.Fatal("invalid field must not be marked changed")
	}
	if got.Codes[0] != 1254 || got.Codes[2] != 55 {
		t.Fatalf("codes lost: %+v", got.Codes)
	}
	// 无缺失时**不得**多出 invalid 掩码字节（零额外开销）
	noInvalid := &FrameData{Changed: []bool{true, true, true}, Codes: []uint64{1, 2, 3}}
	p2, err := EncodeFrame(noInvalid, specs)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	wantBytes := 1 + 1 + (int(specs[0].Bits+specs[1].Bits+specs[2].Bits)+7)/8
	if len(p2) != wantBytes {
		t.Fatalf("no-invalid frame has %d bytes, want %d (§4.2 布局零额外开销)", len(p2), wantBytes)
	}
}

// ── helpers ─────────────────────────────────────────────────────────

func hexOf(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, hexdigits[c>>4], hexdigits[c&0x0F])
	}
	return string(out)
}

func isErr(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
