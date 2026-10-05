#ifndef HOST_TEST_ESP_HEAP_CAPS_H
#define HOST_TEST_ESP_HEAP_CAPS_H

#include <stddef.h>

/* 宿主机桩：scheduler.c 自 2026-10-05 起在任务创建失败时打印**内存实况**
 * （free / largest block / min ever，整体与 INTERNAL 各一组），用于区分
 * "总量不够" 与 "碎片导致没有连续块"。这里给出与 IDF 同名的声明，
 * 实现由各测试文件提供（与 esp_get_free_heap_size 的既有做法一致）。 */
#define MALLOC_CAP_8BIT      (1 << 2)
#define MALLOC_CAP_INTERNAL  (1 << 11)

/* 只用 heap_caps_* 这一族：签名统一为 size_t f(unsigned caps)，
 * 且没有任何既有测试文件定义过它们，因此不会出现类型冲突。
 *
 * 刻意**不使用** esp_get_free_heap_size / esp_get_minimum_free_heap_size：
 * 本仓 6 个测试文件各自定义过它们，且返回类型既有 uint32_t 也有 size_t，
 * 任何声明都会与其中一半冲突（实测 "conflicting types"）。
 * 生产代码里的等价替换：
 *   esp_get_free_heap_size()          -> heap_caps_get_free_size(MALLOC_CAP_8BIT)
 *   esp_get_minimum_free_heap_size()  -> heap_caps_get_minimum_free_size(MALLOC_CAP_8BIT)
 * 两者在 IDF 中本就互为封装，数值语义一致。 */
size_t heap_caps_get_free_size(unsigned caps);
size_t heap_caps_get_largest_free_block(unsigned caps);
size_t heap_caps_get_minimum_free_size(unsigned caps);

/* 这里**故意不声明** esp_get_free_heap_size / esp_get_minimum_free_heap_size。
 *
 * 原因：各测试文件对这两个函数的返回类型并不统一（既有 uint32_t 也有 size_t，
 * 本仓历史遗留），任何在此处写下的声明都会与其中一半的定义冲突 ——
 * 实测 "conflicting types for esp_get_free_heap_size"（按 size_t 声明时）
 * 或 long unsigned int（按 unsigned long 声明时）都会触发。
 *
 * 这两个符号改由 stubs/heap_stub_impl.c 提供**弱定义**兜底：
 *   - 测试文件自带同名强定义时，链接器优先用测试那份，行为完全不变；
 *   - 没定义的测试目标由弱定义补上，恒返回充裕值。
 * 这样既不需要在头里声明，也不再要求各测试文件统一返回类型。
 *
 * 新增测试建议统一用 size_t，但这不是硬性要求。 */

#endif /* HOST_TEST_ESP_HEAP_CAPS_H */