/**
 * @file hw_tables.h
 * @brief Static hardware resource tables — extracted from hw_profile for
 *        separation of data (this file) from encoding logic (hw_profile).
 *
 * Supports ESP32-S3 and ESP32-C6 via CONFIG_IDF_TARGET_* conditionals.
 */

#ifndef HW_TABLES_H
#define HW_TABLES_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include "dma_pool.h"  /* for hw_dma_t */
#include "driver/uart.h"  /* P3-7: for uart_port_t in hw_derive_uart_port */
#include "driver/spi_master.h"
#include "driver/i2c_master.h"

#ifdef __cplusplus
extern "C" {
#endif

/* === Hardware resource descriptors === */

typedef struct {
    const char *id;
    uint8_t     port;
    uint8_t     default_tx_pin;
    uint8_t     default_rx_pin;
    uint32_t    max_baud;
    uint8_t     flags;           /* bit0 = dma_supported */
} hw_uart_t;

/* UART flags */
#define HW_UART_FLAG_DMA       0x01   /* DMA supported */
#define HW_UART_FLAG_LP_UART   0x02   /* Low-power UART (fixed pins, no DMA, small FIFO) */

#define hw_uart_is_lp(u)  ((u)->flags & HW_UART_FLAG_LP_UART)

typedef struct {
    const char *id;
    uint8_t     port;
    uint8_t     default_sda;
    uint8_t     default_scl;
    uint32_t    max_freq_hz;
    uint8_t     flags;           /* bit0 = dma_supported */
} hw_i2c_t;

typedef struct {
    const char *id;
    uint8_t     port;
    uint8_t     default_mosi;
    uint8_t     default_miso;
    uint8_t     default_sclk;
    uint8_t     default_cs;
    uint32_t    max_freq_hz;
    uint8_t     flags;           /* bit0 = dma_supported */
} hw_spi_t;

/* GPIO flags */
#define HW_GPIO_FLAG_RESERVED  0x01  /* 不得分配给用户外设（strap/按键/调试占用） */

typedef struct {
    const char *id;
    uint8_t     pin;
    uint8_t     flags;  /* bit0 = reserved（见 HW_GPIO_FLAG_RESERVED） */
} hw_gpio_t;

/* 引脚是否禁止分配给用户外设（GPIO/PWM 等）。
 *
 * 为什么需要它：2026-10-04 现场事故 —— 压力测试把 PWM 配到 S3 的 GPIO0，
 * 而 GPIO0 正是 BOOT 按键引脚。PWM 以 3% 占空比把它拉低 97% 的时间，
 * factory_reset_task 轮询到低电平并走满 5s 长按判定，于是每次上电约 8.8s
 * 就擦一次 NVS 并重启，设备陷入"恢复出厂→重启→重连→再恢复出厂"死循环，
 * 表现为红/蓝/紫灯交替闪烁、配置永远 success=false。
 *
 * 这类引脚即使在用户视角"看起来空着"，也不该被业务配置驱动：
 *   - BOOT/strap 引脚决定启动模式，且被按键轮询逻辑占用；
 *   - USB D+/D- 决定能否被主机枚举（占用即失联）；
 *   - RGB LED 引脚被 LED 驱动独占。
 * 因此标注为 reserved 的引脚一律不参与资源上报、也不接受下发配置。 */
static inline bool hw_gpio_is_reserved(const hw_gpio_t *g)
{
    return (g->flags & HW_GPIO_FLAG_RESERVED) != 0;
}

typedef struct {
    const char *id;
    uint8_t     unit;
    uint8_t     channel;
    uint8_t     pin;
    uint8_t     max_bits;
} hw_adc_t;

typedef struct {
    const char *id;
    uint8_t     channel;
    uint8_t     timer_count;
    uint8_t     max_resolution_bits;
} hw_pwm_t;

/* hw_dma_t is defined in dma_pool.h (included above) */

/* === Platform-specific constants === */

#ifdef CONFIG_IDF_TARGET_ESP32S3

  #define HW_PLATFORM_STRING  "ESP32S3"
  /* S3: 3 UARTs (all DMA-capable), 2 I2C, 2 SPI */
  #define HW_UART_COUNT   3
  #define HW_I2C_COUNT    2
  #define HW_SPI_COUNT    2
  #define HW_GPIO_COUNT   12
  #define HW_ADC_COUNT    5
  #define HW_PWM_COUNT    8
  #define HW_DMA_COUNT    5  /* S3: 5 GDMA channels (CH0-4) */

  /* Reserved pins — must NOT be used for user peripherals */
  #define HW_RESERVED_USB_DN   19  /* USB_D- */
  #define HW_RESERVED_USB_DP   20  /* USB_D+ */
  #define HW_RESERVED_LED      48  /* RGB LED (WS2812) */
  /* S3 BOOT 按键 / strapping 引脚：rom 下载模式判定（bus_dma.c 的
   * BOOT_STRAP_GPIO）与 factory_reset 长按轮询（factory_reset.c 的
   * BOOT_BUTTON_GPIO）都用它。任何把它拉低的输出配置都会伪装成
   * "按键长按"，触发 NVS 擦除 + 重启。 */
  #define HW_RESERVED_BOOT      0  /* BOOT 按键 / strapping (S3) */

#elif defined(CONFIG_IDF_TARGET_ESP32C6)

  #define HW_PLATFORM_STRING  "ESP32C6"
  /* C6 runtime owns workers for two HP UARTs. LP_UART0 is not advertised
   * until it has a dedicated command queue and worker route. */
  #define HW_UART_COUNT   2
  #define HW_I2C_COUNT    1
  #define HW_SPI_COUNT    1
  #define HW_GPIO_COUNT   8
  #define HW_ADC_COUNT    3
  #define HW_PWM_COUNT    6
  #define HW_DMA_COUNT    3

  /* Reserved pins — must NOT be used for user peripherals */
  #define HW_RESERVED_USB_DN   12  /* USB_D- */
  #define HW_RESERVED_USB_DP   13  /* USB_D+ */
  #define HW_RESERVED_LED       8  /* RGB LED (WS2812) */
  /* C6 BOOT 按键 / strapping 引脚（factory_reset.c 的 BOOT_BUTTON_GPIO=9）。
   * 注意 C6 的 GPIO8 是 RGB LED（由 LED 驱动占用），GPIO9 才是 BOOT 按键。 */
  #define HW_RESERVED_BOOT      9  /* BOOT 按键 (C6) */

#else
  #error "Unsupported IDF target — add profile for this chip"
#endif

/* Total hardware bus/pin resources (excludes config channels) */
#define HW_RESOURCE_COUNT  (HW_UART_COUNT + HW_I2C_COUNT + HW_SPI_COUNT + \
                            HW_GPIO_COUNT + HW_ADC_COUNT + HW_PWM_COUNT + \
                            HW_DMA_COUNT)

