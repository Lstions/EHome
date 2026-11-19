/**
 * @file ota.h
 * @brief OTA Upgrade Manager
 */

#ifndef OTA_H
#define OTA_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include "esp_err.h"

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
    uint32_t sequence;
} ota_cmd_t;

void ota_init(void);

/**
 * @brief Start OTA upgrade with command structure
 * @param cmd Heap-allocated command (ota_start takes ownership and will free it)
 */
esp_err_t ota_start(const ota_cmd_t *cmd);
bool ota_is_upgrading(void);

/**
 * @brief Check if the given ota_id is a duplicate of the last processed OTA.
 * @param ota_id The OTA ID to check
 * @return true if duplicate (should be ignored), false if new
 */
bool ota_is_duplicate(const char *ota_id);
typedef enum {
    OTA_CMD_NEW = 0,
    OTA_CMD_EXACT_REPLAY,
    OTA_CMD_COLLISION,
    OTA_CMD_BUSY,
} ota_cmd_class_t;
ota_cmd_class_t ota_classify_cmd(const ota_cmd_t *cmd);
void ota_forget_duplicate(const char *ota_id);
void ota_replay_last_progress(const char *ota_id);

/**
 * @brief Check OTA NVS state for power-loss recovery.
 * @return ota_state: 0=none, 1=downloading, 2=verifying
 */
uint8_t ota_get_nvs_state(void);

/**
 * @brief Confirm current app is valid and cancel rollback.
 *
 * ⚠ 判据必须**完全本地可达成** —— 不要写成“WiFi 连上 / Hello 完成 / 首条
 *   StatusReport 成功之后再确认”。引导器的语义是“镜像处于 PENDING_VERIFY
 *   且没被标 VALID 就复位 ⇒ 下次启动置 ABORTED 并退回旧分区”
 *   （bootloader_support/src/bootloader_utility.c:396-400、445-447），
 *   因此把确认挂到任何网络条件上，等于让“应用正常、只是网络不通”的设备在
 *   下次意外复位时被误滚回旧固件（方案 §3.4 判断 2 / §6.6 / AC-15）。
 *   唯一调用点：main.c 的 ota_boot_validation_poll()（每秒本地自检）。
 *
 * 重复调用安全：镜像已是 VALID 时直接返回 ESP_OK（IDF 侧跳过写 otadata，
 * 这里只重试 NVS 恢复态的清理）。
 */
esp_err_t ota_confirm_valid(void);

/* ============================================================================
 * 回滚确认判据（方案 §6.6，M3a / T3a.1）
 * ============================================================================ */

/** 新镜像启动后需要稳定运行多久才允许标 VALID（秒）。
 *
 * 30s 与旧实现及 docs/设计/固件OTA.md 的「30s mark-valid」表述一致：短到不
 * 明显拖慢上线，长到足以让“启动期崩溃 / 看门狗复位”暴露出来 —— 引导器只认
 * “没确认就复位”，确认越晚，坏镜像越有机会被抓到。
 *
 * 为什么是宏而不是 Kconfig int：
 *   Kconfig 选项必须同时写在 Kconfig 文件里（components/ota/Kconfig 或
 *   main/Kconfig.projbuild）。本卡（T3a.1）的改动范围**不含**任何 Kconfig
 *   文件；若只在 sdkconfig.defaults 里写 CONFIG_OTA_CONFIRM_DELAY_S，对应的
 *   kconfig 符号不存在，该行会被 kconfgen **静默丢弃** —— 而
 *   tools/check_sdkconfig_symbols.py 门禁正是为抓这种「写了但不生效」而存在
 *   （它会直接让构建失败）。⇒ 先落头文件常量；Kconfig 可改时再迁移为 int，
 *   默认值与这里保持一致。 */
#define OTA_CONFIRM_DELAY_S 30U

/**
 * @brief 纯判据：此刻是否应当确认当前镜像（无 I/O、无副作用）。
 *
 * 三个条件**全部本地可达成**（方案 §6.6 实施要点 3）：
 *   1. `uptime_sec >= OTA_CONFIRM_DELAY_S`：稳定运行够久；
 *   2. `!safe_mode`：启动熔断的安全模式意味着设备正在“降级但活着”或反复
 *      重启，此时确认会把坏固件钉成 VALID（实施要点 2）；
 *   3. `core_ready`：关键初始化序列已跑完（见 main.c 的 s_core_ready）。
 *
 * 反例（**禁止**加入本函数）：WiFi 已连接、Hello 已应答、NTP 已同步、
 * 服务端可达。它们回答的是“网络通不通”，不是“固件能不能用”。
 *
 * @param need_confirm 运行分区是否处于 PENDING_VERIFY（app_state_t.ota_need_confirm）
 * @param uptime_sec   本次上电以来的真实秒数（app_state_uptime_sec_now()）
 * @param safe_mode    boot_guard_in_safe_mode()
 * @param core_ready   关键子系统就绪（main.c 的 critical_ready()）
 */
static inline bool ota_confirm_due(bool need_confirm, uint32_t uptime_sec,
                                   bool safe_mode, bool core_ready)
{
    return need_confirm && !safe_mode && core_ready &&
           uptime_sec >= (uint32_t)OTA_CONFIRM_DELAY_S;
}

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
    OTA_ROLLBACK_ON_BOOT_FAIL = 0,  ///< Rollback because boot validation failed locally (critical init unusable; never a network verdict)
    OTA_ROLLBACK_MANUAL,             ///< Manual rollback triggered by user/server command
} ota_rollback_trigger_t;

/**
 * @brief Mark the running app as invalid and reboot to the previous partition.
 *
 * **只用于「能跑但确定不可用」**（如关键外设初始化失败）。不要用它处理
 * 崩溃 / 启动即死：那种情况什么都不做，让引导器在下次启动时把
 * PENDING_VERIFY 置 ABORTED 更可靠 —— 崩溃时往往来不及跑完这条路径。
 *
 * 内部调用 esp_ota_mark_app_invalid_rollback_and_reboot()：成功时设备直接
 * 复位（**本函数不返回**）。若另一分区没有可用镜像，IDF 返回
 * ESP_ERR_OTA_ROLLBACK_FAILED 且**不复位** —— 此时退化为「不确认 + 复位」：
 * 记 ERROR 后主动 esp_restart()，把决定权交回引导器。
 *
 * @param trigger Reason for the rollback (for logging/auditing)
 */
void ota_mark_invalid_rollback(ota_rollback_trigger_t trigger);

#ifdef __cplusplus
}
#endif

#endif
