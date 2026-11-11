package telemetry

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"ehome/backend/internal/models"
)

// 本文件在 **SQLite 内存库**上覆盖落库/读取/解码层（§5.1）与双写旁路。
//
// 为什么 SQLite 够用：被测的是**我们自己的**逻辑（SQL 形状、状态机、幂等、
// 非阻塞语义），不是 PG 分区/索引（那由 datalifecycle 的 PG 集成测试覆盖）。
// 用 SQLite 让这些用例在任意环境可跑，不需要外部 PG。
//
// 表由本包自建（AutoMigrate 两个模型）——生产上它们是 partition_mgr 建的分区
// 母表，见 models/telemetry_frame.go 的范式说明。

func openTelemetryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// 用**文件**库而不是 :memory:：并发场景（双写 worker + 读）用内存库会
	// 因不同连接看到不同数据库而出现"表不存在"的假红。
	// 文件名带测试名，t.TempDir 保证隔离与自动清理。
	dsn := filepath.Join(t.TempDir(), "telemetry.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.TelemetryFrame{}, &models.TelemetrySlot{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

// synthFrame 生成一帧合成数据（固定 29 字段，模拟 device14）。
func synthFrame(t time.Time, tick int) []Reading {
	out := make([]Reading, 0, 29)
	for i := 1; i <= 16; i++ {
		// 电芯电压在 3.1–3.4 V 之间缓慢变化（1 mV 步长的真实量级）
		out = append(out, Reading{Name: fmt.Sprintf("cell_voltage_%d", i), Value: 3.20 + 0.001*float64((tick+i)%200)})
	}
	out = append(out,
		Reading{Name: "cell_voltage_max", Value: 3.40},
		Reading{Name: "cell_voltage_min", Value: 3.10},
		Reading{Name: "current", Value: float64(tick%50) * 0.37},
		Reading{Name: "cycle_count", Value: 322},
		Reading{Name: "fet_status", Value: 2},
		Reading{Name: "nominal_capacity", Value: 98.7},
		Reading{Name: "protection_status", Value: 0},
		Reading{Name: "remaining_capacity", Value: 60 + float64(tick%20)*0.01},
		Reading{Name: "rsoc", Value: float64(60 + tick%20)},
		Reading{Name: "temperature_1", Value: 25.45 + float64(tick%10)*0.01},
		Reading{Name: "temperature_2", Value: 23.65},
		Reading{Name: "temperature_3", Value: 25.65},
		Reading{Name: "total_voltage", Value: 50.0 + float64(tick%100)*0.01},
	)
	return out
}

// TestStoreEncodePersistDecodeRange 是落库 → DecodeRange 的端到端闭环。
func TestStoreEncodePersistDecodeRange(t *testing.T) {
	db := openTelemetryTestDB(t)
	dict := NewDBSlots(db)
	enc := NewEncoder(EncoderConfig{}, dict)
	store := NewStore(db, dict, enc)

	base := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	const n = 300
	for i := 0; i < n; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		if _, err := store.EncodeAndPersist(1, at, 0, synthFrame(at, i)); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}

	st, err := StatsForDevice(db, 1)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.Frames != n {
		t.Fatalf("frames = %d, want %d", st.Frames, n)
	}
	if st.KeyFrames == 0 {
		t.Fatal("必须至少有一个关键帧（首帧），否则解码无从起链")
	}
	// 平均 payload 必须明显小于"逐字段行 × 178.7 B"（29 × 178.7 ≈ 5.2 KB）
	if avg := st.AvgPayload(); avg > 200 {
		t.Fatalf("平均 payload = %.1f B，远超帧级编码的目标（应 ~12–20 B）", avg)
	} else {
		t.Logf("合成 29 字段帧：帧=%d 平均 payload=%.2f B 关键帧=%d", st.Frames, avg, st.KeyFrames)
	}

	// DecodeRange 取全窗
	points, dstats, err := DecodeRange(context.Background(), db, dict, 1, base, base.Add(time.Duration(n)*time.Second), nil)
	if err != nil {
		t.Fatalf("DecodeRange: %v", err)
	}
	if dstats.FramesDecoded != n {
		t.Errorf("decoded frames = %d, want %d", dstats.FramesDecoded, n)
	}
	if len(points) != n*29 {
		t.Errorf("points = %d, want %d (每帧 29 字段)", len(points), n*29)
	}
	// 字段过滤
	filtered, _, err := DecodeRange(context.Background(), db, dict, 1, base, base.Add(time.Duration(n)*time.Second), []string{"rsoc", "current"})
	if err != nil {
		t.Fatalf("DecodeRange filtered: %v", err)
	}
	if len(filtered) != n*2 {
		t.Errorf("filtered points = %d, want %d", len(filtered), n*2)
	}
	for _, p := range filtered {
		if p.Name != "rsoc" && p.Name != "current" {
			t.Fatalf("字段过滤失效：%q", p.Name)
		}
	}
}

