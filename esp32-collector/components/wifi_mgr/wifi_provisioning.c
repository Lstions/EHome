/**
 * @file wifi_provisioning.c
 * @brief WiFi Provisioning - HTTP captive portal with security hardening
 *
 * Features:
 * - URL decoding for form data
 * - Input validation with length limits
 * - 30-minute auto-timeout for security
 * - Safe sscanf parsing with width specifiers
 */

#include "wifi_mgr.h"
#include "wifi_provisioning.h"
#include "esp_wifi.h"
#include "esp_log.h"
#include "esp_http_server.h"
#include "esp_netif.h"
#include "freertos/FreeRTOS.h"
#include "freertos/timers.h"
#include <string.h>
#include <ctype.h>
#include <stdlib.h>

#define TAG "WIFI_PROV"

/* Provisioning timeout: 30 minutes */
#define PROVISION_TIMEOUT_MINUTES  30
#define PROVISION_TIMEOUT_MS       (PROVISION_TIMEOUT_MINUTES * 60 * 1000)

static httpd_handle_t s_http_server = NULL;
static TimerHandle_t s_provision_timer = NULL;
static bool s_provisioning_active = false;

/* Forward declarations */
static void provision_timeout_callback(TimerHandle_t timer);
static int url_decode(char *dst, size_t dst_len, const char *src, size_t src_len);
static bool parse_form_data(const char *form_data, char *ssid, size_t ssid_len,
                           char *password, size_t password_len);

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
                       "<button type=\"submit\">Connect</button></form>"
                       "<p style=\"color:#666;font-size:12px;\">Note: This portal will close in 30 minutes.</p>"
                       "</body></html>";
    httpd_resp_send(req, html, strlen(html));
    return ESP_OK;
}

static esp_err_t prov_connect_handler(httpd_req_t *req)
{
    char buf[256] = {0};
    char ssid[33] = {0};      /* 32 chars + NUL */
    char password[65] = {0};  /* 64 chars + NUL */
    
    /* Receive form data with size limit */
    int ret = httpd_req_recv(req, buf, sizeof(buf) - 1);
    if (ret <= 0) {
        ESP_LOGE(TAG, "Failed to receive form data");
        httpd_resp_send_500(req);
        return ESP_FAIL;
    }
    buf[ret] = '\0';  /* Ensure NUL termination */
    
    ESP_LOGI(TAG, "Received form data (length=%d)", ret);
    
    /* Parse and validate form data with URL decoding */
    if (!parse_form_data(buf, ssid, sizeof(ssid), password, sizeof(password))) {
        ESP_LOGE(TAG, "Failed to parse form data");
        const char *resp = "<html><body><h1>Error</h1>"
                          "<p>Invalid SSID or password format.</p>"
                          "<a href=\"/\">Go back</a></body></html>";
        httpd_resp_send(req, resp, strlen(resp));
        return ESP_FAIL;
    }
    
    /* Validate SSID is not empty */
    if (strlen(ssid) == 0) {
        ESP_LOGE(TAG, "SSID is empty");
        const char *resp = "<html><body><h1>Error</h1>"
                          "<p>SSID cannot be empty.</p>"
                          "<a href=\"/\">Go back</a></body></html>";
        httpd_resp_send(req, resp, strlen(resp));
        return ESP_FAIL;
    }
    
    ESP_LOGI(TAG, "Provisioning: SSID=%s (len=%d)", ssid, (int)strlen(ssid));
    
    /* Save credentials via wifi_mgr API */
    wifi_mgr_save_credentials(ssid, password);
    
    const char *resp = "<html><body><h1>Connecting...</h1>"
                       "<p>Device will restart and connect to your WiFi.</p>"
                       "<p>You can close this window.</p></body></html>";
    httpd_resp_send(req, resp, strlen(resp));
    
    /* Stop provisioning timer and cleanup */
    if (s_provision_timer) {
        xTimerStop(s_provision_timer, 0);
    }
    
    /* Delay and restart */
    vTaskDelay(pdMS_TO_TICKS(2000));
    esp_restart();
    
    return ESP_OK;
}

/**
 * @brief URL decode (percent-decode) a string
 * 
 * @param dst       Destination buffer
 * @param dst_len   Size of destination buffer
 * @param src       Source URL-encoded string
 * @param src_len   Length of source string
 * @return          Number of bytes written to dst (excluding NUL), or -1 on error
 */
static int url_decode(char *dst, size_t dst_len, const char *src, size_t src_len)
{
    if (!dst || !src || dst_len == 0) {
        return -1;
    }
    
    size_t i = 0;
    size_t j = 0;
    
    while (i < src_len && j < dst_len - 1) {
        if (src[i] == '%') {
            /* Percent-encoded character - must have 2 hex digits */
            if (i + 2 >= src_len) {
                /* Incomplete percent encoding */
                return -1;
            }
            
            char hex[3] = {src[i + 1], src[i + 2], '\0'};
            char *endptr;
            long val = strtol(hex, &endptr, 16);
            
            if (endptr != hex + 2) {
                /* Invalid hex */
                return -1;
            }
            
            dst[j++] = (char)val;
            i += 3;
        } else if (src[i] == '+') {
            /* Plus sign represents space */
            dst[j++] = ' ';
            i++;
        } else {
            /* Regular character */
            dst[j++] = src[i++];
        }
    }
    
    dst[j] = '\0';
    return (int)j;
}

