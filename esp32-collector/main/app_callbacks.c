/**
 * @file app_callbacks.c
 * @brief WiFi / MQTT / Transport state and message callbacks.
 *
 * All callbacks receive app_state_t *ctx for thread-safe access to
 * shared state.  Config-manifest application is protected by mutex.
 *
 * Config-manifest application is an ordered transaction: active manifest and
 * ConfigResult success are published only after DMA, peripheral, bus, and
 * scheduler setup all succeed. Runtime rollback is checked; unrecoverable
 * rollback enters a deterministic safe state.
 */

#include "app_state.h"
#include "app_callbacks.h"
#include "net_policy.h"   /* D-03：TCP 启动决策（宿主可测纯函数）*/
#include "config_apply_transaction.h"
#include "periph_config_apply.h"
#include "bus_manager.h"
#include "hello_handshake.h"
#include "crash_diag.h"
#include "boot_guard.h"
#include "msg_handler.h"
#include "msg_handler_internal.h"
#include "scheduler.h"
#include "bus_worker.h"
#include "config_mgr.h"
#include "config_tx_arena.h"
#include "mem_guard.h"
#include "dma_pool.h"
#include "sync_manager.h"
#include "rgb_led.h"
#include "wifi_mgr.h"
#include "ehome_mqtt.h"
#include "transport.h"
#include "log_stream.h"
#include "gpio_ctrl.h"
#include "pwm_ctrl.h"
#include "periph_owner.h"
#include "esp_log.h"
#include "esp_heap_caps.h"
#include "esp_system.h"
#include "frame_codec.h"
#include <stdbool.h>
#include <stddef.h>
#include <string.h>
#include <stdlib.h>

#define TAG "CALLBACK"

/* ==== Config Manifest helper ==== */

static bool is_config_manifest(const uint8_t *data, size_t len)
{
    return len > 0 && data[0] == MSG_CONFIG_MFST;
}

/* Strong override of the weak hook declared in components/msg_handler/handler_data.c.
 *
 * msg_handler cannot call into main/, so the OTA admission gate is injected
 * this way, backed by the one water-level implementation (mem_guard).  Defined
 * here because this file already owns the config-apply memory gate and is
 * linked into the same image. */
bool ehome_mem_can_start(size_t need_bytes)
{
    return mem_guard_can_start(need_bytes);
}

typedef struct {
    app_state_t *app;
    dma_pool_state_t old_dma;
    periph_runtime_snapshot_t old_peripherals;
    bool dma_snapshot_valid;
    bool old_log_active;
    uint8_t old_log_level;
    scheduler_queues_t queues;
} manifest_tx_ctx_t;

static esp_err_t tx_begin(void *opaque)
{
    (void)opaque;
    return periph_owner_transaction_begin();
}

static void tx_end(void *opaque)
{
    (void)opaque;
    periph_owner_transaction_end();
}

static esp_err_t tx_snapshot(void *opaque)
{
    manifest_tx_ctx_t *tx = opaque;
    esp_err_t err = ESP_OK;
    if (tx->app->dma_pool) {
        err = dma_pool_snapshot_state(tx->app->dma_pool, &tx->old_dma);
        tx->dma_snapshot_valid = err == ESP_OK;
    } else {
        tx->dma_snapshot_valid = true;
    }
    tx->old_log_active = log_stream_is_active();
    tx->old_log_level = log_stream_get_level();
    if (err == ESP_OK) err = periph_config_snapshot_locked(&tx->old_peripherals);
    if (err == ESP_OK) bus_manager_snapshot_leases(&tx->app->bus_runtime);
    return err;
}

static esp_err_t tx_prepare(void *opaque)
{
    (void)opaque;
    return !scheduler_is_running() || scheduler_stop() == SCHED_OK ? ESP_OK : ESP_FAIL;
}

static esp_err_t tx_apply_dma(void *opaque, const config_manifest_t *manifest)
{
    manifest_tx_ctx_t *tx = opaque;
    if (!tx->app->dma_pool) return manifest->dma_config_count ? ESP_ERR_INVALID_STATE : ESP_OK;
    esp_err_t err = dma_pool_reset_runtime(tx->app->dma_pool);
    for (int i = 0; err == ESP_OK && i < manifest->dma_config_count; i++) {
        const config_dma_channel_t *dc = &manifest->dma_configs[i];
        err = dma_pool_apply_config(tx->app->dma_pool, dc->dma_id,
                                    dc->enabled, dc->bind_to);
    }
    return err;
}

static esp_err_t tx_apply_peripherals(void *opaque, const config_manifest_t *manifest)
{
    (void)opaque;
    return handler_periph_apply_configs_locked(manifest);
}

static esp_err_t tx_apply_buses(void *opaque, const config_manifest_t *manifest)
{
    manifest_tx_ctx_t *tx = opaque;
    return bus_manager_apply_manifest(&tx->app->bus_runtime, manifest);
}

