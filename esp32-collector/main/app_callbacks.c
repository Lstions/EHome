/**
 * @file app_callbacks.c
 * @brief WiFi / MQTT / Transport state and message callbacks.
 *
 * All callbacks receive app_state_t *ctx for thread-safe access to
 * shared state.  Config-manifest application is protected by spinlock.
 */

#include "app_state.h"
#include "app_callbacks.h"
#include "bus_manager.h"
#include "hello_handshake.h"
#include "msg_handler.h"
#include "scheduler.h"
#include "bus_worker.h"
#include "config_mgr.h"
#include "rgb_led.h"
#include "wifi_mgr.h"
#include "ehome_mqtt.h"
#include "transport.h"
#include "esp_log.h"
#include "frame_codec.h"

#define TAG "CALLBACK"

/* ==== Config Manifest helper ==== */

static bool is_config_manifest(const uint8_t *data, size_t len)
{
    return len > 0 && data[0] == MSG_CONFIG_MFST;
}

static void handle_config_applied(app_state_t *s)
{
    /* Idempotency guard: skip if same manifest_id already applied.
     * Backend may re-push the same config due to timing (e.g. StatusReport
     * triggering a sync decision), but re-applying causes scheduler restart
     * which resets channel_count to 0, creating a feedback loop. */
    const config_manifest_t *cfg = config_mgr_get_manifest();
    const char *new_id = cfg ? cfg->manifest_id : NULL;
    const char *last_id = config_mgr_get_last_known_manifest_id();
    if (new_id && last_id && strcmp(new_id, last_id) == 0 && config_mgr_has_manifest()) {
        ESP_LOGI(TAG, "handle_config_applied: SKIP (same manifest %s, config active)", new_id);
        return;
    }

    ESP_LOGI(TAG, "handle_config_applied: START");
    
    /* Suspend rx_task/cmd_task before cleanup to prevent race:
     * rx_task polls bus_dma_read() in a tight loop; if it accesses
     * a UART driver that bus_manager_cleanup_all is about to delete,
     * uart_read_bytes() hits a NULL queue → assert crash. */
    bus_worker_suspend();
    
    /* 使用互斥锁而不是自旋锁，因为需要调用可能阻塞的函数 */
    app_state_lock_config();
    
    if (scheduler_is_running()) {
        ESP_LOGI(TAG, "Stopping scheduler...");
        scheduler_stop();  /* 内部有vTaskDelay，不能在自旋锁内 */
    }
    
    ESP_LOGI(TAG, "Cleaning up buses...");
    bus_manager_cleanup_all(s);
    
    ESP_LOGI(TAG, "Setting up buses from manifest...");
    bus_manager_setup_from_manifest(s);
    
    ESP_LOGI(TAG, "Starting scheduler...");
    scheduler_start(s->cmd_queue);  /* 内部有xTaskCreate，不能在自旋锁内 */
    
    ESP_LOGI(TAG, "Config→scheduler %d ch", scheduler_get_channel_count());
    
    app_state_unlock_config();
    
    /* Resume rx_task/cmd_task — buses are now re-initialized */
    bus_worker_resume();
    
    rgb_led_set_state(LED_STATE_RUNNING);  /* Config applied — device operational */
    ESP_LOGI(TAG, "handle_config_applied: DONE");
}

/* ==== WiFi callback ==== */

void on_wifi_state_cb(wifi_mgr_state_t state, void *ctx)
{
    app_state_t *s = (app_state_t *)ctx;
    if (!s) return;

    switch (state) {
    case WIFI_MGR_CONNECTED:
        rgb_led_set_state(LED_STATE_MQTT_CONNECTING);
        if (s->ota_need_confirm) {
            extern void ota_confirm_valid(void);
            ota_confirm_valid();
            s->ota_need_confirm = false;
        }
        mqtt_client_start();

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
        break;

    case WIFI_MGR_FAILED:
        rgb_led_set_state(LED_STATE_WIFI_FAILED);
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
        handle_config_applied(s);
    }
}

/* ==== Transport state callback ==== */

void on_transport_state_cb(transport_state_t state, void *ctx)
{
    (void)ctx;
    ESP_LOGI(TAG, "Transport state: %d", state);

    if (state == TRANSPORT_CONNECTED) {
        /* Don't set RUNNING — server config state determines LED, not transport */
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
        rgb_led_set_state(LED_STATE_MQTT_CONNECTING);  /* Not RUNNING yet — Hello not complete */
        if (!s->hello_task_running) {
            msg_handler_reset_hello_ack();
            hello_handshake_start(s);
        }
    } else if (state == MQTT_CLIENT_FAILED) {
        rgb_led_set_state(LED_STATE_MQTT_FAILED);
    }
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

    if (is_cfg) handle_config_applied(s);
}
