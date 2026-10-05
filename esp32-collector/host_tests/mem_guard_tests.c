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
 *   1. MSG_MEM_RPT(0x20) 类型号与冲突检查
 *   2. can_start = largest >= max(need, floor)，且只看 largest，不看 free
 *   3. 低水位回调只触发一次 + 迟滞回弹后才能再触发
 *   4. MemReport 0x20 字段 1..5 编码/解码 roundtrip，单位字节
 */

#include <stdio.h>
#include <string.h>
#include <stdint.h>
#include <stdbool.h>

#include "frame_codec.h"

/* 可控堆值：覆盖 stubs/heap_stub_impl.c 的弱定义 */
static size_t s_free = 200000;
static size_t s_largest = 65536;
static size_t s_min_ever = 100000;

size_t heap_caps_get_free_size(unsigned caps) { (void)caps; return s_free; }
size_t heap_caps_get_largest_free_block(unsigned caps) { (void)caps; return s_largest; }
size_t heap_caps_get_minimum_free_size(unsigned caps) { (void)caps; return s_min_ever; }

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
    CHECK(MSG_MEM_RPT == 0x20, "MSG_MEM_RPT should be 0x20, got 0x%02X", MSG_MEM_RPT);
    CHECK(MSG_MEM_RPT != MSG_STATUS_RPT, "mem report collides with status report");
    CHECK(MSG_MEM_RPT != MSG_DIAG_REPORT, "mem report collides with diag report");
    CHECK(MSG_MEM_RPT != MSG_DIAG_ACK, "mem report collides with diag ack");
    /* 后端已知类型中最大的旧值是 0x1F；0x20 必须是第一个空闲号。 */
    CHECK(MSG_MEM_RPT == MSG_DIAG_ACK + 1, "0x20 must be the first free type after 0x1F");
}

static void test_can_start_uses_largest_not_free(void)
{
    s_largest = 4096;
    s_free = 200000;                    /* free 很大但 largest 小 = 碎片 */
    CHECK(!mem_guard_can_start(4096), "largest=4096 < floor must refuse even with huge free");
    CHECK(!mem_guard_can_start(1024), "largest=4096 < floor must refuse a small need");

    s_largest = FLOOR;                  /* 恰好等于 floor */
    CHECK(mem_guard_can_start(1024), "largest==floor should allow need<=floor");

    s_largest = FLOOR - 1;
    CHECK(!mem_guard_can_start(1024), "largest just below floor must refuse (hard floor)");

    s_largest = 32768;
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
