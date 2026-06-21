/**
 * @file scheduler.c
 * @brief Channel Scheduler v3.1 — pure timer, all buses via unified command queue
 *
 * Every channel (UART / I2C / SPI) is sampled by posting a CMD_SAMPLE
 * descriptor to the injected command queue.  A single bus-worker task drains the queue
 * and performs the actual bus transactions, eliminating per-bus special
 * cases and the race-prone vTaskDelete in scheduler_stop.
 *
 * v2.3: channels with edge_devices use a three-level loop
 *       (channel → edge_device → command) with independent per-command timing.
 *       Channels without edge_devices fall back to the legacy template_ids[0] path.
 */

#include "scheduler.h"
#include "config_mgr.h"
#include "cmd_queue.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/queue.h"
#include <string.h>
#include <inttypes.h>

#define TAG "SCHEDULER"

/* ── per-channel state (struct definition now in scheduler.h) ──── */
static sched_channel_t s_channels[SCHED_MAX_CHANNELS];
static TaskHandle_t    s_task_handle;
static volatile bool   s_running;
static QueueHandle_t   s_cmd_queue = NULL;

static void scheduler_task(void *p);

/* ── public API ──────────────────────────────────────────────────── */

void scheduler_init(void)
{
    memset(s_channels, 0, sizeof(s_channels));
    s_running     = false;
    s_task_handle = NULL;
}

void scheduler_start(QueueHandle_t cmd_queue)
{
    if (s_task_handle) return;

    s_cmd_queue = cmd_queue;
    if (s_cmd_queue == NULL) {
        ESP_LOGE(TAG, "cmd_queue is NULL, cannot start");
        return;
    }

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
    s_channels[slot].error_count      = 0;
    s_channels[slot].skip_count       = 0;

    /* v2.3: initialise edge_device + command scheduler state */
    s_channels[slot].edge_device_count = 0;
    if (ch->edge_device_count > 0) {
        uint8_t count = ch->edge_device_count;
        if (count > MAX_EDGE_DEVICES_PER_CH)
            count = MAX_EDGE_DEVICES_PER_CH;
        s_channels[slot].edge_device_count = count;

        for (int ed = 0; ed < count; ed++) {
            const config_edge_device_t *src = &ch->edge_devices[ed];
            sched_edge_device_t *dst = &s_channels[slot].edge_devices[ed];
            dst->edge_device_id = src->edge_device_id;
            dst->hardware_id    = src->hardware_id;
            dst->command_count  = 0;

            uint8_t cmd_count = src->command_count;
            if (cmd_count > MAX_COMMANDS_PER_DEVICE)
                cmd_count = MAX_COMMANDS_PER_DEVICE;
            dst->command_count = cmd_count;

            for (int ci = 0; ci < cmd_count; ci++) {
                dst->commands[ci].template_id  = src->commands[ci].template_id;
                dst->commands[ci].interval_ms  = src->commands[ci].interval_ms;
                dst->commands[ci].enabled      = src->commands[ci].enabled;
                dst->commands[ci].last_run_ms  = 0;
            }
        }
    }

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
        UBaseType_t queue_spaces = uxQueueSpacesAvailable(s_cmd_queue);
        bool queue_pressure = (queue_spaces < (CMD_QUEUE_DEPTH / 4));  /* < 25% free */

        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            if (!s_channels[i].active || !s_channels[i].config.enabled)
                continue;

            sched_channel_t *ch = &s_channels[i];

            /* Decide v2 (edge_device) vs v1 (legacy template) path */
            bool use_v2 = (ch->edge_device_count > 0);

            if (use_v2) {
                /* ── v2.3: three-level loop, independent per-command timing ── */
                for (int ed = 0; ed < ch->edge_device_count; ed++) {
                    sched_edge_device_t *dev = &ch->edge_devices[ed];
                    for (int ci = 0; ci < dev->command_count; ci++) {
                        sched_command_t *scmd = &dev->commands[ci];
                        if (!scmd->enabled) continue;

                        /* Independent timing check */
                        if (now - scmd->last_run_ms < pdMS_TO_TICKS(scmd->interval_ms))
                            continue;

                        scmd->last_run_ms = now;

                        /* Backpressure: skip if queue is nearly full */
                        if (queue_pressure) {
                            queue_full_count++;
                            continue;
                        }

                        /* Look up template for this command */
                        const config_template_t *t =
                            config_mgr_get_template(scmd->template_id);
                        if (!t || t->write_data_len == 0) continue;

                        /* Build bus_cmd_t */
                        bus_cmd_t bcmd = {
                            .channel_id = ch->config.id,
                            .bus_type   = ch->config.bus_type,
                            .tx_len     = t->write_data_len < CMD_TX_MAX
                                              ? t->write_data_len : CMD_TX_MAX,
                            .delay_ms   = t->delay_ms > 0 ? t->delay_ms : 0,
                            .type       = CMD_SAMPLE,
                        };
                        memcpy(bcmd.tx_data, t->write_data, bcmd.tx_len);

                        if (xQueueSend(s_cmd_queue, &bcmd, 0) != pdTRUE) {
                            queue_full_count++;
                        } else {
                            total_samples++;
                        }
                    }
                }
            } else {
                /* ── v1: legacy template_ids[0] path (unchanged) ── */

                if (now - ch->last_sample_time <
                    pdMS_TO_TICKS(ch->config.interval_ms))
                    continue;

                /* Adaptive backoff: if channel has errors, skip some samples */
                if (ch->error_count > 3) {
                    ch->skip_count++;
                    /* Exponential backoff: skip 2^min(error_count, 5) samples */
                    uint32_t skip_threshold = (ch->error_count > 5) ? 32 :
                                              (1 << (ch->error_count - 3));
                    if (ch->skip_count < skip_threshold) {
                        continue;
                    }
                    ch->skip_count = 0;
                }

                ch->last_sample_time = now;

                /* Backpressure: skip if queue is nearly full */
                if (queue_pressure) {
                    queue_full_count++;
                    continue;
                }

                /* Build a unified bus command for any bus type.
                 * Only channels with templates need active TX (e.g. Modbus polling).
                 * Channels without templates (e.g. GPS NMEA) are passive —
                 * rx_task handles them. */
                bus_cmd_t cmd = {
                    .channel_id = ch->config.id,
                    .bus_type   = ch->config.bus_type,
                    .tx_len     = 0,
                    .delay_ms   = 0,
                    .type       = CMD_SAMPLE,
                };

                /* If the channel references a template, copy its TX payload and delay. */
                if (ch->config.template_count > 0) {
                    const config_template_t *t =
                        config_mgr_get_template(ch->config.template_ids[0]);
                    if (t && t->write_data_len > 0) {
                        cmd.tx_len = t->write_data_len < CMD_TX_MAX
                                         ? t->write_data_len : CMD_TX_MAX;
                        memcpy(cmd.tx_data, t->write_data, cmd.tx_len);
                        if (t->delay_ms > 0) {
                            cmd.delay_ms = t->delay_ms;
                        }
                    }
                } else {
                    /* No template — skip this channel.  rx_task handles passive
                     * UART RX; SPI/I2C without a template have nothing to do. */
                    continue;
                }

                if (xQueueSend(s_cmd_queue, &cmd, 0) != pdTRUE) {
                    queue_full_count++;
                } else {
                    total_samples++;
                }
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
