/**
 * @file mqtt_client.h
 * @brief MQTT client with supervisor-owned recovery
 */

#ifndef EHOME_MQTT_H
#define EHOME_MQTT_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include "esp_err.h"
#include "mqtt_client.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"

#ifdef __cplusplus
extern "C" {
#endif

/* === MQTT state === */
typedef enum {
    MQTT_CLIENT_DISCONNECTED,
    MQTT_CLIENT_CONNECTING,
    MQTT_CLIENT_CONNECTED,
    MQTT_CLIENT_FAILED,
} mqtt_client_state_t;

typedef enum {
    MQTT_SUB_IDLE,
    MQTT_SUB_NEEDS_SEND,
    MQTT_SUB_WAIT_ACK,
    MQTT_SUB_SUCCEEDED,
    MQTT_SUB_FAILED,
} mqtt_subscription_phase_t;

/* === Message callback === */
typedef void (*mqtt_msg_cb_t)(const char *topic, const uint8_t *data, size_t len, void *ctx);
typedef void (*mqtt_state_cb_t)(mqtt_client_state_t state, void *ctx);
typedef void (*mqtt_ready_cb_t)(uint32_t generation, void *ctx);
/* Lightweight transport-connected callback fired from the MQTT event handler
 * on every MQTT_EVENT_CONNECTED after the stale-client guard. Must be
 * non-blocking (no I/O, no allocations). Used to immediately disarm stale
 * ACK state before the ready callback fires. */
typedef void (*mqtt_transport_cb_t)(uint32_t generation, void *ctx);
/* Wakes the lifecycle-owner task after a guarded transport event changes
 * recovery state. The callback must only perform a non-blocking notification. */
typedef void (*mqtt_owner_wake_cb_t)(void *ctx);

/* === MQTT client context (encapsulates all mutable state) === */
typedef struct {
    esp_mqtt_client_handle_t client;
    mqtt_client_state_t      state;
    mqtt_msg_cb_t            msg_cb;
    void                    *msg_cb_ctx;
    mqtt_state_cb_t          state_cb;
    void                    *state_cb_ctx;
    mqtt_ready_cb_t          ready_cb;
    void                    *ready_cb_ctx;
    mqtt_transport_cb_t      transport_cb;
    void                    *transport_cb_ctx;
    mqtt_owner_wake_cb_t     owner_wake_cb;
    void                    *owner_wake_cb_ctx;
    SemaphoreHandle_t        mutex;
    /* Written by the MQTT event task, consumed by the lifecycle owner. */
    bool                     transport_connected;
    bool                     disconnected_event_pending;
    uint32_t                 connection_generation;
    uint32_t                 subscription_generation;
    uint32_t                 client_generation;
    /* One batched SUBSCRIBE transaction for the current connection. */
    mqtt_subscription_phase_t subscription_phase;
    int                       subscription_msg_id;
    int64_t                   subscription_deadline_us;
} mqtt_client_ctx_t;

/* === Init / lifecycle owner === */
void mqtt_client_init(void);
/* Requests are safe from transport callbacks; the supervisor consumes them. */
esp_err_t mqtt_client_request_start(void);
esp_err_t mqtt_client_request_stop(void);
/* Sole lifecycle entry point. Only the MQTT supervisor task may call it. */
void mqtt_client_owner_step(bool network_available);

/* === Publish === */

/* 发布结果。**必须用枚举而不是 bool**（2026-10-06，L-02 根因）：
 *
 * 100 Hz 台架实测 120 s 产生 3,873 条 "Publish failed"，而每次广播失败只应
 * 对应 1 条。根因之一是调用方拿不到"没连上"这个信息 —— 它只能看到 bool
 * false，于是**在同一个调用里继续尝试下游 transport**，同一帧被发布两次
 * （一次同步适配器 + 一次 msg_handler 回退）。
 *
 * 语义：
 *   MQTT_PUBLISH_OK             已交给 esp-mqtt（成功）
 *   MQTT_PUBLISH_NOT_CONNECTED  本地未连接，调用方**不应**重试其它 MQTT 路径
 *   MQTT_PUBLISH_FAILED         对端/队列问题（含 outbox 满 -2），可上报失败 */
typedef enum {
    MQTT_PUBLISH_OK = 0,
    MQTT_PUBLISH_NOT_CONNECTED = 1,
    MQTT_PUBLISH_FAILED = 2,
} mqtt_publish_result_t;

/**
 * @brief 发布一帧并返回**分类后**的结果。
 *
 * 与 mqtt_client_publish_impl 的区别：后者把三类结果压成一个 bool，调用方
 * 无法区分"没连上"与"发失败"，因此无法避免重复发布。
 */
mqtt_publish_result_t mqtt_client_publish_ex(const uint8_t *data, size_t len);

/* 兼容包装：仅返回是否成功。新代码请用 mqtt_client_publish_ex()。 */
bool mqtt_client_publish_impl(const uint8_t *data, size_t len);

/* === State === */
mqtt_client_state_t mqtt_client_get_state(void);
bool mqtt_client_is_connected_impl(void);

/* === Callbacks === */
void mqtt_client_register_msg_cb(mqtt_msg_cb_t cb, void *ctx);
void mqtt_client_register_state_cb(mqtt_state_cb_t cb, void *ctx);
void mqtt_client_register_ready_cb(mqtt_ready_cb_t cb, void *ctx);
void mqtt_client_register_transport_cb(mqtt_transport_cb_t cb, void *ctx);
void mqtt_client_register_owner_wake_cb(mqtt_owner_wake_cb_t cb, void *ctx);

/* === Set node_id (for topic construction) === */
void mqtt_client_set_node_id(const char *node_id);

/**
 * @brief 注册 MQTT transport
 */
esp_err_t mqtt_transport_register(void);

#ifdef __cplusplus
}
#endif

#endif /* EHOME_MQTT_H */
