/**
 * @file ota.h
 * @brief OTA Upgrade Manager
 */

#ifndef OTA_H
#define OTA_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/**
 * @brief OTA command structure (heap-allocated, eliminates static buffer concurrency issues)
 */
typedef struct {
    char ota_id[64];
    char firmware_url[256];
    char checksum[128];
    char version[32];
    uint64_t size_bytes;
} ota_cmd_t;

void ota_init(void);

/**
 * @brief Start OTA upgrade with command structure
 * @param cmd Heap-allocated command (ota_start takes ownership and will free it)
 */
void ota_start(const ota_cmd_t *cmd);
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

/**
 * @brief OTA progress callback type for decoupling from msg_handler.
 * @param ota_id Unique OTA session identifier
 * @param status 0=downloading, 1=completed, 2=verifying, 3=failed
 * @param progress_pct Progress percentage (0-100)
 * @param error_msg Error message (NULL if no error)
 */
typedef void (*ota_progress_cb_t)(const char *ota_id, uint8_t status,
                                   uint8_t progress_pct, const char *error_msg);

/**
 * @brief Inject OTA progress callback to decouple from msg_handler.
 * Must be called before ota_start().
 */
void ota_set_progress_callback(ota_progress_cb_t cb);

/**
 * @brief Rollback trigger reasons for ota_mark_invalid_rollback().
 */
typedef enum {
    OTA_ROLLBACK_ON_BOOT_FAIL = 0,  ///< Rollback because boot validation failed (WiFi, etc.)
    OTA_ROLLBACK_MANUAL,             ///< Manual rollback triggered by user/server command
} ota_rollback_trigger_t;

/**
 * @brief Mark the running app as invalid and reboot to the previous partition.
 *
 * Internally calls esp_ota_mark_app_invalid_rollback_and_reboot().
 * Clears NVS state and logs the trigger reason.
 *
 * @param trigger Reason for the rollback (for logging/auditing)
 */
void ota_mark_invalid_rollback(ota_rollback_trigger_t trigger);

#ifdef __cplusplus
}
#endif

#endif
