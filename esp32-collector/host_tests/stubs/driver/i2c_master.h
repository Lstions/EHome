#ifndef HOST_TEST_DRIVER_I2C_MASTER_H
#define HOST_TEST_DRIVER_I2C_MASTER_H

#include <stdint.h>
#include <stddef.h>
#include "esp_err.h"

typedef int i2c_port_t;
#define I2C_NUM_0 0
#define I2C_NUM_1 1
#define I2C_NUM_MAX 2

#define I2C_CLK_SRC_DEFAULT  0
#define I2C_ADDR_BIT_LEN_7   0

typedef void *i2c_master_bus_handle_t;
typedef void *i2c_master_dev_handle_t;

typedef struct {
    i2c_port_t i2c_port;
    int sda_io_num;
    int scl_io_num;
    int clk_source;
    uint32_t glitch_ignore_cnt;
    struct {
        uint32_t enable_internal_pullup : 1;
    } flags;
} i2c_master_bus_config_t;

typedef struct {
    uint8_t dev_addr_length;
    uint16_t device_address;
    uint32_t scl_speed_hz;
} i2c_device_config_t;

/* Driver function declarations — implementations provided by test files */
esp_err_t i2c_new_master_bus(const i2c_master_bus_config_t *cfg,
                             i2c_master_bus_handle_t *handle);
esp_err_t i2c_del_master_bus(i2c_master_bus_handle_t handle);
esp_err_t i2c_master_bus_add_device(i2c_master_bus_handle_t bus,
                                    const i2c_device_config_t *cfg,
                                    i2c_master_dev_handle_t *handle);
esp_err_t i2c_master_bus_rm_device(i2c_master_dev_handle_t handle);
esp_err_t i2c_master_transmit_receive(i2c_master_dev_handle_t dev,
                                      const uint8_t *tx, size_t tx_len,
                                      uint8_t *rx, size_t rx_len, int timeout);
esp_err_t i2c_master_transmit(i2c_master_dev_handle_t dev,
                              const uint8_t *tx, size_t tx_len, int timeout);
esp_err_t i2c_master_receive(i2c_master_dev_handle_t dev,
                             uint8_t *rx, size_t rx_len, int timeout);

#endif
