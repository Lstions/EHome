/**
 * @file main.c
 * @brief EHomeSystem ESP32-C6 v2.4 — Unified Bus DMA + Resource Reporting
 *
 * app_main() is the sole entry point.  All business logic lives in:
 *   app_state / app_callbacks / bus_manager / hello_handshake / bus_worker
 */

#include "app_state.h"
#include "app_callbacks.h"
#include "bus_manager.h"
#include "hello_handshake.h"
#include "bus_worker.h"
#include "msg_handler.h"
#include "config_mgr.h"
#include "scheduler.h"
#include "sync_manager.h"
#include "ota.h"
#include "rgb_led.h"
#include "factory_reset.h"
#include "wifi_mgr.h"
#include "ehome_mqtt.h"
#include "transport.h"
#ifdef CONFIG_DEBUG_TCP_ENABLED
#include "ehome_tcp.h"
#endif
#include "nvs_flash.h"
#include "esp_log.h"

#define TAG "EHOME"

/* ---- status_task — still in main.c (single-loop, minimal dependency) ---- */

static void status_task(void *pv)
{
    app_state_t *s = (app_state_t *)pv;
    while (1) {
        s->uptime_sec++;
        if (mqtt_client_is_connected_impl())
            msg_handler_send_status(s->uptime_sec, "online",
                                    config_mgr_get_active_channel_count());
        vTaskDelay(pdMS_TO_TICKS(5000));
    }
}

/* ---- sync send_hello callback ---- */

static void on_sync_send_hello(void)
{
    app_state_t *s = app_state_get();
    msg_handler_send_hello(s->node_id, get_firmware_version(), get_model_name(),
                           config_mgr_get_active_channel_count());
}

/* ---- Weak-symbol bridges for msg_handler callbacks ---- */

void on_write_cmd_received(uint32_t rid, uint32_t ch,
                           const uint8_t *d, size_t l, uint32_t rs)
{
    bus_manager_on_write_cmd(app_state_get(), rid, ch, d, l, rs);
}

void on_query_resources_received(const char *request_id)
{
    (void)request_id;
}

void factory_reset(void) { /* implemented in factory_reset component */ }

/* ================================================================== */
/*  app_main                                                          */
/* ================================================================== */

void app_main(void)
{
    ESP_LOGI(TAG, "EHomeSystem v%s unified bus DMA", get_firmware_version());

    /* ---- NVS ---- */
    esp_err_t r = nvs_flash_init();
    if (r == ESP_ERR_NVS_NO_FREE_PAGES || r == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        ESP_ERROR_CHECK(nvs_flash_erase());
        r = nvs_flash_init();
    }
    ESP_ERROR_CHECK(r);

    /* ---- OTA ---- */
    if (ota_get_nvs_state() != 0) {
        extern void ota_confirm_valid(void);
        ota_confirm_valid();
    }

    /* ---- App state (singleton — node_id from MAC, cmd_queue, spinlock) ---- */
    app_state_t *s = app_state_init();

    /* ---- Subsystem init ---- */
    config_mgr_init();
    sync_manager_init();
    sync_manager_register_send_hello_cb(on_sync_send_hello);
    msg_handler_init();
    ota_init();
    scheduler_init();

    /* ---- Transports ---- */
    mqtt_client_init();
    transport_manager_init();
    mqtt_transport_register();

    /* ---- WiFi + callbacks ---- */
    wifi_mgr_register_state_cb(on_wifi_state_cb, s);
    mqtt_client_register_state_cb(on_mqtt_state_cb, s);
    mqtt_client_register_msg_cb(on_mqtt_msg_cb, s);
    mqtt_client_set_node_id(s->node_id);

    rgb_led_init(8);
    rgb_led_start();
    factory_reset_init();

    wifi_mgr_init();
    wifi_mgr_start();

    /* ---- Background tasks ---- */
    xTaskCreate(status_task, "status", 2048, (void *)s, 3, NULL);
    xTaskCreate(sync_manager_periodic_task, "sync", 3072, NULL, 2, NULL);
    bus_worker_start(s);

#ifdef CONFIG_DEBUG_TCP_ENABLED
    /* TCP transport (parallel with MQTT, debug only) */
    ESP_LOGI(TAG, "Starting TCP transport on port %d", CONFIG_DEBUG_TCP_PORT);
    tcp_transport_config_t tcp_cfg = {
        .port        = CONFIG_DEBUG_TCP_PORT,
        .max_clients = 4,
    };
    s->tcp_transport = tcp_transport_create(&tcp_cfg);
    if (s->tcp_transport) {
        s->tcp_transport->msg_cb = on_transport_msg_cb;
        s->tcp_transport->msg_cb_ctx = s;
        s->tcp_transport->state_cb = on_transport_state_cb;
        s->tcp_transport->state_cb_ctx = NULL;
        s->tcp_transport->ops->init(s->tcp_transport, NULL);
        transport_register(s->tcp_transport);

        if (wifi_mgr_get_state() == WIFI_MGR_CONNECTED
            && s->tcp_transport->state != TRANSPORT_CONNECTED
            && s->tcp_transport->ops->start) {
            s->tcp_transport->ops->start(s->tcp_transport);
        }
    }
#endif

    ESP_LOGI(TAG, "Init done, node=%s", s->node_id);
}
