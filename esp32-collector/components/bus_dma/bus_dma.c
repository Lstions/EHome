/**
 * @file bus_dma.c
 * @brief Unified Bus DMA Engine for ESP-IDF v6
 *
 * Supports UART, SPI, and I2C with dynamic DMA on/off switch.
 * - UART DMA:  driver_install with ring buffers + rx_timeout gap detection
 * - UART polled: flush + write + read with timeout
 * - SPI DMA:   spi_bus_initialize with SPI_DMA_CH_AUTO
 * - SPI polled: spi_bus_initialize with SPI_DMA_DISABLED
 * - I2C DMA:   new master API (i2c_new_master_bus + i2c_master_bus_add_device)
 * - I2C polled: legacy API (i2c_param_config + i2c_driver_install + cmd_link)
 */

#include "bus_dma.h"
#include "esp_log.h"
#include "driver/uart.h"
#include "driver/spi_master.h"
#include "driver/i2c_master.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include <string.h>

#define TAG "BUS_DMA"

/* ------------------------------------------------------------------ */
/*  Helpers                                                           */
/* ------------------------------------------------------------------ */

static inline uint32_t read_be32(const uint8_t *p)
{
    return ((uint32_t)p[0] << 24) | ((uint32_t)p[1] << 16) |
           ((uint32_t)p[2] << 8)  |  (uint32_t)p[3];
}

/* ------------------------------------------------------------------ */
/*  UART init / transact / deinit                                     */
/* ------------------------------------------------------------------ */

static esp_err_t uart_init(bus_dma_ctx_t *ctx, const uint8_t *cfg, size_t len)
{
    if (len < 6) return ESP_ERR_INVALID_SIZE;

    int tx_pin  = cfg[0];
    int rx_pin  = cfg[1];
    uint32_t baud = read_be32(&cfg[2]);

    ctx->cfg.uart.port   = UART_NUM_1;
    ctx->cfg.uart.baud   = baud;
    ctx->cfg.uart.tx_pin = tx_pin;
    ctx->cfg.uart.rx_pin = rx_pin;

    uart_config_t uart_cfg = {
        .baud_rate  = (int)baud,
        .data_bits  = UART_DATA_8_BITS,
        .parity     = UART_PARITY_DISABLE,
        .stop_bits  = UART_STOP_BITS_1,
        .flow_ctrl  = UART_HW_FLOWCTRL_DISABLE,
        .source_clk = UART_SCLK_DEFAULT,
    };

    esp_err_t r;
    r = uart_param_config(ctx->cfg.uart.port, &uart_cfg);
    if (r != ESP_OK) return r;

    r = uart_set_pin(ctx->cfg.uart.port, tx_pin, rx_pin, -1, -1);
    if (r != ESP_OK) return r;

    if (ctx->dma_enabled) {
        /* DMA-backed ring buffers: RX 1024B, TX 256B */
        r = uart_driver_install(ctx->cfg.uart.port, 1024, 256, 0, NULL, 0);
        if (r != ESP_OK) return r;
        uart_set_rx_timeout(ctx->cfg.uart.port, 4);
    } else {
        /* Polled: small RX buffer, no TX buffer */
        r = uart_driver_install(ctx->cfg.uart.port, 256, 0, 0, NULL, 0);
        if (r != ESP_OK) return r;
    }

    ESP_LOGI(TAG, "UART%d %s init (TX=%d RX=%d baud=%lu)",
             ctx->cfg.uart.port,
             ctx->dma_enabled ? "DMA" : "polled",
             tx_pin, rx_pin, (unsigned long)baud);
    return ESP_OK;
}

