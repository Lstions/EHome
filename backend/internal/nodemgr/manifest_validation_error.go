package nodemgr

import "errors"

// ManifestValidationError 标记「**用户可行动的配置问题**」导致的 manifest 拒绝。
//
// ⚠ 2026-10-09（用户要求"前端显示错误"）：
//
//	在这之前，所有 manifest 拒绝都以裸 error 返回，API 层一律映射成
//	HTTP 500 Internal Server Error。后果：用户把两条 UART 都开了 DMA
//	（前端能改的配置问题）会看到"服务端错误" —— 既看不出是自己配错了，
//	也可能被前端当成可重试的故障。
//
// 判据（回答"用户看到这个错误能做什么？"）：
//
//	· 能改配置解决 ⇒ ManifestValidationError ⇒ API 映射 400
//	· 服务端/链路故障 ⇒ 普通 error ⇒ API 映射 500
//
// ⚠ 用 errors.As 而不是字符串匹配：字符串会在文案改动时静默失效。
type ManifestValidationError struct {
	Reason string
}

func (e *ManifestValidationError) Error() string { return e.Reason }

// newManifestValidationError 构造一个校验类错误。
func newManifestValidationError(reason string) error {
	return &ManifestValidationError{Reason: reason}
}

// IsManifestValidationError 供 API 层判断该返回 400 还是 500。
func IsManifestValidationError(err error) bool {
	var target *ManifestValidationError
	return errors.As(err, &target)
}
