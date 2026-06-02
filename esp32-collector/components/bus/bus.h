/**
 * @file bus.h
 * @brief Bus Abstraction Layer (I2C/SPI/UART)
 */

#ifndef BUS_H
#define BUS_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef enum {
    BUS_OK = 0,
    BUS_ERR_INVALID = -1,
    BUS_ERR_OPEN = -2,
    BUS_ERR_TRANSACT = -3,
} bus_error_t;

typedef void *bus_handle_t;

bus_handle_t bus_open(uint8_t bus_type, const uint8_t *config, size_t config_len);
void bus_close(bus_handle_t handle);
bus_error_t bus_transact(bus_handle_t handle, uint8_t *rx_buf, size_t rx_buf_size, size_t *rx_len);

/* Set the transmit buffer and read length for next transaction */
void bus_set_tx(bus_handle_t handle, const uint8_t *tx_data, size_t tx_len, uint32_t read_length);

#ifdef __cplusplus
}
#endif

#endif
