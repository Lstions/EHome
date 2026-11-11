package telemetry

import (
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"
)

// L2 帧级编码的写入路径：解析成功的每一帧额外编码进 telemetry_frame。
//
// ============ 无条件启用（2026-11-07 用户裁决）============
//
// 此前它是「默认关闭的旁路」，一个环境变量开关决定是否构造。
// 用户裁决（docs/设计/遥测存储-L1L2无条件启用-契约-2026-11-07.md §1）要求
// **删除开关、L2 无条件生效**：帧表从「影子」变成**主路径之一**。
//
// 因此：
//
//	· 不存在「开关关闭 ⇒ 返回 nil」这条路径了；构造失败 ⇒ 调用方 Fatal
//	  （契约 §3.4：不允许「主写成功但帧表没建」的半残状态）；
//	· 保留的两个环境变量是**参数**不是开关：
//	  EnvQueueSize（队列深度）、EnvKeyFrameSeconds（关键帧间隔）。
//
// ============ 两条仍在的硬约束 ============
//
//  1. **失败绝不影响主写入**：编码/写库错误在这里被吞掉并计数，绝不返回给写
//     路径。unified_data 是用户可见功能（图表/告警/自动化都读它），帧表是压缩
//     表示；让表示层把主路径拖垮是本末倒置（契约 §3.4 的失败语义表）。
//  2. **不阻塞写路径**：Observe 只做「投递到有界队列」，编码与 DB 写在**独立
//     goroutine** 里。队列满 ⇒ 丢弃 + 计数，绝不反压。理由同 1：写路径是
//     1 Hz × 每设备，任何同步等待都会被放大成采集延迟。
//
// ============ 为什么用回调而不是直接记 Prometheus 指标 ============
//
// 本包的写范围是 internal/telemetry/**，pkg/metrics 与 cmd/server 属于公共面
// （别的成员在改）。用 OnWriteError / OnDrop 回调把可观测性**注入**进来，
// 本包保持纯逻辑 + 可测，接线方自己决定指标名与标签。
//
// ============ 为什么包一层 sink 而不是在 databus 里直接写 SQL ============
//
// 数据面零依赖红线：internal/databus 只允许看到一个 func 值，不允许 import
// 本包的实现细节。接线方只需 consumer.SetTelemetrySink(dual.Observe)。
type DualWriter struct {
	store *Store
	enc   *Encoder

	queue  chan dualItem
	stop   chan struct{}
	once   sync.Once
	wg     sync.WaitGroup
	onErr  func(error)
	onDrop func()

	dropped    atomic.Int64
	writeFails atomic.Int64
	encoded    atomic.Int64
	payloadSum atomic.Int64

	// flushInterval 是定时 flush 周期，**构造后不可变**（只读）。
	//
	// 做成字段而不是直接用常量：用例要能在**秒级**验证「到点必落盘」，
	// 等 2 秒 × 多条用例会把包测试拖成分钟级；缩短周期不改变被测语义
	// （仍然是"时间驱动"而不是"数量驱动"）。
	//
	// ⚠ 不可变是**正确性要求**，不是风格偏好：run() 在无锁情况下读它，
	// 任何构造后的写入都是 data race。来源只有一个：DualWriterConfig.FlushInterval。
	flushInterval time.Duration
}

// dualItem 是队列里的一次观测。
type dualItem struct {
	deviceID          uint
	at                time.Time
	commandTemplateID int64
	readings          []Reading
	// raw 是重组后的原始总线帧，只在关键帧上落库（原始数据退役 §4.4 方案 A）。
	//
	// 为什么随队列一起传而不是在 worker 里回查：worker 与写路径不在同一 goroutine，
	// 而且那时原始字节早已被复用的缓冲区覆盖（reassembler 的 caller 只在 Handle
	// 内持有它）。跟着 item 走是唯一安全的传递方式。
	raw []byte
}

