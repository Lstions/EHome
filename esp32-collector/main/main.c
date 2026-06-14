/**
 * @file main.c
 * @brief EHomeSystem ESP32-C6 v2.3 — DMA UART + Command Queue
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
#include "uart_dma.h"

#define TAG "EHOME"
#define FIRMWARE_VERSION "2.3.0"

/* Inline Command Queue */
#define CQ_DEPTH 16
#define CQ_TX_MAX 128
typedef enum { CMD_WRITE = 0, CMD_SAMPLE = 1 } cmd_type_t;
typedef struct {
    uint32_t request_id, channel_id;
    uint8_t  tx_data[CQ_TX_MAX];
    size_t   tx_len;
    uint32_t read_size, timeout_ms;
    cmd_type_t type;
} uart_cmd_t;
QueueHandle_t g_cmd_queue;

/* UART DMA contexts */
#define MAX_UART 4
static uart_dma_ctx_t s_uart_ctx[MAX_UART];
static uint32_t s_uart_ch[MAX_UART];

static char s_node_id[32];
static uint32_t s_uptime_sec;
static bool s_config_received, s_ota_need_confirm;

const char *get_firmware_version(void) { return FIRMWARE_VERSION; }

static void on_wifi_state(wifi_mgr_state_t s, void *c);
static void on_mqtt_state(mqtt_client_state_t s, void *c);
static void on_mqtt_msg(const char *t, const uint8_t *d, size_t l, void *c);
static void generate_node_id(void);
static void status_task(void *p);
static void on_sync_send_hello(void);
static void hello_handshake_task(void *p);
static void uart_worker_task(void *p);

static uart_dma_ctx_t *find_ctx(uint32_t ch)
{
    for (int i = 0; i < MAX_UART; i++)
        if (s_uart_ch[i] == ch && s_uart_ctx[i].initialized)
            return &s_uart_ctx[i];
    return NULL;
}

static void reg_uart(uint32_t ch, uart_port_t port, int tx, int rx, uint32_t baud)
{
    for (int i = 0; i < MAX_UART; i++)
        if (s_uart_ch[i] == ch && s_uart_ctx[i].initialized) return;
    for (int i = 0; i < MAX_UART; i++) {
        if (s_uart_ch[i] == 0) {
            s_uart_ch[i] = ch;
            uart_dma_init(&s_uart_ctx[i], port, tx, rx, baud);
            ESP_LOGI(TAG, "UART ch=%lu DMA reg (idx=%d TX=%d RX=%d baud=%lu)",
                     (unsigned long)ch, i, tx, rx, (unsigned long)baud);
            return;
        }
    }
    ESP_LOGE(TAG, "UART slots full");
}

void app_main(void)
{
    ESP_LOGI(TAG, "EHomeSystem v%s DMA starting...", FIRMWARE_VERSION);
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
    g_cmd_queue = xQueueCreate(CQ_DEPTH, sizeof(uart_cmd_t));
    wifi_mgr_register_state_cb(on_wifi_state, NULL);
    mqtt_client_register_state_cb(on_mqtt_state, NULL);
    mqtt_client_register_msg_cb(on_mqtt_msg, NULL);
    mqtt_client_set_node_id(s_node_id);
    rgb_led_init(8); rgb_led_start();
    factory_reset_init();
    wifi_mgr_init(); wifi_mgr_start();
    xTaskCreate(status_task, "status", 2048, NULL, 3, NULL);
    xTaskCreate(sync_manager_periodic_task, "sync", 3072, NULL, 2, NULL);
    xTaskCreate(uart_worker_task, "uart_worker", 4096, NULL, 8, NULL);
    ESP_LOGI(TAG, "Init done, node=%s", s_node_id);
}

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

static void on_mqtt_state(mqtt_client_state_t s, void *c)
{
    (void)c;
    if (s == MQTT_CLIENT_CONNECTED) {
        rgb_led_set_state(LED_STATE_RUNNING);
        msg_handler_reset_hello_ack();
        xTaskCreate(hello_handshake_task, "hello", 3072, NULL, 5, NULL);
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
        scheduler_start();
        ESP_LOGI(TAG, "Config→scheduler %d ch", scheduler_get_channel_count());
    }
}

static void generate_node_id(void) { strlcpy(s_node_id, "1001", sizeof(s_node_id)); }

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
    if (!ok) { ESP_LOGE(TAG, "Hello failed"); rgb_led_set_state(LED_STATE_MQTT_FAILED); }
    else {
        rgb_led_set_state(LED_STATE_RUNNING);
        if (!scheduler_is_running() && config_mgr_has_manifest()) {
            config_mgr_load_from_nvs();
            scheduler_start();
        }
    }
    vTaskDelete(NULL);
}

static void uart_worker_task(void *p)
{
    (void)p;
    uart_cmd_t cmd;
    uint8_t rx[256];
    ESP_LOGI(TAG, "UART worker (prio=%d)", uxTaskPriorityGet(NULL));
    while (1) {
        if (!xQueueReceive(g_cmd_queue, &cmd, portMAX_DELAY)) continue;
        uart_dma_ctx_t *ctx = find_ctx(cmd.channel_id);
        if (!ctx) {
            if (cmd.type == CMD_WRITE)
                msg_handler_send_write_rsp(cmd.request_id, false, 4, "no ctx");
            continue;
        }
        size_t rl = 0;
        esp_err_t e = uart_dma_transact(ctx, cmd.tx_data, cmd.tx_len,
                                         cmd.timeout_ms ? cmd.timeout_ms : 50,
                                         rx, sizeof(rx), &rl);
        if (cmd.type == CMD_WRITE) {
            bool ok = (e == ESP_OK);
            msg_handler_send_write_rsp(cmd.request_id, ok, ok ? 0 : (uint32_t)e,
                                       ok ? NULL : "uart err");
            if (rl > 0) {
                uint64_t ts = esp_timer_get_time();
                msg_handler_send_data_report(cmd.channel_id, ts, 0, rx, rl, 0, cmd.request_id);
            }
        }
        if (cmd.type == CMD_SAMPLE && rl > 0) {
            uint64_t ts = esp_timer_get_time();
            msg_handler_send_data_report(cmd.channel_id, ts, 0, rx, rl, 0, 0);
        }
    }
}

void on_write_cmd_received(uint32_t rid, uint32_t ch, const uint8_t *d, size_t l, uint32_t rs)
{
    uart_cmd_t cmd = {
        .request_id = rid, .channel_id = ch,
        .tx_len = l < CQ_TX_MAX ? l : CQ_TX_MAX,
        .read_size = rs, .timeout_ms = 50, .type = CMD_WRITE
    };
    if (l > 0 && d) memcpy(cmd.tx_data, d, cmd.tx_len);
    if (!xQueueSend(g_cmd_queue, &cmd, 0))
        msg_handler_send_write_rsp(rid, false, 0xFFFF, "queue full");
}

void scheduler_register_uart(uint32_t ch, uint8_t bt, const uint8_t *cfg, size_t cfglen)
{
    if (bt != 1) return;
    int tx = 21, rx = 20;
    uint32_t bd = 9600;
    if (cfg && cfglen >= 2) { tx = cfg[0]; rx = cfg[1]; }
    if (cfg && cfglen >= 6)
        bd = ((uint32_t)cfg[2] << 24) | ((uint32_t)cfg[3] << 16) |
             ((uint32_t)cfg[4] << 8) | cfg[5];
    reg_uart(ch, UART_NUM_1, tx, rx, bd);
}
