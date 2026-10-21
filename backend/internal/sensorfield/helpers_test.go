package sensorfield

import (
	"context"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

// enabledConfig 返回“开启 + 全默认 limits”的配置。
func enabledConfig() Config { return Config{Enabled: true} }

// newTestRegistry 构造开启态的 Registry。
func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	return New(enabledConfig())
}

// deviceFixture 是一台设备的声明/观测夹具。
type deviceFixture struct {
	deviceID   uint
	deviceType string
	decls      []Field
	obs        []Field
}

// setupDevice 按夹具注册设备类型声明、绑定设备类型、写入观测。
func setupDevice(t *testing.T, r *Registry, f deviceFixture) {
	t.Helper()
	if f.deviceType != "" {
		r.RegisterDeclarations(f.deviceType, f.decls)
		r.SetDeviceType(f.deviceID, f.deviceType)
	}
	if len(f.obs) > 0 {
		r.SetObservations(f.deviceID, f.obs)
	}
}

// numField 构造已知数值类型字段。
func numField(name string) Field { return Field{Name: name, ValueType: TypeNumber} }

// typedField 构造指定类型字段。
func typedField(name string, typ ValueType, unit string) Field {
	return Field{Name: name, ValueType: typ, Unit: unit}
}

// mustRegisterDecl 注册声明（失败即 t.Fatal —— 当前声明接口无 error 返回）。
func mustRegisterDecl(t *testing.T, r *Registry, deviceType string, fields ...Field) {
	t.Helper()
	r.RegisterDeclarations(deviceType, fields)
}

// mustSetAliases 设置别名，失败即 Fatal。
func mustSetAliases(t *testing.T, r *Registry, rules ...AliasRule) {
	t.Helper()
	if err := r.SetAliases(rules); err != nil {
		t.Fatalf("SetAliases failed: %v", err)
	}
}

// mustSetVirtuals 设置派生定义，失败即 Fatal。
func mustSetVirtuals(t *testing.T, r *Registry, defs ...VirtualDef) {
	t.Helper()
	if err := r.SetVirtuals(defs); err != nil {
		t.Fatalf("SetVirtuals failed: %v", err)
	}
}

// mustCompile 编译 selector，失败即 Fatal。
func mustCompile(t *testing.T, r *Registry, deviceID uint, selector string) Plan {
	t.Helper()
	p, err := r.Compile(deviceID, selector)
	if err != nil {
		t.Fatalf("Compile(%q) failed: %v", selector, err)
	}
	if p == nil {
		t.Fatalf("Compile(%q) returned nil plan", selector)
	}
	return p
}

// readings 按名字/值构造一帧读数。
func readings(pairs ...any) []Reading {
	if len(pairs)%2 != 0 {
		panic("readings: odd number of arguments")
	}
	out := make([]Reading, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, Reading{Name: pairs[i].(string), Value: pairs[i+1].(float64)})
	}
	return out
}

// nearlyEqual 浮点近似比较。
func nearlyEqual(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return false
	}
	return math.Abs(a-b) <= 1e-9*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}

// captureSink 是测试用 FlushSink。
type captureSink struct {
	mu   sync.Mutex
	bat  [][]Observation
	fail error
}

func (s *captureSink) UpsertObservations(obs []Observation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	cp := make([]Observation, len(obs))
	copy(cp, obs)
	s.bat = append(s.bat, cp)
	return nil
}

func (s *captureSink) batches() [][]Observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]Observation, len(s.bat))
	copy(out, s.bat)
	return out
}

func (s *captureSink) totalObs() int {
	n := 0
	for _, b := range s.batches() {
		n += len(b)
	}
	return n
}

// findObs 在最近一批里按 (device, name) 找观测。
func findObs(batch []Observation, deviceID uint, name string) (Observation, bool) {
	for _, o := range batch {
		if o.DeviceID == deviceID && o.Name == name {
			return o, true
		}
	}
	return Observation{}, false
}

// flushNoSink 用 nil sink Flush（只合并内存）。
func flushNoSink(t *testing.T, r *Registry) {
	t.Helper()
	if err := r.Flush(context.Background(), nil); err != nil {
		t.Fatalf("Flush(nil) failed: %v", err)
	}
}

// fixedTime 是测试用固定事件时间。
func fixedTime(seconds int64) time.Time { return time.Unix(1_700_000_000+seconds, 0) }

// mustVerdict 断言 OK 并在失败时输出上下文。
func requireVerdict(t *testing.T, v Verdict, wantOK bool, contextStr string) {
	t.Helper()
	if v.OK != wantOK {
		t.Fatalf("%s: OK=%v want %v (reason=%q)", contextStr, v.OK, wantOK, v.Reason)
	}
}

// describe 是测试名辅助。
func describe(parts ...any) string { return fmt.Sprint(parts...) }
