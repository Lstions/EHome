/**
 * @file config_mgr.c
 * @brief Configuration Manager Implementation
 */

#include "config_mgr.h"
#include "esp_log.h"
#include "nvs_flash.h"
#include "frame_codec.h"
#include "dma_pool.h"
#include <string.h>
#include <stdlib.h>

#define TAG "CONFIG"

#define NVS_NAMESPACE "config"
/* v2.1 sync keys */
#define NVS_KEY_CONFIG_EPOCH   "cfg_epoch"     /* uint64 */
#define NVS_KEY_MANIFEST_ID    "manifest_id"   /* string */
#define NVS_KEY_LAST_SYNC_TIME "last_sync"     /* uint32 */

/* State */
static config_manifest_t s_manifest = {0};
static bool s_initialized = false;
static dma_pool_t *s_dma_pool = NULL;  /* Injected via setter (DIP) */

/* Forward declarations */
static void clear_manifest(void);
static bool parse_manifest(const uint8_t *data, size_t len);
/* === Public API === */

void config_mgr_set_dma_pool(dma_pool_t *pool)
{
    s_dma_pool = pool;
}

void config_mgr_init(void)
{
    if (s_initialized) {
        return;
    }

    ESP_LOGI(TAG, "Initializing config manager...");
    clear_manifest();
    s_initialized = true;
    /* Server is single source of truth — no NVS load at boot */
}

bool config_mgr_apply_manifest(const uint8_t *data, size_t len)
{
    if (data == NULL || len < 1) {
        ESP_LOGE(TAG, "Invalid manifest data");
        return false;
    }

    /* Clear existing config */
    clear_manifest();

    /* Parse new manifest */
    if (!parse_manifest(data, len)) {
        ESP_LOGE(TAG, "Failed to parse manifest");
        clear_manifest();
        return false;
    }

    s_manifest.applied = true;
    ESP_LOGD(TAG, "Applied manifest: %s, templates=%d, channels=%d",
             s_manifest.manifest_id, s_manifest.template_count, s_manifest.channel_count);

    return true;
}

const config_manifest_t *config_mgr_get_manifest(void)
{
    return s_initialized ? &s_manifest : NULL;
}

const config_template_t *config_mgr_get_template(uint32_t id)
{
    for (int i = 0; i < s_manifest.template_count; i++) {
        if (s_manifest.templates[i].id == id) {
            return &s_manifest.templates[i];
        }
    }
    return NULL;
}

const config_channel_t *config_mgr_get_channel(uint8_t index)
{
    if (index >= s_manifest.channel_count) {
        return NULL;
    }
    return &s_manifest.channels[index];
}

uint8_t config_mgr_get_active_channel_count(void)
{
    uint8_t count = 0;
    for (int i = 0; i < s_manifest.channel_count; i++) {
        if (s_manifest.channels[i].enabled) {
            count++;
        }
    }
    return count;
}

/* === Internal === */

static void clear_manifest(void)
{
    memset(&s_manifest, 0, sizeof(s_manifest));
}

