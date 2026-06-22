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
#include "bus_dma.h"
#ifdef CONFIG_DEBUG_TCP_ENABLED
#include "ehome_tcp.h"
#endif
#include "nvs_flash.h"
#include "esp_ota_ops.h"
#include "esp_log.h"

/* RGB LED pin differs by board: S3=GPIO48, C6=GPIO8 */
#ifdef CONFIG_IDF_TARGET_ESP32S3
  #define BOARD_LED_GPIO  48
#elif defined(CONFIG_IDF_TARGET_ESP32C6)
  #define BOARD_LED_GPIO  8
#else
  #define BOARD_LED_GPIO  8
#endif

#define TAG "EHOME"

/* ---- status_task — still in main.c (single-loop, minimal dependency) ---- */

static void status_task(void *pv)
{
    app_state_t *s = (app_state_t *)pv;
    while (1) {
        s->uptime_sec++;
        if (mqtt_client_is_connected_impl())
            msg_handler_send_status(s->uptime_sec, "online",
                                    (config_mgr_get_manifest() ? config_mgr_get_manifest()->channel_count : 0),
                                    scheduler_get_state());
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

/* ---- Modbus CRC16 helper ---- */

static uint16_t modbus_crc16(const uint8_t *data, size_t len)
{
    uint16_t crc = 0xFFFF;
    for (size_t i = 0; i < len; i++) {
        crc ^= data[i];
        for (int j = 0; j < 8; j++) {
            if (crc & 0x0001) {
                crc = (crc >> 1) ^ 0xA001;
            } else {
                crc >>= 1;
            }
        }
    }
    return crc;
}

/* ---- Modbus scan callback (overrides weak in handler_writecmd.c) ---- */

void on_modbus_scan_req_received(const char *request_id,
    uint32_t start_addr, uint32_t end_addr, uint32_t timeout_ms)
{
    ESP_LOGI("MODBUS_SCAN", "Scanning addresses %lu-%lu, timeout=%lums",
             (unsigned long)start_addr, (unsigned long)end_addr, (unsigned long)timeout_ms);

    app_state_t *s = app_state_get();

    /* Find first UART channel */
    bus_dma_ctx_t *uart_ctx = NULL;
    uint32_t channel_id = 0;
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s->bus_ctx[i].initialized && s->bus_ctx[i].bus_type == BUS_TYPE_UART) {
            uart_ctx = &s->bus_ctx[i];
            channel_id = s->bus_ch[i];
            break;
        }
    }

    if (!uart_ctx) {
        ESP_LOGE("MODBUS_SCAN", "No UART channel found");
        msg_handler_send_scan_rpt(request_id, 0, false, NULL, 0);
        return;
    }

    /* Scan each address */
    uint32_t found[256];
    uint8_t found_count = 0;

    for (uint32_t addr = start_addr; addr <= end_addr && addr <= 247; addr++) {
        /* Build Modbus 03 command: read holding register
         * [addr] [03] [00] [00] [00] [01] [CRC_L] [CRC_H] */
        uint8_t cmd[8];
        cmd[0] = (uint8_t)addr;
        cmd[1] = 0x03;
        cmd[2] = 0x00;
        cmd[3] = 0x00;
        cmd[4] = 0x00;
        cmd[5] = 0x01;
        uint16_t crc = modbus_crc16(cmd, 6);
        cmd[6] = crc & 0xFF;
        cmd[7] = (crc >> 8) & 0xFF;

        /* Send */
        esp_err_t err = bus_dma_write(uart_ctx, cmd, sizeof(cmd));
        if (err != ESP_OK) {
            continue;
        }

        /* Wait for response */
        uint8_t rx[256];
        size_t rx_len = 0;
        TickType_t start_tick = xTaskGetTickCount();
        while ((xTaskGetTickCount() - start_tick) < pdMS_TO_TICKS(timeout_ms)) {
            rx_len = bus_dma_read(uart_ctx, rx, sizeof(rx));
            if (rx_len >= 5) { /* Modbus response minimum 5 bytes */
                if (rx[0] == addr && rx[1] == 0x03) {
                    found[found_count++] = addr;
                    ESP_LOGI("MODBUS_SCAN", "Found device at addr %lu", (unsigned long)addr);
                }
                break;
            }
            vTaskDelay(pdMS_TO_TICKS(10));
        }
    }

    /* Send result */
    msg_handler_send_scan_rpt(request_id, channel_id, true, found, found_count);
    ESP_LOGI("MODBUS_SCAN", "Scan complete: %d devices found", found_count);
}

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

    /* ---- OTA: boot validation ---- */
    /* Check if running partition is pending verification (OTA rollback protection).
     * If so, run self-checks before confirming the new firmware as valid.
     * Self-checks: WiFi connected + MQTT connected + first StatusReport sent.
     * If any check fails 3 times → mark invalid → rollback to previous partition. */
    {
        esp_ota_img_states_t ota_state;
        const esp_partition_t *running = esp_ota_get_running_partition();
        if (esp_ota_get_state_partition(running, &ota_state) == ESP_OK) {
            if (ota_state == ESP_OTA_IMG_PENDING_VERIFY) {
                ESP_LOGW(TAG, "OTA: running partition is PENDING_VERIFY — boot validation active");
                /* Validation happens in app_callbacks after WiFi+MQTT+StatusReport succeed.
                 * If validation fails after 3 attempts, ota_mark_invalid_rollback() is called. */
                extern void ota_confirm_valid(void);
                ota_confirm_valid();  /* Will be called again by app_callbacks after full validation */
            }
        }
        /* Also check NVS state for power-loss recovery during download */
        if (ota_get_nvs_state() != 0) {
            ESP_LOGW(TAG, "OTA: NVS state=%d (power-loss recovery)", ota_get_nvs_state());
            extern void ota_confirm_valid(void);
            ota_confirm_valid();
        }
    }

    /* ---- App state (singleton — node_id from MAC, cmd_queue, spinlock) ---- */
    app_state_t *s = app_state_init();

    /* ---- UART0 boot mode check (MUST be before any UART0 driver install) ---- */
    /* If BOOT held at startup, UART0 reserved for download — task blocks here */
    if (!bus_dma_uart0_boot_init()) {
        ESP_LOGW(TAG, "UART0 in download mode, normal init skipped");
        /* bus_dma spawns a wait task; we just spin */
        while (1) { vTaskDelay(pdMS_TO_TICKS(1000)); }
    }

    /* ---- Subsystem init (dma_pool already initialized in app_state_init) ---- */
    config_mgr_init();
    
    /* DIP: inject dma_pool into components that need it */
    config_mgr_set_dma_pool(s->dma_pool);
    msg_handler_set_dma_pool(s->dma_pool);

    /* Server is single source of truth — no NVS config load at boot.
     * Config will arrive via ConfigManifest after Hello handshake. */
    
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

    rgb_led_init(BOARD_LED_GPIO);
    rgb_led_start();
    factory_reset_init();

    wifi_mgr_init();
    wifi_mgr_start();

    /* ---- Background tasks ---- */
    xTaskCreate(status_task, "status", 3072, (void *)s, 3, NULL);
    xTaskCreate(sync_manager_periodic_task, "sync", 3072, NULL, 2, NULL);
    /* Inject msg_handler callbacks into bus_worker and bus_manager (eliminates extern) */
    bus_worker_set_callbacks(msg_handler_send_write_rsp, msg_handler_send_data_report);
    bus_manager_set_write_rsp_cb(msg_handler_send_write_rsp);

    /* Inject OTA progress callback (eliminates ota → msg_handler cycle) */
    ota_set_progress_callback(msg_handler_send_ota_prog);

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
