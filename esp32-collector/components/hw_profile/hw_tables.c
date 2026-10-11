/**
 * @file hw_tables.c
 * @brief Static hardware resource tables — per-target pin tables
 *
 * Extracted from hw_profile.c to separate data tables from encoding logic.
 * Supports ESP32-S3 and ESP32-C6 via CONFIG_IDF_TARGET_* conditionals.
 *
 * S3: 3 UART (all DMA), 2 I2C, 2 SPI, 12 GPIO, 5 ADC, 5 GDMA
 * C6: 2 HP UART (DMA) + 1 LP UART (no DMA), 1 I2C, 1 SPI, 8 GPIO, 3 ADC, 3 GDMA
 */

#include "hw_tables.h"

/* Compile-time resource inventory contract for every supported target. */
#ifdef CONFIG_IDF_TARGET_ESP32C6
_Static_assert(HW_GPIO_COUNT == 8, "C6 GPIO resource count mismatch");
_Static_assert(HW_PWM_COUNT == 6, "C6 PWM resource count mismatch");
#elif defined(CONFIG_IDF_TARGET_ESP32S3)
_Static_assert(HW_GPIO_COUNT == 12, "S3 GPIO resource count mismatch");
_Static_assert(HW_PWM_COUNT == 8, "S3 PWM resource count mismatch");
#endif

/* === P3-7: Common UART port derivation === */

uart_port_t hw_derive_uart_port(int tx_pin, int rx_pin, uart_port_t default_port)
{
    for (int i = 0; i < HW_UART_COUNT; i++) {
        if (hw_uarts[i].default_tx_pin == tx_pin &&
            hw_uarts[i].default_rx_pin == rx_pin) {
            return (uart_port_t)hw_uarts[i].port;
        }
    }
    return default_port;
}

spi_host_device_t hw_derive_spi_host(int mosi_pin, int miso_pin, int sclk_pin,
                                     spi_host_device_t default_host)
{
    for (int i = 0; i < HW_SPI_COUNT; i++) {
        if (hw_spis[i].default_mosi == mosi_pin &&
            hw_spis[i].default_miso == miso_pin &&
            hw_spis[i].default_sclk == sclk_pin)
            return (spi_host_device_t)hw_spis[i].port;
    }
    return default_host;
}

i2c_port_t hw_derive_i2c_port(int sda_pin, int scl_pin, i2c_port_t default_port)
{
    for (int i = 0; i < HW_I2C_COUNT; i++) {
        if (hw_i2cs[i].default_sda == sda_pin &&
            hw_i2cs[i].default_scl == scl_pin)
            return (i2c_port_t)hw_i2cs[i].port;
    }
    return default_port;
}

/* ================================================================
 *  Static Hardware Profile — per-target pin tables
 *
 *  RESERVED PINS (do NOT assign to user peripherals):
 *    S3: GPIO19=USB_D-, GPIO20=USB_D+, GPIO48=RGB LED
 *    C6: GPIO12=USB_D-, GPIO13=USB_D+, GPIO8=RGB LED
 *
 *  S3 UART0: TX=43 RX=44 (ROM bootloader download port)
 *  S3 UART1: TX=4  RX=5  (general purpose, avoids USB 19/20)
 *  S3 UART2: TX=1  RX=2  (general purpose)
 *  C6 UART0: TX=16 RX=17 (ROM bootloader download port)
 *  C6 UART1: TX=20 RX=21 (general purpose, avoids USB 12/13)
 *  C6 LP_UART0: TX=LP_GPIO5 RX=LP_GPIO4 (low-power, fixed pins, no DMA, 16B FIFO)
 *
 *  All HP UARTs on both chips support DMA (flags = 0x01).
 *  LP_UART has flags = 0x02 (bit1 = is_lp_uart, no DMA).
 * ================================================================ */

#ifdef CONFIG_IDF_TARGET_ESP32S3

/* S3 USB pins: GPIO19=USB_D-, GPIO20=USB_D+ (RESERVED for USB Serial/JTAG) */
const hw_uart_t hw_uarts[HW_UART_COUNT] = {
    { .id = "UART0", .port = 0, .default_tx_pin = 43, .default_rx_pin = 44,
      .max_baud = 5000000, .flags = 0x01 },  /* DMA, ROM download port */
    { .id = "UART1", .port = 1, .default_tx_pin = 4,  .default_rx_pin = 5,
      .max_baud = 5000000, .flags = 0x01 },  /* DMA, avoids USB pins 19/20 */
    { .id = "UART2", .port = 2, .default_tx_pin = 1,  .default_rx_pin = 2,
      .max_baud = 5000000, .flags = 0x01 },  /* DMA, general purpose */
};

