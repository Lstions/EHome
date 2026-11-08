package telemetry

import (
	"fmt"
	"math"
	"sync"
	"time"
)

// Reading 是一帧里的一个读数（物理名 + 物理量）。
//
// 刻意不 import pkg/parser / internal/ingest：本包是**编码核心**，不应耦合
// 任何解析层的类型。写路径做一次零成本转换（Name/Value 两个字段）。
type Reading struct {
	Name  string
	Value float64
}

// EncodeStats 是一次编码的可观测统计（供双写旁路的指标与验收对拍）。
type EncodeStats struct {
	// Frames 编码帧数。
	Frames int64
	// KeyFrames 关键帧数（首帧 + 心跳关键帧 + 链断裂后重建）。
	KeyFrames int64
	// PayloadBytes payload 总字节数。
	PayloadBytes int64
	// ChangedValues 变化的字段总数。
	ChangedValues int64
	// ClampedValues 量化时被**夹到量程边界**的字段数。
	// 非 0 意味着真实精度损失（误差 > Step/2）⇒ 验收要求为 0，否则该扩量程/位宽。
	ClampedValues int64
	// InvalidValues 本帧缺失（NaN/Inf）的字段数。
	InvalidValues int64
	// ChainBreaks 因长时间无帧而强制重发关键帧的次数（§8 风险表 60 s 兜底）。
	ChainBreaks int64
}

// EncoderConfig 是编码器参数。
type EncoderConfig struct {
	// KeyFrameInterval 是**强制关键帧**的最大间隔：距上次编码该 slot 超过这个
	// 时长就发关键帧（方案 §8「60 s 关键帧限制最坏窗口」）。
	//
	// 为什么必须有：增量链一旦断（丢一帧、进程重启、slot 版本切换），后续所有帧
	// 都会解错位置。定期关键帧把"最坏错位窗口"限制在一个区间内，而不是永久。
	// 0 表示用默认值 60s。
	KeyFrameInterval time.Duration
	// SpecVersion 量化规格版本（0 视为 SpecVersion）。
	SpecVersion uint16
}

// 默认参数（与方案 §3.3 / §8 一致）。
const (
	// DefaultKeyFrameInterval 是默认强制关键帧间隔 = 60s（方案 §3.3 心跳同源口径）。
	DefaultKeyFrameInterval = 60 * time.Second
)

// normalize 填默认值。
func (c EncoderConfig) normalize() EncoderConfig {
	if c.KeyFrameInterval <= 0 {
		c.KeyFrameInterval = DefaultKeyFrameInterval
	}
	if c.SpecVersion == 0 {
		c.SpecVersion = SpecVersion
	}
	return c
}

// EncodedFrame 是一次编码的产物：可直接写进 telemetry_frame 的一行。
type EncodedFrame struct {
	DeviceID  uint
	TS        time.Time
	Seq       int16
	FrameKind int16
	SlotID    int16
	Payload   []byte
}

// Size 返回该行的 payload 字节数（验收口径：§4.5 的"平均 payload"就是它的均值）。
func (f EncodedFrame) Size() int { return len(f.Payload) }

// slotState 是一个 (设备, slot) 的增量链状态。
type slotState struct {
	lastCodes  []uint64
	lastValid  []bool
	lastAt     time.Time
	hasState   bool
	lastSlotID int16
}

// Encoder 是有状态增量编码器（并发安全）。
//
// 状态按 (deviceID, slotSignature) 维护：同一设备的不同帧类型各有独立的
// 增量链（device15 的 7 种帧类型如果共享一条链，会把不同帧的码点当成
// "上一帧"来比较 ⇒ 几乎每帧都全变化，压缩率归零）。
type Encoder struct {
	cfg  EncoderConfig
	dict SlotDictionary

	mu     sync.Mutex
	states map[stateKey]*slotState
	seqs   map[seqKey]int16

	stats EncodeStats
}

type stateKey struct {
	deviceID  uint
	signature string
}

type seqKey struct {
	deviceID uint
	ts       int64 // UnixNano
}

// NewEncoder 构造编码器。
func NewEncoder(cfg EncoderConfig, dict SlotDictionary) *Encoder {
	return &Encoder{
		cfg:    cfg.normalize(),
		dict:   dict,
		states: make(map[stateKey]*slotState),
		seqs:   make(map[seqKey]int16),
	}
}

// SpecVersion 返回编码器使用的规格版本。
func (e *Encoder) SpecVersion() uint16 { return e.cfg.SpecVersion }

// Stats 返回当前统计快照。
func (e *Encoder) Stats() EncodeStats {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stats
}

// ResetStats 清零统计（验收分段统计用）。
func (e *Encoder) ResetStats() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stats = EncodeStats{}
}

// Reset 清空全部增量链状态（进程重启/链路排障/测试用）。
//
// 清空后下一帧对每个 slot 都是关键帧 —— 这正是"进程重启首帧全量写一遍"的语义
// （方案 §3.3 配套项）。
func (e *Encoder) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.states = make(map[stateKey]*slotState)
	e.seqs = make(map[seqKey]int16)
}

