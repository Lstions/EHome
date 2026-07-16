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
static int s_staged_idx = -1;
static SemaphoreHandle_t s_mutex = NULL;
static bool s_initialized = false;
static dma_pool_t *s_dma_pool = NULL;  /* Injected via setter (DIP) */

/* Forward declarations */
static void clear_manifest(void);
static bool parse_manifest(config_manifest_t *target, const uint8_t *data, size_t len);
static bool parse_field_manifest_id(config_manifest_t *target, const frame_field_t *field);
static bool parse_field_template(config_manifest_t *target, const frame_field_t *field);
static bool parse_field_channel(config_manifest_t *target, const frame_field_t *field);
static bool parse_field_dma_channel(config_manifest_t *target, const frame_field_t *field);
static bool parse_field_log_stream(config_manifest_t *target, const frame_field_t *field);
static bool parse_field_gpio_config(config_manifest_t *target, const frame_field_t *field);
static bool parse_field_pwm_config(config_manifest_t *target, const frame_field_t *field);
static bool validate_peripheral_pin_ownership(const config_manifest_t *target);
static bool parse_edge_device(config_channel_t *cur_channel, const uint8_t *data, size_t len);
static bool parse_command(config_edge_device_t *cur_ed, const uint8_t *data, size_t len);
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

bool config_mgr_stage_manifest(const uint8_t *data, size_t len)
{
    if (data == NULL || len < 1) {
        ESP_LOGE(TAG, "Invalid manifest data");
        return false;
    }

    /* Any previous staged candidate is superseded even when this parse fails. */
    s_staged_idx = -1;

    /* Parse into inactive buffer (readers still see old config) */
    config_manifest_t *target = inactive_manifest();
    memset(target, 0, sizeof(*target));

    if (!parse_manifest(target, data, len)) {
        ESP_LOGE(TAG, "Failed to parse manifest");
        memset(target, 0, sizeof(*target));  /* clean up failed parse */
        return false;
    }

    if (!validate_peripheral_pin_ownership(target)) {
        ESP_LOGE(TAG, "Manifest peripheral pin conflict");
        memset(target, 0, sizeof(*target));
        return false;
    }

    target->applied = false;
    s_staged_idx = 1 - s_active_idx;
    ESP_LOGD(TAG, "Staged manifest: %s, templates=%d, channels=%d",
             target->manifest_id, target->template_count, target->channel_count);
    return true;
}

const config_manifest_t *config_mgr_get_staged_manifest(void)
{
    return s_staged_idx >= 0 ? &s_manifests[s_staged_idx] : NULL;
}

bool config_mgr_commit_staged_manifest(void)
{
    if (s_staged_idx < 0) return false;
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    s_manifests[s_staged_idx].applied = true;
    s_active_idx = s_staged_idx;
    s_staged_idx = -1;
    xSemaphoreGive(s_mutex);
    return true;
}

void config_mgr_discard_staged_manifest(void)
{
    if (s_staged_idx >= 0) memset(&s_manifests[s_staged_idx], 0, sizeof(config_manifest_t));
    s_staged_idx = -1;
}

bool config_mgr_snapshot_active(config_manifest_t *out)
{
    if (!out || !s_initialized) return false;
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    memcpy(out, active_manifest(), sizeof(*out));
    xSemaphoreGive(s_mutex);
    return out->applied;
}

