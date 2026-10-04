package nodemgr

import (
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

// TestDecodeManifestTransportPins_SPI_ReportsMOSIOnce 是 2026-10-04 的回归锁。
//
// 事故：任何启用 SPI 的节点都无法同步配置，服务端每次都以
//
//	ConfigManifest rejected: error=GPIO pin 11 conflict between channel 6 and channel 6
//
// 拒收整份 manifest，ConfigResult 永远 success=false。S3 的 SPI2 因此完全不可用。
//
// 根因：SPI 的 bus_config 里 MOSI 出现两次 ——
//
//	byte0   = MOSI（旧式单引脚形式）
//	byte[6] = MOSI（与 byte0 同义）
//	byte[7] = MISO
//	byte[8] = SCLK
//
// 而本函数原样返回 [data0, data6, data7, data8]，调用方对每个 pin 调 claim()，
// 第二次 claim 同一个 MOSI 即命中"冲突"，报出的却是"channel 6 与 channel 6"——
// 信息把"同一通道重复声明"说成"两个通道冲突"，排查时严重误导。
//
// 为什么只有 SPI 中招：UART/I2C 的 byte0/byte1 是两个不同引脚，不会自撞。
//
// 本用例同时锁三件事：
//
//	① Pins 里不得有重复元素（去掉去重逻辑即变红）；
//	② MOSI/MISO/SCLK 三个引脚都必须被覆盖（不能靠"只返回一个"蒙过去）；
//	③ 与固件的字节语义一致（byte0 与 byte6 同义，缺一不可）。
func TestDecodeManifestTransportPins_SPI_ReportsMOSIOnce(t *testing.T) {
	// 与 S3 实机一致：SPI2 的 mosi=11 miso=13 sclk=12 cs=10。
	// 编码为 byte0=11（mosi）、bytes[6..8]=11,13,12（mosi,miso,sclk）。
	ch := models.Channel{ID: 6, BusType: "SPI", BusConfig: "0b00000000000b0d0c", Enabled: true}

	pins, err := decodeManifestTransportPins(ch, "SPI")
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}

	seen := map[int]int{}
	for _, p := range pins {
		seen[p]++
	}
	for pin, n := range seen {
		if n > 1 {
			t.Errorf("引脚 %d 在 Pins 里出现 %d 次：调用方会对同一引脚重复 claim，"+
				"命中\"conflict between channel 6 and channel 6\"并拒收整份 manifest（2026-10-04 事故）",
				pin, n)
		}
	}

	// MOSI(11)/MISO(13)/SCLK(12) 必须都在：不能靠"少返回几个"通过去重断言。
	for _, want := range []int{11, 13, 12} {
		if seen[want] == 0 {
			t.Errorf("引脚 %d 未被上报：SPI 的 MOSI/MISO/SCLK 必须全部参与占用检查", want)
		}
	}

	// 反向：不得凭空多报引脚（CS=10 不在 bus_config 的 6..8 内，不应出现）。
	if seen[10] != 0 {
		t.Errorf("引脚 10(CS) 被上报：CS 是设备级、不参与通道占用检查，多报会误判冲突")
	}
}

// TestValidateManifestAuthority_SPIChannelDoesNotConflictWithItself 是端到端锁：
// 走真正的权威校验路径，断言一条正常配置的 SPI 通道**能通过**。
//
// 与上面的解码用例互补：解码用例锁"数据形状"，本用例锁"数据形状"是否足以让
// 上层放行。只改解码而忘了 claim 路径、或将来有人在 claim 里加别的约束，
// 本用例都会红。
func TestValidateManifestAuthority_SPIChannelDoesNotConflictWithItself(t *testing.T) {
	node := models.Node{
		NodeID:   "SIM-S3",
		Platform: "ESP32S3",
		// 上报 UART + SPI 用到的全部引脚（0 已被固件剔除，故从 1 开始）
		Capabilities: `{"buses":{"gpio":[
			{"pin":1},{"pin":2},{"pin":3},{"pin":4},{"pin":5},{"pin":6},{"pin":7},{"pin":8},
			{"pin":10},{"pin":11},{"pin":12},{"pin":13},{"pin":15},{"pin":16},{"pin":17}]}}`,
	}
	channels := []models.Channel{
		{ID: 6, BusType: "SPI", BusConfig: "0b00000000000b0d0c", Enabled: true},
	}

	got, err := validateManifestAuthority(node, channels, nil, nil)
	if err != nil {
		if strings.Contains(err.Error(), "channel 6 and channel 6") {
			t.Fatalf("SPI 通道与**自己**冲突：%v\n"+
				"这正是 2026-10-04 让 S3 的 SPI2 永远 success=false 的那个缺陷", err)
		}
		t.Fatalf("正常 SPI 通道被拒: %v", err)
	}
	if len(got) != 1 || got[0].ID != 6 {
		t.Fatalf("SPI 通道未被放行: %+v", got)
	}
}

// TestValidateManifestAuthority_RealConflictStillRejected 防止"去重去过头"。
//
// 去重只应消除**同一通道内**的重复；两个不同通道抢同一个引脚必须继续被拒，
// 否则会把一个真实的硬件冲突放行到设备上。
func TestValidateManifestAuthority_RealConflictStillRejected(t *testing.T) {
	node := models.Node{
		NodeID:   "SIM-S3",
		Platform: "ESP32S3",
		Capabilities: `{"buses":{"gpio":[
			{"pin":1},{"pin":2},{"pin":3},{"pin":4},{"pin":5},{"pin":6},{"pin":7},{"pin":8},
			{"pin":10},{"pin":11},{"pin":12},{"pin":13},{"pin":15},{"pin":16},{"pin":17}]}}`,
	}
	channels := []models.Channel{
		// SPI 用 11/13/12；下面这条 UART 故意也声明 11（tx）-> 真实跨通道冲突。
		{ID: 6, BusType: "SPI", BusConfig: "0b00000000000b0d0c", Enabled: true},
		{ID: 9, BusType: "UART", BusConfig: "0b050000258001", Enabled: true},
	}

	if _, err := validateManifestAuthority(node, channels, nil, nil); err == nil {
		t.Fatal("两个通道抢同一个引脚却被放行：去重把真实冲突也消掉了")
	} else if !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("跨通道冲突的错误信息未含 conflict: %v", err)
	}
}
