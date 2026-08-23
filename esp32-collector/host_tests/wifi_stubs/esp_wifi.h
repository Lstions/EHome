#ifndef HOST_TEST_WIFI_STUBS_ESP_WIFI_H
#define HOST_TEST_WIFI_STUBS_ESP_WIFI_H

/*
 * Minimal esp_wifi.h stub for host-side unit tests of wifi_mgr.c.
 * Provides only what wifi_mgr.c / wifi_mgr_get_rssi_dbm() need.
 */

#include "esp_err.h"
#include <stdint.h>

typedef enum {
    WIFI_MODE_NULL = 0,
    WIFI_MODE_STA,
    WIFI_MODE_AP,
    WIFI_MODE_APSTA,
    WIFI_MODE_MAX
} wifi_mode_t;

typedef enum {
    WIFI_IF_AP = 0,
    WIFI_IF_STA,
    WIFI_IF_MAX
} wifi_interface_t;

typedef enum {
    WIFI_AUTH_OPEN = 0,
    WIFI_AUTH_WPA2_PSK = 4,
} wifi_auth_mode_t;

typedef struct {
    uint8_t ssid[32];
    uint8_t password[64];
    uint8_t ssid_len;
    uint8_t channel;
    uint8_t authmode;
    uint8_t max_connection;
    struct {
        wifi_auth_mode_t authmode;
    } threshold;
} wifi_sta_config_t;

typedef struct {
    uint8_t ssid[32];
    uint8_t password[64];
    uint8_t ssid_len;
    uint8_t channel;
    wifi_auth_mode_t authmode;
    uint8_t max_connection;
} wifi_ap_config_t;

typedef struct {
    wifi_sta_config_t sta;
    wifi_ap_config_t  ap;
} wifi_config_t;

typedef struct {
    int dummy;          /* WIFI_INIT_CONFIG_DEFAULT content not needed */
} wifi_init_config_t;

#define WIFI_INIT_CONFIG_DEFAULT() {0}

typedef struct {
    uint8_t bssid[6];
    uint8_t ssid[33];
    uint8_t primary;
    uint32_t authmode;
    int8_t  rssi;       /* signal strength of AP, negative dBm */
} wifi_ap_record_t;

esp_err_t esp_wifi_init(const wifi_init_config_t *config);
esp_err_t esp_wifi_set_mode(wifi_mode_t mode);
esp_err_t esp_wifi_set_config(wifi_interface_t interface, wifi_config_t *conf);
esp_err_t esp_wifi_start(void);
esp_err_t esp_wifi_stop(void);
esp_err_t esp_wifi_connect(void);
esp_err_t esp_wifi_sta_get_ap_info(wifi_ap_record_t *ap_info);

#endif /* HOST_TEST_WIFI_STUBS_ESP_WIFI_H */