static bool parse_manifest(const uint8_t *data, size_t len)
{
    if (len < 1) return false;

    /* Debug: dump raw hex */
    ESP_LOGD(TAG, "parse_manifest raw len=%d first_bytes=%02x %02x %02x %02x %02x",
             (int)len, len > 0 ? data[0] : 0, len > 1 ? data[1] : 0,
             len > 2 ? data[2] : 0, len > 3 ? data[3] : 0, len > 4 ? data[4] : 0);

    uint8_t msg_type = data[0];
    if (msg_type != MSG_CONFIG_MFST) {
        ESP_LOGE(TAG, "Invalid message type: 0x%02X", msg_type);
        return false;
    }

    frame_decoder_t dec;
    frame_err_t err = frame_decoder_init(&dec, data, len);
    if (err != FRAME_OK) {
        ESP_LOGE(TAG, "Decoder init failed: %d", err);
        return false;
    }

    frame_field_t field;

    while ((err = frame_decoder_next(&dec, &field)) == FRAME_OK) {
        /* Field 1: manifest_id (string) */
        if (field.field_num == 1 && field.wire_type == WIRE_LENGTH_DELIMITED && field.value.bytes.ptr) {
            size_t copy_len = field.value.bytes.len < sizeof(s_manifest.manifest_id) - 1
                            ? field.value.bytes.len : sizeof(s_manifest.manifest_id) - 1;
            memcpy(s_manifest.manifest_id, field.value.bytes.ptr, copy_len);
            s_manifest.manifest_id[copy_len] = '\0';
        }
        /* Field 3: templates (nested message) */
        else if (field.field_num == 3 && field.wire_type == WIRE_LENGTH_DELIMITED) {
            if (s_manifest.template_count < MAX_TEMPLATES) {
                config_template_t *cur_template = &s_manifest.templates[s_manifest.template_count++];
                /* Parse nested template fields */
                frame_decoder_t tdec;
                if (frame_decoder_init_sub(&tdec, field.value.bytes.ptr, field.value.bytes.len) == FRAME_OK) {
                    frame_field_t tf;
                    while (frame_decoder_next(&tdec, &tf) == FRAME_OK) {
                        switch (tf.field_num) {
                        case 1: /* id */
                            cur_template->id = (uint32_t)tf.value.varint;
                            break;
                        case 2: /* write_data */
                            if (tf.wire_type == WIRE_LENGTH_DELIMITED && tf.value.bytes.ptr) {
                                size_t cpy = tf.value.bytes.len < sizeof(cur_template->write_data)
                                            ? tf.value.bytes.len : sizeof(cur_template->write_data);
                                memcpy(cur_template->write_data, tf.value.bytes.ptr, cpy);
                                cur_template->write_data_len = cpy;
                            }
                            break;
                        case 3: /* read_length */
                            cur_template->read_length = (uint32_t)tf.value.varint;
                            break;
                        case 4: /* delay_ms */
                            cur_template->delay_ms = (uint32_t)tf.value.varint;
                            break;
                        }
                    }
                }
            }
        }
        /* Field 4: channels (nested message) */
        else if (field.field_num == 4 && field.wire_type == WIRE_LENGTH_DELIMITED && field.value.bytes.ptr) {
            ESP_LOGD(TAG, "Found channel field: len=%d", (int)field.value.bytes.len);
            if (s_manifest.channel_count < MAX_CHANNELS) {
                config_channel_t *cur_channel = &s_manifest.channels[s_manifest.channel_count++];
                /* Parse nested channel fields */
                frame_decoder_t cdec;
                frame_err_t ci_err = frame_decoder_init_sub(&cdec, field.value.bytes.ptr, field.value.bytes.len);
                ESP_LOGD(TAG, "Channel decoder init: err=%d", ci_err);
                if (ci_err == FRAME_OK) {
                    frame_field_t cf;
                    while (frame_decoder_next(&cdec, &cf) == FRAME_OK) {
                        ESP_LOGD(TAG, "  cf: field=%d wire=%d varint=%llu bytes_len=%d",
                                 cf.field_num, cf.wire_type, cf.value.varint,
                                 cf.wire_type == WIRE_LENGTH_DELIMITED ? (int)cf.value.bytes.len : 0);
                        switch (cf.field_num) {
                        case 1: /* id */
                            cur_channel->id = (uint32_t)cf.value.varint;
                            break;
                        case 2: /* hardware_id — server sends string like "UART1" or "0x68" */
                        	if (cf.wire_type == WIRE_LENGTH_DELIMITED && cf.value.bytes.ptr) {
                        		/* Read first 4 bytes as varint backward-compat, or parse as string */
                        		if (cf.value.bytes.len >= 1 && cf.value.bytes.len <= 4) {
                        			/* Treat as raw bytes → uint32 (little-endian like varint) */
                        			uint32_t val = 0;
                        			for (size_t b = 0; b < cf.value.bytes.len; b++) {
                        				val |= ((uint32_t)cf.value.bytes.ptr[b]) << (b * 8);
                        			}
                        			cur_channel->hardware_id = val;
                        		} else {
                        			/* Longer string — use a simple hash/first-chars as ID */
                        			cur_channel->hardware_id = (uint32_t)cf.value.bytes.ptr[0];
                        		}
                        	} else if (cf.wire_type == WIRE_VARINT) {
                        		cur_channel->hardware_id = (uint32_t)cf.value.varint;
                        	}
                        	break;
                        case 3: /* template_ids (repeated varint) */
                            if (cf.wire_type == WIRE_VARINT && cur_channel->template_count < MAX_TEMPLATE_IDS) {
                                cur_channel->template_ids[cur_channel->template_count++] = (uint32_t)cf.value.varint;
                            }
                            break;
                        case 4: /* interval_ms */
                            cur_channel->interval_ms = (uint32_t)cf.value.varint;
                            break;
                        case 5: /* enabled */
                            cur_channel->enabled = cf.value.varint != 0;
                            break;
                        case 6: /* bus_type */
                            cur_channel->bus_type = (uint8_t)cf.value.varint;
                            break;
                        case 7: /* bus_config */
                            if (cf.wire_type == WIRE_LENGTH_DELIMITED && cf.value.bytes.ptr) {
                                size_t cpy = cf.value.bytes.len < sizeof(cur_channel->bus_config)
                                            ? cf.value.bytes.len : sizeof(cur_channel->bus_config);
                                memcpy(cur_channel->bus_config, cf.value.bytes.ptr, cpy);
                                cur_channel->bus_config_len = cpy;
                            }
                            break;
                        }
                    }
                }
            }
        }
        /* Field 5: DmaChannelConfig (repeated nested message) */
        else if (field.field_num == 5 && field.wire_type == WIRE_LENGTH_DELIMITED && field.value.bytes.ptr) {
            uint32_t dma_id = 0;
            bool enabled = true;
            char bind_to[DMA_BOUND_MAX] = {0};

            frame_decoder_t ddec;
            if (frame_decoder_init_sub(&ddec, field.value.bytes.ptr,
                                        field.value.bytes.len) == FRAME_OK) {
                frame_field_t df;
                while (frame_decoder_next(&ddec, &df) == FRAME_OK) {
                    switch (df.field_num) {
                    case 1: dma_id = (uint32_t)df.value.varint; break;
                    case 2: enabled = df.value.varint != 0; break;
                    case 3:
                        if (df.wire_type == WIRE_LENGTH_DELIMITED && df.value.bytes.ptr) {
                            size_t cpy = df.value.bytes.len < sizeof(bind_to) - 1
                                        ? df.value.bytes.len : sizeof(bind_to) - 1;
                            memcpy(bind_to, df.value.bytes.ptr, cpy);
                            bind_to[cpy] = '\0';
                        }
                        break;
                    }
                }
            }

            ESP_LOGI(TAG, "DmaChannelConfig: id=%lu enabled=%d bind_to='%s'",
                     (unsigned long)dma_id, enabled, bind_to);
            /* Store in manifest for in-memory access */
            if (s_manifest.dma_config_count < MAX_DMA_CONFIGS) {
                config_dma_channel_t *dc = &s_manifest.dma_configs[s_manifest.dma_config_count++];
                dc->dma_id = dma_id;
                dc->enabled = enabled;
                strncpy(dc->bind_to, bind_to, sizeof(dc->bind_to) - 1);
                dc->bind_to[sizeof(dc->bind_to) - 1] = '\0';
            }
            /* Apply to pool if available */
            if (s_dma_pool) {
                dma_pool_apply_config(s_dma_pool, dma_id, enabled, bind_to);
            } else {
                ESP_LOGD(TAG, "DmaChannelConfig stored (pool not yet injected)");
            }
        }
    }

    ESP_LOGD(TAG, "Parsed: manifest_id=%s, templates=%d, channels=%d",
             s_manifest.manifest_id, s_manifest.template_count, s_manifest.channel_count);
    
    /* Debug: dump channel details */
    for (int i = 0; i < s_manifest.channel_count; i++) {
        config_channel_t *ch = &s_manifest.channels[i];
        ESP_LOGD(TAG, "  channel[%d]: id=%lu hw_id=%lu interval=%lu enabled=%d bus_type=%u bus_config_len=%u template_count=%u",
                 i, (unsigned long)ch->id, (unsigned long)ch->hardware_id,
                 (unsigned long)ch->interval_ms, ch->enabled, ch->bus_type,
                 (unsigned)ch->bus_config_len, (unsigned)ch->template_count);
        for (int t = 0; t < ch->template_count; t++) {
            ESP_LOGD(TAG, "    template_id[%d]=%lu", t, (unsigned long)ch->template_ids[t]);
        }
    }

    return s_manifest.manifest_id[0] != '\0';
}

