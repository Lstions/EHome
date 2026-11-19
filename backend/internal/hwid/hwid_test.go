package hwid

import "testing"

// TestNormalize 锁住迁移侧的唯一改写规则：**仅大小写不符**才修，其它一律不动。
// 这条边界一旦放宽（比如加去空格/模糊匹配），迁移就会去"猜"用户意图，
// 而 2026-10-04 事故的教训正是"系统猜着用了一个用户没填的值"。
func TestNormalize(t *testing.T) {
	cases := []struct {
		in        string
		want      string
		wantKnown bool
	}{
		// 仅大小写不符 —— 迁移要修的就是这一类（生产原值）。
		{"uart/uart1", "uart/UART1", true},
		{"spi/spi2", "spi/SPI2", true},
		{"i2c/i2c0", "i2c/I2C0", true},
		{"usb/usb0", "usb/USB0", true},
		// 已是规范值 —— 无需改写，但仍算"认识"。
		{"uart/UART1", "uart/UART1", true},
		{"spi/SPI2", "spi/SPI2", true},
		// 不认识 —— 原样保留，交给写入校验拒绝。
		{"uart/UART9", "uart/UART9", false},
		{"uart/unknown_11", "uart/unknown_11", false},
		{"foo/bar", "foo/bar", false},
		{" uart/UART1", " uart/UART1", false}, // 不去空格：不做猜测性修复
		{"", "", false},
	}
	for _, c := range cases {
		got, known := Normalize(c.in)
		if got != c.want || known != c.wantKnown {
			t.Errorf("Normalize(%q) = (%q, %v), 期望 (%q, %v)", c.in, got, known, c.want, c.wantKnown)
		}
	}
}

// TestCanonical 锁住写入校验侧的语义：只对"仅大小写不符"给建议，
// 完全匹配与不认识都返回空串（完全匹配无需提示；不认识无"正确写法"可给）。
func TestCanonical(t *testing.T) {
	if got := Canonical("uart/uart1"); got != "uart/UART1" {
		t.Fatalf("Canonical(uart/uart1) = %q", got)
	}
	for _, ok := range []string{"uart/UART0", "uart/UART1", "spi/SPI2", "i2c/I2C0", "usb/USB0"} {
		if got := Canonical(ok); got != "" {
			t.Errorf("Canonical(%q) = %q, 期望空串（已是规范写法）", ok, got)
		}
	}
	for _, unknown := range []string{"uart/UART9", "foo/bar", ""} {
		if got := Canonical(unknown); got != "" {
			t.Errorf("Canonical(%q) = %q, 期望空串（规范集中无此 id）", unknown, got)
		}
	}
}

// TestShapeOK 保留形状边界的关键反例（细节见 internal/api 的同名用例）。
func TestShapeOK(t *testing.T) {
	for _, ok := range []string{"uart/UART0", "spi/SPI2", "i2c/I2C1", "usb/USB0", "uart/UNKNOWN_11"} {
		if !ShapeOK(ok) {
			t.Errorf("ShapeOK(%q) = false, 期望 true", ok)
		}
	}
	for _, bad := range []string{"", "uart", "UART0", "/UART0", "uart/", "uart/UART0/extra", "serial/UART0", "uart/UART 0", "uart/UART0;DROP", "uart/UNKNOWN_0B_0C"} {
		if ShapeOK(bad) {
			t.Errorf("ShapeOK(%q) = true, 期望 false", bad)
		}
	}
}
