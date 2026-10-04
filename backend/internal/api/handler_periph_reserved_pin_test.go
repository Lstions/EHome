package api

import (
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

// TestReservedPinForPlatform_KnowsBootPins 锁住"哪个引脚是 BOOT 按键"这份清单。
//
// 背景（2026-10-04 现场事故）：压力测试把 PWM0 配到 S3 的 GPIO0，而 GPIO0 是
// BOOT 按键 / strapping 引脚。PWM 以 duty=500/16384（3%）把它拉低 97% 的时间，
// factory_reset_task 每 100ms 轮询到低电平并走满 5s 长按判定，于是设备每 8.8s
// 擦一次 NVS 并重启：红/蓝/紫灯交替闪烁、配置永远 success=false、根本连不稳定
// 运行。一个 UI 上完全合法的 PWM 配置把设备变成了砖。
//
// 这条门禁刻意做成"按平台查表"而不是"按范围猜"：S3 的 GPIO0 与 C6 的 GPIO9 是
// 各自 BOOT 按键，两个数字完全不同，任何"统一写死 GPIO0"的实现都会漏掉 C6。
func TestReservedPinForPlatform_KnowsBootPins(t *testing.T) {
	cases := []struct {
		platform string
		pin      int
		known    bool
	}{
		{"ESP32S3", 0, true},
		{"esp32s3", 0, true},
		{"ESP32-S3", 0, true},
		{"ESP32C6", 9, true},
		{"esp32c6", 9, true},
		{"ESP32-C6", 9, true},
		// 未知平台不设限：不能因为新增芯片就让所有配置被拒（固件侧的过滤
		// 对任何平台都生效，这里只是纵深防线）。
		{"ESP32", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		pin, known := reservedPinForPlatform(tc.platform)
		if known != tc.known {
			t.Errorf("reservedPinForPlatform(%q) known=%v，期望 %v", tc.platform, known, tc.known)
			continue
		}
		if known && pin != tc.pin {
			t.Errorf("reservedPinForPlatform(%q) pin=%d，期望 %d", tc.platform, pin, tc.pin)
		}
	}
}

// TestCheckNotReservedPin_RejectsBootPin 是本次事故的直接回归锁。
//
// 它断言的是 checkNotReservedPin 的**判定方向**：BOOT 引脚必须被拒，
// 其它引脚必须放行。只测前者会让"把所有引脚都拒掉"这种实现通过；
// 只测后者则完全测不到这次的事故。两个方向都锁。
func TestCheckNotReservedPin_RejectsBootPin(t *testing.T) {
	s3 := &models.Node{Platform: "ESP32S3"}
	c6 := &models.Node{Platform: "ESP32C6"}
	unknown := &models.Node{Platform: "ESP32"}

	// 必须被拒：各自平台的 BOOT 引脚。
	for _, tc := range []struct {
		node *models.Node
		pin  int
	}{
		{s3, 0},
		{c6, 9},
	} {
		err := checkNotReservedPin(tc.node, tc.pin)
		if err == nil {
			t.Fatalf("平台 %s 的 BOOT 引脚 %d 被放行了：它可以被配成 GPIO/PWM 输出，"+
				"从而伪装成按键长按并触发 NVS 擦除 + 重启（2026-10-04 事故）",
				tc.node.Platform, tc.pin)
		}
		// 错误信息必须能让人知道"该换个引脚"，而不是只说"非法"。
		if !strings.Contains(err.Error(), "BOOT") {
			t.Errorf("错误信息没有点明 BOOT 引脚：%v", err)
		}
		if !strings.Contains(err.Error(), "factory reset") {
			t.Errorf("错误信息没有说明后果（factory reset）：%v", err)
		}
	}

	// 必须放行：非 BOOT 引脚（含另一个平台的 BOOT 引脚号，不能张冠李戴）。
	for _, tc := range []struct {
		node *models.Node
		pin  int
	}{
		{s3, 9}, // GPIO9 在 S3 上不是 BOOT（C6 才是）
		{c6, 0}, // GPIO0 在 C6 上是普通引脚
		{s3, 3},
		{unknown, 0}, // 未知平台不设限
	} {
		if err := checkNotReservedPin(tc.node, tc.pin); err != nil {
			t.Errorf("平台 %s 的普通引脚 %d 被误拒：%v", tc.node.Platform, tc.pin, err)
		}
	}
}

// TestValidateReportedGPIO_RejectsReservedEvenWhenReported 覆盖"旧固件"路径。
//
// 场景：现场设备跑的是 2.6.x 旧固件，它的 ResourceReport **仍然上报 GPIO0**
// （固件侧的过滤是 2.7.0 才加的），而服务端已经升级。如果没有这道服务端检查，
// 旧固件的节点依然能被配上 PWM0→GPIO0，事故照旧复发。
//
// 这里的 capabilities 就是把 GPIO0 上报成可用 GPIO 的旧固件形态。
func TestValidateReportedGPIO_RejectsReservedEvenWhenReported(t *testing.T) {
	node := &models.Node{
		NodeID:       "SIM-S3",
		Platform:     "ESP32S3",
		Capabilities: `{"buses":{"gpio":[{"pin":0},{"pin":1},{"pin":3}]}}`,
	}

	err := validateReportedGPIO(nil, node, 0)
	if err == nil {
		t.Fatal("GPIO0 被旧固件上报了就直接放行：这正是 2026-10-04 事故的复发路径")
	}
	if !strings.Contains(err.Error(), "BOOT") {
		t.Errorf("错误信息没有点明 BOOT 引脚：%v", err)
	}
}