static esp_err_t tx_apply_scheduler(void *opaque, const config_manifest_t *manifest)
{
    manifest_tx_ctx_t *tx = opaque;
    sched_err_t err = manifest->applied
        ? scheduler_start_manifest(&tx->queues, manifest)
        : scheduler_prepare(&tx->queues, manifest);
    return err == SCHED_OK ? ESP_OK : ESP_FAIL;
}

/* Log streaming is a diagnostic side-channel: driving it to the requested state
 * must never be able to abort a config transaction, because an aborted
 * transaction can escalate to a fail-hard restart -- i.e. rebooting the device
 * merely because a log toggle did not finish in time.
 *
 * That escalation is reachable in practice, and log_stream's own behaviour is
 * correct as designed. log_stream_stop() is cooperative: when the TX worker is
 * inside publish() it does not force-delete the worker, it leaves the state at
 * STOPPING and reports ESP_FAIL. The old code turned that report straight into
 * CONFIG_APPLY_FATAL. Measured on ESP32-C6 (fw 2.5.29) at a ~1.2 s toggle
 * cadence, 12 flips:
 *
 *     I LOG_STREAM: Started (level=2, ring=4 entries)
 *     W LOG_STREAM: log_tx_task stop timed out; awaiting cooperative exit
 *     E CALLBACK:   Rejecting ConfigManifest transaction: result=4
 *     E CALLBACK:   Unrecoverable config transaction; restarting fail-hard
 *     rst:0xc (SW_CPU),boot:0xc            <- device rebooted mid-operation
 *
 * So retry briefly instead: the worker owns its exit and does reach STOPPED on
 * its own, so a bounded retry normally lands the requested state (a stop retry
 * returns ESP_OK as soon as the worker has settled). If it still has not
 * settled, report success and let the next config sync reconcile -- a
 * transiently stale log stream is strictly better than a reboot. */
#define LOG_STREAM_SETTLE_ATTEMPTS 8
#define LOG_STREAM_SETTLE_DELAY_MS 250

static esp_err_t log_stream_apply_state(bool enabled, uint8_t level)
{
    esp_err_t err = ESP_FAIL;
    for (int attempt = 0; attempt < LOG_STREAM_SETTLE_ATTEMPTS; ++attempt) {
        if (enabled) {
            if (!log_stream_is_active()) {
                /* WS-E water gate on the START transition only (stopping or
                 * changing level on a live stream costs nothing).  log_stream
                 * was already changed to a static task, but its publish path
                 * still needs contiguous buffers; refusing the start is
                 * harmless (next sync retries) whereas failing the whole
                 * transaction for a diagnostic side channel is not. */
                if (!mem_guard_can_start(2048)) {
                    ESP_LOGW(TAG, "[memgate] phase=B step=log_stream need=2048 "
                                  "largest=%u free=%u floor=%u rc=SKIP",
                             (unsigned)mem_guard_largest(), (unsigned)mem_guard_free(),
                             (unsigned)mem_guard_floor_bytes());
                    return ESP_OK;
                }
                err = log_stream_start(level);
            } else {
                err = log_stream_set_level(level);
            }
        } else {
            err = log_stream_stop();
        }
        if (err == ESP_OK) return ESP_OK;
        /* Only lifecycle-transient outcomes are worth retrying: a stop that is
         * still waiting on the worker reports ESP_FAIL, and a start/stop that
         * collides with an in-flight transition reports ESP_ERR_INVALID_STATE.
         * Anything else is a genuine error the caller should still see. */
        if (err != ESP_FAIL && err != ESP_ERR_INVALID_STATE) return err;
        vTaskDelay(pdMS_TO_TICKS(LOG_STREAM_SETTLE_DELAY_MS));
    }
    ESP_LOGW(TAG, "log stream did not settle (want=%d); deferring to next sync",
             (int)enabled);
    return ESP_OK;
}

static esp_err_t tx_apply_log_stream(void *opaque, const config_manifest_t *manifest)
{
    (void)opaque;
    /* 安全模式下强制关闭日志上报，**忽略服务端下发的开关**。
     *
     * 这是启动熔断能真正止血的关键一环。原因：日志开关的配置随 ConfigManifest
     * 持久化到 NVS，设备每次重启后都会重新应用**同一个**配置；如果那个配置正是
     * 引发崩溃的那一个，熔断就会陷入"重启 -> 重新应用坏配置 -> 再崩"的循环，
     * 而熔断本身解决不了这个问题 —— 它只能让重启变慢，不能让它停。
     *
     * 因此安全模式必须**覆盖**服务端配置。这是刻意的"配置被拒绝"行为，
     * 并且：
     *   - 由 boot_guard_notify_server_contact() 在设备证明健康后自动解除，
     *     不需要人工干预；
     *   - 会打 ERROR 级日志，运维能看到"配置没有按预期生效及原因"。
     *
     * 取舍：运维在安全模式下**无法**远程重新打开日志上报（要等设备证明健康后
     * 自动恢复）。这是有意的 —— 允许远程重新打开就等于允许远程重新触发崩溃循环。
     * 设备仍保持联网、上报心跳、接受命令，所以仍然可诊断、可恢复。 */
    if (boot_guard_in_safe_mode() && manifest->log_stream_enabled) {
        ESP_LOGE(TAG, "BOOT_GUARD: log stream requested ON by manifest but device is "
                      "in SAFE MODE (%s); forcing it OFF. It will be restored "
                      "automatically once a server sync proves health.",
                 boot_guard_safe_mode_reason());
        return log_stream_apply_state(false, manifest->log_stream_level);
    }
    return log_stream_apply_state(manifest->log_stream_enabled,
                                  manifest->log_stream_level);
}

