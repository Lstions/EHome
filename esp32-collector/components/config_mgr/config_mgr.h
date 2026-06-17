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

/* === NVS version === */
#define CONFIG_NVS_VERSION  1  /* Increment when config_manifest_t layout changes */

/* === Template === */
typedef struct {
    uint32_t id;
    uint8_t  write_data[64];
    size_t   write_data_len;
    uint32_t read_length;
    uint32_t delay_ms;
} config_template_t;

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

/* === Get config hash (CRC32) === */
uint32_t config_mgr_get_hash(void);

/* === NVS persistence === */
bool config_mgr_save_to_nvs(void);
bool config_mgr_load_from_nvs(void);

/* === v2.1 Sync: Epoch / Manifest ID persistence === */
uint64_t config_mgr_get_epoch(void);
bool config_mgr_has_manifest(void);
const char *config_mgr_get_manifest_id(void);
void config_mgr_set_epoch(uint64_t epoch);
void config_mgr_set_manifest_id(const char *id);
void config_mgr_clear_epoch(void);  /* factory_reset use */

/* === v2.4 bus_config flags helpers === */
/* Note: bus_config_get_dma_enabled() is defined in bus_dma.h */

/* === DIP: DMA pool injection ===
 * config_mgr receives dma_pool_t* via setter (not a global).
 * Must be called before config_mgr_apply_manifest if DmaChannelConfig
 * fields need to be applied. */
struct dma_pool_t;
void config_mgr_set_dma_pool(struct dma_pool_t *pool);

/** Replay stored DMA configs into dma_pool (called after NVS load + pool inject) */
void config_mgr_replay_dma_configs(void);

#ifdef __cplusplus
}
#endif

#endif /* CONFIG_MGR_H */
