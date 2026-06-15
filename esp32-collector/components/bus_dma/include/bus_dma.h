/**
 * @file bus_dma.h
 * @brief Unified Bus DMA Engine — supports UART, SPI, I2C with dynamic DMA on/off
 *
 * Replaces the uart_dma component with a multi-bus abstraction.
 * Each bus_dma_ctx_t instance manages one physical bus channel.
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

/* === Bus DMA context === */
typedef struct {
    uint8_t  bus_type;
    bool     dma_enabled;
    bool     initialized;
    SemaphoreHandle_t mutex;

    union {
        struct {
            uart_port_t port;
            uint32_t    baud;
            int         tx_pin;
            int         rx_pin;
        } uart;
        struct {
            spi_host_device_t  host;
            spi_device_handle_t dev;
            int         cs_pin;
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

/**
 * @brief Initialize a bus DMA context.
 *
 * @param ctx         Context to initialize (caller-allocated)
 * @param bus_type    BUS_TYPE_UART / BUS_TYPE_I2C / BUS_TYPE_SPI
 * @param dma_enabled true = use DMA-backed APIs, false = polled/legacy
 * @param config      Bus-specific configuration bytes
 * @param config_len  Length of config buffer
 *
 * Config byte layouts:
 *   UART: [tx_pin, rx_pin, baud×4(BE)] + optional flags byte at offset 6
 *   SPI:  [cs_pin, mode, freq×4(BE)]   + optional flags byte at offset 6
 *   I2C:  [sda, scl, addr, freq×4(BE)] + optional flags byte at offset 7
 *
 * @return ESP_OK on success
 */
esp_err_t bus_dma_init(bus_dma_ctx_t *ctx, uint8_t bus_type, bool dma_enabled,
                       const uint8_t *config, size_t config_len);

/**
 * @brief Execute a bus transaction (write then read).
 *
 * @param ctx         Initialized context
 * @param tx          Transmit data (NULL for read-only)
 * @param tx_len      Transmit length
 * @param timeout_ms  RX timeout in milliseconds
 * @param rx          Receive buffer
 * @param rx_size     Receive buffer capacity
 * @param rx_len      [out] Actual bytes received
 *
 * @return ESP_OK on success, ESP_ERR_TIMEOUT if no response
 */
esp_err_t bus_dma_transact(bus_dma_ctx_t *ctx,
                           const uint8_t *tx, size_t tx_len,
                           uint32_t timeout_ms,
                           uint8_t *rx, size_t rx_size, size_t *rx_len);

/**
 * @brief Deinitialize and release bus resources.
 */
void bus_dma_deinit(bus_dma_ctx_t *ctx);

#ifdef __cplusplus
}
#endif

#endif /* BUS_DMA_H */
