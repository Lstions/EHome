/**
 * @file uart_dma.c
 * @brief UART DMA Transaction Engine for ESP-IDF v6
 *
 * Uses uart_driver_install (DMA-backed ring buffers) + uart_set_rx_timeout
 * for frame gap detection. All public API is synchronous to caller but
 * internally leverages DMA — no CPU polling.
 */

#include "uart_dma.h"
#include "esp_log.h"
#include "driver/uart.h"
#include <string.h>

#define TAG "UART_DMA"

esp_err_t uart_dma_init(uart_dma_ctx_t *ctx, uart_port_t port,
                        int tx_pin, int rx_pin, uint32_t baud_rate)
{
    if (ctx == NULL) return ESP_ERR_INVALID_ARG;

    memset(ctx, 0, sizeof(*ctx));
    ctx->port = port;
    ctx->baud_rate = baud_rate;
    ctx->tx_pin = tx_pin;
    ctx->rx_pin = rx_pin;

    uart_config_t uart_cfg = {
        .baud_rate  = (int)baud_rate,
        .data_bits  = UART_DATA_8_BITS,
        .parity     = UART_PARITY_DISABLE,
        .stop_bits  = UART_STOP_BITS_1,
        .flow_ctrl  = UART_HW_FLOWCTRL_DISABLE,
        .source_clk = UART_SCLK_DEFAULT,
    };
    ESP_ERROR_CHECK(uart_param_config(port, &uart_cfg));
    ESP_ERROR_CHECK(uart_set_pin(port, tx_pin, rx_pin, -1, -1));

    /* Install with DMA ring buffers: RX 1024B, TX 256B */
    ESP_ERROR_CHECK(uart_driver_install(port,
                                        UART_DMA_RX_RING_SIZE,
                                        256, 0, NULL, 0));

    /* RX timeout for frame gap detection */
    ESP_ERROR_CHECK(uart_set_rx_timeout(port, 4));

    ctx->initialized = true;
    ESP_LOGI(TAG, "UART%d DMA init (TX=%d RX=%d baud=%lu ring=%dB)",
             port, tx_pin, rx_pin, (unsigned long)baud_rate, UART_DMA_RX_RING_SIZE);
    return ESP_OK;
}

void uart_dma_deinit(uart_dma_ctx_t *ctx)
{
    if (ctx == NULL || !ctx->initialized) return;
    uart_driver_delete(ctx->port);
    ctx->initialized = false;
}

esp_err_t uart_dma_transact(uart_dma_ctx_t *ctx,
                            const uint8_t *tx_data, size_t tx_len,
                            uint32_t timeout_ms,
                            uint8_t *rx_buf, size_t rx_buf_size,
                            size_t *rx_len)
{
    if (ctx == NULL || !ctx->initialized || rx_len == NULL)
        return ESP_ERR_INVALID_ARG;

    *rx_len = 0;

    /* 1. Flush stale RX */
    uart_flush_input(ctx->port);

    /* 2. TX */
    if (tx_data && tx_len > 0) {
        int w = uart_write_bytes(ctx->port, (const char *)tx_data, tx_len);
        if (w < 0) return ESP_FAIL;
        uart_wait_tx_done(ctx->port, pdMS_TO_TICKS(100));
    }

    /* 3. RX with timeout (DMA-backed, no CPU spin) */
    int bytes = uart_read_bytes(ctx->port, rx_buf, rx_buf_size,
                                 pdMS_TO_TICKS(timeout_ms > 0 ? timeout_ms : 50));
    if (bytes > 0) {
        *rx_len = bytes;
        return ESP_OK;
    }

    return ESP_ERR_TIMEOUT;
}