// 配置项名（环境变量）。
//
// 这两个是**参数**不是开关（契约 §3.2 明确保留）：删掉的只有那个
// "是否启用"的开关（名字见契约 §1，本包已不再引用它）。
const (
	// EnvKeyFrameSeconds 关键帧间隔，默认 60s（与 L1 心跳同口径，方案 §3.3）。
	EnvKeyFrameSeconds = "EHOME_TELEMETRY_HEARTBEAT_SECONDS"
	// EnvQueueSize 队列深度，默认 4096。
	EnvQueueSize = "EHOME_TELEMETRY_DUAL_WRITE_QUEUE"
)

// DualWriterConfig 是双写参数。
type DualWriterConfig struct {
	// QueueSize 队列深度（0 = 4096）。
	QueueSize int
	// KeyFrameInterval 强制关键帧间隔（0 = 60s）。
	KeyFrameInterval time.Duration
	// SpecVersion 量化规格版本（0 = SpecVersion）。
	SpecVersion uint16
	// OnWriteError 是写库/编码失败回调（nil = 只记日志）。
	// 影子校验用它累计"双写失败但主写入成功"的帧数 —— 那是必须为 0 的指标。
	OnWriteError func(error)
	// OnDrop 是队列满丢弃回调（nil = 只计数）。
	OnDrop func()
	// FlushInterval 是定时 flush 周期（0 = dualWriteFlushInterval，即 2s）。
	//
	// ==================== 为什么它是构造参数而不是一个 setter ====================
	//
	// 第一版把它做成 `SetFlushInterval()` 方法，注释写「只在构造后、首次 Observe
	// 之前调用才有效」—— 那是**错的措辞**，掩盖了真正的问题：
	//
	//	NewDualWriter 里 `go w.run()` 启动 worker；
	//	worker 立刻在 run() 里读 `w.flushInterval`；
	//	测试在构造函数返回后才写 `w.flushInterval`。
	//
	// 两者**没有 happens-before 关系** ⇒ 这不是「可能读到旧值」的语义问题，
	// 是 Go memory model 下的 **data race / undefined behavior**。
	// `go test -race` 100% 复现，而 Makefile 的 `make test` 就是 `-race ./...`。
	//
	// 修法不是加锁，而是**让它不可变**：周期是构造参数，构造完成后没有任何
	// 路径再写它 ⇒ worker 读到的一定是最终值，天然无竞态。
	//
	// 为什么不保留 setter 加 atomic：worker 里的 ticker 是启动时创建的，
	// 改周期仍然要重启 worker 才生效 —— 那只解决竞态，不解决语义，反而更误导。
	//
	// 判据（值得记住）：**任何「构造后还能改」的字段，只要运行期 goroutine 会读它，
	// 就必须有同步、或改成不可变。** 而「测试钩子」正是最容易违反这条的地方 ——
	// 作者觉得「生产只读、只有测试写」，但测试恰恰是**边读边写**的场景。
	FlushInterval time.Duration
}

// keyFrameIntervalFromEnv 读关键帧间隔（复用 L1 的心跳配置名，两者同口径）。
func keyFrameIntervalFromEnv() time.Duration {
	v, ok := os.LookupEnv(EnvKeyFrameSeconds)
	if !ok {
		return DefaultKeyFrameInterval
	}
	sec, err := strconv.Atoi(v)
	if err != nil || sec <= 0 {
		return DefaultKeyFrameInterval
	}
	return time.Duration(sec) * time.Second
}

// ConfigFromEnv 从环境变量组装**参数**（不是开关 —— 启不启用不再可选）。
//
// 调用方：
//
//	w, err := telemetry.NewDualWriter(db, telemetry.NewDBSlots(db), telemetry.ConfigFromEnv())
//	if err != nil { /* 契约 §3.4：Fatal */ }
//	defer w.Close()
//	consumer.SetTelemetrySink(w.Observe)
func ConfigFromEnv() DualWriterConfig {
	cfg := DualWriterConfig{KeyFrameInterval: keyFrameIntervalFromEnv()}
	if v, ok := os.LookupEnv(EnvQueueSize); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.QueueSize = n
		}
	}
	return cfg
}

