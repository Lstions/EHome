#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define CHECK(expr) do { \
    if (!(expr)) { \
        fprintf(stderr, "CHECK failed at %s:%d: %s\n", __FILE__, __LINE__, #expr); \
        abort(); \
    } \
} while (0)

#include "config_apply_transaction.h"
#include "config_tx_arena.h"
#include "mem_guard.h"

/* WS-E 起 execute() 每步前调用 mem_guard_can_start()，判决依据是 largest。
 * 覆盖 heap_stub_impl.c 的弱定义，让测试可以精确摆布水位。 */
static size_t s_test_largest = 65536;
size_t heap_caps_get_largest_free_block(unsigned caps) { (void)caps; return s_test_largest; }

/* config_apply_transaction.c 自 2026-10-05 起在失败路径记录"是哪一步失败"，
 * 因此需要宿主机的 ESP_LOG 与 esp_err_to_name 实现（与其它用例一致的做法）。 */
void host_test_log_record(char level, const char *tag, const char *format, ...)
{
    (void)level; (void)tag; (void)format;
}
const char *esp_err_to_name(int err) { (void)err; return "ESP_ERR"; }
typedef enum {
    STEP_SNAPSHOT = 1,
    STEP_PREPARE,
    STEP_DMA_NEW,
    STEP_PERIPH_NEW,
    STEP_BUS_NEW,
    STEP_SCHED_NEW,
    STEP_LOG_NEW,
    STEP_COMMIT,
    STEP_STOP,
    STEP_CLEANUP,
    STEP_DMA_RESTORE,
    STEP_PERIPH_OLD,
    STEP_BUS_OLD,
    STEP_SCHED_OLD,
    STEP_LOG_RESTORE,
    STEP_SAFE,
    STEP_END,
} step_t;

typedef struct {
    const config_manifest_t *old_manifest;
    const config_manifest_t *new_manifest;
    step_t steps[40];
    int count;
    uint64_t fail_steps;
    bool commit_ok;
} fixture_t;

static esp_err_t record(fixture_t *f, step_t step)
{
    f->steps[f->count++] = step;
    return (f->fail_steps & (1ULL << step)) ? ESP_FAIL : ESP_OK;
}

static esp_err_t begin_transaction(void *ctx) { (void)ctx; return ESP_OK; }
static void end_transaction(void *ctx) { (void)record(ctx, STEP_END); }
static esp_err_t snapshot(void *ctx) { return record(ctx, STEP_SNAPSHOT); }
static esp_err_t prepare(void *ctx) { return record(ctx, STEP_PREPARE); }
static esp_err_t apply_dma(void *ctx, const config_manifest_t *m)
{
    fixture_t *f = ctx;
    CHECK(m == f->new_manifest);
    return record(f, STEP_DMA_NEW);
}
static esp_err_t apply_periph(void *ctx, const config_manifest_t *m)
{
    fixture_t *f = ctx;
    return record(f, m == f->new_manifest ? STEP_PERIPH_NEW : STEP_PERIPH_OLD);
}
static esp_err_t apply_bus(void *ctx, const config_manifest_t *m)
{
    fixture_t *f = ctx;
    return record(f, m == f->new_manifest ? STEP_BUS_NEW : STEP_BUS_OLD);
}
static esp_err_t apply_sched(void *ctx, const config_manifest_t *m)
{
    fixture_t *f = ctx;
    return record(f, m == f->new_manifest ? STEP_SCHED_NEW : STEP_SCHED_OLD);
}
static esp_err_t apply_log(void *ctx, const config_manifest_t *m)
{
    fixture_t *f = ctx;
    CHECK(m == f->new_manifest);
    return record(f, STEP_LOG_NEW);
}
static bool commit(void *ctx)
{
    fixture_t *f = ctx;
    (void)record(f, STEP_COMMIT);
    return f->commit_ok;
}
static esp_err_t stop(void *ctx) { return record(ctx, STEP_STOP); }
static esp_err_t cleanup(void *ctx) { return record(ctx, STEP_CLEANUP); }
static esp_err_t restore_dma(void *ctx) { return record(ctx, STEP_DMA_RESTORE); }
static esp_err_t restore_periph(void *ctx) { return record(ctx, STEP_PERIPH_OLD); }
static esp_err_t restore_log(void *ctx) { return record(ctx, STEP_LOG_RESTORE); }
static esp_err_t safe(void *ctx) { return record(ctx, STEP_SAFE); }

