#include <stdio.h>

#include "bus_rx_boundary.h"

#define CHECK(condition, message) do { \
    if (!(condition)) { \
        fprintf(stderr, "FAIL: %s:%d: %s\n", __FILE__, __LINE__, message); \
        return 1; \
    } \
} while (0)

int main(void)
{
    /* ⭐ 2026-10-10 压测修复（待办 2）：**无 pending 命令（真被动）**的边界
     * 语义已改 —— 从"必须攒够 512 B"改为"有字节就交出"。
     *
     * 旧语义（本条断言原为 == 0）的问题：短报文协议（Modbus 9 B 响应、
     * ASCII 行协议）永远攒不到 512 B ⇒ 被动数据无法按自动边界完成，
     * 只能靠软 idle 兜底，延迟高。
     *
     * 为什么"有字节就交出"是安全的：调用方只在**静默窗口到达后**才调用本
     * 函数（complete_idle_response 的 now_us - s_last_rx_us 判定），
     * 因此此刻"缓冲区有字节"等价于"一帧已收完"。
     *
     * ⚠ 这条是**有意改变**的既有行为，不是回归 —— 见下方主动路径断言
     *   （有 pending 时仍严格按 read_size / 固定块）。 */
    /* ⚠ 2026-10-10：曾把"无 pending（真被动）"改为"有字节就交出"，**已回退**。
     * 原因见 bus_rx_boundary.h 的说明：本函数在**每批字节到达后立即**被调用，
     * 不是只在静默后才调用 ⇒ 立即交出会把半帧当整帧上报。
     * 被动路径的正确解法是硬件 timeout_flag 边界（P3-1，已实现）。 */
    CHECK(bus_rx_boundary_length(0, false, false, 0) == 0,
          "empty input must not emit a report");
    CHECK(bus_rx_boundary_length(511, false, false, 0) == 0,
          "short passive input must wait for the fixed boundary");
    CHECK(bus_rx_boundary_length(512, false, false, 0) == 512,
          "passive input must emit at the fixed boundary");
    CHECK(bus_rx_boundary_length(99, true, false, 100) == 0,
          "explicit read_size must not complete early");
    CHECK(bus_rx_boundary_length(150, true, false, 100) == 100,
          "explicit read_size must complete exactly at its length");
    CHECK(bus_rx_boundary_length(512, true, false, 0) == 512,
          "length-less pending input must use the common block boundary");
    CHECK(bus_rx_boundary_length(512, true, true, 0) == 0,
          "unknown ChannelCmdV2 length must not emit a partial final");
    puts("bus_rx_boundary_tests: all tests passed");
    return 0;
}
