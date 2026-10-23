package ingest

import (
	"errors"
	"fmt"
	"strings"
)

// Reason 是解析失败的可分类原因（**有界枚举**，公理 A5）。
//
// 进指标标签与审计字段，因此禁止拼接设备 ID / 字段名等高基数值。
type Reason string

const (
	// ReasonNoAdapter 没有任何 adapter 支持该设备。
	//
	// 出现即说明设备类型未注册驱动、且没有可用的 DeviceConfig parser ——
	// 这是**配置缺陷**（不是设备故障），运维应补驱动或补 device_configs。
	ReasonNoAdapter Reason = "no_adapter"
	// ReasonDecodeFailed 解析器返回错误（帧格式不符/校验失败等）。
	ReasonDecodeFailed Reason = "decode_failed"
	// ReasonEmptyResult 解析成功但零字段。
	//
	// 必须与 ReasonDecodeFailed 区分：零字段通常是**协议版本不匹配**
	// （解析器认为帧合法但取不到任何声明字段），比格式错误更难发现。
	ReasonEmptyResult Reason = "empty_result"
	// ReasonCalibrationMiss 标定驱动缺标定字节。
	//
	// 既有行为是**拒绝落库**（consumers_heavy.go:310 附近），不能降级解析 ——
	// 未标定的读数会污染历史数据且无法事后区分。
	ReasonCalibrationMiss Reason = "calibration_missing"
	// ReasonCommandCtxMiss 命令感知驱动缺命令上下文。
	ReasonCommandCtxMiss Reason = "command_ctx_missing"
)

// DecodeError 是可分类的解析失败。
//
// 为什么要有专门的错误类型：consumer 需要按 Reason 落不同审计与指标标签，
// 用字符串匹配 err.Error() 会因文案改动而静默失效。
type DecodeError struct {
	// Adapter 是失败发生时尝试的 adapter 名（有界枚举）。
	Adapter string
	// Reason 是分类原因。
	Reason Reason
	// Err 是底层错误（可为 nil，例如零字段情形）。
	Err error
}

// Error 实现 error。
func (e *DecodeError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err != nil {
		return fmt.Sprintf("ingest: adapter=%s reason=%s: %v", e.Adapter, e.Reason, e.Err)
	}
	return fmt.Sprintf("ingest: adapter=%s reason=%s", e.Adapter, e.Reason)
}

// Unwrap 支持 errors.Is/As 穿透到底层错误。
func (e *DecodeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// NewDecodeError 构造一个分类错误。
func NewDecodeError(adapter string, reason Reason, err error) *DecodeError {
	return &DecodeError{Adapter: adapter, Reason: reason, Err: err}
}

// ReasonOf 从错误链里提取 Reason。
//
// 返回 ok=false 表示不是 DecodeError（例如 adapter 内部 panic 转成的普通 error）。
// 调用方对 ok=false 应当按 ReasonDecodeFailed 归类，**不要**静默丢弃。
func ReasonOf(err error) (Reason, bool) {
	var de *DecodeError
	if errors.As(err, &de) {
		return de.Reason, true
	}
	return "", false
}

// MultiError 聚合多个 adapter 的失败原因。
//
// 为什么需要：Resolver 会按顺序尝试多个 adapter，全部失败时需要知道
// 「每个都为什么失败」—— 只报最后一个会掩盖真正原因（例如 ConfigParser 因
// JSON 非法失败、Driver 因未注册失败，只报后者会误导运维去补驱动）。
type MultiError struct {
	Errs []*DecodeError
}

// Error 实现 error（保持简短；详情用 FailedAdapters）。
func (m *MultiError) Error() string {
	if m == nil || len(m.Errs) == 0 {
		return "ingest: no adapter attempted"
	}
	parts := make([]string, 0, len(m.Errs))
	for _, e := range m.Errs {
		parts = append(parts, fmt.Sprintf("%s:%s", e.Adapter, e.Reason))
	}
	return "ingest: all adapters failed [" + strings.Join(parts, ", ") + "]"
}

// Unwrap 返回第一个错误，支持 errors.Is/As 穿透。
func (m *MultiError) Unwrap() error {
	if m == nil || len(m.Errs) == 0 {
		return nil
	}
	return m.Errs[0]
}

// FailedAdapters 返回「adapter 名 → 原因」的映射（供审计与指标）。
func (m *MultiError) FailedAdapters() map[string]Reason {
	out := make(map[string]Reason, len(m.Errs))
	for _, e := range m.Errs {
		out[e.Adapter] = e.Reason
	}
	return out
}
