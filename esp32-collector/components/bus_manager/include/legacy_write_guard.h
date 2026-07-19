#ifndef LEGACY_WRITE_GUARD_H
#define LEGACY_WRITE_GUARD_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define LEGACY_WRITE_BUS_UART 1U
#define LEGACY_WRITE_BUS_I2C  2U
#define LEGACY_WRITE_BUS_SPI  3U

static inline bool legacy_write_args_valid(uint32_t channel_id,
                                           const uint8_t *data, size_t len,
                                           uint32_t read_size,
                                           uint32_t rx_timeout_ms,
                                           size_t tx_max)
{
    return channel_id != 0 && len <= tx_max && (len == 0 || data != NULL) &&
           read_size <= 256 && rx_timeout_ms >= 1 && rx_timeout_ms <= 30000;
}

static inline bool legacy_write_route_valid(uint8_t bus_type, int uart_port)
{
    if (bus_type == LEGACY_WRITE_BUS_UART) return uart_port == 0 || uart_port == 1;
    return bus_type == LEGACY_WRITE_BUS_I2C || bus_type == LEGACY_WRITE_BUS_SPI;
}

#endif