// TestDecodeRangeGuards 钉住 §5.1 的解码护栏（超窗必须拒绝，超帧数必须可见截断）。
func TestDecodeRangeGuards(t *testing.T) {
	db := openTelemetryTestDB(t)
	dict := NewDBSlots(db)
	base := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)

	// 超过 MaxDecodeWindow 必须拒绝
	if _, _, err := DecodeRange(context.Background(), db, dict, 1, base, base.Add(MaxDecodeWindow+time.Second), nil); err == nil {
		t.Fatal("超过 MaxDecodeWindow 的窗口必须被拒绝（否则一次图表请求能拉 570 MB 进内存）")
	}
	// 空窗必须拒绝
	if _, _, err := DecodeRange(context.Background(), db, dict, 1, base, base, nil); err == nil {
		t.Fatal("空窗口必须被拒绝")
	}
	// ctx 已取消必须短路
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := DecodeRange(ctx, db, dict, 1, base, base.Add(time.Hour), nil); err == nil {
		t.Fatal("已取消的 ctx 必须短路返回")
	}
}

// TestLatestSnapshot 钉住「最近关键帧 + 其后增量」的快照语义（§5.1）。
func TestLatestSnapshot(t *testing.T) {
	db := openTelemetryTestDB(t)
	dict := NewDBSlots(db)
	enc := NewEncoder(EncoderConfig{}, dict)
	store := NewStore(db, dict, enc)

	base := time.Now().Add(-time.Hour).UTC()
	const n = 120
	var last []Reading
	for i := 0; i < n; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		last = synthFrame(at, i)
		if _, err := store.EncodeAndPersist(7, at, 0, last); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}

	snap, stats, err := LatestSnapshot(context.Background(), db, dict, 7)
	if err != nil {
		t.Fatalf("LatestSnapshot: %v", err)
	}
	if stats.FramesDecoded == 0 {
		t.Fatal("快照必须至少解码一帧")
	}
	if len(snap) != 29 {
		t.Fatalf("快照字段数 = %d, want 29（少字段 = 增量链起点不对）", len(snap))
	}
	for _, r := range last {
		sp, _ := SpecFor(r.Name)
		got, ok := snap[r.Name]
		if !ok {
			t.Errorf("快照缺字段 %q", r.Name)
			continue
		}
		if d := absFloat(got - r.Value); d > sp.MaxError()+1e-9 {
			t.Errorf("字段 %q 快照=%v 原值=%v 误差=%v > %v", r.Name, got, r.Value, d, sp.MaxError())
		}
	}

	// 没有数据的设备必须返回空快照 + 无错误（新建设备是合法状态）
	empty, _, err := LatestSnapshot(context.Background(), db, dict, 999)
	if err != nil {
		t.Fatalf("空设备快照不该报错: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("空设备快照应有 0 字段，得到 %d", len(empty))
	}
}

// TestPersistFramesIdempotent 钉住主键冲突幂等（重放安全，§9.6 可回滚）。
func TestPersistFramesIdempotent(t *testing.T) {
	db := openTelemetryTestDB(t)
	dict := NewDBSlots(db)
	enc := NewEncoder(EncoderConfig{}, dict)
	store := NewStore(db, dict, enc)

	at := time.Date(2026, 11, 4, 1, 2, 3, 0, time.UTC)
	frame, err := enc.EncodeFrame(3, at, 0, synthFrame(at, 0))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := store.PersistFrames([]EncodedFrame{frame}); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if err := store.PersistFrames([]EncodedFrame{frame}); err != nil {
		t.Fatalf("replay persist must be idempotent, got: %v", err)
	}
	st, err := StatsForDevice(db, 3)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.Frames != 1 {
		t.Fatalf("重放后帧数 = %d, want 1（主键必须幂等）", st.Frames)
	}
}

