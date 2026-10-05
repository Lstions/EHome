/**
 * @file wifi_mgr.c
 * @brief WiFi Manager - STA mode with auto-reconnect and NVS persistence.
 *
 * Provisioning (SoftAP + HTTP captive portal) lives in wifi_provisioning.c.
 */

#include "wifi_mgr.h"
#include "wifi_provisioning.h"
#include "esp_wifi.h"
#include "esp_event.h"
#include "esp_log.h"
#include "esp_netif.h"
#include "nvs_flash.h"
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include <string.h>

#define TAG "WIFI_MGR"

#define NVS_NAMESPACE "wifi_cfg"
#define KEY_SSID      "ssid"
#define KEY_PASSWORD  "password"

#define WIFI_CONNECT_TIMEOUT_MS  30000
#define WIFI_RECONNECT_DELAY_MS  5000

/* 快速重试阶段：连续失败这么多次后转入"永久慢速重试"。
 *
 * 2026-10-05 实机事故（比 TWDT 那个更严重）：原实现是
 *
 *     if (s_auto_reconnect && s_retry_count < s_max_retry) { ...esp_wifi_connect(); }
 *     else { set_state(WIFI_MGR_FAILED); }   // <- 永久放弃，再没有任何重试
 *
 * 即 10 次 x 5s = **50 秒**后设备**永久放弃 WiFi**。而 WIFI_MGR_FAILED 的处理
 * 只做两件事：点红灯、唤醒 MQTT supervisor（main/app_callbacks.c:548）——
 * 没有任何地方会再调用 esp_wifi_connect()。
 *
 * 实测后果：设备固件仍在运行（串口 uptime 一路涨到 1600s+，UART/SPI/I2C 采样
 * 全部正常），但**彻底脱离网络**：
 *   - 服务端 ping 100% 丢包；
 *   - ARP 表里连一条表项都没有（L2 都不在）；
 *   - EMQX 从来看不到连接尝试（TCP 根本没发起）；
 *   - MQTT 侧只表现为反复的 esp-tls select() timeout + 重连，极具误导性。
 *
 * 这是"设备看起来还活着、其实已经不可达"的典型静默失联：只能靠人工断电恢复。
 * 对远程部署的节点来说，这比崩溃重启更糟 —— 崩溃至少会重启并重新入网。
 *
 * 修复：**永不永久放弃**。快速重试用完后退到慢速无限重试，让设备在网络恢复
 * 或 AP 重启后能自行回来。 */
#define WIFI_FAST_RETRY_LIMIT    10
#define WIFI_SLOW_RETRY_DELAY_MS 30000

/* Event bits */
#define WIFI_CONNECTED_BIT    BIT0
#define WIFI_FAIL_BIT         BIT1

/* State */
static wifi_mgr_state_t s_state = WIFI_MGR_DISCONNECTED;
static EventGroupHandle_t s_wifi_event_group = NULL;
static wifi_mgr_state_cb_t s_state_cb = NULL;
static void *s_state_cb_ctx = NULL;
static int s_retry_count = 0;
static bool s_auto_reconnect = true;

/* Forward declarations */
static void wifi_event_handler(void *arg, esp_event_base_t event_base,
                               int32_t event_id, void *event_data);
static void set_state(wifi_mgr_state_t state);

/* === Public API === */

void wifi_mgr_init(void)
{
    ESP_LOGI(TAG, "Initializing WiFi manager...");

    s_wifi_event_group = xEventGroupCreate();

    ESP_ERROR_CHECK(esp_netif_init());
    ESP_ERROR_CHECK(esp_event_loop_create_default());
    esp_netif_create_default_wifi_sta();

    wifi_init_config_t cfg = WIFI_INIT_CONFIG_DEFAULT();
    ESP_ERROR_CHECK(esp_wifi_init(&cfg));

    ESP_ERROR_CHECK(esp_event_handler_instance_register(
        WIFI_EVENT, ESP_EVENT_ANY_ID, &wifi_event_handler, NULL, NULL));
    ESP_ERROR_CHECK(esp_event_handler_instance_register(
        IP_EVENT, IP_EVENT_STA_GOT_IP, &wifi_event_handler, NULL, NULL));

    ESP_LOGI(TAG, "WiFi manager initialized");
}