static esp_err_t uart_transact(bus_dma_ctx_t *ctx,
                               const uint8_t *tx, size_t tx_len,
                               uint32_t timeout_ms,
                               uint8_t *rx, size_t rx_size, size_t *rx_len)
{
    uart_port_t port = ctx->cfg.uart.port;
    *rx_len = 0;

    if (ctx->dma_enabled) {
        /*
         * DMA path: NO flush_input — the DMA ring captures everything.
         * 1. TX
         * 2. Wait for TX complete
         * 3. Read first chunk with generous timeout (500ms)
         * 4. Loop reading with short gap timeout (50ms) until idle
         */
        if (tx && tx_len > 0) {
            int w = uart_write_bytes(port, (const char *)tx, tx_len);
            if (w < 0) return ESP_FAIL;
            uart_wait_tx_done(port, pdMS_TO_TICKS(100));
        }

        /* First read: allow up to 500ms for the first byte */
        size_t total = 0;
        int n = uart_read_bytes(port, rx, rx_size, pdMS_TO_TICKS(500));
        if (n > 0) {
            total = (size_t)n;
            /* Continue reading while data arrives within 50ms gap */
            while (total < rx_size) {
                n = uart_read_bytes(port, rx + total, rx_size - total,
                                    pdMS_TO_TICKS(50));
                if (n <= 0) break;
                total += (size_t)n;
            }
        }

        *rx_len = total;
        return (total > 0) ? ESP_OK : ESP_ERR_TIMEOUT;
    } else {
        /*
         * Polled path: flush stale RX, TX, wait, single read with timeout.
         */
        uart_flush_input(port);

        if (tx && tx_len > 0) {
            int w = uart_write_bytes(port, (const char *)tx, tx_len);
            if (w < 0) return ESP_FAIL;
            uart_wait_tx_done(port, pdMS_TO_TICKS(100));
        }

        uint32_t tmo = (timeout_ms > 0) ? timeout_ms : 50;
        int n = uart_read_bytes(port, rx, rx_size, pdMS_TO_TICKS(tmo));
        if (n > 0) {
            *rx_len = (size_t)n;
            return ESP_OK;
        }
        return ESP_ERR_TIMEOUT;
    }
}

static void uart_deinit(bus_dma_ctx_t *ctx)
{
    uart_driver_delete(ctx->cfg.uart.port);
}

/* ------------------------------------------------------------------ */
/*  SPI init / transact / deinit                                      */
/* ------------------------------------------------------------------ */

static esp_err_t spi_init(bus_dma_ctx_t *ctx, const uint8_t *cfg, size_t len)
{
    if (len < 6) return ESP_ERR_INVALID_SIZE;

    int cs_pin     = cfg[0];
    uint8_t mode   = cfg[1];
    uint32_t freq  = read_be32(&cfg[2]);

    ctx->cfg.spi.host   = SPI2_HOST;
    ctx->cfg.spi.cs_pin = cs_pin;
    ctx->cfg.spi.freq   = freq;
    ctx->cfg.spi.mode   = mode;

    /* Default bus pins — caller may override via SPI pin config */
    spi_bus_config_t bus_cfg = {
        .mosi_io_num   = -1,
        .miso_io_num   = -1,
        .sclk_io_num   = -1,
        .quadwp_io_num = -1,
        .quadhd_io_num = -1,
        .max_transfer_sz = 256,
    };

    /*
     * NOTE: In real usage the bus may already be initialized by another
     * channel. spi_bus_initialize will return ESP_ERR_INVALID_STATE if so.
     * We ignore that and just add the device.
     */
    esp_err_t r = spi_bus_initialize(SPI2_HOST, &bus_cfg,
                                     ctx->dma_enabled ? SPI_DMA_CH_AUTO
                                                      : SPI_DMA_DISABLED);
    if (r != ESP_OK && r != ESP_ERR_INVALID_STATE) return r;

    spi_device_interface_config_t dev_cfg = {
        .clock_speed_hz = (int)freq,
        .mode           = mode,
        .spics_io_num   = cs_pin,
        .queue_size     = 1,
    };

    r = spi_bus_add_device(SPI2_HOST, &dev_cfg, &ctx->cfg.spi.dev);
    if (r != ESP_OK) return r;

    ESP_LOGI(TAG, "SPI %s init (CS=%d mode=%d freq=%lu)",
             ctx->dma_enabled ? "DMA" : "polled",
             cs_pin, mode, (unsigned long)freq);
    return ESP_OK;
}

