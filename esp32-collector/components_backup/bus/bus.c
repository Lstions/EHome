/**
 * @file bus.c
 * @brief 总线抽象层实现
 */

#include "bus.h"
#include "esp_log.h"
#include <string.h>

#define TAG "BUS"

typedef struct {
    bus_type_t type;
    union {
        void *uart;
        void *i2c;
        void *spi;
    } handle;
    const bus_ops_t *ops;
} bus_instance_t;

static bus_instance_t s_buses[4]; // 最多4个总线实例
static bool s_initialized = false;

int bus_init(bus_handle_t *handle, bus_type_t type, const void *config)
{
    (void)config;
    
    if (!s_initialized) {
        memset(s_buses, 0, sizeof(s_buses));
        s_initialized = true;
    }
    
    // 查找空闲槽位
    for (int i = 0; i < 4; i++) {
        if (s_buses[i].type == 0) {
            s_buses[i].type = type;
            // TODO: 根据类型初始化具体硬件
            *handle = (bus_handle_t)&s_buses[i];
            ESP_LOGI(TAG, "Bus %d initialized, type=%d", i, type);
            return 0;
        }
    }
    
    return -1; // 无空闲槽位
}

int bus_deinit(bus_handle_t handle)
{
    bus_instance_t *bus = (bus_instance_t *)handle;
    if (bus == NULL) {
        return -1;
    }
    
    bus->type = 0;
    bus->ops = NULL;
    
    return 0;
}

int bus_write(bus_handle_t handle, const uint8_t *data, size_t len)
{
    bus_instance_t *bus = (bus_instance_t *)handle;
    if (bus == NULL || bus->ops == NULL || bus->ops->write == NULL) {
        return -1;
    }
    
    return bus->ops->write(handle, data, len);
}

int bus_read(bus_handle_t handle, uint8_t *data, size_t len)
{
    bus_instance_t *bus = (bus_instance_t *)handle;
    if (bus == NULL || bus->ops == NULL || bus->ops->read == NULL) {
        return -1;
    }
    
    return bus->ops->read(handle, data, len);
}

int bus_transact(bus_handle_t handle, const uint8_t *tx_data, size_t tx_len,
                 uint8_t *rx_data, size_t rx_len, uint32_t delay_ms)
{
    bus_instance_t *bus = (bus_instance_t *)handle;
    if (bus == NULL || bus->ops == NULL || bus->ops->transact == NULL) {
        return -1;
    }
    
    return bus->ops->transact(handle, tx_data, tx_len, rx_data, rx_len, delay_ms);
}
