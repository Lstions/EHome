/**
 * @file sync_manager.c
 * @brief Sync Manager v2.1 Implementation
 *
 * 7-reason decision model + periodic sync task.
 * Integrates with config_mgr for NVS epoch/manifest persistence.
 */

#include "sync_manager.h"

/* ⚠ 2026-10-08：本文件用了 strncpy（:100/:240）却**没有**包含 <string.h>。
 * 它之所以能在 IDF 下编过，只是因为别处**恰好**间接带进了这个声明 ——
 * 而"恰好"不是保证：把它编进宿主目标时立刻报
 *   "implicit declaration of function 'strncpy' [-Werror=implicit-function-declaration]"
 * ⇒ 补上。这类"靠传递包含活着"的用法会在改动包含图时静默炸掉。 */
#include <string.h>

#include "config_mgr.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "rgb_led.h"
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

#define CONFIG_RECEIVE_TIMEOUT_SEC  120  /* 2 minutes to receive ConfigManifest after HelloAck */

/* Config receive timeout timer */
static esp_timer_handle_t s_config_timeout_timer = NULL;

/* === Internal state === */
static sync_state_t s_state = {0};
static sync_state_enum_t s_sync_enum = SYNC_STATE_IDLE;
static bool s_initialized = false;
static sync_send_hello_cb_t s_send_hello_cb = NULL;
/* 见 sync_manager.h 的说明：由 main 侧接到上行仲裁（P4：一处定义）。 */
static sync_uplink_available_cb_t s_uplink_available_cb = NULL;

/* === Forward declarations === */
static bool should_request_sync(sync_reason_t reason);
static uint32_t get_time_sec(void);
static void update_has_active_config(void);

/* === Public API === */

static void config_timeout_callback(void *arg)
{
    (void)arg;
    if (!config_mgr_has_manifest()) {
        ESP_LOGW(TAG, "ConfigManifest not received within %ds — server offline", CONFIG_RECEIVE_TIMEOUT_SEC);
        rgb_led_set_state(LED_STATE_SERVER_OFFLINE);
        /* Trigger sync retry */
        if (s_initialized) {
            sync_manager_request_sync(SYNC_REASON_DOUBT);
        }
    } else {
        /* Config arrived during timeout window — ensure LED is correct */
        rgb_led_set_state(LED_STATE_RUNNING);
    }
}

void sync_manager_start_config_timeout(void)
{
    if (!s_config_timeout_timer) {
        esp_timer_create_args_t args = {
            .callback = config_timeout_callback,
            .name = "cfg_timeout"
        };
        esp_timer_create(&args, &s_config_timeout_timer);
    }
    /* Restart the timer (cancel if already running) */
    esp_timer_stop(s_config_timeout_timer);
    esp_timer_start_once(s_config_timeout_timer, CONFIG_RECEIVE_TIMEOUT_SEC * 1000000ULL);
    ESP_LOGI(TAG, "Config receive timeout started: %ds", CONFIG_RECEIVE_TIMEOUT_SEC);
}

void sync_manager_cancel_config_timeout(void)
{
    if (s_config_timeout_timer) {
        esp_timer_stop(s_config_timeout_timer);
    }
}

void sync_manager_init(void)
{
    if (s_initialized) {
        return;
    }

    ESP_LOGI(TAG, "Initializing sync manager v2.1...");

    /* Load state from config_mgr (NVS) */
    s_state.epoch = config_mgr_get_epoch();

    const char *mid = config_mgr_get_last_known_manifest_id();
    if (mid && mid[0] != '\0') {
        strncpy(s_state.manifest_id, mid, sizeof(s_state.manifest_id) - 1);
        s_state.manifest_id[sizeof(s_state.manifest_id) - 1] = '\0';
    }

    update_has_active_config();

    s_state.last_sync_time_sec = 0;
    s_state.last_sync_id_hash = 0;
    s_sync_enum = SYNC_STATE_IDLE;

    s_initialized = true;

    ESP_LOGI(TAG, "Sync manager ready: epoch=%llu, manifest=%s, nvs_has=%d",
             (unsigned long long)s_state.epoch,
             s_state.manifest_id[0] ? s_state.manifest_id : "(none)",
             s_state.has_active_config);

    /* If NVS is empty, immediately request sync */
    if (!s_state.has_active_config) {
        ESP_LOGI(TAG, "No active config, requesting sync");
        sync_manager_request_sync(SYNC_REASON_NO_CONFIG);
    }
}

void sync_manager_register_send_hello_cb(sync_send_hello_cb_t cb)
{
    s_send_hello_cb = cb;
}

void sync_manager_register_uplink_available_cb(sync_uplink_available_cb_t cb)
{
    s_uplink_available_cb = cb;
}

