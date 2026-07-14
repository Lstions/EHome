/**
 * @file config_mgr.c
 * @brief Configuration Manager Implementation
 */

#include "config_mgr.h"
#include "esp_log.h"
#include "nvs_flash.h"
#include "frame_codec.h"
#include "dma_pool.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include <string.h>
#include <stdlib.h>

#define TAG "CONFIG"

#define NVS_NAMESPACE "config"
/* v2.1 sync keys */
#define NVS_KEY_CONFIG_EPOCH   "cfg_epoch"     /* uint64 */
#define NVS_KEY_MANIFEST_ID    "manifest_id"   /* string */
#define NVS_KEY_LAST_SYNC_TIME "last_sync"     /* uint32 */

/* State — double-buffer for TOCTOU-safe concurrent access */
static config_manifest_t s_manifests[2];
static volatile int s_active_idx = 0;  /* Xtensa: single-word read/write is atomic */
static SemaphoreHandle_t s_mutex = NULL;
static bool s_initialized = false;
static dma_pool_t *s_dma_pool = NULL;  /* Injected via setter (DIP) */

/* Forward declarations */
static void clear_manifest(void);
static bool parse_manifest(config_manifest_t *target, const uint8_t *data, size_t len);
static void parse_field_manifest_id(config_manifest_t *target, const frame_field_t *field);
static void parse_field_template(config_manifest_t *target, const frame_field_t *field);
static void parse_field_channel(config_manifest_t *target, const frame_field_t *field);
static void parse_field_dma_channel(config_manifest_t *target, const frame_field_t *field);
static void parse_field_log_stream(config_manifest_t *target, const frame_field_t *field);
static void parse_field_gpio_config(config_manifest_t *target, const frame_field_t *field);
static void parse_field_pwm_config(config_manifest_t *target, const frame_field_t *field);
static void parse_edge_device(config_channel_t *cur_channel, const uint8_t *data, size_t len);
static void parse_command(config_edge_device_t *cur_ed, const uint8_t *data, size_t len);
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
    s_mutex = xSemaphoreCreateMutex();
    memset(s_manifests, 0, sizeof(s_manifests));
    s_active_idx = 0;
    s_initialized = true;
    /* Server is single source of truth — no NVS load at boot */
}

/* Double-buffer helpers */
static config_manifest_t *active_manifest(void) { return &s_manifests[s_active_idx]; }
static config_manifest_t *inactive_manifest(void) { return &s_manifests[1 - s_active_idx]; }

bool config_mgr_apply_manifest(const uint8_t *data, size_t len)
{
    if (data == NULL || len < 1) {
        ESP_LOGE(TAG, "Invalid manifest data");
        return false;
    }

    /* Parse into inactive buffer (readers still see old config) */
    config_manifest_t *target = inactive_manifest();
    memset(target, 0, sizeof(*target));

    if (!parse_manifest(target, data, len)) {
        ESP_LOGE(TAG, "Failed to parse manifest");
        memset(target, 0, sizeof(*target));  /* clean up failed parse */
        return false;
    }

    target->applied = true;

    /* Atomic switch — hold lock < 1us, only swap index */
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    s_active_idx = 1 - s_active_idx;
    xSemaphoreGive(s_mutex);

    ESP_LOGD(TAG, "Applied manifest: %s, templates=%d, channels=%d",
             target->manifest_id, target->template_count, target->channel_count);
    return true;
}

const config_manifest_t *config_mgr_get_manifest(void)
{
    return s_initialized ? active_manifest() : NULL;
}

const config_template_t *config_mgr_get_template(uint32_t id)
{
    const config_manifest_t *m = active_manifest();
    for (int i = 0; i < m->template_count; i++) {
        if (m->templates[i].id == id) {
            return &m->templates[i];
        }
    }
    return NULL;
}

const config_channel_t *config_mgr_get_channel(uint8_t index)
{
    const config_manifest_t *m = active_manifest();
    if (index >= m->channel_count) {
        return NULL;
    }
    return &m->channels[index];
}

uint8_t config_mgr_get_active_channel_count(void)
{
    const config_manifest_t *m = active_manifest();
    uint8_t count = 0;
    for (int i = 0; i < m->channel_count; i++) {
        if (m->channels[i].enabled) {
            count++;
        }
    }
    return count;
}

/* === Long-lock API (for app_callbacks handle_config_applied) === */

void config_mgr_lock(void)
{
    xSemaphoreTake(s_mutex, portMAX_DELAY);
}

void config_mgr_unlock(void)
{
    xSemaphoreGive(s_mutex);
}

/* === Internal === */

static void clear_manifest(void)
{
    memset(s_manifests, 0, sizeof(s_manifests));
}

