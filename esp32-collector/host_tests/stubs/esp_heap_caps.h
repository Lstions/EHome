#ifndef HOST_TEST_ESP_HEAP_CAPS_H
#define HOST_TEST_ESP_HEAP_CAPS_H

#include <stddef.h>

/* 宿主机桩：scheduler.c 自 2026-10-05 起在任务创建失败时打印**内存实况**
 * （free / largest block / min ever，整体与 INTERNAL 各一组），用于区分
 * "总量不够" 与 "碎片导致没有连续块"。这里给出与 IDF 同名的声明，
 * 实现由各测试文件提供（与 esp_get_free_heap_size 的既有做法一致）。 */
#define MALLOC_CAP_8BIT      (1 << 2)
#define MALLOC_CAP_INTERNAL  (1 << 11)

size_t heap_caps_get_free_size(unsigned caps);
size_t heap_caps_get_largest_free_block(unsigned caps);

/* esp_get_free_heap_size / esp_get_minimum_free_heap_size 由各测试文件
 * 自行定义（本仓既有做法），这里只补声明以便 scheduler.c 编译。 */
size_t esp_get_free_heap_size(void);
size_t esp_get_minimum_free_heap_size(void);

#endif /* HOST_TEST_ESP_HEAP_CAPS_H */