package telemetry

import (
	"errors"
	"fmt"
)

// payload 编码格式（方案 §4.2）：
//
//	+--------+--------------------+----------------------------+
//	| 1B     | ceil(N/8) B        | 剩余                       |
//	| flags  | changed_mask       | 变化字段的量化值（按位打包）|
//	+--------+--------------------+----------------------------+
//
// flags:
//
//	bit0 = 关键帧（该 slot 的全部**有效**字段都带值，解码器可从本帧重建全量状态）
//	bit1 = 附加 invalid_mask（紧随 changed_mask 之后 ceil(N/8) B）
//	bit2..7 保留，必须为 0（读到非 0 视为格式错误 —— 宁可拒解也不要错解）
//
// # 为什么要有 invalid_mask（对 §4.2 的唯一扩展）
//
// §4.2 只定义了 changed_mask。真实数据上"字段缺失"（NaN / 解析出来是空）
// 不是我们能假设不存在的情况：驱动升级、部分解析、位序错位都可能产出 NaN。
// 若把 NaN 塞进量化，Round(NaN) 是实现相关的码点 —— 那是**静默错值**。
// 因此：
//   - 正常帧（无 NaN）flags.bit1 = 0，**字节布局与 §4.2 完全相同**，零额外开销；
//   - 有 NaN 时多一段掩码，该字段既不进 changed_mask 也不占位宽。
//
// 解码器对这类的处置：该字段本帧无值（快照里不出现 / 序列里跳过），
// 而不是塞一个假的 0 进去。
const (
	// FlagKeyFrame 标记关键帧。
	FlagKeyFrame byte = 1 << 0
	// FlagHasInvalid 标记本帧附带 invalid_mask。
	FlagHasInvalid byte = 1 << 1
	// flagsReservedMask 是必须为 0 的保留位。
	flagsReservedMask byte = 0xFC
)

// 编解码错误。全部**不包装底层原因**（这里是纯内存逻辑，没有底层）—— 调用方
// 只需要区分"这帧坏了"与"这帧是本版本不支持的格式"，不需要错误链。
var (
	// ErrEmptyPayload 表示 payload 为空（连 flags 都没有）。
	ErrEmptyPayload = errors.New("telemetry: empty payload")
	// ErrPayloadTruncated 表示 payload 比掩码/位流声明的长度短。
	ErrPayloadTruncated = errors.New("telemetry: payload truncated")
	// ErrPayloadTrailing 表示位流读完后还有多余字节（多半是 slot 字段集合与写入方不一致）。
	ErrPayloadTrailing = errors.New("telemetry: payload has trailing bytes")
	// ErrReservedFlags 表示使用了保留 flag 位。
	ErrReservedFlags = errors.New("telemetry: reserved flag bits set")
	// ErrInvalidMaskChanged 表示同一字段既标 changed 又标 invalid。
	ErrInvalidMaskChanged = errors.New("telemetry: field marked both changed and invalid")
	// ErrTooManyFields 表示字段数超出 slot 表能表达的上限。
	ErrTooManyFields = errors.New("telemetry: too many fields in slot")
	// ErrNoFields 表示 slot 为空。
	ErrNoFields = errors.New("telemetry: slot has no fields")
)

// MaxSlotFields 是单 slot 字段数上限。
//
// 16 位而不是 32 位：slot 表里 field_count 是 SMALLINT（§4.1 邻近设计），
// 且实测最大帧是 29 字段（device14）。把上限钉在合理值上，坏数据会被
// **显式拒绝**而不是让 mask 长度算出一个巨大的值。
const MaxSlotFields = 4096

// FrameData 是一帧的**编码中间形态**：掩码 + 码点。
//
// 它与物理量解耦：编码器负责 float64 → 码点（量化），解码器负责码点 → float64，
// 本结构只承载"哪些字段变了 / 哪些缺失 / 变成什么码"。
type FrameData struct {
	// KeyFrame 表示关键帧：所有有效字段都带值。
	KeyFrame bool
	// Changed 长度 = 字段数；true 表示本帧该字段带新码点（码点写进位流）。
	Changed []bool
	// Invalid 长度 = 字段数；true 表示本帧该字段缺失（不存在）。仅在有缺失时非 nil。
	Invalid []bool
	// Codes 长度 = 字段数；Changed[i] 为 true 时 Codes[i] 有效。
	Codes []uint64
}

// NumFields 返回本帧字段数（按 Changed 长度，Changed 为 nil 时返回 0）。
func (f *FrameData) NumFields() int {
	if f == nil {
		return 0
	}
	return len(f.Changed)
}

// ChangedCount 返回变化字段数。
func (f *FrameData) ChangedCount() int {
	n := 0
	for _, c := range f.Changed {
		if c {
			n++
		}
	}
	return n
}

// HasInvalid 返回是否存在缺失字段。
func (f *FrameData) HasInvalid() bool {
	for _, x := range f.Invalid {
		if x {
			return true
		}
	}
	return false
}