static void parse_field_manifest_id(config_manifest_t *target, const frame_field_t *field)
{
    if (field->field_num == 1 && field->wire_type == WIRE_LENGTH_DELIMITED && field->value.bytes.ptr) {
        size_t copy_len = field->value.bytes.len < sizeof(target->manifest_id) - 1
                        ? field->value.bytes.len : sizeof(target->manifest_id) - 1;
        memcpy(target->manifest_id, field->value.bytes.ptr, copy_len);
        target->manifest_id[copy_len] = '\0';
    }
}

static void parse_template_fields(config_template_t *cur_template, const uint8_t *data, size_t len)
{
    frame_decoder_t tdec;
    if (frame_decoder_init_sub(&tdec, data, len) == FRAME_OK) {
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

static void parse_field_template(config_manifest_t *target, const frame_field_t *field)
{
    if (field->field_num == 3 && field->wire_type == WIRE_LENGTH_DELIMITED) {
        if (target->template_count < MAX_TEMPLATES) {
            config_template_t *cur_template = &target->templates[target->template_count++];
            parse_template_fields(cur_template, field->value.bytes.ptr, field->value.bytes.len);
        }
    }
}

static void parse_channel_fields(config_channel_t *cur_channel, const uint8_t *data, size_t len)
{
    frame_decoder_t cdec;
    frame_err_t ci_err = frame_decoder_init_sub(&cdec, data, len);
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
            case 2: /* hardware_id */
                if (cf.wire_type == WIRE_LENGTH_DELIMITED && cf.value.bytes.ptr) {
                    if (cf.value.bytes.len >= 1 && cf.value.bytes.len <= 4) {
                        uint32_t val = 0;
                        for (size_t b = 0; b < cf.value.bytes.len; b++) {
                            val |= ((uint32_t)cf.value.bytes.ptr[b]) << (b * 8);
                        }
                        cur_channel->hardware_id = val;
                    } else {
                        cur_channel->hardware_id = (uint32_t)cf.value.bytes.ptr[0];
                    }
                } else if (cf.wire_type == WIRE_VARINT) {
                    cur_channel->hardware_id = (uint32_t)cf.value.varint;
                }
                break;
            case 3: /* template_ids */
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
            case 9: /* edge_device_groups */
                if (cf.wire_type == WIRE_LENGTH_DELIMITED && cf.value.bytes.ptr
                    && cur_channel->edge_device_count < MAX_EDGE_DEVICES_PER_CH) {
                    ESP_LOGI(TAG, "Parsed edge_device: ch_id=%lu, ed_count=%d, bytes_len=%d",
                             (unsigned long)cur_channel->id, cur_channel->edge_device_count, (int)cf.value.bytes.len);
                    parse_edge_device(cur_channel, cf.value.bytes.ptr, cf.value.bytes.len);
                }
                break;
            }
        }
    }
}

static void parse_field_channel(config_manifest_t *target, const frame_field_t *field)
{
    if (field->field_num == 4 && field->wire_type == WIRE_LENGTH_DELIMITED && field->value.bytes.ptr) {
        ESP_LOGD(TAG, "Found channel field: len=%d", (int)field->value.bytes.len);
        if (target->channel_count < MAX_CHANNELS) {
            config_channel_t *cur_channel = &target->channels[target->channel_count++];
            parse_channel_fields(cur_channel, field->value.bytes.ptr, field->value.bytes.len);
        }
    }
}

static void parse_command(config_edge_device_t *cur_ed, const uint8_t *data, size_t len)
{
    if (cur_ed->command_count >= MAX_COMMANDS_PER_DEVICE) return;

    config_command_t *cur_cmd = &cur_ed->commands[cur_ed->command_count++];

    frame_decoder_t cdec2;
    if (frame_decoder_init_sub(&cdec2, data, len) == FRAME_OK) {
        frame_field_t cf2;
        while (frame_decoder_next(&cdec2, &cf2) == FRAME_OK) {
            switch (cf2.field_num) {
            case 1: /* template_id */
                cur_cmd->template_id = (uint32_t)cf2.value.varint;
                break;
            case 2: /* interval_ms */
                cur_cmd->interval_ms = (uint32_t)cf2.value.varint;
                break;
            case 3: /* enabled */
                cur_cmd->enabled = cf2.value.varint != 0;
                break;
            }
        }
    }
}

static void parse_edge_device_fields(config_edge_device_t *cur_ed, const uint8_t *data, size_t len)
{
    frame_decoder_t edec;
    if (frame_decoder_init_sub(&edec, data, len) == FRAME_OK) {
        frame_field_t ef;
        while (frame_decoder_next(&edec, &ef) == FRAME_OK) {
            switch (ef.field_num) {
            case 1: /* edge_device_id */
                cur_ed->edge_device_id = (uint32_t)ef.value.varint;
                ESP_LOGI(TAG, "  ed.f1: edge_device_id=%lu", (unsigned long)cur_ed->edge_device_id);
                break;
            case 2: /* hardware_id */
                cur_ed->hardware_id = (uint32_t)ef.value.varint;
                break;
            case 3: /* commands */
                if (ef.wire_type == WIRE_LENGTH_DELIMITED && ef.value.bytes.ptr) {
                    parse_command(cur_ed, ef.value.bytes.ptr, ef.value.bytes.len);
                }
                break;
            }
        }
    }
}