/* === v2.1 Sync: Epoch / Manifest ID persistence === */

uint64_t config_mgr_get_epoch(void)
{
    nvs_handle_t handle;
    esp_err_t err = nvs_open(NVS_NAMESPACE, NVS_READONLY, &handle);
    if (err != ESP_OK) {
        return 0;
    }

    uint64_t epoch = 0;
    err = nvs_get_u64(handle, NVS_KEY_CONFIG_EPOCH, &epoch);
    nvs_close(handle);

    if (err != ESP_OK) {
        return 0;  /* Not found or error */
    }

    return epoch;
}

bool config_mgr_has_manifest(void)
{
    /* Server is single source of truth.
     * Only report "has manifest" if we have an in-memory applied config.
     * Do NOT fall through to NVS — that's sync metadata, not active config. */
    return s_initialized
        && s_manifest.manifest_id[0] != '\0'
        && s_manifest.applied;
}

const char *config_mgr_get_manifest_id(void)
{
    /* In-memory manifest_id — set by apply_manifest() or set_manifest_id() */
    if (s_manifest.manifest_id[0] != '\0') {
        return s_manifest.manifest_id;
    }
    return NULL;
}

const char *config_mgr_get_last_known_manifest_id(void)
{
    /* Reads NVS for the Hello v2.1 protocol field.
     * This is NOT the same as "has active config" — it's "what config did I last see?".
     * Uses its own buffer to avoid the caching bug in the old get_manifest_id(). */
    static char buf[64];
    nvs_handle_t handle;
    esp_err_t err = nvs_open(NVS_NAMESPACE, NVS_READONLY, &handle);
    if (err != ESP_OK) return NULL;
    size_t len = sizeof(buf);
    err = nvs_get_str(handle, NVS_KEY_MANIFEST_ID, buf, &len);
    nvs_close(handle);
    if (err != ESP_OK || buf[0] == '\0') return NULL;
    return buf;
}

