/**
 * @file ota.h
 * @brief OTA Upgrade Manager
 */

#ifndef OTA_H
#define OTA_H

#include <stdint.h>
#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

void ota_init(void);
void ota_start(const char *ota_id, const char *url, const char *checksum, uint64_t size, const char *version);
bool ota_is_upgrading(void);

/**
 * @brief Check if the given ota_id is a duplicate of the last processed OTA.
 * @param ota_id The OTA ID to check
 * @return true if duplicate (should be ignored), false if new
 */
bool ota_is_duplicate(const char *ota_id);

/**
 * @brief Check OTA NVS state for power-loss recovery.
 * @return ota_state: 0=none, 1=downloading, 2=verifying
 */
uint8_t ota_get_nvs_state(void);

/**
 * @brief Confirm current app is valid and cancel rollback.
 * Call after boot validation (e.g. WiFi connected successfully).
 */
void ota_confirm_valid(void);

#ifdef __cplusplus
}
#endif

#endif