static void parse_edge_device(config_channel_t *cur_channel, const uint8_t *data, size_t len)
{
    config_edge_device_t *cur_ed = &cur_channel->edge_devices[cur_channel->edge_device_count++];
    parse_edge_device_fields(cur_ed, data, len);
}

static void parse_dma_channel_fields(uint32_t *dma_id, bool *enabled, char *bind_to, size_t bind_to_size,
                                     const uint8_t *data, size_t len)
{
    frame_decoder_t ddec;
    if (frame_decoder_init_sub(&ddec, data, len) == FRAME_OK) {
        frame_field_t df;
        while (frame_decoder_next(&ddec, &df) == FRAME_OK) {
            switch (df.field_num) {
            case 1: *dma_id = (uint32_t)df.value.varint; break;
            case 2: *enabled = df.value.varint != 0; break;
            case 3:
                if (df.wire_type == WIRE_LENGTH_DELIMITED && df.value.bytes.ptr) {
                    size_t cpy = df.value.bytes.len < bind_to_size - 1
                                ? df.value.bytes.len : bind_to_size - 1;
                    memcpy(bind_to, df.value.bytes.ptr, cpy);
                    bind_to[cpy] = '\0';
                }
                break;
            }
        }
    }
}

static void parse_field_dma_channel(config_manifest_t *target, const frame_field_t *field)
{
    if (field->field_num == 5 && field->wire_type == WIRE_LENGTH_DELIMITED && field->value.bytes.ptr) {
        uint32_t dma_id = 0;
        bool enabled = true;
        char bind_to[DMA_BOUND_MAX] = {0};

        parse_dma_channel_fields(&dma_id, &enabled, bind_to, sizeof(bind_to),
                                field->value.bytes.ptr, field->value.bytes.len);

        ESP_LOGI(TAG, "DmaChannelConfig: id=%lu enabled=%d bind_to='%s'",
                 (unsigned long)dma_id, enabled, bind_to);

        if (target->dma_config_count < MAX_DMA_CONFIGS) {
            config_dma_channel_t *dc = &target->dma_configs[target->dma_config_count++];
            dc->dma_id = dma_id;
            dc->enabled = enabled;
            strncpy(dc->bind_to, bind_to, sizeof(dc->bind_to) - 1);
            dc->bind_to[sizeof(dc->bind_to) - 1] = '\0';
        }
    }
}

/* === v2.5: Log stream config (field 10) === */
static void parse_field_log_stream(config_manifest_t *target, const frame_field_t *field)
{
    if (field->field_num == 10 && field->wire_type == WIRE_LENGTH_DELIMITED && field->value.bytes.ptr) {
        frame_decoder_t sub;
        if (frame_decoder_init_sub(&sub, field->value.bytes.ptr, field->value.bytes.len) != FRAME_OK) return;
        frame_field_t sf;
        while (frame_decoder_next(&sub, &sf) == FRAME_OK) {
            switch (sf.field_num) {
            case 1: /* enabled */
                target->log_stream_enabled = sf.value.varint != 0;
                break;
            case 2: /* level */
                target->log_stream_level = (uint8_t)sf.value.varint;
                break;
            }
        }
        ESP_LOGI(TAG, "LogStream config: enabled=%d level=%d",
                 target->log_stream_enabled, target->log_stream_level);
    }
}

/* === v3.0: GPIO config (field 11) === */

static void parse_gpio_config_fields(config_gpio_t *cur, const uint8_t *data, size_t len)
{
    frame_decoder_t gdec;
    if (frame_decoder_init_sub(&gdec, data, len) == FRAME_OK) {
        frame_field_t gf;
        while (frame_decoder_next(&gdec, &gf) == FRAME_OK) {
            switch (gf.field_num) {
            case 1: cur->pin = (uint8_t)gf.value.varint; break;
            case 2: cur->direction = (uint8_t)gf.value.varint; break;
            case 3: cur->initial_level = (uint8_t)gf.value.varint; break;
            }
        }
    }
}