// TestEncoderSeqDisambiguatesSameTS 钉住 §8「秒内多帧」：同 ts 多帧不能互相覆盖。
func TestEncoderSeqDisambiguatesSameTS(t *testing.T) {
	db := openTelemetryTestDB(t)
	dict := NewDBSlots(db)
	enc := NewEncoder(EncoderConfig{}, dict)
	store := NewStore(db, dict, enc)

	at := time.Date(2026, 11, 4, 2, 0, 0, 0, time.UTC)
	const sameTsFrames = 5
	for i := 0; i < sameTsFrames; i++ {
		if _, err := store.EncodeAndPersist(5, at, 0, synthFrame(at, i)); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	st, err := StatsForDevice(db, 5)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.Frames != sameTsFrames {
		t.Fatalf("同一时间戳的 %d 帧只落了 %d 帧 —— seq 没生效，后帧覆盖了前帧（静默丢数据）",
			sameTsFrames, st.Frames)
	}
}

// TestDualWriterNonBlockingAndIsolated 钉住双写旁路的三条硬约束。
func TestDualWriterNonBlockingAndIsolated(t *testing.T) {
	db := openTelemetryTestDB(t)
	dict := NewDBSlots(db)

	var writeErrs int64
	var mu sync.Mutex
	w, err := NewDualWriter(db, dict, DualWriterConfig{
		QueueSize: 4, // 故意极小，逼出"队列满必须丢而不是阻塞"
		OnWriteError: func(error) {
			mu.Lock()
			writeErrs++
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("NewDualWriter: %v", err)
	}

	base := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	// 灌 500 帧，远多于队列深度：Observe 必须全部**立即返回**（不阻塞）。
	done := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			w.Observe(11, base.Add(time.Duration(i)*time.Second), 0, synthFrame(base, i), nil)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Observe 阻塞了 —— 双写绝不能反压写路径")
	}
	w.Close()

	st := w.Stats()
	if st.Encoded+st.Dropped != 500 {
		t.Errorf("encoded(%d)+dropped(%d) != 500", st.Encoded, st.Dropped)
	}
	if st.Encoded == 0 {
		t.Error("至少要有帧落库（全部丢弃说明 worker 没跑起来）")
	}
	t.Logf("双写：encoded=%d dropped=%d failed=%d payload=%dB", st.Encoded, st.Dropped, st.Failed, st.PayloadSum)

	// 落库的帧必须能解出正确的值（双写的价值就在这里：影子可对拍）
	dbStats, err := StatsForDevice(db, 11)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if dbStats.Frames != st.Encoded {
		t.Errorf("库内帧数 %d != encoded %d", dbStats.Frames, st.Encoded)
	}
}

// TestDualWriterTablesMissingIsFatalError 钉住**无条件启用后的失败语义**
// （契约 §3.4）：表不存在不再"安静禁用"，而是返回错误让启动期 Fatal。
//
// 为什么这条必须反过来钉：此前是影子旁路，表缺失 fail-open 是合理的；
// 现在帧表是主路径，静默禁用等于「帧数据缺失一整天，而所有面板都正常」——
// 那种失败要到用户发现图表只有 60s 心跳点时才会暴露，代价远大于启动失败。
func TestDualWriterTablesMissingIsFatalError(t *testing.T) {
	db := openTelemetryTestDB(t)
	if err := db.Migrator().DropTable(&models.TelemetryFrame{}, &models.TelemetrySlot{}); err != nil {
		t.Fatalf("drop: %v", err)
	}
	w, err := NewDualWriter(db, NewDBSlots(db), ConfigFromEnv())
	if w != nil {
		w.Close()
		t.Fatal("表不存在时不应返回可用的 writer")
	}
	if !isErr(err, ErrTablesMissing) {
		t.Fatalf("表不存在必须返回 ErrTablesMissing（调用方据此 Fatal），得到 %v", err)
	}
}

// TestConfigFromEnvKeepsParamsNotSwitch 钉住「删开关但保留参数」（契约 §3.2）。
//
// 三个被删的开关名都不在本包里；剩下的是两个参数。这条同时挡住
// "顺手把参数也删了" 的过度删除（心跳/队列深度仍需可调）。
func TestConfigFromEnvKeepsParamsNotSwitch(t *testing.T) {
	t.Setenv(EnvQueueSize, "123")
	t.Setenv(EnvKeyFrameSeconds, "30")
	cfg := ConfigFromEnv()
	if cfg.QueueSize != 123 {
		t.Errorf("QueueSize = %d, want 123（EnvQueueSize 是参数，必须保留）", cfg.QueueSize)
	}
	if cfg.KeyFrameInterval != 30*time.Second {
		t.Errorf("KeyFrameInterval = %s, want 30s（EnvKeyFrameSeconds 是参数，必须保留）", cfg.KeyFrameInterval)
	}
}

// TestDualWriterPersistFailureIsolated 钉住「双写写库失败不影响主写入」。
func TestDualWriterPersistFailureIsolated(t *testing.T) {
	db := openTelemetryTestDB(t)
	dict := NewDBSlots(db)
	w, err := NewDualWriter(db, dict, DualWriterConfig{})
	if err != nil {
		t.Fatalf("NewDualWriter: %v", err)
	}
	// 把表删掉，让后续 PersistFrames 全部失败
	if err := db.Migrator().DropTable(&models.TelemetryFrame{}); err != nil {
		t.Fatalf("drop: %v", err)
	}
	base := time.Date(2026, 11, 4, 4, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		w.Observe(13, base.Add(time.Duration(i)*time.Second), 0, synthFrame(base, i), nil)
	}
	w.Close()
	st := w.Stats()
	if st.Failed == 0 {
		t.Error("写库失败必须被计数（否则影子静默失效，没人发现）")
	}
	if st.Encoded != 0 {
		t.Errorf("表不存在时不应有成功写入，encoded=%d", st.Encoded)
	}
	// 关键断言：调用 Observe 全程没有 panic、没有阻塞、没有错误外泄。
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
