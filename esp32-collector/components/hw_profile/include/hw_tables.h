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

typedef struct {
    const char *id;
    uint8_t     pin;
} hw_gpio_t;

typedef struct {
    const char *id;
    uint8_t     unit;
    uint8_t     channel;
    uint8_t     pin;
    uint8_t     max_bits;
} hw_adc_t;

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
  #define HW_DMA_COUNT    5  /* S3: 5 GDMA channels (CH0-4) */

  /* Reserved pins — must NOT be used for user peripherals */
  #define HW_RESERVED_USB_DN   19  /* USB_D- */
  #define HW_RESERVED_USB_DP   20  /* USB_D+ */
  #define HW_RESERVED_LED      48  /* RGB LED (WS2812) */

#elif defined(CONFIG_IDF_TARGET_ESP32C6)

  #define HW_PLATFORM_STRING  "ESP32C6"
  /* C6: 2 HP UARTs (DMA-capable) + 1 LP UART (no DMA, fixed pins, 16B FIFO), 1 I2C, 1 SPI */
  #define HW_UART_COUNT   3
  #define HW_I2C_COUNT    1
  #define HW_SPI_COUNT    1
  #define HW_GPIO_COUNT   8
  #define HW_ADC_COUNT    3
  #define HW_DMA_COUNT    3

  /* Reserved pins — must NOT be used for user peripherals */
  #define HW_RESERVED_USB_DN   12  /* USB_D- */
  #define HW_RESERVED_USB_DP   13  /* USB_D+ */
  #define HW_RESERVED_LED       8  /* RGB LED (WS2812) */

#else
  #error "Unsupported IDF target — add profile for this chip"
#endif

/* Total hardware bus/pin resources (excludes config channels) */
#define HW_RESOURCE_COUNT  (HW_UART_COUNT + HW_I2C_COUNT + HW_SPI_COUNT + \
                            HW_GPIO_COUNT + HW_ADC_COUNT + HW_DMA_COUNT)

/* === Extern const arrays (defined in hw_tables.c) === */
extern const hw_uart_t hw_uarts[HW_UART_COUNT];
extern const hw_i2c_t  hw_i2cs[HW_I2C_COUNT];
extern const hw_spi_t  hw_spis[HW_SPI_COUNT];
extern const hw_gpio_t hw_gpios[HW_GPIO_COUNT];
extern const hw_adc_t  hw_adcs[HW_ADC_COUNT];
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

#ifdef __cplusplus
}
#endif

#endif /* HW_TABLES_H */