bool config_mgr_apply_manifest(const uint8_t *data, size_t len)
{
    return config_mgr_stage_manifest(data, len) && config_mgr_commit_staged_manifest();
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

static bool parse_field_manifest_id(config_manifest_t *target, const frame_field_t *field)
{
    if (field->wire_type != WIRE_LENGTH_DELIMITED || !field->value.bytes.ptr ||
        field->value.bytes.len >= sizeof(target->manifest_id)) return false;
    memcpy(target->manifest_id, field->value.bytes.ptr, field->value.bytes.len);
    target->manifest_id[field->value.bytes.len] = '\0';
    return true;
}

static bool parse_template_fields(config_template_t *cur_template, const uint8_t *data, size_t len)
{
    frame_decoder_t tdec;
    if (frame_decoder_init_sub(&tdec, data, len) != FRAME_OK) return false;
    frame_field_t tf;
    frame_err_t err;
    while ((err = frame_decoder_next(&tdec, &tf)) == FRAME_OK) {
            switch (tf.field_num) {
            case 1: /* id */
                if (tf.wire_type != WIRE_VARINT) return false;
                cur_template->id = (uint32_t)tf.value.varint;
                break;
            case 2: /* write_data */
                if (tf.wire_type != WIRE_LENGTH_DELIMITED || !tf.value.bytes.ptr ||
                    tf.value.bytes.len > sizeof(cur_template->write_data)) return false;
                memcpy(cur_template->write_data, tf.value.bytes.ptr, tf.value.bytes.len);
                cur_template->write_data_len = tf.value.bytes.len;
                break;
            case 3: /* read_length */
                if (tf.wire_type != WIRE_VARINT) return false;
                cur_template->read_length = (uint32_t)tf.value.varint;
                break;
            case 4: /* delay_ms */
                if (tf.wire_type != WIRE_VARINT) return false;
                cur_template->delay_ms = (uint32_t)tf.value.varint;
                break;
            }
    }
    return err == FRAME_DONE;
}

static bool parse_field_template(config_manifest_t *target, const frame_field_t *field)
{
    if (field->field_num != 3) return true;
    if (field->wire_type != WIRE_LENGTH_DELIMITED || !field->value.bytes.ptr) return false;
    if (target->template_count >= MAX_TEMPLATES) return false;
    config_template_t *cur_template = &target->templates[target->template_count];
    if (!parse_template_fields(cur_template, field->value.bytes.ptr, field->value.bytes.len)) return false;
    target->template_count++;
    return true;
}

static bool parse_channel_fields(config_channel_t *cur_channel, const uint8_t *data, size_t len)
{
    frame_decoder_t cdec;
    frame_err_t ci_err = frame_decoder_init_sub(&cdec, data, len);
    ESP_LOGD(TAG, "Channel decoder init: err=%d", ci_err);
    if (ci_err == FRAME_OK) {
        frame_field_t cf;
        frame_err_t err;
        while ((err = frame_decoder_next(&cdec, &cf)) == FRAME_OK) {
            ESP_LOGD(TAG, "  cf: field=%d wire=%d varint=%llu bytes_len=%d",
                     cf.field_num, cf.wire_type, cf.value.varint,
                     cf.wire_type == WIRE_LENGTH_DELIMITED ? (int)cf.value.bytes.len : 0);
            switch (cf.field_num) {
            case 1: /* id */
                if (cf.wire_type != WIRE_VARINT) return false;
                cur_channel->id = (uint32_t)cf.value.varint;
                break;
            case 2: /* hardware_id */
                if (cf.wire_type == WIRE_LENGTH_DELIMITED && cf.value.bytes.ptr) {
                    if (cf.value.bytes.len < 1 || cf.value.bytes.len > 4) return false;
                    uint32_t val = 0;
                    for (size_t b = 0; b < cf.value.bytes.len; b++) {
                        val |= ((uint32_t)cf.value.bytes.ptr[b]) << (b * 8);
                    }
                    cur_channel->hardware_id = val;
                } else if (cf.wire_type == WIRE_VARINT) {
                    cur_channel->hardware_id = (uint32_t)cf.value.varint;
                } else return false;
                break;
            case 3: /* template_ids */
                if (cf.wire_type != WIRE_VARINT) return false;
                if (cur_channel->template_count >= MAX_TEMPLATE_IDS) return false;
                cur_channel->template_ids[cur_channel->template_count++] = (uint32_t)cf.value.varint;
                break;
            case 4: /* interval_ms */
                if (cf.wire_type != WIRE_VARINT) return false;
                cur_channel->interval_ms = (uint32_t)cf.value.varint;
                break;
            case 5: /* enabled */
                if (cf.wire_type != WIRE_VARINT) return false;
                cur_channel->enabled = cf.value.varint != 0;
                break;
            case 6: /* bus_type */
                if (cf.wire_type != WIRE_VARINT) return false;
                cur_channel->bus_type = (uint8_t)cf.value.varint;
                break;
            case 7: /* bus_config */
                if (cf.wire_type != WIRE_LENGTH_DELIMITED || !cf.value.bytes.ptr ||
                    cf.value.bytes.len > sizeof(cur_channel->bus_config)) return false;
                memcpy(cur_channel->bus_config, cf.value.bytes.ptr, cf.value.bytes.len);
                cur_channel->bus_config_len = cf.value.bytes.len;
                break;
            case 9: /* edge_device_groups */
                if (cf.wire_type != WIRE_LENGTH_DELIMITED || !cf.value.bytes.ptr) return false;
                if (cur_channel->edge_device_count >= MAX_EDGE_DEVICES_PER_CH) return false;
                ESP_LOGI(TAG, "Parsed edge_device: ch_id=%lu, ed_count=%d, bytes_len=%d",
                         (unsigned long)cur_channel->id, cur_channel->edge_device_count, (int)cf.value.bytes.len);
                if (!parse_edge_device(cur_channel, cf.value.bytes.ptr, cf.value.bytes.len)) return false;
                break;
            }
        }
        return err == FRAME_DONE;
    }
    return false;
}

static bool parse_field_channel(config_manifest_t *target, const frame_field_t *field)
{
    if (field->field_num != 4) return true;
    if (field->wire_type != WIRE_LENGTH_DELIMITED || !field->value.bytes.ptr) return false;
    ESP_LOGD(TAG, "Found channel field: len=%d", (int)field->value.bytes.len);
    if (target->channel_count >= MAX_CHANNELS) return false;
    config_channel_t *cur_channel = &target->channels[target->channel_count];
    if (!parse_channel_fields(cur_channel, field->value.bytes.ptr, field->value.bytes.len)) return false;
    target->channel_count++;
    return true;
}

static bool parse_command(config_edge_device_t *cur_ed, const uint8_t *data, size_t len)
{
    if (cur_ed->command_count >= MAX_COMMANDS_PER_DEVICE) return false;

    config_command_t *cur_cmd = &cur_ed->commands[cur_ed->command_count];
    frame_decoder_t cdec2;
    if (frame_decoder_init_sub(&cdec2, data, len) != FRAME_OK) return false;
    frame_field_t cf2;
    frame_err_t err;
    while ((err = frame_decoder_next(&cdec2, &cf2)) == FRAME_OK) {
        switch (cf2.field_num) {
        case 1:
            if (cf2.wire_type != WIRE_VARINT) return false;
            cur_cmd->template_id = (uint32_t)cf2.value.varint;
            break;
        case 2:
            if (cf2.wire_type != WIRE_VARINT) return false;
            cur_cmd->interval_ms = (uint32_t)cf2.value.varint;
            break;
        case 3:
            if (cf2.wire_type != WIRE_VARINT) return false;
            cur_cmd->enabled = cf2.value.varint != 0;
            break;
        }
    }
    if (err != FRAME_DONE) return false;
    cur_ed->command_count++;
    return true;
}

static bool parse_edge_device_fields(config_edge_device_t *cur_ed, const uint8_t *data, size_t len)
{
    frame_decoder_t edec;
    if (frame_decoder_init_sub(&edec, data, len) != FRAME_OK) return false;
    frame_field_t ef;
    frame_err_t err;
    while ((err = frame_decoder_next(&edec, &ef)) == FRAME_OK) {
        switch (ef.field_num) {
        case 1:
            if (ef.wire_type != WIRE_VARINT) return false;
            cur_ed->edge_device_id = (uint32_t)ef.value.varint;
            ESP_LOGI(TAG, "  ed.f1: edge_device_id=%lu", (unsigned long)cur_ed->edge_device_id);
            break;
        case 2:
            if (ef.wire_type != WIRE_VARINT) return false;
            cur_ed->hardware_id = (uint32_t)ef.value.varint;
            break;
        case 3:
            if (ef.wire_type != WIRE_LENGTH_DELIMITED || !ef.value.bytes.ptr) return false;
            if (!parse_command(cur_ed, ef.value.bytes.ptr, ef.value.bytes.len)) return false;
            break;
        }
    }
    return err == FRAME_DONE;
}

static bool parse_edge_device(config_channel_t *cur_channel, const uint8_t *data, size_t len)
{
    config_edge_device_t *cur_ed = &cur_channel->edge_devices[cur_channel->edge_device_count];
    if (!parse_edge_device_fields(cur_ed, data, len)) return false;
    cur_channel->edge_device_count++;
    return true;
}

static bool parse_dma_channel_fields(uint32_t *dma_id, bool *enabled, char *bind_to, size_t bind_to_size,
                                     const uint8_t *data, size_t len)
{
    frame_decoder_t ddec;
    if (frame_decoder_init_sub(&ddec, data, len) != FRAME_OK) return false;
    frame_field_t df;
    frame_err_t err;
    while ((err = frame_decoder_next(&ddec, &df)) == FRAME_OK) {
        switch (df.field_num) {
        case 1:
            if (df.wire_type != WIRE_VARINT) return false;
            *dma_id = (uint32_t)df.value.varint;
            break;
        case 2:
            if (df.wire_type != WIRE_VARINT) return false;
            *enabled = df.value.varint != 0;
            break;
        case 3:
            if (df.wire_type != WIRE_LENGTH_DELIMITED || !df.value.bytes.ptr ||
                df.value.bytes.len >= bind_to_size) return false;
            memcpy(bind_to, df.value.bytes.ptr, df.value.bytes.len);
            bind_to[df.value.bytes.len] = '\0';
            break;
        }
    }
    return err == FRAME_DONE;
}

static bool parse_field_dma_channel(config_manifest_t *target, const frame_field_t *field)
{
    if (field->field_num != 5) return true;
    if (field->wire_type != WIRE_LENGTH_DELIMITED || !field->value.bytes.ptr) return false;
    if (target->dma_config_count >= MAX_DMA_CONFIGS) return false;
    uint32_t dma_id = 0;
    bool enabled = true;
    char bind_to[DMA_BOUND_MAX] = {0};
    if (!parse_dma_channel_fields(&dma_id, &enabled, bind_to, sizeof(bind_to),
                                  field->value.bytes.ptr, field->value.bytes.len)) return false;

    ESP_LOGI(TAG, "DmaChannelConfig: id=%lu enabled=%d bind_to='%s'",
             (unsigned long)dma_id, enabled, bind_to);
    config_dma_channel_t *dc = &target->dma_configs[target->dma_config_count++];
    dc->dma_id = dma_id;
    dc->enabled = enabled;
    memcpy(dc->bind_to, bind_to, sizeof(dc->bind_to));
    return true;
}

/* === v2.5: Log stream config (field 10) === */
static bool parse_field_log_stream(config_manifest_t *target, const frame_field_t *field)
{
    if (field->field_num != 10) return true;
    if (field->wire_type != WIRE_LENGTH_DELIMITED || !field->value.bytes.ptr) return false;
    frame_decoder_t sub;
    if (frame_decoder_init_sub(&sub, field->value.bytes.ptr, field->value.bytes.len) != FRAME_OK) return false;
    frame_field_t sf;
    frame_err_t err;
    while ((err = frame_decoder_next(&sub, &sf)) == FRAME_OK) {
        switch (sf.field_num) {
        case 1:
            if (sf.wire_type != WIRE_VARINT) return false;
            target->log_stream_enabled = sf.value.varint != 0;
            break;
        case 2:
            if (sf.wire_type != WIRE_VARINT) return false;
            target->log_stream_level = (uint8_t)sf.value.varint;
            break;
        }
    }
    if (err != FRAME_DONE) return false;
    ESP_LOGI(TAG, "LogStream config: enabled=%d level=%d",
             target->log_stream_enabled, target->log_stream_level);
    return true;
}

/* === v3.0: GPIO config (field 11) === */

static bool parse_gpio_config_fields(config_gpio_t *cur, const uint8_t *data, size_t len)
{
    frame_decoder_t gdec;
    if (frame_decoder_init_sub(&gdec, data, len) != FRAME_OK) return false;
    frame_field_t gf;
    frame_err_t err;
    uint32_t seen = 0;
    while ((err = frame_decoder_next(&gdec, &gf)) == FRAME_OK) {
        if (gf.field_num < 1 || gf.field_num > 3 || (seen & (1U << gf.field_num))) return false;
        seen |= 1U << gf.field_num;
        switch (gf.field_num) {
        case 1:
            if (gf.wire_type != WIRE_VARINT || gf.value.varint > UINT8_MAX) return false;
            cur->pin = (uint8_t)gf.value.varint;
            break;
        case 2:
            if (gf.wire_type != WIRE_VARINT || gf.value.varint > UINT8_MAX) return false;
            cur->direction = (uint8_t)gf.value.varint;
            break;
        case 3:
            if (gf.wire_type != WIRE_VARINT || gf.value.varint > 1) return false;
            cur->initial_level = (uint8_t)gf.value.varint;
            break;
        }
    }
    return err == FRAME_DONE && (seen & 0x0eU) == 0x0eU;
}

static bool parse_field_gpio_config(config_manifest_t *target, const frame_field_t *field)
{
    if (field->field_num != 11) return true;
    if (field->wire_type != WIRE_LENGTH_DELIMITED || !field->value.bytes.ptr) return false;
    if (target->gpio_config_count >= MAX_GPIO_CONFIGS) return false;
    config_gpio_t *cur = &target->gpio_configs[target->gpio_config_count];
    memset(cur, 0, sizeof(*cur));
    if (!parse_gpio_config_fields(cur, field->value.bytes.ptr, field->value.bytes.len)) return false;
    target->gpio_config_count++;
    ESP_LOGI(TAG, "GPIO config: pin=%d dir=%d init=%d", cur->pin, cur->direction, cur->initial_level);
    return true;
}

/* === v3.0: PWM config (field 12) === */

static bool parse_pwm_config_fields(config_pwm_t *cur, const uint8_t *data, size_t len)
{
    frame_decoder_t pdec;
    if (frame_decoder_init_sub(&pdec, data, len) != FRAME_OK) return false;
    frame_field_t pf;
    frame_err_t err;
    uint32_t seen = 0;
    while ((err = frame_decoder_next(&pdec, &pf)) == FRAME_OK) {
        if (pf.field_num < 1 || pf.field_num > 6 || (seen & (1U << pf.field_num))) return false;
        seen |= 1U << pf.field_num;
        switch (pf.field_num) {
        case 1:
            if (pf.wire_type != WIRE_VARINT || pf.value.varint > UINT8_MAX) return false;
            cur->channel = (uint8_t)pf.value.varint;
            break;
        case 2:
            if (pf.wire_type != WIRE_VARINT || pf.value.varint > UINT8_MAX) return false;
            cur->pin = (uint8_t)pf.value.varint;
            break;
        case 3:
            if (pf.wire_type != WIRE_VARINT || pf.value.varint > UINT32_MAX) return false;
            cur->frequency = (uint32_t)pf.value.varint;
            break;
        case 4:
            if (pf.wire_type != WIRE_VARINT || pf.value.varint > UINT16_MAX) return false;
            cur->duty = (uint16_t)pf.value.varint;
            break;
        case 5:
            if (pf.wire_type != WIRE_VARINT || pf.value.varint > UINT8_MAX) return false;
            cur->resolution = (uint8_t)pf.value.varint;
            break;
        case 6:
            if (pf.wire_type != WIRE_VARINT || pf.value.varint > 1) return false;
            cur->auto_start = pf.value.varint != 0;
            break;
        }
    }
    return err == FRAME_DONE && (seen & 0x7eU) == 0x7eU;
}

static bool parse_field_pwm_config(config_manifest_t *target, const frame_field_t *field)
{
    if (field->field_num != 12) return true;
    if (field->wire_type != WIRE_LENGTH_DELIMITED || !field->value.bytes.ptr) return false;
    if (target->pwm_config_count >= MAX_PWM_CONFIGS) return false;
    config_pwm_t *cur = &target->pwm_configs[target->pwm_config_count];
    memset(cur, 0, sizeof(*cur));
    if (!parse_pwm_config_fields(cur, field->value.bytes.ptr, field->value.bytes.len)) return false;
    target->pwm_config_count++;
    ESP_LOGI(TAG, "PWM config: channel=%d pin=%d freq=%lu duty=%u res=%d auto=%d",
             cur->channel, cur->pin, (unsigned long)cur->frequency,
             cur->duty, cur->resolution, cur->auto_start);
    return true;
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

static bool channel_uses_pin(const config_channel_t *channel, uint8_t pin)
{
    if (!channel->enabled) return false;
    const uint8_t *cfg = channel->bus_config;
    size_t len = channel->bus_config_len;
    if (channel->bus_type == 1 || channel->bus_type == 2) {
        return len >= 2 && (cfg[0] == pin || cfg[1] == pin);
    }
    if (channel->bus_type == 3) {
        if (len >= 6 && cfg[0] == pin) return true;
        return len >= 9 && (cfg[6] == pin || cfg[7] == pin || cfg[8] == pin);
    }
    return false;
}

static bool manifest_bus_uses_pin(const config_manifest_t *target, uint8_t pin)
{
    for (int i = 0; i < target->channel_count; i++) {
        if (channel_uses_pin(&target->channels[i], pin)) return true;
    }
    return false;
}

static bool validate_peripheral_pin_ownership(const config_manifest_t *target)
{
    for (int i = 0; i < target->gpio_config_count; i++) {
        uint8_t pin = target->gpio_configs[i].pin;
        if (manifest_bus_uses_pin(target, pin)) return false;
        for (int j = i + 1; j < target->gpio_config_count; j++) {
            if (target->gpio_configs[j].pin == pin) return false;
        }
        for (int j = 0; j < target->pwm_config_count; j++) {
            if (target->pwm_configs[j].pin == pin) {
                return false;
            }
        }
    }
    for (int i = 0; i < target->pwm_config_count; i++) {
        uint8_t pin = target->pwm_configs[i].pin;
        if (manifest_bus_uses_pin(target, pin)) return false;
        for (int j = i + 1; j < target->pwm_config_count; j++) {
            if (target->pwm_configs[j].pin == pin) {
                return false;
            }
        }
    }
    return true;
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
        if (field.field_num == 1 && !parse_field_manifest_id(target, &field)) return false;
		if (field.field_num == 8) {
			if (field.wire_type != WIRE_LENGTH_DELIMITED || !field.value.bytes.ptr ||
			    field.value.bytes.len == 0 || field.value.bytes.len >= sizeof(target->sync_id)) return false;
			memcpy(target->sync_id, field.value.bytes.ptr, field.value.bytes.len);
			target->sync_id[field.value.bytes.len] = '\0';
		}
        if (!parse_field_template(target, &field) ||
            !parse_field_channel(target, &field) ||
            !parse_field_dma_channel(target, &field) ||
            !parse_field_log_stream(target, &field) ||
            !parse_field_gpio_config(target, &field) ||
            !parse_field_pwm_config(target, &field)) {
            return false;
        }
    }
    if (err != FRAME_DONE) {
        ESP_LOGE(TAG, "Manifest decoder terminated with error: %d", err);
        return false;
    }

    log_parsed_manifest(target);
    return target->manifest_id[0] != '\0' && target->sync_id[0] != '\0';
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

esp_err_t config_mgr_persist_sync_metadata(uint64_t epoch, const char *id)
{
    if (!id || !id[0]) return ESP_ERR_INVALID_ARG;
    nvs_handle_t handle;
    esp_err_t err = nvs_open(NVS_NAMESPACE, NVS_READWRITE, &handle);
    if (err != ESP_OK) return err;
    if (epoch > 0) err = nvs_set_u64(handle, NVS_KEY_CONFIG_EPOCH, epoch);
    if (err == ESP_OK) err = nvs_set_str(handle, NVS_KEY_MANIFEST_ID, id);
    if (err == ESP_OK) err = nvs_commit(handle);
    nvs_close(handle);
    return err;
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

