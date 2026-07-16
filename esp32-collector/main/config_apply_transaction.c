#include "config_apply_transaction.h"

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
    if (ops->restore_peripherals(ctx) != ESP_OK) rollback_err = ESP_FAIL;
    if (old_manifest) {
        if (ops->apply_buses(ctx, old_manifest) != ESP_OK) rollback_err = ESP_FAIL;
        if (ops->apply_scheduler(ctx, old_manifest) != ESP_OK) rollback_err = ESP_FAIL;
    }
    if (ops->restore_log_stream(ctx) != ESP_OK) rollback_err = ESP_FAIL;

    if (rollback_err != ESP_OK) {
        return ops->enter_safe_state(ctx) == ESP_OK
            ? CONFIG_APPLY_FAILED_SAFE : CONFIG_APPLY_FATAL;
    }
    return CONFIG_APPLY_FAILED_RESTORED;
}

config_apply_result_t config_apply_transaction_execute(
    const config_apply_ops_t *ops, void *ctx,
    const config_manifest_t *old_manifest,
    const config_manifest_t *staged_manifest)
{
    if (!valid_ops(ops) || !staged_manifest) return CONFIG_APPLY_FAILED_UNCHANGED;

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
    err = ops->apply_dma(ctx, staged_manifest);
    if (err == ESP_OK) err = ops->apply_peripherals(ctx, staged_manifest);
    if (err == ESP_OK) err = ops->apply_buses(ctx, staged_manifest);
    if (err == ESP_OK) err = ops->apply_scheduler(ctx, staged_manifest);
    if (err == ESP_OK) err = ops->apply_log_stream(ctx, staged_manifest);
    if (err == ESP_OK && ops->commit_manifest(ctx)) {
        ops->end_transaction(ctx);
        return CONFIG_APPLY_OK;
    }
    if (err == ESP_OK) err = ESP_FAIL;

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
