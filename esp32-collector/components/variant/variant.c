/**
 * @file variant.c
 * @brief 型号能力表 —— 数值均来自实测/门禁输出，出处见注释（原则 P6/P7）
 */
#include "variant.h"

#include <string.h>

/* 实测出处（2026-10-06）：
 *   S3  内部堆 306,155 B = boot log "_heap_start 3FCAC080 245K + ..."（299.0 KiB）
 *   S3  largest 23,552 B（整机稳态，见内存门禁输出）
 *   S3  IRAM iram0_0_seg ORIGIN 0x40374000 LENGTH 0x00057700 = 358,144 B
 *   S3P = S3 + PSRAM 8,388,608 B（实测仅用 53.8 KiB）
 *   C6  内部堆 345.0 KiB（boot log）；largest 88,064 B；【无独立 IRAM 段】
 *       （链接脚本用 iram_text_seg，代码主要在 flash）
 *
 * 这些数字是【设计依据】，必须与 tools/mem_budget.json 保持一致 ——
 * 后续增量会加一条 CI 断言锁住这个一致性（设计文档 1.13.7）。 */
static const variant_caps_t s_caps[VARIANT_COUNT] = {
    [VARIANT_S3] = {
        .name = "s3", .id = VARIANT_S3,
        .has_psram = false, .psram_bytes = 0,
        .internal_heap_bytes = 306155,
        .internal_contiguous_max = 23552,
        .has_iram_segment = true,
        .max_channels = 5,
    },
    [VARIANT_S3P] = {
        .name = "s3p", .id = VARIANT_S3P,
        .has_psram = true, .psram_bytes = 8388608,
        .internal_heap_bytes = 306155,
        .internal_contiguous_max = 23552,
        .has_iram_segment = true,
        .max_channels = 5,
    },
    [VARIANT_C6] = {
        .name = "c6", .id = VARIANT_C6,
        .has_psram = false, .psram_bytes = 0,
        .internal_heap_bytes = 353280,
        .internal_contiguous_max = 88064,
        .has_iram_segment = false,
        .max_channels = 4,
    },
};

/* 选择发生在【构建期】，但选择的是"哪一行数据"，不是"哪一段代码"。
 * VARIANT_SELECTED 可由 CMake 注入；未注入时按 IDF 目标推断。 */
#ifndef VARIANT_SELECTED
#  if defined(CONFIG_IDF_TARGET_ESP32C6)
#    define VARIANT_SELECTED VARIANT_C6
#  elif defined(CONFIG_COLLECTOR_PSRAM) && CONFIG_COLLECTOR_PSRAM
#    define VARIANT_SELECTED VARIANT_S3P
#  else
#    define VARIANT_SELECTED VARIANT_S3
#  endif
#endif

const variant_caps_t *variant_caps(void)
{
    return &s_caps[VARIANT_SELECTED];
}

variant_id_t variant_selected(void)
{
    return (variant_id_t)VARIANT_SELECTED;
}

const variant_caps_t *variant_caps_for(variant_id_t id)
{
    if ((int)id < 0 || id >= VARIANT_COUNT) return NULL;  /* 不静默兜底 */
    return &s_caps[id];
}

const variant_caps_t *variant_caps_by_name(const char *name)
{
    if (name == NULL) return NULL;
    for (int i = 0; i < VARIANT_COUNT; i++) {
        if (strcmp(s_caps[i].name, name) == 0) return &s_caps[i];
    }
    return NULL;
}