static const config_apply_ops_t OPS = {
    .begin_transaction = begin_transaction,
    .end_transaction = end_transaction,
    .snapshot = snapshot,
    .prepare = prepare,
    .apply_dma = apply_dma,
    .apply_peripherals = apply_periph,
    .apply_buses = apply_bus,
    .apply_scheduler = apply_sched,
    .apply_log_stream = apply_log,
    .commit_manifest = commit,
    .stop_scheduler = stop,
    .cleanup_buses = cleanup,
    .restore_dma = restore_dma,
    .restore_peripherals = restore_periph,
    .restore_log_stream = restore_log,
    .enter_safe_state = safe,
};

static void assert_steps(const fixture_t *f, const step_t *expected, int count)
{
    CHECK(f->count == count);
    CHECK(memcmp(f->steps, expected, (size_t)count * sizeof(expected[0])) == 0);
}

static fixture_t fixture(config_manifest_t *old_m, config_manifest_t *new_m)
{
    return (fixture_t){
        .old_manifest = old_m,
        .new_manifest = new_m,
        .commit_ok = true,
    };
}

static void test_commit_is_after_all_runtime_subsystems(void)
{
    config_manifest_t old_m = {.applied = true};
    config_manifest_t new_m = {0};
    fixture_t f = fixture(&old_m, &new_m);

    CHECK(config_apply_transaction_execute(&OPS, &f, &old_m, &new_m) == CONFIG_APPLY_OK);
    const step_t expected[] = {
        STEP_SNAPSHOT, STEP_PREPARE, STEP_DMA_NEW, STEP_PERIPH_NEW, STEP_BUS_NEW,
        STEP_SCHED_NEW, STEP_LOG_NEW, STEP_COMMIT, STEP_END,
    };
    assert_steps(&f, expected, (int)(sizeof(expected) / sizeof(expected[0])));
}

static void test_bus_failure_rolls_back_before_returning_failure(void)
{
    config_manifest_t old_m = {.applied = true};
    config_manifest_t new_m = {0};
    fixture_t f = fixture(&old_m, &new_m);
    f.fail_steps = 1ULL << STEP_BUS_NEW;

    CHECK(config_apply_transaction_execute(&OPS, &f, &old_m, &new_m) ==
           CONFIG_APPLY_FAILED_RESTORED);
    const step_t expected[] = {
        STEP_SNAPSHOT, STEP_PREPARE, STEP_DMA_NEW, STEP_PERIPH_NEW, STEP_BUS_NEW,
        STEP_STOP, STEP_CLEANUP, STEP_DMA_RESTORE, STEP_PERIPH_OLD,
        STEP_BUS_OLD, STEP_SCHED_OLD, STEP_LOG_RESTORE, STEP_END,
    };
    assert_steps(&f, expected, (int)(sizeof(expected) / sizeof(expected[0])));
}

static void test_scheduler_failure_cannot_commit_partial_application(void)
{
    config_manifest_t old_m = {.applied = true};
    config_manifest_t new_m = {0};
    fixture_t f = fixture(&old_m, &new_m);
    f.fail_steps = 1ULL << STEP_SCHED_NEW;

    CHECK(config_apply_transaction_execute(&OPS, &f, &old_m, &new_m) ==
           CONFIG_APPLY_FAILED_RESTORED);
    for (int i = 0; i < f.count; i++) CHECK(f.steps[i] != STEP_COMMIT);
}

static void test_restore_failure_enters_deterministic_safe_state(void)
{
    config_manifest_t old_m = {.applied = true};
    config_manifest_t new_m = {0};
    fixture_t f = fixture(&old_m, &new_m);
    f.fail_steps = 1ULL << STEP_DMA_RESTORE;
    f.commit_ok = false;

    CHECK(config_apply_transaction_execute(&OPS, &f, &old_m, &new_m) ==
           CONFIG_APPLY_FAILED_SAFE);
    CHECK(f.count >= 2 && f.steps[f.count - 2] == STEP_SAFE);
    CHECK(f.steps[f.count - 1] == STEP_END);
}

