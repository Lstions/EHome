#ifndef HOST_TEST_ESP_MAC_H
#define HOST_TEST_ESP_MAC_H
#include <stdint.h>
#include "esp_err.h"
typedef enum { ESP_MAC_WIFI_STA = 0 } esp_mac_type_t;
esp_err_t esp_read_mac(uint8_t *mac, esp_mac_type_t type);
esp_err_t esp_efuse_mac_get_default(uint8_t *mac);
#endif
