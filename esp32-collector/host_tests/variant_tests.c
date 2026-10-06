/* variant_tests.c —— 型号能力表（设计文档 1.13；原则 P8） */
#include "variant.h"
#include "config_mgr.h"   /* MAX_CHANNELS：与 variant 的 max_channels 是同一事实 */

#include <assert.h>
#include <stdio.h>
#include <string.h>

static int s_failures = 0;

#define CHECK(cond)                                                          \
    do {                                                                     \
        if (!(cond)) {                                                       \
            printf("FAIL %s:%d  %s\n", __FILE__, __LINE__, #cond);          \
            s_failures++;                                                    \
        }                                                                    \
    } while (0)

/* 1) 三个型号都能取到，且字段自洽 */
static void test_all_variants_exist(void)
{
    for (int i = 0; i < VARIANT_COUNT; i++) {
        const variant_caps_t *c = variant_caps_for((variant_id_t)i);
        CHECK(c != NULL);
        CHECK(c->name != NULL && c->name[0] != '\0');
        CHECK(c->id == (variant_id_t)i);
        CHECK(c->internal_heap_bytes > 0);
        CHECK(c->internal_contiguous_max > 0);
        CHECK(c->max_channels > 0);
        /* 放置判据必须自洽：连续块不可能大于总堆 */
        CHECK(c->internal_contiguous_max <= c->internal_heap_bytes);
    }
}

/* 2) PSRAM 与 bytes 字段必须一致 —— 不许"说有 PSRAM 但 0 字节" */
static void test_psram_fields_consistent(void)
{
    for (int i = 0; i < VARIANT_COUNT; i++) {
        const variant_caps_t *c = variant_caps_for((variant_id_t)i);
        CHECK(c->has_psram == (c->psram_bytes > 0));
    }
}

/* 3) 只有 s3p 有 PSRAM（当前产品事实；若将来变化，此断言会红以提醒更新设计） */
static void test_only_s3p_has_psram(void)
{
    CHECK(variant_caps_for(VARIANT_S3)->has_psram == false);
    CHECK(variant_caps_for(VARIANT_S3P)->has_psram == true);
    CHECK(variant_caps_for(VARIANT_C6)->has_psram == false);
}

/* 4) IRAM 段：C6 无（S3/S3P 有）—— 决定"ISR 可达代码"能否驻留 */
static void test_iram_segment_presence(void)
{
    CHECK(variant_caps_for(VARIANT_S3)->has_iram_segment == true);
    CHECK(variant_caps_for(VARIANT_S3P)->has_iram_segment == true);
    CHECK(variant_caps_for(VARIANT_C6)->has_iram_segment == false);
}

/* 5) 通道数：S3/S3P=5，C6=4（产品硬需求） */
static void test_channel_counts(void)
{
    CHECK(variant_caps_for(VARIANT_S3)->max_channels == 5);
    CHECK(variant_caps_for(VARIANT_S3P)->max_channels == 5);
    CHECK(variant_caps_for(VARIANT_C6)->max_channels == 4);
}

/* 5b) ⚠ 通道数有**两处**定义，必须证明它们一致（P4：一个语义一处定义）
 *
 *   - `config_mgr.h` 的 `MAX_CHANNELS`（编译期宏，按 CONFIG_IDF_TARGET_* 分支）
 *   - `variant.c` 的 `max_channels`（运行期型号能力表）
 *
 * 两者今天数值相同，但**没有任何东西在保证它们相同**：
 * 改一处、忘另一处 ⇒ scheduler 的 SCHED_MAX_CHANNELS（由 MAX_CHANNELS 派生）、
 * hw_profile 上报给后端的 manifest_capacity（读 MAX_CHANNELS）、
 * 与型号能力表（读 variant）三处对"这台设备有几个通道"给出**不同答案**，
 * 而每一处单独看都自洽。
 *
 * 这条断言把两者钉在一起：**数值必须相等**。
 * 真正的单一来源是后一步的事（把 MAX_CHANNELS 改为读 variant），
 * 但在那之前，至少让不一致**立刻变红**，而不是等到现场才发现。
 *
 * 注意口径：本用例编译时带了某个 CONFIG_IDF_TARGET_*（见 CMakeLists），
 * 所以这里比对的是"**当前构建目标**的 MAX_CHANNELS"与"**同一目标**的型号能力"。
 * 这正是生产中会同时生效的那一对。 */
static void test_max_channels_matches_variant_caps(void)
{
    variant_id_t sel = variant_selected();
    const variant_caps_t *c = variant_caps();
    CHECK(c != NULL);
    if (c == NULL) return;

    /* 本用例被编译【三份】（见 CMakeLists）：每份注入一个 CONFIG_IDF_TARGET_*，
     * 于是三种型号下"MAX_CHANNELS 的编译分支"与"型号能力表"都被真正比对到。
     * 单份构建只能覆盖一个分支 —— 那正是"改一处忘另一处"最容易漏的形态。 */
#if defined(CONFIG_IDF_TARGET_ESP32S3)
    /* S3 与 S3P 共用同一个 IDF target 宏，靠 PSRAM 区分；两者通道数相同（5）。 */
    CHECK(sel == VARIANT_S3 || sel == VARIANT_S3P);
    CHECK((int)c->max_channels == (int)MAX_CHANNELS);
#elif defined(CONFIG_IDF_TARGET_ESP32C6)
    CHECK(sel == VARIANT_C6);
    CHECK((int)c->max_channels == (int)MAX_CHANNELS);
#else
    printf("FAIL %s:%d  本用例必须带 CONFIG_IDF_TARGET_* 编译，",
           __FILE__, __LINE__);
    printf("否则 MAX_CHANNELS 走宿主 fallback 8，与型号能力表的比对会被跳过（假绿）\n");
    s_failures++;
#endif
}

/* 6) 越界/未知【不静默兜底】—— 返回 NULL 而不是"默认型号"（P3 精神） */
static void test_no_silent_fallback(void)
{
    CHECK(variant_caps_for((variant_id_t)VARIANT_COUNT) == NULL);
    CHECK(variant_caps_for((variant_id_t)-1) == NULL);
    CHECK(variant_caps_by_name("nope") == NULL);
    CHECK(variant_caps_by_name(NULL) == NULL);
}

/* 7) 按名字查得到 */
static void test_by_name(void)
{
    CHECK(variant_caps_by_name("s3") == variant_caps_for(VARIANT_S3));
    CHECK(variant_caps_by_name("s3p") == variant_caps_for(VARIANT_S3P));
    CHECK(variant_caps_by_name("c6") == variant_caps_for(VARIANT_C6));
}

/* 8) variant_caps() 必须等于编译期选中的那一行 */
static void test_selected_is_consistent(void)
{
    CHECK(variant_caps() == variant_caps_for(variant_selected()));
    CHECK(variant_selected() >= 0 && variant_selected() < VARIANT_COUNT);
}

/* 9) 实测数字锁定（P6/P7）：改动这些数字必须是有意的，且要更新设计文档 */
static void test_measured_numbers(void)
{
    const variant_caps_t *s3p = variant_caps_for(VARIANT_S3P);
    CHECK(s3p->psram_bytes == 8388608u);          /* 8 MiB 实测 */
    CHECK(s3p->internal_contiguous_max == 23552u);/* largest 实测 */
    CHECK(variant_caps_for(VARIANT_C6)->internal_heap_bytes == 353280u); /* 345 KiB */
    CHECK(variant_caps_for(VARIANT_C6)->internal_contiguous_max == 88064u);
}

int main(void)
{
    test_all_variants_exist();
    test_psram_fields_consistent();
    test_only_s3p_has_psram();
    test_iram_segment_presence();
    test_channel_counts();
    test_max_channels_matches_variant_caps();
    test_no_silent_fallback();
    test_by_name();
    test_selected_is_consistent();
    test_measured_numbers();

    if (s_failures != 0) {
        printf("variant_tests: %d FAILURE(S)\n", s_failures);
        return 1;
    }
    printf("variant_tests: all checks passed\n");
    return 0;
}
