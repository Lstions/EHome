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

/**
 * 注入远程运维（0x22）的三个原语：NVS 擦除 / 同步发送 / 重启。
 * 必须在传输开始收包之前调用（见 device_op_wiring.c）。
 */
void device_op_wiring_init(void);

/**
 * 下行消息的**唯一**处理入口（P4/P1）：判断是否 ConfigManifest、分发、必要时应用。
 *
 * MQTT 回调、debug-TCP 回调、3.0 链路（devlink_on_msg）**都调它** ——
 * 此前三处各写一遍，而 3.0 那份**只做了分发、漏了应用** ⇒ 它的 ConfigManifest
 * "到达、被分发、然后什么都不发生"。见
 * docs/设计/决策-3.0-配置应用所有权-2026-10-07.md。
 *
 * ⚠ 本函数会做**配置事务**（较重）。在 3.0 链路上它跑在 devlink 任务栈上
 *   （栈 6144 B）⇒ 该任务栈的余量必须据此复核。
 *
 * @param t 该消息所属 transport；NULL = 无特定 transport（回执走广播/MQTT）。
 * @return  是否被当作 ConfigManifest 处理。
 */
bool ehome_handle_downlink(const uint8_t *data, size_t len, transport_t *t);

#ifdef __cplusplus
}
#endif

#endif /* APP_CALLBACKS_H */
