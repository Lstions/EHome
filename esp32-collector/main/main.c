/**
 * @file main.c
 * @brief EHomeSystem ESP32 Collector v2.0
 */

#include <stdio.h>
#include <string.h>
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "esp_system.h"
#include "esp_log.h"
#include "esp_ota_ops.h"
#include "nvs_flash.h"
#include "nvs.h"
#include "esp_mac.h"

#include "frame_codec.h"
#include "wifi_mgr.h"
#include "ehome_mqtt.h"
#include "msg_handler.h"
#include "config_mgr.h"
#include "scheduler.h"
#include "ota.h"
#include "rgb_led.h"
#include "factory_reset.h"
#include "sync_manager.h"

#define TAG "EHOME"
#define FIRMWARE_VERSION "2.2.8"

/* Device ID from MAC address */
static char s_node_id[32] = {0};
static uint32_t s_uptime_sec = 0;
static bool s_config_received = false;
static bool s_ota_need_confirm = false;

/* Firmware version accessor for sync_manager */
const char *get_firmware_version(void)
{
    return FIRMWARE_VERSION;
}

/* Forward declarations */
static void on_wifi_state(wifi_mgr_state_t state, void *ctx);
static void on_mqtt_state(mqtt_client_state_t state, void *ctx);
static void on_mqtt_msg(const char *topic, const uint8_t *data, size_t len, void *ctx);
static void generate_node_id(void);
static void status_task(void *pvParameters);
static void on_sync_send_hello(void);

/* === Main === */
void app_main(void)
{
    ESP_LOGI(TAG, "EHomeSystem Collector v%s starting...", FIRMWARE_VERSION);

    /* Init NVS */
    esp_err_t ret = nvs_flash_init();
    if (ret == ESP_ERR_NVS_NO_FREE_PAGES || ret == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        ESP_ERROR_CHECK(nvs_flash_erase());
        ret = nvs_flash_init();
    }
    ESP_ERROR_CHECK(ret);

    /* === OTA power-loss recovery check ===
     * If the device rebooted with ota_state != none, the previous OTA was
     * interrupted.  Since rollback is enabled, the bootloader boots the new
     * partition in "pending validation" state.  If the new app itself detects
     * stale NVS state from a *different* OTA attempt, it means the new app
     * booted but never confirmed — we should still confirm it (the fact it
     * booted and got this far is proof it works).  However, if we just
     * flashed and the state is still downloading/verifying, the old app
     * would have been running — that case is caught by the bootloader's
     * own rollback timeout.
     *
     * Strategy: read NVS state; if non-zero, log it, then clear it.
     * We will confirm validity once WiFi connects.
     */
    uint8_t ota_boot_state = ota_get_nvs_state();
    if (ota_boot_state != 0) {
        ESP_LOGW(TAG, "OTA state non-zero at boot (%d) — previous OTA interrupted, will validate after WiFi connect",
                 ota_boot_state);
        s_ota_need_confirm = true;
    }

    /* Generate device ID */
    generate_node_id();

    /* Init components */
    config_mgr_init();
    sync_manager_init();  /* v2.1: sync manager */
    sync_manager_register_send_hello_cb(on_sync_send_hello);  /* v2.1: register callback */
    msg_handler_init();
    ota_init();
    scheduler_init();
    mqtt_client_init();  /* Must call before start */

    /* Register callbacks */
    wifi_mgr_register_state_cb(on_wifi_state, NULL);
    mqtt_client_register_state_cb(on_mqtt_state, NULL);
    mqtt_client_register_msg_cb(on_mqtt_msg, NULL);
    mqtt_client_set_node_id(s_node_id);

    /* Initialize and start RGB LED */
    rgb_led_init(8);  /* GPIO8 = ESP32-C6 built-in RGB LED */
    rgb_led_start();

    /* Factory reset button monitor */
    factory_reset_init();

    /* Start WiFi */
    wifi_mgr_init();
    wifi_mgr_start();

    /* Status report task */
    xTaskCreate(status_task, "status", 2048, NULL, 3, NULL);

    /* v2.1: Periodic sync task */
    xTaskCreate(sync_manager_periodic_task, "sync", 3072, NULL, 2, NULL);

    ESP_LOGI(TAG, "Collector initialized, node_id=%s", s_node_id);
}