// NewDualWriter 构造双写器并启动后台 worker。
//
// 返回值 error 在 nil db / nil 字典 / **表不存在**时非 nil。
//
// ⚠ 失败语义（契约 §3.4）：L2 已是主路径，调用方**必须 Fatal**，不允许
// "主写成功但帧表没建"的半残状态 —— 那会让帧表静默缺失一整天数据，
// 而所有面板看起来都正常（unified_data 还在写）。启动期挡住比运行期静默丢好。
func NewDualWriter(db *gorm.DB, dict SlotDictionary, cfg DualWriterConfig) (*DualWriter, error) {
	if db == nil {
		return nil, ErrNilDB
	}
	if dict == nil {
		return nil, ErrNilDictionary
	}
	size := cfg.QueueSize
	if size <= 0 {
		size = 4096
	}
	enc := NewEncoder(EncoderConfig{
		KeyFrameInterval: cfg.KeyFrameInterval,
		SpecVersion:      cfg.SpecVersion,
	}, dict)
	store := NewStore(db, dict, enc)
	if !store.HasTables() {
		return nil, ErrTablesMissing
	}
	flushInterval := cfg.FlushInterval
	if flushInterval <= 0 {
		flushInterval = dualWriteFlushInterval
	}
	w := &DualWriter{
		store:         store,
		enc:           enc,
		queue:         make(chan dualItem, size),
		stop:          make(chan struct{}),
		onErr:         cfg.OnWriteError,
		onDrop:        cfg.OnDrop,
		flushInterval: flushInterval,
	}
	w.wg.Add(1)
	go w.run()
	logger.Infof("telemetry: dual write enabled (queue=%d, keyframe=%s)", size, enc.cfg.KeyFrameInterval)
	return w, nil
}

// Observe 是挂到写路径旁的 sink（**非阻塞**）。
//
// 参数与 databus 的解析产物一一对应：
//   - deviceID = EdgeDevice.ID（= unified_data.device_id 口径）；
//   - at = 本次写入使用的时间戳（与 unified_data.timestamp **同源**，否则
//     两张表对不上，影子校验会全量误判）；
//   - commandTemplateID = DataEvent.CommandTemplateID（device15 的 7 种帧类型
//     唯一可靠判据，§4.4）；
//   - readings = 解析出的物理量。
func (w *DualWriter) Observe(deviceID uint, at time.Time, commandTemplateID int64, readings []Reading, raw []byte) {
	if w == nil || len(readings) == 0 {
		return
	}
	// readings 由调用方传入，异步 worker 读取时调用方可能已改写底层数组
	// （"现在没复用"不等于"以后不会复用"），这里付一次小拷贝换取安全。
	cp := make([]Reading, len(readings))
	copy(cp, readings)
	// raw 同理必须拷贝：databus 的 merged 来自 reassembler 的内部缓冲，
	// 下一帧 Append 可能就地扩展/覆盖它。不拷贝 ⇒ 落库的 raw 与 payload 对不上，
	// 而那种错**不会报错**，只有在事后核对时才会发现（最坏的一种 bug）。
	var rawCopy []byte
	if len(raw) > 0 {
		rawCopy = make([]byte, len(raw))
		copy(rawCopy, raw)
	}
	select {
	case w.queue <- dualItem{deviceID: deviceID, at: at, commandTemplateID: commandTemplateID, readings: cp, raw: rawCopy}:
	default:
		// 队列满 ⇒ 丢弃本帧，不阻塞。**不重试、不落盘缓冲**：双写是影子，
		// 队列满说明落库已经跟不上采集；攒更多只会让延迟无限增长。
		// 丢弃数在 Stats().Dropped 里可见（影子校验要求它为 0）。
		w.dropped.Add(1)
		if w.onDrop != nil {
			w.onDrop()
		}
	}
}

