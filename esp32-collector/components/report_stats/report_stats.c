/**
 * @file report_stats.c
 * @brief 上报路径统计量实现（无 IDF 依赖，宿主可测）
 */
#include "report_stats.h"

#include <stddef.h>   /* NULL */

/* 与 bus_worker 原有实现同款：生产者与消费者可能在不同任务上，
 * 用 __atomic_* 而非普通读写（原代码即如此，此处保持语义不变）。 */
static volatile uint32_t s_queue_high_water;
static volatile uint32_t s_drop_count;
static uint32_t (*s_watermark_provider)(void);

void report_stats_note_queue_depth(uint32_t queued)
{
    uint32_t old = __atomic_load_n(&s_queue_high_water, __ATOMIC_RELAXED);
    while (queued > old &&
           !__atomic_compare_exchange_n(&s_queue_high_water, &old, queued, false,
                                        __ATOMIC_RELAXED, __ATOMIC_RELAXED)) {
        /* CAS 失败时 old 已被更新为当前值，循环继续比较 */
    }
}

void report_stats_note_drop(void)
{
    __atomic_add_fetch(&s_drop_count, 1, __ATOMIC_RELAXED);
}

void report_stats_reset(void)
{
    s_queue_high_water = 0;
    s_drop_count = 0;
    /* 刻意【不】清 provider：它是"接线"而不是"统计量"，
     * reset 的是数据，不是装配。 */
}

uint32_t report_stats_get_queue_high_water(void)
{
    return __atomic_load_n(&s_queue_high_water, __ATOMIC_RELAXED);
}

uint32_t report_stats_get_drop_count(void)
{
    return __atomic_load_n(&s_drop_count, __ATOMIC_RELAXED);
}

void report_stats_set_stack_watermark_provider(uint32_t (*fn)(void))
{
    s_watermark_provider = fn;
}

uint32_t report_stats_get_min_stack_watermark(void)
{
    if (s_watermark_provider == NULL) {
        /* 未接线：返回"没有低水位"，【不是】0（0 会被读成栈耗尽）。 */
        return UINT32_MAX;
    }
    return s_watermark_provider();
}

bool report_stats_has_stack_watermark_provider(void)
{
    return s_watermark_provider != NULL;
}