static bool tx_commit(void *opaque)
{
    (void)opaque;
    if (!config_mgr_commit_staged_manifest()) return false;
    scheduler_activate();
    return true;
}

static esp_err_t tx_stop_scheduler(void *opaque)
{
    (void)opaque;
    return scheduler_stop() == SCHED_OK ? ESP_OK : ESP_FAIL;
}

static esp_err_t tx_cleanup_buses(void *opaque)
{
    manifest_tx_ctx_t *tx = opaque;
    bus_worker_discard_queued(&tx->app->bus_runtime);
    return bus_manager_cleanup_all(&tx->app->bus_runtime);
}

static esp_err_t tx_restore_dma(void *opaque)
{
    manifest_tx_ctx_t *tx = opaque;
    if (!tx->app->dma_pool) return ESP_OK;
    return tx->dma_snapshot_valid
        ? dma_pool_restore_state(tx->app->dma_pool, &tx->old_dma)
        : ESP_FAIL;
}

static esp_err_t tx_restore_peripherals(void *opaque)
{
    manifest_tx_ctx_t *tx = opaque;
    return periph_config_restore_locked(&tx->old_peripherals);
}

static esp_err_t tx_restore_log_stream(void *opaque)
{
    manifest_tx_ctx_t *tx = opaque;
    return log_stream_apply_state(tx->old_log_active, tx->old_log_level);
}

static esp_err_t tx_safe_state(void *opaque)
{
    manifest_tx_ctx_t *tx = opaque;
    static const config_manifest_t empty_manifest;
    esp_err_t scheduler_err = scheduler_stop() == SCHED_OK ? ESP_OK : ESP_FAIL;
    if (scheduler_err != ESP_OK) return ESP_FAIL;
    esp_err_t bus_err = bus_manager_cleanup_all(&tx->app->bus_runtime);
    esp_err_t dma_err = tx->app->dma_pool
        ? dma_pool_reset_runtime(tx->app->dma_pool) : ESP_OK;
    esp_err_t periph_err = handler_periph_apply_configs_locked(&empty_manifest);
    /* Log streaming must not gate the safe state. A cooperative stop that has
     * not finished yet is not a reason to keep the runtime unrecovered, and
     * failing here is what escalated a log toggle into a fail-hard restart. */
    (void)log_stream_stop();
    return scheduler_err == ESP_OK && bus_err == ESP_OK && dma_err == ESP_OK &&
           periph_err == ESP_OK ? ESP_OK : ESP_FAIL;
}

static const config_apply_ops_t s_manifest_tx_ops = {
    .begin_transaction = tx_begin,
    .end_transaction = tx_end,
    .snapshot = tx_snapshot,
    .prepare = tx_prepare,
    .apply_dma = tx_apply_dma,
    .apply_peripherals = tx_apply_peripherals,
    .apply_buses = tx_apply_buses,
    .apply_scheduler = tx_apply_scheduler,
    .apply_log_stream = tx_apply_log_stream,
    .commit_manifest = tx_commit,
    .stop_scheduler = tx_stop_scheduler,
    .cleanup_buses = tx_cleanup_buses,
    .restore_dma = tx_restore_dma,
    .restore_peripherals = tx_restore_peripherals,
    .restore_log_stream = tx_restore_log_stream,
    .enter_safe_state = tx_safe_state,
    /* WS-E memory needs: the contiguous allocation each step is expected to
     * make.  apply_buses keeps only SPI (~1.35 KB) and I2C (~1.42 KB) driver
     * rebuilds because the preinstall step installs UARTs beforehand, so 4 KiB
     * bounds it.  arena_need reserves the transaction workspace. */
    .preflight_need            = 4096u,
    .step_need_apply_dma       = 1024u,
    .step_need_apply_peripherals = 1024u,
    .step_need_apply_buses     = 4096u,
    .step_need_apply_scheduler = 1024u,
    .step_need_apply_log_stream = 2048u,
    .arena_need                = 1152u,
};

static bool extract_manifest_identity(const uint8_t *data, size_t len,
                                      char *manifest_id, size_t manifest_cap,
                                      char *sync_id, size_t sync_cap)
{
    frame_decoder_t dec;
    if (frame_decoder_init(&dec, data, len) != FRAME_OK) return false;
    frame_field_t field;
    frame_err_t err;
    bool have_manifest = false, have_sync = false;
    while ((err = frame_decoder_next(&dec, &field)) == FRAME_OK) {
        if (field.field_num == 1) {
            if (frame_field_get_string(&field, manifest_id, manifest_cap) != FRAME_OK) return false;
            have_manifest = true;
        } else if (field.field_num == 8) {
            if (frame_field_get_string(&field, sync_id, sync_cap) != FRAME_OK) return false;
            have_sync = true;
        }
    }
    return err == FRAME_DONE && have_manifest && have_sync && manifest_id[0] && sync_id[0];
}

