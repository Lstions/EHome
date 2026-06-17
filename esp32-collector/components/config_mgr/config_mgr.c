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

#define TAG "CONFIG"

#define NVS_NAMESPACE "config"
#define KEY_MANIFEST  "manifest"
#define KEY_HASH      "hash"
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
static uint32_t compute_crc32(const uint8_t *data, size_t len);

/* === Public API === */

void config_mgr_set_dma_pool(dma_pool_t *pool)
{
    s_dma_pool = pool;
}

void config_mgr_replay_dma_configs(void)
{
    if (!s_dma_pool) {
        ESP_LOGW(TAG, "replay_dma: pool not set, skipping");
        return;
    }
    for (int i = 0; i < s_manifest.dma_config_count; i++) {
        const config_dma_channel_t *dc = &s_manifest.dma_configs[i];
        ESP_LOGI(TAG, "Replaying DMA config: id=%lu enabled=%d bind_to='%s'",
                 (unsigned long)dc->dma_id, dc->enabled, dc->bind_to);
        dma_pool_apply_config(s_dma_pool, dc->dma_id, dc->enabled, dc->bind_to);
    }
    if (s_manifest.dma_config_count > 0) {
        ESP_LOGI(TAG, "Replayed %d DMA configs from NVS", s_manifest.dma_config_count);
    }
}

void config_mgr_init(void)
{
    if (s_initialized) {
        return;
    }

    ESP_LOGI(TAG, "Initializing config manager...");
    clear_manifest();
    s_initialized = true;
    /* NVS load is done explicitly in main.c after pool injection */
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

    /* Auto-save to NVS */
    config_mgr_save_to_nvs();

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

uint32_t config_mgr_get_hash(void)
{
    /* Compute CRC32 of manifest_id + template/channel IDs */
    uint8_t hash_buf[256];
    size_t pos = 0;

    /* Add manifest_id */
    size_t id_len = strlen(s_manifest.manifest_id);
    if (id_len > 0 && pos + id_len <= sizeof(hash_buf)) {
        memcpy(&hash_buf[pos], s_manifest.manifest_id, id_len);
        pos += id_len;
    }

    /* Add template IDs */
    for (int i = 0; i < s_manifest.template_count && pos + 4 <= sizeof(hash_buf); i++) {
        uint32_t tid = s_manifest.templates[i].id;
        hash_buf[pos++] = (tid >> 0) & 0xFF;
        hash_buf[pos++] = (tid >> 8) & 0xFF;
        hash_buf[pos++] = (tid >> 16) & 0xFF;
        hash_buf[pos++] = (tid >> 24) & 0xFF;
    }

    /* Add channel IDs */
    for (int i = 0; i < s_manifest.channel_count && pos + 4 <= sizeof(hash_buf); i++) {
        uint32_t cid = s_manifest.channels[i].id;
        hash_buf[pos++] = (cid >> 0) & 0xFF;
        hash_buf[pos++] = (cid >> 8) & 0xFF;
        hash_buf[pos++] = (cid >> 16) & 0xFF;
        hash_buf[pos++] = (cid >> 24) & 0xFF;
    }

    return compute_crc32(hash_buf, pos);
}

bool config_mgr_save_to_nvs(void)
{
    nvs_handle_t handle;
    esp_err_t err = nvs_open(NVS_NAMESPACE, NVS_READWRITE, &handle);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to open NVS: %s", esp_err_to_name(err));
        return false;
    }

    /* Save manifest as blob */
    err = nvs_set_blob(handle, KEY_MANIFEST, &s_manifest, sizeof(s_manifest));
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to save manifest: %s", esp_err_to_name(err));
        nvs_close(handle);
        return false;
    }

    /* Save hash */
    uint32_t hash = config_mgr_get_hash();
    err = nvs_set_u32(handle, KEY_HASH, hash);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to save hash: %s", esp_err_to_name(err));
        nvs_close(handle);
        return false;
    }

    err = nvs_commit(handle);
    nvs_close(handle);

    ESP_LOGI(TAG, "Config saved to NVS (hash=0x%08lX)", (unsigned long)hash);
    return err == ESP_OK;
}

