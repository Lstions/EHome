#ifndef CONFIG_APPLY_TRANSACTION_H
#define CONFIG_APPLY_TRANSACTION_H

#include <stdbool.h>
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
    bool (*commit_manifest)(void *ctx);

    esp_err_t (*stop_scheduler)(void *ctx);
    esp_err_t (*cleanup_buses)(void *ctx);
    esp_err_t (*restore_dma)(void *ctx);
    esp_err_t (*restore_peripherals)(void *ctx);
    esp_err_t (*restore_log_stream)(void *ctx);
    esp_err_t (*enter_safe_state)(void *ctx);
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

#endif
