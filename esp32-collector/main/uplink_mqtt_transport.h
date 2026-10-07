/**
 * @file uplink_mqtt_transport.h
 * @brief 门控版 MQTT 上行出口的接口（理由见 .c 的文件头）。
 *
 * 本头只依赖 transport.h + esp_err.h ⇒ 宿主可编，因此实现能进宿主测试
 * （check_host_coverage.py 的口径是被宿主 CMake 引用的 .c，见该脚本 :76-86）。
 */
#ifndef EHOME_UPLINK_MQTT_TRANSPORT_H
#define EHOME_UPLINK_MQTT_TRANSPORT_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "esp_err.h"
#include "transport.h"

#ifdef __cplusplus
extern "C" {
#endif

/**
 * 注入的 IO —— 让本模块与 ehome_mqtt 解耦（从而宿主可测）。
 *
 * publish      发布一帧，返回 esp_err_t（三态映射由绑定方完成：
 *              PUBLISH_OK -> ESP_OK，NOT_CONNECTED/FAILED -> ESP_FAIL，
 *              与 mqtt_transport_adapter.c:60-67 逐条一致）
 * is_connected 当前能否上行（真实绑定见 uplink_arbiter.c 的 IDF 段：
 *              它 = mqtt 客户端已连 且 仲裁层门开）
 */
typedef struct {
    esp_err_t (*publish)(const uint8_t *data, size_t len);
    bool      (*is_connected)(void);
} uplink_mqtt_io_t;

/**
 * 绑定 IO 并返回唯一的 MQTT 上行 transport 对象（幂等，重复调用只更新 IO）。
 *
 * 返回对象的 msg_cb / state_cb 恒为 NULL：本对象只做上行出口。
 * 本模块没有任何 mqtt_client_register_* 调用 —— 这是结构性保证，
 * 不是约定（由 host_tests/uplink_mqtt_transport_tests.c 断言 ops 表钉住）。
 *
 * @return transport 句柄；io 为 NULL 时返回 NULL。
 */
transport_t *uplink_mqtt_transport_ops(const uplink_mqtt_io_t *io);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_UPLINK_MQTT_TRANSPORT_H */
