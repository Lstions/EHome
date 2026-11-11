package telemetry

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"ehome/backend/internal/models"
)

// TableFrame / TableSlot 是两张表的逻辑表名。
//
// telemetry_frame 的**生产物理表**由 datalifecycle.partition_mgr 按
// partitionParentDDL 建（月分区、主键 (device_id, ts, seq)），本包只读写，
// 不建表 —— 依赖方向是 telemetry → models，绝不反过来。
const (
	TableFrame = "telemetry_frame"
	TableSlot  = "telemetry_slot"
)

// 读取窗口的两条护栏（§5.1 实测读成本表）：
//
//	24 小时 device14 = 86,342 帧 / 1.0 MB 压缩 / 解码后 19 MB 内存
//	30 天   device14 = 2.6 M 帧 / 37 MB 压缩 / 解码后 570 MB ⚠
//
// 即「解码只在短窗内划算」。这两个常量是**硬上限**而不是建议值：
// 超出就直接报错让调用方去走 rollup，而不是让某个图表请求把 570 MB 拉进内存
// 把进程打爆（§5.1 的 ⚠ 就是这条）。
const (
	// MaxDecodeFrames 是**一次性解码**（DecodeRange / DecodeFrames 单批）的帧数上限
	// （≈ 6 小时 1 Hz）。分页解码（DecodeRangeAnchored 的生产路径）不再受它限制，
	// 由下面的 maxScanFrames 兜底 —— 32768 墙正是「锚点被截断丢掉」的根因。
	MaxDecodeFrames = 32768
	// MaxDecodeWindow 是单次 DecodeRange 允许的时间窗上限。
	MaxDecodeWindow = 24 * time.Hour
	// LatestSnapshotLookback 是 LatestSnapshot 无关键帧时的回退窗口。
	LatestSnapshotLookback = 24 * time.Hour

	// MaxScanFrames 是分页解码的**扫描帧数**硬上限：从锚点起最多扫这么多帧。
	//
	// 为什么是 8 × MaxDecodeFrames = 262144 帧：分页解码去掉了「按请求窗口截断」
	// 的墙（保留锚点），但仍需要防「锚点远古 + 大窗口」把无界帧拉进内存/CPU。
	// 262144 帧 @1 Hz ≈ 72.8 小时，覆盖生产最坏形态（37 小时历史无关键帧 + 6 小时
	// 请求窗口 ≈ 43 小时）仍有 ~1.7 倍余量；10 fps 设备也只多扫 7.3 小时。
	// 超限**不静默**：剩余未解码帧数记进 DecodeStats.FramesTruncated，调用方据此报错/回落。
	//
	// ============ 为什么导出（2026-11-11）============
	//
	// rollup 回填（cmd/server/main.go 的 SetFrameDecoder）需要它。此前那里显式传了
	// MaxDecodeFrames（32768），把本默认值覆盖掉了 —— 而 32768 是**一次性解码**
	// （DecodeRange 单批）的上限，语义不是「扫描预算」。
	//
	// 后果（生产实测 2026-11-11）：device14 在 2026-11-08 15:57 之后到 11-10 05:21
	// 之间没有关键帧，回填 11-10 的每一段都要从 32 小时前的锚点预热 ≈ 11.5 万帧，
	// 超过 32768 ⇒ FramesTruncated>0 ⇒ 该单元标 partial ⇒ 读路径判定「覆盖未验证」
	// ⇒ 24h/7d/30d 图表全部报「超过帧表单次解码上限」⇒ **用户可见的图表坏掉**。
	// 导出它，让调用方按语义选对常量（扫描预算 vs 单批上限）。
	MaxScanFrames = 8 * MaxDecodeFrames
	// maxScanFrames 是包内别名，保持既有内部调用点不变。
	maxScanFrames = MaxScanFrames
	// decodeScanPageSize 是分页解码每页取的帧数（≤ maxScanFrames）。
	// 8192 帧 @1 Hz ≈ 2.3 小时/页；一页的解码工作 ≈ 几十毫秒，避免单页过大。
	decodeScanPageSize = 8192
)

// RangeTooLargeError 表示请求超出解码护栏。
type RangeTooLargeError struct {
	Frames int64
	Window time.Duration
}

func (e *RangeTooLargeError) Error() string {
	return fmt.Sprintf("telemetry: decode range too large (frames=%d limit=%d, window=%s limit=%s); use rollup for long ranges",
		e.Frames, MaxDecodeFrames, e.Window, MaxDecodeWindow)
}

