/* WS-E: Configuration transaction determinism.
 *
 * Two guarantees this file owns (design docs/设计/配置事务确定性设计-2026-10-05.md):
 *
 * 1. The transaction workspace never comes from the general heap.  The caller
 *    passes a context block (manifest_tx_ctx_t in app_callbacks.c) that lives
 *    in the fixed config_tx_arena; this module only verifies the capacity up
 *    front via ops->arena_need and never allocates per step.  A transaction can
 *    no longer fail just because the heap was fragmented at that instant
 *    (field evidence: apply_buses left free=876 / largest=832).
 *
 * 2. Memory water level is checked twice:
 *      Phase A (before begin_transaction, runtime untouched) -> UNCHANGED.
 *      Phase B (before each step, after prepare may have stopped the
 *      scheduler) -> normal step failure -> rollback.  Returning UNCHANGED
 *      here would lie: runtime state is already partially mutated.
 *    The predicate is mem_guard_can_start() from main/mem_guard.h, which judges
 *    largest (contiguity), not free — see 218edc62 §4.1.
 */
#include "config_apply_transaction.h"
#include "config_tx_arena.h"
#include "mem_guard.h"

#include "esp_log.h"
#include "esp_heap_caps.h"
#include "esp_system.h"

static const char *TAG = "CFG_TX";

/* Fallback contiguous need for a step whose ops entry is left at 0.  Derived
 * from field measurements: apply_peripherals moved -372 B; apply_buses was
 * -12,060 B when it reinstalled 3 UART drivers, but WS-E driver reuse leaves
 * only the SPI (~1.35 KB) and I2C (~1.42 KB) drivers to rebuild, so 4 KiB is a
 * conservative bound for that step. */
#define CONFIG_TX_STEP_NEED_DEFAULT       1024u
#define CONFIG_TX_STEP_NEED_APPLY_BUSES   4096u

_Static_assert(CONFIG_TX_ARENA_BYTES >= CONFIG_TX_ARENA_BUMP_RESERVE,
               "config_tx_arena too small for the transaction workspace");

/* Arena implementation is included here (single definition site, header-only)
 * so main/CMakeLists.txt needs no new source file.  app_callbacks.c only needs
 * the declarations in config_tx_arena.h and links against this TU. */
#include "config_tx_arena_impl.h"

static bool valid_ops(const config_apply_ops_t *ops)
{
    return ops && ops->begin_transaction && ops->end_transaction &&
           ops->snapshot && ops->prepare && ops->apply_dma &&
           ops->apply_peripherals && ops->apply_buses && ops->apply_scheduler &&
           ops->apply_log_stream && ops->commit_manifest &&
           ops->stop_scheduler && ops->cleanup_buses && ops->restore_dma &&
           ops->restore_peripherals && ops->restore_log_stream &&
           ops->enter_safe_state;
}

static size_t step_need(size_t configured, size_t fallback)
{
    return configured ? configured : fallback;
}

static size_t preflight_need(const config_apply_ops_t *ops)
{
    if (ops->preflight_need) return ops->preflight_need;
    size_t need = step_need(ops->step_need_apply_dma, CONFIG_TX_STEP_NEED_DEFAULT);
    size_t candidates[] = {
        step_need(ops->step_need_apply_peripherals, CONFIG_TX_STEP_NEED_DEFAULT),
        step_need(ops->step_need_apply_buses, CONFIG_TX_STEP_NEED_APPLY_BUSES),
        step_need(ops->step_need_apply_scheduler, CONFIG_TX_STEP_NEED_DEFAULT),
        step_need(ops->step_need_apply_log_stream, CONFIG_TX_STEP_NEED_DEFAULT),
    };
    for (size_t i = 0; i < sizeof(candidates) / sizeof(candidates[0]); i++) {
        if (candidates[i] > need) need = candidates[i];
    }
    return need;
}