void wifi_mgr_start(void)
{
    char ssid[32] = {0};
    char password[64] = {0};

    if (!wifi_mgr_load_credentials(ssid, sizeof(ssid), password, sizeof(password))) {
        /* Fallback to sdkconfig defaults. */
        const char *def_ssid = CONFIG_COLLECTOR_WIFI_SSID;
        const char *def_pwd  = CONFIG_COLLECTOR_WIFI_PASSWORD;
        if (def_ssid[0] != '\0') {
            ESP_LOGI(TAG, "Using sdkconfig defaults: SSID=%s", def_ssid);
            strlcpy(ssid, def_ssid, sizeof(ssid));
            strlcpy(password, def_pwd, sizeof(password));
            wifi_mgr_save_credentials(ssid, password);
        } else {
            ESP_LOGW(TAG, "No WiFi credentials, starting provisioning...");
            wifi_mgr_start_provisioning();
            return;
        }
    }

    ESP_LOGI(TAG, "Connecting to SSID: %s", ssid);

    wifi_config_t wifi_config = {0};
    strlcpy((char *)wifi_config.sta.ssid, ssid, sizeof(wifi_config.sta.ssid));
    strlcpy((char *)wifi_config.sta.password, password, sizeof(wifi_config.sta.password));
    wifi_config.sta.threshold.authmode = WIFI_AUTH_WPA2_PSK;

    ESP_ERROR_CHECK(esp_wifi_set_mode(WIFI_MODE_STA));
    ESP_ERROR_CHECK(esp_wifi_set_config(WIFI_IF_STA, &wifi_config));
    ESP_ERROR_CHECK(esp_wifi_start());

    set_state(WIFI_MGR_CONNECTING);

    EventBits_t bits = xEventGroupWaitBits(
        s_wifi_event_group, WIFI_CONNECTED_BIT | WIFI_FAIL_BIT,
        pdFALSE, pdFALSE, pdMS_TO_TICKS(WIFI_CONNECT_TIMEOUT_MS));

    if (bits & WIFI_CONNECTED_BIT) {
        ESP_LOGI(TAG, "Connected to AP");
    } else if (bits & WIFI_FAIL_BIT) {
        ESP_LOGW(TAG, "Failed to connect to AP");
        set_state(WIFI_MGR_FAILED);
    } else {
        ESP_LOGW(TAG, "Connection timeout");
        set_state(WIFI_MGR_FAILED);
    }
}

void wifi_mgr_stop(void)
{
    esp_wifi_stop();
    set_state(WIFI_MGR_DISCONNECTED);
}

wifi_mgr_state_t wifi_mgr_get_state(void)
{
    return s_state;
}

bool wifi_mgr_is_connected(void)
{
    return s_state == WIFI_MGR_CONNECTED;
}

int wifi_mgr_get_rssi_dbm(void)
{
    if (s_state != WIFI_MGR_CONNECTED) {
        return 0;
    }

    wifi_ap_record_t ap_info = {0};
    if (esp_wifi_sta_get_ap_info(&ap_info) != ESP_OK) {
        return 0;
    }
    return ap_info.rssi;
}

bool wifi_mgr_save_credentials(const char *ssid, const char *password)
{
    if (!ssid) return false;

    /* Reject oversize inputs before touching NVS. */
    if (strlen(ssid) > WIFI_PROVISION_SSID_MAX) return false;
    if (password && strlen(password) > WIFI_PROVISION_PASSWORD_MAX) return false;

    nvs_handle_t handle;
    esp_err_t err = nvs_open(NVS_NAMESPACE, NVS_READWRITE, &handle);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to open NVS: %s", esp_err_to_name(err));
        return false;
    }

    err = nvs_set_str(handle, KEY_SSID, ssid);
    if (err != ESP_OK) {
        nvs_close(handle);
        return false;
    }

    err = nvs_set_str(handle, KEY_PASSWORD, password ? password : "");
    if (err != ESP_OK) {
        nvs_close(handle);
        return false;
    }

    err = nvs_commit(handle);
    nvs_close(handle);

    ESP_LOGI(TAG, "WiFi credentials saved");
    return err == ESP_OK;
}

