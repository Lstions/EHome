/**
 * @file link_mqtt.h
 * @brief MQTT 的 link 驱动 —— 把四态【原样】交给调用方（3.0 link 迁移）
 *
 * ## 它解决什么
 * 旧路径 `transport_ops.send` 返回 `esp_err_t`：只有成功/失败两档，
 * 于是底层已经分出来的"本地未连接 / 背压 / 真失败"在适配器里被**压平**
 * （见 mqtt_transport_adapter.c 的 D-01 注释）。调用方拿不到区分，
 * 就无法决定"该退避"还是"该报错"。
 *
 * 本驱动把 `mqtt_publish_result_t`（四态）如实映射到 `link_result_t`：
 *
 * | 底层 | link_result_t | 调用方应当 |
 * |---|---|---|
 * | `MQTT_PUBLISH_OK` | `LINK_SENT` | 继续 |
 * | `MQTT_PUBLISH_NOT_CONNECTED` | `LINK_NOT_READY` | 降级/入队，**不要**换路重试 |
 * | `MQTT_PUBLISH_BACKPRESSURE` | `LINK_BACKPRESSURE` | **退避后重试**（不是故障） |
 * | `MQTT_PUBLISH_FAILED` | `LINK_FATAL` | 上报故障，**不要**重试 |
 *
 * 注意 `MTU`：MQTT 单帧上限由 broker/esp-mqtt 决定（本仓实测约 2011 B，
 * 见 R1）。驱动如实报出该上限，让 `link_send` 在**发出前**就拒绝超长帧 ——
 * 那是 P2（端到端契约在发出前可验证）在 MQTT 上的落点。
 */
#ifndef EHOME_LINK_MQTT_H
#define EHOME_LINK_MQTT_H

#include "link.h"

#ifdef __cplusplus
extern "C" {
#endif

/** MQTT 单帧上限（字节）。取值来源：R1 实测 —— 超此长度的帧 MQTT 侧失败。 */
#define LINK_MQTT_MTU_BYTES 2011u

/** 取 MQTT 的 link 驱动（可直接传给 link_create）。 */
const link_driver_t *link_mqtt_driver(void);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_LINK_MQTT_H */
