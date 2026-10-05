/**
 * @file mem_guard.c
 * @brief 运行期内存水位门禁实现（最小实现，WS-E/OTA/log_stream 的接口点）。
 *
 * 设计边界（Lead 2026-10-05 裁决）：
 *   - 本模块只提供"读取水位 + 谓词 + 低水位回调 + 报告编码"，不执行任何
 *     熔断动作；熔断动作（关 log stream、暂停 OTA、降级遥测）由调用方在
 *     low callback 里决策，避免把策略耦合进门禁。
 *   - `mem_guard_can_start()` 是纯谓词，不触发回调、不改状态。
 *   - 回调只在 `mem_guard_poll()` 中触发，且带迟滞（见 MEM_GUARD_HYSTERESIS），
 *     避免水位在阈值附近抖动造成事件风暴。
 *
 * 数值依据：方案 §2.1/§2.3（s3p ≥ 16 KiB、s3 ≥ 8 KiB、c6 ≥ 12 KiB）。
 */

#include "mem_guard.h"
#include "frame_codec.h"
#include "esp_heap_caps.h"

#include <string.h>

/* 门禁口径：只认**内部 RAM**。
 *
 * 为什么必须显式并上 MALLOC_CAP_INTERNAL（2026-10-05 实机缺陷）：
 *   开了 CONFIG_SPIRAM_USE_MALLOC 的 s3p 上，MALLOC_CAP_8BIT 是"内部 RAM +
 *   PSRAM"的合计，heap_caps_get_largest_free_block(MALLOC_CAP_8BIT) 跨两个堆
 *   取最大值，于是 largest 恒等于 PSRAM 的 8 MB 连续块。实测 s3p-n16 的
 *   MemReport 为 free=8333027 largest=8257536 —— 内部 RAM 水位完全没有反映在
 *   里面，can_start() 恒 true、低水位回调永不触发。门禁在最需要它的型号上
 *   完全失效（PSRAM 型号恰恰是最会耗尽内部 RAM 的一类）。
 *
 * 为什么是内部 RAM 而不是合计：本模块所有消费点（配置事务各步、UART 驱动
 *   install、OTA、log_stream、任务栈）要的都是**内部 RAM 的连续块**——任务栈与
 *   DMA/ISR 缓冲必须内部，且 flash 写（OTA/NVS）期间 cache 关闭、访问 PSRAM 会崩。
 *   floor 的语义本来就是"内部 RAM 水位"。
 *
 * 无 PSRAM 的 s3/c6 只有内部堆，加上 INTERNAL 后取值与改动前逐字节一致，
 *   因此这是纯修复、不改这两个型号的行为。 */
#define MEM_GUARD_CAPS  (MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT)

/* 硬地板：低于此值绝不启动任何重操作（配置事务/OTA/log_stream/大块分配）。
 * 判决依据是 largest（连续块），不是 free —— 见调试方法论 §4.1。 */
#if defined(CONFIG_COLLECTOR_PSRAM) && CONFIG_COLLECTOR_PSRAM
#define MEM_GUARD_FLOOR_BYTES       (16u * 1024u)
#elif defined(CONFIG_IDF_TARGET_ESP32S3)
#define MEM_GUARD_FLOOR_BYTES       (8u * 1024u)
#elif defined(CONFIG_IDF_TARGET_ESP32C6)
#define MEM_GUARD_FLOOR_BYTES       (12u * 1024u)
#else
/* 宿主测试/未知目标：取最保守的 8 KiB；测试用例会显式定义目标宏。 */
#define MEM_GUARD_FLOOR_BYTES       (8u * 1024u)
#endif

/* 回弹迟滞：largest 必须回到 floor + 2 KiB 以上，才允许下一次低水位事件。
 * 这不是拍脑袋：floor=8 KiB 时，一次 8 KiB 的分配失败/释放会让 largest
 * 在 7-9 KiB 之间来回跳，若无迟滞会每秒触发一次回调。 */
#define MEM_GUARD_HYSTERESIS_BYTES  (2u * 1024u)

static mem_guard_low_cb_t s_low_cb;
static bool              s_low_latched;
static size_t            s_min_stack_high_water_bytes;

size_t mem_guard_free(void)
{
    return heap_caps_get_free_size(MEM_GUARD_CAPS);
}

size_t mem_guard_largest(void)
{
    return heap_caps_get_largest_free_block(MEM_GUARD_CAPS);
}

size_t mem_guard_min_ever(void)
{
    return heap_caps_get_minimum_free_size(MEM_GUARD_CAPS);
}

size_t mem_guard_floor_bytes(void)
{
    return (size_t)MEM_GUARD_FLOOR_BYTES;
}

bool mem_guard_can_start(size_t need_bytes)
{
    const size_t floor = (size_t)MEM_GUARD_FLOOR_BYTES;
    const size_t required = (need_bytes > floor) ? need_bytes : floor;
    return mem_guard_largest() >= required;
}

void mem_guard_register_low_cb(mem_guard_low_cb_t cb)
{
    s_low_cb = cb;
}

void mem_guard_poll(void)
{
    const size_t largest = mem_guard_largest();
    const size_t floor = (size_t)MEM_GUARD_FLOOR_BYTES;

    if (!s_low_latched) {
        if (largest < floor) {
            s_low_latched = true;
            if (s_low_cb != NULL) {
                s_low_cb(mem_guard_free(), largest);
            }
        }
        return;
    }

    if (largest >= floor + (size_t)MEM_GUARD_HYSTERESIS_BYTES) {
        s_low_latched = false;
    }
}

void mem_guard_reset_latch(void)
{
    s_low_latched = false;
}

void mem_guard_set_min_stack_high_water(size_t bytes)
{
    /* 记录"历史最小剩余"：uxTaskGetStackHighWaterMark 本身已经是每个任务
     * 的历史最小值，跨任务取 min 后再跨时间取 min，避免任务重建（如
     * scheduler）把该指标重新抬高而掩盖曾经的紧张。
     *
     * 0 = "尚未采样" 的哨兵值，作为输入是 no-op；否则测试/初始化路径会把
     * 它误当成"0 字节剩余"从而永久污染该指标。 */
    if (bytes == 0) return;
    if (s_min_stack_high_water_bytes == 0 || bytes < s_min_stack_high_water_bytes) {
        s_min_stack_high_water_bytes = bytes;
    }
}

size_t mem_guard_min_stack_high_water(void)
{
    return s_min_stack_high_water_bytes;
}

size_t mem_guard_encode_report(uint8_t *buf, size_t cap)
{
    if (buf == NULL || cap < 2) return 0;

    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, cap, MSG_MEM_RPT);

    const uint64_t fields[5] = {
        (uint64_t)mem_guard_free(),
        (uint64_t)mem_guard_largest(),
        (uint64_t)mem_guard_min_ever(),
        (uint64_t)mem_guard_min_stack_high_water(),
        (uint64_t)mem_guard_floor_bytes(),
    };
    for (uint8_t i = 0; i < 5; i++) {
        if (frame_encode_varint(&enc, (uint8_t)(i + 1), fields[i]) != FRAME_OK) {
            return 0;
        }
    }
    return frame_encoder_size(&enc);
}
