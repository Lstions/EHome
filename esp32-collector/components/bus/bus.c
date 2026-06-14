/**
 * @file bus.c
 * @brief Bus Abstraction Layer - I2C/SPI/UART implementation for ESP32
 *
 * Thread safety: each bus handle has its own mutex (I2C/SPI only).
 * UART driver is internally thread-safe.
 */

#include "bus.h"
#include "esp_log.h"
#include "driver/i2c.h"
#include "driver/uart.h"
#include "freertos/semphr.h"
#include <string.h>
#include <stdlib.h>

#define TAG "BUS"

/* Bus types */
#define BUS_TYPE_UART 1
#define BUS_TYPE_I2C  2
#define BUS_TYPE_SPI  3

/* Internal config structures */
typedef struct {
    i2c_port_t port;
    uint8_t    slave_addr;
    uint32_t   freq_hz;
    uint8_t    sda_pin;
    uint8_t    scl_pin;
} my_i2c_config_t;

typedef struct {
    uint8_t  cs_pin;
    uint32_t freq_hz;
    uint8_t  mode;
} my_spi_config_t;

typedef struct {
    uart_port_t port;
    uint32_t    baud_rate;
} my_uart_config_t;

/* Bus handle */
typedef struct bus_handle_impl {
    uint8_t      bus_type;
    union {
        my_i2c_config_t  i2c;
        my_spi_config_t  spi;
        my_uart_config_t uart;
    } config;
    /* Transaction parameters */
    uint8_t  tx_buf[64];
    size_t   tx_len;
    uint32_t read_length;
    /* I2C mutex — protects bus_transact from concurrent access */
    SemaphoreHandle_t mutex;
} bus_handle_impl_t;

static bool s_i2c_initialized[I2C_NUM_MAX] = {false};