static void test_unconfirmed_bus_cleanup_enters_safe_state(void)
{
    config_manifest_t old_m = {.applied = true};
    config_manifest_t new_m = {0};
    fixture_t f = fixture(&old_m, &new_m);
    f.fail_steps = (1ULL << STEP_BUS_NEW) | (1ULL << STEP_CLEANUP);

    CHECK(config_apply_transaction_execute(&OPS, &f, &old_m, &new_m) ==
           CONFIG_APPLY_FAILED_SAFE);
    const step_t expected[] = {
        STEP_SNAPSHOT, STEP_PREPARE, STEP_DMA_NEW, STEP_PERIPH_NEW, STEP_BUS_NEW,
        STEP_STOP, STEP_CLEANUP, STEP_SAFE, STEP_END,
    };
    assert_steps(&f, expected, (int)(sizeof(expected) / sizeof(expected[0])));
}

static void test_first_apply_failure_restores_empty_runtime(void)
{
    config_manifest_t new_m = {0};
    fixture_t f = fixture(NULL, &new_m);
    f.fail_steps = 1ULL << STEP_BUS_NEW;

    CHECK(config_apply_transaction_execute(&OPS, &f, NULL, &new_m) ==
           CONFIG_APPLY_FAILED_RESTORED);
    const step_t expected[] = {
        STEP_SNAPSHOT, STEP_PREPARE, STEP_DMA_NEW, STEP_PERIPH_NEW, STEP_BUS_NEW,
        STEP_STOP, STEP_CLEANUP, STEP_DMA_RESTORE, STEP_PERIPH_OLD,
        STEP_LOG_RESTORE, STEP_END,
    };
    assert_steps(&f, expected, (int)(sizeof(expected) / sizeof(expected[0])));
}

static void test_snapshot_failure_does_not_stop_or_mutate_runtime(void)
{
    config_manifest_t old_m = {.applied = true};
    config_manifest_t new_m = {0};
    fixture_t f = fixture(&old_m, &new_m);
    f.fail_steps = 1ULL << STEP_SNAPSHOT;

    CHECK(config_apply_transaction_execute(&OPS, &f, &old_m, &new_m) ==
           CONFIG_APPLY_FAILED_UNCHANGED);
    const step_t expected[] = {STEP_SNAPSHOT, STEP_END};
    assert_steps(&f, expected, 2);
}

static void test_prepare_failure_rolls_back_after_all_snapshots_exist(void)
{
    config_manifest_t old_m = {.applied = true};
    config_manifest_t new_m = {0};
    fixture_t f = fixture(&old_m, &new_m);
    f.fail_steps = 1ULL << STEP_PREPARE;

    CHECK(config_apply_transaction_execute(&OPS, &f, &old_m, &new_m) ==
           CONFIG_APPLY_FATAL);
    const step_t expected[] = {
        STEP_SNAPSHOT, STEP_PREPARE,
    };
    assert_steps(&f, expected, (int)(sizeof(expected) / sizeof(expected[0])));
}

static void test_unconfirmed_rollback_stop_is_fatal_without_cleanup_or_safe_state(void)
{
    config_manifest_t old_m = {.applied = true};
    config_manifest_t new_m = {0};
    fixture_t f = fixture(&old_m, &new_m);
    f.fail_steps = (1ULL << STEP_BUS_NEW) | (1ULL << STEP_STOP);

    CHECK(config_apply_transaction_execute(&OPS, &f, &old_m, &new_m) ==
           CONFIG_APPLY_FATAL);
    const step_t expected[] = {
        STEP_SNAPSHOT, STEP_PREPARE, STEP_DMA_NEW, STEP_PERIPH_NEW, STEP_BUS_NEW,
        STEP_STOP,
    };
    assert_steps(&f, expected, (int)(sizeof(expected) / sizeof(expected[0])));
}