/* One-time UART driver cost per transaction.
 *
 * WS-E: UART drivers are installed once and then reconfigured
 * (install-once contract in bus_manager/bus_dma).  The very first manifest
 * after boot still has to install them, and that is ~3.1 KB of contiguous heap
 * per controller (IDF allocates 9 objects per driver; three S3 UARTs measured
 * ~9.3 KB).  Doing it here, as an explicitly named and gated step, keeps that
 * cost out of apply_buses, whose budget is 4 KiB.
 *
 * It runs with the workers already suspended, so an RX event cannot race the
 * install/remap; gating happens before any install with a conservative bound
 * for one controller (largest single object is the 512 B rx_data_buf plus ring
 * buffers — 3584 covers all nine) and the driver itself reports allocation
 * failure with full heap state. */
#define CONFIG_TX_UART_INSTALL_LARGEST_NEED 3584u

static void log_heap_step_here(const char *step, const char *phase)
{
#ifdef EHOME_MEM_DIAG
    /* 口径：internal —— 与门禁（mem_guard）同一口径，也与 config_apply_transaction.c
     * 的 [heap] 每步行一致（同一份差分表里两处口径不能不同，否则同一列数字不可比）。
     *
     * 这里**故意不并打 total**：本行的消费方式是"in/out 按步骤差分"，而 total
     * 在 s3p 上恒等于 PSRAM 的 8.25 MB —— UART 驱动分配的是内部 RAM，不碰 PSRAM，
     * 于是 total 的差分恒为 0，只会把行拉长、稀释真正的读数。被这一步吃掉的
     * ~9.3 KB（S3 三路 UART）全部记在 internal 上，也正是门禁判的那个数。
     * "内部紧、PSRAM 宽裕"的对照请看 [bootheap]/[stack] 的双口径行。 */
    ESP_LOGI(TAG, "[heap] %-18s %-5s free=%u largest=%u (internal)",
             step, phase,
             (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT),
             (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT));
#else
    (void)step; (void)phase;
#endif
}

/* Transactional config apply. A checked full runtime rebuild is intentionally
 * used for both first and subsequent manifests so no incremental path can
 * acknowledge a partially applied channel set. */