bus_handle_t bus_open(uint8_t bus_type, const uint8_t *config_data, size_t config_len)
{
    (void)config_len;
    bus_handle_impl_t *h = calloc(1, sizeof(bus_handle_impl_t));
    if (!h) return NULL;

    h->bus_type = bus_type;
    /* Create mutex for I2C/SPI (UART doesn't need one) */
    if (bus_type == BUS_TYPE_I2C || bus_type == BUS_TYPE_SPI) {
        h->mutex = xSemaphoreCreateMutex();
    }

    switch (bus_type) {
    case BUS_TYPE_I2C: {
        if (config_data && config_len >= 7) {
            h->config.i2c.sda_pin = config_data[0];
            h->config.i2c.scl_pin = config_data[1];
            h->config.i2c.slave_addr = config_data[2];
            h->config.i2c.freq_hz = ((uint32_t)config_data[3] << 24) |
                                    ((uint32_t)config_data[4] << 16) |
                                    ((uint32_t)config_data[5] << 8) |
                                    config_data[6];
        } else {
            h->config.i2c.sda_pin = 21;
            h->config.i2c.scl_pin = 22;
            h->config.i2c.slave_addr = 0x76;
            h->config.i2c.freq_hz = 100000;
        }
        h->config.i2c.port = I2C_NUM_0;

        if (!s_i2c_initialized[h->config.i2c.port]) {
            i2c_config_t conf = {
                .mode = I2C_MODE_MASTER,
                .sda_io_num = h->config.i2c.sda_pin,
                .scl_io_num = h->config.i2c.scl_pin,
                .sda_pullup_en = GPIO_PULLUP_ENABLE,
                .scl_pullup_en = GPIO_PULLUP_ENABLE,
                .master.clk_speed = h->config.i2c.freq_hz,
            };
            i2c_param_config(h->config.i2c.port, &conf);
            i2c_driver_install(h->config.i2c.port, I2C_MODE_MASTER, 0, 0, 0);
            s_i2c_initialized[h->config.i2c.port] = true;
        }
        ESP_LOGI(TAG, "I2C bus opened (SDA=%d, SCL=%d, addr=0x%02X, freq=%lu)",
                 h->config.i2c.sda_pin, h->config.i2c.scl_pin,
                 h->config.i2c.slave_addr, (unsigned long)h->config.i2c.freq_hz);
        break;
    }

    case BUS_TYPE_SPI: {
        if (config_data && config_len >= 6) {
            h->config.spi.cs_pin = config_data[0];
            h->config.spi.mode = config_data[1];
            h->config.spi.freq_hz = ((uint32_t)config_data[2] << 24) |
                                    ((uint32_t)config_data[3] << 16) |
                                    ((uint32_t)config_data[4] << 8) |
                                    config_data[5];
        } else {
            h->config.spi.cs_pin = 5;
            h->config.spi.mode = 0;
            h->config.spi.freq_hz = 1000000;
        }
        ESP_LOGI(TAG, "SPI bus opened (CS=%d, mode=%d, freq=%lu)",
                 h->config.spi.cs_pin, h->config.spi.mode,
                 (unsigned long)h->config.spi.freq_hz);
        break;
    }

    case BUS_TYPE_UART: {
        if (config_data && config_len >= 6) {
            h->config.uart.baud_rate = ((uint32_t)config_data[2] << 24) |
                                       ((uint32_t)config_data[3] << 16) |
                                       ((uint32_t)config_data[4] << 8) |
                                       config_data[5];
        } else {
            h->config.uart.baud_rate = 9600;
        }
        h->config.uart.port = UART_NUM_1;

        uart_config_t uart_conf = {
            .baud_rate = (int)h->config.uart.baud_rate,
            .data_bits = UART_DATA_8_BITS,
            .parity = UART_PARITY_DISABLE,
            .stop_bits = UART_STOP_BITS_1,
            .flow_ctrl = UART_HW_FLOWCTRL_DISABLE,
            .source_clk = UART_SCLK_DEFAULT,
        };
        uart_param_config(h->config.uart.port, &uart_conf);

        /* Set UART pins from config_data[0..1] or default GPIO20/21 */
        int tx_pin = 21;
        int rx_pin = 20;
        if (config_data && config_len >= 2) {
            tx_pin = config_data[0];
            rx_pin = config_data[1];
        }
        uart_set_pin(h->config.uart.port, tx_pin, rx_pin, UART_PIN_NO_CHANGE, UART_PIN_NO_CHANGE);
        uart_driver_install(h->config.uart.port, 256, 0, 0, NULL, 0);
        ESP_LOGI(TAG, "UART bus opened (TX=%d, RX=%d, baud=%lu)",
                 tx_pin, rx_pin, (unsigned long)h->config.uart.baud_rate);
        break;
    }

    default:
        ESP_LOGW(TAG, "Unknown bus type: %d", bus_type);
        free(h);
        return NULL;
    }

    return h;
}

void bus_close(bus_handle_t handle)
{
    if (!handle) return;
    bus_handle_impl_t *h = (bus_handle_impl_t *)handle;
    switch (h->bus_type) {
    case BUS_TYPE_UART:
        uart_driver_delete(h->config.uart.port);
        break;
    case BUS_TYPE_SPI:
        /* SPI cleanup not implemented — needs driver/spi_master.h + esp_driver_spi */
        break;
    default:
        break;
    }
    free(h);
}

void bus_set_tx(bus_handle_t handle, const uint8_t *tx_data, size_t tx_len, uint32_t read_length)
{
    if (!handle) return;
    bus_handle_impl_t *h = (bus_handle_impl_t *)handle;
    if (tx_data && tx_len > 0 && tx_len <= sizeof(h->tx_buf)) {
        memcpy(h->tx_buf, tx_data, tx_len);
        h->tx_len = tx_len;
    } else {
        h->tx_len = 0;
    }
    h->read_length = read_length;
}

