/**
 * @file app_callbacks.h
 * @brief WiFi / MQTT / Transport state and message callbacks.
 */

#ifndef APP_CALLBACKS_H
#define APP_CALLBACKS_H

#include <stdint.h>
#include <stddef.h>
#include "wifi_mgr.h"
#include "ehome_mqtt.h"
#include "transport.h"

#ifdef __cplusplus
extern "C" {
#endif

void on_wifi_state_cb(wifi_mgr_state_t state, void *ctx);
void on_mqtt_state_cb(mqtt_client_state_t state, void *ctx);
void on_mqtt_transport_cb(uint32_t generation, void *ctx);
void on_mqtt_owner_wake_cb(void *ctx);
void on_mqtt_ready_cb(uint32_t generation, void *ctx);
void on_mqtt_msg_cb(const char *topic, const uint8_t *data, size_t len, void *ctx);
void on_transport_msg_cb(const uint8_t *data, size_t len, void *ctx);
void on_transport_state_cb(transport_state_t state, void *ctx);

#ifdef __cplusplus
}
#endif

#endif /* APP_CALLBACKS_H */
