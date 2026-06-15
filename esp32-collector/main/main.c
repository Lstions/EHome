/**
 * @file main.c
 * @brief EHomeSystem ESP32-C6 v2.4 — Unified Bus DMA + Resource Reporting
 */

#include <stdio.h>
#include <string.h>
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/queue.h"
#include "esp_system.h"
#include "esp_log.h"
#include "esp_ota_ops.h"
#include "esp_timer.h"
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
#include "bus_dma.h"
#include "cmd_queue.h"
#include "hw_profile.h"

#define TAG "EHOME"
#define FIRMWARE_VERSION "2.4.0"

/* Global command queue (declared extern in cmd_queue.h) */
QueueHandle_t g_cmd_queue;

/* Unified bus DMA contexts — supports UART, I2C, SPI (up to SCHED_MAX_CHANNELS) */
static bus_dma_ctx_t s_bus_ctx[SCHED_MAX_CHANNELS];
static uint32_t    s_bus_ch[SCHED_MAX_CHANNELS];

static char s_node_id[32];
static uint32_t s_uptime_sec;
static bool s_config_received, s_ota_need_confirm;

const char *get_firmware_version(void) { return FIRMWARE_VERSION; }

/* Forward declarations */
static void on_wifi_state(wifi_mgr_state_t s, void *c);
static void on_mqtt_state(mqtt_client_state_t s, void *c);
static void on_mqtt_msg(const char *t, const uint8_t *d, size_t l, void *c);
static void generate_node_id(void);
static void status_task(void *p);
static void on_sync_send_hello(void);
static void hello_handshake_task(void *p);
static void bus_worker_task(void *p);

/* Provided by msg_handler — sends hw_profile ResourceReport (MSG 0x19) */
extern void msg_handler_send_resource_report(void);

/* === Helper: find bus DMA context by channel ID === */
static bus_dma_ctx_t *find_ctx(uint32_t ch)
{
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++)
        if (s_bus_ch[i] == ch && s_bus_ctx[i].initialized)
            return &s_bus_ctx[i];
    return NULL;
}

/* === Helper: extract dma_enabled flag from bus config bytes === */
static bool get_dma_enabled(uint8_t bus_type, const uint8_t *cfg, size_t cfglen)
{
    size_t flags_offset;
    switch (bus_type) {
    case BUS_TYPE_UART: flags_offset = 6; break;
    case BUS_TYPE_I2C:  flags_offset = 7; break;
    case BUS_TYPE_SPI:  flags_offset = 6; break;
    default: return true;
    }
    if (cfg && cfglen > flags_offset)
        return (cfg[flags_offset] & 0x01) != 0;
    return true; /* default: DMA enabled */
}

/* === Helper: look up bus_type for a channel from the config manifest === */
static uint8_t find_bus_type(uint32_t ch)
{
    const config_manifest_t *m = config_mgr_get_manifest();
    if (!m) return 0;
    for (int i = 0; i < m->channel_count; i++)
        if (m->channels[i].id == ch) return m->channels[i].bus_type;
    return 0;
}

/* Forward declarations */
static void reg_bus_channel(uint32_t ch_id, uint8_t bus_type,
                            const uint8_t *config, size_t config_len);

/* === Cleanup all bus DMA contexts === */
static void cleanup_bus_channels(void)
{
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s_bus_ctx[i].initialized) {
            bus_dma_deinit(&s_bus_ctx[i]);
            s_bus_ch[i] = 0;
        }
    }
}

/* === Register all bus channels from manifest === */
static void setup_bus_channels(void)
{
    const config_manifest_t *m = config_mgr_get_manifest();
    if (!m || !m->applied) return;
    for (int i = 0; i < m->channel_count; i++) {
        if (!m->channels[i].enabled) continue;
        reg_bus_channel(m->channels[i].id, m->channels[i].bus_type,
                        m->channels[i].bus_config, m->channels[i].bus_config_len);
    }
}

/* === Register a bus channel (UART/I2C/SPI) in the unified pool === */
static void reg_bus_channel(uint32_t ch_id, uint8_t bus_type,
                            const uint8_t *config, size_t config_len)
{
    /* Already registered? */
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++)
        if (s_bus_ch[i] == ch_id && s_bus_ctx[i].initialized) return;

    /* Find a free slot */
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s_bus_ch[i] == 0) {
            s_bus_ch[i] = ch_id;
            bool dma = get_dma_enabled(bus_type, config, config_len);
            esp_err_t err = bus_dma_init(&s_bus_ctx[i], bus_type, dma,
                                         config, config_len);
            if (err == ESP_OK) {
                ESP_LOGI(TAG, "Bus ch=%lu type=%u DMA=%d reg (idx=%d)",
                         (unsigned long)ch_id, bus_type, dma, i);
            } else {
                ESP_LOGE(TAG, "Bus ch=%lu type=%u init failed: %s",
                         (unsigned long)ch_id, bus_type, esp_err_to_name(err));
                s_bus_ch[i] = 0;
            }
            return;
        }
    }
    ESP_LOGE(TAG, "Bus slots full (max=%d)", SCHED_MAX_CHANNELS);
}