const dualWriteBatchSize = 128

// dualWriteFlushInterval 是**定时 flush** 的周期。
//
// ==================== 为什么必须有定时器（2026-11-07 P0）====================
//
// 此前 flush 只有两个触发点：攒满 128 帧、或进程退出（Close）。**没有时间驱动**。
// 生产实测后果（帧速率 87.8 帧/分 ⇒ 填满 128 帧要 87 秒）：
//
//	telemetry_frame max(ts) = 34 秒前     ← 帧表滞后
//	unified_data   max(ts) =  1 秒前     ← 主表实时（同步写，无缓冲）
//
// 三条后果，一条比一条严重：
//
//  1. **崩溃丢 ≤127 帧**。非优雅退出时内存里那批永远不落库；而 L1 门控下
//     这些帧可能没有对应的 unified_data 行（值未变）⇒ **永久丢失**。
//     一旦 unified_data 被删（「删 12 GB 老分区」就是本系列的终点），
//     这就是真的数据丢失，不是"另一份副本还在"。
//
//  2. **图表静默少尾**。覆盖判据若只按「5 分钟余量」判，end=now 的窗口会被
//     判成"已覆盖"⇒ 走帧表 ⇒ 末尾凭空少 34~90 秒，**且不回落**。用户看到的是
//     "曲线在这里断了"，而系统认为一切正常。
//
//  3. **帧表不再是可信的实时来源**。"删老 unified_data" 的前提是帧表实时完整；
//     滞后 90 秒意味着最近 90 秒只有 unified_data 有数据 —— 那 12 GB 就删不得。
//
// 2 秒的来由：
//
//	· 帧速率 87.8 帧/分 ⇒ 2 秒约 3 帧，flush 频率与写入量都不高；
//	· 2 秒的滞后在 1 小时窗口的图上小于 1 像素，用户不可见；
//	· 崩溃最坏丢 2 秒的帧（约 3 帧），而不是 87 秒（约 127 帧）。
//
// 为什么不设成 200ms 更保险：每次 flush 是一次 `PersistFrames` 事务，
// 频率提高 10 倍就是 10 倍的 WAL 提交压力，而收益只是把"丢 3 帧"变成"丢 0.3 帧"
// —— 在 unified_data 还在的过渡期毫无意义，在它被删之后 3 帧的缺口也远小于
// 一次心跳间隔。2 秒是这个权衡的拐点。
const dualWriteFlushInterval = 2 * time.Second