// FieldInvalid 报告第 i 个字段本帧是否缺失。
//
// 为什么要有这个方法而不是直接下标 Invalid[i]：Invalid 在**无缺失时是 nil**
// （编码器不为"零缺失"付一段全 0 的掩码），直接下标会 panic。所有调用方
// （decode / 快照 / 测试）一律经本方法，就不会漏判。
func (f *FrameData) FieldInvalid(i int) bool {
	if f == nil || i < 0 || i >= len(f.Invalid) {
		return false
	}
	return f.Invalid[i]
}

// maskLen 返回 ceil(n/8)。
func maskLen(n int) int { return (n + 7) / 8 }

// bitsLen 返回按 specs 计、changed 字段所占位宽和。
func bitsLen(changed []bool, specs []FieldSpec) int {
	total := 0
	for i, c := range changed {
		if c {
			total += int(specs[i].Bits)
		}
	}
	return total
}

// PayloadSize 返回编码后 payload 的字节数（不实际编码）。
//
// 用途：写入前估算、验收统计（"实测平均 payload"就是它的均值），
// 以及嵌入式侧预估带宽。公式与 EncodeFrame 必须一致 —— 单测里有一条
// 随机化对拍（TestPayloadSizeMatchesEncode），防止两处各算一套。
func PayloadSize(f *FrameData, specs []FieldSpec) int {
	if f == nil || len(f.Changed) == 0 {
		return 0
	}
	n := len(f.Changed)
	size := 1 + maskLen(n)
	if f.HasInvalid() {
		size += maskLen(n)
	}
	size += (bitsLen(f.Changed, specs) + 7) / 8
	return size
}

// EncodeFrame 把 FrameData 编码成 payload。
//
// 布局严格按 §4.2；位打包 **LSB-first**：第 i 个变化字段的位宽 bits[i]，
// 从低位到高位填入连续位流，无对齐填充（方案实测比按字节切分省 5–8%）。
func EncodeFrame(f *FrameData, specs []FieldSpec) ([]byte, error) {
	if f == nil {
		return nil, ErrEmptyPayload
	}
	n := len(f.Changed)
	if n == 0 {
		return nil, ErrNoFields
	}
	if n > MaxSlotFields {
		return nil, fmt.Errorf("%w: %d > %d", ErrTooManyFields, n, MaxSlotFields)
	}
	if len(specs) != n {
		return nil, fmt.Errorf("telemetry: spec count %d != field count %d", len(specs), n)
	}
	if len(f.Codes) != n {
		return nil, fmt.Errorf("telemetry: code count %d != field count %d", len(f.Codes), n)
	}
	if len(f.Invalid) != 0 && len(f.Invalid) != n {
		return nil, fmt.Errorf("telemetry: invalid mask length %d != field count %d", len(f.Invalid), n)
	}

	hasInvalid := f.HasInvalid()
	flags := byte(0)
	if f.KeyFrame {
		flags |= FlagKeyFrame
	}
	if hasInvalid {
		flags |= FlagHasInvalid
	}

	out := make([]byte, 0, PayloadSize(f, specs))
	out = append(out, flags)

	// changed_mask：第 i 位 = 1 表示字段 i 本帧带新值。LSB-first（bit0 = 字段 0）。
	changedMask := make([]byte, maskLen(n))
	for i := 0; i < n; i++ {
		if f.Changed[i] {
			if len(f.Invalid) != 0 && f.Invalid[i] {
				return nil, fmt.Errorf("%w: field index %d", ErrInvalidMaskChanged, i)
			}
			changedMask[i/8] |= 1 << uint(i%8)
		}
	}
	out = append(out, changedMask...)

	if hasInvalid {
		invalidMask := make([]byte, maskLen(n))
		for i := 0; i < n; i++ {
			if f.Invalid[i] {
				invalidMask[i/8] |= 1 << uint(i%8)
			}
		}
		out = append(out, invalidMask...)
	}

	// 位流：只在 changed 字段上按 slot 顺序写码点。
	var w bitWriter
	w.reset()
	for i, c := range f.Changed {
		if !c {
			continue
		}
		bits := specs[i].Bits
		if bits == 0 || bits > 62 {
			return nil, fmt.Errorf("telemetry: field %d has invalid bit width %d", i, bits)
		}
		if f.Codes[i] > specs[i].MaxCode() {
			// 码点越界 = 编码器上游 bug（量化后不可能超过 MaxCode）。
			// 截断会静默产生错值，必须报错。
			return nil, fmt.Errorf("telemetry: code %d exceeds %d bits for field %d", f.Codes[i], bits, i)
		}
		w.writeBits(f.Codes[i], bits)
	}
	out = append(out, w.flush()...)
	return out, nil
}