/* === Callbacks === */
static void on_wifi_state(wifi_mgr_state_t state, void *ctx)
{
    (void)ctx;
    ESP_LOGI(TAG, "WiFi state: %d", state);
    if (state == WIFI_MGR_CONNECTED) {
        rgb_led_set_state(LED_STATE_MQTT_CONNECTING);

        /* Confirm OTA: WiFi connected → new firmware is functional.
         * Only call if we detected a pending OTA at boot (ota_boot_state != 0).
         * Avoids unnecessary NVS writes on every WiFi reconnection. */
        if (s_ota_need_confirm) {
            ota_confirm_valid();
            s_ota_need_confirm = false;
        }

        mqtt_client_start();
    } else if (state == WIFI_MGR_CONNECTING) {
        rgb_led_set_state(LED_STATE_WIFI_CONNECTING);
    } else if (state == WIFI_MGR_FAILED) {
        rgb_led_set_state(LED_STATE_WIFI_FAILED);
    }
}

static void on_mqtt_state(mqtt_client_state_t state, void *ctx)
{
    (void)ctx;
    ESP_LOGI(TAG, "MQTT state: %d", state);
    if (state == MQTT_CLIENT_CONNECTED) {
        rgb_led_set_state(LED_STATE_RUNNING);
        
        /* Hello retry mechanism: wait for HelloAck confirmation */
        #define HELLO_MAX_RETRIES  3
        #define HELLO_TIMEOUT_MS   10000
        #define HELLO_RETRY_INTERVAL_MS 5000
        
        msg_handler_reset_hello_ack();
        bool hello_confirmed = false;
        
        for (int retry = 0; retry < HELLO_MAX_RETRIES && !hello_confirmed; retry++) {
            ESP_LOGI(TAG, "Sending Hello (attempt %d/%d)...", retry + 1, HELLO_MAX_RETRIES);
            msg_handler_send_hello(s_node_id, FIRMWARE_VERSION, CONFIG_IDF_TARGET,
                                   config_mgr_get_active_channel_count());
            
            /* Wait for HelloAck */
            uint32_t waited = 0;
            while (waited < HELLO_TIMEOUT_MS) {
                if (msg_handler_is_hello_ack_received()) {
                    hello_confirmed = true;
                    ESP_LOGI(TAG, "HelloAck received! server_time=%llu",
                             (unsigned long long)msg_handler_get_server_time());
                    break;
                }
                vTaskDelay(pdMS_TO_TICKS(500));
                waited += 500;
            }
            
            if (!hello_confirmed) {
                ESP_LOGW(TAG, "HelloAck timeout, retrying...");
                rgb_led_set_state(LED_STATE_MQTT_CONNECTING);
                vTaskDelay(pdMS_TO_TICKS(HELLO_RETRY_INTERVAL_MS));
            }
        }
        
        if (!hello_confirmed) {
            ESP_LOGE(TAG, "HelloAck not received after %d retries, server offline", HELLO_MAX_RETRIES);
            rgb_led_set_state(LED_STATE_MQTT_FAILED);
        } else {
            rgb_led_set_state(LED_STATE_RUNNING);
        }
        /* Scheduler will be started after ConfigManifest received */
    } else if (state == MQTT_CLIENT_FAILED) {
        rgb_led_set_state(LED_STATE_MQTT_FAILED);
    }
}

static void on_mqtt_msg(const char *topic, const uint8_t *data, size_t len, void *ctx)
{
    (void)topic;
    (void)ctx;
    
    /* Check if this is a ConfigManifest */
    if (len > 0 && data[0] == 0x04) {
        s_config_received = true;
    }
    
    /* Process message first (applies config for ConfigManifest) */
    msg_handler_process(data, len);
    
    /* Start scheduler AFTER config is applied */
    if (s_config_received && !scheduler_is_running()) {
        scheduler_start();
        ESP_LOGI(TAG, "ConfigManifest applied, scheduler started");
    }
}

/* === Helpers === */
static void generate_node_id(void)
{
    const char *cfg_node_id = CONFIG_COLLECTOR_NODE_ID;
    if (cfg_node_id[0] != '\0') {
        strlcpy(s_node_id, cfg_node_id, sizeof(s_node_id));
    } else {
        uint8_t mac[6];
        esp_read_mac(mac, ESP_MAC_WIFI_STA);
        snprintf(s_node_id, sizeof(s_node_id), CONFIG_IDF_TARGET "_%02X%02X%02X%02X%02X%02X",
                 mac[0], mac[1], mac[2], mac[3], mac[4], mac[5]);
    }
}

static void status_task(void *pvParameters)
{
    (void)pvParameters;
    while (1) {
        s_uptime_sec++;
        if (mqtt_client_is_connected_impl()) {
            msg_handler_send_status(s_uptime_sec, "online",
                                    config_mgr_get_active_channel_count());
        }
        vTaskDelay(pdMS_TO_TICKS(5000));
    }
}

/* v2.1: Callback for sync_manager to trigger Hello send */
static void on_sync_send_hello(void)
{
    msg_handler_send_hello(s_node_id, FIRMWARE_VERSION, CONFIG_IDF_TARGET,
                           config_mgr_get_active_channel_count());
}
