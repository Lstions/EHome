#include "config_apply_transaction.h"

#include "esp_log.h"
#include "esp_heap_caps.h"
#include "esp_system.h"

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
 * **按步骤差分**即可定位真正的消费者与泄漏点。 */
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
    log_heap_step("apply_dma", "in");
    err = ops->apply_dma(ctx, staged_manifest);
    log_heap_step("apply_dma", "out");
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "config apply failed at apply_dma: %s (0x%x)",
                 esp_err_to_name(err), (unsigned)err);
    }
    if (err == ESP_OK) {
        log_heap_step("apply_peripherals", "in");
        err = ops->apply_peripherals(ctx, staged_manifest);
        log_heap_step("apply_peripherals", "out");
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "config apply failed at apply_peripherals: %s (0x%x)",
                     esp_err_to_name(err), (unsigned)err);
        }
    }
    if (err == ESP_OK) {
        log_heap_step("apply_buses", "in");
        err = ops->apply_buses(ctx, staged_manifest);
        log_heap_step("apply_buses", "out");
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "config apply failed at apply_buses: %s (0x%x)",
                     esp_err_to_name(err), (unsigned)err);
        }
    }
    if (err == ESP_OK) {
        log_heap_step("apply_scheduler", "in");
        err = ops->apply_scheduler(ctx, staged_manifest);
        log_heap_step("apply_scheduler", "out");
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "config apply failed at apply_scheduler: %s (0x%x)",
                     esp_err_to_name(err), (unsigned)err);
        }
    }
    if (err == ESP_OK) {
        log_heap_step("apply_log_stream", "in");
        err = ops->apply_log_stream(ctx, staged_manifest);
        log_heap_step("apply_log_stream", "out");
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "config apply failed at apply_log_stream: %s (0x%x)",
                     esp_err_to_name(err), (unsigned)err);
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
