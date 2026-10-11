/**
 * @file mqtt_client.h
 * @brief 宿主测试用的 esp-mqtt 头桩（D-24）
 *
 * 为什么需要它：esp-mqtt 是 IDF 的 managed component
 * （components/managed_components/espressif__mqtt），**不能**在宿主编译。
 * 而 ehome_mqtt.h 只是需要一个不透明句柄类型 `esp_mqtt_client_handle_t`
 * 来声明它的上下文结构 —— 于是给出这个最小桩。
 *
 * 注意：本桩【只】提供类型与常量，不提供任何行为。
 * 依赖真实 MQTT 行为的测试不应该链接本桩（它们需要跑在设备上）。
 */
#ifndef EHOME_HOST_STUB_MQTT_CLIENT_H
#define EHOME_HOST_STUB_MQTT_CLIENT_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

/* 不透明句柄 —— 与 IDF 一致：宿主测试不需要它的内部结构。 */
typedef void *esp_mqtt_client_handle_t;

typedef enum {
    MQTT_EVENT_ANY = -1,
    MQTT_EVENT_ERROR = 0,
    MQTT_EVENT_CONNECTED,
    MQTT_EVENT_DISCONNECTED,
    MQTT_EVENT_SUBSCRIBED,
    MQTT_EVENT_UNSUBSCRIBED,
    MQTT_EVENT_PUBLISHED,
    MQTT_EVENT_DATA,
    MQTT_EVENT_BEFORE_CONNECT,
} esp_mqtt_event_id_t;

#endif /* EHOME_HOST_STUB_MQTT_CLIENT_H */
