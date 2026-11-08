package telemetry

import (
	"errors"
	"fmt"
)

// L2 写入路径的构造类错误。
//
// ⚠ 失败语义（契约 §3.4，2026-11-07 起 L2 无条件启用）：这三个错误在启动期
// **必须导致 Fatal** —— 帧表已是主路径之一，"主写成功但帧表没建"是不可接受的
// 半残状态。ErrTablesMissing 尤其如此：它意味着分区迁移没跑，而不是"派生表
// 可缺席"。（此前作为影子旁路时它们是 fail-open + Warn 的。）
var (
	// ErrNilDB 表示构造双写器时没有数据库。
	ErrNilDB = errors.New("telemetry: nil db")
	// ErrNilDictionary 表示没有 slot 字典（编解码都无从进行）。
	ErrNilDictionary = errors.New("telemetry: nil slot dictionary")
	// ErrTablesMissing 表示 telemetry_frame / telemetry_slot 尚未建表
	// （迁移没跑过 / 分区母表没建）。
	ErrTablesMissing = errors.New("telemetry: telemetry_frame/telemetry_slot not found (partition migration not applied)")
)

// UnknownSpecVersionError 表示遇到本进程不认识的量化规格版本。
// UnknownSpecVersionError 表示遇到本进程不认识的量化规格版本。
//
// 为什么要显式报错而不是回落当前版本：见 SpecVersion 注释 —— 规格不同
// 意味着码点语义不同，静默按错规格解会产生**看起来正常但全错**的物理量，
// 比直接跳过该帧危险得多。解码层遇到它按"跳到下一个关键帧并打点"处理（§8）。
type UnknownSpecVersionError struct {
	Version uint16
}

func (e *UnknownSpecVersionError) Error() string {
	return fmt.Sprintf("telemetry: unknown quantization spec version %d (this build supports %d)", e.Version, SpecVersion)
}
