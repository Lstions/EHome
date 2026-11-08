package datalifecycle

// L3 rollup 单元测试（SQLite）：编码往返、聚合语义、分钟切换落库、坏行容错。
//
// 为什么这些必须真跑：方案 §5.2 的核心承诺是「宽行 = 每设备每分钟一行」+
// 「旁路聚合不扫库」。前者是主键语义（写两遍必须只有一行），后者是性能前提
// （Flush 只搬内存桶）。两者都只能用行为断言，不能靠代码审阅。

import (
	"context"
	"math"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// newRollupTestDB 建一个只含 telemetry_rollup_1m 的 SQLite 库（普通表，不分区）。
// 生产 PG 的分区母表由 partition_mgr 建，这里只验证读写语义。
func newRollupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		// 内存库的每条新连接是独立库：钉死单连接（history-batch 的既有教训）。
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&models.TelemetryRollup1m{}); err != nil {
		t.Fatalf("automigrate telemetry_rollup_1m: %v", err)
	}
	return db
}

func TestRollupBucketTruncatesToUTCMinute(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	at := time.Date(2026, 11, 4, 10, 23, 45, 123456789, loc)
	got := RollupBucket(at)
	want := time.Date(2026, 11, 4, 2, 23, 0, 0, time.UTC) // 本地 10:23 = UTC 02:23
	if !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("RollupBucket(%v) = %v (%v), want %v (UTC)", at, got, got.Location(), want)
	}
}

func TestRollupCollectorAggregatesAvgMinMaxLast(t *testing.T) {
	c := NewRollupCollector(nil) // nil db: 只测累加，不落库
	base := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	c.Observe(14, []RollupSample{{Name: "cell_voltage_1", Value: 3.300}, {Name: "temperature_1", Value: 25.0}}, base.Add(5*time.Second))
	c.Observe(14, []RollupSample{{Name: "cell_voltage_1", Value: 3.302}, {Name: "temperature_1", Value: 25.4}}, base.Add(35*time.Second))
	c.Observe(14, []RollupSample{{Name: "cell_voltage_1", Value: 3.298}}, base.Add(58*time.Second))
	if got := c.PendingBuckets(); got != 1 {
		t.Fatalf("同一分钟的 3 帧必须累加进 1 个桶, got %d", got)
	}
	c.mu.Lock()
	acc := c.pending[rollupKey{deviceID: 14, bucket: base}]
	c.mu.Unlock()
	if acc == nil {
		t.Fatal("缺少 (device14, 10:00) 桶")
	}
	cv := acc.fields["cell_voltage_1"]
	if cv == nil {
		t.Fatal("缺少 cell_voltage_1 统计量")
	}
	if cv.Count != 3 {
		t.Errorf("count = %d, want 3", cv.Count)
	}
	if math.Abs(cv.Min-3.298) > 1e-9 || math.Abs(cv.Max-3.302) > 1e-9 {
		t.Errorf("min/max = %v/%v, want 3.298/3.302", cv.Min, cv.Max)
	}
	if math.Abs(cv.Last-3.298) > 1e-9 {
		t.Errorf("last = %v, want 3.298 (最后一帧)", cv.Last)
	}
	if math.Abs(cv.Avg-3.300) > 1e-9 {
		t.Errorf("avg = %v, want 3.300", cv.Avg)
	}
	if tp := acc.fields["temperature_1"]; tp == nil || tp.Count != 2 {
		t.Errorf("temperature_1 统计量缺失或 count 错: %+v", tp)
	}
}

func TestRollupCollectorFlushOnlyCompletedBuckets(t *testing.T) {
	db := newRollupTestDB(t)
	c := NewRollupCollector(db)
	base := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	c.SetClock(func() time.Time { return base.Add(90 * time.Second) }) // 当前分钟 = 10:01

	c.Observe(14, []RollupSample{{Name: "rsoc", Value: 88}}, base.Add(10*time.Second)) // 已结束
	c.Observe(14, []RollupSample{{Name: "rsoc", Value: 87}}, base.Add(70*time.Second)) // 当前分钟
	rows, err := c.FlushCompleted(context.Background())
	if err != nil {
		t.Fatalf("FlushCompleted: %v", err)
	}
	if rows != 1 {
		t.Fatalf("只应落已结束的 1 个桶, got %d", rows)
	}
	if c.PendingBuckets() != 1 {
		t.Fatalf("当前分钟桶必须留在内存继续累加, pending=%d", c.PendingBuckets())
	}
	var stored []models.TelemetryRollup1m
	if err := db.Find(&stored).Error; err != nil {
		t.Fatalf("读回 rollup 行: %v", err)
	}
	if len(stored) != 1 || !stored[0].Bucket.Equal(base) || stored[0].DeviceID != 14 {
		t.Fatalf("落库行不符: %+v", stored)
	}
	stats, err := DecodeRollupPayload(stored[0].Payload)
	if err != nil {
		t.Fatalf("解码落库 payload: %v", err)
	}
	if got := stats["rsoc"]; got == nil || got.Last != 88 {
		t.Fatalf("payload 里 rsoc = %+v, want last=88", got)
	}
}