static void handle_config_applied(app_state_t *s, const uint8_t *data, size_t len)
{
    /* Parse and validate the complete manifest first. Same-manifest retransmits
     * must still echo their current sync generation in ConfigResult. */
    char incoming_manifest_id[64] = {0};
    char incoming_sync_id[64] = {0};
    if (!extract_manifest_identity(data, len, incoming_manifest_id, sizeof(incoming_manifest_id),
                                   incoming_sync_id, sizeof(incoming_sync_id))) {
        ESP_LOGE(TAG, "Rejecting ConfigManifest without valid correlation identity");
        return;
    }
    /* Rollback source: the live active manifest.
     *
     * stage_manifest() only writes the INACTIVE slot and commit_staged_manifest()
     * is the transaction's last step, so this pointer still names the
     * pre-transaction config when rollback runs, and no concurrent writer can
     * touch it (config lock + bus_worker_suspend).  That is why there is no
     * 5,400 B calloc snapshot here any more.
     *
     * NULL is expected until the first manifest is committed. */
    const config_manifest_t *active_cfg = config_mgr_get_manifest();
    bool had_old = active_cfg != NULL && active_cfg->applied;

    /* Phase A (pre-suspend): refuse before touching anything.  The arena must
     * be able to hold the transaction workspace and the steps must fit under
     * the model's memory floor.  This is the only point that may report
     * "unchanged" without having mutated runtime state. */
    if (!config_tx_arena_init() ||
        !config_tx_arena_can_reserve(CONFIG_TX_ARENA_BUMP_RESERVE)) {
        ESP_LOGE(TAG, "[memgate] phase=A step=arena reason=unavailable "
                      "capacity=%u", (unsigned)config_tx_arena_capacity());
        msg_handler_send_config_result(incoming_manifest_id, incoming_sync_id, false);
        return;
    }
    if (!config_apply_transaction_can_start(&s_manifest_tx_ops)) {
        ESP_LOGE(TAG, "Rejecting ConfigManifest before suspend: memory gate");
        msg_handler_send_config_result(incoming_manifest_id, incoming_sync_id, false);
        return;
    }

    /* Suspend rx_task/cmd_task before cleanup to prevent race.  A worker
     * waiting on a queue must acknowledge within a bounded deadline; never
     * proceed to delete driver/event queues after an incomplete suspend. */
    if (!bus_worker_suspend()) {
        ESP_LOGE(TAG, "Rejecting ConfigManifest: worker suspend timeout");
        msg_handler_send_config_result(incoming_manifest_id, incoming_sync_id, false);
        return;
    }

    /* Mutex — bus teardown/rebuild may block */
    app_state_lock_config();

    /* === Phase 0: Stage and validate new manifest while the active one remains visible. === */
    if (!config_mgr_stage_manifest(data, len)) {
        ESP_LOGE(TAG, "Rejecting invalid or conflicting ConfigManifest");
        msg_handler_send_config_result(incoming_manifest_id, incoming_sync_id, false);
        app_state_unlock_config();
        bus_worker_resume();
        return;
    }
    const config_manifest_t *staged_cfg = config_mgr_get_staged_manifest();
    if (!staged_cfg) {
        ESP_LOGE(TAG, "Rejecting ConfigManifest because staging disappeared");
        config_mgr_discard_staged_manifest();
        msg_handler_send_config_result(incoming_manifest_id, incoming_sync_id, false);
        app_state_unlock_config();
        bus_worker_resume();
        return;
    }

    char attempted_id[sizeof(staged_cfg->manifest_id)];
	char attempted_sync_id[sizeof(staged_cfg->sync_id)];
    memcpy(attempted_id, staged_cfg->manifest_id, sizeof(attempted_id));
    attempted_id[sizeof(attempted_id) - 1] = '\0';
	memcpy(attempted_sync_id, staged_cfg->sync_id, sizeof(attempted_sync_id));
	attempted_sync_id[sizeof(attempted_sync_id) - 1] = '\0';
    /* Reserve the transaction workspace from the fixed arena.  No heap
     * allocation can fail later because the water level moved. */
    if (!config_tx_arena_begin()) {
        ESP_LOGE(TAG, "[memgate] step=arena reason=begin_failed");
        config_mgr_discard_staged_manifest();
        msg_handler_send_config_result(incoming_manifest_id, incoming_sync_id, false);
        app_state_unlock_config();
        bus_worker_resume();
        return;
    }
    manifest_tx_ctx_t *tx = config_tx_arena_alloc(sizeof(*tx));
    if (tx == NULL) {
        ESP_LOGE(TAG, "[memgate] step=arena reason=alloc_failed need=%u capacity=%u",
                     (unsigned)sizeof(manifest_tx_ctx_t), (unsigned)config_tx_arena_capacity());
        (void)config_tx_arena_end();
        config_mgr_discard_staged_manifest();
        msg_handler_send_config_result(incoming_manifest_id, incoming_sync_id, false);
        app_state_unlock_config();
        bus_worker_resume();
        return;
    }
    memset(tx, 0, sizeof(*tx));

    /* Pay the one-time UART install cost as its own step (design §4.4/§5.7).
     * Idempotent for controllers already resident, so on steady-state applies
     * this loop only reconfigures.  A refusal is reported as an unchanged
     * manifest: staging is discarded and nothing has been applied. */
    if (!mem_guard_can_start(CONFIG_TX_UART_INSTALL_LARGEST_NEED)) {
        ESP_LOGE(TAG, "[memgate] phase=preinstall step=uart_install need=%u largest=%u "
                      "free=%u floor=%u rc=UNCHANGED",
                 (unsigned)CONFIG_TX_UART_INSTALL_LARGEST_NEED,
                 (unsigned)mem_guard_largest(), (unsigned)mem_guard_free(),
                 (unsigned)mem_guard_floor_bytes());
        (void)config_tx_arena_end();
        config_mgr_discard_staged_manifest();
        msg_handler_send_config_result(incoming_manifest_id, incoming_sync_id, false);
        app_state_unlock_config();
        bus_worker_resume();
        return;
    }
    log_heap_step_here("uart_preinstall", "in");
    esp_err_t preinstall_err = bus_manager_preinstall_uarts(&s->bus_runtime, staged_cfg);
    log_heap_step_here("uart_preinstall", "out");
    if (preinstall_err != ESP_OK) {
        ESP_LOGE(TAG, "UART preinstall failed: %s (0x%x)", esp_err_to_name(preinstall_err),
                 (unsigned)preinstall_err);
        (void)config_tx_arena_end();
        config_mgr_discard_staged_manifest();
        msg_handler_send_config_result(incoming_manifest_id, incoming_sync_id, false);
        app_state_unlock_config();
        bus_worker_resume();
        return;
    }

    tx->app = s;
    tx->queues = (scheduler_queues_t){
        .uart0_cmd_queue = s->uart0_cmd_queue,
        .uart1_cmd_queue = s->uart1_cmd_queue,
        .uart2_cmd_queue = s->uart2_cmd_queue,
        .spi_cmd_queue = s->spi_cmd_queue,
        .i2c_cmd_queue = s->i2c_cmd_queue,
        .uart_route = bus_manager_get_uart_port,
        .route_ctx = &s->bus_runtime,
    };
    config_apply_result_t tx_result = config_apply_transaction_execute(
        &s_manifest_tx_ops, tx, had_old ? active_cfg : NULL, staged_cfg);
    if (tx_result != CONFIG_APPLY_OK) {
        ESP_LOGE(TAG, "Rejecting ConfigManifest transaction: result=%d", (int)tx_result);
        config_mgr_discard_staged_manifest();
        msg_handler_send_config_result(attempted_id, attempted_sync_id, false);
        if (config_apply_result_requires_restart(tx_result)) {
            /* Runtime is neither restored nor safely stopped. Keep bus workers
             * suspended and retain the config lock until the restart executes. */
            ESP_LOGE(TAG, "Unrecoverable config transaction; restarting fail-hard");
            /* Arena state does not matter across the restart; release the
             * transaction marker so a post-restart path cannot see it stuck. */
            (void)config_tx_arena_end();
            /* 记下重启原因 -> 下次启动随 BOOT 报告上传。没有这一行，
             * 复位原因只会笼统显示 SOFTWARE，无法区分是配置事务失败、
             * 还是别处的 esp_restart()。 */
            crash_diag_mark_reboot_reason("config_apply_failed");
            esp_restart();
            return;
        }
        if (!config_tx_arena_end()) {
            ESP_LOGE(TAG, "[memgate] step=arena reason=canary_corrupted (failed transaction)");
        }
        app_state_unlock_config();
        bus_worker_resume();
        return;
    }
    if (!config_tx_arena_end()) {
        /* Canary damaged: something wrote past its arena allocation.  The IDF
         * driver state is already committed at this point, so escalate on the
         * next config sync rather than aborting a successful apply; the loud
         * error is what makes it diagnosable. */
        ESP_LOGE(TAG, "[memgate] step=arena reason=canary_corrupted rc=DEGRADED");
    }

    const config_manifest_t *new_cfg = config_mgr_get_manifest();
    const char *new_id = new_cfg ? new_cfg->manifest_id : NULL;
    ESP_LOGI(TAG, "Config transaction committed: manifest=%s channels=%d",
             new_id ? new_id : "(null)", new_cfg ? new_cfg->channel_count : 0);

    /* ConfigResult success is deliberately last: bus, DMA, peripherals,
     * scheduler creation and active manifest publication all succeeded. */
    /* Persist synchronization metadata before acknowledging success. */
    if (sync_manager_on_config_applied(0, new_id ? new_id : "") != ESP_OK) {
        msg_handler_send_config_result(new_id ? new_id : "unknown", new_cfg ? new_cfg->sync_id : "", false);
        app_state_unlock_config();
        bus_worker_resume();
        return;
    }
	if (msg_handler_send_config_result(new_id ? new_id : "unknown", new_cfg ? new_cfg->sync_id : "", true) != ESP_OK) {
		ESP_LOGE(TAG, "Config committed but ConfigResult publish failed; keeping timeout/retry active");
	} else {
		sync_manager_cancel_config_timeout();
		sync_manager_on_downlink_received(MSG_CONFIG_MFST);
		/* 健康证明：一次完整的端到端往返成功了（收到 manifest -> 应用 -> 回 ConfigResult）。
		 * 这是"设备已恢复正常"的最强证据，用来自动解除启动熔断的安全模式。
		 *
		 * 为什么用"同步成功"而不是"等够时间"：等待型判据会让一个仍在崩溃循环里
		 * 的设备自己解除降级、再次冲进去。只有真正完成了与服务端的往返，
		 * 才说明网络、配置、事务三条链路都是通的。 */
		boot_guard_notify_server_contact();
		/* The backend admits V2 actions only when the latest ResourceReport
		 * proves the applied runtime channel is enabled.  Hello-time reports
		 * describe the pre-manifest state, so refresh immediately after a
		 * successful commit rather than leaving the node falsely channel-less
		 * until an unrelated QueryResources or reconnect. */
		msg_handler_send_resource_report();
	}

    ESP_LOGI(TAG, "Config→scheduler %d ch", scheduler_get_channel_count());

    app_state_unlock_config();

    /* Resume rx_task/cmd_task */
    bus_worker_resume();

    /* Log stream state was checked and applied inside the transaction, before
     * manifest commit and the success ConfigResult. */

    rgb_led_set_state(LED_STATE_RUNNING);
    ESP_LOGI(TAG, "handle_config_applied: DONE");
}

