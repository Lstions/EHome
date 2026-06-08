/**
 * @file wifi_mgr.c
 * @brief WiFi Manager Implementation
 */

#include "wifi_mgr.h"
#include "esp_wifi.h"
#include "esp_event.h"
#include "esp_log.h"
#include "nvs_flash.h"
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "esp_http_server.h"
#include <string.h>

#define TAG "WIFI_MGR"

static httpd_handle_t s_http_server = NULL;

/* HTTP server handlers */
static esp_err_t prov_root_handler(httpd_req_t *req)
{
    const char *html = "<!DOCTYPE html><html><head><title>EHome Setup</title>"
                       "<style>body{font-family:Arial,sans-serif;max-width:400px;margin:50px auto;padding:20px}"
                       "h1{color:#333}input{width:100%;padding:10px;margin:10px 0;box-sizing:border-box}"
                       "button{width:100%;padding:12px;background:#4CAF50;color:#fff;border:none;cursor:pointer}"
                       "</style></head><body>"
                       "<h1>EHome WiFi Setup</h1>"
                       "<form method=\"POST\" action=\"/connect\">"
                       "<input name=\"ssid\" placeholder=\"WiFi SSID\" required>"
                       "<input name=\"password\" type=\"password\" placeholder=\"Password\">"
                       "<button type=\"submit\">Connect</button></form></body></html>";
    httpd_resp_send(req, html, strlen(html));
    return ESP_OK;
}

static esp_err_t prov_connect_handler(httpd_req_t *req)
{
    char buf[128] = {0};
    int ret = httpd_req_recv(req, buf, sizeof(buf) - 1);
    if (ret <= 0) {
        httpd_resp_send_500(req);
        return ESP_FAIL;
    }
    
    /* Simple form parsing: ssid=xxx&password=yyy */
    char ssid[32] = {0}, password[64] = {0};
    sscanf(buf, "ssid=%[^&]&password=%s", ssid, password);
    
    ESP_LOGI(TAG, "Provisioning: SSID=%s", ssid);
    
    /* Save credentials */
    wifi_mgr_save_credentials(ssid, password);
    
    const char *resp = "<html><body><h1>Connecting...</h1>"
                       "<p>Device will restart and connect to your WiFi.</p></body></html>";
    httpd_resp_send(req, resp, strlen(resp));
    
    /* Delay and restart */
    vTaskDelay(pdMS_TO_TICKS(2000));
    esp_restart();
    return ESP_OK;
}

void wifi_mgr_start_http_server(void)
{
    httpd_config_t config = HTTPD_DEFAULT_CONFIG();
    config.server_port = 80;
    
    if (httpd_start(&s_http_server, &config) == ESP_OK) {
        httpd_uri_t root_uri = {
            .uri = "/",
            .method = HTTP_GET,
            .handler = prov_root_handler,
        };
        httpd_uri_t connect_uri = {
            .uri = "/connect",
            .method = HTTP_POST,
            .handler = prov_connect_handler,
        };
        httpd_register_uri_handler(s_http_server, &root_uri);
        httpd_register_uri_handler(s_http_server, &connect_uri);
        ESP_LOGI(TAG, "HTTP server started on port 80");
    }
}

void wifi_mgr_stop_http_server(void)
{
    if (s_http_server) {
        httpd_stop(s_http_server);
        s_http_server = NULL;
    }
}

#define NVS_NAMESPACE "wifi_cfg"
#define KEY_SSID      "ssid"
#define KEY_PASSWORD  "password"

#define WIFI_CONNECT_TIMEOUT_MS 30000
#define WIFI_RECONNECT_DELAY_MS 5000

/* Event bits */
#define WIFI_CONNECTED_BIT    BIT0
#define WIFI_FAIL_BIT         BIT1

/* State */
static wifi_mgr_state_t s_state = WIFI_MGR_DISCONNECTED;
static EventGroupHandle_t s_wifi_event_group = NULL;
static wifi_mgr_state_cb_t s_state_cb = NULL;
static void *s_state_cb_ctx = NULL;
static int s_retry_count = 0;
static int s_max_retry = 10;
static bool s_auto_reconnect = true;
static bool s_provisioning_active = false;

/* Forward declarations */
static void wifi_event_handler(void *arg, esp_event_base_t event_base,
                               int32_t event_id, void *event_data);
static void set_state(wifi_mgr_state_t state);

/* === Public API === */

void wifi_mgr_init(void)
{
    ESP_LOGI(TAG, "Initializing WiFi manager...");

    // Create event group
    s_wifi_event_group = xEventGroupCreate();

    // Initialize TCP/IP stack
    ESP_ERROR_CHECK(esp_netif_init());
    ESP_ERROR_CHECK(esp_event_loop_create_default());
    esp_netif_create_default_wifi_sta();

    // Initialize WiFi with default config
    wifi_init_config_t cfg = WIFI_INIT_CONFIG_DEFAULT();
    ESP_ERROR_CHECK(esp_wifi_init(&cfg));

    // Register event handlers
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
        // Fallback to sdkconfig defaults (CONFIG_COLLECTOR_WIFI_SSID / CONFIG_COLLECTOR_WIFI_PASSWORD)
        const char *def_ssid = CONFIG_COLLECTOR_WIFI_SSID;
        const char *def_pwd = CONFIG_COLLECTOR_WIFI_PASSWORD;
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

    // Wait for connection
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

bool wifi_mgr_save_credentials(const char *ssid, const char *password)
{
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

    err = nvs_set_str(handle, KEY_PASSWORD, password);
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

void wifi_mgr_start_provisioning(void)
{
    if (s_provisioning_active) return;
    s_provisioning_active = true;
    
    ESP_LOGI(TAG, "=== Provisioning Mode ===");
    ESP_LOGI(TAG, "Connect to AP: EHome-Setup-XXXX");
    ESP_LOGI(TAG, "Visit: http://192.168.4.1");
    ESP_LOGI(TAG, "Or use serial: AT+WIFI=ssid,password");
    ESP_LOGI(TAG, "==========================");
    
    // Start SoftAP for provisioning
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
    
    /* Start HTTP server for provisioning */
    wifi_mgr_start_http_server();
    
    set_state(WIFI_MGR_DISCONNECTED);
}

void wifi_mgr_stop_provisioning(void)
{
    s_provisioning_active = false;
    ESP_LOGI(TAG, "Provisioning stopped");
}

/* === Internal === */

static void set_state(wifi_mgr_state_t state)
{
    if (s_state != state) {
        s_state = state;
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
            wifi_event_sta_disconnected_t *event = (wifi_event_sta_disconnected_t *)event_data;
            ESP_LOGW(TAG, "Disconnected from AP, reason=%d", event->reason);
            
            xEventGroupClearBits(s_wifi_event_group, WIFI_CONNECTED_BIT);
            
            if (s_auto_reconnect && s_retry_count < s_max_retry) {
                s_retry_count++;
                ESP_LOGI(TAG, "Reconnecting... attempt %d/%d", s_retry_count, s_max_retry);
                set_state(WIFI_MGR_CONNECTING);
                vTaskDelay(pdMS_TO_TICKS(WIFI_RECONNECT_DELAY_MS));
                esp_wifi_connect();
            } else {
                ESP_LOGE(TAG, "Connection failed after %d attempts", s_retry_count);
                xEventGroupSetBits(s_wifi_event_group, WIFI_FAIL_BIT);
                set_state(WIFI_MGR_FAILED);
            }
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
