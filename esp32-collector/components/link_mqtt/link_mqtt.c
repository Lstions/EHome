/**
 * @file link_mqtt.c
 * @brief MQTT link 驱动实现
 */
#include "link_mqtt.h"

#include "ehome_mqtt.h"
#include "esp_log.h"

static const char *TAG = "LINK_MQTT";

static link_result_t mqtt_open(void *ctx)
{
    (void)ctx;
    /* ehome_mqtt 的生命周期由 main.c 的 supervisor 管；此处只做"能否开始"的判定。 */
    return mqtt_client_is_connected_impl() ? LINK_SENT : LINK_NOT_READY;
}

static void mqtt_close(void *ctx)
{
    (void)ctx;
    /* 同样：MQTT 的连接生命周期不归本驱动管（supervisor 负责重连）。 */
}

static link_result_t mqtt_send(void *ctx, const uint8_t *data, size_t len)
{
    (void)ctx;
    /* 四态如实映射 —— 【不压平】。这张表就是本文件存在的理由。 */
    switch (mqtt_client_publish_ex(data, len)) {
    case MQTT_PUBLISH_OK:             return LINK_SENT;
    case MQTT_PUBLISH_NOT_CONNECTED:  return LINK_NOT_READY;
    case MQTT_PUBLISH_BACKPRESSURE:   return LINK_BACKPRESSURE;
    case MQTT_PUBLISH_FAILED:
    default:                          return LINK_FATAL;
    }
}

static uint32_t mqtt_mtu(void *ctx)
{
    (void)ctx;
    return LINK_MQTT_MTU_BYTES;
}

static bool mqtt_is_ready(void *ctx)
{
    (void)ctx;
    return mqtt_client_is_connected_impl();
}

static const link_driver_t s_mqtt_driver = {
    .open = mqtt_open,
    .close = mqtt_close,
    .send = mqtt_send,
    .mtu = mqtt_mtu,
    .is_ready = mqtt_is_ready,
    .name = "mqtt",
};

const link_driver_t *link_mqtt_driver(void)
{
    return &s_mqtt_driver;
}
