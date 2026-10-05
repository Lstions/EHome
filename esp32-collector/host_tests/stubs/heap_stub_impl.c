/* 宿主机共享桩：堆内存查询函数（2026-10-05）。
 *
 * 为什么需要单独一个 .c：本仓的测试习惯是"#include 目标 .c 直接编译"，
 * 因此各测试文件都自带一份 esp_get_free_heap_size 定义。但 2026-10-05 起
 * 有三处生产代码开始在错误路径上打印内存实况（用于区分"总量不够"与
 * "碎片导致没有连续块"）：
 *   - components/bus_dma/bus_dma.c      （uart_driver_install 失败时）
 *   - components/scheduler/scheduler.c  （任务创建失败时）
 *   - components/bus_manager/bus_manager.c（逐总线差分）
 *   - main/config_apply_transaction.c   （逐步骤差分）
 * 于是多个测试目标都需要这几个符号，但它们的源文件里并没有定义。
 *
 * 这里提供一份**弱符号**实现作为兜底：测试文件若自带同名定义（非弱），
 * 链接器优先用测试文件的那份，行为不变；没定义的则由这里补上。
 * 返回恒定的充裕值，使这些诊断分支在宿主机测试中不因"内存不足"误触发。 */
#include <stddef.h>

__attribute__((weak)) unsigned long esp_get_free_heap_size(void) { return 200000; }
__attribute__((weak)) unsigned long esp_get_minimum_free_heap_size(void) { return 180000; }
__attribute__((weak)) size_t heap_caps_get_free_size(unsigned caps) { (void)caps; return 200000; }
__attribute__((weak)) size_t heap_caps_get_largest_free_block(unsigned caps) { (void)caps; return 65536; }
__attribute__((weak)) size_t heap_caps_get_minimum_free_size(unsigned caps) { (void)caps; return 180000; }
