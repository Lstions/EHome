/**
 * @file wifi_mgr.h
 * @brief WiFi Manager - STA mode with auto-reconnect and NVS persistence
 */

#ifndef WIFI_MGR_H
#define WIFI_MGR_H

#include <stdbool.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* === WiFi state callback === */
typedef enum {
    WIFI_MGR_DISCONNECTED,
    WIFI_MGR_CONNECTING,
    WIFI_MGR_CONNECTED,
    WIFI_MGR_FAILED,
} wifi_mgr_state_t;

typedef void (*wifi_mgr_state_cb_t)(wifi_mgr_state_t state, void *ctx);

/* === Init / Start === */
void wifi_mgr_init(void);
void wifi_mgr_start(void);
void wifi_mgr_stop(void);

/* === State query === */
wifi_mgr_state_t wifi_mgr_get_state(void);
bool wifi_mgr_is_connected(void);

/* === Credentials (NVS) === */
bool wifi_mgr_save_credentials(const char *ssid, const char *password);
bool wifi_mgr_load_credentials(char *ssid, size_t ssid_len, char *password, size_t pwd_len);
void wifi_mgr_clear_credentials(void);
bool wifi_mgr_has_credentials(void);

/* === Callbacks === */
void wifi_mgr_register_state_cb(wifi_mgr_state_cb_t cb, void *ctx);

/* === Provisioning === */
void wifi_mgr_start_provisioning(void);
void wifi_mgr_stop_provisioning(void);

#ifdef __cplusplus
}
#endif

#endif /* WIFI_MGR_H */