// 同一 (device, bucket) 写两次必须只有一行（主键语义 + UPSERT 覆盖）。
func TestRollupFlushIsIdempotentPerBucket(t *testing.T) {
	db := newRollupTestDB(t)
	base := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)

	for round, value := range []float64{10, 20} {
		c := NewRollupCollector(db)
		c.SetClock(func() time.Time { return base.Add(2 * time.Minute) })
		c.Observe(15, []RollupSample{{Name: "output_active_power", Value: value}}, base.Add(30*time.Second))
		if _, err := c.FlushCompleted(context.Background()); err != nil {
			t.Fatalf("round %d flush: %v", round, err)
		}
	}
	var count int64
	db.Model(&models.TelemetryRollup1m{}).Count(&count)
	if count != 1 {
		t.Fatalf("同 (device,bucket) 必须只有 1 行, got %d", count)
	}
	var row models.TelemetryRollup1m
	db.First(&row)
	stats, err := DecodeRollupPayload(row.Payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := stats["output_active_power"]; got == nil || got.Last != 20 {
		t.Fatalf("覆盖后 last = %+v, want 20", got)
	}
}

func TestRollupPayloadRoundTrip(t *testing.T) {
	fields := map[string]*RollupFieldStat{
		"cell_voltage_1": {Avg: 3.3005, Min: 3.298, Max: 3.302, Last: 3.301, Count: 60},
		"temperature_1":  {Avg: -12.5, Min: -13.25, Max: -11.75, Last: -12.0, Count: 60},
		"rsoc":           {Avg: 88.5, Min: 88, Max: 89, Last: 89, Count: 60},
	}
	payload, err := EncodeRollupPayloadV1(fields)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeRollupPayload(payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != len(fields) {
		t.Fatalf("字段数 = %d, want %d", len(got), len(fields))
	}
	for name, want := range fields {
		gotStat, ok := got[name]
		if !ok {
			t.Fatalf("缺字段 %q", name)
		}
		if *gotStat != *want {
			t.Errorf("字段 %q 往返不一致: got %+v want %+v", name, *gotStat, *want)
		}
	}

	// 确定性编码：同样输入必须同样字节（否则对拍/去重都不可靠）。
	again, err := EncodeRollupPayloadV1(fields)
	if err != nil {
		t.Fatalf("encode again: %v", err)
	}
	if string(again) != string(payload) {
		t.Error("同样输入两次编码结果不同 —— 字段排序不确定")
	}
}

func TestRollupPayloadRejectsCorrupt(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
	}{
		{"空 payload", nil},
		{"未知版本", []byte{0x7f, 0, 1}},
		{"v1 头截断", []byte{1, 0}},
		{"v1 字段数 0", []byte{1, 0, 0}},
		{"v1 名字长度 0", []byte{1, 0, 1, 0}},
		{"v1 统计量截断", func() []byte {
			b := []byte{1, 0, 1, 1, 'a'}
			return b // 名字后应有 36B 统计量，这里没有
		}()},
	}
	for _, c := range cases {
		if _, err := DecodeRollupPayload(c.payload); err == nil {
			t.Errorf("%s: 必须报错（坏行应被跳过而不是构造出假数据）", c.name)
		}
	}
}

