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
#include "crash_diag.h"
#include "msg_handler_internal.h"
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
#include "log_stream.h"
#ifdef CONFIG_DEBUG_TCP_ENABLED
#include "ehome_tcp.h"
#endif
#include "nvs_flash.h"
#include "esp_ota_ops.h"
#include "esp_log.h"
#include "esp_task_wdt.h"   /* 8.3: Task watchdog initialization */

/* RGB LED pin differs by board: S3=GPIO48, C6=GPIO8 */
#ifdef CONFIG_IDF_TARGET_ESP32S3
  #define BOARD_LED_GPIO  48
#elif defined(CONFIG_IDF_TARGET_ESP32C6)
  #define BOARD_LED_GPIO  8
#else
  #define BOARD_LED_GPIO  8
#endif

#define TAG "EHOME"

/* status_task 的栈。6144 在 2026-10-04 被证明不够：设备在压测中两次以
 * reset_reason=4(PANIC) 崩溃，pc 落在 FreeRTOS 的 prvTaskCheckFreeStackSpace
 * （栈溢出检测点），crash_diag 记下的 task 名就是 "status"。
 *
 * 为什么 6144 不够：status_task 每秒调用 msg_handler_send_status，而该函数有
 * uint8_t buf[1400] 的栈上帧（components/msg_handler/handler_data.c:107），
 * 之后还要走 msg_handler_publish_checked → MQTT → lwIP，这条链本身有 1KB 以上
 * 开销 —— 与 log_tx 那次栈溢出是同一形态（见 log_stream.c 的同类修复）。
 * 1400 + 发布链 + 调用者帧，6144 的余量过薄。
 *
 * 取 8192：与 OTA 任务同量级（ota.c 的 OTA_TASK_STACK_BYTES），
 * 给 buf[1400] + 发布链留约 2 倍余量。
 *
 * ⚠ 这只是把余量做够，**没有**消除根因：msg_handler_send_status 仍把 1400 字节
 * 放在栈上。main/CMakeLists.txt 的 -Wframe-larger-than 门禁此前只作用于
 * app_callbacks.c 这一个文件，所以这个 1400 字节帧从未被门禁检查过；
 * handler_data.c 属于 msg_handler 组件，完全在门禁视野之外。 */
#define STATUS_TASK_STACK 8192

/* StatusReport 上报周期 (毫秒)。1s 是节点离线可见时延预算的一部分：
 * 最坏 = 1s(本周期) + 3s(服务端 NodeOfflineThreshold) + 1s(服务端检测 ticker) = 5s。
 * 改这里必须同步 backend/internal/offlinedetector/offlinedetector.go 的
 * FirmwareStatusReportPeriod 常量与预算注释，否则指标会被静默破坏。
 * 关于 uptime：status_task 取的是真实单调时钟（app_state_uptime_sec_now），
 * 不是按本周期自增，所以把 5s 压到 1s 不会让 uptime 失真。 */
#define STATUS_REPORT_PERIOD_MS 1000

static bool s_ota_pending_verify = false;

/* ---- status_task — still in main.c (single-loop, minimal dependency) ---- */

static void status_task(void *pv)
{
    app_state_t *s = (app_state_t *)pv;
    while (1) {
        /* uptime 必须取真实单调时钟，不能按上报周期自增：
         * 若改成每循环 +1，StatusReport field 1（单位=秒）就会变成
         * "真实运行秒数 / STATUS_REPORT_PERIOD_MS" 的比例值——周期从 5s
         * 收到 1s 后这个偏差会放大 5 倍。取 app_state_uptime_sec_now()
         * 与周期无关，所以收紧周期不会让 uptime 失真。 */
        s->uptime_sec = app_state_uptime_sec_now();
        if (mqtt_client_is_connected_impl()) {
            esp_err_t status_err = msg_handler_send_status(
                s->uptime_sec, "online",
                (config_mgr_get_manifest() ? config_mgr_get_manifest()->channel_count : 0),
                scheduler_get_state());
			if (s->ota_need_confirm && status_err == ESP_OK) {
				if (ota_confirm_valid() == ESP_OK) s->ota_need_confirm = false;
			}
		}
        vTaskDelay(pdMS_TO_TICKS(STATUS_REPORT_PERIOD_MS));
    }
}

/* ---- sync send_hello callback ---- */

static void on_sync_send_hello(void)
{
    (void)hello_handshake_request_sync();
}

/* ---- Weak-symbol bridges for msg_handler callbacks ---- */

void on_write_cmd_received(uint32_t rid, uint32_t ch,
                           const uint8_t *d, size_t l, uint32_t rs,
                           uint32_t edge_device_id, uint32_t rx_timeout_ms)
{
    bus_manager_on_write_cmd(&app_state_get()->bus_runtime, rid, ch, d, l, rs,
                             edge_device_id, rx_timeout_ms);
}

const char *channel_cmd_v2_current_boot_id(void)
{
    return app_state_get()->boot_id;
}

