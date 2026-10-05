/*
 * wifi_mgr_rssi_tests.c
 *
 * Host tests for wifi_mgr_get_rssi_dbm() in wifi_mgr.c.
 *
 * Compiles the real wifi_mgr.c against host stubs (wifi_stubs/) and drives
 * the registered WIFI/IP event handler directly to flip the module into
 * CONNECTED / FAILED states, then exercises the RSSI query contract:
 *
 *   1. Disconnected (initial state)                 -> returns 0
 *   2. Connected, esp_wifi reports -55 dBm          -> returns -55
 *   3. Connected, esp_wifi reports -92 dBm          -> returns -92
 *   4. Connected, esp_wifi_sta_get_ap_info() fails  -> returns 0
 *   5. After STA_DISCONNECTED (retries exhausted)   -> returns 0
 */

#include <stdio.h>
#include <string.h>
#include <stdlib.h>
#include <stdint.h>

/* ---- Stub headers (wifi_stubs/ first so it wins over stubs/) ---- */
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "esp_err.h"
#include "esp_log.h"
#include "esp_check.h"
#include "esp_wifi.h"
#include "esp_event.h"
#include "esp_netif.h"
#include "nvs_flash.h"

#include "wifi_mgr.h"

/* =====================================================================
 * Test infrastructure
 * ===================================================================== */
static int g_failures = 0;

#define CHECK(cond, msg) do { \
    if (!(cond)) { \
        fprintf(stderr, "FAIL %s:%d: %s\n", __func__, __LINE__, (msg)); \
        g_failures++; \
    } \
} while (0)

/* =====================================================================
 * Controllable esp_wifi stub
 * ===================================================================== */
static int g_stub_ap_rssi = -55;
static esp_err_t g_stub_ap_err = ESP_OK;
static int g_connect_calls = 0;
static int g_disconnect_calls = 0;

/* stubs/esp_timer.h 声明为 extern，这里给出唯一一份定义（可控假时钟）。 */
int64_t g_test_time_us = 0;

esp_err_t esp_wifi_sta_get_ap_info(wifi_ap_record_t *ap_info) {
    if (g_stub_ap_err != ESP_OK) return g_stub_ap_err;
    memset(ap_info, 0, sizeof(*ap_info));
    ap_info->rssi = (int8_t)g_stub_ap_rssi;
    return ESP_OK;
}

/* =====================================================================
 * Minimal stubs for the rest of wifi_mgr.c's dependencies
 * ===================================================================== */
void host_test_log_record(char level, const char *tag, const char *format, ...) {
    (void)level; (void)tag; (void)format;
}
const char *esp_err_to_name(esp_err_t err) { (void)err; return "ESP_ERR"; }

/* --- event groups --- */
typedef struct { EventBits_t bits; } stub_eg_t;
static stub_eg_t g_eg;
EventGroupHandle_t xEventGroupCreate(void) { g_eg.bits = 0; return &g_eg; }
EventBits_t xEventGroupWaitBits(EventGroupHandle_t eg, EventBits_t bits,
                                int clear, int waitall, uint32_t ticks) {
    (void)eg; (void)bits; (void)clear; (void)waitall; (void)ticks;
    return 0;   /* timeout: wifi_mgr_start() path not used in these tests */
}
EventBits_t xEventGroupClearBits(EventGroupHandle_t eg, EventBits_t bits) {
    stub_eg_t *g = eg; g->bits &= ~bits; return g->bits;
}
EventBits_t xEventGroupSetBits(EventGroupHandle_t eg, EventBits_t bits) {
    stub_eg_t *g = eg; g->bits |= bits; return g->bits;
}
void vTaskDelay(uint32_t ticks) { (void)ticks; }

/* --- esp_netif --- */
esp_err_t esp_netif_init(void) { return ESP_OK; }
esp_netif_t *esp_netif_create_default_wifi_sta(void) { return (esp_netif_t *)&g_eg; }
esp_netif_t *esp_netif_create_default_wifi_ap(void) { return (esp_netif_t *)&g_eg; }

/* --- esp_event: capture the WIFI/IP handler so tests can fire events --- */
#define WIFI_EVENT ((esp_event_base_t)"WIFI_EVENT")
#define IP_EVENT   ((esp_event_base_t)"IP_EVENT")
enum {
    WIFI_EVENT_STA_START = 0,
    WIFI_EVENT_STA_DISCONNECTED = 5,
    WIFI_EVENT_STA_CONNECTED = 4,
    IP_EVENT_STA_GOT_IP = 0,
};
typedef struct {
    uint8_t reason;
} wifi_event_sta_disconnected_t;
typedef struct {
    esp_netif_ip_info_t ip_info;
} ip_event_got_ip_t;

static void (*g_wifi_handler)(void *, esp_event_base_t, int32_t, void *) = NULL;
static void *g_wifi_handler_arg = NULL;