const hw_i2c_t hw_i2cs[HW_I2C_COUNT] = {
    { .id = "I2C0", .port = 0, .default_sda = 8,  .default_scl = 9,
      .max_freq_hz = 1000000, .flags = 0x01 },
    /* ⚠⚠ 2026-10-08（§197 真机发现）：default_scl 原为 **48**，而 GPIO48 是本型号
     * 的 **RGB LED 保留脚** —— 见本文件 :62 自己写的
     *   "S3: GPIO19=USB_D-, GPIO20=USB_D+, **GPIO48=RGB LED**"
     * 与 hw_tables.h 的 HW_RESERVED_LED=48；且 main.c 的 rgb_led_init(48) **真的在驱动它**。
     *
     * ⇒ 后果（真机复现）：用户按默认值建一条 I2C1 通道 ⇒ 后端下发 manifest ⇒ 设备
     *     BUS_MGR: preinstall rejected by resource plan: ESP_ERR_INVALID_ARG
     *     ⇒ **整份 manifest 被拒**（连同 3 条 UART 一起不装）⇒ ConfigResult success=0，
     *       而操作员在界面上看到的是「通道创建成功」。
     *   ⇒ 这正是本文件 :104-107 记过的那族缺陷（GPIO0 是 BOOT 脚，PWM 配到 GPIO0
     *     导致设备每 8.8 秒恢复出厂一次）——**同一个坑，换个引脚又来一次**。
     *
     * 改成 46：0-48 内除已用 {1,2,4,5,8,9,10,11,12,13,34,35,36,37,43,44,47} 与
     * 保留 {0,19,20,48} 之外的第一个空闲脚，且与 sda=47 相邻。
     * ⚠ **待硬件确认**：若实机把 I2C1 的 SCL 实际接在 48 上，那它与 LED 是物理冲突，
     *   正解是**不把 I2C1 报进资源表** —— 那需要硬件信息，不在本轮范围。
     * ⇒ 已加门禁 tools/check_hw_bus_defaults.py 防复发（同一文件内相隔 29 行也会写错）。 */
    { .id = "I2C1", .port = 1, .default_sda = 47, .default_scl = 46,
      .max_freq_hz = 1000000, .flags = 0x01 },
};

const hw_spi_t hw_spis[HW_SPI_COUNT] = {
    { .id = "SPI2", .port = 2, .default_mosi = 11, .default_miso = 13,
      .default_sclk = 12, .default_cs = 10, .max_freq_hz = 80000000,
      .flags = 0x01 },
    { .id = "SPI3", .port = 3, .default_mosi = 35, .default_miso = 37,
      .default_sclk = 36, .default_cs = 34, .max_freq_hz = 80000000,
      .flags = 0x01 },
};

/* GPIO0 标 reserved：它是 BOOT 按键 / strapping 引脚（见 hw_tables.h 的
 * hw_gpio_is_reserved 注释，2026-10-04 PWM 配到 GPIO0 导致设备每 8.8s
 * 恢复出厂一次的现场事故）。reserved 引脚不进资源上报，服务端因此拿不到
 * 它，用户也就配不上去 —— 这是"配不上"的第一道闸。 */
const hw_gpio_t hw_gpios[HW_GPIO_COUNT] = {
    { .id = "GPIO0",  .pin = 0,  .flags = HW_GPIO_FLAG_RESERVED },
    { .id = "GPIO1",  .pin = 1  },
    { .id = "GPIO2",  .pin = 2  },
    { .id = "GPIO3",  .pin = 3  },
    { .id = "GPIO4",  .pin = 4  },
    { .id = "GPIO5",  .pin = 5  },
    { .id = "GPIO6",  .pin = 6  },
    { .id = "GPIO7",  .pin = 7  },
    { .id = "GPIO8",  .pin = 8  },
    { .id = "GPIO15", .pin = 15 },
    { .id = "GPIO16", .pin = 16 },
    { .id = "GPIO17", .pin = 17 },
};

const hw_adc_t hw_adcs[HW_ADC_COUNT] = {
    { .id = "ADC1_CH0", .unit = 1, .channel = 0, .pin = 1,  .max_bits = 12 },
    { .id = "ADC1_CH1", .unit = 1, .channel = 1, .pin = 2,  .max_bits = 12 },
    { .id = "ADC1_CH2", .unit = 1, .channel = 2, .pin = 3,  .max_bits = 12 },
    { .id = "ADC1_CH3", .unit = 1, .channel = 3, .pin = 4,  .max_bits = 12 },
    { .id = "ADC1_CH4", .unit = 1, .channel = 4, .pin = 5,  .max_bits = 12 },
};

