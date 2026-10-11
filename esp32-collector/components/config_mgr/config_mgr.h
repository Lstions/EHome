/**
 * @file config_mgr.h
 * @brief Configuration Manager - ConfigManifest handling, template/channel management
 */

#ifndef CONFIG_MGR_H
#define CONFIG_MGR_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

/* === Limits === */
#define MAX_TEMPLATES     16
/* Physical channel ceiling per SoC.  This is a HARDWARE bound, not a memory
 * tuning knob: the product must run 100 Hz on every physical bus at full
 * population.  S3 = UART0/1/2 + SPI + I2C = 5; C6 = UART0/1 + SPI + I2C = 4.
 * (C6's third UART is LP-only and is not a HP data bus.)
 *
 * The fallback 8 keeps host tests/target-less builds at the historical value;
 * firmware builds always define exactly one CONFIG_IDF_TARGET_ESP32* macro
 * (sdkconfig.h reaches this header through esp_err.h -> esp_compiler.h).
 * scheduler.h's SCHED_MAX_CHANNELS is defined FROM this constant, and
 * hw_profile publishes this value to the backend as manifest_capacity.
 * Do NOT lower it below the physical ceiling to save RAM. */
#if defined(CONFIG_IDF_TARGET_ESP32S3)
#define MAX_CHANNELS      5
#elif defined(CONFIG_IDF_TARGET_ESP32C6)
#define MAX_CHANNELS      4
#else
#define MAX_CHANNELS      8
#endif
#define MAX_TEMPLATE_IDS  8
#define MAX_DMA_CONFIGS   8
#define MAX_EDGE_DEVICES_PER_CH 5
#define MAX_COMMANDS_PER_DEVICE 3


/* === Template === */
typedef struct {
    uint32_t id;
    uint8_t  write_data[64];
    size_t   write_data_len;
    uint32_t read_length;
    uint32_t delay_ms;
} config_template_t;

/* === Edge Device Command === */
typedef struct {
    uint32_t template_id;
    uint32_t interval_ms;
    bool     enabled;
} config_command_t;

/* === Edge Device === */
typedef struct {
    uint32_t edge_device_id;
    uint32_t hardware_id;
    config_command_t commands[MAX_COMMANDS_PER_DEVICE];
    uint8_t  command_count;
} config_edge_device_t;

/* === Channel === */
typedef struct {
    uint32_t id;
    uint32_t hardware_id;
    uint32_t template_ids[MAX_TEMPLATE_IDS];
    uint8_t  template_count;
    uint32_t interval_ms;
    bool     enabled;
    /* P0: field-8 DMA preference.  Presence is tracked so old manifests can
     * still fall back to bus_config flags without confusing missing with
     * explicit false. */
    bool     dma_enabled;
    bool     dma_enabled_present;
    uint8_t  bus_type;    // 1=UART, 2=I2C, 3=SPI, 5=ADC (4=legacy GPIO, rejected)
    uint8_t  bus_config[64];
    size_t   bus_config_len;
    /* v2.3: edge device groups */
    config_edge_device_t edge_devices[MAX_EDGE_DEVICES_PER_CH];
    uint8_t  edge_device_count;
} config_channel_t;

/** Resolve DMA preference with field-8/legacy bus_config compatibility.
 *
 * ⚠⚠ 2026-10-09（用户明确要求）：**DMA 默认都不开，由用户手动配置。**
 *   用户原话："C6 S3的所有UART同时都只有有一个能用DMA！！！"
 *           "修改原则：DMA默认都不开，由用户手动配置"
 *
 * 五条返回路径全部收敛为"默认 false"：
 *   ① 无 channel                    -> false
 *   ② field 8 存在                  -> 用它的值（用户显式配置）
 *   ③ field 8 缺失 + bus_config 够长 -> 读 bus_config 的 DMA 位（legacy 显式配置）
 *   ④ field 8 缺失 + bus_config 太短 -> false（原为 true，fail-open）
 *   ⑤ bus_type 未知                 -> false
 *
 * ⚠ 第 ④ 条原来是 return true —— 那是 2026-07 修"field 8 缺失 case 8 的
 *   fail-open"时留下的**向后兼容**默认（docs/设计/DMA资源管理设计.md §6）。
 *   它的语义是"没说就当作要开 DMA"，与用户现在的要求**正好相反**：
 *   S3/C6 上 UART 同时只有 1 条能用 DMA（用户 2026-10-09 指正），
 *   所以 fail-open 会让"没配置"变成"抢 DMA" ⇒ 另一个 UART 静默失效
 *   或整份 manifest 被资源计划拒绝（§211 的 C6 现场事故：2818 次 config failed）。
 *   ⇒ 收敛为 false 后，"没配置" = 不开 DMA = 走中断/轮询，功能不受影响。
 *
 * ⚠ ③ 仍保留：bus_config 里的 DMA 位是**用户显式配置过**的值（前端开关会写
 *   这一位），不是"默认"。只有"既没 field 8 也没 flags 字节"才算未配置。
 */
