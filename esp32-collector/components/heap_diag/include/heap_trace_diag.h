/* heap_trace_diag.h —— 堆分配归因工具（诊断镜像专用；默认不编译）
 *
 * 背景与理由见 heap_trace_diag.c 顶部注释（本卡四次"数值吻合"式归因全部打空的记录）。
 *
 * ⚠ 所有函数在未开 EHOME_MEM_DIAG + EHOME_HEAP_TRACE 时退化为**空实现**，
 *   因此调用点可以无条件书写，交付镜像不受影响。
 */
#pragma once

#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

/** 记录缓冲的实际落点（PSRAM 或内部 .bss）—— 如实报告，不静默。 */
const char *heap_trace_diag_where(void);

/** 装载记录缓冲。重复调用安全。返回是否可用。 */
bool heap_trace_diag_init(void);

/** 开始追踪（HEAP_TRACE_ALL）。未编译时为空实现。 */
void heap_trace_diag_start(const char *label);

/** 停止并 dump（含溢出告警）。未编译时为空实现。 */
void heap_trace_diag_stop_and_dump(const char *label);

#ifdef __cplusplus
}
#endif
