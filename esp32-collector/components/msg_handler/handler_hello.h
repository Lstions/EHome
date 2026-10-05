/**
 * @file handler_hello.h
 * @brief Hello/HelloAck 处理器对外接口 —— HelloAck 能力位读取。
 *
 * 为什么单独开这个头：HelloAck 的 features(field 2) 以前是 (void)features
 * 被忽略。V3-2a 起它承载服务端能力位图（契约 §1），而 bus_worker 的
 * report_tx 需要在"要不要发 DataBatch(0x20)"上读它。
 *
 * 为什么不让 bus_worker 直接包含 msg_handler.h：msg_handler.h 依赖
 * scheduler.h/config_mgr.h/esp_err.h，会把一条重量级头链拖进 bus_worker。
 * 这个头只声明一个返回 uint64_t 的函数，零依赖。
 */

#ifndef HANDLER_HELLO_H
#define HANDLER_HELLO_H

#include <stdint.h>
#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

/**
 * @brief 读取最近一次被接受的 HelloAck 携带的服务端能力位图。
 *
 * 返回 0 表示：尚未收到 HelloAck，或对端未置任何能力位 —— 此时固件行为
 * 必须与 V3-2a 之前逐字节一致（只发 0x03）。
 *
 * 读取无需加锁：内部是一个 64 位对齐的 volatile 量，在 32 位 ESP32 上
 * 读写各自原子；且这是单向的"服务端告诉我它能干什么"，最坏情况是某一帧
 * 早/晚一个能力位周期，不影响正确性（DataBatch 本身容忍丢帧）。
 */
uint64_t hello_get_server_caps(void);

#ifdef __cplusplus
}
#endif

#endif /* HANDLER_HELLO_H */
