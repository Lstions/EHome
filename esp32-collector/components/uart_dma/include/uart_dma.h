/**
 * @file uart_dma.h
 * @brief UART DMA Transaction Engine — minimal header
 */

#ifndef UART_DMA_H
#define UART_DMA_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include "driver/uart.h"
#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

#define UART_DMA_RX_RING_SIZE  1024
#define UART_DMA_DEFAULT_TIMEOUT_MS  50

typedef struct {
    uart_port_t  port;
    uint32_t     baud_rate;
    int          tx_pin, rx_pin;
    bool         initialized;
} uart_dma_ctx_t;

esp_err_t uart_dma_init(uart_dma_ctx_t *ctx, uart_port_t port,
                        int tx_pin, int rx_pin, uint32_t baud_rate);
void uart_dma_deinit(uart_dma_ctx_t *ctx);

esp_err_t uart_dma_transact(uart_dma_ctx_t *ctx,
                            const uint8_t *tx_data, size_t tx_len,
                            uint32_t timeout_ms,
                            uint8_t *rx_buf, size_t rx_buf_size,
                            size_t *rx_len);

#ifdef __cplusplus
}
#endif
#endif