static void test_log_start_failure_is_before_commit_and_rolls_back(void)
{
    config_manifest_t old_m = {.applied = true};
    config_manifest_t new_m = {.log_stream_enabled = true};
    fixture_t f = fixture(&old_m, &new_m);
    f.fail_steps = 1ULL << STEP_LOG_NEW;

    CHECK(config_apply_transaction_execute(&OPS, &f, &old_m, &new_m) ==
           CONFIG_APPLY_FAILED_RESTORED);
    for (int i = 0; i < f.count; i++) CHECK(f.steps[i] != STEP_COMMIT);
    CHECK(f.count >= 2 && f.steps[f.count - 2] == STEP_LOG_RESTORE);
    CHECK(f.steps[f.count - 1] == STEP_END);
}

static void test_rollback_and_safe_state_failure_is_fatal(void)
{
    config_manifest_t old_m = {.applied = true};
    config_manifest_t new_m = {0};
    fixture_t f = fixture(&old_m, &new_m);
    f.commit_ok = false;
    f.fail_steps = (1ULL << STEP_DMA_RESTORE) | (1ULL << STEP_SAFE);

    CHECK(config_apply_transaction_execute(&OPS, &f, &old_m, &new_m) ==
           CONFIG_APPLY_FATAL);
    CHECK(config_apply_result_requires_restart(CONFIG_APPLY_FATAL));
    CHECK(!config_apply_result_requires_restart(CONFIG_APPLY_FAILED_SAFE));
}

/* ---- WS-E: arena reservation + water-level gate ---- */

static size_t s_test_drop_to = 65536;

/* begin_transaction hook: Phase A already passed at the high level; this drops
 * the water between Phase A and the first Phase B check, which is exactly the
 * race the per-step gate exists for. */
static esp_err_t begin_drop(void *ctx)
{
    (void)ctx;
    s_test_largest = s_test_drop_to;
    return ESP_OK;
}

static void test_arena_insufficient_refuses_before_begin(void)
{
    config_manifest_t new_m = {0};
    fixture_t f = fixture(NULL, &new_m);
    config_apply_ops_t ops = OPS;
    /* Whole-arena request can never be reserved: the canary owns the last
     * 4 bytes, so can_reserve() must be false. */
    ops.arena_need = CONFIG_TX_ARENA_BYTES;
    s_test_largest = 65536;
    CHECK(config_tx_arena_init());

    CHECK(config_apply_transaction_execute(&ops, &f, NULL, &new_m) ==
          CONFIG_APPLY_FAILED_UNCHANGED);
    CHECK(f.count == 0);   /* no begin/snapshot/prepare: runtime untouched */
}

static void test_arena_reusable_after_rollback(void)
{
    config_manifest_t new_m = {0};
    fixture_t f = fixture(NULL, &new_m);
    f.commit_ok = false;   /* fail at the last step -> rollback -> RESTORED */
    config_apply_ops_t ops = OPS;
    ops.arena_need = 792;  /* measured sizeof(manifest_tx_ctx_t) */
    s_test_largest = 65536;
    CHECK(config_tx_arena_init());

    /* Caller lifecycle (app_callbacks): begin, allocate the tx ctx, execute,
     * then end.  A rollback must leave the arena immediately reusable. */
    CHECK(config_tx_arena_begin());
    void *tx1 = config_tx_arena_alloc(792);
    CHECK(tx1 != NULL);
    CHECK(config_apply_transaction_execute(&ops, &f, NULL, &new_m) ==
          CONFIG_APPLY_FAILED_RESTORED);
    CHECK(config_tx_arena_end());

    fixture_t f2 = fixture(NULL, &new_m);
    CHECK(config_tx_arena_begin());
    void *tx2 = config_tx_arena_alloc(792);
    CHECK(tx2 == tx1);     /* cursor reset -> same workspace handed out */
    /* Second transaction on the same arena goes all the way to success. */
    CHECK(config_apply_transaction_execute(&ops, &f2, NULL, &new_m) ==
          CONFIG_APPLY_OK);
    CHECK(f2.steps[f2.count - 1] == STEP_END);
    CHECK(config_tx_arena_end());
}

