/**
 * @file wifi_mgr.c
 * @brief WiFi Manager - STA mode with auto-reconnect and NVS persistence.
 *
 * Provisioning (SoftAP + HTTP captive portal) lives in wifi_provisioning.c.
 */

#include "wifi_mgr.h"
#include "wifi_provisioning.h"
#include "esp_wifi.h"
#include "esp_timer.h"
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

/* 主动链路探针（2026-10-05 第二版）。
 *
 * 第一版用 `esp_wifi_sta_get_ap_info()` 是否成功作为判据 —— **那是错的**，
 * 因为它读的是**驱动自己缓存的 AP 记录**，而驱动状态正是失效时不可信的那个东西：
 * 驱动认为"还连着"，于是 get_ap_info() 照常成功返回陈旧数据，探针什么也发现不了。
 * 实测印证：加了第一版探针后设备仍然静默失联，串口里 0 条探针日志。
 *
 * 正确判据必须来自**链路之外**的、应用层可观测的证据。这里用两个信号：
 *
 *   (a) `esp_wifi_sta_get_ap_info()` —— 仍保留，用于捕捉驱动层掉线；
 *   (b) `app_network_ok` —— 调用方传入"应用层网络是否真的通"
 *       （main.c 传 mqtt_client_is_connected_impl()）。
 *
 * (b) 才是本次事故的克星：WiFi 自述 CONNECTED、MQTT 却连不上、
 * 服务端 ping 100% 丢包 —— 这个组合只有"链路实际已断"能解释。
 * 只要 (a) 或 (b) 任一持续失败超过阈值，就强制重新关联。
 *
 * 取舍：服务端长时间宕机时本探针也会周期性触发重新关联。这是可接受的 ——
 * 重新关联是无害的重连尝试且有 60s 冷却；而"链路静默死掉后永不恢复、
 * 只能人工断电"是不可接受的。
 */
#define WIFI_LIVENESS_PROBE_INTERVAL_MS 5000
/* 持续失败多久才动手。取 60s：必须显著大于正常启动时 MQTT 建连时间（约 20s），
 * 否则每次启动都会误触发一次重新关联。 */
#define WIFI_LIVENESS_FAIL_AFTER_MS     60000
#define WIFI_LIVENESS_RECOVER_COOLDOWN_MS 60000
/* 诊断日志间隔：让"探针到底看到了什么"在串口可见，便于现场定性。 */
#define WIFI_LIVENESS_DIAG_INTERVAL_MS  30000

static int64_t s_liveness_first_fail_us = 0;
static int64_t s_last_liveness_probe_us = 0;
static int64_t s_last_liveness_recover_us = 0;
/* 恢复动作的退避间隔（毫秒）。每次强制重新关联后翻倍，上限 15 分钟；
 * 链路恢复正常时重置回 60s。
 *
 * 为什么需要它：若网络/AP 长时间真的不可达，固定 60s 的强制断连会变成
 * 持续的 churn —— 2026-10-05 实测过这个自持故障（探针每 60s 拆一次正常 WiFi，
 * 导致 MQTT 永远建不起来）。退避让"持续不可达"时的重试代价可接受，
 * 同时保留"短暂抖动后自愈"的能力。 */
static int64_t s_liveness_backoff_ms = 60000;
#define WIFI_LIVENESS_BACKOFF_MAX_MS (15 * 60 * 1000)
static int64_t s_last_liveness_diag_us = 0;

