// Package uartcfg 是 **UART bus_config 字节布局的唯一实现**（P4：同一语义一份定义）。
//
// 为什么必须抽出来：这份布局此前只存在于 internal/api（channel_reconfigure.go），
// 而**下发给设备**的那条路径在 internal/nodemgr（sender_snapshot.go）。
// 后者不能 import api（api 依赖 nodemgr ⇒ 会成环），于是只能各写一份 ——
// 而"各写一份"已经在真机上造成了可观测缺陷：
//
//   UART 的 bus_config 只有 2 字节（只配了引脚、还没配波特率）时，
//   后端**原样下发**，而固件要求 >= 6 字节：
//     bus_manager.c:443  `if (ch->bus_config_len < 6) return ESP_ERR_INVALID_SIZE;`
//     （它必须读到 byte 2..5 的 big-endian 波特率）
//   ⇒ 接口返回 201「通道创建成功」，设备侧
//     `BUS_MGR: preinstall rejected by resource plan` + `ConfigResult success=0`，
//     而**操作员在界面上看到的是成功** —— 本仓反复记的"后端说成功、设备静默失败"。
//
// 布局（与固件 bus_manager.c 的读法逐字节对齐）：
//
//	byte 0     tx 引脚
//	byte 1     rx 引脚
//	byte 2..5  波特率（**big-endian**）
//	byte 6     DMA flags（固件只读 bit0；旧布局偶有缺失 ⇒ 6 与 7 都合法）
//
// ⚠ 长度**至少 6**（固件硬下限）；本仓的标准布局是 7（含 DMA 位）。
package uartcfg

import "encoding/hex"

// DefaultBaudrate 是新建/补齐 UART 通道时的默认波特率。
//
// 取 9600 而不是 115200：本仓真实 UART 设备（BMS 4800/9600、SN-3001 4800、逆变器 2400）
// 都不支持 115200。拿设备不支持的速率当默认值，会让「建完就能用」变成「建完必须先改波特率」。
const DefaultBaudrate = 9600

// LayoutLen 是本仓 UART bus_config 的标准长度（7 字节）：tx + rx + 4 字节波特率 + 1 字节 DMA flags。
// 与生产既有行、固件 uart_init（>= 6）和 dma 位读取（>= 7）都对齐。
const LayoutLen = 7

// MinLen 是**固件**接受下发的硬下限（bus_manager.c:443）。
// 短于此值的 bus_config 会被固件判 ESP_ERR_INVALID_SIZE。
const MinLen = 6

// SetBaudrateBytes 把波特率写进 bus_config 的字节 2..5（big-endian）。
//
// 这是**唯一的字节偏移实现**：新建补齐（Build）、改写（WithBaudrate）与下发补全
// 都经由它 —— 任何第三处再抄一遍 2/3/4/5 的位移都视为布局漂移。
// ⚠ 调用方必须保证 len(data) >= MinLen（本函数不做长度检查，以免在热点路径上重复判断）。
func SetBaudrateBytes(data []byte, baudrate int) {
	target := uint32(baudrate)
	data[2] = byte(target >> 24)
	data[3] = byte(target >> 16)
	data[4] = byte(target >> 8)
	data[5] = byte(target)
}

// Build 按上面的布局造出 UART bus_config 的 hex 串（大写）。
//
// dmaEnabled 直接决定 byte 6：固件按 `byte6 & 0x01` 判 DMA。
func Build(txPin, rxPin, baudrate int, dmaEnabled bool) string {
	data := make([]byte, LayoutLen)
	data[0] = byte(txPin)
	data[1] = byte(rxPin)
	SetBaudrateBytes(data, baudrate)
	if dmaEnabled {
		data[6] = 0x01
	}
	return hex.EncodeToString(data)
}

// PadShortUART 把**短于 MinLen 的 UART bus_config** 补齐到 LayoutLen。
//
// 为什么是补齐而不是拒绝：2 字节是合法的"只配了引脚、还没配波特率"状态，
// 仿真套件与存量库都在用（内部 api 包的 TestChannelUpdate_UART2ByteRouteAccepted
// 明确断言"调用方显式给的引脚路由必须原样保留"）。⇒ **入库保持原样、下发时补全**，
// 两个契约各自成立：既不改用户看到的值，也不让设备收到它必然拒绝的字节。
//
// 引脚沿用已有字节（不覆盖）；波特率用 DefaultBaudrate；DMA 位为 0
// （不擅自替用户打开 DMA —— 该位另有 dma_enabled 字段承载）。
//
// 返回 (补齐后的字节, 是否做了改动)。已 >= MinLen 时返回 (nil, false)，
// 调用方应保持**逐字节不动**，否则会篡改用户已配的波特率/DMA 位。
func PadShortUART(data []byte) ([]byte, bool) {
	if len(data) >= MinLen {
		return nil, false
	}
	if len(data) < 2 {
		return nil, false /* 连引脚都不够：交由调用方按"非法"处理，本函数不猜 */
	}
	padded := make([]byte, LayoutLen)
	padded[0] = data[0]
	padded[1] = data[1]
	SetBaudrateBytes(padded, DefaultBaudrate)
	return padded, true
}
