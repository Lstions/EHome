package sensorfield

import (
	"context"
	"sync"
	"testing"
)

// TestConcurrentRegistryUsage 是 -race 并发门禁：读路径（Compile/Resolve/Eval）
// 与写路径（Observe/Flush/SetObservations/SetDeviceType/SetFieldMeta）并发。
func TestConcurrentRegistryUsage(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("dev", []Field{
		numField("rsoc"), numField("temperature_1"), numField("temperature_2"),
	})
	reg.SetDeviceType(1, "dev")
	mustSetAliases(t, reg, AliasRule{Semantic: "soc", Candidates: []string{"rsoc"}})
	mustSetVirtuals(t, reg, VirtualDef{Name: "double_soc", Fn: "scale", Args: []string{"@soc"}, Scale: 2})
	frame := readings("rsoc", 50.0, "temperature_1", 30.0, "temperature_2", 40.0)

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = reg.Enabled()
				_ = reg.Epoch()
				_ = reg.KnownFields(1)
				_ = reg.SemanticsOf("rsoc")
				_ = reg.Aliases()
				_ = reg.Virtuals()
				_ = reg.Config()
				if p, err := reg.Compile(1, "temperature_*:max"); err == nil {
					_ = EvalPredicate(p, frame, CmpGT, 20)
					_ = p.Eval(frame)
				}
				if p, err := reg.Compile(1, "@soc"); err == nil {
					_ = EvalPredicate(p, frame, CmpGTE, 30)
				}
				if p, err := reg.Compile(1, "virtual:double_soc"); err == nil {
					_ = EvalPredicate(p, frame, CmpLTE, 200)
				}
				_, _ = reg.Resolve(1, "temperature_*:max")
				_, _ = reg.Resolve(1, "@soc")
				_, _ = reg.Resolve(1, "rsoc")

				if id == 0 && i%10 == 0 {
					reg.Observe(1, "temperature_2", "°C", fixedTime(int64(i)))
				}
				if id == 0 && i%50 == 0 {
					if err := reg.Flush(context.Background(), nil); err != nil {
						t.Errorf("concurrent Flush failed: %v", err)
					}
				}
				if id == 1 && i%20 == 0 {
					reg.SetObservations(1, []Field{
						numField("rsoc"), numField("temperature_1"), numField("temperature_2"),
					})
				}
				if id == 2 && i%30 == 0 {
					reg.SetFieldMeta([]Field{{Name: "rsoc", ValueType: TypeNumber, Unit: "%"}})
				}
				if id == 3 && i%40 == 0 {
					reg.SetDeviceType(1, "dev")
				}
			}
		}(g)
	}
	wg.Wait()
}

// TestConcurrentObserveAggregation 验证并发 Observe 后 Samples 精确等于次数。
func TestConcurrentObserveAggregation(t *testing.T) {
	reg := New(enabledConfig())
	sink := &captureSink{}
	const goroutines = 8
	const perGoroutine = 100
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				reg.Observe(1, "temperature_1", "°C", fixedTime(int64(id*perGoroutine+i)))
				reg.Observe(2, "temperature_1", "°C", fixedTime(int64(id*perGoroutine+i)))
			}
		}(g)
	}
	wg.Wait()
	if err := reg.Flush(context.Background(), sink); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}
	batch := sink.batches()[0]
	if len(batch) != 2 {
		t.Fatalf("batch size = %d want 2", len(batch))
	}
	for _, obs := range batch {
		if obs.Samples != goroutines*perGoroutine {
			t.Fatalf("device %d samples = %d want %d", obs.DeviceID, obs.Samples, goroutines*perGoroutine)
		}
	}
}