bool wifi_mgr_check_liveness(bool app_network_ok)
{
    if (!s_auto_reconnect) return true;

    /* 只在"自述已连接"时才做这个检查。
     * 若状态不是 CONNECTED，说明事件路径已经在处理（重试阶梯），无需叠加。 */
    if (s_state != WIFI_MGR_CONNECTED) {
        s_liveness_first_fail_us = 0;
        return true;
    }

    const int64_t now = esp_timer_get_time();
    if (now - s_last_liveness_probe_us <
        (int64_t)WIFI_LIVENESS_PROBE_INTERVAL_MS * 1000) {
        return true;
    }
    s_last_liveness_probe_us = now;

    /* 信号 (a)：驱动层是否还能给出 AP 信息。 */
    wifi_ap_record_t ap_info = {0};
    const bool driver_ok = (esp_wifi_sta_get_ap_info(&ap_info) == ESP_OK);

    /* **触发条件只看驱动层信号 `driver_ok`，不看 `app_network_ok`。**
     *
     * 这是 2026-10-05 实测踩到的一个自伤缺陷，记录下来：
     *
     * 第一版把 MQTT 连接状态（app_ok）也当成 WiFi 存活的判据，于是在"MQTT 因
     * 与 WiFi 无关的原因连不上"时（broker 侧问题、鉴权、MQTT 客户端自身故障），
     * 探针会误判成"WiFi 链路静默失效"，**每 60s 把一条完全正常的 WiFi 拆掉重连**。
     *
     * 现场证据（当时 ping 设备 1/4 通、59ms、broker 1883 正常、无鉴权失败，
     * 即网络本身是好的）：
     *
     *     WIFI_MGR: Liveness degraded for 42001 ms: wifi_state=CONNECTED driver_ok=1 app_ok=0 rssi=-48
     *     WIFI_MGR: Got IP: 192.168.110.250
     *     WIFI_MGR: Liveness suspect: driver_ok=1 app_ok=0 (starting 60000 ms window)
     *
     * 危害是自持的：断连 churn 会让 MQTT 永远无法稳定建立连接，于是 app_ok 永远为 0，
     * 探针就一直拆 —— **探针本身成了故障源**。
     *
     * 教训：一个**恢复动作**的触发判据必须是"它要修的那个东西确实坏了"。
     * MQTT 连不上 ≠ WiFi 坏了；用前者触发后者，就是在用一个观测去修另一个系统。
     *
     * 因此 app_ok 现在**只用于日志**（区分"驱动也不认"还是"只有应用层不通"），
     * 不参与触发。若将来要覆盖"驱动自述正常但 L3 确实不通"的情形，
     * 正确的判据是**独立的 L3 探测**（如 ping 网关），而不是复用上层协议状态。 */
    /* 触发判据：驱动层与**应用层**任一不健康即计入失败。
     *
     * 这里改过两版，两版的错误都记下来：
     *
     * 第一版：`driver_ok && app_ok` 才算健康 —— **过于激进**。MQTT 因与 WiFi 无关的
     *   原因连不上时，探针会把一条完全正常的 WiFi 每 60s 拆一次；churn 又让 MQTT
     *   永远建不起来，形成自持故障（现场：ping 通、broker 正常、无鉴权失败）。
     *
     * 第二版：只看 `driver_ok` —— **过于被动，等于没修**。`esp_wifi_sta_get_ap_info()`
     *   读的是驱动缓存的 AP 记录；链路静默死掉时驱动仍返回成功，于是探针每次都在
     *   第一行 return true，**永远不触发**。实机证据：设备离线（ping 100% 丢包）
     *   而 150 秒抓包里 0 条 WIFI_MGR 日志。这正是本次要修的盲区本身。
     *
     * 现在的判据：两者任一为假即视为"可疑"，持续 BOOT 窗口后强制重新关联。
     * 为了不重蹈第一版的 churn，恢复动作带**指数退避**（见下面的 cooldown），
     * 使"网络真的不通"时重试间隔从 60s 逐步拉长到 15 分钟，而不是死循环。
     *
     * 注意 app_ok 在**启动初期**必然为假（MQTT 还没建连）。所以窗口取 60s，
     * 显著大于正常建连时间（约 20s），避免每次启动都误触发一次重新关联。 */
    if (driver_ok && app_network_ok) {
        if (s_liveness_first_fail_us != 0) {
            ESP_LOGI(TAG, "Liveness recovered (driver_ok=%d app_ok=%d)",
                     (int)driver_ok, (int)app_network_ok);
        }
        s_liveness_first_fail_us = 0;
        /* 链路恢复 -> 把退避重置回初始值，下一次故障重新从 60s 起算。 */
        s_liveness_backoff_ms = WIFI_LIVENESS_RECOVER_COOLDOWN_MS;
        return true;
    }

    if (s_liveness_first_fail_us == 0) {
        s_liveness_first_fail_us = now;
        ESP_LOGW(TAG, "Liveness suspect: driver_ok=%d app_ok=%d (starting %d ms window)",
                 (int)driver_ok, (int)app_network_ok, WIFI_LIVENESS_FAIL_AFTER_MS);
    }

    const int64_t failing_ms = (now - s_liveness_first_fail_us) / 1000;

    /* 周期性诊断：把"WiFi 自述已连接、但实际不通"暴露到串口。 */
    if (now - s_last_liveness_diag_us >=
        (int64_t)WIFI_LIVENESS_DIAG_INTERVAL_MS * 1000) {
        s_last_liveness_diag_us = now;
        ESP_LOGW(TAG, "Liveness degraded for %lld ms: wifi_state=CONNECTED driver_ok=%d "
                      "app_ok=%d rssi=%d",
                 (long long)failing_ms, (int)driver_ok, (int)app_network_ok,
                 driver_ok ? ap_info.rssi : 0);
    }

    if (failing_ms < (int64_t)WIFI_LIVENESS_FAIL_AFTER_MS) return true;

    if (now - s_last_liveness_recover_us < s_liveness_backoff_ms * 1000) {
        return true;
    }
    s_last_liveness_recover_us = now;
    s_liveness_first_fail_us = 0;
    /* 指数退避：下一次若仍不恢复，等待时间翻倍（上限 15 分钟）。 */
    s_liveness_backoff_ms *= 2;
    if (s_liveness_backoff_ms > WIFI_LIVENESS_BACKOFF_MAX_MS) {
        s_liveness_backoff_ms = WIFI_LIVENESS_BACKOFF_MAX_MS;
    }

    /* 静默失联：驱动以为连着，实际 L2 已经不在。强制断开再关联。
     *
     * esp_wifi_disconnect() 会触发 WIFI_EVENT_STA_DISCONNECTED，
     * 从而走上事件路径的重连阶梯；这里再显式 connect 一次，
     * 保证即使事件没有立刻到达也能发起关联。 */
    ESP_LOGE(TAG, "WiFi link is silently dead (wifi_state=CONNECTED driver_ok=%d app_ok=%d "
                  "for %lld ms); forcing re-association",
             (int)driver_ok, (int)app_network_ok, (long long)failing_ms);
    xEventGroupClearBits(s_wifi_event_group, WIFI_CONNECTED_BIT);
    esp_wifi_disconnect();
    s_retry_count = 0;
    set_state(WIFI_MGR_CONNECTING);
    vTaskDelay(pdMS_TO_TICKS(200));
    esp_wifi_connect();
    return false;
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
