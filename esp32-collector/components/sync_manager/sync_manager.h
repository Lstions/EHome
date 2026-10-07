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
#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

/* === Sync reasons === */
typedef enum {
    SYNC_REASON_NONE,              // No sync needed
    SYNC_REASON_PERIODIC,          // Periodic (default 10min)
    SYNC_REASON_EPOCH_LAG,         // Response indicates epoch lag
    SYNC_REASON_NO_CONFIG,          // No active config (boot/factory reset)
    SYNC_REASON_FORCED,            // force_sync flag received
    SYNC_REASON_USER_ACTION,       // User-triggered action
    SYNC_REASON_DOUBT,             // Suspected state inconsistency
    SYNC_REASON_MANIFEST_MISMATCH, // Received manifest_id mismatch
} sync_reason_t;

/* === Sync state === */
typedef struct {
    uint64_t epoch;               // Current local epoch
    char     manifest_id[64];     // Current local manifest_id
    bool     has_active_config;    // Device has active config in memory
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

/* === 注入"现在有没有可用上行"（P7：宿主可测；P4：一处定义）===
 *
 * 为什么必须有它（2026-10-08 真机缺陷）：
 * 本模块原先**硬编码**判 `mqtt_client_is_connected_impl()`，于是：
 *   · 无 MQTT 时 `sync_manager_request_sync()` **直接 return**（只留一条 WARN）；
 *   · 配置之所以还能同步，是靠 **device_link 自己的握手 Hello**（3.0 路径）兜住的，
 *     不是本模块的功劳；
 *   · 而"周期 / 怀疑 / 无配置"这三条**主动请求同步**的路径**全部失效**。
 * ⇒ §7.3 P4（后端关闭 MQTT 监听）之后，`mqtt_client_is_connected_impl()` **永远 false**
 *   ⇒ 这三条路径**永久死掉**，且**不报错**（只有 WARN）—— 典型的静默死角。
 *
 * ⇒ 修法：把"有没有可用上行"做成**注入的函数指针**，由 main 侧接到上行仲裁
 *   （`uplink_arbiter_tcp3_connected() || uplink_arbiter_mqtt_connected()`）。
 *   ⚠ 语义是"**任意一条上行可用**"，不是"MQTT 可用" —— 这正是 P4 需要的。
 *
 * 未注入时的行为：退化为只看 MQTT（与改动前**逐位一致**），
 * 以免宿主测试与既有接线被静默改变。 */
typedef bool (*sync_uplink_available_cb_t)(void);
void sync_manager_register_uplink_available_cb(sync_uplink_available_cb_t cb);

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
esp_err_t sync_manager_on_config_applied(uint64_t server_epoch, const char *manifest_id);

/* === Config receive timeout (esp_timer one-shot) === */
void sync_manager_start_config_timeout(void);
void sync_manager_cancel_config_timeout(void);

#ifdef __cplusplus
}
#endif

#endif /* SYNC_MANAGER_H */
