/*
 * mem_guard_tests.c — 运行期内存水位门禁的宿主机契约测试。
 *
 * 为什么必须有这组测试：
 *   mem_guard 是配置事务/OTA/log_stream 共同的启动判决。它的语义如果
 *   反了（例如误用 free 而不是 largest、或迟滞失效导致每秒回调），
 *   现场表现分别是"设备在内存充足时拒绝重操作"和"低内存事件风暴"，
 *   两者都很难从串口日志反推。这里用可控堆值把每条边界钉死。
 *
 * 覆盖：
 *   1. MSG_MEM_RPT(0x21) 类型号与冲突检查
 *   2. can_start = largest >= max(need, floor)，且只看 largest，不看 free
 *   3. **口径**：门禁只认内部 RAM —— PSRAM 很大而内部 RAM 低于 floor 时必须拒绝，
 *      且传给 heap_caps_* 的 caps 必须带 MALLOC_CAP_INTERNAL（2026-10-05 实机缺陷回归）
 *   4. 低水位回调只触发一次 + 迟滞回弹后才能再触发
 *   5. MemReport 0x21 字段 1..5 编码/解码 roundtrip，单位字节
 */

#include <stdio.h>
#include <string.h>
#include <stdint.h>
#include <stdbool.h>

#include "frame_codec.h"
/* 桩头文件：给出 MALLOC_CAP_* 与 heap_caps_* 声明（-I stubs）。 */
#include "esp_heap_caps.h"

/* 可控堆值：覆盖 stubs/heap_stub_impl.c 的弱定义。
 *
 * ⚠ 这里**必须区分 caps**（2026-10-05 缺陷的逃逸原因）。
 *
 * 2026-10-05 的实机缺陷是"caps 选错"：mem_guard 用 MALLOC_CAP_8BIT 在开了
 * CONFIG_SPIRAM_USE_MALLOC 的 s3p 上跨"内部 RAM + PSRAM"取最大值，于是
 * largest 恒等于 PSRAM 的 8 MB 连续块、门禁恒放行。旧桩把 caps 参数
 * `(void)` 掉、内部与 PSRAM 返回同一个值，因此"caps 选错"在宿主测试里
 * **完全不可见** —— 测试只证明了谓词逻辑对，没证明口径对。
 *
 * 桩的行为（关键）：**默认口径（不含 MALLOC_CAP_INTERNAL，即 s3p 上"内部 +
 * PSRAM 合计"）可以很大，而 INTERNAL 口径可以很小**。既有用例继续用
 * `s_largest/s_free/s_min_ever` 驱动"总量口径"，而 mem_guard 实际读的是
 * INTERNAL 口径 `s_internal_*`；两者默认同步，因此既有语义不变。
 * 一旦 mem_guard 回退到裸 MALLOC_CAP_8BIT，它会读到被刻意放大的总量值，
 * test_gate_uses_internal_caps_when_psram_is_large() 立即变红。 */
static size_t s_free = 200000;
static size_t s_largest = 65536;
static size_t s_min_ever = 100000;

/* 非 INTERNAL（"合计"）口径，只在需要制造"PSRAM 大 / 内部小"对照时使用；
 * 默认与内部口径相同，因此既有用例无需关心它。 */
static size_t s_total_free = 200000;
static size_t s_total_largest = 65536;
static size_t s_total_min_ever = 100000;

/* 记录最近一次调用收到的 caps，供"口径正确性"断言直接检查 —— 只测返回值
 * 的话，桩只要碰巧返回同一个数就仍可能漏掉 caps 错误。 */
static unsigned s_last_free_caps = 0;
static unsigned s_last_largest_caps = 0;
static unsigned s_last_min_ever_caps = 0;

#define CAPS_IS_INTERNAL(c)  (((c) & MALLOC_CAP_INTERNAL) != 0u)

size_t heap_caps_get_free_size(unsigned caps)
{
    s_last_free_caps = caps;
    return CAPS_IS_INTERNAL(caps) ? s_free : s_total_free;
}
size_t heap_caps_get_largest_free_block(unsigned caps)
{
    s_last_largest_caps = caps;
    return CAPS_IS_INTERNAL(caps) ? s_largest : s_total_largest;
}
size_t heap_caps_get_minimum_free_size(unsigned caps)
{
    s_last_min_ever_caps = caps;
    return CAPS_IS_INTERNAL(caps) ? s_min_ever : s_total_min_ever;
}

