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

/* === Config state === */
typedef struct {
    char              manifest_id[64];
    config_template_t templates[MAX_TEMPLATES];
    uint8_t           template_count;
    config_channel_t  channels[MAX_CHANNELS];
    uint8_t           channel_count;
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

#ifdef __cplusplus
}
#endif

#endif /* CONFIG_MGR_H */