esp_err_t esp_event_loop_create_default(void) { return ESP_OK; }
esp_err_t esp_event_handler_instance_register(esp_event_base_t base, int32_t id,
                                              void *handler, void *arg, void *inst) {
    (void)id; (void)inst;
    if (base == WIFI_EVENT) {
        g_wifi_handler = handler;
        g_wifi_handler_arg = arg;
    }
    return ESP_OK;
}

/* --- esp_wifi init/config (no-ops) --- */
esp_err_t esp_wifi_init(const wifi_init_config_t *cfg) { (void)cfg; return ESP_OK; }
esp_err_t esp_wifi_set_mode(wifi_mode_t mode) { (void)mode; return ESP_OK; }
esp_err_t esp_wifi_set_config(wifi_interface_t ifx, wifi_config_t *conf) {
    (void)ifx; (void)conf; return ESP_OK;
}
esp_err_t esp_wifi_start(void) { return ESP_OK; }
esp_err_t esp_wifi_stop(void) { return ESP_OK; }
esp_err_t esp_wifi_connect(void) { g_connect_calls++; return ESP_OK; }
/* esp_wifi_disconnect 由 2026-10-05 的静默失联修复引入（主动链路探针）。 */
esp_err_t esp_wifi_disconnect(void) { g_disconnect_calls++; return ESP_OK; }

/* 探针用 esp_timer 做节流。时钟由 stubs/esp_timer.h 提供（g_fake_now_us），
 * 测试通过推进 g_test_time_us 来跨越 5s 节流窗口。 */

/* --- nvs_flash --- */
esp_err_t nvs_open(const char *ns, int mode, nvs_handle_t *out) {
    (void)ns; (void)mode; *out = 1; return ESP_OK;
}
esp_err_t nvs_set_str(nvs_handle_t h, const char *k, const char *v) {
    (void)h; (void)k; (void)v; return ESP_OK;
}
esp_err_t nvs_get_str(nvs_handle_t h, const char *k, char *v, size_t *len) {
    (void)h; (void)k; (void)v; (void)len; return ESP_FAIL;
}
esp_err_t nvs_commit(nvs_handle_t h) { (void)h; return ESP_OK; }
void nvs_close(nvs_handle_t h) { (void)h; }
esp_err_t nvs_erase_all(nvs_handle_t h) { (void)h; return ESP_OK; }

/* --- provisioning (implemented in wifi_provisioning.c; stubbed here) --- */
bool wifi_provisioning_is_active(void) { return false; }
void wifi_provisioning_set_active(bool a) { (void)a; }
void wifi_provisioning_start_http_server(void) {}
void wifi_provisioning_stop_http_server(void) {}

/* =====================================================================
 * Include the real implementation under test
 * ===================================================================== */
#include "../components/wifi_mgr/wifi_mgr.c"

/* =====================================================================
 * Event simulation helpers
 * ===================================================================== */
static void simulate_got_ip(void) {
    ip_event_got_ip_t ev;
    memset(&ev, 0, sizeof(ev));
    g_wifi_handler(g_wifi_handler_arg, IP_EVENT, IP_EVENT_STA_GOT_IP, &ev);
}

static void simulate_disconnect_reason(int reason) {
    wifi_event_sta_disconnected_t ev;
    memset(&ev, 0, sizeof(ev));
    ev.reason = (uint8_t)reason;
    g_wifi_handler(g_wifi_handler_arg, WIFI_EVENT, WIFI_EVENT_STA_DISCONNECTED, &ev);
}

/* =====================================================================
 * Tests
 * ===================================================================== */
static void test_rssi_when_disconnected_initially(void) {
    CHECK(wifi_mgr_get_state() == WIFI_MGR_DISCONNECTED,
          "initial state should be DISCONNECTED");
    CHECK(wifi_mgr_get_rssi_dbm() == 0,
          "disconnected: rssi must be 0 (no WiFi data)");
}

static void test_rssi_when_connected(void) {
    simulate_got_ip();
    CHECK(wifi_mgr_get_state() == WIFI_MGR_CONNECTED,
          "state should be CONNECTED after GOT_IP");

    g_stub_ap_err = ESP_OK;
    g_stub_ap_rssi = -55;
    CHECK(wifi_mgr_get_rssi_dbm() == -55, "connected: rssi -55 dBm");

    g_stub_ap_rssi = -92;
    CHECK(wifi_mgr_get_rssi_dbm() == -92, "connected: rssi -92 dBm");
}

static void test_rssi_when_ap_info_fails(void) {
    g_stub_ap_err = ESP_FAIL;
    CHECK(wifi_mgr_get_rssi_dbm() == 0,
          "esp_wifi_sta_get_ap_info failure: rssi must be 0");
    g_stub_ap_err = ESP_OK;
}

/* 断连后的状态与 RSSI。
 *
 * 2026-10-05 修正：原用例断言"重试耗尽后进入 WIFI_MGR_FAILED"，
 * **它把缺陷当成了契约**。
 *
 * 实际缺陷：wifi_mgr 在 10 次快速重试（10x5s=50s）后永久放弃，
 * WIFI_MGR_FAILED 无人恢复，设备从此彻底脱网 —— 实测固件仍运行
 * （uptime 涨到 1600s+）但 ping 100% 丢包、ARP 无表项（L2 都不在），
 * 只能人工断电。对远程节点来说这比崩溃重启更糟（崩溃至少会重新入网）。
 *
 * 修复后：快速阶段用完转慢速（30s）**无限**重试，永不永久放弃。
 * 因此本用例现在断言的是"仍然在重试"，而不是"放弃了"。 */