// run 是后台 worker：串行编码 + 批量落库。
func (w *DualWriter) run() {
	defer w.wg.Done()
	batch := make([]EncodedFrame, 0, dualWriteBatchSize)

	// 定时 flush：把「攒满 128 帧」的等待上界从「无上界」压到 flushInterval。
	//
	// 为什么放在这里（worker 内）而不是 main.go 再起一个 goroutine：
	//  · flush 会改 batch 与执行 DB 写，必须与 worker **同 goroutine** 才没有竞态；
	//  · 从外面驱动就需要加锁或再开一个 channel，把一个 5 行的修复变成并发问题。
	tick := time.NewTicker(w.flushInterval)
	defer tick.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := w.store.PersistFrames(batch); err != nil {
			// 双写失败**不上报给写路径**：计数 + 日志，继续服务后续帧。
			w.writeFails.Add(int64(len(batch)))
			logger.Warnf("telemetry: dual write persist failed (%d frames): %v", len(batch), err)
			if w.onErr != nil {
				w.onErr(err)
			}
		} else {
			w.encoded.Add(int64(len(batch)))
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-w.stop:
			// 优雅退出：把队列里剩下的排空（有界队列，最多 cap 条）。
			for {
				select {
				case it := <-w.queue:
					w.appendEncoded(&batch, it)
					if len(batch) >= dualWriteBatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case <-tick.C:
			// 到点就落盘，不管攒了多少。这是「帧表滞后」的唯一兜底 ——
			// 没有它，攒不满 128 帧就一直压在内存里（设备少时可达数分钟）。
			flush()
		case it := <-w.queue:
			w.appendEncoded(&batch, it)
			if len(batch) >= dualWriteBatchSize {
				flush()
			}
		}
	}
}

// appendEncoded 编码一条观测并追加到批（编码失败只计数，不影响其余帧）。
func (w *DualWriter) appendEncoded(batch *[]EncodedFrame, it dualItem) {
	frame, err := w.enc.EncodeFrame(it.deviceID, it.at, it.commandTemplateID, it.readings)
	if err != nil {
		// 单帧坏（新字段名非法/字段数不一致）不该拖垮旁路。
		w.writeFails.Add(1)
		if w.onErr != nil {
			w.onErr(err)
		}
		logger.Debugf("telemetry: dual write encode skipped: %v", err)
		return
	}
	// ==================== 关键帧才填 raw（原始数据退役 §4.4 方案 A）====================
	//
	// 判据用 frame.FrameKind（编码器已经算好的那一个），**不要**在这里重新判断
	// 「是不是首帧/距上次多久」—— 那会把关键帧判据变成两处实现，而它们一定会漂移
	// （本仓 §8 已经有过一次同类事故：加定时器前的 flush 判据）。
	//
	// 增量帧显式置 nil（而不是留着 it.raw）：增量帧的 raw 对事后追溯没有独立价值，
	// 而留着的成本是 78~256 B × 92.9% 的帧 ⇒ 本方案的收益被吃掉大半。
	if frame.FrameKind == models.TelemetryFrameKindKey {
		frame.Raw = it.raw
	}
	w.payloadSum.Add(int64(frame.Size()))
	*batch = append(*batch, frame)
}

// Close 停止 worker 并排空队列（幂等）。
func (w *DualWriter) Close() {
	if w == nil {
		return
	}
	w.once.Do(func() {
		close(w.stop)
		w.wg.Wait()
		logger.Infof("telemetry: dual write stopped (encoded=%d dropped=%d failed=%d payload=%dB)",
			w.encoded.Load(), w.dropped.Load(), w.writeFails.Load(), w.payloadSum.Load())
	})
}

// DualWriteStats 是双写计数（验收与指标对账用）。
type DualWriteStats struct {
	// Encoded 已成功落库的帧数。
	Encoded int64
	// Dropped 因队列满丢弃的帧数（影子校验要求 0）。
	Dropped int64
	// Failed 编码或落库失败的帧数（影子校验要求 0）。
	Failed int64
	// PayloadSum 已编码帧的 payload 字节总数（§4.5 对账口径）。
	PayloadSum int64
}

// Stats 返回当前计数快照。
func (w *DualWriter) Stats() DualWriteStats {
	if w == nil {
		return DualWriteStats{}
	}
	return DualWriteStats{
		Encoded:    w.encoded.Load(),
		Dropped:    w.dropped.Load(),
		Failed:     w.writeFails.Load(),
		PayloadSum: w.payloadSum.Load(),
	}
}

// Encoder 返回内部编码器（读取 EncodeStats 用）。
func (w *DualWriter) Encoder() *Encoder {
	if w == nil {
		return nil
	}
	return w.enc
}

// Sink 返回可直接注入消费者（databus）的 sink 函数；w 为 nil 时返回 nil。
//
// 这个方法是"默认关闭"落地方式的关键：接线方写成
//
//	consumer.SetTelemetrySink(dual.Sink())   // dual 为 nil 时得到 nil，零成本
//
// 而不需要到处判空。
func (w *DualWriter) Sink() Sink {
	if w == nil {
		return nil
	}
	return w.Observe
}