bool config_mgr_has_last_known_manifest(void)
{
    /* For Hello field 6 (nvs_has): did this device ever receive a config?
     * Checks NVS directly, independent of in-memory state. */
    return config_mgr_get_last_known_manifest_id() != NULL;
}

void config_mgr_set_epoch(uint64_t epoch)
{
    nvs_handle_t handle;
    esp_err_t err = nvs_open(NVS_NAMESPACE, NVS_READWRITE, &handle);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to open NVS for epoch: %s", esp_err_to_name(err));
        return;
    }

    err = nvs_set_u64(handle, NVS_KEY_CONFIG_EPOCH, epoch);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to save epoch: %s", esp_err_to_name(err));
    } else {
        ESP_LOGI(TAG, "Epoch saved: %llu", (unsigned long long)epoch);
    }

    nvs_commit(handle);
    nvs_close(handle);
}

void config_mgr_set_manifest_id(const char *id)
{
    if (id == NULL || id[0] == '\0') {
        return;
    }

    nvs_handle_t handle;
    esp_err_t err = nvs_open(NVS_NAMESPACE, NVS_READWRITE, &handle);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to open NVS for manifest_id: %s", esp_err_to_name(err));
        return;
    }

    err = nvs_set_str(handle, NVS_KEY_MANIFEST_ID, id);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to save manifest_id: %s", esp_err_to_name(err));
    } else {
        ESP_LOGI(TAG, "Manifest ID saved: %s", id);
    }

    nvs_commit(handle);
    nvs_close(handle);
}

void config_mgr_clear_epoch(void)
{
    nvs_handle_t handle;
    esp_err_t err = nvs_open(NVS_NAMESPACE, NVS_READWRITE, &handle);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to open NVS for clear: %s", esp_err_to_name(err));
        return;
    }

    /* Erase epoch and manifest_id keys */
    nvs_erase_key(handle, NVS_KEY_CONFIG_EPOCH);
    nvs_erase_key(handle, NVS_KEY_MANIFEST_ID);
    nvs_erase_key(handle, NVS_KEY_LAST_SYNC_TIME);

    nvs_commit(handle);
    nvs_close(handle);

    /* Clear in-memory state */
    memset(s_manifest.manifest_id, 0, sizeof(s_manifest.manifest_id));

    ESP_LOGI(TAG, "Epoch and manifest_id cleared from NVS");
}