/* 把 INTERNAL 口径与"合计"口径设成同一个值 —— 既有用例的默认前提。 */
static void set_all_caps(size_t value)
{
    s_free = s_largest = s_min_ever = value;
    s_total_free = s_total_largest = s_total_min_ever = value;
}

/* 直接编入被测实现，便于驱动计数器/检查私有状态语义 */
#include "../main/mem_guard.c"

static int g_fail = 0;
static int g_pass = 0;

#define CHECK(cond, ...)                                                     \
    do {                                                                     \
        if (cond) {                                                          \
            g_pass++;                                                        \
        } else {                                                             \
            g_fail++;                                                        \
            printf("FAIL %s:%d: ", __FILE__, __LINE__);                      \
            printf(__VA_ARGS__);                                             \
            printf("\n");                                                    \
        }                                                                    \
    } while (0)

#define FLOOR mem_guard_floor_bytes()

static void test_type_and_collisions(void)
{
    /* V3-2a 裁决：0x20 让给 DataBatch，MSG_MEM_RPT 顺延到 0x21。 */
    CHECK(MSG_MEM_RPT == 0x21, "MSG_MEM_RPT should be 0x21, got 0x%02X", MSG_MEM_RPT);
    CHECK(MSG_MEM_RPT != MSG_STATUS_RPT, "mem report collides with status report");
    CHECK(MSG_MEM_RPT != MSG_DIAG_REPORT, "mem report collides with diag report");
    CHECK(MSG_MEM_RPT != MSG_DIAG_ACK, "mem report collides with diag ack");
    CHECK(MSG_MEM_RPT != MSG_DATA_BATCH, "mem report collides with data batch");
    CHECK(MSG_DATA_BATCH == 0x20, "MSG_DATA_BATCH should be 0x20, got 0x%02X", MSG_DATA_BATCH);
    CHECK(MSG_MEM_RPT == MSG_DATA_BATCH + 1, "MSG_MEM_RPT must follow MSG_DATA_BATCH (0x21)");
}

static void test_can_start_uses_largest_not_free(void)
{
    set_all_caps(4096);
    s_free = 200000;                    /* free 很大但 largest 小 = 碎片 */
    CHECK(!mem_guard_can_start(4096), "largest=4096 < floor must refuse even with huge free");
    CHECK(!mem_guard_can_start(1024), "largest=4096 < floor must refuse a small need");

    set_all_caps(FLOOR);                /* 恰好等于 floor */
    CHECK(mem_guard_can_start(1024), "largest==floor should allow need<=floor");

    set_all_caps(FLOOR - 1);
    CHECK(!mem_guard_can_start(1024), "largest just below floor must refuse (hard floor)");

    set_all_caps(32768);
    CHECK(mem_guard_can_start(32768), "need == largest should pass");
    CHECK(!mem_guard_can_start(32769), "need > largest must fail even if above floor");
    CHECK(mem_guard_can_start(0), "zero need only requires floor, not free");
}

static int s_low_calls = 0;
static size_t s_last_free = 0, s_last_largest = 0;

static void low_cb(size_t free_bytes, size_t largest_bytes)
{
    s_low_calls++;
    s_last_free = free_bytes;
    s_last_largest = largest_bytes;
}

/* 本任务的核心回归用例：**PSRAM 很大、内部 RAM 很小** 时必须拒绝。
 *
 * 这是 2026-10-05 实机缺陷的直接复现：s3p-n16 上
 *   MALLOC_CAP_8BIT          -> free=8333027 largest=8257536（PSRAM 污染）
 *   MALLOC_CAP_INTERNAL|8BIT -> 内部 RAM 真值（几十 KiB 量级）
 * 旧代码读前者，can_start() 恒 true。这里把"总量口径"设成远大于 floor、
 * "内部口径"设成小于 floor，断言门禁必须拒绝 —— 同时直接检查 mem_guard
 * 传给 heap_caps_* 的 caps 确实带 MALLOC_CAP_INTERNAL（否则桩返回的仍是
 * 那个巨大的总量值，说明口径没改对）。 */
