/**
 * @file wifi_provisioning.c
 * @brief WiFi 配网模式实现
 */

#include "wifi_provisioning.h"
#include "nvs_flash.h"
#include "esp_log.h"
#include <string.h>

#define TAG "WIFI_PROV"
#define NVS_NAMESPACE "wifi_prov"
#define KEY_SSID "ssid"
#define KEY_PASSWORD "password"

static bool s_provisioning_active = false;

bool wifi_provisioning_init(void)
{
    // NVS 已在 main.c 中初始化
    ESP_LOGI(TAG, "WiFi provisioning initialized");
    return true;
}

bool wifi_provisioning_needed(void)
{
    char ssid[32] = {0};
    char password[64] = {0};
    return !wifi_provisioning_load_credentials(ssid, sizeof(ssid), password, sizeof(password));
}

void wifi_provisioning_start(void)
{
    if (s_provisioning_active) {
        return;
    }
    s_provisioning_active = true;
    ESP_LOGI(TAG, "WiFi provisioning started");
    ESP_LOGI(TAG, "Available methods:");
    ESP_LOGI(TAG, "  1. BLE provisioning (if supported)");
    ESP_LOGI(TAG, "  2. SoftAP: Connect to HomeStation-XXXX, visit 192.168.4.1");
    ESP_LOGI(TAG, "  3. Serial: AT+WIFI=ssid,password");
    
    // TODO: 实现 BLE/SoftAP 配网服务器
    // 这里仅作为占位，实际实现需要 esp_wifi 和蓝牙协议栈
}

void wifi_provisioning_stop(void)
{
    s_provisioning_active = false;
    ESP_LOGI(TAG, "WiFi provisioning stopped");
}

bool wifi_provisioning_save_credentials(const char *ssid, const char *password)
{
    nvs_handle_t nvs_handle;
    esp_err_t err = nvs_open(NVS_NAMESPACE, NVS_READWRITE, &nvs_handle);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to open NVS: %s", esp_err_to_name(err));
        return false;
    }
    
    err = nvs_set_str(nvs_handle, KEY_SSID, ssid);
    if (err != ESP_OK) {
        nvs_close(nvs_handle);
        return false;
    }
    
    err = nvs_set_str(nvs_handle, KEY_PASSWORD, password);
    if (err != ESP_OK) {
        nvs_close(nvs_handle);
        return false;
    }
    
    err = nvs_commit(nvs_handle);
    nvs_close(nvs_handle);
    
    return err == ESP_OK;
}

bool wifi_provisioning_load_credentials(char *ssid, size_t ssid_len, char *password, size_t password_len)
{
    nvs_handle_t nvs_handle;
    esp_err_t err = nvs_open(NVS_NAMESPACE, NVS_READONLY, &nvs_handle);
    if (err != ESP_OK) {
        return false;
    }
    
    size_t len = ssid_len;
    err = nvs_get_str(nvs_handle, KEY_SSID, ssid, &len);
    if (err != ESP_OK) {
        nvs_close(nvs_handle);
        return false;
    }
    
    len = password_len;
    err = nvs_get_str(nvs_handle, KEY_PASSWORD, password, &len);
    nvs_close(nvs_handle);
    
    return err == ESP_OK;
}

void wifi_provisioning_clear_credentials(void)
{
    nvs_handle_t nvs_handle;
    esp_err_t err = nvs_open(NVS_NAMESPACE, NVS_READWRITE, &nvs_handle);
    if (err == ESP_OK) {
        nvs_erase_all(nvs_handle);
        nvs_commit(nvs_handle);
        nvs_close(nvs_handle);
    }
    ESP_LOGI(TAG, "WiFi credentials cleared");
}