static void log_mem_gate(const char *phase, const char *step, size_t need_bytes)
{
    /* Structured single line, grep-able by "memgate".  largest and free are
     * both printed on purpose: the 2026-10-05 failure was fragmentation
     * (free=18464 but largest=7680), which a free-only log would hide. */
    ESP_LOGW(TAG, "[memgate] phase=%s step=%s need=%u largest=%u free=%u floor=%u",
             phase, step, (unsigned)need_bytes,
             (unsigned)mem_guard_largest(), (unsigned)mem_guard_free(),
             (unsigned)mem_guard_floor_bytes());
}

bool config_apply_transaction_can_start(const config_apply_ops_t *ops)
{
    if (!valid_ops(ops)) {
        ESP_LOGE(TAG, "[memgate] phase=preflight step=ops reason=invalid_ops");
        return false;
    }
    if (ops->arena_need != 0 && !config_tx_arena_can_reserve(ops->arena_need)) {
        log_mem_gate("preflight", "arena", ops->arena_need);
        ESP_LOGE(TAG, "[memgate] phase=preflight step=arena need=%u capacity=%u "
                      "reason=arena_exhausted",
                 (unsigned)ops->arena_need, (unsigned)config_tx_arena_capacity());
        return false;
    }
    const size_t need = preflight_need(ops);
    if (!mem_guard_can_start(need)) {
        log_mem_gate("A", "preflight", need);
        return false;
    }
    return true;
}

static config_apply_result_t rollback(const config_apply_ops_t *ops, void *ctx,
                                      const config_manifest_t *old_manifest)
{
    esp_err_t rollback_err = ESP_OK;

    if (ops->stop_scheduler(ctx) != ESP_OK) {
        /* A scheduler task may still be dereferencing bus state. Neither
         * rollback cleanup nor safe-state teardown is safe until stop is
         * confirmed; the caller must keep workers suspended and restart. */
        return CONFIG_APPLY_FATAL;
    }
    if (ops->cleanup_buses(ctx) != ESP_OK) {
        return ops->enter_safe_state(ctx) == ESP_OK
            ? CONFIG_APPLY_FAILED_SAFE : CONFIG_APPLY_FATAL;
    }
    if (ops->restore_dma(ctx) != ESP_OK) rollback_err = ESP_FAIL;
    /* Rebuilding peripherals/buses is the only fallible allocation work left
     * in rollback.  Gate it against the floor so a transaction that lost the
     * memory race does not drive the allocator further into the ground; a
     * failed rebuild already routes to enter_safe_state().  The zero-need
     * check is deliberate: the caller cannot pass a per-step value here, and
     * the hard floor is exactly the "do not start reconfiguration" line. */
    if (!mem_guard_can_start(0)) {
        log_mem_gate("rollback", "restore_peripherals", 0);
        rollback_err = ESP_FAIL;
    } else if (ops->restore_peripherals(ctx) != ESP_OK) {
        rollback_err = ESP_FAIL;
    }
    if (old_manifest) {
        if (!mem_guard_can_start(ops->step_need_apply_buses)) {
            log_mem_gate("rollback", "apply_buses", ops->step_need_apply_buses);
            rollback_err = ESP_FAIL;
        } else if (ops->apply_buses(ctx, old_manifest) != ESP_OK) {
            rollback_err = ESP_FAIL;
        }
        if (rollback_err == ESP_OK) {
            if (ops->apply_scheduler(ctx, old_manifest) != ESP_OK) rollback_err = ESP_FAIL;
        }
    }
    if (ops->restore_log_stream(ctx) != ESP_OK) rollback_err = ESP_FAIL;

    if (rollback_err != ESP_OK) {
        return ops->enter_safe_state(ctx) == ESP_OK
            ? CONFIG_APPLY_FAILED_SAFE : CONFIG_APPLY_FATAL;
    }
    return CONFIG_APPLY_FAILED_RESTORED;
}