bool wifi_mgr_load_credentials(char *ssid, size_t ssid_len, char *password, size_t pwd_len)
{
    if (!ssid || !password || ssid_len == 0 || pwd_len == 0) return false;

    nvs_handle_t handle;
    esp_err_t err = nvs_open(NVS_NAMESPACE, NVS_READONLY, &handle);
    if (err != ESP_OK) {
        return false;
    }

    size_t len = ssid_len;
    err = nvs_get_str(handle, KEY_SSID, ssid, &len);
    if (err != ESP_OK) {
        nvs_close(handle);
        return false;
    }

    len = pwd_len;
    err = nvs_get_str(handle, KEY_PASSWORD, password, &len);
    nvs_close(handle);

    return err == ESP_OK;
}

void wifi_mgr_clear_credentials(void)
{
    nvs_handle_t handle;
    esp_err_t err = nvs_open(NVS_NAMESPACE, NVS_READWRITE, &handle);
    if (err == ESP_OK) {
        nvs_erase_all(handle);
        nvs_commit(handle);
        nvs_close(handle);
    }
    ESP_LOGI(TAG, "WiFi credentials cleared");
}

bool wifi_mgr_has_credentials(void)
{
    char ssid[32], password[64];
    return wifi_mgr_load_credentials(ssid, sizeof(ssid), password, sizeof(password));
}

void wifi_mgr_register_state_cb(wifi_mgr_state_cb_t cb, void *ctx)
{
    s_state_cb = cb;
    s_state_cb_ctx = ctx;
}

/* === Provisioning wrappers (implementation in wifi_provisioning.c) === */

void wifi_mgr_start_provisioning(void)
{
    if (wifi_provisioning_is_active()) return;

    ESP_LOGI(TAG, "=== Provisioning Mode ===");
    ESP_LOGI(TAG, "Connect to AP: EHome-Setup");
    ESP_LOGI(TAG, "Visit: http://192.168.4.1");
    ESP_LOGI(TAG, "Portal auto-closes after %d minutes",
             WIFI_PROVISION_TIMEOUT_MS / (60 * 1000));
    ESP_LOGI(TAG, "==========================");

    /* Start SoftAP for provisioning. */
    esp_netif_t *ap_netif = esp_netif_create_default_wifi_ap();
    (void)ap_netif;

    wifi_config_t ap_config = {
        .ap = {
            .ssid = "EHome-Setup",
            .ssid_len = 0,
            .channel = 1,
            .password = "setup123",
            .max_connection = 4,
            .authmode = WIFI_AUTH_WPA2_PSK,
        },
    };

    ESP_ERROR_CHECK(esp_wifi_set_mode(WIFI_MODE_APSTA));
    ESP_ERROR_CHECK(esp_wifi_set_config(WIFI_IF_AP, &ap_config));
    ESP_ERROR_CHECK(esp_wifi_start());

    wifi_provisioning_set_active(true);

    /* Start HTTP server + 30-minute timeout timer. */
    wifi_provisioning_start_http_server();

    set_state(WIFI_MGR_DISCONNECTED);
}

void wifi_mgr_stop_provisioning(void)
{
    wifi_provisioning_stop_http_server();
    wifi_provisioning_set_active(false);
    ESP_LOGI(TAG, "Provisioning stopped");
}

/* === Internal === */

