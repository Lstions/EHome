package nodemgr

import (
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

// TestDecodeManifestTransportPins_RealS3SPIConfig 用**产线上的真实字节**做锚点。
//
// 值取自 30EDA0A9A808（S3）channel 6 的实际 bus_config：
//
//	0b00000000000b0d0c
//
// 即 byte0=0x0b(11)、byte6=0x0b(11)、byte7=0x0d(13)、byte8=0x0c(12)，
// 对应 SPI2 的 mosi=11 / mosi=11 / miso=13 / sclk=12 —— MOSI 出现两次。
//
// 为什么单独立一条：上面那条用例用的是同一个值，但它的注释只说"与 S3 实机一致"。
// 这里把"这个字符串就是从生产库读出来的"固定下来，将来若有人改了用例里的值，
// 这条会提醒他产线上真正跑的是什么。测试数据的来源必须可追溯。
func TestDecodeManifestTransportPins_RealS3SPIConfig(t *testing.T) {
	const prodBusConfig = "0b00000000000b0d0c" // 生产库原值
	ch := models.Channel{ID: 6, BusType: "SPI", BusConfig: prodBusConfig, Enabled: true}

	pins, err := decodeManifestTransportPins(ch, "SPI")
	if err != nil {
		t.Fatalf("解码产线真实配置失败: %v", err)
	}

	// 期望恰好 3 个不同引脚：MOSI=11, MISO=13, SCLK=12
	want := map[int]bool{11: true, 13: true, 12: true}
	if len(pins) != len(want) {
		t.Fatalf("产线配置解出 %d 个引脚 %v，期望恰好 %d 个（MOSI/MISO/SCLK 各一次）",
			len(pins), pins, len(want))
	}
	for _, p := range pins {
		if !want[p] {
			t.Errorf("解出意外引脚 %d（期望 11/13/12）", p)
		}
	}

	// 并串一次真正的权威校验，确认产线配置现在能被放行。
	node := models.Node{NodeID: "30EDA0A9A808", Platform: "ESP32S3",
		Capabilities: `{"buses":{"gpio":[{"pin":11},{"pin":12},{"pin":13},{"pin":10}]}}`}
	if _, err := validateManifestAuthority(node, []models.Channel{ch}, nil, nil); err != nil {
		if strings.Contains(err.Error(), "channel 6 and channel 6") {
			t.Fatalf("产线真实配置仍与自身冲突: %v", err)
		}
		t.Fatalf("产线真实配置被拒: %v", err)
	}
}