// Store 是 telemetry_frame 的读写入口（db + slot 字典）。
//
// 为什么要包一层而不是散装函数：slot 字典必须由**调用方注入**（本包不得
// import sensorfield/database/api），而解码又必须用同一个字典 —— 把两者绑在
// 一个结构里，就不可能出现"写用 A 字典、读用 B 字典"的静默错解。
type Store struct {
	db   *gorm.DB
	dict SlotDictionary
	enc  *Encoder
}

// NewStore 构造读写入口。
//
// enc 可为 nil（只读场景）：此时 EncodeAndPersist 返回错误而 DecodeRange 正常。
func NewStore(db *gorm.DB, dict SlotDictionary, enc *Encoder) *Store {
	return &Store{db: db, dict: dict, enc: enc}
}

// Dictionary 返回使用的 slot 字典。
func (s *Store) Dictionary() SlotDictionary { return s.dict }

// Encoder 返回编码器（可为 nil）。
func (s *Store) Encoder() *Encoder { return s.enc }

// HasTables 报告两张表是否都存在。
//
// 语义与 DB 侧分区母表一致：**表不存在 = 本旁路未启用**，返回 false 而不是
// 报错。双写开关打开但迁移没跑过的库上，这会让双写安静地退场，
// 而不是每次写入都刷一条 "relation does not exist"。
func (s *Store) HasTables() bool {
	if s.db == nil {
		return false
	}
	return s.db.Migrator().HasTable(TableFrame) && s.db.Migrator().HasTable(TableSlot)
}

// PersistFrames 批量写入帧（幂等：主键冲突跳过）。
//
// 用 ON CONFLICT DO NOTHING 而不是普通 INSERT：主键 (device_id, ts, seq)
// 在**重放**场景下会重复（影子校验重跑同一批数据、进程重启后重发首帧）。
// 幂等让重放安全，也让"双写期间旧表与新表不一致"可以靠重放修复。
func (s *Store) PersistFrames(frames []EncodedFrame) error {
	if len(frames) == 0 {
		return nil
	}
	if s.db == nil {
		return fmt.Errorf("telemetry: nil db")
	}
	rows := make([]models.TelemetryFrame, len(frames))
	for i := range frames {
		rows[i] = models.TelemetryFrame{
			DeviceID:  frames[i].DeviceID,
			TS:        frames[i].TS,
			Seq:       frames[i].Seq,
			FrameKind: frames[i].FrameKind,
			SlotID:    frames[i].SlotID,
			Payload:   frames[i].Payload,
			// Raw 只在关键帧上非 nil（写路径在 DualWriter.appendEncoded 里填）。
			// 增量帧为 nil ⇒ 落 NULL，与「常规增量帧留 NULL」的设计口径一致。
			Raw: frames[i].Raw,
		}
	}
	return s.db.Session(&gorm.Session{}).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&rows).Error
}

// EncodeAndPersist 编码并落库一帧（双写旁路的主入口）。
func (s *Store) EncodeAndPersist(deviceID uint, at time.Time, commandTemplateID int64, readings []Reading) (EncodedFrame, error) {
	if s.enc == nil {
		return EncodedFrame{}, fmt.Errorf("telemetry: store has no encoder")
	}
	frame, err := s.enc.EncodeFrame(deviceID, at, commandTemplateID, readings)
	if err != nil {
		return EncodedFrame{}, err
	}
	if err := s.PersistFrames([]EncodedFrame{frame}); err != nil {
		return frame, err
	}
	return frame, nil
}

// frameCursor 是 (ts, seq) keyset 分页游标（上一页最后一帧的键）。
type frameCursor struct {
	ts  time.Time
	seq int16
}