static esp_err_t spi_transact(bus_dma_ctx_t *ctx,
                               const uint8_t *tx, size_t tx_len,
                               uint32_t timeout_ms,
                               uint8_t *rx, size_t rx_size, size_t *rx_len)
{
    (void)timeout_ms;
    *rx_len = 0;

    spi_transaction_t t = {
        .length    = tx_len * 8,          /* bits */
        .tx_buffer = tx,
        .rxlength  = rx_size * 8,
        .rx_buffer = rx,
    };

    /* If TX only, don't request RX */
    if (rx == NULL || rx_size == 0) {
        t.rxlength  = 0;
        t.rx_buffer = NULL;
    }
    /* If RX only, don't send TX */
    if (tx == NULL || tx_len == 0) {
        t.length    = rx_size * 8;
        t.tx_buffer = NULL;
    }

    esp_err_t r = spi_device_transmit(ctx->cfg.spi.dev, &t);
    if (r != ESP_OK) return r;

    *rx_len = (t.rxlength > 0) ? (t.rxlength / 8) : 0;
    return ESP_OK;
}

static void spi_deinit(bus_dma_ctx_t *ctx)
{
    if (ctx->cfg.spi.dev) {
        spi_bus_remove_device(ctx->cfg.spi.dev);
        ctx->cfg.spi.dev = NULL;
    }
}

/* ------------------------------------------------------------------ */
/*  I2C init / transact / deinit                                      */
/* ------------------------------------------------------------------ */

static esp_err_t i2c_init(bus_dma_ctx_t *ctx, const uint8_t *cfg, size_t len)
{
    if (len < 7) return ESP_ERR_INVALID_SIZE;

    int sda       = cfg[0];
    int scl       = cfg[1];
    uint8_t addr  = cfg[2];
    uint32_t freq = read_be32(&cfg[3]);

    ctx->cfg.i2c.addr    = addr;
    ctx->cfg.i2c.freq    = freq;
    ctx->cfg.i2c.sda_pin = sda;
    ctx->cfg.i2c.scl_pin = scl;

    /*
     * ESP-IDF v6: Only new i2c_master API available.
     * dma_enabled controls combined (transmit_receive) vs split operations.
     */
    i2c_master_bus_config_t bus_cfg = {
        .i2c_port        = I2C_NUM_0,
        .sda_io_num      = sda,
        .scl_io_num      = scl,
        .clk_source      = I2C_CLK_SRC_DEFAULT,
        .glitch_ignore_cnt = 7,
        .flags.enable_internal_pullup = true,
    };

    esp_err_t r = i2c_new_master_bus(&bus_cfg, &ctx->cfg.i2c.bus_handle);
    if (r != ESP_OK) {
        ESP_LOGE(TAG, "i2c_new_master_bus failed: %s", esp_err_to_name(r));
        return r;
    }

    i2c_device_config_t dev_cfg = {
        .dev_addr_length = I2C_ADDR_BIT_LEN_7,
        .device_address  = addr,
        .scl_speed_hz    = freq,
    };

    r = i2c_master_bus_add_device(ctx->cfg.i2c.bus_handle, &dev_cfg, &ctx->cfg.i2c.dev_handle);
    if (r != ESP_OK) {
        ESP_LOGE(TAG, "i2c_master_bus_add_device failed: %s", esp_err_to_name(r));
        i2c_del_master_bus(ctx->cfg.i2c.bus_handle);
        ctx->cfg.i2c.bus_handle = NULL;
        return r;
    }

    ESP_LOGI(TAG, "I2C %s init (SDA=%d SCL=%d addr=0x%02X freq=%lu)",
             ctx->dma_enabled ? "DMA" : "std",
             sda, scl, addr, (unsigned long)freq);
    return ESP_OK;
}

static esp_err_t i2c_transact(bus_dma_ctx_t *ctx,
                               const uint8_t *tx, size_t tx_len,
                               uint32_t timeout_ms,
                               uint8_t *rx, size_t rx_size, size_t *rx_len)
{
    *rx_len = 0;
    int tmo = (timeout_ms > 0) ? (int)timeout_ms : 100;

    i2c_master_dev_handle_t dev = ctx->cfg.i2c.dev_handle;
    if (dev == NULL) return ESP_ERR_INVALID_STATE;

    esp_err_t r;
    if (tx && tx_len > 0 && rx && rx_size > 0) {
        /* Write then read — DMA uses combined, std uses split */
        if (ctx->dma_enabled) {
            r = i2c_master_transmit_receive(dev, tx, tx_len, rx, rx_size, tmo);
        } else {
            r = i2c_master_transmit(dev, tx, tx_len, tmo);
            if (r == ESP_OK)
                r = i2c_master_receive(dev, rx, rx_size, tmo);
        }
        if (r == ESP_OK) *rx_len = rx_size;
    } else if (tx && tx_len > 0) {
        r = i2c_master_transmit(dev, tx, tx_len, tmo);
    } else if (rx && rx_size > 0) {
        r = i2c_master_receive(dev, rx, rx_size, tmo);
        if (r == ESP_OK) *rx_len = rx_size;
    } else {
        r = ESP_ERR_INVALID_ARG;
    }
    return r;
}

