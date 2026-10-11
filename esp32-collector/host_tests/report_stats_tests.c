/* report_stats_tests.c —— D-14：中立统计量组件的语义
 *
 * 这些断言的存在理由：为了让 msg_handler 不再 REQUIRES bus_worker
 * （破除唯一组件依赖环），把三个上报统计量搬到了本组件。
 * 搬迁【不能改变语义】—— 尤其是：
 *   - 高水位只升不降；
 *   - provider 未注册时 min_stack_watermark 返回 UINT32_MAX（"无低水位"），
 *     **不是 0** —— 0 在那个字段里读作"栈已耗尽"，会制造假的栈告警。
 */
#include "report_stats.h"

#include <stdio.h>
#include <string.h>

static int s_failures = 0;
#define CHECK(cond)                                                          \
    do {                                                                     \
        if (!(cond)) { printf("FAIL %s:%d  %s\n", __FILE__, __LINE__, #cond); s_failures++; } \
    } while (0)

/* 1) drop 计数累加；reset 清零 */
static void test_drop_count(void)
{
    report_stats_reset();
    CHECK(report_stats_get_drop_count() == 0);
    report_stats_note_drop();
    report_stats_note_drop();
    report_stats_note_drop();
    CHECK(report_stats_get_drop_count() == 3);
    report_stats_reset();
    CHECK(report_stats_get_drop_count() == 0);
}

/* 2) 高水位【只升不降】—— 这是"高水位"的定义 */
static void test_high_water_monotonic(void)
{
    report_stats_reset();
    CHECK(report_stats_get_queue_high_water() == 0);

    report_stats_note_queue_depth(5);
    CHECK(report_stats_get_queue_high_water() == 5);

    report_stats_note_queue_depth(3);      /* 降下去 */
    CHECK(report_stats_get_queue_high_water() == 5);   /* ← 高水位不动 */

    report_stats_note_queue_depth(2);
    CHECK(report_stats_get_queue_high_water() == 5);

    report_stats_note_queue_depth(9);      /* 再创新高 */
    CHECK(report_stats_get_queue_high_water() == 9);
}

/* 3) reset 会把高水位也清零 */
static void test_reset_clears_high_water(void)
{
    report_stats_reset();
    report_stats_note_queue_depth(7);
    CHECK(report_stats_get_queue_high_water() == 7);
    report_stats_reset();
    CHECK(report_stats_get_queue_high_water() == 0);
}

/* 4) 【关键】provider 未注册 => UINT32_MAX（无低水位），**不是 0**。
 *    返回 0 会被上游读成"栈已耗尽" ⇒ 假告警。 */
static void test_no_provider_reports_no_low_water(void)
{
    report_stats_set_stack_watermark_provider(NULL);
    CHECK(report_stats_has_stack_watermark_provider() == false);
    CHECK(report_stats_get_min_stack_watermark() == UINT32_MAX);
    CHECK(report_stats_get_min_stack_watermark() != 0);   /* 重点 */
}

/* 5) 注册 provider 后原样转发 */
static uint32_t s_fake_watermark = 123;
static uint32_t fake_provider(void) { return s_fake_watermark; }

static void test_provider_is_forwarded(void)
{
    report_stats_set_stack_watermark_provider(fake_provider);
    CHECK(report_stats_has_stack_watermark_provider() == true);

    s_fake_watermark = 123;
    CHECK(report_stats_get_min_stack_watermark() == 123);
    s_fake_watermark = 456;
    CHECK(report_stats_get_min_stack_watermark() == 456);

    /* 撤销注册 -> 回到"无低水位" */
    report_stats_set_stack_watermark_provider(NULL);
    CHECK(report_stats_get_min_stack_watermark() == UINT32_MAX);
}

/* 6) reset 只清数据，【不】动装配 —— provider 是接线而不是统计量 */
static void test_reset_does_not_clear_provider(void)
{
    report_stats_set_stack_watermark_provider(fake_provider);
    s_fake_watermark = 42;
    report_stats_note_queue_depth(4);
    report_stats_note_drop();

    report_stats_reset();

    CHECK(report_stats_get_queue_high_water() == 0);   /* 数据清了 */
    CHECK(report_stats_get_drop_count() == 0);
    CHECK(report_stats_has_stack_watermark_provider() == true);  /* 接线还在 */
    CHECK(report_stats_get_min_stack_watermark() == 42);
    report_stats_set_stack_watermark_provider(NULL);
}

int main(void)
{
    test_drop_count();
    test_high_water_monotonic();
    test_reset_clears_high_water();
    test_no_provider_reports_no_low_water();
    test_provider_is_forwarded();
    test_reset_does_not_clear_provider();
    if (s_failures) { printf("report_stats_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("report_stats_tests: all checks passed\n");
    return 0;
}