// queryFramesPage 按 (ts, seq) keyset 分页取帧（升序，[from, to) 左闭右开）。
//
// 为什么用 keyset 而不是 LIMIT/OFFSET：偏移分页在深翻页时要把前面所有行再扫一遍
// （O(n²)），而读路径一次可能要翻 30+ 页（262144 帧 / 8192）。keyset 每页都走
// 主键索引 (device_id, ts, seq) 的区间扫描，代价与页码无关。
//
// after == nil 表示第一页（等价于 ts >= from）；否则只取 (ts, seq) **严格大于**
// 游标的帧 —— seq 参与比较，保证同一时间戳的多帧不会在页边界重复/丢帧。
func (s *Store) queryFramesPage(deviceID uint, from, to time.Time, after *frameCursor, limit int) ([]models.TelemetryFrame, error) {
	if s.db == nil {
		return nil, fmt.Errorf("telemetry: nil db")
	}
	if limit <= 0 {
		return nil, nil
	}
	q := s.db.Session(&gorm.Session{}).
		Where("device_id = ? AND ts >= ? AND ts < ?", deviceID, from, to)
	if after != nil {
		q = q.Where("(ts > ? OR (ts = ? AND seq > ?))", after.ts, after.ts, after.seq)
	}
	q = q.Order("ts ASC, seq ASC").Limit(limit)
	var frames []models.TelemetryFrame
	if err := q.Find(&frames).Error; err != nil {
		return nil, fmt.Errorf("telemetry: query frame page for device %d [%s,%s): %w",
			deviceID, from.Format(time.RFC3339), to.Format(time.RFC3339), err)
	}
	return frames, nil
}

// countFramesAfter 统计 [from, to) 内 (ts, seq) 严格大于游标的帧数。
//
// 只服务于「扫描预算耗尽」这一个分支（把未解码的剩余帧数如实记进
// FramesTruncated），正常路径零额外查询。
func (s *Store) countFramesAfter(deviceID uint, from, to time.Time, after *frameCursor) (int64, error) {
	if s.db == nil {
		return 0, fmt.Errorf("telemetry: nil db")
	}
	q := s.db.Session(&gorm.Session{}).
		Model(&models.TelemetryFrame{}).
		Where("device_id = ? AND ts >= ? AND ts < ?", deviceID, from, to)
	if after != nil {
		q = q.Where("(ts > ? OR (ts = ? AND seq > ?))", after.ts, after.ts, after.seq)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return 0, fmt.Errorf("telemetry: count remaining frames for device %d [%s,%s): %w",
			deviceID, from.Format(time.RFC3339), to.Format(time.RFC3339), err)
	}
	return n, nil
}