/* ================================================================== */
/*  app_main                                                          */
/* ================================================================== */

void app_main(void)
{
    ESP_LOGI(TAG, "EHomeSystem v%s unified bus DMA starting...", FIRMWARE_VERSION);

    esp_err_t r = nvs_flash_init();
    if (r == ESP_ERR_NVS_NO_FREE_PAGES || r == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        ESP_ERROR_CHECK(nvs_flash_erase());
        r = nvs_flash_init();
    }
    ESP_ERROR_CHECK(r);

    if (ota_get_nvs_state() != 0) s_ota_need_confirm = true;
    ota_confirm_valid();
    s_ota_need_confirm = false;

    generate_node_id();
    config_mgr_init();
    sync_manager_init();
    sync_manager_register_send_hello_cb(on_sync_send_hello);
    msg_handler_init();
    ota_init();
    scheduler_init();
    mqtt_client_init();

    g_cmd_queue = xQueueCreate(CMD_QUEUE_DEPTH, sizeof(bus_cmd_t));

    wifi_mgr_register_state_cb(on_wifi_state, NULL);
    mqtt_client_register_state_cb(on_mqtt_state, NULL);
    mqtt_client_register_msg_cb(on_mqtt_msg, NULL);
    mqtt_client_set_node_id(s_node_id);

    rgb_led_init(8);
    rgb_led_start();
    factory_reset_init();

    wifi_mgr_init();
    wifi_mgr_start();

    xTaskCreate(status_task, "status", 2048, NULL, 3, NULL);
    xTaskCreate(sync_manager_periodic_task, "sync", 3072, NULL, 2, NULL);
    xTaskCreate(bus_worker_task, "bus_worker", 4096, NULL, 8, NULL);

    ESP_LOGI(TAG, "Init done, node=%s", s_node_id);
}

/* ================================================================== */
/*  WiFi / MQTT callbacks                                             */
/* ================================================================== */

static void on_wifi_state(wifi_mgr_state_t s, void *c)
{
    (void)c;
    if (s == WIFI_MGR_CONNECTED) {
        rgb_led_set_state(LED_STATE_MQTT_CONNECTING);
        if (s_ota_need_confirm) { ota_confirm_valid(); s_ota_need_confirm = false; }
        mqtt_client_start();
    } else if (s == WIFI_MGR_CONNECTING) {
        rgb_led_set_state(LED_STATE_WIFI_CONNECTING);
    } else if (s == WIFI_MGR_FAILED) {
        rgb_led_set_state(LED_STATE_WIFI_FAILED);
    }
}

static bool s_hello_task_running = false;

static void on_mqtt_state(mqtt_client_state_t s, void *c)
{
    (void)c;
    if (s == MQTT_CLIENT_CONNECTED) {
        rgb_led_set_state(LED_STATE_RUNNING);
        if (!s_hello_task_running) {
            s_hello_task_running = true;
            msg_handler_reset_hello_ack();
            xTaskCreate(hello_handshake_task, "hello", 6144, NULL, 5, NULL);
        }
    } else if (s == MQTT_CLIENT_FAILED) {
        rgb_led_set_state(LED_STATE_MQTT_FAILED);
    }
}

static void on_mqtt_msg(const char *t, const uint8_t *d, size_t l, void *c)
{
    (void)t; (void)c;
    bool is_cfg = (l > 0 && d[0] == 0x04);
    if (is_cfg) s_config_received = true;
    msg_handler_process(d, l);
    if (is_cfg) {
        if (scheduler_is_running()) scheduler_stop();
        cleanup_bus_channels();
        setup_bus_channels();
        scheduler_start();
        ESP_LOGI(TAG, "Config→scheduler %d ch", scheduler_get_channel_count());
    }
}

/* ================================================================== */
/*  Utility                                                           */
/* ================================================================== */

static void generate_node_id(void) { strlcpy(s_node_id, "1001", sizeof(s_node_id)); }

/* ================================================================== */
/*  Status task                                                       */
/* ================================================================== */

static void status_task(void *p)
{
    (void)p;
    while (1) {
        s_uptime_sec++;
        if (mqtt_client_is_connected_impl())
            msg_handler_send_status(s_uptime_sec, "online", config_mgr_get_active_channel_count());
        vTaskDelay(pdMS_TO_TICKS(5000));
    }
}

/* ================================================================== */
/*  Sync / Hello handshake                                            */
/* ================================================================== */

static void on_sync_send_hello(void)
{
    msg_handler_send_hello(s_node_id, FIRMWARE_VERSION, CONFIG_IDF_TARGET,
                           config_mgr_get_active_channel_count());
}

#define HR 3
#define HT 10000
#define HI 5000