static void test_rssi_after_disconnect(void) {
    /* 打满快速重试阶段（WIFI_FAST_RETRY_LIMIT = 10）并越过它。 */
    for (int i = 0; i < 12; i++) simulate_disconnect_reason(8);

    /* 关键断言：越过快速阶段后**不得**停在 WIFI_MGR_FAILED。
     * 修复前这里是 FAILED（永久放弃）；修复后应停在 CONNECTING（仍在重试）。 */
    CHECK(wifi_mgr_get_state() != WIFI_MGR_FAILED,
          "must NOT permanently give up after fast retries are exhausted "
          "(device became unreachable for 26 min on 2026-10-05: ping 100% loss, "
          "no ARP entry, firmware still running)");
    CHECK(wifi_mgr_get_state() == WIFI_MGR_CONNECTING,
          "state should be CONNECTING while slow retries continue");

    CHECK(wifi_mgr_get_rssi_dbm() == 0,
          "after disconnect: rssi must be 0");
}

/* 静默失联探针（2026-10-05）。
 *
 * 现场：设备 uptime 525s 正常运行，但服务端 ping 100% 丢包、ARP 无表项，
 * 串口里**0 条 WiFi 事件**（WIFI_EVENT_STA_DISCONNECTED 从未触发）。
 * 没有事件就没有重试，所以事件驱动的重连阶梯永远不会启动。
 *
 * 本用例构造"驱动自述 CONNECTED，但 esp_wifi_sta_get_ap_info() 持续失败"的
 * 静默失联场景，断言探针最终会强制重新关联。
 *
 * 关键点：修复前这个场景下什么都不会发生（没有事件可等），
 * 所以这条用例在修复前必然红。 */
static void test_liveness_detects_silent_link_loss(void) {
    /* 先进入"已连接"状态（复用既有的 GOT_IP 事件模拟）。 */
    simulate_got_ip();
    CHECK(wifi_mgr_is_connected(), "precondition: must be CONNECTED");

    const int connects_before = g_connect_calls;
    const int disconnects_before = g_disconnect_calls;

    /* 制造静默失联：驱动仍报 CONNECTED，但拿不到 AP 信息。 */
    g_stub_ap_err = ESP_FAIL;

    /* 推进假时钟，逐次调用探针。
     * 探针节流 5s、持续失败阈值 60s，所以需要推进 >60s 才会动手。
     * 这里推 15 次 x 6s = 90s，稳稳越过阈值。 */
    for (int i = 0; i < 15; i++) {
        g_test_time_us += 6 * 1000 * 1000; /* 每次推进 6s，越过 5s 节流 */
        /* app_network_ok=false 模拟"WiFi 自述已连接但应用层不通"。 */
        (void)wifi_mgr_check_liveness(false);
    }

    /* 断言：必须已经强制断连并重新关联。 */
    CHECK(g_disconnect_calls > disconnects_before,
          "silent link loss must trigger a forced esp_wifi_disconnect() "
          "(driver reported CONNECTED but AP info was unavailable; on 2026-10-05 this "
          "left the device unreachable for 26 min with zero WiFi events)");
    CHECK(g_connect_calls > connects_before,
          "silent link loss must trigger re-association (esp_wifi_connect())");

    g_stub_ap_err = ESP_OK;
}

/* 反向对照：链路正常时探针**不得**动它。
 * 防"把正确行为当缺陷修" —— 探针误判会打断正常连接。 */
static void test_liveness_does_not_disturb_healthy_link(void) {
    simulate_got_ip();
    const int disconnects_before = g_disconnect_calls;

    g_stub_ap_err = ESP_OK; /* AP 信息可得 => 链路正常 */
    for (int i = 0; i < 10; i++) {
        g_test_time_us += 6 * 1000 * 1000;
        (void)wifi_mgr_check_liveness(true); /* 应用层也健康 */
    }

    CHECK(g_disconnect_calls == disconnects_before,
          "healthy link must NOT be disturbed by the liveness probe");
    CHECK(wifi_mgr_is_connected(), "healthy link must stay CONNECTED");
}

int main(void) {
    wifi_mgr_init();
    CHECK(g_wifi_handler != NULL, "wifi event handler must be registered");

    test_rssi_when_disconnected_initially();
    test_rssi_when_connected();
    test_rssi_when_ap_info_fails();
    test_rssi_after_disconnect();
    test_liveness_detects_silent_link_loss();
    test_liveness_does_not_disturb_healthy_link();

    if (g_failures > 0) {
        fprintf(stderr, "\nwifi_mgr_rssi_tests: %d FAILURES\n", g_failures);
        return 1;
    }
    puts("wifi_mgr_rssi_tests: all tests passed");
    return 0;
}
