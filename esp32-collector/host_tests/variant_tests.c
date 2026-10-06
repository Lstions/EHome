/* variant_tests.c —— 型号能力表（设计文档 1.13；原则 P8） */
#include "variant.h"

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