/* "现在有没有可用上行"的**唯一**判定（P4）。
 *
 * ⭐ 2026-10-08（§194）：MQTT 已彻底移除 ⇒ 这条函数的语义就是"3.0 传输是否已连接"，
 *    由注入回调（main.c 的 on_sync_uplink_available → transport_any_connected）提供。
 *
 * ⚠ 历史教训（留着别再犯）：本模块曾**硬编码**判 mqtt_client_is_connected_impl()，
 *    于是在"某条上行不可用但另一条可用"时，三条**主动请求同步**的路径会**永久死掉
 *    且不报错**（只留一条 WARN）。真机实测 §164：t=334 与 t=31493 两次请求都被挡住。
 *    ⇒ 判据要问"**这件事需要什么**"，不要问"某个特定实现是否在线"。 */
static bool uplink_available(void)
{
    if (s_uplink_available_cb != NULL) return s_uplink_available_cb();
    /* 未注入 ⇒ **保守返回 false**（不知道就说没有）：宁可少发一次同步请求，
     * 也不要在没有上行时假装有。⚠ 这与"逐位一致"的旧兜底行为**不同**，
     * 是有意为之 —— 旧兜底依赖的 mqtt_client_* 已经不存在。 */
    return false;
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

    /* Check **uplink** availability（不是 MQTT 可用性 —— 见 uplink_available() 的说明）。
     * ⚠ 日志措辞也要跟着改：原文写死 "MQTT not connected"，在 3.0 已就绪时会**误导排障**
     *   （操作员会去查 MQTT，而真正的原因是上行仲裁层说两条都不通）。 */
    if (!uplink_available()) {
        ESP_LOGW(TAG, "无可用上行（MQTT 与 3.0 均未就绪），暂缓同步请求");
        s_sync_enum = SYNC_STATE_ERROR;
        return;
    }

    /* Update state */
    s_sync_enum = SYNC_STATE_SYNCING;
    s_state.last_sync_time_sec = get_time_sec();

    /* Send Hello with v2.1 fields (includes epoch/nvs_has/manifest_id) */
    ESP_LOGI(TAG, "Triggering sync via Hello (reason=%d, epoch=%llu, has_config=%d)",
             reason, (unsigned long long)s_state.epoch, s_state.has_active_config);

    /* Invoke callback to send Hello */
    if (s_send_hello_cb && uplink_available()) {
        s_send_hello_cb();
    } else {
        ESP_LOGW(TAG, "Cannot send Hello: cb=%p, uplink_available=%d",
                 s_send_hello_cb, (int)uplink_available());
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

esp_err_t sync_manager_on_config_applied(uint64_t server_epoch, const char *manifest_id)
{
    if (!s_initialized || !manifest_id || !manifest_id[0]) return ESP_ERR_INVALID_STATE;

    esp_err_t persist_err = config_mgr_persist_sync_metadata(server_epoch, manifest_id);
    if (persist_err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to persist sync metadata: %s", esp_err_to_name(persist_err));
        return persist_err;
    }

    ESP_LOGI(TAG, "Config applied: server_epoch=%llu, manifest_id=%s",
             (unsigned long long)server_epoch, manifest_id ? manifest_id : "(null)");

    /* Update local epoch */
    if (server_epoch > 0) {
        s_state.epoch = server_epoch;

    }

    /* Update manifest_id */
    if (manifest_id && manifest_id[0] != '\0') {
        strncpy(s_state.manifest_id, manifest_id, sizeof(s_state.manifest_id) - 1);
        s_state.manifest_id[sizeof(s_state.manifest_id) - 1] = '\0';

    }

    /* Update NVS has config flag */
    update_has_active_config();

    /* Update sync time */
    s_state.last_sync_time_sec = get_time_sec();
    s_sync_enum = SYNC_STATE_IDLE;

    ESP_LOGI(TAG, "Sync state updated: epoch=%llu, manifest=%s, nvs_has=%d",
             (unsigned long long)s_state.epoch,
             s_state.manifest_id[0] ? s_state.manifest_id : "(none)",
             s_state.has_active_config);
	return ESP_OK;
}

void sync_manager_periodic_task(void *pvParameters)
{
    (void)pvParameters;

    ESP_LOGI(TAG, "Periodic sync task started (interval=%d sec, dedup=%d sec)",
             CONFIG_COLLECTOR_SYNC_PERIODIC_SEC, CONFIG_COLLECTOR_SYNC_DEDUP_SEC);

    while (1) {
        if (!s_initialized) {
            vTaskDelay(pdMS_TO_TICKS(60 * 1000));
            continue;
        }

        /* Refresh active config state */
        update_has_active_config();

        /* Priority 1: No config — aggressive 30s retry */
        if (!s_state.has_active_config) {
            vTaskDelay(pdMS_TO_TICKS(30 * 1000));
            uint32_t now = get_time_sec();
            if (now - s_state.last_sync_time_sec > 25) {  /* >25s since last attempt */
                ESP_LOGI(TAG, "No active config, requesting sync (30s interval)");
                sync_manager_request_sync(SYNC_REASON_NO_CONFIG);
            }
            continue;
        }

        /* Priority 2: Has config — normal 60s periodic sync */
        vTaskDelay(pdMS_TO_TICKS(60 * 1000));
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
    case SYNC_REASON_NO_CONFIG:
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

static void update_has_active_config(void)
{
    s_state.has_active_config = config_mgr_has_manifest();
}
