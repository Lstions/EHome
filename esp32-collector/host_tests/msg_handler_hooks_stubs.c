/**
 * @file msg_handler_hooks_stubs.c
 * @brief 宿主测试用的应用层钩子桩（B2 弱符号同族收敛，2026-10-06）
 *
 * 为什么需要它：这些钩子原先是 handler 的 .c 里的 __attribute__((weak)) 空实现，
 * 所以"宿主测试不提供实现"也能链接。弱符号删除后，**漏实现会变成链接错误** ——
 * 这正是我们要的（构建期响亮，而不是运行期静默无操作）。
 *
 * 于是宿主测试必须【显式】表态："本测试不关心这个钩子"。
 * 本文件就是那个表态，集中在**一处**（而不是每个测试各写一遍副本）。
 *
 * 注意桩的语义刻意与"最小惊讶"一致：
 *   - ehome_mem_can_start 返回 true（宿主测试不模拟内存门禁）；
 *   - 其余为无操作/中性值。
 * 生产实现见 main/main.c 与 main/app_callbacks.c。
 */
#include "msg_handler_hooks.h"

#include <stddef.h>

/* 内存门禁：宿主测试不做门禁。刻意【不是】弱符号 ——
 * 需要不同语义的测试请在自己的 TU 里定义强符号并**不要**链接本文件。 */
bool ehome_mem_can_start(size_t need_bytes)
{
    (void)need_bytes;
    return true;
}

/* 扫描：宿主解码器测试只关心"解出来没有"，不关心副作用。
 *
 * 注意 on_write_cmd_received **不在这里** —— writecmd_decoder_tests 需要
 * 捕获它的入参做断言，那个测试自带实现（带捕获语义），
 * 与本文件的"中性桩"语义不同。把它放这里会造成重复定义。
 * ⇒ 本文件只提供【多个测试都需要且语义一致】的钩子。 */
void on_scan_req_received(const char *request_id, uint32_t hardware_id)
{
    (void)request_id; (void)hardware_id;
}

void on_modbus_scan_req_received(const char *request_id,
                                 uint32_t start_addr, uint32_t end_addr,
                                 uint32_t timeout_ms)
{
    (void)request_id; (void)start_addr; (void)end_addr; (void)timeout_ms;
}

void on_query_resources_received(const char *request_id)
{
    (void)request_id;
}

/* 注意 msg_handler_publish_checked **也不在这里** ——
 * handler_data_tests 与 rx_health_e2e_tests 各自定义了带断言语义的版本
 * （它们要验证"发布是否被调用/调用了几次"）。
 * 需要中性版本的测试请在自己的 TU 里定义（三行）。 */
