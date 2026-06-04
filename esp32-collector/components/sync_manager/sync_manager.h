/**
 * @file sync_manager.h
 * @brief Sync Manager v2.1 - Unified synchronization decision engine
 *
 * Replaces scattered sync logic in main.c / on_mqtt_state / on_mqtt_msg.
 * Implements 7-reason decision model + periodic sync task.
 */

#ifndef SYNC_MANAGER_H
#define SYNC_MANAGER_H

#include <stdint.h>
#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

/* === Sync reasons === */
typedef enum {
    SYNC_REASON_NONE,              // No sync needed
    SYNC_REASON_PERIODIC,          // Periodic (default 10min)
    SYNC_REASON_EPOCH_LAG,         // Response indicates epoch lag
    SYNC_REASON_NVS_EMPTY,         // NVS empty (factory reset)
    SYNC_REASON_FORCED,            // force_sync flag received
    SYNC_REASON_USER_ACTION,       // User-triggered action
    SYNC_REASON_DOUBT,             // Suspected state inconsistency
    SYNC_REASON_MANIFEST_MISMATCH, // Received manifest_id mismatch
} sync_reason_t;

/* === Sync state === */
typedef struct {
    uint64_t epoch;               // Current local epoch
    char     manifest_id[64];     // Current local manifest_id
    bool     nvs_has_config;      // NVS has manifest data
    uint32_t last_sync_time_sec;  // Last sync time (seconds since boot)
    uint32_t last_sync_id_hash;   // Last sync_id hash (dedup)
} sync_state_t;

/* === Sync state enum for StatusReport field 5 === */
typedef enum {
    SYNC_STATE_IDLE    = 0,  // No sync in progress
    SYNC_STATE_SYNCING = 1,  // Sync request sent, awaiting response
    SYNC_STATE_ERROR   = 2,  // Sync failed
} sync_state_enum_t;

/* === Init === */
void sync_manager_init(void);

/* === Callback: called when sync_manager wants to send Hello === */
typedef void (*sync_send_hello_cb_t)(void);
void sync_manager_register_send_hello_cb(sync_send_hello_cb_t cb);

/* === Request sync with given reason === */
void sync_manager_request_sync(sync_reason_t reason);

/* === Called on any downlink message (may trigger sync check) === */
void sync_manager_on_downlink_received(uint8_t msg_type);

/* === Get current sync state (read-only) === */
sync_state_t *sync_get_state(void);

/* === Get current sync state enum (for StatusReport) === */
sync_state_enum_t sync_manager_get_state_enum(void);

/* === Periodic task entry point === */
void sync_manager_periodic_task(void *pvParameters);

/* === Update state after successful ConfigManifest apply === */
void sync_manager_on_config_applied(uint64_t server_epoch, const char *manifest_id);

#ifdef __cplusplus
}
#endif

#endif /* SYNC_MANAGER_H */
