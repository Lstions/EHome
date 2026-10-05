#ifndef CONFIG_APPLY_TRANSACTION_H
#define CONFIG_APPLY_TRANSACTION_H

#include <stdbool.h>
#include <stddef.h>
#include "esp_err.h"
#include "config_mgr.h"

typedef struct {
    /* Acquire before snapshot; release after commit, rollback, or safe state. */
    esp_err_t (*begin_transaction)(void *ctx);
    void (*end_transaction)(void *ctx);
    /* Snapshot every fallible runtime state before prepare can stop/mutate it. */
    esp_err_t (*snapshot)(void *ctx);
    esp_err_t (*prepare)(void *ctx);
    esp_err_t (*apply_dma)(void *ctx, const config_manifest_t *manifest);
    esp_err_t (*apply_peripherals)(void *ctx, const config_manifest_t *manifest);
    esp_err_t (*apply_buses)(void *ctx, const config_manifest_t *manifest);
    esp_err_t (*apply_scheduler)(void *ctx, const config_manifest_t *manifest);
    esp_err_t (*apply_log_stream)(void *ctx, const config_manifest_t *manifest);
    /* MUST be the transaction's last step.
     *
     * The rollback contract depends on it: stage_manifest() only writes the
     * inactive manifest slot, and commit is what flips the active pointer.  So
     * by the time any earlier step can fail, config_mgr_get_manifest() still
     * returns the pre-transaction manifest, and rollback may rebuild runtime
     * state from that live pointer instead of copying it.
     *
     * If a future change adds a fallible step AFTER commit, the caller must
     * stop passing the live pointer as old_manifest and re-establish a
     * rollback snapshot first. */
    bool (*commit_manifest)(void *ctx);

    esp_err_t (*stop_scheduler)(void *ctx);
    esp_err_t (*cleanup_buses)(void *ctx);
    esp_err_t (*restore_dma)(void *ctx);
    esp_err_t (*restore_peripherals)(void *ctx);
    esp_err_t (*restore_log_stream)(void *ctx);
    esp_err_t (*enter_safe_state)(void *ctx);
    /* ---- WS-E runtime memory gate (optional; 0 = module default) ----
     *
     * Each field is the largest CONTIGUOUS allocation the step is expected to
     * need.  execute() calls mem_guard_can_start(need) before the step and
     * treats a false result as a step failure (rollback, never UNCHANGED once
     * runtime state has been mutated).  preflight_need is checked before
     * begin_transaction() and can still return UNCHANGED. */
    size_t preflight_need;
    size_t step_need_apply_dma;
    size_t step_need_apply_peripherals;
    size_t step_need_apply_buses;
    size_t step_need_apply_scheduler;
    size_t step_need_apply_log_stream;
    /* When non-zero, execute() also requires the WS-E transaction arena to be
     * able to reserve this many bytes (pure capacity predicate). */
    size_t arena_need;
} config_apply_ops_t;

typedef enum {
    CONFIG_APPLY_OK = 0,
    CONFIG_APPLY_FAILED_UNCHANGED,
    CONFIG_APPLY_FAILED_RESTORED,
    CONFIG_APPLY_FAILED_SAFE,
    CONFIG_APPLY_FATAL,
} config_apply_result_t;

/**
 * Apply staged runtime state and publish it only after every checked subsystem
 * succeeds. On failure, restore the old manifest's runtime or enter safe state.
 * ConfigResult publication deliberately remains outside this function.
 */
config_apply_result_t config_apply_transaction_execute(
    const config_apply_ops_t *ops, void *ctx,
    const config_manifest_t *old_manifest,
    const config_manifest_t *staged_manifest);

bool config_apply_result_requires_restart(config_apply_result_t result);

/* Phase-A preflight predicate: same checks execute() performs before touching
 * runtime state (valid ops, arena reservation when requested, and the
 * per-transaction memory floor).  Pure: no side effects, no logging required.
 * Callers that must not even suspend workers use this to decide up front;
 * execute() calls it too, so a missed external call is still caught. */
bool config_apply_transaction_can_start(const config_apply_ops_t *ops);

#endif
