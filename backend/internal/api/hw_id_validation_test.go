package api

import "testing"

// TestHwIDShapeOK 锁定 bind_to 的"形状"校验。
//
// 事故背景（2026-10-04）：S3 的 node.Config 里存着 bind_to="uart/uart1"，
// 而固件规范 id 是 "uart/UART1"（仅大小写不同）。固件 dma_pool_apply_config
// 不校验值、直接标 ALLOCATED；之后 preempt 与 release 都用 strcmp 比对，
// 永远匹配不上 -> 该 GDMA 通道被永久占用。S3 只有 5 条 GDMA，占掉 1 条后
// 「3 UART + SPI2 + I2C0」分配不出 DMA，I2C0 报 "No DMA for i2c/I2C0"，
// 整份 manifest 以 ESP_ERR_NOT_FOUND 被拒、success=false。
//
// 当时后端只校验 bind_to 的**长度**（<=16），任意字符串都被接受并持久化，
// 所以错配能在库里躺很久，直到某天通道数刚好用满 DMA 资源才爆发。
func TestHwIDShapeOK(t *testing.T) {
	valid := []string{
		"uart/UART0", "uart/UART1", "uart/UART2",
		"spi/SPI2", "spi/SPI3",
		"i2c/I2C0", "i2c/I2C1",
		"usb/USB0",
		// 恰好 16 字节（上限）的未知控制器形式："uart/UNKNOWN_11" = 5+11
		"uart/UNKNOWN_11",
	}
	for _, s := range valid {
		if !hwIDShapeOK(s) {
			t.Errorf("hwIDShapeOK(%q) = false, 期望 true（固件会产出的规范形状）", s)
		}
	}

	invalid := []string{
		"",                 // 空串：调用方另行跳过，形状校验本身应判否
		"uart",             // 缺 "/<ID>"
		"UART0",            // 缺总线前缀
		"/UART0",           // 缺总线名
		"uart/",            // 缺 ID
		"uart/UART0/extra", // 多余层级
		"serial/UART0",     // 总线名不在 {uart,spi,i2c,usb}
		"uart/UART 0",      // 含空格
		"uart/UART0;DROP",  // 含分隔符
		// 固件 derive_hw_id 的兜底形式是 20 字节（"uart/UNKNOWN_FF_FF"），
		// 超过 16 字节的线缆/缓冲区上限，因此**不可能**成为合法的 bind_to：
		// 那条兜底路径只在固件内部自证唯一性，不该被回填成配置值。
		"uart/UNKNOWN_0B_0C",
	}
	for _, s := range invalid {
		if hwIDShapeOK(s) {
			t.Errorf("hwIDShapeOK(%q) = true, 期望 false", s)
		}
	}
}

// TestHwIDCaseSuggestion_CatchesTheProductionValue 是本缺陷的直接回归锁。
//
// 断言产线上真实存在的 "uart/uart1" 必须被识别为大小写不匹配，并给出
// 规范写法 "uart/UART1"。去掉修复（或改成大小写不敏感比较）即变红。
func TestHwIDCaseSuggestion_CatchesTheProductionValue(t *testing.T) {
	// 生产库原值：30EDA0A9A808 (S3) 的 node.Config.dma_configs[0].bind_to
	const prodValue = "uart/uart1"

	got := hwIDCaseSuggestion(prodValue)
	if got != "uart/UART1" {
		t.Fatalf("hwIDCaseSuggestion(%q) = %q, 期望 \"uart/UART1\"\n"+
			"产线正是用这个值让一条 GDMA 通道被永久占用，必须能被拦下", prodValue, got)
	}

	// 已是规范写法的不应再提示，否则会误报合法的存量配置。
	for _, ok := range []string{"uart/UART0", "uart/UART1", "spi/SPI2", "i2c/I2C0"} {
		if s := hwIDCaseSuggestion(ok); s != "" {
			t.Errorf("hwIDCaseSuggestion(%q) = %q, 期望空串（已是规范写法）", ok, s)
		}
	}

	// 规范集里没有的 id 不提示（无"正确写法"可给）；
	// 形状问题由 hwIDShapeOK 负责，两者分工不重叠。
	if s := hwIDCaseSuggestion("uart/UART9"); s != "" {
		t.Errorf("hwIDCaseSuggestion(\"uart/UART9\") = %q, 期望空串（规范集中无 UART9）", s)
	}
}

// TestHwIDCaseSuggestion_CoversEveryBus 防止提示表只覆盖 UART 而漏掉 SPI/I2C。
//
// 本次事故发生在 UART 上，但同一机制对 SPI/I2C 完全一样 —— 若有人只按
// "事故现场"补 UART 分支，SPI/I2C 的同类错配仍会静默通过。
func TestHwIDCaseSuggestion_CoversEveryBus(t *testing.T) {
	cases := map[string]string{
		"uart/uart0": "uart/UART0",
		"spi/spi2":   "spi/SPI2",
		"spi/spi3":   "spi/SPI3",
		"i2c/i2c0":   "i2c/I2C0",
		"i2c/i2c1":   "i2c/I2C1",
	}
	for in, want := range cases {
		if got := hwIDCaseSuggestion(in); got != want {
			t.Errorf("hwIDCaseSuggestion(%q) = %q, 期望 %q", in, got, want)
		}
	}
}
