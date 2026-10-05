/*
 * config_tx_arena_tests.c — WS-E 事务 arena 的宿主机契约测试（设计 §2/§7.2）。
 *
 * 为什么需要这组测试：
 *   配置事务的成败必须不依赖"当时堆碎片"。arena 是那个保证：固定 4 KiB、
 *   只动游标、结束只 reset。这里把每条边界钉死 —— 尤其是
 *     (a) 耗尽时 alloc 返回 NULL 且**不移动游标**（否则已分配对象被覆盖），
 *     (b) end() 后立即可复用（rollback 后必须能再开一笔事务），
 *     (c) canary 能抓到越界写（抓到即视为不可恢复，不能继续 rollback）。
 *   测试直接 include 实现头文件，等价于 firmware 单 TU 的编译方式。
 *
 * 目标宏：测试同时覆盖静态 .bss 分支（默认，非 PSRAM）与接口常量。
 * 本测试不链接 heap_stub_impl.c：非 PSRAM 分支不需要 heap。
 */

#include <stdio.h>
#include <string.h>
#include <stdint.h>
#include <stdbool.h>

/* Host tests drive the exact implementation header the firmware includes. */
#define CONFIG_TX_ARENA_TESTING 1
#include "../main/config_tx_arena_impl.h"

static int g_fail = 0;
static int g_pass = 0;

#define CHECK(cond, ...)                                                  \
    do {                                                                  \
        if (cond) {                                                       \
            g_pass++;                                                     \
        } else {                                                          \
            g_fail++;                                                     \
            printf("FAIL %s:%d: ", __FILE__, __LINE__);                   \
            printf(__VA_ARGS__);                                          \
            printf("\n");                                                 \
        }                                                                 \
    } while (0)

static void test_capacity_constants(void)
{
    CHECK(config_tx_arena_capacity() == CONFIG_TX_ARENA_BYTES,
          "capacity=%u, expected %u",
          (unsigned)config_tx_arena_capacity(), (unsigned)CONFIG_TX_ARENA_BYTES);
    CHECK(CONFIG_TX_ARENA_BYTES == 4096u,
          "design pins arena at 4096 bytes, got %u", (unsigned)CONFIG_TX_ARENA_BYTES);
    CHECK(CONFIG_TX_ARENA_BUMP_RESERVE <= CONFIG_TX_ARENA_BYTES,
          "reserve must fit in the arena");
    /* The transaction context measured at 792 B; the reserve must cover it. */
    CHECK(CONFIG_TX_ARENA_BUMP_RESERVE >= 792u,
          "reserve must cover manifest_tx_ctx_t (792 B), got %u",
          (unsigned)CONFIG_TX_ARENA_BUMP_RESERVE);
    CHECK((CONFIG_TX_ARENA_BYTES % CONFIG_TX_ARENA_ALIGN) == 0,
          "capacity must be a multiple of alignment");
}

static void test_init_begin_end_reuse(void)
{
    CHECK(config_tx_arena_init(), "init must succeed on the static branch");
    CHECK(config_tx_arena_init(), "init must be idempotent");

    /* begin/end/begin: rollback path relies on immediate reuse. */
    CHECK(config_tx_arena_begin(), "first begin must succeed");
    void *p1 = config_tx_arena_alloc(792);
    CHECK(p1 != NULL, "792-byte tx ctx must fit");
    CHECK(((uintptr_t)p1 % CONFIG_TX_ARENA_ALIGN) == 0,
          "allocation must be %u-byte aligned", (unsigned)CONFIG_TX_ARENA_ALIGN);
    CHECK(config_tx_arena_end(), "end must verify the canary");

    CHECK(config_tx_arena_begin(), "begin after end must succeed");
    void *p2 = config_tx_arena_alloc(792);
    CHECK(p2 == p1, "cursor must reset, so the same block is handed out again");
    CHECK(p2 != NULL, "reused block must be valid");
    CHECK(config_tx_arena_end(), "second end must verify the canary");
}

static void test_alignment_and_monotonic_addresses(void)
{
    CHECK(config_tx_arena_begin(), "begin failed");
    void *a = config_tx_arena_alloc(1);
    void *b = config_tx_arena_alloc(17);
    void *c = config_tx_arena_alloc(16);
    CHECK(a && b && c, "small allocations must succeed");
    CHECK(((uintptr_t)b % CONFIG_TX_ARENA_ALIGN) == 0, "b must be aligned");
    CHECK(((uintptr_t)c % CONFIG_TX_ARENA_ALIGN) == 0, "c must be aligned");
    CHECK((uintptr_t)b >= (uintptr_t)a + CONFIG_TX_ARENA_ALIGN,
          "1-byte alloc must consume a full aligned slot");
    CHECK((uintptr_t)c > (uintptr_t)b, "allocations must advance");
    if (!b || !c) {
        /* A broken cursor/reset path can return NULL here; do not dereference
         * it (keeps the mutation red as an assertion, not a segfault). */
        CHECK(config_tx_arena_end(), "end failed");
        return;
    }
    /* Non-overlap: writing the whole requested region must not touch b. */
    memset(b, 0xAB, 17);
    CHECK(((uint8_t *)c)[0] == 0, "memory after b must not be written by b's fill");
    CHECK(config_tx_arena_end(), "end failed");
}

