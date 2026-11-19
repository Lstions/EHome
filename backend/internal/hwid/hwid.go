// Package hwid 是设备端**规范硬件标识**（bind_to / hw_id）的唯一真源。
//
// 为什么单独成包：这份规范现在有两个消费者，且两者必须**完全一致** ——
//   - internal/api：PUT /nodes/:id/dma-config 拒绝大小写不符的写入（报 400 并给出正确写法）；
//   - internal/database：启动迁移修复历史落库的小写值。
//
// 两份拷贝一旦漂移，迁移会把值"修"成 API 不接受的字符串，或漏修 ⇒ 设备侧
// GDMA 通道继续被永久占用。
//
// 值的形状与固件 derive_hw_id() 完全一致：
//
//	bus_manager.c: snprintf(buf, buflen, "%s/%s", bus_name, id)
//
// bus_name ∈ {uart, spi, i2c, usb}（小写）；id 来自 hw_profile/hw_tables.c 的 .id，
// **大写**（UART0 / SPI2 / I2C0 / USB0）。比较在设备侧是 strcmp，大小写敏感。
package hwid

import (
	"regexp"
	"strings"
)

// shapeRE 匹配固件可能产出的 hw_id 形状。
//
// 未知控制器时固件产出 "uart/UNKNOWN_%02X_%02X"（20 字节），超出 16 字节的
// 线缆/固件缓冲区上限（dma_pool.h DMA_BOUND_MAX=16），因此 **不可能** 成为合法
// bind_to —— 本正则允许到 12 个字符的 ID 只为覆盖截断后的 "UNKNOWN_11"，
// 具体长度上限由 ShapeOK 的 len<=16 兜住。
var shapeRE = regexp.MustCompile(`^(uart|spi|i2c|usb)/([A-Za-z0-9_]{1,12})$`)

// canonicalIDs 是固件可能产出的 hw_id 全集（S3 与 C6 两平台并集）。
//
// 刻意用**白名单**而不是"看着像就行"：真正的失败模式是"拼错/大小写错之后被
// 静默接受"。代价是新增控制器要同步一次，而这个表极小、极稳定。
var canonicalIDs = []string{
	"uart/UART0", "uart/UART1", "uart/UART2",
	"spi/SPI2", "spi/SPI3",
	"i2c/I2C0", "i2c/I2C1",
	"usb/USB0",
}

// ShapeOK 判断 s 是否符合规范 hw_id 的形状（不判断该控制器是否真实存在）。
//
// 长度上限 16 是**线缆与固件缓冲区**的硬上限（handler_node.go 的长度检查、
// 固件 dma_pool.h 的 DMA_BOUND_MAX），此处一并检查，避免"形状对但存不下"。
func ShapeOK(s string) bool {
	return len(s) <= 16 && shapeRE.MatchString(s)
}

// Canonical 在 s 与某个规范 id **仅大小写不同**时返回该规范 id，否则返回空串。
//
// 刻意只提示、不改写：2026-10-04 的事故正是"用户填的值和系统实际用的值不一致，
// 而界面上看不出任何问题"。静默把 uart/uart1 改成 uart/UART1 会让同样的错配
// 再次隐形成立 —— 用户以为填对了，实际系统用的是另一个字符串。
func Canonical(s string) string {
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	for _, id := range canonicalIDs {
		if id == s {
			return "" // 完全匹配，无需提示
		}
		if strings.ToLower(id) == lower {
			return id
		}
	}
	return ""
}

// Normalize 用于**修复存量数据**（不是校验入口）：
//
//	canonical, ok := hwid.Normalize(stored)  // ok=false 表示值不认识
//
// 与 Canonical 的区别只在返回值形态：已是规范写法时也返回自身（ok=true），
// 调用方据此判断"是否需要改写"。只做大小写归一，不做去空格/截断等猜测性处理 ——
// 任何超出"仅大小写不符"的输入都应原样保留，让它在写入时被 API 明确拒绝。
func Normalize(s string) (string, bool) {
	if s == "" {
		return s, false
	}
	lower := strings.ToLower(s)
	for _, id := range canonicalIDs {
		if id == s {
			return id, true
		}
		if strings.ToLower(id) == lower {
			return id, true
		}
	}
	return s, false
}
