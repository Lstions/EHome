package sensorfield

import "errors"

// 错误哨兵。上层据此映射 HTTP 400 + error_code（方案 §8.6）。
//
// 约定：
//   - 配置类错误（语法/未知语义/超长）⇒ fail-closed，保存期 400；
//   - 运行期"解析不到"**不是错误**，用 Resolved.Status == unbound 表达。
var (
	// ErrDisabled 表示开关关闭且调用了特殊选择器（字面量仍允许）。
	ErrDisabled = errors.New("sensorfield: field resolver is disabled")

	// ErrNotReady 表示快照尚未加载（启动早期）。
	ErrNotReady = errors.New("sensorfield: registry not ready")

	// ErrEmptySelector 空串/全空白。
	ErrEmptySelector = errors.New("sensorfield: empty selector")

	// ErrSelectorTooLong 超过 MaxSelectorChars。
	ErrSelectorTooLong = errors.New("sensorfield: selector too long")

	// ErrInvalidSyntax 文法非法（含非 ASCII、多聚合、冒号后空、prefix 以 _ 结尾等）。
	ErrInvalidSyntax = errors.New("sensorfield: invalid selector syntax")

	// ErrUnknownSemantic @x 未在别名表定义。
	ErrUnknownSemantic = errors.New("sensorfield: unknown semantic")

	// ErrUnknownVirtual virtual:x 未定义。
	ErrUnknownVirtual = errors.New("sensorfield: unknown virtual")

	// ErrUnsupportedAggregate 聚合不在白名单或大小写错。
	ErrUnsupportedAggregate = errors.New("sensorfield: unsupported aggregate")

	// ErrAliasConflict 别名表冲突（同一物理名多语义且未 allow_overlap；
	// 或候选单位类与 ExpectedUnit 不符）。
	ErrAliasConflict = errors.New("sensorfield: alias conflict")

	// ErrVirtualInvalid 派生定义非法（参数个数/类型错、无环、深度超限）。
	ErrVirtualInvalid = errors.New("sensorfield: invalid virtual definition")

	// ErrVirtualCycle 派生定义存在循环引用。
	ErrVirtualCycle = errors.New("sensorfield: virtual cycle")
)

// ErrorCode 把哨兵映射为对外 error_code（方案 §8.6）。未知错误返回空串。
func ErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrEmptySelector):
		return "empty_selector"
	case errors.Is(err, ErrSelectorTooLong):
		return "selector_too_long"
	case errors.Is(err, ErrInvalidSyntax):
		return "invalid_selector_syntax"
	case errors.Is(err, ErrUnknownSemantic):
		return "unknown_semantic"
	case errors.Is(err, ErrUnknownVirtual):
		return "unknown_virtual"
	case errors.Is(err, ErrUnsupportedAggregate):
		return "unsupported_aggregate"
	case errors.Is(err, ErrDisabled):
		return "field_resolver_disabled"
	default:
		return ""
	}
}
