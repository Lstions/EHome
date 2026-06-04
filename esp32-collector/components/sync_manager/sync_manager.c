/**
 * @file sync_manager.c
 * @brief Sync Manager v2.1 Implementation
 *
 * 7-reason decision model + periodic sync task.
 * Integrates with config_mgr for NVS epoch/manifest persistence.
 */

#include "sync_manager.h"
#include "config_mgr.h"
#include "ehome_mqtt.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#define TAG "SYNC"

/* === Config defaults (overridable via Kconfig) === */
#ifndef CONFIG_COLLECTOR_SYNC_PERIODIC_SEC
#define CONFIG_COLLECTOR_SYNC_PERIODIC_SEC 600
#endif

#ifndef CONFIG_COLLECTOR_SYNC_DEDUP_SEC
#define CONFIG_COLLECTOR_SYNC_DEDUP_SEC 30
#endif

/* === Internal state === */
static sync_state_t s_state = {0};
static sync_state_enum_t s_sync_enum = SYNC_STATE_IDLE;
static bool s_initialized = false;
static sync_send_hello_cb_t s_send_hello_cb = NULL;

/* === Forward declarations === */
static bool should_request_sync(sync_reason_t reason);
static uint32_t get_time_sec(void);
static void update_nvs_has_config(void);

/* === Public API === */

void sync_manager_init(void)
{
    if (s_initialized) {
        return;
    }

    ESP_LOGI(TAG, "Initializing sync manager v2.1...");

    /* Load state from config_mgr (NVS) */
    s_state.epoch = config_mgr_get_epoch();

    const char *mid = config_mgr_get_manifest_id();
    if (mid && mid[0] != '\0') {
        strncpy(s_state.manifest_id, mid, sizeof(s_state.manifest_id) - 1);
        s_state.manifest_id[sizeof(s_state.manifest_id) - 1] = '\0';
    }

    update_nvs_has_config();

    s_state.last_sync_time_sec = 0;
    s_state.last_sync_id_hash = 0;
    s_sync_enum = SYNC_STATE_IDLE;

    s_initialized = true;

    ESP_LOGI(TAG, "Sync manager ready: epoch=%llu, manifest=%s, nvs_has=%d",
             (unsigned long long)s_state.epoch,
             s_state.manifest_id[0] ? s_state.manifest_id : "(none)",
             s_state.nvs_has_config);

    /* If NVS is empty, immediately request sync */
    if (!s_state.nvs_has_config) {
        ESP_LOGI(TAG, "NVS empty, requesting immediate sync");
        sync_manager_request_sync(SYNC_REASON_NVS_EMPTY);
    }
}

void sync_manager_register_send_hello_cb(sync_send_hello_cb_t cb)
{
    s_send_hello_cb = cb;
}

void sync_manager_request_sync(sync_reason_t reason)
{
    if (!s_initialized) {
        ESP_LOGW(TAG, "Sync manager not initialized, ignoring request");
        return;
    }

    ESP_LOGI(TAG, "Sync request: reason=%d", reason);

    if (!should_request_sync(reason)) {
        ESP_LOGD(TAG, "Sync suppressed by dedup policy (reason=%d)", reason);
        return;
    }

    /* Check MQTT connectivity */
    if (!mqtt_client_is_connected_impl()) {
        ESP_LOGW(TAG, "MQTT not connected, deferring sync request");
        s_sync_enum = SYNC_STATE_ERROR;
        return;
    }

    /* Update state */
    s_sync_enum = SYNC_STATE_SYNCING;
    s_state.last_sync_time_sec = get_time_sec();

    /* Send Hello with v2.1 fields (includes epoch/nvs_has/manifest_id) */
    ESP_LOGI(TAG, "Triggering sync via Hello (reason=%d, epoch=%llu, nvs_has=%d)",
             reason, (unsigned long long)s_state.epoch, s_state.nvs_has_config);

    /* Invoke callback to send Hello */
    if (s_send_hello_cb && mqtt_client_is_connected_impl()) {
        s_send_hello_cb();
    } else {
        ESP_LOGW(TAG, "Cannot send Hello: cb=%p, mqtt_connected=%d",
                 s_send_hello_cb, mqtt_client_is_connected_impl());
        s_sync_enum = SYNC_STATE_ERROR;
    }
}