bus_error_t bus_transact(bus_handle_t handle, uint8_t *rx_buf, size_t rx_buf_size, size_t *rx_len)
{
    if (!handle || !rx_buf || !rx_len) {
        return BUS_ERR_INVALID;
    }

    bus_handle_impl_t *h = (bus_handle_impl_t *)handle;
    *rx_len = 0;

    /* Lock I2C/SPI mutex */
    if (h->mutex) {
        xSemaphoreTake(h->mutex, pdMS_TO_TICKS(500));
    }

    bus_error_t result = BUS_OK;

    switch (h->bus_type) {
    case BUS_TYPE_I2C: {
        i2c_cmd_handle_t cmd = i2c_cmd_link_create();
        i2c_master_start(cmd);
        i2c_master_write_byte(cmd, (h->config.i2c.slave_addr << 1) | I2C_MASTER_WRITE, true);
        if (h->tx_len > 0) {
            i2c_master_write(cmd, h->tx_buf, h->tx_len, true);
        }
        i2c_master_stop(cmd);
        esp_err_t err = i2c_master_cmd_begin(h->config.i2c.port, cmd, pdMS_TO_TICKS(100));
        i2c_cmd_link_delete(cmd);

        if (err != ESP_OK) {
            ESP_LOGW(TAG, "I2C write failed: %s", esp_err_to_name(err));
            result = BUS_ERR_TRANSACT;
            break;
        }

        if (h->read_length > 0 && rx_buf_size > 0) {
            size_t to_read = h->read_length;
            if (to_read > rx_buf_size) to_read = rx_buf_size;

            cmd = i2c_cmd_link_create();
            i2c_master_start(cmd);
            i2c_master_write_byte(cmd, (h->config.i2c.slave_addr << 1) | I2C_MASTER_READ, true);
            if (to_read > 1) {
                i2c_master_read(cmd, rx_buf, to_read - 1, I2C_MASTER_ACK);
            }
            i2c_master_read_byte(cmd, rx_buf + to_read - 1, I2C_MASTER_NACK);
            i2c_master_stop(cmd);
            err = i2c_master_cmd_begin(h->config.i2c.port, cmd, pdMS_TO_TICKS(100));
            i2c_cmd_link_delete(cmd);

            if (err == ESP_OK) {
                *rx_len = to_read;
            } else {
                ESP_LOGW(TAG, "I2C read failed: %s", esp_err_to_name(err));
                result = BUS_ERR_TRANSACT;
            }
        }
        break;
    }

    case BUS_TYPE_SPI: {
        ESP_LOGW(TAG, "SPI transaction not fully implemented");
        break;
    }

    case BUS_TYPE_UART: {
        /* Flush stale RX data before transaction */
        uart_flush_input(h->config.uart.port);
    
        if (h->tx_len > 0) {
            ESP_LOGD(TAG, "UART TX %d bytes", (int)h->tx_len);
            uart_write_bytes(h->config.uart.port, (const char *)h->tx_buf, h->tx_len);
            uart_wait_tx_done(h->config.uart.port, pdMS_TO_TICKS(100));
        }
        if (h->read_length > 0 && rx_buf_size > 0) {
            /* Wait for first byte (500ms), then stream remaining (50ms gaps) */
            int total_read = 0;
            int first_read = uart_read_bytes(h->config.uart.port, rx_buf,
                                             (h->read_length < rx_buf_size) ? h->read_length : rx_buf_size,
                                             pdMS_TO_TICKS(500));
            if (first_read > 0) {
                total_read = first_read;
                /* Continue reading until 50ms gap = end of Modbus frame */
                while (total_read < (int)rx_buf_size && total_read < 256) {
                    int more = uart_read_bytes(h->config.uart.port, rx_buf + total_read,
                                               rx_buf_size - total_read, pdMS_TO_TICKS(50));
                    if (more <= 0) break;
                    total_read += more;
                }
                *rx_len = total_read;
                ESP_LOGI(TAG, "UART RX %d bytes: %02x %02x %02x %02x %02x %02x %02x",
                         total_read, rx_buf[0], total_read > 1 ? rx_buf[1] : 0,
                         total_read > 2 ? rx_buf[2] : 0, total_read > 3 ? rx_buf[3] : 0,
                         total_read > 4 ? rx_buf[4] : 0, total_read > 5 ? rx_buf[5] : 0,
                         total_read > 6 ? rx_buf[6] : 0);
            } else {
                ESP_LOGW(TAG, "UART RX timeout (0 bytes, waited 500ms)");
            }
        }
        break;
    }

    default:
        result = BUS_ERR_INVALID;
        break;
    }

    if (h->mutex) {
        xSemaphoreGive(h->mutex);
    }

    return result;
}