// ReadRollupStats：只返回请求字段命中的桶；坏行跳过但计数；缺该字段的桶不算坏行。
func TestReadRollupStats(t *testing.T) {
	db := newRollupTestDB(t)
	base := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	writeRow := func(deviceID uint, bucket time.Time, payload []byte) {
		if err := db.Create(&models.TelemetryRollup1m{DeviceID: deviceID, Bucket: bucket, Payload: payload}).Error; err != nil {
			t.Fatalf("seed rollup: %v", err)
		}
	}
	for i := 0; i < 3; i++ {
		payload, err := EncodeRollupPayloadV1(map[string]*RollupFieldStat{
			"rsoc": {Avg: float64(80 + i), Min: float64(80 + i), Max: float64(80 + i), Last: float64(80 + i), Count: 60},
		})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		writeRow(14, base.Add(time.Duration(i)*time.Minute), payload)
	}
	// 坏行（未知版本）与「没有该字段」的行各一条。
	writeRow(14, base.Add(3*time.Minute), []byte{0x7f, 0, 1})
	otherPayload, err := EncodeRollupPayloadV1(map[string]*RollupFieldStat{
		"temperature_1": {Avg: 1, Min: 1, Max: 1, Last: 1, Count: 1},
	})
	if err != nil {
		t.Fatalf("encode other: %v", err)
	}
	writeRow(14, base.Add(4*time.Minute), otherPayload)

	buckets, stats, bad, err := ReadRollupStats(context.Background(), db, 14, "rsoc", base, base.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("ReadRollupStats: %v", err)
	}
	if bad != 1 {
		t.Errorf("坏行计数 = %d, want 1", bad)
	}
	if len(buckets) != 3 || len(stats) != 3 {
		t.Fatalf("命中 %d 个桶, want 3（坏行与缺字段行都不该出现）", len(buckets))
	}
	for i := range buckets {
		if !buckets[i].Equal(base.Add(time.Duration(i) * time.Minute)) {
			t.Errorf("桶 %d = %v, want 升序按分钟", i, buckets[i])
		}
		if want := float64(80 + i); stats[i].Last != want {
			t.Errorf("桶 %d last = %v, want %v", i, stats[i].Last, want)
		}
	}
}

