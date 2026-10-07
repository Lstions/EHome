package api

import (
	"encoding/hex"
	"fmt"
	"strings"

	"ehome/backend/internal/uartcfg"
)

// ===== UART bus_config 在 api 包内的唯一布局真源 =====
//
// 与 `nodemgr/handler_channel_cmd_v2.go` 的 `set_baud_rate` 副作用**逐字节一致**：
//
//	[0]    tx 引脚
//	[1]    rx 引脚
//	[2..5] 波特率（big-endian uint32）
//	[6]    DMA flags（`& 0x01` = DMA 使能）
//
// 为什么 byte 6 是 DMA flags 而不是 data_bits（2026-10-03 实机核对）：
// 固件的权威定义在 `bus_dma.h:59-62`（"UART: [tx, rx, baud×4] + optional flags
// byte at offset 6"）与 `config_mgr.h` 的 config_channel_get_dma_enabled()
// （UART: flags_offset=6, min_len=7 ⇒ `bus_config[6] & 0x01`）。本仓生产里
// 三条 UART 通道的实际值也全是 `...258001`：byte 6 = 0x01 = DMA 开。
//
// 旧实现在这里写 data_bits（默认 8）⇒ byte 6 = 0x08 ⇒ `0x08 & 0x01 == 0`
// ⇒ **写成 DMA 关闭**。当时未爆是因为 manifest 的 field 8（dma_enabled）每通道
// 都会下发，固件 `dma_enabled_present=true` 短路了 flags 读取；只在 field 8
// 缺失时才会退化成读 byte 6，那时 DMA 被静默关掉。
//
// 帧格式（data_bits/stop_bits/parity/flow）**不进 bus_config**：固件把 UART
// 硬编码为 8N1（`bus_dma.c:431-436` 的 uart_config_t 是常量），且 UART 分支
// 从不读 byte 7..9。把它们编进去只会制造"看着配了、实际没配"的假象。
// 表单里的这些参数仍在 Channel.Config（JSON）里保留可读性。
//
// 为什么必须抽成共用实现：本文件原先只服务「改波特率」一处；现在「新建 UART 通道时
// 按节点能力补齐 bus_config」也要造同一份布局。若各写一份，任何一处漂移都会造出
// **节点端解析不了**的 bus_config —— 而且是在「创建时看着成功、以后改波特率才报错」
// 这种延迟形态里暴露。故创建与改写两处共用下面的构造函数与字节写入函数。
//
// 不能直接复用 nodemgr 的函数：后者嵌在 `applySN3001ControlSideEffectTx` 里
// （绑定 device_type/action_id/事务），无法直接调用。**两处若将来分叉，本注释是发现点**。
//
// 默认波特率取 9600 而不是 115200：本仓真实 UART 设备（BMS 4800/9600、SN-3001 4800、
// 逆变器 2400）都不支持 115200。拿设备不支持的速率当默认值，会让「建完就能用」变成
// 「建完必须先改波特率」。9600 是这些设备实际在用的档位，故选它。
//
// ⚠ 2026-10-07：这些布局常量/函数**已搬到 internal/uartcfg**（P4：同一语义一份定义）。
// 原因：**下发路径**在 internal/nodemgr（sender_snapshot.go），而它不能 import 本包
// （api 依赖 nodemgr ⇒ 会成环）⇒ 只能各写一份，而"各写一份"已在真机上造成缺陷：
// 2 字节的 UART bus_config 被原样下发，固件要求 >= 6 ⇒ 设备
// `preinstall rejected by resource plan`，而后端返回 201。
// ⇒ 现由 uartcfg 提供唯一实现，本包与 nodemgr **共用**。
// 下面保留同名薄包装只为不惊扰本包内的既有调用点与测试。
const defaultUARTBaudrate = uartcfg.DefaultBaudrate

// uartBusConfigLen 是 UART bus_config 的标准长度（7 字节）。
const uartBusConfigLen = uartcfg.LayoutLen

// setUARTBaudrateBytes 把波特率写进 bus_config 的字节 2..5（big-endian）。
// 委托给 uartcfg —— 全仓**唯一的字节偏移实现**。
func setUARTBaudrateBytes(data []byte, baudrate int) {
	uartcfg.SetBaudrateBytes(data, baudrate)
}

// buildUARTBusConfig 按标准布局造出 7 字节的 UART bus_config hex 串（大写）。
// 新建 UART 通道且调用方未提供 bus_config 时由后端兜底调用，保证落库的一定是
// 可被 withUARTBaudrate 解析/改写的合法布局（否则用户会得到一个「改不了波特率」的通道）。
func buildUARTBusConfig(txPin, rxPin, baudrate int, dmaEnabled bool) string {
	return uartcfg.Build(txPin, rxPin, baudrate, dmaEnabled)
}

// uartDMAFlagsByte 是 byte 6 的唯一构造点：固件只读 bit0。
func uartDMAFlagsByte(enabled bool) byte {
	if enabled {
		return 0x01
	}
	return 0x00
}

// withUARTBaudrate 在 UART 通道的 hex bus_config 上改写波特率，返回新的 hex 串。
//
// 字节 2..5 的写入走 setUARTBaudrateBytes —— 与「新建补齐」的 buildUARTBusConfig
// 共用同一实现，故两条路径不可能写出不同的波特率偏移。其余字节（引脚/模式）原样保留，
// 不擅自截断调用方数据（历史布局可能长于/短于标准 10 字节）。
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
	setUARTBaudrateBytes(data, baudrate)
	return strings.ToUpper(hex.EncodeToString(data)), nil
}

