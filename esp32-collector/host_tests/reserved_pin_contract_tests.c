#include <inttypes.h>
#include <stdbool.h>
#include <stdio.h>
#include <string.h>

#include "frame_codec.h"
#include "hw_profile.h"
#include "hw_tables.h"

static int s_failures;

#define CHECK(condition, ...) do { \
    if (!(condition)) { \
        fprintf(stderr, "FAIL %s:%d: ", __func__, __LINE__); \
        fprintf(stderr, __VA_ARGS__); \
        fprintf(stderr, "\n"); \
        s_failures++; \
        return; \
    } \
} while (0)

size_t dma_pool_serialize(dma_pool_t *pool, uint8_t *buf, size_t buf_size)
{
    (void)pool; (void)buf; (void)buf_size;
    return 0;
}

/* 不变式 1：BOOT 按键引脚永远不可分配给用户外设。
 *
 * 两种"挡住"机制都算数，但必须命中其一：
 *   机制 A：引脚在 hw_gpios 表里但带 HW_GPIO_FLAG_RESERVED（S3 的 GPIO0）；
 *   机制 B：引脚根本不在表里（C6 的 GPIO9，从未列入）。
 *
 * 只断言其中一种，另一个 target 上就是空断言 —— 那正是 2026-10-04 事故的
 * 形态："C6 上全绿"被当成了"S3 上也没事"。 */
static void test_boot_pin_is_never_allocatable(void)
{
    bool in_table = false;
    bool flagged = false;
    for (int i = 0; i < HW_GPIO_COUNT; i++) {
        if (hw_gpios[i].pin != HW_RESERVED_BOOT) continue;
        in_table = true;
        if (hw_gpio_is_reserved(&hw_gpios[i])) flagged = true;
    }
    CHECK(!in_table || flagged,
          "BOOT 引脚 (pin=%u) 在 hw_gpios 表里却没带 reserved 标记：它可以被配成 "
          "GPIO/PWM 输出，从而伪装成按键长按，触发 NVS 擦除 + 重启",
          (unsigned)HW_RESERVED_BOOT);
}

/* 机制生效性自检：避免"两个 target 都靠机制 B 侥幸通过"。 */
static void test_reserved_flag_mechanism_is_actually_used(void)
{
    int reserved_count = 0;
    for (int i = 0; i < HW_GPIO_COUNT; i++) {
        if (hw_gpio_is_reserved(&hw_gpios[i])) reserved_count++;
    }
#ifdef CONFIG_IDF_TARGET_ESP32S3
    CHECK(reserved_count >= 1,
          "S3 上没有任何 reserved GPIO：GPIO0(BOOT) 的防护已失效");
#else
    CHECK(reserved_count == 0,
          "C6 上出现了 %d 个 reserved GPIO，但 C6 的 BOOT 引脚本就不在表里："
          "不要用 reserved 标记去替代机制 B", reserved_count);
#endif
}

/* 不变式 2：保留引脚不得出现在 ResourceReport 里。
 *
 * 服务端只允许配置"节点上报过的引脚"（handler_periph.go 的
 * validateReportedGPIO）。让保留引脚不出现在上报里，就使用户根本选不中它。
 * 断言打在**编码结果**上，而不是 hw_gpios 表的 flags 位 —— 后者与"是否真的
 * 没上报"是两件事，中间还隔着 hw_profile_build_report 的过滤循环。 */
