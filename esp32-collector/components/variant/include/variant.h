/**
 * @file variant.h
 * @brief 型号能力描述 —— 唯一型号知识来源（设计原则 P8）
 *
 * 设计文档：docs/设计/ESP32-与后端-3.0-接口与模块设计-2026-10-06.md 1.13
 *
 * 规则（P8）：型号差异【只】影响"资源放置与尺寸"，绝不影响"可观测行为"。
 * 本文件是【数据】而非 #ifdef 森林：新增型号 = 加一行表项，不改控制流。
 *
 * 本头文件【不依赖 IDF】，可在宿主编译并测试（约束 C2）。
 */
#ifndef EHOME_VARIANT_H
#define EHOME_VARIANT_H

#include <stdbool.h>
#include <stdint.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef enum {
    VARIANT_S3 = 0,   /* ESP32-S3 无 PSRAM */
    VARIANT_S3P,      /* ESP32-S3 + PSRAM（现场 S3 实为此型） */
    VARIANT_C6,       /* ESP32-C6（无 PSRAM） */
    VARIANT_COUNT
} variant_id_t;

typedef struct {
    const char *name;
    variant_id_t id;

    bool     has_psram;
    uint32_t psram_bytes;

    /* 预算断言用（P5）。数值来源见 variant.c 注释中的实测出处。 */
    uint32_t internal_heap_bytes;

    /* 放置判据：> 此值的【连续】内部需求必失败。
     * 注意这是 largest 而非 free —— OTA 8KB 连续块事故即此类。 */
    uint32_t internal_contiguous_max;

    bool     has_iram_segment;  /* S3/S3P=1；C6 无独立 IRAM 段 */
    uint8_t  max_channels;
} variant_caps_t;

/** 构建期选定的型号能力（运行期只读）。 */
const variant_caps_t *variant_caps(void);

/** 显式取任一型号能力 —— 让三型号差异都能在【宿主】上被测试。 */
const variant_caps_t *variant_caps_for(variant_id_t id);

/** 按名字查（测试与诊断用）；未知返回 NULL，不静默兜底。 */
const variant_caps_t *variant_caps_by_name(const char *name);

/** 编译期选中的型号 id（供断言使用）。 */
variant_id_t variant_selected(void);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_VARIANT_H */
