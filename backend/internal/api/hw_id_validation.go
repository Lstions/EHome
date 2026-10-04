package api

import (
	"regexp"
	"strings"
)

// hwIDShapeRE 匹配固件 derive_hw_id() 产出的规范 hw_id 形状：
//
//	bus_manager.c: snprintf(buf, buflen, "%s/%s", bus_name, id)
//
// 其中 bus_name ∈ {uart, spi, i2c, usb}（小写），id 来自 hw_tables 的 .id
// （大写，形如 UART0 / SPI2 / I2C0）。未知控制器时固件产出
// "uart/UNKNOWN_%02X_%02X" 形式，也一并接受。
//
// 注意：**比较是大小写敏感的**（dma_pool 里是 strcmp），所以这里只做"形状"
// 校验，"值是否真的存在"交给 hwIDCaseSuggestion。
var hwIDShapeRE = regexp.MustCompile(`^(uart|spi|i2c|usb)/([A-Za-z0-9_]{1,12})$`)

// canonicalHwIDs 是当前固件可能产出的 hw_id 全集。
//
// 来源：esp32-collector/components/hw_profile/hw_tables.c 的 .id 字段。
// 这里刻意用**白名单**而不是"看起来像就行"：
//   - 真正的失败模式是"拼错/写错大小写后被静默接受"，白名单能挡住；
//   - 代价只是新增控制器时要同步一次，而这个表本身极小、极稳定。
//
// 覆盖 S3 与 C6 两个平台的全部控制器（两表并集）。
var canonicalHwIDs = []string{
	"uart/UART0", "uart/UART1", "uart/UART2",
	"spi/SPI2", "spi/SPI3",
	"i2c/I2C0", "i2c/I2C1",
}

// hwIDShapeOK 判断 bind_to 是否符合规范 hw_id 的形状（不判断值是否存在）。
//
// 同时强制 <= 16 字节：这是**线缆与固件缓冲区**的硬上限（服务端
// handler_node.go 的 len>16 检查、固件 dma_pool.h 的 DMA_BOUND_MAX=16）。
// 把长度并进形状检查，是让"形状对但根本存不下"的输入在这里就被拒，
// 而不是靠两个分散的检查各管一半。
//
// 顺带一提：固件 derive_hw_id 的兜底形式 "uart/UNKNOWN_FF_FF" 是 20 字节，
// 因此它**永远不可能**成为合法的 bind_to —— 那条兜底路径只在固件内部用来
// 自证唯一性，不能、也不该被回填成配置值。
func hwIDShapeOK(s string) bool {
	return len(s) <= 16 && hwIDShapeRE.MatchString(s)
}

// hwIDCaseSuggestion 在 bind_to 与某个规范 id **仅大小写不同**时返回该规范 id，
// 否则返回空串。
//
// 为什么只提示不改写：2026-10-04 的事故正是"用户填的值和系统实际用的值不一致，
// 而界面上看不出任何问题"。静默把 uart/uart1 改成 uart/UART1 会让同样的错配
// 再次隐形成立 —— 用户以为自己填对了，实际系统用的是另一个字符串。
// 明确报错 + 给出正确写法，才能让错配在提交时而不是在设备端暴露。
func hwIDCaseSuggestion(s string) string {
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	for _, id := range canonicalHwIDs {
		if id == s {
			return "" // 完全匹配，无需提示
		}
		if strings.ToLower(id) == lower {
			return id
		}
	}
	return ""
}