/* ==== WiFi callback ==== */

/* === MQTT lifecycle supervisor ===
 * The supervisor is the sole owner of start/reconnect/retire. WiFi callbacks
 * only wake it; status_task must remain bounded even when retire drains an
 * in-flight ESP-MQTT API operation. */
static TaskHandle_t s_mqtt_supervisor_task;

static void wake_mqtt_supervisor(void)
{
    TaskHandle_t task = s_mqtt_supervisor_task;
    if (task != NULL) (void)xTaskNotifyGive(task);
}

/* MQTT event callbacks may only wake the lifecycle owner. All subscribe,
 * reconnect, retire, and destroy work remains in mqtt_supervisor_task. */
void on_mqtt_owner_wake_cb(void *ctx)
{
    (void)ctx;
    wake_mqtt_supervisor();
}

static void mqtt_supervisor_task(void *pv)
{
    (void)pv;
    for (;;) {
        /* This task is the sole caller allowed to create, reconnect, retire,
         * or destroy the MQTT client. WiFi/transport callbacks only request
         * state and wake this owner. */
        mqtt_client_owner_step(wifi_mgr_get_state() == WIFI_MGR_CONNECTED);
        /* Wake immediately for WiFi state changes, while retaining a bounded
         * periodic recovery deadline if no callback arrives. */
        (void)ulTaskNotifyTake(pdTRUE, pdMS_TO_TICKS(5000));
    }
}