// DecodeFrame 解析 payload，得到掩码与码点。
//
// specs 提供字段数与每字段位宽 —— 解码器**不需要**知道物理量语义，
// 量化/反量化在 QuantizedValues 里完成，这样格式层可以独立测试。
func DecodeFrame(payload []byte, specs []FieldSpec) (*FrameData, error) {
	n := len(specs)
	if n == 0 {
		return nil, ErrNoFields
	}
	if n > MaxSlotFields {
		return nil, fmt.Errorf("%w: %d > %d", ErrTooManyFields, n, MaxSlotFields)
	}
	if len(payload) == 0 {
		return nil, ErrEmptyPayload
	}
	flags := payload[0]
	if flags&flagsReservedMask != 0 {
		return nil, fmt.Errorf("%w: 0x%02x", ErrReservedFlags, flags)
	}
	off := 1
	mLen := maskLen(n)
	if len(payload) < off+mLen {
		return nil, fmt.Errorf("%w: need %d bytes for changed_mask, have %d", ErrPayloadTruncated, mLen, len(payload)-off)
	}
	changedMask := payload[off : off+mLen]
	off += mLen

	f := &FrameData{
		KeyFrame: flags&FlagKeyFrame != 0,
		Changed:  make([]bool, n),
		Codes:    make([]uint64, n),
	}
	for i := 0; i < n; i++ {
		f.Changed[i] = changedMask[i/8]&(1<<uint(i%8)) != 0
	}

	if flags&FlagHasInvalid != 0 {
		if len(payload) < off+mLen {
			return nil, fmt.Errorf("%w: need %d bytes for invalid_mask, have %d", ErrPayloadTruncated, mLen, len(payload)-off)
		}
		invalidMask := payload[off : off+mLen]
		off += mLen
		f.Invalid = make([]bool, n)
		for i := 0; i < n; i++ {
			f.Invalid[i] = invalidMask[i/8]&(1<<uint(i%8)) != 0
			if f.Invalid[i] && f.Changed[i] {
				return nil, fmt.Errorf("%w: field index %d", ErrInvalidMaskChanged, i)
			}
		}
	}

	wantBits := bitsLen(f.Changed, specs)
	needBytes := (wantBits + 7) / 8
	if len(payload) < off+needBytes {
		return nil, fmt.Errorf("%w: need %d bytes for bit stream, have %d", ErrPayloadTruncated, needBytes, len(payload)-off)
	}
	r := bitReader{data: payload[off : off+needBytes]}
	for i, c := range f.Changed {
		if !c {
			continue
		}
		f.Codes[i] = r.readBits(specs[i].Bits)
	}
	// 尾部多余字节：说明写入方用了不同的字段集合/位宽（slot 版本不一致）。
	// 静默忽略会让"值写串"变成无人发现的慢性病（§8 风险表 device15 多帧类型），
	// 因此显式报错 —— 解码层据此跳到下一个关键帧并打点。
	if len(payload) > off+needBytes {
		return nil, fmt.Errorf("%w: %d extra byte(s)", ErrPayloadTrailing, len(payload)-off-needBytes)
	}
	return f, nil
}

// bitWriter 是 LSB-first 位写入器。
//
// 按"每次最多填满当前字节"的方式推进，而不是把整个 uint64 累加器一次左移 ——
// 后者在 Bits 最大 62 + 残留 7 位时会溢出 64 位。这里每个 chunk 都 ≤ 8 位，
// 永远不越界。
type bitWriter struct {
	out  []byte
	acc  byte
	nacc uint8
}

func (w *bitWriter) reset() {
	w.out = w.out[:0]
	w.acc = 0
	w.nacc = 0
}

func (w *bitWriter) writeBits(v uint64, n uint8) {
	for n > 0 {
		avail := uint8(8) - w.nacc
		take := avail
		if n < take {
			take = n
		}
		chunk := byte(v & ((uint64(1) << take) - 1))
		w.acc |= chunk << w.nacc
		w.nacc += take
		v >>= take
		n -= take
		if w.nacc == 8 {
			w.out = append(w.out, w.acc)
			w.acc = 0
			w.nacc = 0
		}
	}
}

// flush 输出最后一个不满的字节（高位补 0）。无残留时返回空切片。
func (w *bitWriter) flush() []byte {
	if w.nacc > 0 {
		w.out = append(w.out, w.acc)
		w.acc = 0
		w.nacc = 0
	}
	return w.out
}

// bitReader 是 LSB-first 位读取器（与 bitWriter 严格对称）。
type bitReader struct {
	data []byte
	pos  int // 位游标
}

func (r *bitReader) readBits(n uint8) uint64 {
	var v uint64
	var shift uint8
	for n > 0 {
		byteIdx := r.pos / 8
		if byteIdx >= len(r.data) {
			// 调用方已按 needBytes 校验过长度，这里不可达；返回已读部分而不是 panic。
			return v
		}
		bitIdx := uint8(r.pos % 8)
		avail := 8 - bitIdx
		take := avail
		if n < take {
			take = n
		}
		chunk := (r.data[byteIdx] >> bitIdx) & byte((uint64(1)<<take)-1)
		v |= uint64(chunk) << shift
		shift += take
		r.pos += int(take)
		n -= take
	}
	return v
}