/* 每个配置步骤执行前后都记录堆实况（2026-10-05）。
 *
 * 背景：S3 在配置事务**提交成功后约 4 秒**必然出现
 *   E transport_base: tcp_write error, errno=No more processes
 *   E MQTT: MQTT transport error: type=1 ... sock_errno=11 | free=3252 largest=1024
 * 即堆只剩 3.2KB、最大连续块仅 1KB，lwIP 分配 pbuf 失败。
 *
 * 但这只说明"提交之后堆很小"，**没有说明是被哪一步吃掉的**。
 * 六个步骤里只有 apply_buses（UART 驱动 + DMA）与 apply_scheduler 已知会分配；
 * 其余步骤是否也吃内存、以及是否存在"分配了但回滚时没释放"的泄漏，
 * 光看最终数字无法区分。因此在每步前后打印 free/largest，
 * **按步骤差分**即可定位真正的消费者与泄漏点。
 *
 * WS-E 起该日志同时用于验收"apply_buses ≤ 4 KiB"：UART 冷安装被拆到
 * preinstall（app_callbacks 在 suspend 之前调用），因此稳态 apply_buses 只
 * 剩 SPI/I2C 驱动重建。失败路径上的这组打印保持常开。 */
static void log_heap_step(const char *step, const char *phase)
{
    ESP_LOGI(TAG, "[heap] %-18s %-5s free=%u largest=%u",
             step, phase,
             (unsigned)heap_caps_get_free_size(MALLOC_CAP_8BIT),
             (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_8BIT));
}