static void ensure_mqtt_supervisor(app_state_t *s)
{
    if (s_mqtt_supervisor_task != NULL) {
        wake_mqtt_supervisor();
        return;
    }
    TaskHandle_t created = NULL;
    if (xTaskCreate(mqtt_supervisor_task, "mqtt_super", 4096, NULL, 5,
                    &created) != pdPASS) {
        ESP_LOGE(TAG, "failed to create MQTT supervisor; leaving MQTT failed");
        (void)mqtt_client_request_stop();
        rgb_led_set_state(LED_STATE_MQTT_FAILED);
        on_mqtt_state_cb(MQTT_CLIENT_FAILED, s);
        return;
    }
    s_mqtt_supervisor_task = created;
}

void on_wifi_state_cb(wifi_mgr_state_t state, void *ctx)
{
    app_state_t *s = (app_state_t *)ctx;
    if (!s) return;

    switch (state) {
    case WIFI_MGR_CONNECTED:
        rgb_led_set_state(LED_STATE_MQTT_CONNECTING);
        /* Pending OTA images are confirmed only by status_task after MQTT is
         * connected and the first StatusReport has been sent. */
        /* WiFi callbacks never own MQTT lifecycle operations. They only wake
         * the long-lived supervisor, which serializes recovery and teardown. */
        ESP_LOGI(TAG, "WiFi connected, waking MQTT supervisor");
        ensure_mqtt_supervisor(s);

#ifdef CONFIG_DEBUG_TCP_ENABLED
        /* D-03 修复（2026-10-06）：本块原先写在上面的 break 之后，属于
         * 【不可达代码】—— 编译器不报错，sdkconfig 里开关也确实是 y，
         * 但 TCP 传输因此【从不启动】。3.0 以 TCP 为主传输，不修则上线即不可用。
         *
         * 不只是"把代码挪上来"：决策改由宿主可测的纯函数给出
         * （components/netpolicy），这样"该不该启动"不再只存在于控制流里，
         * 而是有测试与变异自证兜底。 */
        {
            const bool tcp_configured = (s->tcp_transport != NULL
                                         && s->tcp_transport->ops != NULL
                                         && s->tcp_transport->ops->start != NULL);
            const bool tcp_connected = (s->tcp_transport != NULL
                                        && s->tcp_transport->state == TRANSPORT_CONNECTED);
            /* 进入本 case 即 WiFi 已连上，故第三个参数为 true。 */
            if (net_policy_should_start_tcp(tcp_configured, tcp_connected, true)) {
                ESP_LOGI(TAG, "Starting TCP transport");
                s->tcp_transport->ops->start(s->tcp_transport);
            }
        }
#endif
        break;

    case WIFI_MGR_CONNECTING:
        rgb_led_set_state(LED_STATE_WIFI_CONNECTING);
        wake_mqtt_supervisor();
        break;

    case WIFI_MGR_FAILED:
        rgb_led_set_state(LED_STATE_WIFI_FAILED);
        wake_mqtt_supervisor();
        break;

    default:
        break;
    }
}

/* ==== Transport message callback ==== */

/* ⭐ 下行消息的**唯一**处理入口（P4 + P1，2026-10-07 / 决策-3.0-配置应用所有权）。
 *
 * 为什么要有它：此前"下行消息到达后做什么"被写了**两遍**（MQTT 回调、debug-TCP 回调），
 * 而 3.0 链路（devlink_on_msg）**只做了分发、没做应用** ⇒ 它的 ConfigManifest
 * "到达、被分发、然后什么都不发生"（真机：3.0 送 10 次 0 次回执，MQTT 每次都有）。
 * 根因是同一语义两份定义：`msg_handler` 分发表里的 `handler_config_process_manifest`
 * 是 no-op（它自己写明"实际工作在 handle_config_applied"），而真正管应用的地方
 * **只有两个传输回调会调** ⇒ 3.0 落在缝里。
 *
 * 现在三条路径（MQTT / debug-TCP / 3.0）**都调本函数** ⇒ 不可能再漂移。
 *
 * @param t  该消息所属的 transport；NULL 表示"没有特定 transport"（回执走广播/MQTT）。
 *           ⚠ 只有 debug-TCP 那条路径会传非 NULL（它需要"从哪来、回哪去"）。
 * @return   true 表示这条消息被当作 ConfigManifest 处理（含应用动作）。
 *
 * ⚠ 本函数做**配置事务**（较重）。在 3.0 链路上它跑在 devlink 任务栈上
 *   （栈 6144 B，实测峰值 4168 B）—— 决策文档 §4 判据 3 要求实测其影响。 */