/* === Extern const arrays (defined in hw_tables.c) === */
extern const hw_uart_t hw_uarts[HW_UART_COUNT];
extern const hw_i2c_t  hw_i2cs[HW_I2C_COUNT];
extern const hw_spi_t  hw_spis[HW_SPI_COUNT];
extern const hw_gpio_t hw_gpios[HW_GPIO_COUNT];
extern const hw_adc_t  hw_adcs[HW_ADC_COUNT];
extern const hw_pwm_t  hw_pwms[HW_PWM_COUNT];
extern const hw_dma_t  hw_dmas[HW_DMA_COUNT];

/* === P3-7: Common UART port derivation === */

/**
 * @brief Derive uart_port_t from TX/RX pin numbers via hw_uarts lookup table.
 *
 * Iterates hw_uarts[] to find a matching (tx_pin, rx_pin) pair and returns
 * the corresponding port number.  This eliminates the duplicate derive_uart_port
 * functions that were in both scheduler.c and bus_manager.c.
 *
 * @param tx_pin  TX pin number (bus_config[0] for UART channels)
 * @param rx_pin  RX pin number (bus_config[1] for UART channels)
 * @return        Matching uart_port_t, or default_port if no match found
 */
uart_port_t hw_derive_uart_port(int tx_pin, int rx_pin, uart_port_t default_port);

/** Resolve SPI/I2C controller from the configured bus pins. */
spi_host_device_t hw_derive_spi_host(int mosi_pin, int miso_pin, int sclk_pin,
                                     spi_host_device_t default_host);
i2c_port_t hw_derive_i2c_port(int sda_pin, int scl_pin, i2c_port_t default_port);

#ifdef __cplusplus
}
#endif

#endif /* HW_TABLES_H */