static void hello_handshake_task(void *p)
{
    (void)p;
    bool ok = false;

    for (int i = 0; i < HR && !ok; i++) {
        msg_handler_send_hello(s_node_id, FIRMWARE_VERSION, CONFIG_IDF_TARGET,
                               config_mgr_get_active_channel_count());
        uint32_t w = 0;
        while (w < HT) {
            if (msg_handler_is_hello_ack_received()) { ok = true; break; }
            vTaskDelay(pdMS_TO_TICKS(500));
            w += 500;
        }
        if (!ok) {
            ESP_LOGW(TAG, "Hello timeout");
            rgb_led_set_state(LED_STATE_MQTT_CONNECTING);
            vTaskDelay(pdMS_TO_TICKS(HI));
        }
    }

    if (!ok) {
        ESP_LOGE(TAG, "Hello failed");
        rgb_led_set_state(LED_STATE_MQTT_FAILED);
    } else {
        rgb_led_set_state(LED_STATE_RUNNING);

        /* v2.4: Send ResourceReport after successful handshake */
        msg_handler_send_resource_report();

        if (!scheduler_is_running() && config_mgr_has_manifest()) {
            config_mgr_load_from_nvs();
            setup_bus_channels();
            scheduler_start();
        }
    }
    s_hello_task_running = false;
    vTaskDelete(NULL);
}

/* ================================================================== */
/*  Bus worker task — unified UART/I2C/SPI DMA transaction engine     */
/* ================================================================== */

static void bus_worker_task(void *p)
{
    (void)p;
    bus_cmd_t cmd;
    uint8_t rx[256];

    ESP_LOGI(TAG, "Bus worker (prio=%d)", uxTaskPriorityGet(NULL));

    /* Performance metrics */
    uint32_t total_transactions = 0;
    uint32_t total_errors = 0;
    uint32_t no_ctx_count = 0;
    TickType_t last_stats_time = xTaskGetTickCount();

    while (1) {
        if (!xQueueReceive(g_cmd_queue, &cmd, portMAX_DELAY)) continue;

        bus_dma_ctx_t *ctx = find_ctx(cmd.channel_id);
        if (!ctx) {
            no_ctx_count++;
            if (cmd.type == CMD_WRITE)
                msg_handler_send_write_rsp(cmd.request_id, false, 4, "no ctx");
            
            /* Notify scheduler of error for backoff */
            scheduler_notify_channel_error(cmd.channel_id);
            continue;
        }

        size_t rl = 0;
        esp_err_t e = bus_dma_transact(ctx, cmd.tx_data, cmd.tx_len,
                                        cmd.timeout_ms ? cmd.timeout_ms : 50,
                                        rx, sizeof(rx), &rl);

        total_transactions++;
        if (e != ESP_OK) {
            total_errors++;
            scheduler_notify_channel_error(cmd.channel_id);
        } else {
            scheduler_notify_channel_success(cmd.channel_id);
        }

        if (cmd.type == CMD_WRITE) {
            bool ok = (e == ESP_OK);
            msg_handler_send_write_rsp(cmd.request_id, ok, ok ? 0 : (uint32_t)e,
                                       ok ? NULL : "bus err");
            if (rl > 0) {
                uint64_t ts = esp_timer_get_time();
                msg_handler_send_data_report(cmd.channel_id, ts, 0, rx, rl, 0, cmd.request_id);
            }
        }

        if (cmd.type == CMD_SAMPLE && rl > 0) {
            uint64_t ts = esp_timer_get_time();
            msg_handler_send_data_report(cmd.channel_id, ts, 0, rx, rl, 0, 0);
        }

        /* Periodic performance logging (every 10 seconds) */
        TickType_t now = xTaskGetTickCount();
        if (now - last_stats_time > pdMS_TO_TICKS(10000)) {
            if (total_transactions > 0 || total_errors > 0 || no_ctx_count > 0) {
                uint32_t success_rate = total_transactions > 0 ? 
                    ((total_transactions - total_errors) * 100 / total_transactions) : 0;
                ESP_LOGI(TAG, "Worker stats: txn=%" PRIu32 " err=%" PRIu32 
                         " (%" PRIu32 "%%) no_ctx=%" PRIu32,
                         total_transactions, total_errors, success_rate, no_ctx_count);
            }
            total_transactions = 0;
            total_errors = 0;
            no_ctx_count = 0;
            last_stats_time = now;
        }
    }
}

/* ================================================================== */
/*  Public API — called by scheduler and msg_handler                  */
/* ================================================================== */

void on_write_cmd_received(uint32_t rid, uint32_t ch,
                           const uint8_t *d, size_t l, uint32_t rs)
{
    bus_cmd_t cmd = {
        .request_id = rid,
        .channel_id = ch,
        .bus_type   = find_bus_type(ch),
        .tx_len     = l < CMD_TX_MAX ? l : CMD_TX_MAX,
        .read_size  = rs,
        .timeout_ms = 50,
        .type       = CMD_WRITE,
    };
    if (l > 0 && d) memcpy(cmd.tx_data, d, cmd.tx_len);

    if (!xQueueSend(g_cmd_queue, &cmd, 0))
        msg_handler_send_write_rsp(rid, false, 0xFFFF, "queue full");
}