static void test_gate_uses_internal_caps_when_psram_is_large(void)
{
    /* s3p 实机观测值的量级：合计口径 ~8.25 MB（PSRAM 污染值），
     * 内部口径取 floor 以下（s3p floor=16 KiB，本用例按目标宏自适应）。 */
    s_total_free = 8333027;
    s_total_largest = 8257536;
    s_total_min_ever = 8301920;
    s_free = FLOOR - 4096;
    s_largest = FLOOR - 4096;
    s_min_ever = FLOOR - 8192;

    /* 缺陷复现：合计口径（PSRAM）远大于 floor，若门禁读错口径就会放行。 */
    CHECK(!mem_guard_can_start(0),
          "PSRAM huge + internal largest=%u(<floor %u) must refuse: got largest=%u",
          (unsigned)s_largest, (unsigned)FLOOR, (unsigned)mem_guard_largest());
    CHECK(!mem_guard_can_start(FLOOR),
          "PSRAM huge + internal below floor must refuse a floor-sized need");
    CHECK(!mem_guard_can_start(2048),
          "internal below floor must refuse even a 2KiB need (floor is the hard line)");

    /* 口径断言：mem_guard 必须把 MALLOC_CAP_INTERNAL 传给 heap_caps_*。
     * 只断言返回值不够 —— 桩只要碰巧返回同一个数就仍会漏掉 caps 错误。
     * 这里显式调用三个读取器，保证每个 caps 记录都被刷新。 */
    (void)mem_guard_largest();
    (void)mem_guard_free();
    (void)mem_guard_min_ever();
    CHECK((s_last_largest_caps & MALLOC_CAP_INTERNAL) != 0u,
          "mem_guard_largest must query MALLOC_CAP_INTERNAL (got caps=0x%X)",
          s_last_largest_caps);
    CHECK((s_last_free_caps & MALLOC_CAP_INTERNAL) != 0u,
          "mem_guard_free must query MALLOC_CAP_INTERNAL (got caps=0x%X)",
          s_last_free_caps);
    CHECK((s_last_min_ever_caps & MALLOC_CAP_INTERNAL) != 0u,
          "mem_guard_min_ever must query MALLOC_CAP_INTERNAL (got caps=0x%X)",
          s_last_min_ever_caps);

    /* 内部 RAM 回到 floor 之上后必须放行：修复不能把门禁变成"恒拒绝"。
     * （任务卡风险点：改口径后若内部 largest 长期 < floor，现场配置下不去。） */
    s_largest = FLOOR + 4096;
    s_free = FLOOR + 8192;
    CHECK(mem_guard_can_start(0), "internal largest above floor must allow");
    CHECK(mem_guard_can_start(4096), "internal largest above floor must allow a 4KiB need");

    /* 低水位回调必须真的会被触发（旧代码在 s3p 上永不触发）。 */
    mem_guard_register_low_cb(low_cb);
    mem_guard_reset_latch();
    s_low_calls = 0;
    s_largest = FLOOR - 1;
    mem_guard_poll();
    CHECK(s_low_calls == 1, "low callback must fire on internal low water (fired %d)", s_low_calls);
    CHECK(s_last_largest == FLOOR - 1,
          "callback must carry the INTERNAL largest=%u, got %u",
          (unsigned)(FLOOR - 1), (unsigned)s_last_largest);
    mem_guard_register_low_cb(NULL);
    mem_guard_reset_latch();

    set_all_caps(200000);               /* 恢复默认，避免污染后续用例 */
}

static void test_low_callback_latch_and_hysteresis(void)
{
    mem_guard_register_low_cb(low_cb);
    mem_guard_reset_latch();
    s_low_calls = 0;

    /* 水位正常：不触发 */
    s_largest = FLOOR + 4096;
    s_free = s_largest + 1000;
    mem_guard_poll();
    CHECK(s_low_calls == 0, "healthy level must not fire callback");

    /* 首次跌破 floor：触发一次，并携带当时的 free/largest */
    s_largest = FLOOR - 1024;
    s_free = s_largest + 512;
    mem_guard_poll();
    CHECK(s_low_calls == 1, "first drop below floor must fire exactly once");
    CHECK(s_last_free == s_free, "callback free=%u, expected %u",
          (unsigned)s_last_free, (unsigned)s_free);
    CHECK(s_last_largest == s_largest, "callback largest=%u, expected %u",
          (unsigned)s_last_largest, (unsigned)s_largest);

    /* 仍低于 floor：锁存，不重复触发（否则 1Hz 会变成事件风暴） */
    mem_guard_poll();
    mem_guard_poll();
    CHECK(s_low_calls == 1, "latched low level must not re-fire without recovery");

    /* 回到 floor + 1KiB（低于迟滞 2KiB）：仍未解除锁存 */
    s_largest = FLOOR + 1024;
    mem_guard_poll();
    s_largest = FLOOR - 1024;
    mem_guard_poll();
    CHECK(s_low_calls == 1, "hysteresis must require floor + 2KiB before re-arming");

    /* 回到 floor + 2KiB：解除；再次跌破必须再触发 */
    s_largest = FLOOR + 2048;
    mem_guard_poll();
    s_largest = FLOOR - 1;
    mem_guard_poll();
    CHECK(s_low_calls == 2, "after full recovery the next drop must fire again");

    /* 注销回调后不再计数 */
    mem_guard_register_low_cb(NULL);
    mem_guard_reset_latch();
    s_largest = FLOOR - 1;
    mem_guard_poll();
    CHECK(s_low_calls == 2, "unregistered callback must not be called");
}

