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
esp_err_t esp_wifi_connect(void) { return ESP_OK; }

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

static void test_rssi_after_disconnect(void) {
    /* Exhaust reconnect attempts (s_max_retry = 10) */
    for (int i = 0; i < 11; i++) simulate_disconnect_reason(8);
    CHECK(wifi_mgr_get_state() == WIFI_MGR_FAILED,
          "state should be FAILED after retries exhausted");
    CHECK(wifi_mgr_get_rssi_dbm() == 0,
          "after disconnect: rssi must be 0");
}

int main(void) {
    wifi_mgr_init();
    CHECK(g_wifi_handler != NULL, "wifi event handler must be registered");

    test_rssi_when_disconnected_initially();
    test_rssi_when_connected();
    test_rssi_when_ap_info_fails();
    test_rssi_after_disconnect();

    if (g_failures > 0) {
        fprintf(stderr, "\nwifi_mgr_rssi_tests: %d FAILURES\n", g_failures);
        return 1;
    }
    puts("wifi_mgr_rssi_tests: all tests passed");
    return 0;
}