static void i2c_deinit(bus_dma_ctx_t *ctx)
{
    if (ctx->cfg.i2c.dev_handle) {
        i2c_master_bus_rm_device(ctx->cfg.i2c.dev_handle);
        ctx->cfg.i2c.dev_handle = NULL;
    }
    if (ctx->cfg.i2c.bus_handle) {
        i2c_del_master_bus(ctx->cfg.i2c.bus_handle);
        ctx->cfg.i2c.bus_handle = NULL;
    }
}

/* ------------------------------------------------------------------ */
/*  Public API                                                        */
/* ------------------------------------------------------------------ */

esp_err_t bus_dma_init(bus_dma_ctx_t *ctx, uint8_t bus_type, bool dma_enabled,
                       const uint8_t *config, size_t config_len)
{
    if (ctx == NULL || config == NULL) return ESP_ERR_INVALID_ARG;

    memset(ctx, 0, sizeof(*ctx));
    ctx->bus_type    = bus_type;
    ctx->dma_enabled = dma_enabled;

    ctx->mutex = xSemaphoreCreateMutex();
    if (ctx->mutex == NULL) return ESP_ERR_NO_MEM;

    esp_err_t r;
    switch (bus_type) {
        case BUS_TYPE_UART: r = uart_init(ctx, config, config_len); break;
        case BUS_TYPE_SPI:  r = spi_init(ctx, config, config_len);  break;
        case BUS_TYPE_I2C:  r = i2c_init(ctx, config, config_len);  break;
        default:
            ESP_LOGE(TAG, "Unknown bus type: %d", bus_type);
            r = ESP_ERR_NOT_SUPPORTED;
            break;
    }

    if (r != ESP_OK) {
        vSemaphoreDelete(ctx->mutex);
        ctx->mutex = NULL;
        return r;
    }

    ctx->initialized = true;
    return ESP_OK;
}

esp_err_t bus_dma_transact(bus_dma_ctx_t *ctx,
                           const uint8_t *tx, size_t tx_len,
                           uint32_t timeout_ms,
                           uint8_t *rx, size_t rx_size, size_t *rx_len)
{
    if (ctx == NULL || !ctx->initialized || rx_len == NULL)
        return ESP_ERR_INVALID_ARG;

    *rx_len = 0;

    if (xSemaphoreTake(ctx->mutex, pdMS_TO_TICKS(1000)) != pdTRUE)
        return ESP_ERR_TIMEOUT;

    esp_err_t r;
    switch (ctx->bus_type) {
        case BUS_TYPE_UART:
            r = uart_transact(ctx, tx, tx_len, timeout_ms, rx, rx_size, rx_len);
            break;
        case BUS_TYPE_SPI:
            r = spi_transact(ctx, tx, tx_len, timeout_ms, rx, rx_size, rx_len);
            break;
        case BUS_TYPE_I2C:
            r = i2c_transact(ctx, tx, tx_len, timeout_ms, rx, rx_size, rx_len);
            break;
        default:
            r = ESP_ERR_NOT_SUPPORTED;
            break;
    }

    xSemaphoreGive(ctx->mutex);
    return r;
}

void bus_dma_deinit(bus_dma_ctx_t *ctx)
{
    if (ctx == NULL || !ctx->initialized) return;

    switch (ctx->bus_type) {
        case BUS_TYPE_UART: uart_deinit(ctx); break;
        case BUS_TYPE_SPI:  spi_deinit(ctx);  break;
        case BUS_TYPE_I2C:  i2c_deinit(ctx);  break;
    }

    if (ctx->mutex) {
        vSemaphoreDelete(ctx->mutex);
        ctx->mutex = NULL;
    }

    ctx->initialized = false;
    ESP_LOGI(TAG, "Bus type=%d deinit", ctx->bus_type);
}
