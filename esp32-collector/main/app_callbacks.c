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
#include "config_apply_transaction.h"
#include "periph_config_apply.h"
#include "bus_manager.h"
#include "hello_handshake.h"
#include "msg_handler.h"
#include "msg_handler_internal.h"
#include "scheduler.h"
#include "bus_worker.h"
#include "config_mgr.h"
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
#include "esp_system.h"
#include "frame_codec.h"
#include <string.h>
#include <stdlib.h>

#define TAG "CALLBACK"

/* ==== Config Manifest helper ==== */

static bool is_config_manifest(const uint8_t *data, size_t len)
{
    return len > 0 && data[0] == MSG_CONFIG_MFST;
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

static esp_err_t tx_apply_log_stream(void *opaque, const config_manifest_t *manifest)
{
    (void)opaque;
    if (!manifest->log_stream_enabled) return log_stream_stop();
    return log_stream_is_active()
        ? log_stream_set_level(manifest->log_stream_level)
        : log_stream_start(manifest->log_stream_level);
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
    if (!tx->old_log_active) return log_stream_stop();
    return log_stream_is_active()
        ? log_stream_set_level(tx->old_log_level)
        : log_stream_start(tx->old_log_level);
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
    esp_err_t log_err = log_stream_stop();
    return scheduler_err == ESP_OK && bus_err == ESP_OK && dma_err == ESP_OK &&
           periph_err == ESP_OK && log_err == ESP_OK ? ESP_OK : ESP_FAIL;
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
    /* Large rollback state lives in bounded heap, never on the MQTT callback stack. */
    config_manifest_t *old_snapshot = calloc(1, sizeof(*old_snapshot));
    manifest_tx_ctx_t *tx = calloc(1, sizeof(*tx));
    if (!old_snapshot || !tx) {
        free(old_snapshot);
        free(tx);
        msg_handler_send_config_result(incoming_manifest_id, incoming_sync_id, false);
        return;
    }
    bool had_old = config_mgr_snapshot_active(old_snapshot);

    /* Suspend rx_task/cmd_task before cleanup to prevent race.  A worker
     * waiting on a queue must acknowledge within a bounded deadline; never
     * proceed to delete driver/event queues after an incomplete suspend. */
    if (!bus_worker_suspend()) {
        ESP_LOGE(TAG, "Rejecting ConfigManifest: worker suspend timeout");
        msg_handler_send_config_result(incoming_manifest_id, incoming_sync_id, false);
        free(old_snapshot);
        free(tx);
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
        free(old_snapshot);
        free(tx);
        return;
    }
    const config_manifest_t *staged_cfg = config_mgr_get_staged_manifest();
    if (!staged_cfg) {
        ESP_LOGE(TAG, "Rejecting ConfigManifest because staging disappeared");
        config_mgr_discard_staged_manifest();
        msg_handler_send_config_result(incoming_manifest_id, incoming_sync_id, false);
        app_state_unlock_config();
        bus_worker_resume();
        free(old_snapshot);
        free(tx);
        return;
    }

    char attempted_id[sizeof(staged_cfg->manifest_id)];
	char attempted_sync_id[sizeof(staged_cfg->sync_id)];
    memcpy(attempted_id, staged_cfg->manifest_id, sizeof(attempted_id));
    attempted_id[sizeof(attempted_id) - 1] = '\0';
	memcpy(attempted_sync_id, staged_cfg->sync_id, sizeof(attempted_sync_id));
	attempted_sync_id[sizeof(attempted_sync_id) - 1] = '\0';
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
        &s_manifest_tx_ops, tx, had_old ? old_snapshot : NULL, staged_cfg);
    if (tx_result != CONFIG_APPLY_OK) {
        ESP_LOGE(TAG, "Rejecting ConfigManifest transaction: result=%d", (int)tx_result);
        config_mgr_discard_staged_manifest();
        msg_handler_send_config_result(attempted_id, attempted_sync_id, false);
        if (config_apply_result_requires_restart(tx_result)) {
            /* Runtime is neither restored nor safely stopped. Keep bus workers
             * suspended and retain the config lock until the restart executes. */
            ESP_LOGE(TAG, "Unrecoverable config transaction; restarting fail-hard");
            free(old_snapshot);
            free(tx);
            esp_restart();
            return;
        }
        app_state_unlock_config();
        bus_worker_resume();
        free(old_snapshot);
        free(tx);
        return;
    }
    free(old_snapshot);
    free(tx);

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
    if (xTaskCreate(mqtt_supervisor_task, "mqtt_super", 8192, NULL, 5,
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
        break;

#ifdef CONFIG_DEBUG_TCP_ENABLED
        if (s->tcp_transport && s->tcp_transport->state != TRANSPORT_CONNECTED
            && s->tcp_transport->ops->start) {
            ESP_LOGI(TAG, "Starting TCP transport");
            s->tcp_transport->ops->start(s->tcp_transport);
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

void on_transport_msg_cb(const uint8_t *data, size_t len, void *ctx)
{
    app_state_t *s = (app_state_t *)ctx;
    if (!s || !data || len == 0) return;

    ESP_LOGI(TAG, "Transport msg: %d bytes", (int)len);

    bool is_cfg = is_config_manifest(data, len);
    ESP_LOGI(TAG, "is_config_manifest: %d, msg_type: 0x%02X", is_cfg, data[0]);

    if (is_cfg) s->config_received = true;

    msg_handler_process_with_transport(data, len, s->tcp_transport);

    if (is_cfg) {
        ESP_LOGI(TAG, "Calling handle_config_applied...");
        handle_config_applied(s, data, len);
    }
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
}

/* ==== MQTT message callback ==== */

void on_mqtt_msg_cb(const char *topic, const uint8_t *data, size_t len, void *ctx)
{
    (void)topic;
    app_state_t *s = (app_state_t *)ctx;
    if (!s) return;

    bool is_cfg = is_config_manifest(data, len);
    if (is_cfg) s->config_received = true;

    msg_handler_process(data, len);

    if (is_cfg) handle_config_applied(s, data, len);
}
