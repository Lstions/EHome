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
