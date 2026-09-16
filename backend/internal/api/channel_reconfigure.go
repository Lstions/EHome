package api

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// withUARTBaudrate 在 UART 通道的 hex bus_config 上改写波特率，返回新的 hex 串。
//
// ===== 布局来源（不新创格式）=====
// 与 `nodemgr/handler_channel_cmd_v2.go` 的 `set_baud_rate` 副作用**逐字节一致**：
//
//	· bus_config 为 hex 串（允许 `\x` / `0x` 前缀）；
//	· 波特率位于**字节 2..5**（big-endian，uint32）；
//	· 其余字节（模式/引脚等）原样保留。
//
// 把布局抄在这里而不是复用 nodemgr 的函数，是因为后者嵌在
// `applySN3001ControlSideEffectTx` 里（绑定 device_type/action_id/事务），
// 无法直接调用。**两处若将来分叉，本注释是发现点** —— 故此处显式记录来源。
//
// ===== 为什么校验这么严 =====
// 原缺陷是「不校验就报成功」。改 bus_config 属于**会下发到硬件**的操作，
// 宁可明确失败，也不能写一个坏 hex 进去让节点端解析失败（那时更查不出来）。
func withUARTBaudrate(raw string, baudrate int) (string, error) {
	if baudrate <= 0 {
		return "", fmt.Errorf("baudrate 必须为正整数，收到 %d", baudrate)
	}
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", fmt.Errorf("通道 bus_config 为空，无法重配波特率（请先配置该 UART 通道）")
	}
	if strings.HasPrefix(text, "\\x") || strings.HasPrefix(text, "0x") || strings.HasPrefix(text, "0X") {
		text = text[2:]
	}
	data, err := hex.DecodeString(text)
	if err != nil {
		return "", fmt.Errorf("通道 bus_config 不是合法 hex（%q）: %w", raw, err)
	}
	// 与 nodemgr 同一门槛：至少 6 字节才容纳 字节 2..5 的波特率。
	if len(data) < 6 {
		return "", fmt.Errorf("通道 bus_config 长度不足 6 字节（%d），不似 UART 布局", len(data))
	}
	target := uint32(baudrate)
	current := uint32(data[2])<<24 | uint32(data[3])<<16 | uint32(data[4])<<8 | uint32(data[5])
	if current == target {
		// 返回**原串**（未改动），由调用方据此走「unchanged」分支。
		// 注意：原串可能带 \x / 0x 前缀，而 data 已去前缀 —— 故直接回原串保证幂等比较成立。
		return raw, nil
	}
	data[2] = byte(target >> 24)
	data[3] = byte(target >> 16)
	data[4] = byte(target >> 8)
	data[5] = byte(target)
	return strings.ToUpper(hex.EncodeToString(data)), nil
}
