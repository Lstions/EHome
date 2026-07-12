/**
 * @file config_mgr.h
 * @brief Configuration Manager - ConfigManifest handling, template/channel management
 */

#ifndef CONFIG_MGR_H
#define CONFIG_MGR_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/* === Limits === */
#define MAX_TEMPLATES     16
#define MAX_CHANNELS      8
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
    uint8_t  bus_type;    // 1=UART, 2=I2C, 3=SPI, 4=GPIO, 5=ADC
    uint8_t  bus_config[128];
    size_t   bus_config_len;
    /* v2.3: edge device groups */
    config_edge_device_t edge_devices[MAX_EDGE_DEVICES_PER_CH];
    uint8_t  edge_device_count;
} config_channel_t;

/* === DMA Channel Config (persisted with manifest) === */
typedef struct {
    uint32_t dma_id;
    bool     enabled;
    char     bind_to[16];
} config_dma_channel_t;

/* === Config state === */
typedef struct {
    char              manifest_id[64];
    config_template_t templates[MAX_TEMPLATES];
    uint8_t           template_count;
    config_channel_t  channels[MAX_CHANNELS];
    uint8_t           channel_count;
    config_dma_channel_t dma_configs[MAX_DMA_CONFIGS];
    uint8_t           dma_config_count;
    bool              applied;
    /* v2.5: log stream config */
    bool              log_stream_enabled;
    uint8_t           log_stream_level;
} config_manifest_t;

/* === Init === */
void config_mgr_init(void);

/* === Apply manifest from raw frame data === */
bool config_mgr_apply_manifest(const uint8_t *data, size_t len);

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
