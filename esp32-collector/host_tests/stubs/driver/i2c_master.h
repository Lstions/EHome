#ifndef HOST_TEST_DRIVER_I2C_MASTER_H
#define HOST_TEST_DRIVER_I2C_MASTER_H

#include <stdint.h>

typedef int i2c_port_t;
#define I2C_NUM_0 0
#define I2C_NUM_MAX 2

typedef void *i2c_master_bus_handle_t;
typedef void *i2c_master_dev_handle_t;

typedef struct {
    i2c_port_t i2c_port;
    int sda_io_num;
    int scl_io_num;
    int clk_source;
    uint32_t glitch_ignore_cnt;
    uint32_t flags;
} i2c_master_bus_config_t;

typedef struct {
    uint8_t device_address;
    uint32_t dev_config_flags;
} i2c_device_config_t;

#endif
