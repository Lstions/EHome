/**
 * @file bus_dma.h
 * @brief Unified Bus DMA Engine — UART TX/RX separated, SPI/I2C transactional
 *
 * Design principle:
 *   UART is full-duplex at the hardware level — TX and RX are independent.
 *   cmd_task does TX (fire-and-forget), rx_task does RX (non-blocking poll).
 *   SPI and I2C are transactional — cmd_task does atomic write+read.
 *
 * Bus sharing:
 *   I2C: shared by (sda, scl) pins.  SPI: shared by (mosi, miso, sclk) pins.
 *   UART: each channel owns its port (no sharing needed).
 */

#ifndef BUS_DMA_H
#define BUS_DMA_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include "esp_err.h"
#include "driver/uart.h"
#include "driver/spi_master.h"
#include "driver/i2c_master.h"

#ifdef __cplusplus
extern "C" {
#endif

/* === Bus type identifiers === */
#define BUS_TYPE_UART  1
#define BUS_TYPE_I2C   2
#define BUS_TYPE_SPI   3

/* === Bus config helper === */

/**
 * @brief Extract DMA enabled flag from bus_config raw bytes.
 *
 * Bus config byte layouts:
 *   UART: [tx_pin, rx_pin, baud×4(BE)] + optional flags byte at offset 6
 *   SPI:  [cs, mode, freq×4(BE), MOSI, MISO, SCLK] + optional flags byte
 *   I2C:  [sda, scl, addr, freq×4(BE)] + optional flags byte at offset 7
 *
 * @return true if DMA is enabled, true if flags byte is missing (default)
 */
static inline bool bus_config_get_dma_enabled(uint8_t bus_type,
                                               const uint8_t *bus_config,
                                               size_t bus_config_len)
{
    size_t flags_offset = 0;
    size_t min_len = 0;

    switch (bus_type) {
    case BUS_TYPE_UART: flags_offset = 6; min_len = 7;  break;
    case BUS_TYPE_I2C:  flags_offset = 7; min_len = 8;  break;
    case BUS_TYPE_SPI:  flags_offset = 6; min_len = 7;  break;
    default: return true;
    }

    if (bus_config && bus_config_len >= min_len)
        return (bus_config[flags_offset] & 0x01) != 0;
    return true;  /* Flags byte missing, default to DMA enabled */
}

/* === Bus DMA context === */
typedef struct {
    uint8_t bus_type;
    bool dma_enabled;
    bool initialized;
    SemaphoreHandle_t tx_mutex;

    union {
        struct {
            uart_port_t port;
            uint32_t    baud;
            int         tx_pin;
            int         rx_pin;
            int32_t     turnaround_us;  /* P2-2: Turnaround delay in us. 0=auto (Modbus 3.5 char), -1=none (full duplex), >0=manual. Default: 0 */
        } uart;
        struct {
            spi_host_device_t  host;
            spi_device_handle_t dev;
            int         cs_pin;
            int         mosi_pin;
            int         miso_pin;
            int         sclk_pin;
            uint32_t    freq;
            uint8_t     mode;
        } spi;
        struct {
            i2c_master_bus_handle_t bus_handle;
            i2c_master_dev_handle_t dev_handle;
            uint8_t    addr;
            uint32_t   freq;
            int        sda_pin;
            int        scl_pin;
        } i2c;
    } cfg;
} bus_dma_ctx_t;

/* ==================================================================
 *  Lifecycle
 * ================================================================== */

esp_err_t bus_dma_init(bus_dma_ctx_t *ctx, uint8_t bus_type, bool dma_enabled,
                       const uint8_t *config, size_t config_len);
void bus_dma_deinit(bus_dma_ctx_t *ctx);

/* ==================================================================
 *  UART: independent TX / RX (full-duplex hardware)
 * ================================================================== */

/**
 * @brief Write data to UART TX line.  Fire-and-forget — does not wait for
 *        any response.  Returns after TX FIFO is drained.
 *
 * @return ESP_OK on success, ESP_ERR_INVALID_ARG if ctx is not UART
 */
esp_err_t bus_dma_write(bus_dma_ctx_t *ctx, const uint8_t *data, size_t len);

/**
 * @brief Read any available data from UART RX (DMA ring buffer or FIFO).
 *        Non-blocking — returns 0 immediately if no data is waiting.
 *
 * @return number of bytes read into buf (0 = nothing available)
 */
size_t bus_dma_read(bus_dma_ctx_t *ctx, uint8_t *buf, size_t buf_size);

/* ==================================================================
 *  SPI / I2C: transactional (write then read, atomic)
 * ================================================================== */

/**
 * @brief Execute an atomic write-then-read transaction on SPI or I2C.
 *        Not valid for UART (use bus_dma_write + bus_dma_read instead).
 *
 * @param ctx       Initialized SPI or I2C context
 * @param tx        Transmit data (NULL for read-only)
 * @param tx_len    Transmit length in bytes
 * @param rx        Receive buffer
 * @param rx_size   Receive buffer capacity
 * @param rx_len    [out] Actual bytes received
 *
 * @return ESP_OK on success
 */
esp_err_t bus_dma_transact(bus_dma_ctx_t *ctx,
                           const uint8_t *tx, size_t tx_len,
                           uint8_t *rx, size_t rx_size, size_t *rx_len);

/**
 * @brief Initialize UART0 boot mode manager.
 *
 * MUST be called BEFORE any UART0 driver installation.
 * Checks BOOT button (GPIO0) state:
 *   - If held: UART0 reserved for download, enters wait loop (LED fast blink)
 *   - If released: UART0 available for data use
 *
 * @return true  if UART0 is available for data use
 * @return false if UART0 is reserved for firmware download mode
 */
bool bus_dma_uart0_boot_init(void);

/**
 * @brief Enter UART0 firmware download mode.
 *
 * Pulls GPIO0 low, enables RTC GPIO hold so it persists across reset,
 * then calls esp_restart(). ROM bootloader detects GPIO0 low and enters
 * UART0 serial download mode automatically.
 *
 * This function does not return.
 */
void bus_dma_uart0_enter_download(void) __attribute__((noreturn));

/**
 * @brief Check if UART0 is available for data use.
 * @return true if UART0 can be used as data serial port
 */
bool bus_dma_uart0_is_available(void);

#ifdef __cplusplus
}
#endif

#endif /* BUS_DMA_H */
