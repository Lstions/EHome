/**
 * @file wifi_provisioning.h
 * @brief WiFi 配网模式
 * 
 * 支持 BLE 配网、SoftAP 配网、串口配网三种方式。
 */

#ifndef WIFI_PROVISIONING_H
#define WIFI_PROVISIONING_H

#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

/* === 配网结果 === */
typedef struct {
    char ssid[32];
    char password[64];
    bool success;
} wifi_prov_result_t;

/* === 初始化配网模块 === */
bool wifi_provisioning_init(void);

/* === 检查是否需要配网 === */
bool wifi_provisioning_needed(void);

/* === 启动配网模式 === */
void wifi_provisioning_start(void);

/* === 停止配网模式 === */
void wifi_provisioning_stop(void);

/* === 保存 WiFi 凭据到 NVS === */
bool wifi_provisioning_save_credentials(const char *ssid, const char *password);

/* === 从 NVS 读取 WiFi 凭据 === */
bool wifi_provisioning_load_credentials(char *ssid, size_t ssid_len, char *password, size_t password_len);

/* === 清除 WiFi 凭据 === */
void wifi_provisioning_clear_credentials(void);

#ifdef __cplusplus
}
#endif

#endif /* WIFI_PROVISIONING_H */