/**
 * @brief Parse URL-encoded form data (application/x-www-form-urlencoded)
 * 
 * Extracts ssid and password fields with proper URL decoding and length validation.
 * 
 * @param form_data     Raw form data string (e.g., "ssid=MyNetwork&password=secret123")
 * @param ssid          Output buffer for SSID
 * @param ssid_len      Size of SSID buffer
 * @param password      Output buffer for password
 * @param password_len  Size of password buffer
 * @return              true on success, false on parse error
 */
static bool parse_form_data(const char *form_data, char *ssid, size_t ssid_len,
                           char *password, size_t password_len)
{
    if (!form_data || !ssid || !password) {
        return false;
    }
    
    /* Initialize output buffers */
    ssid[0] = '\0';
    password[0] = '\0';
    
    /* Find ssid= field */
    const char *ssid_start = strstr(form_data, "ssid=");
    if (!ssid_start) {
        ESP_LOGE(TAG, "Missing 'ssid=' in form data");
        return false;
    }
    ssid_start += 5;  /* Skip "ssid=" */
    
    /* Find end of ssid value (next & or end of string) */
    const char *ssid_end = strchr(ssid_start, '&');
    if (!ssid_end) {
        ssid_end = ssid_start + strlen(ssid_start);
    }
    
    /* URL decode SSID */
    size_t ssid_raw_len = ssid_end - ssid_start;
    if (ssid_raw_len >= 128) {
        ESP_LOGE(TAG, "SSID too long in raw form");
        return false;
    }
    
    int decoded_len = url_decode(ssid, ssid_len, ssid_start, ssid_raw_len);
    if (decoded_len < 0) {
        ESP_LOGE(TAG, "Failed to decode SSID");
        return false;
    }
    
    /* Find password= field (optional) */
    const char *pwd_start = strstr(form_data, "password=");
    if (pwd_start) {
        pwd_start += 9;  /* Skip "password=" */
        
        /* Find end of password value (next & or end of string) */
        const char *pwd_end = strchr(pwd_start, '&');
        if (!pwd_end) {
            pwd_end = pwd_start + strlen(pwd_start);
        }
        
        /* URL decode password */
        size_t pwd_raw_len = pwd_end - pwd_start;
        if (pwd_raw_len >= 128) {
            ESP_LOGE(TAG, "Password too long in raw form");
            return false;
        }
        
        decoded_len = url_decode(password, password_len, pwd_start, pwd_raw_len);
        if (decoded_len < 0) {
            ESP_LOGE(TAG, "Failed to decode password");
            return false;
        }
    }
    
    return true;
}

/**
 * @brief Provisioning timeout callback - auto-stop after 30 minutes
 */
static void provision_timeout_callback(TimerHandle_t timer)
{
    ESP_LOGW(TAG, "Provisioning timeout reached (%d minutes), stopping...", 
             PROVISION_TIMEOUT_MINUTES);
    wifi_mgr_stop_provisioning();
}

void wifi_provisioning_start_http_server(void)
{
    if (s_http_server) {
        ESP_LOGW(TAG, "HTTP server already running");
        return;
    }
    
    httpd_config_t config = HTTPD_DEFAULT_CONFIG();
    config.server_port = 80;
    config.max_uri_handlers = 4;
    config.stack_size = 4096;
    
    if (httpd_start(&s_http_server, &config) != ESP_OK) {
        ESP_LOGE(TAG, "Failed to start HTTP server");
        return;
    }
    
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
    
    if (httpd_register_uri_handler(s_http_server, &root_uri) != ESP_OK ||
        httpd_register_uri_handler(s_http_server, &connect_uri) != ESP_OK) {
        ESP_LOGE(TAG, "Failed to register URI handlers");
        httpd_stop(s_http_server);
        s_http_server = NULL;
        return;
    }
    
    ESP_LOGI(TAG, "HTTP server started on port 80");
    
    /* Start 30-minute timeout timer */
    if (!s_provision_timer) {
        s_provision_timer = xTimerCreate("prov_timeout",
                                         pdMS_TO_TICKS(PROVISION_TIMEOUT_MS),
                                         pdFALSE,  /* one-shot */
                                         NULL,
                                         provision_timeout_callback);
    }
    
    if (s_provision_timer) {
        xTimerStart(s_provision_timer, 0);
        ESP_LOGI(TAG, "Provisioning timeout set: %d minutes", PROVISION_TIMEOUT_MINUTES);
    }
}

void wifi_provisioning_stop_http_server(void)
{
    if (s_provision_timer) {
        xTimerStop(s_provision_timer, 0);
    }
    
    if (s_http_server) {
        httpd_stop(s_http_server);
        s_http_server = NULL;
        ESP_LOGI(TAG, "HTTP server stopped");
    }
}

bool wifi_provisioning_is_active(void)
{
    return s_provisioning_active;
}

void wifi_provisioning_set_active(bool active)
{
    s_provisioning_active = active;
}