static void parse_field_gpio_config(config_manifest_t *target, const frame_field_t *field)
{
    if (field->field_num == 11 && field->wire_type == WIRE_LENGTH_DELIMITED && field->value.bytes.ptr) {
        if (target->gpio_config_count < MAX_GPIO_CONFIGS) {
            config_gpio_t *cur = &target->gpio_configs[target->gpio_config_count++];
            memset(cur, 0, sizeof(*cur));
            parse_gpio_config_fields(cur, field->value.bytes.ptr, field->value.bytes.len);
            ESP_LOGI(TAG, "GPIO config: pin=%d dir=%d init=%d", cur->pin, cur->direction, cur->initial_level);
        } else {
            ESP_LOGW(TAG, "GPIO config count overflow (max=%d)", MAX_GPIO_CONFIGS);
        }
    }
}

/* === v3.0: PWM config (field 12) === */

static void parse_pwm_config_fields(config_pwm_t *cur, const uint8_t *data, size_t len)
{
    frame_decoder_t pdec;
    if (frame_decoder_init_sub(&pdec, data, len) == FRAME_OK) {
        frame_field_t pf;
        while (frame_decoder_next(&pdec, &pf) == FRAME_OK) {
            switch (pf.field_num) {
            case 1: cur->pin = (uint8_t)pf.value.varint; break;
            case 2: cur->frequency = (uint32_t)pf.value.varint; break;
            case 3: cur->duty = (uint16_t)pf.value.varint; break;
            case 4: cur->resolution = (uint8_t)pf.value.varint; break;
            case 5: cur->auto_start = pf.value.varint != 0; break;
            }
        }
    }
}

static void parse_field_pwm_config(config_manifest_t *target, const frame_field_t *field)
{
    if (field->field_num == 12 && field->wire_type == WIRE_LENGTH_DELIMITED && field->value.bytes.ptr) {
        if (target->pwm_config_count < MAX_PWM_CONFIGS) {
            config_pwm_t *cur = &target->pwm_configs[target->pwm_config_count++];
            memset(cur, 0, sizeof(*cur));
            parse_pwm_config_fields(cur, field->value.bytes.ptr, field->value.bytes.len);
            ESP_LOGI(TAG, "PWM config: pin=%d freq=%lu duty=%u res=%d auto=%d",
                     cur->pin, (unsigned long)cur->frequency, cur->duty, cur->resolution, cur->auto_start);
        } else {
            ESP_LOGW(TAG, "PWM config count overflow (max=%d)", MAX_PWM_CONFIGS);
        }
    }
}

static void log_parsed_manifest(const config_manifest_t *target)
{
    ESP_LOGD(TAG, "Parsed: manifest_id=%s, templates=%d, channels=%d",
             target->manifest_id, target->template_count, target->channel_count);

    for (int i = 0; i < target->channel_count; i++) {
        const config_channel_t *ch = &target->channels[i];
        ESP_LOGD(TAG, "  channel[%d]: id=%lu hw_id=%lu interval=%lu enabled=%d bus_type=%u bus_config_len=%u template_count=%u",
                 i, (unsigned long)ch->id, (unsigned long)ch->hardware_id,
                 (unsigned long)ch->interval_ms, ch->enabled, ch->bus_type,
                 (unsigned)ch->bus_config_len, (unsigned)ch->template_count);
        ESP_LOGI(TAG, "  channel[%d]: edge_device_count=%d", i, ch->edge_device_count);
        for (int t = 0; t < ch->template_count; t++) {
            ESP_LOGD(TAG, "    template_id[%d]=%lu", t, (unsigned long)ch->template_ids[t]);
        }
    }
}

static bool parse_manifest(config_manifest_t *target, const uint8_t *data, size_t len)
{
    if (len < 1) return false;

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
        parse_field_manifest_id(target, &field);
        parse_field_template(target, &field);
        parse_field_channel(target, &field);
        parse_field_dma_channel(target, &field);
        parse_field_log_stream(target, &field);
        parse_field_gpio_config(target, &field);
        parse_field_pwm_config(target, &field);
    }

    log_parsed_manifest(target);
    return target->manifest_id[0] != '\0';
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
    if (!s_initialized) return false;
    const config_manifest_t *m = active_manifest();
    return m->manifest_id[0] != '\0' && m->applied;
}

const char *config_mgr_get_manifest_id(void)
{
    /* In-memory manifest_id — set by apply_manifest() or set_manifest_id() */
    const config_manifest_t *m = active_manifest();
    if (m->manifest_id[0] != '\0') {
        return m->manifest_id;
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

/* === v2.5: Log stream config getters === */
bool config_mgr_get_log_stream_enabled(void)
{
    if (!s_initialized) return false;
    return active_manifest()->log_stream_enabled;
}

uint8_t config_mgr_get_log_stream_level(void)
{
    if (!s_initialized) return 2; /* INFO default */
    return active_manifest()->log_stream_level;
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

    /* Clear in-memory manifest_id from active buffer */
    config_manifest_t *active = &s_manifests[s_active_idx];
    memset(active->manifest_id, 0, sizeof(active->manifest_id));

    ESP_LOGI(TAG, "Epoch and manifest_id cleared from NVS");
}

