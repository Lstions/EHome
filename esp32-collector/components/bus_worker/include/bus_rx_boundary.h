#ifndef BUS_RX_BOUNDARY_H
#define BUS_RX_BOUNDARY_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define BUS_RX_FIXED_BLOCK_SIZE 512U

/* ⚠ 2026-10-10：本函数曾被尝试改成"无 pending 时立即交出"，**已回退**。
 *
 * 回退理由（host 测试当场抓到）：本函数**不是**只在静默窗口后才被调用 ——
 * rx_append_from_event 在**每收到一批字节后立即**调用它（emit_ready_stream_chunks）。
 * 因此"缓冲区有字节"绝不等于"一帧已收完"，改成立即交出会把**半帧**当成整帧上报。
 * 证据：test_emit_partial_block_no_emission 期望 300 B 时**不**发射。
 *
 * 被动路径的真正问题是"必须攒够 512 B"，正确解法是让**硬件边界**
 * （UART_DATA 的 timeout_flag，uart_set_rx_timeout(port,4) = 4 字符静默）
 * 成为权威帧结束信号 —— 那已由 P3-1 实现（bus_worker.c 的
 * `if (event->type == UART_DATA && event->timeout_flag) emit_buffered_frame(...)`）。
 * 本函数保持原语义不动。
 *
 * 原描述（供参考）：无命令时（真被动）的边界不再是"必须攒够 512 B"。
 *
 * 问题（实测定位）：原实现无 pending 命令时 target 固定为
 * BUS_RX_FIXED_BLOCK_SIZE(512)。对短报文协议（Modbus 9 B 响应、各类 ASCII
 * 行协议）**永远攒不到 512 字节** ⇒ 被动数据既不走 read_size 边界、也不走
 * 512 B 块边界，只能靠 rx_task 的静默兜底，延迟高且语义含糊。
 *
 * 现在的边界优先级（自上而下，第一个满足者胜）：
 *   1. pending + channel_cmd_v2 + read_size == 0 ⇒ 不在此处完成
 *      （V2 的行静默才是权威边界，见 bus_worker.c 的说明）
 *   2. read_size > 0                ⇒ 收满即完成（主动路径，最快）
 *   3. pending（有命令但未声明长度）⇒ 攒够固定块完成（保持原行为）
 *   4. **无 pending（真被动）**     ⇒ 缓冲区有字节就立即交出
 *      ⇒ 被动数据不再需要攒够 512 B，帧边界由调用方的静默判定决定
 *
 * 为什么第 4 条安全：调用方（rx_task）只在**静默窗口到达后**才调用本函数
 * （complete_idle_response 的 now_us - s_last_rx_us 判定），因此"缓冲区有
 * 字节"此时等价于"一帧已收完"，不会把半帧当整帧交出去。
 *
 * 返回值语义不变：0 = 还需要更多字节；非 0 = 可以交出这么多字节。 */
static inline size_t bus_rx_boundary_length(size_t buffered, bool pending,
                                             bool channel_cmd_v2,
                                             uint32_t read_size)
{
    if (buffered == 0 || (pending && channel_cmd_v2 && read_size == 0)) return 0;
    size_t target = read_size > 0 ? (size_t)read_size : BUS_RX_FIXED_BLOCK_SIZE;
    return buffered >= target ? target : 0;
}

#endif