bool config_mgr_load_from_nvs(void)
{
    nvs_handle_t handle;
    esp_err_t err = nvs_open(NVS_NAMESPACE, NVS_READONLY, &handle);
    if (err != ESP_OK) {
        return false;
    }

    /* Load manifest blob */
    size_t len = sizeof(s_manifest);
    err = nvs_get_blob(handle, KEY_MANIFEST, &s_manifest, &len);
    if (err != ESP_OK || len != sizeof(s_manifest)) {
        nvs_close(handle);
        return false;
    }

    /* Verify hash */
    uint32_t saved_hash;
    err = nvs_get_u32(handle, KEY_HASH, &saved_hash);
    nvs_close(handle);

    if (err == ESP_OK) {
        uint32_t computed_hash = config_mgr_get_hash();
        if (saved_hash != computed_hash) {
            ESP_LOGW(TAG, "Hash mismatch: saved=0x%08lX, computed=0x%08lX",
                     (unsigned long)saved_hash, (unsigned long)computed_hash);
        }
    }

    ESP_LOGI(TAG, "Config loaded from NVS: %s", s_manifest.manifest_id);
    s_manifest.applied = true;  /* loaded from NVS means it was previously applied */
    return true;
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
    ESP_LOGI(TAG, "parse_manifest raw len=%d first_bytes=%02x %02x %02x %02x %02x",
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
            ESP_LOGI(TAG, "Found channel field: len=%d", (int)field.value.bytes.len);
            if (s_manifest.channel_count < MAX_CHANNELS) {
                config_channel_t *cur_channel = &s_manifest.channels[s_manifest.channel_count++];
                /* Parse nested channel fields */
                frame_decoder_t cdec;
                frame_err_t ci_err = frame_decoder_init_sub(&cdec, field.value.bytes.ptr, field.value.bytes.len);
                ESP_LOGI(TAG, "Channel decoder init: err=%d", ci_err);
                if (ci_err == FRAME_OK) {
                    frame_field_t cf;
                    while (frame_decoder_next(&cdec, &cf) == FRAME_OK) {
                        ESP_LOGI("CONFIG", "  cf: field=%d wire=%d varint=%llu bytes_len=%d",
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
            /* Store in manifest for NVS persistence + replay */
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
        ESP_LOGI(TAG, "  channel[%d]: id=%lu hw_id=%lu interval=%lu enabled=%d bus_type=%u bus_config_len=%u template_count=%u",
                 i, (unsigned long)ch->id, (unsigned long)ch->hardware_id,
                 (unsigned long)ch->interval_ms, ch->enabled, ch->bus_type,
                 (unsigned)ch->bus_config_len, (unsigned)ch->template_count);
        for (int t = 0; t < ch->template_count; t++) {
            ESP_LOGI(TAG, "    template_id[%d]=%lu", t, (unsigned long)ch->template_ids[t]);
        }
    }

    return s_manifest.manifest_id[0] != '\0';
}

/* CRC32 lookup table */
static const uint32_t crc32_table[256] = {
    0x00000000, 0x77073096, 0xEE0E612C, 0x990951BA,
    0x076DC419, 0x706AF48F, 0xE963A535, 0x9E6495A3,
    0x0EDB8832, 0x79DCB8A4, 0xE0D5E91E, 0x97D2D988,
    0x09B64C2B, 0x7EB17CBD, 0xE7B82D07, 0x90BF1D91,
    0x1DB71064, 0x6AB020F2, 0xF3B97148, 0x84BE41DE,
    0x1ADAD47D, 0x6DDDE4EB, 0xF4D4B551, 0x83D385C7,
    0x136C9856, 0x646BA8C0, 0xFD62F97A, 0x8A65C9EC,
    0x14015C4F, 0x63066CD9, 0xFA0F3D63, 0x8D080DF5,
    0x3B6E20C8, 0x4C69105E, 0xD56041E4, 0xA2677172,
    0x3C03E4D1, 0x4B04D447, 0xD20D85FD, 0xA50AB56B,
    0x35B5A8FA, 0x42B2986C, 0xDBBBC9D6, 0xACBCF940,
    0x32D86CE3, 0x45DF5C75, 0xDCD60DCF, 0xABD13D59,
    0x26D930AC, 0x51DE003A, 0xC8D75180, 0xBFD06116,
    0x21B4F4B5, 0x56B3C423, 0xCFBA9599, 0xB8BDA50F,
    0x2802B89E, 0x5F058808, 0xC60CD9B2, 0xB10BE924,
    0x2F6F7C87, 0x58684C11, 0xC1611DAB, 0xB6662D3D,
    0x76DC4190, 0x01DB7106, 0x98D220BC, 0xEFD5102A,
    0x71B18589, 0x06B6B51F, 0x9FBFE4A5, 0xE8B8D433,
    0x7807C9A2, 0x0F00F934, 0x9609A88E, 0xE10E9818,
    0x7F6A0DBB, 0x086D3D2D, 0x91646C97, 0xE6635C01,
    0x6B6B51F4, 0x1C6C6162, 0x856530D8, 0xF262004E,
    0x6C0695ED, 0x1B01A57B, 0x8208F4C1, 0xF50FC457,
    0x65B0D9C6, 0x12B7E950, 0x8BBEB8EA, 0xFCB9887C,
    0x62DD1DDF, 0x15DA2D49, 0x8CD37CF3, 0xFBD44C65,
    0x4DB26158, 0x3AB551CE, 0xA3BC0074, 0xD4BB30E2,
    0x4ADFA541, 0x3DD895D7, 0xA4D1C46D, 0xD3D6F4FB,
    0x4369E96A, 0x346ED9FC, 0xAD678846, 0xDA60B8D0,
    0x44042D73, 0x33031DE5, 0xAA0A4C5F, 0xDD0D7CC9,
    0x5005713C, 0x270241AA, 0xBE0B1010, 0xC90C2086,
    0x5768B525, 0x206F85B3, 0xB966D409, 0xCE61E49F,
    0x5EDEF90E, 0x29D9C998, 0xB0D09822, 0xC7D7A8B4,
    0x59B33D17, 0x2EB40D81, 0xB7BD5C3B, 0xC0BA6CAD,
    0xEDB88320, 0x9ABFB3B6, 0x03B6E20C, 0x74B1D29A,
    0xEAD54739, 0x9DD277AF, 0x04DB2615, 0x73DC1683,
    0xE3630B12, 0x94643B84, 0x0D6D6A3E, 0x7A6A5AA8,
    0xE40ECF0B, 0x9309FF9D, 0x0A00AE27, 0x7D079EB1,
    0xF00F9344, 0x8708A3D2, 0x1E01F268, 0x6906C2FE,
    0xF762575D, 0x806567CB, 0x196C3671, 0x6E6B06E7,
    0xFED41B76, 0x89D32BE0, 0x10DA7A5A, 0x67DD4ACC,
    0xF9B9DF6F, 0x8EBEEFF9, 0x17B7BE43, 0x60B08ED5,
    0xD6D6A3E8, 0xA1D1937E, 0x38D8C2C4, 0x4FDFF252,
    0xD1BB67F1, 0xA6BC5767, 0x3FB506DD, 0x48B2364B,
    0xD80D2BDA, 0xAF0A1B4C, 0x36034AF6, 0x41047A60,
    0xDF60EFC3, 0xA867DF55, 0x316E8EEF, 0x4669BE79,
    0xCB61B38C, 0xBC66831A, 0x256FD2A0, 0x5268E236,
    0xCC0C7795, 0xBB0B4703, 0x220216B9, 0x5505262F,
    0xC5BA3BBE, 0xB2BD0B28, 0x2BB45A92, 0x5CB36A04,
    0xC2D7FFA7, 0xB5D0CF31, 0x2CD99E8B, 0x5BDEAE1D,
    0x9B64C2B0, 0xEC63F226, 0x756AA39C, 0x026D930A,
    0x9C0906A9, 0xEB0E363F, 0x72076785, 0x05005713,
    0x95BF4A82, 0xE2B87A14, 0x7BB12BAE, 0x0CB61B38,
    0x92D28E9B, 0xE5D5BE0D, 0x7CDCEFB7, 0x0BDBDF21,
    0x86D3D2D4, 0xF1D4E242, 0x68DDB3F8, 0x1FDA836E,
    0x81BE16CD, 0xF6B9265B, 0x6FB077E1, 0x18B74777,
    0x88085AE6, 0xFF0F6A70, 0x66063BCA, 0x11010B5C,
    0x8F659EFF, 0xF862AE69, 0x616BFFD3, 0x166CCF45,
    0xA00AE278, 0xD70DD2EE, 0x4E048354, 0x3903B3C2,
    0xA7672661, 0xD06016F7, 0x4969474D, 0x3E6E77DB,
    0xAED16A4A, 0xD9D65ADC, 0x40DF0B66, 0x37D83BF0,
    0xA9BCAE53, 0xDEBB9EC5, 0x47B2CF7F, 0x30B5FFE9,
    0xBDBDF21C, 0xCABAC28A, 0x53B39330, 0x24B4A3A6,
    0xBAD03605, 0xCDD706B3, 0x54DE5729, 0x23D967BF,
    0xB3667A2E, 0xC4614AB8, 0x5D681B02, 0x2A6F2B94,
    0xB40BBE37, 0xC30C8EA1, 0x5A05DF1B, 0x2D02EF8D
};

static uint32_t compute_crc32(const uint8_t *data, size_t len)
{
    uint32_t crc = 0xFFFFFFFF;
    for (size_t i = 0; i < len; i++) {
        crc = crc32_table[(crc ^ data[i]) & 0xFF] ^ (crc >> 8);
    }
    return crc ^ 0xFFFFFFFF;
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
    /* Check if manifest_id exists and is non-empty */
    const char *mid = config_mgr_get_manifest_id();
    return (mid != NULL && mid[0] != '\0');
}

const char *config_mgr_get_manifest_id(void)
{
    /* First check in-memory manifest */
    if (s_manifest.manifest_id[0] != '\0') {
        return s_manifest.manifest_id;
    }

    /* Try to load from NVS */
    static char cached_manifest_id[64] = {0};
    if (cached_manifest_id[0] != '\0') {
        return cached_manifest_id;
    }

    nvs_handle_t handle;
    esp_err_t err = nvs_open(NVS_NAMESPACE, NVS_READONLY, &handle);
    if (err != ESP_OK) {
        return NULL;
    }

    size_t len = sizeof(cached_manifest_id);
    err = nvs_get_str(handle, NVS_KEY_MANIFEST_ID, cached_manifest_id, &len);
    nvs_close(handle);

    if (err != ESP_OK || cached_manifest_id[0] == '\0') {
        return NULL;
    }

    return cached_manifest_id;
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

/* === v2.4 bus_config flags helpers === */

bool bus_config_get_dma_enabled(const config_channel_t *ch)
{
    if (!ch) return true;

    size_t flags_offset;
    switch (ch->bus_type) {
    case 1: flags_offset = 6; break; /* UART: [tx, rx, baud×4, flags] */
    case 2: flags_offset = 7; break; /* I2C:   [sda, scl, addr, freq×4, flags] */
    case 3: flags_offset = 6; break; /* SPI:   [cs, mode, freq×4, flags] */
    default: return true;
    }

    if (ch->bus_config_len > flags_offset) {
        return (ch->bus_config[flags_offset] & 0x01) != 0;
    }
    return true;  /* default: DMA enabled for backward compatibility */
}
