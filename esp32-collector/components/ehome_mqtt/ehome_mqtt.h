/**
 * @file mqtt_client.h
 * @brief MQTT Client with auto-reconnect
 */

#ifndef EHOME_MQTT_H
#define EHOME_MQTT_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>

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

/* === Message callback === */
typedef void (*mqtt_msg_cb_t)(const char *topic, const uint8_t *data, size_t len, void *ctx);
typedef void (*mqtt_state_cb_t)(mqtt_client_state_t state, void *ctx);

/* === Init / Start / Stop === */
void mqtt_client_init(void);
void mqtt_client_start(void);
void mqtt_client_stop(void);

/* === Publish === */
bool mqtt_client_publish_impl(const uint8_t *data, size_t len);

/* === Subscribe === */
bool mqtt_client_subscribe_impl(const char *topic);

/* === State === */
mqtt_client_state_t mqtt_client_get_state(void);
bool mqtt_client_is_connected_impl(void);

/* === Callbacks === */
void mqtt_client_register_msg_cb(mqtt_msg_cb_t cb, void *ctx);
void mqtt_client_register_state_cb(mqtt_state_cb_t cb, void *ctx);

/* === Set node_id (for topic construction) === */
void mqtt_client_set_node_id(const char *node_id);

#ifdef __cplusplus
}
#endif

#endif /* EHOME_MQTT_H */