static void test_report_roundtrip(void)
{
    s_free = 0x1234;
    s_largest = 0x567;
    s_min_ever = 0x89A;
    mem_guard_set_min_stack_high_water(0x321);

    uint8_t buf[64];
    size_t n = mem_guard_encode_report(buf, sizeof(buf));
    CHECK(n > 0, "encode_report must succeed");
    CHECK(buf[0] == MSG_MEM_RPT, "frame type should be MSG_MEM_RPT, got 0x%02X", buf[0]);

    uint64_t got[6] = {0};
    frame_decoder_t dec;
    frame_field_t field;
    frame_decoder_init(&dec, buf, n);
    while (frame_decoder_next(&dec, &field) == FRAME_OK) {
        uint64_t v = 0;
        if (frame_field_get_varint(&field, &v) != FRAME_OK) continue;
        if (field.field_num <= 5) got[field.field_num] = v;
    }
    CHECK(got[1] == 0x1234, "field1 free=%llu", (unsigned long long)got[1]);
    CHECK(got[2] == 0x567, "field2 largest=%llu", (unsigned long long)got[2]);
    CHECK(got[3] == 0x89A, "field3 min_ever=%llu", (unsigned long long)got[3]);
    CHECK(got[4] == 0x321, "field4 min_task_stack_high_water=%llu", (unsigned long long)got[4]);
    CHECK(got[5] == FLOOR, "field5 floor=%llu", (unsigned long long)got[5]);

    /* cap 太小必须返回 0，而不是截断半帧发给服务端 */
    uint8_t tiny[2];
    CHECK(mem_guard_encode_report(tiny, sizeof(tiny)) == 0, "tiny cap must return 0");
    CHECK(mem_guard_encode_report(NULL, 64) == 0, "NULL buf must return 0");
}

static void test_min_stack_watermark_roundtrip(void)
{
    /* 语义 = 历史最小剩余：后续更大的值不能把水位抬高，否则任务重建会
     * 掩盖曾经的紧张状态。本测试必须在 test_report_roundtrip 之前跑
     * （后者会写入一个固定水位），所以 main() 里顺序被刻意固定。 */
    mem_guard_set_min_stack_high_water(4096);
    CHECK(mem_guard_min_stack_high_water() == 4096, "first sample should latch");
    mem_guard_set_min_stack_high_water(1234);
    CHECK(mem_guard_min_stack_high_water() == 1234, "a lower sample should latch");
    mem_guard_set_min_stack_high_water(4096);
    CHECK(mem_guard_min_stack_high_water() == 1234,
          "a later larger value must not raise the historical minimum");
    mem_guard_set_min_stack_high_water(0);
    CHECK(mem_guard_min_stack_high_water() == 1234,
          "0 is the 'never sampled' sentinel and must be a no-op");
}

int main(void)
{
    test_type_and_collisions();
    test_can_start_uses_largest_not_free();
    test_gate_uses_internal_caps_when_psram_is_large();
    test_low_callback_latch_and_hysteresis();
    test_min_stack_watermark_roundtrip();
    test_report_roundtrip();

    if (g_fail != 0) {
        printf("mem_guard_tests: %d passed, %d FAILED\n", g_pass, g_fail);
        return 1;
    }
    printf("mem_guard_tests: all %d checks passed\n", g_pass);
    return 0;
}