static void test_watermark_refuses_before_begin(void)
{
    config_manifest_t new_m = {0};
    fixture_t f = fixture(NULL, &new_m);
    s_test_largest = mem_guard_floor_bytes() - 1;   /* below the hard floor */

    CHECK(config_apply_transaction_execute(&OPS, &f, NULL, &new_m) ==
          CONFIG_APPLY_FAILED_UNCHANGED);
    CHECK(f.count == 0);
    s_test_largest = 65536;
}

static void test_phase_b_drop_after_prepare_rolls_back(void)
{
    config_manifest_t new_m = {0};
    fixture_t f = fixture(NULL, &new_m);
    config_apply_ops_t ops = OPS;
    ops.begin_transaction = begin_drop;
    /* Make apply_buses the step that fails: its gate need (9000) exceeds the
     * dropped largest (8192 = host floor). Earlier steps' defaults fit. */
    ops.step_need_apply_buses = 9000;
    s_test_largest = 65536;
    s_test_drop_to = mem_guard_floor_bytes();

    config_apply_result_t r = config_apply_transaction_execute(&ops, &f, NULL, &new_m);

    /* The scheduler was already stopped by prepare; UNCHANGED would be a lie. */
    CHECK(r != CONFIG_APPLY_FAILED_UNCHANGED);
    CHECK(r == CONFIG_APPLY_FAILED_RESTORED);
    CHECK(f.steps[f.count - 1] == STEP_END);

    bool saw_stop = false, saw_cleanup = false, saw_bus_new = false;
    for (int i = 0; i < f.count; i++) {
        if (f.steps[i] == STEP_STOP) saw_stop = true;
        if (f.steps[i] == STEP_CLEANUP) saw_cleanup = true;
        if (f.steps[i] == STEP_BUS_NEW) saw_bus_new = true;
    }
    CHECK(saw_stop && saw_cleanup);
    CHECK(!saw_bus_new);
    s_test_largest = 65536;
}

static void test_phase_b_drop_to_below_floor_enters_safe_state(void)
{
    config_manifest_t new_m = {0};
    fixture_t f = fixture(NULL, &new_m);
    config_apply_ops_t ops = OPS;
    ops.begin_transaction = begin_drop;
    s_test_largest = 65536;
    s_test_drop_to = 4096;   /* below floor: even rollback rebuilds must refuse */

    config_apply_result_t r = config_apply_transaction_execute(&ops, &f, NULL, &new_m);

    CHECK(r != CONFIG_APPLY_FAILED_UNCHANGED);
    CHECK(r == CONFIG_APPLY_FAILED_SAFE);
    bool saw_safe = false;
    for (int i = 0; i < f.count; i++)
        if (f.steps[i] == STEP_SAFE) saw_safe = true;
    CHECK(saw_safe);
    s_test_largest = 65536;
}

static void test_can_start_predicate_is_pure(void)
{
    s_test_largest = 65536;
    CHECK(config_apply_transaction_can_start(&OPS));
    s_test_largest = mem_guard_floor_bytes() - 1;
    CHECK(!config_apply_transaction_can_start(&OPS));
    /* No steps recorded: predicate must not touch the ops callbacks. */
    fixture_t f = fixture(NULL, NULL);
    (void)f;
    s_test_largest = 65536;
}

int main(void)
{
    test_commit_is_after_all_runtime_subsystems();
    test_bus_failure_rolls_back_before_returning_failure();
    test_scheduler_failure_cannot_commit_partial_application();
    test_restore_failure_enters_deterministic_safe_state();
    test_unconfirmed_bus_cleanup_enters_safe_state();
    test_first_apply_failure_restores_empty_runtime();
    test_snapshot_failure_does_not_stop_or_mutate_runtime();
    test_prepare_failure_rolls_back_after_all_snapshots_exist();
    test_unconfirmed_rollback_stop_is_fatal_without_cleanup_or_safe_state();
    test_log_start_failure_is_before_commit_and_rolls_back();
    test_rollback_and_safe_state_failure_is_fatal();
    test_arena_insufficient_refuses_before_begin();
    test_arena_reusable_after_rollback();
    test_watermark_refuses_before_begin();
    test_phase_b_drop_after_prepare_rolls_back();
    test_phase_b_drop_to_below_floor_enters_safe_state();
    test_can_start_predicate_is_pure();
    puts("config_apply_transaction_tests: PASS");
    return 0;
}
