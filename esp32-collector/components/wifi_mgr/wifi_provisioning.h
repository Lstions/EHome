/**
 * @file wifi_provisioning.h
 * @brief WiFi Provisioning Module - SoftAP + HTTP captive portal
 *
 * Internal header. Public provisioning API (wifi_mgr_start_provisioning,
 * wifi_mgr_stop_provisioning) is declared in wifi_mgr.h.
 */

#ifndef WIFI_PROVISIONING_H
#define WIFI_PROVISIONING_H

#include <stdbool.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/* === Configuration === */

/** Provisioning mode auto-stop timeout (30 minutes). */
#define WIFI_PROVISION_TIMEOUT_MS  (30 * 60 * 1000)

/** Maximum length of URL-encoded form body we accept. */
#define WIFI_PROVISION_MAX_BODY    256

/** Maximum SSID length (IEEE 802.11). */
#define WIFI_PROVISION_SSID_MAX    32

/** Maximum password length (WPA2-PSK max is 63). */
#define WIFI_PROVISION_PASSWORD_MAX 64

/* === URL decode === */

/**
 * @brief Decode a URL-encoded (percent-encoded) string in-place.
 *
 * Handles '+' as space and %XX hex sequences.  The output is always
 * NUL-terminated and never exceeds @p out_len bytes (including the NUL).
 *
 * @param dst     Destination buffer (may be the same as @p src).
 * @param dst_len Size of @p dst in bytes.
 * @param src     URL-encoded source string.
 * @param src_len Length of @p src in bytes.
 * @return        Number of characters written (excluding NUL), or -1 on error.
 */
static int url_decode(char *dst, size_t dst_len, const char *src, size_t src_len);

/* === HTTP server (internal) === */
void wifi_provisioning_start_http_server(void);
void wifi_provisioning_stop_http_server(void);

/* === Provisioning lifecycle (internal, called from wifi_mgr) === */
bool wifi_provisioning_is_active(void);
void wifi_provisioning_set_active(bool active);

#ifdef __cplusplus
}
#endif

#endif /* WIFI_PROVISIONING_H */