static void test_reserved_pins_absent_from_resource_report(void)
{
    uint8_t report[4096];
    size_t report_len = 0;
    CHECK(hw_profile_build_report(report, sizeof(report), &report_len, NULL, NULL, 0),
          "ResourceReport 编码失败");

    /* 字段路径：顶层 field3 = buses_blob，其内 field4 = gpio_entry(repeated)。
     * 顶层 field4 是 channels_blob，别抄错。 */
    frame_decoder_t top;
    frame_field_t field;
    const uint8_t *buses_blob = NULL;
    size_t buses_len = 0;
    CHECK(frame_decoder_init(&top, report, report_len) == FRAME_OK,
          "ResourceReport 解码失败");
    while (frame_decoder_next(&top, &field) == FRAME_OK) {
        if (field.field_num == 3) {
            CHECK(frame_field_get_bytes(&field, &buses_blob, &buses_len) == FRAME_OK,
                  "buses blob 解码失败");
        }
    }
    CHECK(buses_blob != NULL, "ResourceReport 里没有 buses 字段");

    bool reported[HW_GPIO_COUNT];
    memset(reported, 0, sizeof(reported));
    int entries = 0;

    /* repeated 字段必须边取边解：把 blob 指针存到循环外会被下一次迭代覆盖，
     * 最终只剩最后一个 entry 被检查（对前几个引脚完全失明）。
     *
     * 解码形状：encode_gpio_entry 之后调用方用
     *   frame_encode_bytes(&enc, 4, entry_buf + 1, entry_len - 1)
     * 把 entry 字段平铺进 field4 的载荷，所以每个 field4 的字节里是
     *   (field1 = string id, field2 = varint pin)，而不是再包一层子消息。 */
    frame_decoder_t buses;
    CHECK(frame_decoder_init_sub(&buses, buses_blob, buses_len) == FRAME_OK,
          "buses 嵌套解码失败");
    while (frame_decoder_next(&buses, &field) == FRAME_OK) {
        if (field.field_num != 4) continue;

        const uint8_t *entry_ptr = NULL;
        size_t entry_len = 0;
        CHECK(frame_field_get_bytes(&field, &entry_ptr, &entry_len) == FRAME_OK,
              "gpio entry 解码失败");

        frame_decoder_t entry;
        CHECK(frame_decoder_init_sub(&entry, entry_ptr, entry_len) == FRAME_OK,
              "gpio entry 嵌套解码失败");
        uint64_t pin = UINT64_MAX;
        bool saw_id = false;
        frame_field_t ef;
        while (frame_decoder_next(&entry, &ef) == FRAME_OK) {
            if (ef.field_num == 1) { saw_id = true; continue; }
            if (ef.field_num == 2) {
                CHECK(frame_field_get_varint(&ef, &pin) == FRAME_OK, "pin 不是 varint");
            }
        }
        CHECK(saw_id, "gpio entry 里没有 id 字段：解码形状与编码不符");
        CHECK(pin != UINT64_MAX, "gpio entry 里没有 pin 字段");

        int idx = -1;
        for (int i = 0; i < HW_GPIO_COUNT; i++) {
            if (hw_gpios[i].pin == (uint8_t)pin) { idx = i; break; }
        }
        CHECK(idx >= 0, "上报了不在 hw_gpios 表里的引脚 pin=%" PRIu64, pin);
        CHECK(!hw_gpio_is_reserved(&hw_gpios[idx]),
              "保留引脚 %s (pin=%" PRIu64 ") 被写进了资源上报 —— 服务端会把它当"
              "可分配引脚，用户配上去就会触发 BOOT 按键语义（2026-10-04 事故）",
              hw_gpios[idx].id, pin);
        reported[idx] = true;
        entries++;
    }
    CHECK(entries > 0, "一个 gpio entry 都没解出来：用例前提不成立");

    /* 反向断言：每个非保留引脚都必须被上报（过滤不能误伤可用资源）。 */
    int expected = 0;
    for (int i = 0; i < HW_GPIO_COUNT; i++) {
        if (hw_gpio_is_reserved(&hw_gpios[i])) continue;
        expected++;
        CHECK(reported[i], "非保留引脚 %s 没有被上报（过滤误伤）", hw_gpios[i].id);
    }
    CHECK(entries == expected,
          "上报的引脚数(%d) != 非保留引脚数(%d)：过滤把该留的滤掉了，或多报了",
          entries, expected);
}

int main(void)
{
    printf("target=%s HW_GPIO_COUNT=%d HW_RESERVED_BOOT=%u\n",
           HW_PLATFORM_STRING, HW_GPIO_COUNT, (unsigned)HW_RESERVED_BOOT);
    test_boot_pin_is_never_allocatable();
    test_reserved_flag_mechanism_is_actually_used();
    test_reserved_pins_absent_from_resource_report();
    if (s_failures != 0) {
        fprintf(stderr, "%d test(s) failed\n", s_failures);
        return 1;
    }
    printf("reserved_pin_contract_tests: all tests passed\n");
    return 0;
}