uint64_t channel_cmd_v2_current_time_ms(void)
{
    return msg_handler_get_current_server_time_ms();
}

bool on_channel_cmd_v2_received(const channel_cmd_v2_t *cmd, uint8_t slot)
{
    if (!cmd) return false;
    return bus_manager_on_channel_cmd_v2(&app_state_get()->bus_runtime, cmd->channel_id,
        cmd->tx_data, cmd->tx_len, cmd->read_size, cmd->rx_timeout_ms,
        cmd->post_tx_delay_ms, cmd->plan_data, cmd->plan_len, cmd->plan_step_count, slot);
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
                /* Validation happens in app_callbacks only after WiFi, MQTT,
                 * and the first StatusReport have all succeeded. Do not cancel
                 * rollback here. */
				s_ota_pending_verify = true;
            }
        }
        /* A non-zero download NVS state is recovery metadata, not proof that
         * the running image passed boot validation. Preserve it for OTA
         * recovery logic and never cancel rollback from here. */
        if (ota_get_nvs_state() != 0) {
            ESP_LOGW(TAG, "OTA: NVS state=%d (power-loss recovery pending)", ota_get_nvs_state());
        }
    }

    /* ---- App state (singleton — node_id from MAC, cmd_queue, spinlock) ---- */
    app_state_t *s = app_state_init();
	s->ota_need_confirm = s_ota_pending_verify;

    /* ---- Crash diagnostics (v2.6) ----
     * 必须在 nvs_flash_init() 之后：把上次 panic 留在 RTC_NOINIT 里的记录
     * 搬进 NVS。设备此前完全不记录复位原因，导致"反复重启"无法定性；
     * 这一步让每次启动都带一个可上报的原因。 */
    crash_diag_init();

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
    /* Create the long-lived Hello supervisor before MQTT can start. */
    hello_handshake_start(s);
    log_stream_set_publish_callback(msg_handler_publish);
    if (handler_periph_init() != ESP_OK) {
        ESP_LOGE(TAG, "Peripheral handler initialization failed; restarting");
        crash_diag_mark_reboot_reason("periph_init_failed");
        esp_restart();
    }   /* v3.0: GPIO/PWM peripheral control queues + tasks */
    ota_init();
    scheduler_init();

    /* ---- Transports ---- */
    mqtt_client_init();
    transport_manager_init();
    mqtt_transport_register();

    /* ---- WiFi + callbacks ---- */
    wifi_mgr_register_state_cb(on_wifi_state_cb, s);
    mqtt_client_register_state_cb(on_mqtt_state_cb, s);
    mqtt_client_register_ready_cb(on_mqtt_ready_cb, s);
    mqtt_client_register_transport_cb(on_mqtt_transport_cb, s);
    mqtt_client_register_owner_wake_cb(on_mqtt_owner_wake_cb, s);
    mqtt_client_register_msg_cb(on_mqtt_msg_cb, s);
    mqtt_client_set_node_id(s->node_id);

    rgb_led_init(BOARD_LED_GPIO);
    rgb_led_start();
    factory_reset_init();

    wifi_mgr_init();
    wifi_mgr_start();

    /* ---- Background tasks ---- */
    /* StatusReport now carries runtime queue/performance metrics and nested
     * channel health.  Its bounded encoder buffers live on this call stack;
     * keep a dedicated budget so a valid report cannot trip the stack guard
     * while UART RX workers are active. */
    xTaskCreate(status_task, "status", STATUS_TASK_STACK, (void *)s, 3, NULL);
    xTaskCreate(sync_manager_periodic_task, "sync", 3072, NULL, 2, NULL);
    /* Inject msg_handler callbacks into bus_worker and bus_manager (eliminates extern) */
    bus_worker_set_callbacks(msg_handler_send_write_rsp, msg_handler_send_data_report);
    bus_worker_set_channel_cmd_v2_final_cb(handler_channel_cmd_v2_complete);
    bus_manager_set_write_rsp_cb(msg_handler_send_write_rsp);

    /* Inject OTA progress callback (eliminates ota → msg_handler cycle) */
    ota_set_progress_callback(msg_handler_send_ota_prog);

    /* Inject crash-record ack callback (v2.6): msg_handler cannot depend on
     * main, so the release-on-ACK handler is injected here. */
    msg_handler_set_diag_ack_cb(crash_diag_on_ack);

    /* 8.3: Initialize task watchdog — 10 second timeout, panic on timeout
     * ESP-IDF v6.0 CONFIG_ESP_TASK_WDT_INIT=1 auto-initializes TWDT (5s)
     * before app_main. We must deinit first, then reinit with our config. */
    esp_task_wdt_deinit();
    esp_task_wdt_config_t wdt_config = {
        .timeout_ms = 10000,
        .idle_core_mask = 0,
        .trigger_panic = true,
    };
    ESP_ERROR_CHECK(esp_task_wdt_init(&wdt_config));

    bus_worker_start(&s->bus_runtime);

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
