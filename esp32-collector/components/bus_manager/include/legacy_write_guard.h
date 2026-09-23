#ifndef LEGACY_WRITE_GUARD_H
#define LEGACY_WRITE_GUARD_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define LEGACY_WRITE_BUS_UART 1U
#define LEGACY_WRITE_BUS_I2C  2U
#define LEGACY_WRITE_BUS_SPI  3U
/* Native USB Serial/JTAG as a data bus.  Kept in sync with BUS_TYPE_USB in
 * bus_dma.h; duplicated here because this header is deliberately free of the
 * driver includes so the host tests can compile it standalone. */
#define LEGACY_WRITE_BUS_USB  4U

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
    if (bus_type == LEGACY_WRITE_BUS_UART) return uart_port >= 0 && uart_port < 3;
    /* USB has no uart_port to validate: the endpoint is fixed and there is only
     * one, so any value (including the 0 that an unset field carries) is fine. */
    if (bus_type == LEGACY_WRITE_BUS_USB) return true;
    return bus_type == LEGACY_WRITE_BUS_I2C || bus_type == LEGACY_WRITE_BUS_SPI;
}

#endif
