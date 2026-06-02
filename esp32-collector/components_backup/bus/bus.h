/**
 * @file bus.h
 * @brief 总线抽象层 (HAL)
 * 
 * 支持 SPI/I2C/UART/GPIO/ADC 总线类型。
 */

#ifndef BUS_H
#define BUS_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/* === 总线类型 === */
typedef enum {
    BUS_TYPE_UART = 1,
    BUS_TYPE_I2C  = 2,
    BUS_TYPE_SPI  = 3,
    BUS_TYPE_GPIO = 4,
    BUS_TYPE_ADC  = 5,
} bus_type_t;

/* === 总线配置 === */
typedef struct {
    bus_type_t type;
    union {
        struct {
            uint32_t baud_rate;
            uint8_t  data_bits;
            uint8_t  stop_bits;
            uint8_t  parity;
        } uart;
        struct {
            uint32_t freq_hz;
            uint8_t  slave_address;
        } i2c;
        struct {
            uint32_t freq_hz;
            uint8_t  mode;
            int      cs_pin;
        } spi;
        struct {
            int      pin;
            uint8_t  direction;
        } gpio;
        struct {
            int      pin;
            uint8_t  attenuation;
        } adc;
    } config;
} bus_config_t;

/* === 总线句柄 === */
typedef struct bus_handle_s *bus_handle_t;

/* === 总线操作 === */
typedef struct {
    int (*init)(bus_handle_t *handle, const bus_config_t *config);
    int (*deinit)(bus_handle_t handle);
    int (*write)(bus_handle_t handle, const uint8_t *data, size_t len);
    int (*read)(bus_handle_t handle, uint8_t *data, size_t len);
    int (*transact)(bus_handle_t handle, const uint8_t *tx_data, size_t tx_len, 
                    uint8_t *rx_data, size_t rx_len, uint32_t delay_ms);
} bus_ops_t;

/* === 公共函数 === */
int bus_init(bus_handle_t *handle, bus_type_t type, const void *config);
int bus_deinit(bus_handle_t handle);
int bus_write(bus_handle_t handle, const uint8_t *data, size_t len);
int bus_read(bus_handle_t handle, uint8_t *data, size_t len);
int bus_transact(bus_handle_t handle, const uint8_t *tx_data, size_t tx_len,
                 uint8_t *rx_data, size_t rx_len, uint32_t delay_ms);

#ifdef __cplusplus
}
#endif

#endif /* BUS_H */
