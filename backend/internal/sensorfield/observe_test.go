package sensorfield

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestObserveFlushDelta 验证 Flush 只交出增量、样本/首末时间正确。
func TestObserveFlushDelta(t *testing.T) {
	reg := New(enabledConfig())
	sink := &captureSink{}
	ctx := context.Background()

	reg.Observe(1, "rsoc", "%", fixedTime(0))
	reg.Observe(1, "rsoc", "%", fixedTime(5))
	if err := reg.Flush(ctx, sink); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}
	batches := sink.batches()
	if len(batches) != 1 || len(batches[0]) != 1 {
		t.Fatalf("batches = %+v", batches)
	}
	obs, ok := findObs(batches[0], 1, "rsoc")
	if !ok {
		t.Fatalf("observation missing: %+v", batches[0])
	}
	if obs.Samples != 2 {
		t.Fatalf("Samples = %d want 2 (delta)", obs.Samples)
	}
	if !obs.FirstSeen.Equal(fixedTime(0)) || !obs.LastSeen.Equal(fixedTime(5)) {
		t.Fatalf("times = %v..%v", obs.FirstSeen, obs.LastSeen)
	}
	if obs.Unit != "%" {
		t.Fatalf("Unit = %q", obs.Unit)
	}

	// 无新观测 ⇒ 第二次 Flush 不产生批次。
	if err := reg.Flush(ctx, sink); err != nil {
		t.Fatalf("second Flush failed: %v", err)
	}
	if got := sink.totalObs(); got != 1 {
		t.Fatalf("second flush produced %d observations", got)
	}

	// 新观测 ⇒ 增量为 1，first 保持进程首见、last 前移。
	reg.Observe(1, "rsoc", "%", fixedTime(10))
	if err := reg.Flush(ctx, sink); err != nil {
		t.Fatalf("third Flush failed: %v", err)
	}
	all := sink.batches()
	last := all[len(all)-1]
	obs, ok = findObs(last, 1, "rsoc")
	if !ok || obs.Samples != 1 {
		t.Fatalf("delta batch = %+v", last)
	}
	if !obs.FirstSeen.Equal(fixedTime(0)) || !obs.LastSeen.Equal(fixedTime(10)) {
		t.Fatalf("delta times = %v..%v", obs.FirstSeen, obs.LastSeen)
	}

	// 成功 Flush 后合并内存：Samples 累计 3。
	fields := reg.KnownFields(1)
	if len(fields) != 1 || fields[0].Name != "rsoc" || fields[0].Samples != 3 || !fields[0].Observed {
		t.Fatalf("merged known fields = %+v", fields)
	}
}

// TestFlushFailureRetriesAndDoesNotMerge 验证 sink 失败：增量保留、不合并 known（降级仅声明）。
func TestFlushFailureRetriesAndDoesNotMerge(t *testing.T) {
	reg := New(enabledConfig())
	sink := &captureSink{fail: errors.New("db down")}
	ctx := context.Background()
	reg.Observe(1, "temperature_1", "°C", fixedTime(0))
	reg.Observe(1, "temperature_1", "°C", fixedTime(1))

	if err := reg.Flush(ctx, sink); err == nil {
		t.Fatal("Flush should fail when sink fails")
	}
	if got := sink.totalObs(); got != 0 {
		t.Fatalf("failed sink recorded %d observations", got)
	}
	if fields := reg.KnownFields(1); len(fields) != 0 {
		t.Fatalf("failed flush must not merge into known set (degrade to declarations only): %+v", fields)
	}
	// 恢复后重试：增量完整（2 条）。
	sink.mu.Lock()
	sink.fail = nil
	sink.mu.Unlock()
	if err := reg.Flush(ctx, sink); err != nil {
		t.Fatalf("retry Flush failed: %v", err)
	}
	batches := sink.batches()
	if len(batches) != 1 || len(batches[0]) != 1 || batches[0][0].Samples != 2 {
		t.Fatalf("retried batch = %+v", batches)
	}
}

