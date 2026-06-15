/**
 * @file hw_profile.h
 * @brief ESP32-C6 Hardware Profile - compile-time resource definitions
 *
 * Defines static hardware capabilities (UART, I2C, SPI, GPIO, ADC) and
 * provides hw_profile_build_report() to encode a binary ResourceReport
 * (MSG_RESOURCE_REPORT = 0x19) via frame_codec.
 */

#ifndef HW_PROFILE_H
#define HW_PROFILE_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>

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

/* === Platform constants === */
#define HW_PLATFORM_STRING  "ESP32C6"

/* === Hardware resource counts === */
#define HW_UART_COUNT   2
#define HW_I2C_COUNT    1
#define HW_SPI_COUNT    1
#define HW_GPIO_COUNT   8
#define HW_ADC_COUNT    3

/* Total hardware bus/pin resources (excludes config channels) */
#define HW_RESOURCE_COUNT  (HW_UART_COUNT + HW_I2C_COUNT + HW_SPI_COUNT + \
                            HW_GPIO_COUNT + HW_ADC_COUNT)

/* === Extern const arrays (defined in hw_profile.c) === */
extern const hw_uart_t hw_uarts[HW_UART_COUNT];
extern const hw_i2c_t  hw_i2cs[HW_I2C_COUNT];
extern const hw_spi_t  hw_spis[HW_SPI_COUNT];
extern const hw_gpio_t hw_gpios[HW_GPIO_COUNT];
extern const hw_adc_t  hw_adcs[HW_ADC_COUNT];

/* === Report builder === */

/**
 * @brief Build a binary ResourceReport frame (MSG_RESOURCE_REPORT = 0x19).
 *
 * Encodes the full hardware profile + enabled config channels into a
 * frame_codec binary frame ready for transmission.
 *
 * @param buf      Output buffer (caller-allocated).
 * @param sz       Capacity of buf in bytes.
 * @param out_len  On success, set to the encoded frame length.
 * @return true on success, false on buffer overflow or encoding error.
 */
bool hw_profile_build_report(uint8_t *buf, size_t sz, size_t *out_len);

#ifdef __cplusplus
}
#endif

#endif /* HW_PROFILE_H */
