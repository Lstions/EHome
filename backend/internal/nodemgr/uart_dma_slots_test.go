package nodemgr

import (
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

// §212 用户要求（两层设计的第一层）：
//
//	"用户手动开两条 UART DMA：ESP32应该直接报错，前端显示错误，
//	 DMA 分不到降级为提示"
//
// 本测试锁住第一层：**显式**把多条 UART 的 DMA 打开时，manifest 在**下发前**
// 就被拒，且错误信息可读（前端要原样显示）。
//
// 硬件依据：S3 与 C6 的 UART 共用一个 UHCI 接口，同时只有 1 个能用 DMA。
//
//	S3/C6 soc_caps.h: SOC_UHCI_SUPPORTED 1
//	uhci_ll.h:80-84   uhci_ll_attach_uart_port 的三个 uartN_ce 位只有一个能为 1
func TestValidateUARTDMASlots(t *testing.T) {
	uart := func(id string, dma bool) models.Channel {
		return models.Channel{HardwareID: id, BusType: "UART", Enabled: true, DmaEnabled: dma}
	}

	t.Run("0 条开 DMA 通过", func(t *testing.T) {
		if err := validateUARTDMASlots([]models.Channel{uart("UART0", false), uart("UART1", false)}); err != nil {
			t.Fatalf("默认全关应当通过，得到 %v", err)
		}
	})

	t.Run("1 条开 DMA 通过（正好用满槽位）", func(t *testing.T) {
		if err := validateUARTDMASlots([]models.Channel{uart("UART0", true), uart("UART1", false)}); err != nil {
			t.Fatalf("1 条开 DMA 应当通过，得到 %v", err)
		}
	})

	t.Run("2 条开 DMA 必须被拒（用户要的硬报错）", func(t *testing.T) {
		err := validateUARTDMASlots([]models.Channel{uart("UART0", true), uart("UART1", true)})
		if err == nil {
			t.Fatal("2 条 UART 开 DMA 必须被拒（硬件只有 1 个 UART DMA 槽位）")
		}
		// 错误信息必须可读：前端要原样显示给用户
		for _, want := range []string{"UART DMA 槽位不足", "UART0", "UART1", "只保留 1 条"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("错误信息应包含 %q，实际：%v", want, err)
			}
		}
	})

	t.Run("3 条开 DMA 必须被拒", func(t *testing.T) {
		err := validateUARTDMASlots([]models.Channel{uart("UART0", true), uart("UART1", true), uart("UART2", true)})
		if err == nil {
			t.Fatal("3 条 UART 开 DMA 必须被拒")
		}
	})

	t.Run("未启用的通道不计入（禁用的不该占槽位）", func(t *testing.T) {
		disabled := uart("UART1", true)
		disabled.Enabled = false
		if err := validateUARTDMASlots([]models.Channel{uart("UART0", true), disabled}); err != nil {
			t.Fatalf("禁用通道不应计入，得到 %v", err)
		}
	})

	t.Run("非 UART 总线不计入（SPI/I2C 有自己的 DMA 资源）", func(t *testing.T) {
		spi := models.Channel{HardwareID: "SPI2", BusType: "SPI", Enabled: true, DmaEnabled: true}
		i2c := models.Channel{HardwareID: "I2C0", BusType: "I2C", Enabled: true, DmaEnabled: true}
		if err := validateUARTDMASlots([]models.Channel{uart("UART0", true), spi, i2c}); err != nil {
			t.Fatalf("SPI/I2C 不应计入 UART 槽位，得到 %v", err)
		}
	})

	t.Run("bus_type 大小写不敏感", func(t *testing.T) {
		lower := models.Channel{HardwareID: "UART1", BusType: "uart", Enabled: true, DmaEnabled: true}
		if err := validateUARTDMASlots([]models.Channel{uart("UART0", true), lower}); err == nil {
			t.Fatal("bus_type=uart（小写）也必须被识别为 UART")
		}
	})
}
