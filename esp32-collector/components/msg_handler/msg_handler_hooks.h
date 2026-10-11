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

/* === DMA 降级通道（handler_config.c 调用；实现在 main/app_callbacks.c）===
 *
 * ⚠ 2026-10-09（用户要求："DMA 分不到降级为提示"）：
 * 返回最近一次成功 apply 时因 DMA 不可用而降级为 polled 的通道 id。
 *
 * 为什么走钩子而不是直接调 bus_manager_get_dma_degraded_channels()：
 *   msg_handler 不能 REQUIRES bus_manager —— 会成环：
 *     bus_manager -> bus_worker -> msg_handler
 *   钩子是本仓既有的破环手法（见本文件头部关于 B2 的说明）。
 *
 * ⚠ 声明为**普通 extern**（不是弱符号）：漏实现必须是构建错误。
 *   若给弱默认 return 0，就等于"降级提示静默消失" —— 正是本文件头部
 *   表格里那一族"弱定义生效 ⇒ 功能静默消失"的缺陷形态。
 *
 * @param out_ids 接收通道 id 的数组（可为 NULL，只取数量）
 * @param max     数组容量
 * @return 实际降级的通道数（可能 > max，此时只填了前 max 个）
 */
int dma_degraded_channels(uint32_t *out_ids, int max);

/* === 通道命令 v2（handler_channel_cmd_v2.c 调用）=== */
/* 前置声明：真实定义在 msg_handler_internal.h（已加 struct tag）。
 * 这样钩子头不必把内部头整个拉进来，同时保持**类型安全** ——
 * 用 void* 会让"签名写错"从编译错误退化成运行期问题。 */
struct channel_cmd_v2;

const char *channel_cmd_v2_current_boot_id(void);
uint64_t    channel_cmd_v2_current_time_ms(void);
bool        on_channel_cmd_v2_received(const struct channel_cmd_v2 *cmd, uint8_t slot);

/* === 借用 batch plan（bus_worker 调用；实现在 handler_channel_cmd_v2.c）===
 *
 * ⭐ 方案 D（2026-10-10）：队列元素不再内联 plan buffer，worker 改为**借用**
 * msg_handler control 槽位里的那一份。
 *
 * 为什么是访问器而不是把裸指针放进队列元素：
 *   · 裸指针的正确性依赖「槽位在 worker 读完之前不被复用」这条**隐式**不变量，
 *     而它只在当前的执行顺序下偶然成立（读 plan 与发布 FINAL 恰好同任务串行）。
 *     将来若有人把完成发布提前（完成回调已走 s_control_final_q 异步队列），
 *     立刻变成 use-after-free，且**没有任何机制会报警**。
 *   · 访问器把这条不变量变成**显式运行时校验**：槽位状态不是 QUEUED
 *     （或 COMPLETING）就打印错误并拒绝交出 plan。失败可见，而不是静默损坏。
 *
 * 约定（借用期）：
 *   · 返回 true 时 *out_plan / *out_len / *out_steps 有效，且保证在**当前任务
 *     的这次调用期间**有效（槽位在整个执行期保持 QUEUED）。
 *   · 调用方**不得跨任务**传递该指针，也不得在长时间阻塞后继续使用 ——
 *     应当在同一次执行流程内用完。
 *   · 返回 false 表示「这个 slot 没有可借的 plan」。可能是：
 *       (a) slot 非法或状态不是 QUEUED（不变量被破坏，已打印错误）；
 *       (b) 该命令本来就没有 plan（plan_len == 0）。
 *     调用方应把 false 当作「无 plan」，而不是「致命错误」——
 *     判据由调用方结合 cmd->channel_cmd_v2 自行决定。
 *
 * 线程安全：内部用原子 load 读槽位状态，可在任意任务调用。 */
bool channel_cmd_v2_borrow_plan(uint8_t slot,
                                const uint8_t **out_plan,
                                size_t *out_len,
                                uint8_t *out_steps);

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
