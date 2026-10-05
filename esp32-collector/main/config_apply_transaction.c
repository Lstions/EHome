#include "config_apply_transaction.h"

#include "esp_log.h"

static const char *TAG = "CFG_TX";

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
     * 教训：事务型代码的失败路径必须能指认失败点，否则"失败"是不可诊断的黑洞。 */
    err = ops->apply_dma(ctx, staged_manifest);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "config apply failed at apply_dma: %s (0x%x)",
                 esp_err_to_name(err), (unsigned)err);
    }
    if (err == ESP_OK) {
        err = ops->apply_peripherals(ctx, staged_manifest);
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "config apply failed at apply_peripherals: %s (0x%x)",
                     esp_err_to_name(err), (unsigned)err);
        }
    }
    if (err == ESP_OK) {
        err = ops->apply_buses(ctx, staged_manifest);
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "config apply failed at apply_buses: %s (0x%x)",
                     esp_err_to_name(err), (unsigned)err);
        }
    }
    if (err == ESP_OK) {
        err = ops->apply_scheduler(ctx, staged_manifest);
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "config apply failed at apply_scheduler: %s (0x%x)",
                     esp_err_to_name(err), (unsigned)err);
        }
    }
    if (err == ESP_OK) {
        err = ops->apply_log_stream(ctx, staged_manifest);
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "config apply failed at apply_log_stream: %s (0x%x)",
                     esp_err_to_name(err), (unsigned)err);
        }
    }
    if (err == ESP_OK) {
        /* commit_manifest 返回 bool，单独处理：失败时也要能指认。 */
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