// Sink()/FlushSink() 是 databus 接线用的注入面：必须 nil 安全、且与 Observe/Flush
// 语义完全等价（接线时 databus 只拿到这两个函数值，不可能"多一条隐式行为"）。
func TestRollupCollectorSinkFactories(t *testing.T) {
	// nil collector：两个工厂都必须返回**可调用**的空实现（开关关闭时装配空 sink）。
	var nilCollector *RollupCollector
	nilSink := nilCollector.Sink()
	nilSink(14, []RollupSample{{Name: "rsoc", Value: 1}}, time.Now()) // 不 panic 即通过
	if rows, err := nilCollector.FlushSink()(context.Background()); rows != 0 || err != nil {
		t.Fatalf("nil collector flush = %d/%v, want 0/nil", rows, err)
	}
	if nilCollector.PendingBuckets() != 0 {
		t.Fatal("nil collector 的 PendingBuckets 必须为 0")
	}

	// 真 collector：Sink 必须等价于 Observe，FlushSink 必须等价于 FlushCompleted。
	db := newRollupTestDB(t)
	c := NewRollupCollector(db)
	base := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	c.SetClock(func() time.Time { return base.Add(2 * time.Minute) })

	sink := c.Sink()
	sink(14, []RollupSample{{Name: "rsoc", Value: 80}}, base.Add(10*time.Second))
	sink(14, []RollupSample{{Name: "rsoc", Value: 82}}, base.Add(40*time.Second))
	if got := c.PendingBuckets(); got != 1 {
		t.Fatalf("Sink 两次调用应归入 1 个桶, got %d", got)
	}
	rows, err := c.FlushSink()(context.Background())
	if err != nil || rows != 1 {
		t.Fatalf("FlushSink = %d/%v, want 1/nil", rows, err)
	}
	var stored []models.TelemetryRollup1m
	if err := db.Find(&stored).Error; err != nil || len(stored) != 1 {
		t.Fatalf("落库行 %d/%v, want 1", len(stored), err)
	}
	stats, err := DecodeRollupPayload(stored[0].Payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := stats["rsoc"]; got == nil || got.Count != 2 || got.Min != 80 || got.Max != 82 {
		t.Fatalf("rsoc 统计量 = %+v, want count=2 min=80 max=82", got)
	}
}

// 分区函数在 SQLite 上必须 no-op（与既有 partition 系列同契约）。
func TestTelemetryRollupPartitionHelpersNoopOnSQLite(t *testing.T) {
	db := newRollupTestDB(t)
	if err := EnsureTelemetryRollup1mPartitions(db, 3); err != nil {
		t.Fatalf("EnsureTelemetryRollup1mPartitions on sqlite: %v", err)
	}
	if dropped, err := DropTelemetryRollup1mPartitionsBefore(db, time.Now()); err != nil || dropped != nil {
		t.Fatalf("DropTelemetryRollup1mPartitionsBefore on sqlite = %v/%v, want nil/nil", dropped, err)
	}
	if err := MigrateTelemetryRollup1mToPartitioned(db); err != nil {
		t.Fatalf("MigrateTelemetryRollup1mToPartitioned on sqlite: %v", err)
	}
}

// 分区母表 DDL/索引清单的登记完整性（SQLite 可跑，不需要 PG）：
//   - partitionParentDDL 必须登记本表（否则 createPartitionedParent 回退到
//     unified_data 的列结构，建出一张列不对的表）；
//   - partitionParentIndexes 必须登记本表且**清单非 nil**（nil 与"未登记"在
//     effectiveParentIndexTemplates 里都会回退到 unified_data 的索引，套到宽行表
//     上会引用不存在的列 ⇒ 建表报错）。空切片才是"登记且无索引"。
func TestTelemetryRollupRegisteredInPartitionTemplates(t *testing.T) {
	ddl, ok := partitionParentDDL[TelemetryRollupTable]
	if !ok {
		t.Fatalf("partitionParentDDL 缺 %q 登记 —— createPartitionedParent 会回退到 unified_data 的列结构", TelemetryRollupTable)
	}
	for _, want := range []string{"device_id", "bucket", "payload", "PARTITION BY RANGE (bucket)"} {
		if !containsFold(ddl, want) {
			t.Errorf("partitionParentDDL[%q] 缺 %q:\n%s", TelemetryRollupTable, want, ddl)
		}
	}
	idx, ok := partitionParentIndexes[TelemetryRollupTable]
	if !ok {
		t.Fatalf("partitionParentIndexes 缺 %q 登记 —— 会回退到 unified_data 的 6 条索引并引用本表不存在的列", TelemetryRollupTable)
	}
	if idx == nil {
		t.Fatalf("partitionParentIndexes[%q] 是 nil（= 未登记语义）；无二级索引请用空切片", TelemetryRollupTable)
	}
	if len(idx) != 0 {
		t.Errorf("宽行表只应保留主键 (device_id,bucket)，不应有二级索引；实际 %d 条", len(idx))
	}
	// 迁移列清单：本表没有统一 id 列，createPartitionedParent 迁移路径不适用；
	// 这里只断言"没有把它登记成 unified_data 列清单"，防未来误接线。
	if cols, ok := partitionCopyColumns[TelemetryRollupTable]; ok && foldedContains(cols, "sensor_name") {
		t.Errorf("partitionCopyColumns[%q] = %q 误用了 unified_data 列清单", TelemetryRollupTable, cols)
	}
}

func containsFold(haystack, needle string) bool {
	return foldedContains(haystack, needle)
}

func foldedContains(haystack, needle string) bool {
	h := []rune(haystack)
	n := []rune(needle)
	for i := 0; i+len(n) <= len(h); i++ {
		match := true
		for j := range n {
			if toLowerRune(h[i+j]) != toLowerRune(n[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func toLowerRune(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}

// 内存桶 TTL：DB 长期不可用时丢老桶，不无界增长。
//
// 注意用**真库**：NewRollupCollector(nil) 的 FlushCompleted 是显式 no-op（开关关闭时
// 装配"透明 collector"的语义），那条路径测不到 TTL 丢弃。
func TestRollupCollectorDropsStaleBuckets(t *testing.T) {
	c := NewRollupCollector(newRollupTestDB(t))
	base := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	c.SetBucketTTL(1 * time.Hour)
	c.Observe(14, []RollupSample{{Name: "rsoc", Value: 50}}, base) // 很老的桶
	c.SetClock(func() time.Time { return base.Add(3 * time.Hour) })
	dropReason := ""
	c.SetFlushHooks(nil, func(reason string) { dropReason = reason })
	if _, err := c.FlushCompleted(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if c.PendingBuckets() != 0 {
		t.Errorf("过期桶必须被丢弃, pending=%d", c.PendingBuckets())
	}
	if dropReason == "" {
		t.Error("丢弃过期桶必须有打点（否则容量问题静默）")
	}
}