bool ehome_handle_downlink(const uint8_t *data, size_t len, transport_t *t)
{
    /* ⚠ 不变式：这里用 app_state_get() 而**不是**调用方 ctx —— 因为本函数要能被
     * 3.0 链路（devlink_on_msg，其 dispatch 签名**没有 ctx**）调用。
     * 等价性依据：app_state_get() 返回单例 &s_app（app_state.c:212），
     * 而各回调注册时传的 ctx 也是它（main.c 用 app_state_init() 的返回值，
     * 同样返回 &s_app）⇒ **同一个对象**，行为与旧代码逐位等价。
     * 若将来 app_state 不再是单例，这条必须跟着改。 */
    app_state_t *s = app_state_get();
    if (!s || !data || len == 0) return false;

    /* ⭐ 2026-10-08（round 129）新增探针：补上 §179.6 定位的**未探针窗口**。
     *
     * 缺口：从 [linkheap] task:create（largest=31744）到 uart_preinstall
     * （largest=16384）之间，largest 掉了 **15360 B**，而那段日志里
     * **没有任何 heap 探针**（只有 HelloAck / ResourceReport / 配置解析等业务行）。
     * 现有探针（bootheap/linkheap/tlsheap）**都不覆盖**这段。
     *
     * ⇒ 在**每一次下行帧**前后各读一次堆，把窗口切成"每帧一段"，
     *   从而指认那 15360 是哪一类下行（HelloAck / Manifest / 其它）造成的。
     * ⚠ 口径与内存门禁一致（INTERNAL|8BIT），否则两列数字不可比。
     * ⚠ 仅在 EHOME_MEM_DIAG 下编入（交付态不受影响）。 */
#ifdef EHOME_MEM_DIAG
    ESP_LOGI(TAG, "[dlheap] in  type=0x%02X len=%u free=%u largest=%u",
             (unsigned)data[0], (unsigned)len,
             (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT),
             (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT));
#endif

    const bool is_cfg = is_config_manifest(data, len);
    if (is_cfg) s->config_received = true;

    /* ② 分发（回执路径随 transport 走）。 */
    if (t != NULL) {
        msg_handler_process_with_transport(data, len, t);
    } else {
        msg_handler_process(data, len);
    }

    /* ③ 应用（**这一步此前只有 MQTT/debug-TCP 有**）。 */
    if (is_cfg) {
        ESP_LOGI(TAG, "ConfigManifest 下行 ⇒ 进入应用路径");
        handle_config_applied(s, data, len);
    }
    return is_cfg;
}

void on_transport_msg_cb(const uint8_t *data, size_t len, void *ctx)
{
    app_state_t *s = (app_state_t *)ctx;
    if (!s || !data || len == 0) return;

    ESP_LOGI(TAG, "Transport msg: %d bytes", (int)len);
    /* 本路径保留那行"消息号"日志（排障用），其余全部委托给**唯一入口**。 */
    ESP_LOGI(TAG, "is_config_manifest: %d, msg_type: 0x%02X",
             (int)is_config_manifest(data, len), data[0]);

    (void)ehome_handle_downlink(data, len, s->tcp_transport);
}

/* ==== Transport state callback ==== */

void on_transport_state_cb(transport_state_t state, void *ctx)
{
    (void)ctx;
    ESP_LOGI(TAG, "Transport state: %d", state);

    if (state == TRANSPORT_CONNECTED) {
    } else if (state == TRANSPORT_DISCONNECTED || state == TRANSPORT_FAILED) {
        rgb_led_set_state(LED_STATE_MQTT_FAILED);
    }
}

/* ==== MQTT state callback ==== */

void on_mqtt_state_cb(mqtt_client_state_t state, void *ctx)
{
    app_state_t *s = (app_state_t *)ctx;
    if (!s) return;

    if (state == MQTT_CLIENT_CONNECTED) {
        rgb_led_set_state(LED_STATE_MQTT_CONNECTING);
    } else if (state == MQTT_CLIENT_FAILED) {
        rgb_led_set_state(LED_STATE_MQTT_FAILED);
    }
}

void on_mqtt_transport_cb(uint32_t generation, void *ctx)
{
    (void)ctx;
    hello_handshake_on_transport_connected(generation);
}

void on_mqtt_ready_cb(uint32_t generation, void *ctx)
{
    (void)ctx;
    hello_handshake_on_ready(generation);

    /* v2.6: 每上线一次就补报一条诊断记录。
     *
     * 放在这里而不是 main.c 的启动序列：此时 MQTT 已就绪，publish 才真正
     * 有出口。有未确认崩溃则补报那条（服务端回 ACK 后才释放 NVS 占用），
     * 无则报一条 BOOT（含 esp_reset_reason），让"为什么重启"每次都有答案。
     *
     * 重复调用是安全的：未确认的崩溃记录会重复上报，服务端按
     * (device_id, record_id) 幂等去重。 */
    crash_diag_report_pending();
}

/* ==== MQTT message callback ==== */

void on_mqtt_msg_cb(const char *topic, const uint8_t *data, size_t len, void *ctx)
{
    (void)topic;
    app_state_t *s = (app_state_t *)ctx;
    if (!s) return;

    /* 委托给**唯一入口**（P4）：MQTT 这条路径自己不再实现"判断+分发+应用"。 */
    (void)ehome_handle_downlink(data, len, NULL);
}