// queryFrames 按时间窗取帧（升序）。
func (s *Store) queryFrames(deviceID uint, from, to time.Time, limit int) ([]models.TelemetryFrame, error) {
	if s.db == nil {
		return nil, fmt.Errorf("telemetry: nil db")
	}
	q := s.db.Session(&gorm.Session{}).
		Where("device_id = ? AND ts >= ? AND ts < ?", deviceID, from, to).
		Order("ts ASC, seq ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var frames []models.TelemetryFrame
	if err := q.Find(&frames).Error; err != nil {
		return nil, fmt.Errorf("telemetry: query frames for device %d [%s,%s): %w",
			deviceID, from.Format(time.RFC3339), to.Format(time.RFC3339), err)
	}
	return frames, nil
}

// DecodeRange 按时间窗取帧并按 slot 顺序累积状态，产出任意字段序列（§5.1）。
//
// # 与方案 §5.1 签名的差异（只有一处，必须说明）
//
// 方案写的是 DecodeRange(ctx, db, deviceID, from, to, fields)。这里多一个
// dict 参数：slot 字典是**注入**的（本包不 import sensorfield/api/database，
// 数据面零依赖红线）。其余参数顺序与语义完全一致。
//
// # 护栏
//
// 窗口 > MaxDecodeWindow 直接拒绝；帧数 > MaxDecodeFrames 时**保留最新的**
// 那部分并继续（返回 stats 让调用方知道被截断了）。为什么不直接拒绝帧数超限：
// 调用方（图表）已经按窗查询，截断比报错更能保证"出图"而不是"白屏"，
// 而 stats.FramesSeen 能反映截断 —— 静默截断才是问题，可观测截断不是。
func DecodeRange(ctx context.Context, db *gorm.DB, dict SlotDictionary, deviceID uint, from, to time.Time, fields []string) ([]Point, DecodeStats, error) {
	if err := ctx.Err(); err != nil {
		return nil, DecodeStats{}, err
	}
	if !to.After(from) {
		return nil, DecodeStats{}, fmt.Errorf("telemetry: empty range [%s, %s)", from, to)
	}
	if to.Sub(from) > MaxDecodeWindow {
		return nil, DecodeStats{}, &RangeTooLargeError{Window: to.Sub(from)}
	}
	st := &Store{db: db, dict: dict}
	frames, err := st.queryFrames(deviceID, from, to, MaxDecodeFrames+1)
	if err != nil {
		return nil, DecodeStats{}, err
	}
	truncated := 0
	if len(frames) > MaxDecodeFrames {
		truncated = len(frames) - MaxDecodeFrames
		frames = frames[len(frames)-MaxDecodeFrames:]
	}
	points, stats, err := DecodeFrames(frames, dict, DecodeOptions{Fields: fields})
	if err != nil {
		return nil, stats, err
	}
	stats.FramesSkippedBrokenChain += int64(truncated) // 被截断的帧计入"未解码"
	return points, stats, nil
}

// LatestSnapshot 取最近一个关键帧 + 其后增量，得到当前全量快照（§5.1）。
//
// # 为什么必须从关键帧起解（不能只取最后 N 帧）
//
// 增量帧的语义是"相对上一帧的变化"，最后 N 帧里若没有关键帧，解出的值只覆盖
// "最近 N 帧内变化过的字段"，其余字段**没有值但不是缺失** —— 直接返回会让
// /latest-data 少一半字段，而调用方无从分辨"这设备没这个字段"和"我没解出来"。
//
// 因此：找到该设备最后一个关键帧，从它开始解；找不到（历史数据没有关键帧 /
// 保留期把关键帧删了）就回退到 LatestSnapshotLookback 窗口内最早的帧开始解，
// 并在 stats.MissingBaseline 里记账。
func LatestSnapshot(ctx context.Context, db *gorm.DB, dict SlotDictionary, deviceID uint) (map[string]float64, DecodeStats, error) {
	if err := ctx.Err(); err != nil {
		return nil, DecodeStats{}, err
	}
	if db == nil {
		return nil, DecodeStats{}, fmt.Errorf("telemetry: nil db")
	}
	st := &Store{db: db, dict: dict}

	var key models.TelemetryFrame
	keyErr := db.Session(&gorm.Session{}).
		Where("device_id = ? AND frame_kind = ?", deviceID, models.TelemetryFrameKindKey).
		Order("ts DESC, seq DESC").
		First(&key).Error

	from := time.Now().Add(-LatestSnapshotLookback)
	if keyErr == nil {
		from = key.TS
	} else if keyErr != gorm.ErrRecordNotFound {
		return nil, DecodeStats{}, fmt.Errorf("telemetry: find latest key frame for device %d: %w", deviceID, keyErr)
	}

	frames, err := st.queryFrames(deviceID, from, time.Now().Add(time.Minute), MaxDecodeFrames)
	if err != nil {
		return nil, DecodeStats{}, err
	}
	snapshot, stats, err := SnapshotFromFrames(frames, dict)
	if err != nil {
		return nil, stats, err
	}
	if keyErr != nil && len(frames) == 0 {
		// 完全没有帧：返回空快照而不是错误 —— "这台设备没有遥测数据"是合法状态
		// （新建设备 / 从未上报），调用方按空 map 处理即可。
		return map[string]float64{}, stats, nil
	}
	return snapshot, stats, nil
}

// TableStats 是 telemetry_frame 的一行体积统计（验收用）。
type TableStats struct {
	// Frames 帧数。
	Frames int64
	// PayloadBytes payload 总字节数。
	PayloadBytes int64
	// KeyFrames 关键帧数。
	KeyFrames int64
}

// AvgPayload 返回平均 payload 字节数（§4.5 的对账口径）。
func (t TableStats) AvgPayload() float64 {
	if t.Frames == 0 {
		return 0
	}
	return float64(t.PayloadBytes) / float64(t.Frames)
}

// StatsForDevice 统计某设备的帧数与 payload 体积。
func StatsForDevice(db *gorm.DB, deviceID uint) (TableStats, error) {
	var out TableStats
	if db == nil {
		return out, fmt.Errorf("telemetry: nil db")
	}
	type row struct {
		Frames       int64
		PayloadBytes int64
		KeyFrames    int64
	}
	var r row
	if err := db.Session(&gorm.Session{}).
		Model(&models.TelemetryFrame{}).
		Select("count(*) AS frames, coalesce(sum(length(payload)),0) AS payload_bytes, "+
			"coalesce(sum(case when frame_kind = 1 then 1 else 0 end),0) AS key_frames").
		Where("device_id = ?", deviceID).
		Scan(&r).Error; err != nil {
		return out, fmt.Errorf("telemetry: stats for device %d: %w", deviceID, err)
	}
	out.Frames = r.Frames
	out.PayloadBytes = r.PayloadBytes
	out.KeyFrames = r.KeyFrames
	return out, nil
}