void sync_manager_on_downlink_received(uint8_t msg_type)
{
    if (!s_initialized) return;

    switch (msg_type) {
    case 0x04:  /* ConfigManifest */
        /* Config applied - state will be updated via sync_manager_on_config_applied */
        break;

    case 0x12:  /* HelloAck */
        /* Server acknowledged - check if we need sync based on response */
        s_sync_enum = SYNC_STATE_IDLE;
        break;

    case 0x05:  /* ConfigResult ACK from server */
        s_sync_enum = SYNC_STATE_IDLE;
        break;

    default:
        break;
    }
}

sync_state_t *sync_get_state(void)
{
    return &s_state;
}

sync_state_enum_t sync_manager_get_state_enum(void)
{
    return s_sync_enum;
}

void sync_manager_on_config_applied(uint64_t server_epoch, const char *manifest_id)
{
    if (!s_initialized) return;

    ESP_LOGI(TAG, "Config applied: server_epoch=%llu, manifest_id=%s",
             (unsigned long long)server_epoch, manifest_id ? manifest_id : "(null)");

    /* Update local epoch */
    if (server_epoch > 0) {
        s_state.epoch = server_epoch;
        config_mgr_set_epoch(server_epoch);
    }

    /* Update manifest_id */
    if (manifest_id && manifest_id[0] != '\0') {
        strncpy(s_state.manifest_id, manifest_id, sizeof(s_state.manifest_id) - 1);
        s_state.manifest_id[sizeof(s_state.manifest_id) - 1] = '\0';
        config_mgr_set_manifest_id(manifest_id);
    }

    /* Update NVS has config flag */
    update_nvs_has_config();

    /* Update sync time */
    s_state.last_sync_time_sec = get_time_sec();
    s_sync_enum = SYNC_STATE_IDLE;

    ESP_LOGI(TAG, "Sync state updated: epoch=%llu, manifest=%s, nvs_has=%d",
             (unsigned long long)s_state.epoch,
             s_state.manifest_id[0] ? s_state.manifest_id : "(none)",
             s_state.nvs_has_config);
}

void sync_manager_periodic_task(void *pvParameters)
{
    (void)pvParameters;

    ESP_LOGI(TAG, "Periodic sync task started (interval=%d sec, dedup=%d sec)",
             CONFIG_COLLECTOR_SYNC_PERIODIC_SEC, CONFIG_COLLECTOR_SYNC_DEDUP_SEC);

    while (1) {
        /* Check every 60 seconds */
        vTaskDelay(pdMS_TO_TICKS(60 * 1000));

        if (!s_initialized) continue;

        /* Refresh NVS has config state */
        update_nvs_has_config();

        /* Priority 1: NVS empty - immediate sync */
        if (!s_state.nvs_has_config) {
            ESP_LOGI(TAG, "Periodic check: NVS empty, requesting sync");
            sync_manager_request_sync(SYNC_REASON_NVS_EMPTY);
            continue;
        }

        /* Priority 2: Periodic sync */
        uint32_t now = get_time_sec();
        if (now - s_state.last_sync_time_sec > CONFIG_COLLECTOR_SYNC_PERIODIC_SEC) {
            ESP_LOGI(TAG, "Periodic check: %lu sec since last sync, requesting",
                     (unsigned long)(now - s_state.last_sync_time_sec));
            sync_manager_request_sync(SYNC_REASON_PERIODIC);
        }
    }
}

/* === Internal === */

static bool should_request_sync(sync_reason_t reason)
{
    sync_state_t *st = sync_get_state();
    uint32_t now = get_time_sec();

    switch (reason) {
    case SYNC_REASON_NVS_EMPTY:
    case SYNC_REASON_FORCED:
    case SYNC_REASON_USER_ACTION:
    case SYNC_REASON_EPOCH_LAG:
    case SYNC_REASON_MANIFEST_MISMATCH:
        /* Always allow - these are critical/forced reasons */
        return true;

    case SYNC_REASON_PERIODIC:
        /* Only if enough time has passed since last sync */
        return (now - st->last_sync_time_sec) > CONFIG_COLLECTOR_SYNC_PERIODIC_SEC;

    case SYNC_REASON_DOUBT:
        /* Short dedup window for doubt (30s default) */
        return (now - st->last_sync_time_sec) > CONFIG_COLLECTOR_SYNC_DEDUP_SEC;

    default:
        return false;
    }
}

static uint32_t get_time_sec(void)
{
    return (uint32_t)(esp_timer_get_time() / 1000000LL);
}

static void update_nvs_has_config(void)
{
    s_state.nvs_has_config = config_mgr_has_manifest();
}