const hw_pwm_t hw_pwms[HW_PWM_COUNT] = {
    { .id = "PWM0", .channel = 0, .timer_count = 4, .max_resolution_bits = 14 },
    { .id = "PWM1", .channel = 1, .timer_count = 4, .max_resolution_bits = 14 },
    { .id = "PWM2", .channel = 2, .timer_count = 4, .max_resolution_bits = 14 },
    { .id = "PWM3", .channel = 3, .timer_count = 4, .max_resolution_bits = 14 },
    { .id = "PWM4", .channel = 4, .timer_count = 4, .max_resolution_bits = 14 },
    { .id = "PWM5", .channel = 5, .timer_count = 4, .max_resolution_bits = 14 },
    { .id = "PWM6", .channel = 6, .timer_count = 4, .max_resolution_bits = 14 },
    { .id = "PWM7", .channel = 7, .timer_count = 4, .max_resolution_bits = 14 },
};

/* S3: 5 GDMA channels (CH0-4), all general purpose TX+RX.
 *
 * ⚠⚠ 2026-10-09（用户明确指正）：**UART 侧只有 1 条可用**。
 *
 * 用户原话："C6 S3的所有UART同时都只有有一个能用DMA！！！"
 *
 * 硬件事实（与 C6 同源，S3 也只有一个 UHCI 外设）：
 *   S3 soc_caps.h:  #define SOC_UHCI_SUPPORTED 1
 *   uhci_ll.h:80-84 uhci_ll_attach_uart_port(hw, uart_num):
 *                     hw->conf0.uart0_ce = (uart_num == 0) ? 1 : 0;
 *                     hw->conf0.uart1_ce = (uart_num == 1) ? 1 : 0;
 *                     hw->conf0.uart2_ce = (uart_num == 2) ? 1 : 0;
 *   ⇒ 三个 ce 位**只有一个能为 1** ⇒ 同一时刻只有一个 UART 能接入 UHCI DMA。
 *     后 attach 的会把前一个的 ce 清 0 ⇒ 前者 DMA **静默失效**（不报错）。
 *
 * ⚠ 我曾在 §207.3 撤回这条判断（依据是 DMA 设计文档 v2.0 §1.1 把 S3 写成
 *   "CH0-4 通用"、未提 UHCI 单槽）。**用户指正后确认：文档那一行是错的，
 *   我最初的判断（§206.2）才对，撤回是过度自我怀疑。**
 *
 * 因此与 C6 用**同一套建模**：只有 1 条通道标 UART 兼容，让 dma_pool 自然
 * 强制"两个 UART 不能同时拿到 DMA"，而不是在运行期静默抢占。
 *
 * CH0 = UART|SPI（与 C6 的 CH1 同角色）；CH1-CH4 = SPI only。
 * ⚠ 这会让"3 UART 各拿一条"不再发生（§201/§202 压测日志里的
 *   "Alloc GDMA_CH0/1/2 -> uart/UART0/1/2" 将成为不可能）。
 * ⚠ I2C 本就不支持 DMA（docs/设计/DMA资源管理设计.md §1.1），保持不含 I2C 位。 */
const hw_dma_t hw_dmas[HW_DMA_COUNT] = {
    { .dma_id = 0, .name = "GDMA_CH0", .dma_type = 0,
      .capabilities = 0x03, .max_burst = 4095, .compatible_bus = 0x05 },  /* UART|SPI */
    { .dma_id = 1, .name = "GDMA_CH1", .dma_type = 0,
      .capabilities = 0x03, .max_burst = 4095, .compatible_bus = 0x04 },  /* SPI only */
    { .dma_id = 2, .name = "GDMA_CH2", .dma_type = 0,
      .capabilities = 0x03, .max_burst = 4095, .compatible_bus = 0x04 },  /* SPI only */
    { .dma_id = 3, .name = "GDMA_CH3", .dma_type = 0,
      .capabilities = 0x03, .max_burst = 4095, .compatible_bus = 0x04 },  /* SPI only */
    { .dma_id = 4, .name = "GDMA_CH4", .dma_type = 0,
      .capabilities = 0x03, .max_burst = 4095, .compatible_bus = 0x04 },  /* SPI only */
};

#elif defined(CONFIG_IDF_TARGET_ESP32C6)