// EncodeFrame 编码一帧。
//
// commandTemplateID 参与 slot 派生（§4.4）：它是区分 device15 七种帧类型的
// 唯一可靠信号，**不能**省略或置 0（置 0 会让 7 种帧塌成一个 slot）。
//
// 返回的 EncodedFrame 已带 DeviceID/TS/Seq/FrameKind/SlotID/Payload，可直接落库。
func (e *Encoder) EncodeFrame(deviceID uint, at time.Time, commandTemplateID int64, readings []Reading) (EncodedFrame, error) {
	if len(readings) == 0 {
		return EncodedFrame{}, ErrNoFields
	}
	names := make([]string, len(readings))
	for i, r := range readings {
		names[i] = r.Name
	}
	slot, err := e.dict.Resolve(commandTemplateID, names)
	if err != nil {
		return EncodedFrame{}, err
	}
	if slot.NumFields() != len(readings) {
		return EncodedFrame{}, fmt.Errorf("telemetry: slot %d field count %d != reading count %d",
			slot.ID, slot.NumFields(), len(readings))
	}

	specs := slot.Specs()
	idx := slot.indexOf()
	n := slot.NumFields()

	// 把 readings 按 slot 的（字典序）顺序重排 —— 位流顺序必须与 slot 一致，
	// 否则解码端按 slot 顺序读出来的就是别的字段的值（"写串"）。
	codes := make([]uint64, n)
	valid := make([]bool, n)
	// clamped 先在**锁外**累计到局部变量，随后在锁内并入统计 ——
	// 编码热路径上不能为了计数去抢锁（quantize 在 e.mu 之前完成）。
	clamped := 0
	for _, r := range readings {
		i, ok := idx[r.Name]
		if !ok {
			return EncodedFrame{}, fmt.Errorf("telemetry: reading %q not in slot %d", r.Name, slot.ID)
		}
		if math.IsNaN(r.Value) || math.IsInf(r.Value, 0) {
			valid[i] = false
			continue
		}
		code, wasClamped := specs[i].Quantize(r.Value)
		codes[i] = code
		valid[i] = true
		if wasClamped {
			clamped++
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	key := stateKey{deviceID: deviceID, signature: slot.Signature}
	st := e.states[key]

	// 关键帧判定（三条，任一成立）：
	//  1. 没有状态（首帧 / 进程重启）；
	//  2. 距上次编码该 slot 超过 KeyFrameInterval（心跳兜底，限制最坏错位窗口）；
	//  3. slot 版本变了（规格快照不同 ⇒ 旧码点不能作为增量基线）。
	keyFrame := st == nil || !st.hasState ||
		at.Sub(st.lastAt) > e.cfg.KeyFrameInterval ||
		st.lastSlotID != slot.ID
	chainBreak := st != nil && st.hasState && at.Sub(st.lastAt) > e.cfg.KeyFrameInterval

	changed := make([]bool, n)
	invalid := make([]bool, n)
	for i := 0; i < n; i++ {
		if !valid[i] {
			invalid[i] = true
			continue
		}
		if keyFrame || st.lastCodes[i] != codes[i] {
			changed[i] = true
		}
	}

	fd := &FrameData{
		KeyFrame: keyFrame,
		Changed:  changed,
		Invalid:  invalid,
		Codes:    codes,
	}
	payload, err := EncodeFrame(fd, specs)
	if err != nil {
		return EncodedFrame{}, err
	}

	// 更新链状态：只有**本帧带值**的字段才能作为下一帧的基线。
	if st == nil {
		st = &slotState{}
		e.states[key] = st
	}
	st.lastCodes = codes
	st.lastValid = valid
	st.lastAt = at
	st.hasState = true
	st.lastSlotID = slot.ID

	// seq：同一 (device, ts) 内的自增序号。写路径的时间戳来自 time.Now()，
	// 纳秒精度下同值几乎不可能，但主键冲突会**覆盖**前一帧（静默丢数据），
	// 所以宁可多一个 map 也要保证唯一（§8 风险表「秒内多帧」）。
	sk := seqKey{deviceID: deviceID, ts: at.UnixNano()}
	seq := e.seqs[sk]
	e.seqs[sk] = seq + 1

	frameKind := int16(0)
	if keyFrame {
		frameKind = 1
	}
	e.stats.Frames++
	e.stats.ClampedValues += int64(clamped)
	e.stats.PayloadBytes += int64(len(payload))
	e.stats.ChangedValues += int64(fd.ChangedCount())
	e.stats.InvalidValues += int64(countTrue(invalid))
	if keyFrame {
		e.stats.KeyFrames++
	}
	if chainBreak {
		e.stats.ChainBreaks++
	}

	return EncodedFrame{
		DeviceID:  deviceID,
		TS:        at,
		Seq:       seq,
		FrameKind: frameKind,
		SlotID:    slot.ID,
		Payload:   payload,
	}, nil
}

func countTrue(b []bool) int {
	n := 0
	for _, x := range b {
		if x {
			n++
		}
	}
	return n
}
