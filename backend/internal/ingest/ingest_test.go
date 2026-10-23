package ingest_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/ingest"
	"ehome/backend/internal/models"
)

// 本文件是 L1 接入层的契约测试。
//
// 纪律：**全部用例零 DB**（ingest 包不 import gorm）。这既是公理 A2 的体现，
// 也让分支组合可以穷举 —— Supports 是纯判断，没有理由为它起数据库。

func edgeDevice(id uint, typ string) models.EdgeDevice {
	return models.EdgeDevice{ID: id, Type: typ, Name: "dev-" + typ}
}

func at() time.Time { return time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC) }

// ---------- Adapter 选择 ----------

// TestResolver_OrderIsSemantic 钉住「注册顺序即优先级」。
//
// 为什么这条最重要：若 ConfigParser 排到 Driver 之后，标定设备会被通用解析
// 绕过 —— 这是既有代码里写死的硬约束（consumers_heavy.go:270 附近）。
// 顺序被人无意调换时必须立刻红。
func TestResolver_OrderIsSemantic(t *testing.T) {
	first := &fakeAdapter{name: "first", supports: true, method: "First"}
	second := &fakeAdapter{name: "second", supports: true, method: "Second"}

	r := ingest.NewResolver(first, second)
	s, err := r.Decode(ingest.DecodeContext{At: at()}, ingest.SupportContext{})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if s.Method != "First" {
		t.Errorf("选中 %q，期望 First —— 注册顺序不再是优先级", s.Method)
	}
	if got := r.Adapters(); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Errorf("Adapters() = %v，顺序丢失", got)
	}
}

// TestResolver_SkipsNonSupporting 验证不支持者被跳过、支持者被选中。
func TestResolver_SkipsNonSupporting(t *testing.T) {
	no := &fakeAdapter{name: "no", supports: false}
	yes := &fakeAdapter{name: "yes", supports: true, method: "Yes"}
	r := ingest.NewResolver(no, yes)

	s, err := r.Decode(ingest.DecodeContext{At: at()}, ingest.SupportContext{})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if s.Method != "Yes" {
		t.Errorf("Method = %q, want Yes", s.Method)
	}
}

// TestResolver_NoAdapter 验证无 adapter 支持时报 no_adapter（不得静默返回空）。
func TestResolver_NoAdapter(t *testing.T) {
	r := ingest.NewResolver(&fakeAdapter{name: "no", supports: false})
	_, err := r.Decode(ingest.DecodeContext{At: at()}, ingest.SupportContext{})
	if err == nil {
		t.Fatal("无 adapter 支持时必须报错，不能静默成功")
	}
	reason, ok := ingest.ReasonOf(err)
	if ok && reason == ingest.ReasonNoAdapter {
		return // 顶层就是 no_adapter，符合预期
	}
	// MultiError 包裹时也要能取到 no_adapter
	var me *ingest.MultiError
	if errors.As(err, &me) && len(me.Errs) == 1 && me.Errs[0].Reason == ingest.ReasonNoAdapter {
		return
	}
	t.Errorf("期望 ReasonNoAdapter，实际: %v", err)
}

// TestResolver_MultiErrorKeepsAllReasons 验证「全失败时保留每个 adapter 的原因」。
//
// 为什么重要：只报最后一个会误导运维（例如 ConfigParser 因 JSON 非法失败、
// Driver 因未注册失败，只报后者会让人去补驱动，而真正问题是 JSON）。
func TestResolver_MultiErrorKeepsAllReasons(t *testing.T) {
	a := &fakeAdapter{name: "a", supports: true, err: ingest.NewDecodeError("a", ingest.ReasonDecodeFailed, errors.New("bad json"))}
	b := &fakeAdapter{name: "b", supports: true, err: ingest.NewDecodeError("b", ingest.ReasonCalibrationMiss, nil)}
	r := ingest.NewResolver(a, b)

	_, err := r.Decode(ingest.DecodeContext{At: at()}, ingest.SupportContext{})
	var me *ingest.MultiError
	if !errors.As(err, &me) {
		t.Fatalf("期望 *MultiError，实际: %T %v", err, err)
	}
	got := me.FailedAdapters()
	if got["a"] != ingest.ReasonDecodeFailed {
		t.Errorf("a 的 reason = %q, want decode_failed", got["a"])
	}
	if got["b"] != ingest.ReasonCalibrationMiss {
		t.Errorf("b 的 reason = %q, want calibration_missing", got["b"])
	}
}