/* C6 USB pins: GPIO12=USB_D-, GPIO13=USB_D+ (RESERVED for USB Serial/JTAG) */
const hw_uart_t hw_uarts[HW_UART_COUNT] = {
    { .id = "UART0", .port = 0, .default_tx_pin = 16, .default_rx_pin = 17,
      .max_baud = 5000000, .flags = 0x01 },  /* DMA, ROM download port */
    { .id = "UART1", .port = 1, .default_tx_pin = 20, .default_rx_pin = 21,
      .max_baud = 5000000, .flags = 0x01 },  /* DMA, avoids USB pins 12/13 */
};

const hw_i2c_t hw_i2cs[HW_I2C_COUNT] = {
    { .id = "I2C0", .port = 0, .default_sda = 21, .default_scl = 22,
      .max_freq_hz = 1000000, .flags = 0x00 },  /* C6 I2C: no DMA support */
};

const hw_spi_t hw_spis[HW_SPI_COUNT] = {
    { .id = "SPI2", .port = 2, .default_mosi = 23, .default_miso = 19,
      .default_sclk = 18, .default_cs = 5, .max_freq_hz = 40000000,
      .flags = 0x01 },
};

const hw_gpio_t hw_gpios[HW_GPIO_COUNT] = {
    { .id = "GPIO0", .pin = 0 },
    { .id = "GPIO1", .pin = 1 },
    { .id = "GPIO2", .pin = 2 },
    { .id = "GPIO3", .pin = 3 },
    { .id = "GPIO4", .pin = 4 },
    { .id = "GPIO5", .pin = 5 },
    { .id = "GPIO6", .pin = 6 },
    { .id = "GPIO7", .pin = 7 },
};

const hw_adc_t hw_adcs[HW_ADC_COUNT] = {
    { .id = "ADC1_CH0", .unit = 1, .channel = 0, .pin = 0, .max_bits = 12 },
    { .id = "ADC1_CH1", .unit = 1, .channel = 1, .pin = 1, .max_bits = 12 },
    { .id = "ADC1_CH2", .unit = 1, .channel = 2, .pin = 2, .max_bits = 12 },
};

const hw_pwm_t hw_pwms[HW_PWM_COUNT] = {
    { .id = "PWM0", .channel = 0, .timer_count = 4, .max_resolution_bits = 20 },
    { .id = "PWM1", .channel = 1, .timer_count = 4, .max_resolution_bits = 20 },
    { .id = "PWM2", .channel = 2, .timer_count = 4, .max_resolution_bits = 20 },
    { .id = "PWM3", .channel = 3, .timer_count = 4, .max_resolution_bits = 20 },
    { .id = "PWM4", .channel = 4, .timer_count = 4, .max_resolution_bits = 20 },
    { .id = "PWM5", .channel = 5, .timer_count = 4, .max_resolution_bits = 20 },
};

/* C6: 3 GDMA channel pairs (TX+RX).  Only CH1 is UART-capable because
 * UART0/UART1 share a single UHCI interface on the GDMA peri-select matrix.
 *
 * Hardware reference: ESP32-C6 TRM v1.2, Chapter 4, pp. 122-123
 * - GDMA has 6 independent channels (3 TX + 3 RX)
 * - Peripherals: SPI2, UHCI(UART0/UART1), I2S, AES, SHA, ADC, PARLIO
 * - UHCI occupies ONE peri-select slot — at most one TX and one RX channel
 *   can connect to UHCI at any time
 * - Each TX/RX channel independently selects a peripheral via Peri Select
 *
 * We model the 6 physical channels as 3 pairs (TXn+RXn).  Only pair 1
 * (CH1) is marked UART-compatible so the dma_pool naturally enforces
 * the hardware constraint: two UARTs cannot both get DMA.
 *
 * CH0 and CH2 are reserved for SPI and other peripherals. */
const hw_dma_t hw_dmas[HW_DMA_COUNT] = {
    { .dma_id = 0, .name = "GDMA_CH0", .dma_type = 0,
      .capabilities = 0x03, .max_burst = 4095, .compatible_bus = 0x04 },  /* SPI only */
    { .dma_id = 1, .name = "GDMA_CH1", .dma_type = 0,
      .capabilities = 0x03, .max_burst = 4095, .compatible_bus = 0x05 },  /* UART|SPI */
    { .dma_id = 2, .name = "GDMA_CH2", .dma_type = 0,
      .capabilities = 0x03, .max_burst = 4095, .compatible_bus = 0x04 },  /* SPI only */
};

#else
  #error "Unsupported IDF target"
#endif
