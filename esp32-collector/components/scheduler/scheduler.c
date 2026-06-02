/**
 * @file scheduler.c
 * @brief Channel Scheduler - periodic data acquisition
 */

#include "scheduler.h"
#include "config_mgr.h"
#include "msg_handler.h"
#include "bus.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include <string.h>

#define TAG "SCHEDULER"

typedef struct {
    config_channel_t config;
    bus_handle_t bus;
    uint32_t last_sequence;
    TickType_t last_sample_time;
    bool active;
} sched_channel_t;

static sched_channel_t s_channels[SCHED_MAX_CHANNELS];
static TaskHandle_t s_task_handle = NULL;
static volatile bool s_running = false;

static void scheduler_task(void *pvParameters);
static void sample_channel(sched_channel_t *ch);

void scheduler_init(void)
{
    memset(s_channels, 0, sizeof(s_channels));
    s_running = false;
    s_task_handle = NULL;
    ESP_LOGI(TAG, "Scheduler initialized");
}

void scheduler_start(void)
{
    if (s_task_handle != NULL) {
        return;
    }

    /* Load channels from config_mgr */
    const config_manifest_t *cfg = config_mgr_get_manifest();
    if (cfg != NULL && cfg->applied) {
        for (int i = 0; i < cfg->channel_count && i < MAX_CHANNELS; i++) {
            scheduler_add_channel(&cfg->channels[i]);
        }
        ESP_LOGI(TAG, "Loaded %d channels from config", cfg->channel_count);
    }

    s_running = true;
    xTaskCreatePinnedToCore(scheduler_task, "scheduler", SCHED_TASK_STACK,
                            NULL, SCHED_TASK_PRIORITY, &s_task_handle, SCHED_TASK_CORE);
    ESP_LOGI(TAG, "Scheduler started");
}

void scheduler_stop(void)
{
    s_running = false;
    if (s_task_handle != NULL) {
        vTaskDelete(s_task_handle);
        s_task_handle = NULL;
    }
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s_channels[i].active && s_channels[i].bus != NULL) {
            bus_close(s_channels[i].bus);
            s_channels[i].bus = NULL;
        }
    }
    ESP_LOGI(TAG, "Scheduler stopped");
}

sched_err_t scheduler_add_channel(const config_channel_t *channel)
{
    if (channel == NULL) return SCHED_ERR_INVALID;
    
    int slot = -1;
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s_channels[i].active && s_channels[i].config.id == channel->id) {
            return SCHED_ERR_DUPLICATE;
        }
        if (!s_channels[i].active && slot < 0) {
            slot = i;
        }
    }
    if (slot < 0) return SCHED_ERR_FULL;
    
    memcpy(&s_channels[slot].config, channel, sizeof(config_channel_t));
    s_channels[slot].bus = bus_open(channel->bus_type, channel->bus_config, channel->bus_config_len);
    s_channels[slot].last_sequence = 0;
    s_channels[slot].last_sample_time = 0;
    s_channels[slot].active = true;
    
    ESP_LOGI(TAG, "Channel %lu added (bus_type=%d)", (unsigned long)channel->id, channel->bus_type);
    return SCHED_OK;
}

sched_err_t scheduler_remove_channel(uint32_t channel_id)
{
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s_channels[i].active && s_channels[i].config.id == channel_id) {
            if (s_channels[i].bus != NULL) {
                bus_close(s_channels[i].bus);
            }
            s_channels[i].active = false;
            ESP_LOGI(TAG, "Channel %lu removed", (unsigned long)channel_id);
            return SCHED_OK;
        }
    }
    return SCHED_ERR_NOT_FOUND;
}

bool scheduler_is_running(void)
{
    return s_running;
}

uint8_t scheduler_get_channel_count(void)
{
    uint8_t count = 0;
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s_channels[i].active) count++;
    }
    return count;
}

static void scheduler_task(void *pvParameters)
{
    (void)pvParameters;
    ESP_LOGI(TAG, "Scheduler task running");
    
    while (s_running) {
        TickType_t now = xTaskGetTickCount();
        
        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            if (!s_channels[i].active || !s_channels[i].config.enabled) {
                continue;
            }
            
            uint32_t interval_ticks = pdMS_TO_TICKS(s_channels[i].config.interval_ms);
            if (now - s_channels[i].last_sample_time >= interval_ticks) {
                sample_channel(&s_channels[i]);
                s_channels[i].last_sample_time = now;
            }
        }
        
        vTaskDelay(pdMS_TO_TICKS(10));
    }
    
    vTaskDelete(NULL);
}

static void sample_channel(sched_channel_t *ch)
{
    uint8_t raw_data[256];
    size_t raw_len = 0;
    
    ESP_LOGD(TAG, "Sampling channel %lu: bus=%p templates=%u",
             (unsigned long)ch->config.id, ch->bus, (unsigned)ch->config.template_count);
    
    if (ch->bus != NULL) {
        /* Execute each template's write-then-read sequence */
        for (int t = 0; t < ch->config.template_count && t < MAX_TEMPLATE_IDS; t++) {
            const config_template_t *tmpl = config_mgr_get_template(ch->config.template_ids[t]);
            if (tmpl == NULL) {
                ESP_LOGW(TAG, "Template %u not found for channel %lu", ch->config.template_ids[t], (unsigned long)ch->config.id);
                continue;
            }
            ESP_LOGD(TAG, "Template %u: write_data_len=%u read_length=%u",
                     (unsigned)tmpl->id, (unsigned)tmpl->write_data_len, (unsigned)tmpl->read_length);

            /* Prepare tx_buf from template write_data */
            if (tmpl->write_data_len > 0) {
                bus_set_tx(ch->bus, tmpl->write_data, tmpl->write_data_len, tmpl->read_length);
            } else {
                bus_set_tx(ch->bus, NULL, 0, tmpl->read_length);
            }

            uint8_t tmp_rx[256];
            size_t tmp_rx_len = 0;
            bus_error_t err = bus_transact(ch->bus, tmp_rx, sizeof(tmp_rx), &tmp_rx_len);
            
            ESP_LOGD(TAG, "bus_transact: err=%d rx_len=%u", err, (unsigned)tmp_rx_len);
            if (err != BUS_OK) {
                ESP_LOGW(TAG, "Bus transaction failed for channel %lu template %u",
                         (unsigned long)ch->config.id, (unsigned)tmpl->id);
                continue;
            }

            /* Append response data to raw_data buffer */
            if (tmp_rx_len > 0 && raw_len + tmp_rx_len <= sizeof(raw_data)) {
                memcpy(raw_data + raw_len, tmp_rx, tmp_rx_len);
                raw_len += tmp_rx_len;
            }

            /* Inter-template delay */
            if (tmpl->delay_ms > 0 && t < ch->config.template_count - 1) {
                vTaskDelay(pdMS_TO_TICKS(tmpl->delay_ms));
            }
        }
    }
    
    ch->last_sequence++;
    uint64_t timestamp_us = (uint64_t)(xTaskGetTickCount() * 1000 / configTICK_RATE_HZ) * 1000;
    
    msg_handler_send_data_report(ch->config.id, timestamp_us, ch->last_sequence,
                                 raw_data, raw_len, 0, 0);
}