// TestResolver_EmptyFieldsIsFailure 验证「解析成功但零字段」按失败处理。
//
// 零字段通常意味着协议版本不匹配 —— 比格式错误更难发现，必须显式归因，
// 不能让它静默变成一条空记录。
func TestResolver_EmptyFieldsIsFailure(t *testing.T) {
	a := &fakeAdapter{name: "empty", supports: true, method: "Empty"}
	r := ingest.NewResolver(a)

	_, err := r.Decode(ingest.DecodeContext{At: at()}, ingest.SupportContext{})
	if err == nil {
		t.Fatal("零字段必须按失败处理")
	}
	var me *ingest.MultiError
	if errors.As(err, &me) {
		if got := me.FailedAdapters()["empty"]; got != ingest.ReasonEmptyResult {
			t.Errorf("reason = %q, want empty_result", got)
		}
	}
}

// ---------- ConfigParser adapter ----------

// TestConfigAdapter_SupportsMatrix 穷举 Supports 的分支组合。
//
// 关键断言：**标定驱动必须被排除**（configuration parser 无法解标定帧）。
func TestConfigAdapter_SupportsMatrix(t *testing.T) {
	a := ingest.NewAdapterConfigParser()
	cases := []struct {
		name string
		ctx  ingest.SupportContext
		want bool
	}{
		{"标定驱动+有配置 ⇒ 排除", ingest.SupportContext{DriverKind: ingest.DriverKindCalibration, HasDeviceConfig: true}, false},
		{"普通驱动+有配置 ⇒ 支持", ingest.SupportContext{DriverKind: ingest.DriverKindPlain, HasDeviceConfig: true}, true},
		{"命令驱动+有配置 ⇒ 支持", ingest.SupportContext{DriverKind: ingest.DriverKindCommand, HasDeviceConfig: true}, true},
		{"普通驱动+无配置 ⇒ 不支持", ingest.SupportContext{DriverKind: ingest.DriverKindPlain, HasDeviceConfig: false}, false},
	}
	for _, c := range cases {
		if got := a.Supports(c.ctx); got != c.want {
			t.Errorf("%s: Supports = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestConfigAdapter_MethodFormat 钉住 Method 字符串格式。
//
// 它已进生产日志与审计字段，改格式会造成历史不可比 —— 因此用测试锁死。
func TestConfigAdapter_MethodFormat(t *testing.T) {
	a := ingest.NewAdapterConfigParser()
	// type 必须是 ConfigParser 支持的枚举（uint16/int16/uint32/int32/float32）：
	// 写 "u8" 会让 typeLength 返回 0 ⇒ 报 invalid offset/length（这是真实的
	// 配置错误形态，但会让本用例偏离"验证 Method 格式"的目的）。
	parserJSON := json.RawMessage(`{"data_format":"binary","fields":[{"name":"x","offset":0,"type":"uint16","length":2}]}`)

	s, err := a.Decode(ingest.DecodeContext{
		EdgeDevice:       edgeDevice(7, "modbus"),
		Raw:              []byte{0x00, 0x2A},
		At:               at(),
		ConfigParserJSON: parserJSON,
		ConfigName:       "my-parser",
	})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if want := "ConfigParser(my-parser)"; s.Method != want {
		t.Errorf("Method = %q, want %q（格式变更会破坏历史日志可比性）", s.Method, want)
	}
	if s.EdgeDeviceID != 7 {
		t.Errorf("EdgeDeviceID = %d, want 7", s.EdgeDeviceID)
	}
	if !s.At.Equal(at()) {
		t.Errorf("At = %v, want %v", s.At, at())
	}
	if len(s.Fields) != 1 || s.Fields[0].Name != "x" {
		t.Errorf("Fields = %+v", s.Fields)
	}
	if s.Quality != ingest.QualityOk {
		t.Errorf("Quality = %v, want ok", s.Quality)
	}
}

// TestConfigAdapter_BadJSON 验证非法 parser JSON 归因为 decode_failed。
func TestConfigAdapter_BadJSON(t *testing.T) {
	a := ingest.NewAdapterConfigParser()
	_, err := a.Decode(ingest.DecodeContext{
		EdgeDevice:       edgeDevice(1, "t"),
		Raw:              []byte{0x01},
		At:               at(),
		ConfigParserJSON: []byte("{not json"),
		ConfigName:       "bad",
	})
	if err == nil {
		t.Fatal("非法 JSON 必须报错")
	}
	if r, ok := ingest.ReasonOf(err); !ok || r != ingest.ReasonDecodeFailed {
		t.Errorf("reason = %v (ok=%v), want decode_failed", r, ok)
	}
}

// ---------- Driver adapter ----------

// TestDriverAdapter_MissingCalibration 验证缺标定归因为 calibration_missing。
//
// 这条与 decode_failed 必须区分：前者补标定，后者查协议。混在一起会让人查错方向。
func TestDriverAdapter_MissingCalibration(t *testing.T) {
	reg := drivers.NewRegistry()
	reg.Register(&fakeCalibDriver{})
	a := ingest.NewAdapterDriver(reg)

	_, err := a.Decode(ingest.DecodeContext{
		EdgeDevice:    edgeDevice(1, "calibdev"),
		Raw:           []byte{0x01},
		At:            at(),
		DriverContext: nil, // 故意不给标定
	})
	if err == nil {
		t.Fatal("缺标定必须报错（不得降级解析：未标定读数会污染历史）")
	}
	if r, _ := ingest.ReasonOf(err); r != ingest.ReasonCalibrationMiss {
		t.Errorf("reason = %q, want calibration_missing", r)
	}
}

// TestDriverAdapter_MissingCommandCtx 验证缺命令上下文归因为 command_ctx_missing。
func TestDriverAdapter_MissingCommandCtx(t *testing.T) {
	reg := drivers.NewRegistry()
	reg.Register(&fakeCmdDriver{})
	a := ingest.NewAdapterDriver(reg)

	_, err := a.Decode(ingest.DecodeContext{
		EdgeDevice:    edgeDevice(1, "cmddev"),
		Raw:           []byte{0x01},
		At:            at(),
		DriverContext: &ingest.DriverContext{}, // 空 write_data
	})
	if err == nil {
		t.Fatal("缺命令上下文必须报错")
	}
	if r, _ := ingest.ReasonOf(err); r != ingest.ReasonCommandCtxMiss {
		t.Errorf("reason = %q, want command_ctx_missing", r)
	}
}

// TestDriverAdapter_MethodFormat 钉住 Driver Method 格式。
func TestDriverAdapter_MethodFormat(t *testing.T) {
	reg := drivers.NewRegistry()
	reg.Register(&fakePlainDriver{})
	a := ingest.NewAdapterDriver(reg)

	s, err := a.Decode(ingest.DecodeContext{
		EdgeDevice: edgeDevice(3, "plaindev"),
		Raw:        []byte{0x2A},
		At:         at(),
	})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if want := "Driver(plaindev)"; s.Method != want {
		t.Errorf("Method = %q, want %q", s.Method, want)
	}
	if len(s.Fields) == 0 {
		t.Error("Fields 为空")
	}
}

// TestResolveDriverKind_MatchesDecodeBranch 验证「分支判定与 Decode 内部一致」。
//
// 这是防分裂的关键：若 ResolveDriverKind 与 Decode 的类型断言逻辑不同步，
// 会出现「Supports 认为普通、Decode 走标定」的静默分裂。
func TestResolveDriverKind_MatchesDecodeBranch(t *testing.T) {
	reg := drivers.NewRegistry()
	reg.Register(&fakePlainDriver{})
	reg.Register(&fakeCalibDriver{})
	reg.Register(&fakeCmdDriver{})

	if k, ok := ingest.ResolveDriverKind(reg, "plaindev"); !ok || k != ingest.DriverKindPlain {
		t.Errorf("plain: kind=%v ok=%v", k, ok)
	}
	if k, ok := ingest.ResolveDriverKind(reg, "calibdev"); !ok || k != ingest.DriverKindCalibration {
		t.Errorf("calib: kind=%v ok=%v", k, ok)
	}
	if k, ok := ingest.ResolveDriverKind(reg, "cmddev"); !ok || k != ingest.DriverKindCommand {
		t.Errorf("cmd: kind=%v ok=%v", k, ok)
	}
	if _, ok := ingest.ResolveDriverKind(reg, "missing"); ok {
		t.Error("未注册驱动应返回 ok=false")
	}
	if _, ok := ingest.ResolveDriverKind(nil, "x"); ok {
		t.Error("nil registry 应返回 ok=false（不得 panic）")
	}
}

// TestDriverAdapter_NilRegistryIsSafe 验证 nil registry 不会 panic 在帧路径上。
func TestDriverAdapter_NilRegistryIsSafe(t *testing.T) {
	a := ingest.NewAdapterDriver(nil)
	if a.Supports(ingest.SupportContext{DriverExists: true}) {
		t.Error("nil registry 时 Supports 必须 false")
	}
}

// ---------- 测试替身 ----------

type fakeAdapter struct {
	name     string
	supports bool
	method   string
	err      error
}

func (f *fakeAdapter) Name() string                        { return f.name }
func (f *fakeAdapter) Supports(ingest.SupportContext) bool { return f.supports }
func (f *fakeAdapter) Decode(ingest.DecodeContext) (ingest.Sample, error) {
	if f.err != nil {
		return ingest.Sample{}, f.err
	}
	fields := []ingest.Field{{Name: "v", Value: 1}}
	if f.method == "Empty" {
		fields = nil // 模拟「成功但零字段」
	}
	return ingest.Sample{Fields: fields, Method: f.method, Quality: ingest.QualityOk}, nil
}

type fakePlainDriver struct{}

func (*fakePlainDriver) ParseData([]byte) ([]drivers.SensorData, error) {
	return []drivers.SensorData{{Name: "temp", Value: 42, Unit: "C"}}, nil
}
func (*fakePlainDriver) DeviceType() string                         { return "plaindev" }
func (*fakePlainDriver) DeviceName() string                         { return "Plain" }
func (*fakePlainDriver) OEM() string                                { return "test" }
func (*fakePlainDriver) Category() string                           { return "test" }
func (*fakePlainDriver) HardwareTypes() []string                    { return nil }
func (*fakePlainDriver) GetSensorDefinitions() []drivers.SensorData { return nil }

type fakeCalibDriver struct{ fakePlainDriver }

func (*fakeCalibDriver) DeviceType() string { return "calibdev" }
func (*fakeCalibDriver) ParseDataWithCalibration([]byte, []byte) ([]drivers.SensorData, error) {
	return []drivers.SensorData{{Name: "calib", Value: 1}}, nil
}

type fakeCmdDriver struct{ fakePlainDriver }

func (*fakeCmdDriver) DeviceType() string { return "cmddev" }
func (*fakeCmdDriver) ParseDataWithCommand([]byte, string) ([]drivers.SensorData, error) {
	return []drivers.SensorData{{Name: "cmd", Value: 2}}, nil
}
