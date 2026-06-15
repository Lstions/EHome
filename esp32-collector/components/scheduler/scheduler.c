/**
 * @file scheduler.c
 * @brief Channel Scheduler v3.0 — pure timer, all buses via unified command queue
 *
 * Every channel (UART / I2C / SPI) is sampled by posting a CMD_SAMPLE
 * descriptor to g_cmd_queue.  A single bus-worker task drains the queue
 * and performs the actual bus transactions, eliminating per-bus special
 * cases and the race-prone vTaskDelete in scheduler_stop.
 */

#include "scheduler.h"
#include "config_mgr.h"
#include "msg_handler.h"
#include "cmd_queue.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/queue.h"
#include <string.h>
#include <inttypes.h>

#define TAG "SCHEDULER"

/* ── per-channel state ───────────────────────────────────────────── */
typedef struct {
    config_channel_t config;
    uint32_t         last_sequence;
    TickType_t       last_sample_time;
    bool             active;
    uint32_t         error_count;      /* Track errors for backoff */
    uint32_t         skip_count;       /* Count skipped samples */
} sched_channel_t;

static sched_channel_t s_channels[SCHED_MAX_CHANNELS];
static TaskHandle_t    s_task_handle;
static volatile bool   s_running;

static void scheduler_task(void *p);

/* ── public API ──────────────────────────────────────────────────── */

void scheduler_init(void)
{
    memset(s_channels, 0, sizeof(s_channels));
    s_running     = false;
    s_task_handle = NULL;
}

void scheduler_start(void)
{
    if (s_task_handle) return;

    const config_manifest_t *cfg = config_mgr_get_manifest();
    if (cfg && cfg->applied) {
        for (int i = 0; i < cfg->channel_count && i < MAX_CHANNELS; i++) {
            scheduler_add_channel(&cfg->channels[i]);
        }
    }

    s_running = true;
    xTaskCreatePinnedToCore(scheduler_task, "scheduler",
                            SCHED_TASK_STACK, NULL,
                            SCHED_TASK_PRIORITY, &s_task_handle,
                            SCHED_TASK_CORE);
}

void scheduler_stop(void)
{
    s_running = false;

    if (s_task_handle) {
        /* Wait for the task to notice s_running==false and exit its loop. */
        for (int i = 0; i < 100 && eTaskGetState(s_task_handle) != eDeleted; i++) {
            vTaskDelay(pdMS_TO_TICKS(10));
        }
        s_task_handle = NULL;
    }

    /* Now safe to clear channel state — no task is reading it. */
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        s_channels[i].active = false;
    }
}

sched_err_t scheduler_add_channel(const config_channel_t *ch)
{
    if (!ch) return SCHED_ERR_INVALID;

    int slot = -1;
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s_channels[i].active && s_channels[i].config.id == ch->id)
            return SCHED_ERR_DUPLICATE;
        if (!s_channels[i].active && slot < 0)
            slot = i;
    }
    if (slot < 0) return SCHED_ERR_FULL;

    memcpy(&s_channels[slot].config, ch, sizeof(config_channel_t));
    s_channels[slot].last_sequence    = 0;
    s_channels[slot].last_sample_time = 0;
    s_channels[slot].active           = true;

    return SCHED_OK;
}

sched_err_t scheduler_remove_channel(uint32_t id)
{
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s_channels[i].active && s_channels[i].config.id == id) {
            s_channels[i].active = false;
            return SCHED_OK;
        }
    }
    return SCHED_ERR_NOT_FOUND;
}

bool scheduler_is_running(void) { return s_running; }

uint8_t scheduler_get_channel_count(void)
{
    uint8_t c = 0;
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++)
        if (s_channels[i].active) c++;
    return c;
}

/* ── performance tracking ─────────────────────────────────────────── */

void scheduler_notify_channel_error(uint32_t channel_id)
{
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s_channels[i].active && s_channels[i].config.id == channel_id) {
            s_channels[i].error_count++;
            /* Cap error count to prevent overflow */
            if (s_channels[i].error_count > 100) {
                s_channels[i].error_count = 100;
            }
            break;
        }
    }
}

void scheduler_notify_channel_success(uint32_t channel_id)
{
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s_channels[i].active && s_channels[i].config.id == channel_id) {
            /* Gradually reduce error count on success */
            if (s_channels[i].error_count > 0) {
                s_channels[i].error_count--;
            }
            s_channels[i].skip_count = 0;  /* Reset skip counter */
            break;
        }
    }
}

/* ── scheduler task (10 ms tick) ─────────────────────────────────── */

static void scheduler_task(void *p)
{
    (void)p;
    TickType_t wake = xTaskGetTickCount();
    uint32_t queue_full_count = 0;
    uint32_t total_samples = 0;

    while (s_running) {
        vTaskDelayUntil(&wake, pdMS_TO_TICKS(10));
        TickType_t now = xTaskGetTickCount();

        /* Check queue depth for backpressure */
        UBaseType_t queue_spaces = uxQueueSpacesAvailable(g_cmd_queue);
        bool queue_pressure = (queue_spaces < (CMD_QUEUE_DEPTH / 4));  /* < 25% free */

        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            if (!s_channels[i].active || !s_channels[i].config.enabled)
                continue;
            if (now - s_channels[i].last_sample_time <
                pdMS_TO_TICKS(s_channels[i].config.interval_ms))
                continue;

            /* Adaptive backoff: if channel has errors, skip some samples */
            if (s_channels[i].error_count > 3) {
                s_channels[i].skip_count++;
                /* Exponential backoff: skip 2^min(error_count, 5) samples */
                uint32_t skip_threshold = (s_channels[i].error_count > 5) ? 32 : 
                                          (1 << (s_channels[i].error_count - 3));
                if (s_channels[i].skip_count < skip_threshold) {
                    continue;
                }
                s_channels[i].skip_count = 0;
            }

            s_channels[i].last_sample_time = now;

            /* Backpressure: skip if queue is nearly full */
            if (queue_pressure) {
                queue_full_count++;
                continue;
            }

            /* Build a unified bus command for any bus type. */
            bus_cmd_t cmd = {
                .channel_id = s_channels[i].config.id,
                .bus_type   = s_channels[i].config.bus_type,
                .tx_len     = 0,
                .read_size  = 0,
                .timeout_ms = 30,
                .type       = CMD_SAMPLE,
            };

            /* If the channel references a template, copy its TX payload. */
            if (s_channels[i].config.template_count > 0) {
                const config_template_t *t =
                    config_mgr_get_template(s_channels[i].config.template_ids[0]);
                if (t && t->write_data_len > 0) {
                    cmd.tx_len = t->write_data_len < CMD_TX_MAX
                                     ? t->write_data_len : CMD_TX_MAX;
                    memcpy(cmd.tx_data, t->write_data, cmd.tx_len);
                    cmd.read_size = t->read_length;
                }
            }

            if (xQueueSend(g_cmd_queue, &cmd, 0) != pdTRUE) {
                queue_full_count++;
            } else {
                total_samples++;
            }
        }

        /* Periodic performance logging (every 10 seconds) */
        static uint32_t last_log = 0;
        if (now - last_log > pdMS_TO_TICKS(10000)) {
            if (total_samples > 0 || queue_full_count > 0) {
                ESP_LOGI(TAG, "Stats: samples=%" PRIu32 " full=%" PRIu32 " q_free=%d",
                         total_samples, queue_full_count, (int)queue_spaces);
            }
            last_log = now;
            total_samples = 0;
            queue_full_count = 0;
        }
    }

    vTaskDelete(NULL);
}
