package api

import (
	"errors"
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

// TestValidateI2CBusConfig_RejectsShortValues 是 §197 真机缺陷的回归护栏。
//
// 缺陷（真机复现）：
//
//	后端在写路径上对 I2C 只要求 >= 2 字节（handler_device.go 的 channelRoutePins），
//	而**固件**要求 >= 7（bus_manager.c:582、bus_dma.c:1746）。
//	于是 2 字节的 I2C bus_config 被原样落库、原样下发 ⇒ 设备
//	  BUS_MGR: preinstall rejected by resource plan: ESP_ERR_INVALID_SIZE
//	⇒ **整份 manifest 被拒**（连同其它通道一起不装）⇒ ConfigResult success=0，
//	  而接口返回 201「创建成功」。
//
// 处置：入库时就拒绝"非空但不足 7 字节"的值（400），而不是下发时补。
// ⚠ 为什么不像 UART 那样补：I2C 缺的字节里含 **从机地址** ——
//
//	那是"跟谁说话"，编错了会去访问总线上另一个设备。
//
// 它凭什么会失败：把 validateI2CBusConfig 的 i2cBusConfigMinLen 改成 2，本用例立刻红。
func TestValidateI2CBusConfig_RejectsShortValues(t *testing.T) {
	long := "080900000186a0" // sda=8 scl=9 addr=0x00 freq=100000

	t.Run("空值放行（还没配总线参数是合法建通道状态）", func(t *testing.T) {
		ch := models.Channel{BusType: "I2C", BusConfig: ""}
		if err := validateI2CBusConfig(&ch); err != nil {
			t.Fatalf("空 bus_config 应放行（与 UART 一致），得到 %v", err)
		}
	})

	t.Run("2 字节拒绝（真机缺陷值）", func(t *testing.T) {
		ch := models.Channel{BusType: "I2C", BusConfig: "0809"}
		err := validateI2CBusConfig(&ch)
		if !errors.Is(err, errI2CBusConfigIncomplete) {
			t.Fatalf("2 字节应返回 errI2CBusConfigIncomplete，得到 %v", err)
		}
		// 报错必须写清正确长度与布局 —— 否则用户只知道"错了"、不知道该填什么。
		if !strings.Contains(err.Error(), "7") || !strings.Contains(err.Error(), "从机地址") {
			t.Errorf("错误信息应含正确长度(7)与布局说明(从机地址)，得到：%v", err)
		}
	})

	t.Run("6 字节仍拒绝（差一字节也不行）", func(t *testing.T) {
		ch := models.Channel{BusType: "I2C", BusConfig: "0809000186a0"}
		if err := validateI2CBusConfig(&ch); !errors.Is(err, errI2CBusConfigIncomplete) {
			t.Fatalf("6 字节应被拒（固件下限是 7），得到 %v", err)
		}
	})

	t.Run("7 字节放行", func(t *testing.T) {
		ch := models.Channel{BusType: "I2C", BusConfig: long}
		if err := validateI2CBusConfig(&ch); err != nil {
			t.Fatalf("7 字节应放行，得到 %v", err)
		}
	})

	t.Run("非 hex 拒绝", func(t *testing.T) {
		ch := models.Channel{BusType: "I2C", BusConfig: "zzzzzzzzzzzzzz"}
		if err := validateI2CBusConfig(&ch); !errors.Is(err, errI2CBusConfigIncomplete) {
			t.Fatalf("非 hex 应被拒，得到 %v", err)
		}
	})
}

// TestEnsureBusConfigExtras_DispatchesByBusType 锁住"分派"这一层。
//
// 为什么要有它：四条写 channels 的路径原本只调 ensureUARTBusConfig，
// I2C **整整一类**没有守卫（§197）。收敛成 ensureBusConfigExtras 后，
// 本用例保证"按类型分派"这件事本身不会退化回去。
//
// 它凭什么会失败：把 ensureBusConfigExtras 的 I2C 分支删掉，第二个子用例立刻红。
func TestEnsureBusConfigExtras_DispatchesByBusType(t *testing.T) {
	t.Run("I2C 短值经分派被拒", func(t *testing.T) {
		ch := models.Channel{BusType: "I2C", BusConfig: "0809"}
		if err := ensureBusConfigExtras(nil, &ch); !errors.Is(err, errI2CBusConfigIncomplete) {
			t.Fatalf("应经分派返回 I2C 错误，得到 %v", err)
		}
	})

	t.Run("未知总线类型不拦（无证据不改）", func(t *testing.T) {
		ch := models.Channel{BusType: "SPI", BusConfig: "00"}
		if err := ensureBusConfigExtras(nil, &ch); err != nil {
			t.Fatalf("SPI 本轮未加判据，应放行，得到 %v", err)
		}
	})

	t.Run("大小写与数字别名都要认", func(t *testing.T) {
		for _, bt := range []string{"i2c", "I2C", "2"} {
			ch := models.Channel{BusType: bt, BusConfig: "0809"}
			if err := ensureBusConfigExtras(nil, &ch); !errors.Is(err, errI2CBusConfigIncomplete) {
				t.Errorf("bus_type=%q 应被识别为 I2C 并拒绝，得到 %v", bt, err)
			}
		}
	})
}