static inline bool config_channel_get_dma_enabled(const config_channel_t *channel)
{
    if (!channel) return false;
    if (channel->dma_enabled_present) return channel->dma_enabled;

    size_t flags_offset = 0;
    size_t min_len = 0;
    switch (channel->bus_type) {
    case 1: flags_offset = 6; min_len = 7; break;
    case 2: flags_offset = 7; min_len = 8; break;
    case 3: flags_offset = 6; min_len = 7; break;
    default: return false;
    }
    if (channel->bus_config_len >= min_len)
        return (channel->bus_config[flags_offset] & 0x01U) != 0;
    /* 未配置：默认**不开** DMA（用户 2026-10-09 要求；原为 true 是 fail-open）。 */
    return false;
}

/* === DMA Channel Config (persisted with manifest) === */
typedef struct {
    uint32_t dma_id;
    bool     enabled;
    char     bind_to[16];
} config_dma_channel_t;

/* === v3.0: GPIO/PWM peripheral configs (field 11/12) === */
#define MAX_GPIO_CONFIGS  12
#define MAX_PWM_CONFIGS   8

typedef struct {
    uint8_t pin;
    uint8_t direction;     /* 0=INPUT, 1=OUTPUT, 2=INPUT_PULLUP, 3=INPUT_PULLDOWN */
    uint8_t initial_level; /* OUTPUT 时的初始电平 */
} config_gpio_t;

typedef struct {
    uint8_t  channel;      /* reported LEDC hardware channel identity */
    uint8_t  pin;          /* GPIO output route */
    uint32_t frequency;    /* Hz */
    uint16_t duty;         /* 0-10000 = 0.00%-100.00% */
    uint8_t  resolution;   /* bits (4-20, default 14) */
    bool     auto_start;   /* ConfigManifest 应用后自动启动 */
} config_pwm_t;

/* === Config state === */
typedef struct {
    char              manifest_id[32];
	char              sync_id[64];
    config_template_t templates[MAX_TEMPLATES];
    uint8_t           template_count;
    config_channel_t  channels[MAX_CHANNELS];
    uint8_t           channel_count;
    config_dma_channel_t dma_configs[MAX_DMA_CONFIGS];
    uint8_t           dma_config_count;
    config_gpio_t     gpio_configs[MAX_GPIO_CONFIGS];  /* v3.0 field 11 */
    uint8_t           gpio_config_count;
    config_pwm_t      pwm_configs[MAX_PWM_CONFIGS];     /* v3.0 field 12 */
    uint8_t           pwm_config_count;
    bool              applied;
    /* v2.5: log stream config */
    bool              log_stream_enabled;
    uint8_t           log_stream_level;
} config_manifest_t;

/* === Init === */
void config_mgr_init(void);

/* === Apply manifest from raw frame data === */
bool config_mgr_apply_manifest(const uint8_t *data, size_t len);
bool config_mgr_stage_manifest(const uint8_t *data, size_t len);
const config_manifest_t *config_mgr_get_staged_manifest(void);
bool config_mgr_commit_staged_manifest(void);
void config_mgr_discard_staged_manifest(void);
/* Copy active state into caller storage so staged commit cannot invalidate
 * rollback input. Intended for bounded heap transaction workspaces.
 *
 * NOTE(WS-E 2026-10-05): the configuration transaction no longer calls this.
 * stage_manifest() writes only the inactive slot and commit_staged_manifest()
 * is the transaction's last step, so rollback can use the live pointer from
 * config_mgr_get_manifest() instead of a 5,400 B copy.  Kept as a
 * compatibility API for tests and other callers. */
bool config_mgr_snapshot_active(config_manifest_t *out);

/* === Get current config === */
const config_manifest_t *config_mgr_get_manifest(void);

/* === Get template by ID === */
const config_template_t *config_mgr_get_template(uint32_t id);

/* === Get channel by index === */
const config_channel_t *config_mgr_get_channel(uint8_t index);

/* === Get active channel count === */
uint8_t config_mgr_get_active_channel_count(void);

/* === Sync metadata (NVS) === */
uint64_t config_mgr_get_epoch(void);
void config_mgr_set_epoch(uint64_t epoch);
void config_mgr_clear_epoch(void);  /* factory_reset use */

/* === In-memory manifest state (server = truth) === */
bool config_mgr_has_manifest(void);           /* checks in-memory only */
const char *config_mgr_get_manifest_id(void); /* in-memory only */

/* === Last-known manifest from NVS (for Hello v2.1 protocol fields) === */
const char *config_mgr_get_last_known_manifest_id(void);
bool config_mgr_has_last_known_manifest(void);

/* === DIP: DMA pool injection ===
 * config_mgr receives dma_pool_t* via setter (not a global). */
struct dma_pool_t;
void config_mgr_set_dma_pool(struct dma_pool_t *pool);
void config_mgr_set_manifest_id(const char *id);
esp_err_t config_mgr_persist_sync_metadata(uint64_t epoch, const char *manifest_id);

/* === Double-buffer lock API (for app_callbacks long-lock interval) === */
void config_mgr_lock(void);
void config_mgr_unlock(void);

/* === v2.5: Log stream config === */
bool    config_mgr_get_log_stream_enabled(void);
uint8_t config_mgr_get_log_stream_level(void);

#ifdef __cplusplus
}
#endif

#endif /* CONFIG_MGR_H */
