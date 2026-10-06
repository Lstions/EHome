/**
 * @file msg_handler_hooks.h
 * @brief 应用层钩子（由 main/ 实现）——【唯一】声明处，且**禁止弱符号**
 *
 * 2026-10-06 建立（B2 弱符号同族收敛）。
 *
 * ## 为什么这里曾经用 __attribute__((weak))，以及为什么现在不用了
 *
 * 这些钩子原先各自在 **handler 的 .c 里**带一个 weak 默认实现，形态是：
 *     __attribute__((weak)) void on_write_cmd_received(...) { }   // 空函数体
 * 注释写着"implemented in main.c"。
 *
 * 问题不在"有没有默认值"，而在**默认值的语义** —— 它们全都是
 * "**什么都不做，并且看起来成功**"：
 *
 * | 钩子 | 弱默认的语义 | 若强实现没被链接进来 |
 * |---|---|---|
 * | `ehome_mem_can_start` | `return true` | **内存门禁被静默放行** |
 * | `on_write_cmd_received` | 空函数 | 写命令**被静默忽略** |
 * | `on_scan_req_received` | 空函数 | 扫描请求**被静默忽略** |
 * | `on_modbus_scan_req_received` | 空函数 | 同上 |
 * | `on_query_resources_received` | 空函数 | 资源查询**被静默忽略** |
 * | `on_channel_cmd_v2_received` | `return false` | 通道命令**被静默拒绝** |
 * | `channel_cmd_v2_current_boot_id` | `NULL` | boot_id 变空 |
 * | `channel_cmd_v2_current_time_ms` | `0` | 时间变 0 |
 * | `msg_handler_publish_checked` | 退化为未校验发布 | **绕过 L-02 的校验发布** |
 *
 * 生效与否**取决于链接顺序**（强实现所在的 .o 是否被拉进镜像），
 * 而宿主测试走的是另一套符号表 ⇒ 与 D-06 完全同型：
 * **弱定义一旦生效，功能静默消失，且没有测试会红。**
 *
 * ## 现在的约定
 *  1. 钩子在这里声明为**普通 extern**（无 weak）；
 *  2. 强实现由 `main/` 提供；
 *  3. **漏实现 = 链接错误**（构建期、响亮），而不是运行期静默无操作。
 *
 * 若某个钩子确实允许"没有实现"，那就**显式写出一个有日志的实现**，
 * 而不是靠弱符号把它藏起来 —— 见 `main.c` 里 `on_scan_req_received`
 * 的 NOT-IMPLEMENTED 实现。
 */
#ifndef EHOME_MSG_HANDLER_HOOKS_H
#define EHOME_MSG_HANDLER_HOOKS_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

/* === 写命令 / 扫描（handler_writecmd.c 调用）=== */
void on_write_cmd_received(uint32_t request_id, uint32_t channel_id,
                           const uint8_t *data, size_t len, uint32_t read_size,
                           uint32_t edge_device_id, uint32_t rx_timeout_ms);
void on_scan_req_received(const char *request_id, uint32_t hardware_id);
void on_modbus_scan_req_received(const char *request_id,
                                 uint32_t start_addr, uint32_t end_addr,
                                 uint32_t timeout_ms);

/* === 资源查询（handler_config.c 调用）=== */
void on_query_resources_received(const char *request_id);

/* === 通道命令 v2（handler_channel_cmd_v2.c 调用）=== */
/* 前置声明：真实定义在 msg_handler_internal.h（已加 struct tag）。
 * 这样钩子头不必把内部头整个拉进来，同时保持**类型安全** ——
 * 用 void* 会让"签名写错"从编译错误退化成运行期问题。 */
struct channel_cmd_v2;

const char *channel_cmd_v2_current_boot_id(void);
uint64_t    channel_cmd_v2_current_time_ms(void);
bool        on_channel_cmd_v2_received(const struct channel_cmd_v2 *cmd, uint8_t slot);

/* === 校验发布（handler_channel_cmd_v2.c 调用；实现在 msg_handler.c）===
 * 注意：不是弱默认 —— 绕过它等于绕过 L-02 建立的"发布必须被校验"约束。 */
esp_err_t msg_handler_publish_checked(const uint8_t *data, size_t len);

/* === 内存门禁（handler_data.c 调用；实现在 main/app_callbacks.c）===
 * 声明为普通 extern：**门禁漏实现必须是构建错误**，
 * 绝不能靠弱默认的 `return true` 静默放行。 */
bool ehome_mem_can_start(size_t need_bytes);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_MSG_HANDLER_HOOKS_H */