static void set_state(wifi_mgr_state_t state)
{
    wifi_mgr_state_t old_state = s_state;
    if (old_state != state) {
        s_state = state;
        ESP_LOGD(TAG, "State change: %d -> %d", old_state, state);
        if (s_state_cb) {
            s_state_cb(state, s_state_cb_ctx);
        }
    }
}

static void wifi_event_handler(void *arg, esp_event_base_t event_base,
                               int32_t event_id, void *event_data)
{
    if (event_base == WIFI_EVENT) {
        switch (event_id) {
        case WIFI_EVENT_STA_START:
            ESP_LOGI(TAG, "WiFi STA started");
            esp_wifi_connect();
            break;

        case WIFI_EVENT_STA_DISCONNECTED: {
            wifi_event_sta_disconnected_t *event =
                (wifi_event_sta_disconnected_t *)event_data;
            ESP_LOGW(TAG, "Disconnected from AP, reason=%d", event->reason);

            xEventGroupClearBits(s_wifi_event_group, WIFI_CONNECTED_BIT);

            /* 永不永久放弃重连。
             *
             * 原实现在 s_retry_count 达到 10 次后进入 WIFI_MGR_FAILED 并**停止
             * 一切重试**，而该状态无人恢复 —— 设备从此永久脱网，只能人工断电。
             * 实测：固件继续运行（uptime 涨到 1600s+），但 ping 100% 丢包、
             * ARP 无表项，即 L2 都不在。详见 WIFI_FAST_RETRY_LIMIT 处的说明。
             *
             * 现在：快速阶段（5s 间隔）用完后转入慢速阶段（30s 间隔）**无限**重试。
             * 这样网络或 AP 恢复后设备能自行回来，代价只是 30s 的探测间隔。
             *
             * 注意：这里仍然会置一次 WIFI_FAIL_BIT / 上报 WIFI_MGR_FAILED ——
             * 那是给上层"当前不可用"的信号（红灯、唤醒 MQTT supervisor），
             * 但**不再意味着放弃**。状态机会在下次重试时回到 CONNECTING。 */
            if (!s_auto_reconnect) {
                ESP_LOGE(TAG, "Auto reconnect disabled; not retrying");
                xEventGroupSetBits(s_wifi_event_group, WIFI_FAIL_BIT);
                set_state(WIFI_MGR_FAILED);
                break;
            }

            s_retry_count++;
            bool slow_phase = (s_retry_count > WIFI_FAST_RETRY_LIMIT);
            uint32_t delay_ms = slow_phase ? WIFI_SLOW_RETRY_DELAY_MS
                                           : WIFI_RECONNECT_DELAY_MS;
            if (slow_phase && s_retry_count == WIFI_FAST_RETRY_LIMIT + 1) {
                ESP_LOGW(TAG, "Fast retries exhausted (%d); switching to slow retry every %d ms",
                         WIFI_FAST_RETRY_LIMIT, WIFI_SLOW_RETRY_DELAY_MS);
            }
            ESP_LOGI(TAG, "Reconnecting... attempt %d (%s, next in %u ms)",
                     s_retry_count, slow_phase ? "slow" : "fast",
                     (unsigned)delay_ms);
            set_state(WIFI_MGR_CONNECTING);
            vTaskDelay(pdMS_TO_TICKS(delay_ms));
            esp_wifi_connect();
            break;
        }

        case WIFI_EVENT_STA_CONNECTED:
            ESP_LOGI(TAG, "WiFi STA connected to AP");
            s_retry_count = 0;
            break;

        default:
            break;
        }
    } else if (event_base == IP_EVENT) {
        if (event_id == IP_EVENT_STA_GOT_IP) {
            ip_event_got_ip_t *event = (ip_event_got_ip_t *)event_data;
            ESP_LOGI(TAG, "Got IP: " IPSTR, IP2STR(&event->ip_info.ip));
            xEventGroupSetBits(s_wifi_event_group, WIFI_CONNECTED_BIT);
            set_state(WIFI_MGR_CONNECTED);
            s_retry_count = 0;
        }
    }
}