// TestFlushNilSinkMergesMemory 验证 nil sink（测试/DB 不可用降级路径）仍合并内存并 bump epoch。
func TestFlushNilSinkMergesMemory(t *testing.T) {
	reg := New(enabledConfig())
	before := reg.Epoch()
	reg.Observe(3, "rsoc", "%", fixedTime(0))
	if err := reg.Flush(context.Background(), nil); err != nil {
		t.Fatalf("Flush(nil) failed: %v", err)
	}
	if reg.Epoch() <= before {
		t.Fatalf("flush merge did not bump epoch: %d -> %d", before, reg.Epoch())
	}
	fields := reg.KnownFields(3)
	if len(fields) != 1 || fields[0].Name != "rsoc" || fields[0].Samples != 1 {
		t.Fatalf("merged fields = %+v", fields)
	}
}

// TestFlushContextCanceled 验证取消的 ctx 不丢数据（下次重试仍完整）。
func TestFlushContextCanceled(t *testing.T) {
	reg := New(enabledConfig())
	sink := &captureSink{}
	reg.Observe(1, "rsoc", "%", fixedTime(0))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := reg.Flush(ctx, sink); err == nil {
		t.Fatal("canceled ctx must fail flush")
	}
	if got := sink.totalObs(); got != 0 {
		t.Fatalf("canceled flush wrote %d observations", got)
	}
	if err := reg.Flush(context.Background(), sink); err != nil {
		t.Fatalf("retry flush failed: %v", err)
	}
	if got := sink.totalObs(); got != 1 {
		t.Fatalf("retry flush observations = %d want 1", got)
	}
}

// TestObserveIgnoresInvalid 验证 deviceID=0 / 空名被忽略（DS-14 防御）。
func TestObserveIgnoresInvalid(t *testing.T) {
	reg := New(enabledConfig())
	sink := &captureSink{}
	reg.Observe(0, "rsoc", "%", fixedTime(0))
	reg.Observe(1, "", "%", fixedTime(0))
	reg.Observe(1, "rsoc", "%", fixedTime(0))
	if err := reg.Flush(context.Background(), sink); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}
	if got := sink.totalObs(); got != 1 {
		t.Fatalf("observations = %d want 1", got)
	}
}

// TestFlushMultipleDevicesAndUnits 验证多设备多字段批次排序与单位合并。
func TestFlushMultipleDevicesAndUnits(t *testing.T) {
	reg := New(enabledConfig())
	sink := &captureSink{}
	reg.Observe(2, "b_field", "V", fixedTime(1))
	reg.Observe(1, "a_field", "A", fixedTime(2))
	reg.Observe(1, "c_field", "", fixedTime(3))
	if err := reg.Flush(context.Background(), sink); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}
	batch := sink.batches()[0]
	if len(batch) != 3 {
		t.Fatalf("batch = %+v", batch)
	}
	if batch[0].DeviceID != 1 || batch[0].Name != "a_field" || batch[1].Name != "c_field" || batch[2].DeviceID != 2 {
		t.Fatalf("batch order = %+v", batch)
	}
	// 首次观察单位为空，后续不同单位首次出现时补齐。
	reg.Observe(1, "c_field", "Hz", fixedTime(4))
	if err := reg.Flush(context.Background(), sink); err != nil {
		t.Fatalf("second Flush failed: %v", err)
	}
	last := sink.batches()[1]
	obs, _ := findObs(last, 1, "c_field")
	if obs.Unit != "Hz" {
		t.Fatalf("unit backfill = %q", obs.Unit)
	}
}

// TestSnapshotAgeGaugeUpdated 验证 Flush 会刷新快照年龄指标（不 panic 即可，值可读）。
func TestSnapshotAgeGaugeUpdated(t *testing.T) {
	reg := New(enabledConfig())
	time.Sleep(2 * time.Millisecond)
	reg.Observe(1, "rsoc", "%", fixedTime(0))
	if err := reg.Flush(context.Background(), nil); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}
	if reg.SnapshotAge() < 0 {
		t.Fatal("negative snapshot age")
	}
}