config_apply_result_t config_apply_transaction_execute(
    const config_apply_ops_t *ops, void *ctx,
    const config_manifest_t *old_manifest,
    const config_manifest_t *staged_manifest)
{
    if (!valid_ops(ops) || !staged_manifest) return CONFIG_APPLY_FAILED_UNCHANGED;

    /* Phase A: all-or-nothing refusal before begin_transaction() touches any
     * runtime state.  This is the ONLY outcome that may be reported as
     * CONFIG_APPLY_FAILED_UNCHANGED. */
    if (!config_apply_transaction_can_start(ops)) {
        return CONFIG_APPLY_FAILED_UNCHANGED;
    }

    esp_err_t err = ops->begin_transaction(ctx);
    if (err != ESP_OK) return CONFIG_APPLY_FAILED_UNCHANGED;
    err = ops->snapshot(ctx);
    if (err != ESP_OK) {
        ops->end_transaction(ctx);
        return CONFIG_APPLY_FAILED_UNCHANGED;
    }
    err = ops->prepare(ctx);
    /* prepare owns the checked scheduler stop. A failure can be partial and
     * therefore cannot safely enter destructive rollback teardown. Keep the
     * peripheral transaction gate held until the caller executes fail-hard
     * restart, so no runtime command can enter the inconsistent state. */
    if (err != ESP_OK) return CONFIG_APPLY_FATAL;

    /* 逐步记录失败点（2026-10-05）。
     *
     * 为什么必须这样做：排查"配置永远 success=false"时，现场串口只有一行
     * `E CALLBACK: Rejecting ConfigManifest transaction: result=2`
     * （result=2 = CONFIG_APPLY_FAILED_RESTORED），**没有任何信息说明是哪一步失败**。
     *
     * 下面这条链有 6 个可能失败的步骤，各自的失败现象与修法完全不同。
     * 因为缺少这行日志，排查只能靠"改一处配置、等一轮同步、看是否变好"来猜 ——
     * 实测先后误判为 PWM 引脚冲突、I2C 地址、log_stream，**每次猜测都被证伪**，
     * 白白消耗了多次硬件往返。
     *
     * 教训：事务型代码的失败路径必须能指认失败点，否则"失败"是不可诊断的黑洞。
     *
     * WS-E 起每步之前再加一次水位门禁（Phase B）。prepare 已经停下 scheduler，
     * 这里不能再返回 UNCHANGED（那会谎报"什么都没改"），因此闸门失败按普通
     * 步骤失败处理，交给下面的 rollback()。 */
    if (err == ESP_OK) {
        const size_t need = step_need(ops->step_need_apply_dma,
                                      CONFIG_TX_STEP_NEED_DEFAULT);
        if (!mem_guard_can_start(need)) {
            log_mem_gate("B", "apply_dma", need);
            err = ESP_ERR_NO_MEM;
        } else {
            log_heap_step("apply_dma", "in");
            err = ops->apply_dma(ctx, staged_manifest);
            log_heap_step("apply_dma", "out");
        }
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "config apply failed at apply_dma: %s (0x%x)",
                     esp_err_to_name(err), (unsigned)err);
        }
    }
    if (err == ESP_OK) {
        const size_t need = step_need(ops->step_need_apply_peripherals,
                                      CONFIG_TX_STEP_NEED_DEFAULT);
        if (!mem_guard_can_start(need)) {
            log_mem_gate("B", "apply_peripherals", need);
            err = ESP_ERR_NO_MEM;
        } else {
            log_heap_step("apply_peripherals", "in");
            err = ops->apply_peripherals(ctx, staged_manifest);
            log_heap_step("apply_peripherals", "out");
        }
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "config apply failed at apply_peripherals: %s (0x%x)",
                     esp_err_to_name(err), (unsigned)err);
        }
    }
    if (err == ESP_OK) {
        const size_t need = step_need(ops->step_need_apply_buses,
                                      CONFIG_TX_STEP_NEED_APPLY_BUSES);
        if (!mem_guard_can_start(need)) {
            log_mem_gate("B", "apply_buses", need);
            err = ESP_ERR_NO_MEM;
        } else {
            log_heap_step("apply_buses", "in");
            err = ops->apply_buses(ctx, staged_manifest);
            log_heap_step("apply_buses", "out");
        }
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "config apply failed at apply_buses: %s (0x%x)",
                     esp_err_to_name(err), (unsigned)err);
        }
    }
    if (err == ESP_OK) {
        const size_t need = step_need(ops->step_need_apply_scheduler,
                                      CONFIG_TX_STEP_NEED_DEFAULT);
        if (!mem_guard_can_start(need)) {
            log_mem_gate("B", "apply_scheduler", need);
            err = ESP_ERR_NO_MEM;
        } else {
            log_heap_step("apply_scheduler", "in");
            err = ops->apply_scheduler(ctx, staged_manifest);
            log_heap_step("apply_scheduler", "out");
        }
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "config apply failed at apply_scheduler: %s (0x%x)",
                     esp_err_to_name(err), (unsigned)err);
        }
    }
    if (err == ESP_OK) {
        const size_t need = step_need(ops->step_need_apply_log_stream,
                                      CONFIG_TX_STEP_NEED_DEFAULT);
        if (!mem_guard_can_start(need)) {
            /* Log streaming is a diagnostic side channel; a low-water refusal
             * must not abort the transaction.  The step callback itself also
             * enforces this (see app_callbacks tx_apply_log_stream), so here
             * we only keep the gate from turning into a transaction failure. */
            log_mem_gate("B", "apply_log_stream", need);
        } else {
            log_heap_step("apply_log_stream", "in");
            err = ops->apply_log_stream(ctx, staged_manifest);
            log_heap_step("apply_log_stream", "out");
            if (err != ESP_OK) {
                ESP_LOGE(TAG, "config apply failed at apply_log_stream: %s (0x%x)",
                         esp_err_to_name(err), (unsigned)err);
            }
        }
    }
    if (err == ESP_OK) {
        /* commit_manifest 返回 bool，单独处理：失败时也要能指认。 */
        log_heap_step("commit_manifest", "in");
        if (!ops->commit_manifest(ctx)) {
            ESP_LOGE(TAG, "config apply failed at commit_manifest (returned false)");
            err = ESP_FAIL;
        }
    }
    if (err == ESP_OK) {
        ops->end_transaction(ctx);
        return CONFIG_APPLY_OK;
    }

    config_apply_result_t result = rollback(ops, ctx, old_manifest);
    /* Fatal means runtime is neither restored nor safely stopped. Keep the
     * gate held until the caller's fail-hard restart; non-fatal outcomes are
     * mutually consistent and may release it normally. */
    if (result != CONFIG_APPLY_FATAL) ops->end_transaction(ctx);
    return result;
}

bool config_apply_result_requires_restart(config_apply_result_t result)
{
    return result == CONFIG_APPLY_FATAL;
}
