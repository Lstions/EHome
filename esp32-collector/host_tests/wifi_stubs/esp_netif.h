#ifndef HOST_TEST_WIFI_STUBS_ESP_NETIF_H
#define HOST_TEST_WIFI_STUBS_ESP_NETIF_H

/*
 * Minimal esp_netif.h stub for host-side unit tests of wifi_mgr.c.
 */

#include "esp_err.h"
#include <stdint.h>

typedef struct esp_netif_obj esp_netif_t;

typedef struct {
    uint32_t addr;
} esp_ip4_addr_t;

typedef struct {
    esp_ip4_addr_t ip;
    esp_ip4_addr_t netmask;
    esp_ip4_addr_t gw;
} esp_netif_ip_info_t;

#define IPSTR "%d.%d.%d.%d"
#define IP2STR(ipaddr) \
    (int)((ipaddr)->addr & 0xFF), \
    (int)(((ipaddr)->addr >> 8) & 0xFF), \
    (int)(((ipaddr)->addr >> 16) & 0xFF), \
    (int)(((ipaddr)->addr >> 24) & 0xFF)

esp_err_t esp_netif_init(void);
esp_netif_t *esp_netif_create_default_wifi_sta(void);
esp_netif_t *esp_netif_create_default_wifi_ap(void);

#endif /* HOST_TEST_WIFI_STUBS_ESP_NETIF_H */