static void test_exhaustion_keeps_cursor(void)
{
    CHECK(config_tx_arena_begin(), "begin failed");
    size_t usable = CONFIG_TX_ARENA_BYTES - sizeof(uint32_t);
    size_t slots = (usable / CONFIG_TX_ARENA_ALIGN);          /* 255 aligned slots */
    size_t big_bytes = (slots - 2) * CONFIG_TX_ARENA_ALIGN;   /* leave exactly two */
    void *big = config_tx_arena_alloc(big_bytes);
    CHECK(big != NULL, "large allocation should fit");
    if (!big) { CHECK(config_tx_arena_end(), "end failed"); return; }
    uint8_t *tail = (uint8_t *)big + big_bytes - 1;
    *tail = 0x5A;

    /* Oversized request: must fail WITHOUT consuming the free slots. */
    void *over = config_tx_arena_alloc(4 * CONFIG_TX_ARENA_ALIGN);
    CHECK(over == NULL, "oversized allocation must return NULL");
    CHECK(*tail == 0x5A, "failed allocation must not modify previously handed memory");

    /* Both free slots must still be handed out, then the third must fail.
     * A cursor that advanced on the failed request would return NULL here. */
    uint8_t *slot1 = config_tx_arena_alloc(CONFIG_TX_ARENA_ALIGN);
    CHECK(slot1 == (uint8_t *)big + big_bytes,
          "first free slot must be handed out after a failed alloc");
    uint8_t *slot2 = config_tx_arena_alloc(CONFIG_TX_ARENA_ALIGN);
    CHECK(slot2 == slot1 + CONFIG_TX_ARENA_ALIGN, "second free slot must be handed out");
    CHECK(config_tx_arena_alloc(CONFIG_TX_ARENA_ALIGN) == NULL,
          "allocation past capacity must return NULL");
    CHECK(config_tx_arena_alloc(CONFIG_TX_ARENA_ALIGN) == NULL,
          "repeated failed allocation must keep returning NULL");

    /* A fresh transaction reuses the whole block (rollback path). */
    CHECK(config_tx_arena_end(), "end after failed alloc must still pass");
    CHECK(config_tx_arena_begin(), "begin after exhaustion must work (cursor reset)");
    void *reuse = config_tx_arena_alloc(CONFIG_TX_ARENA_ALIGN);
    CHECK(reuse == big, "cursor reset must hand out from the start again");
    CHECK(config_tx_arena_end(), "end failed");
}

static void test_alloc_requires_transaction(void)
{
    CHECK(config_tx_arena_end(), "end outside transaction must be a no-op success");
    CHECK(config_tx_arena_alloc(16) == NULL, "alloc outside begin() must return NULL");
    CHECK(config_tx_arena_alloc(0) == NULL, "zero-size alloc must return NULL");
}

static void test_reentry_refused(void)
{
    CHECK(config_tx_arena_begin(), "begin failed");
    CHECK(!config_tx_arena_begin(), "nested begin must be refused (single writer contract)");
    /* The refusal must not have reset the cursor under the first transaction. */
    void *p = config_tx_arena_alloc(256);
    CHECK(p != NULL, "first transaction must still be usable");
    CHECK(config_tx_arena_end(), "end failed");
}

static void test_canary_detects_overflow(void)
{
    CHECK(config_tx_arena_begin(), "begin failed");
    uint8_t *storage = config_tx_arena_test_storage();
    /* Simulate an out-of-bounds write landing on the canary. */
    storage[CONFIG_TX_ARENA_BYTES - 1] ^= 0xFF;
    CHECK(!config_tx_arena_end(), "end must fail when the canary is corrupted");
    /* Corruption is unrecoverable: the arena latches unavailable so a later
     * transaction cannot be handed corrupted memory. */
    CHECK(config_tx_arena_alloc(16) == NULL, "alloc after corruption must fail");
    CHECK(!config_tx_arena_begin(), "begin after corruption must be refused");
    /* The latch survives init() by design (fail-safe).  Restore-only-for-tests
     * hook models a reboot, which is the only thing that clears it. */
    CHECK(!config_tx_arena_init(), "init must not heal a faulted arena");
    config_tx_arena_test_clear_fault();
    CHECK(config_tx_arena_init(), "re-init after an explicit fault clear should succeed");
}

static void test_can_reserve_contract(void)
{
    CHECK(config_tx_arena_can_reserve(792), "must reserve the measured ctx size");
    CHECK(config_tx_arena_can_reserve(CONFIG_TX_ARENA_BUMP_RESERVE), "reserve constant must fit");
    CHECK(!config_tx_arena_can_reserve(0), "zero reserve is not a valid use");
    CHECK(!config_tx_arena_can_reserve(CONFIG_TX_ARENA_BYTES),
          "requesting the whole arena must fail (canary needs 4 B)");
    /* can_reserve is a pure predicate: no cursor side effects. */
    CHECK(config_tx_arena_begin(), "begin failed");
    void *p = config_tx_arena_alloc(16);
    CHECK(p != NULL, "alloc after can_reserve must work");
    CHECK(config_tx_arena_end(), "end failed");
}

static void test_high_water(void)
{
    size_t before = config_tx_arena_high_water();
    CHECK(config_tx_arena_begin(), "begin failed");
    (void)config_tx_arena_alloc(100);
    (void)config_tx_arena_alloc(200);
    CHECK(config_tx_arena_high_water() >= before,
          "high water must be monotonic across transactions");
    CHECK(config_tx_arena_high_water() >= 300, "high water should include both allocations");
    CHECK(config_tx_arena_end(), "end failed");
}

int main(void)
{
    test_capacity_constants();
    test_init_begin_end_reuse();
    test_alignment_and_monotonic_addresses();
    test_exhaustion_keeps_cursor();
    test_alloc_requires_transaction();
    test_reentry_refused();
    test_canary_detects_overflow();
    test_can_reserve_contract();
    test_high_water();

    if (g_fail != 0) {
        printf("config_tx_arena_tests: FAIL (%d failed, %d passed)\n", g_fail, g_pass);
        return 1;
    }
    printf("config_tx_arena_tests: PASS (%d checks)\n", g_pass);
    return 0;
}
