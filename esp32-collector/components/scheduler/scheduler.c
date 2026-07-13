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
#include "scheduler_queue_guard.h"
#include "config_mgr.h"
#include "cmd_queue.h"
#include "bus_dma.h"
#include "hw_tables.h"
#include "log_stream.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/queue.h"
#include "driver/uart.h"
#include <string.h>
#include <inttypes.h>

#define TAG "SCHEDULER"

/* ── per-channel state (struct definition now in scheduler.h) ──── */
static sched_channel_t s_channels[SCHED_MAX_CHANNELS];
static TaskHandle_t    s_task_handle;
static volatile bool   s_running;
static scheduler_queues_t s_queues;

static void scheduler_task(void *p);

/* ── queue dispatch: pick the right per-bus queue from bus_cmd_t ── */

static QueueHandle_t dispatch_queue(const scheduler_queues_t *q, const bus_cmd_t *bcmd)
{
    switch (bcmd->bus_type) {
    case BUS_TYPE_UART:
        return (bcmd->uart_port == UART_NUM_0) ? q->uart0_cmd_queue : q->uart1_cmd_queue;
    case BUS_TYPE_SPI:  return q->spi_cmd_queue;
    case BUS_TYPE_I2C:  return q->i2c_cmd_queue;
    default:            return q->uart0_cmd_queue;
    }
}

/* A board may intentionally expose only a subset of bus workers.  Treat a
 * missing queue as fully available for global backpressure accounting; the
 * per-channel dispatch path below rejects a command whose own queue is absent. */
static UBaseType_t queue_spaces_or_depth(QueueHandle_t queue)
{
    return scheduler_queue_is_present(queue) ? uxQueueSpacesAvailable(queue) : CMD_QUEUE_DEPTH;
}

static UBaseType_t min_queue_spaces(UBaseType_t left, UBaseType_t right)
{
    return left < right ? left : right;
}

/* ── derive uart_port_t from bus_config bytes via hw_tables ── */
/* P3-7: Replaced static derive_uart_port with shared hw_derive_uart_port from hw_tables */

static uart_port_t derive_uart_port(const config_channel_t *ch)
{
    if (ch->bus_type != BUS_TYPE_UART || ch->bus_config_len < 2)
        return UART_NUM_0;  /* safe default */

    return hw_derive_uart_port(ch->bus_config[0], ch->bus_config[1], UART_NUM_1);
}

/* ── public API ──────────────────────────────────────────────────── */

void scheduler_init(void)
{
    memset(s_channels, 0, sizeof(s_channels));
    s_running     = false;
    s_task_handle = NULL;
}

void scheduler_start(const scheduler_queues_t *queues)
{
    if (s_task_handle) return;

    if (queues) {
        s_queues = *queues;
    }
    if (s_queues.uart0_cmd_queue == NULL && s_queues.uart1_cmd_queue == NULL &&
        s_queues.spi_cmd_queue == NULL && s_queues.i2c_cmd_queue == NULL) {
        ESP_LOGE(TAG, "all queues are NULL, cannot start");
        LOG_STREAM_E(TAG, "start failed queues=none");
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

/* v2.4: Lightweight pause — stops the task loop but preserves channel state.
 * Unlike scheduler_stop(), this does NOT clear s_channels[].active.
 * Caller must call scheduler_resume() to restart the task. */
void scheduler_pause(void)
{
    s_running = false;
    if (s_task_handle) {
        for (int i = 0; i < 100 && eTaskGetState(s_task_handle) != eDeleted; i++) {
            vTaskDelay(pdMS_TO_TICKS(10));
        }
        s_task_handle = NULL;
    }
    /* Channel state preserved — s_channels[].active untouched */
}

/* v2.4: Resume after pause — recreates the task without reloading channels. */
void scheduler_resume(const scheduler_queues_t *queues)
{
    if (s_task_handle) return;
    if (queues) {
        s_queues = *queues;
    }
    if (s_queues.uart0_cmd_queue == NULL && s_queues.uart1_cmd_queue == NULL &&
        s_queues.spi_cmd_queue == NULL && s_queues.i2c_cmd_queue == NULL) {
        ESP_LOGE(TAG, "all queues are NULL, cannot resume");
        LOG_STREAM_E(TAG, "resume failed queues=none");
        return;
    }
    s_running = true;
    xTaskCreatePinnedToCore(scheduler_task, "scheduler",
                            SCHED_TASK_STACK, NULL,
                            SCHED_TASK_PRIORITY, &s_task_handle,
                            SCHED_TASK_CORE);
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
        ESP_LOGI(TAG, "scheduler_add_channel: ch_id=%lu, edge_device_count=%d, ed[0].id=%lu",
                 (unsigned long)ch->id, ch->edge_device_count,
                 ch->edge_device_count > 0 ? (unsigned long)ch->edge_devices[0].edge_device_id : 0);

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

/* v2.4: In-place update — only copies the config (interval_ms, template_ids, etc.)
 * without changing the active flag or runtime counters.  Used when bus-level
 * config hasn't changed and we don't want to lose the last_sample_time. */
sched_err_t scheduler_update_channel(const config_channel_t *ch)
{
    if (!ch) return SCHED_ERR_INVALID;

    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s_channels[i].active && s_channels[i].config.id == ch->id) {
            /* Preserve runtime state, overwrite config */
            TickType_t saved_last = s_channels[i].last_sample_time;
            uint32_t saved_seq = s_channels[i].last_sequence;
            uint32_t saved_err = s_channels[i].error_count;
            uint32_t saved_skip = s_channels[i].skip_count;

            memcpy(&s_channels[i].config, ch, sizeof(config_channel_t));
            s_channels[i].last_sample_time = saved_last;
            s_channels[i].last_sequence = saved_seq;
            s_channels[i].error_count = saved_err;
            s_channels[i].skip_count = saved_skip;

            /* Re-init edge_device state from new config */
            s_channels[i].edge_device_count = 0;
            if (ch->edge_device_count > 0) {
                uint8_t count = ch->edge_device_count;
                if (count > MAX_EDGE_DEVICES_PER_CH) count = MAX_EDGE_DEVICES_PER_CH;
                s_channels[i].edge_device_count = count;
                for (int ed = 0; ed < count; ed++) {
                    const config_edge_device_t *src = &ch->edge_devices[ed];
                    sched_edge_device_t *dst = &s_channels[i].edge_devices[ed];
                    dst->edge_device_id = src->edge_device_id;
                    dst->hardware_id    = src->hardware_id;
                    dst->command_count  = 0;
                    uint8_t cmd_count = src->command_count;
                    if (cmd_count > MAX_COMMANDS_PER_DEVICE) cmd_count = MAX_COMMANDS_PER_DEVICE;
                    dst->command_count = cmd_count;
                    for (int ci = 0; ci < cmd_count; ci++) {
                        dst->commands[ci].template_id  = src->commands[ci].template_id;
                        dst->commands[ci].interval_ms  = src->commands[ci].interval_ms;
                        dst->commands[ci].enabled      = src->commands[ci].enabled;
                        /* preserve last_run_ms for independent timing */
                    }
                }
            }
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

const scheduler_state_t *scheduler_get_state(void)
{
    static scheduler_state_t state;
    state.channels = s_channels;
    state.channel_count = SCHED_MAX_CHANNELS;
    return &state;
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

/* ── scheduler helper functions ──────────────────────────────────── */

/**
 * @brief Schedule commands for a channel using v2 edge_device mode.
 * 
 * Iterates through all edge devices and their commands, checking timing
 * and sending commands to the queue.
 * 
 * @param ch Channel to schedule
 * @param now Current tick count
 * @param queue_pressure True if queue is nearly full
 * @param total_samples Pointer to sample counter
 * @param queue_full_count Pointer to queue-full counter
 */
static void schedule_v2_channel(sched_channel_t *ch, TickType_t now,
                                bool queue_pressure,
                                uint32_t *total_samples, uint32_t *queue_full_count)
{
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
                (*queue_full_count)++;
                continue;
            }

            /* Look up template for this command */
            const config_template_t *t = config_mgr_get_template(scmd->template_id);
            if (!t || t->write_data_len == 0) continue;

            /* Build bus_cmd_t */
            bus_cmd_t bcmd = {
                .channel_id     = ch->config.id,
                .bus_type       = ch->config.bus_type,
                .tx_len         = t->write_data_len < CMD_TX_MAX ? t->write_data_len : CMD_TX_MAX,
                .delay_ms       = t->delay_ms > 0 ? t->delay_ms : 0,
                .read_size      = t->read_length,  /* P1-8: pass read_length for rx_task metadata */
                .edge_device_id = dev->edge_device_id,
                .command_template_id = scmd->template_id,
                .command_index  = (uint8_t)ci,
                .type           = CMD_SAMPLE,
            };
            memcpy(bcmd.tx_data, t->write_data, bcmd.tx_len);

            bcmd.uart_port = derive_uart_port(&ch->config);
            QueueHandle_t target_q = dispatch_queue(&s_queues, &bcmd);
            if (!scheduler_queue_is_present(target_q) || xQueueSend(target_q, &bcmd, 0) != pdTRUE) {
                (*queue_full_count)++;
                scmd->error_count++;
                if (scmd->error_count > 100) scmd->error_count = 100;
            } else {
                (*total_samples)++;
                scmd->error_count = 0;
            }
        }
    }
}

/**
 * @brief Schedule commands for a channel using v1 legacy template mode.
 * 
 * Uses the first template ID and applies adaptive backoff on errors.
 * 
 * @param ch Channel to schedule
 * @param now Current tick count
 * @param queue_pressure True if queue is nearly full
 * @param total_samples Pointer to sample counter
 * @param queue_full_count Pointer to queue-full counter
 */
static void schedule_v1_channel(sched_channel_t *ch, TickType_t now,
                                bool queue_pressure,
                                uint32_t *total_samples, uint32_t *queue_full_count)
{
    if (now - ch->last_sample_time < pdMS_TO_TICKS(ch->config.interval_ms))
        return;

    /* Adaptive backoff: if channel has errors, skip some samples */
    if (ch->error_count > 3) {
        ch->skip_count++;
        /* Exponential backoff: skip 2^min(error_count, 5) samples */
        uint32_t skip_threshold = (ch->error_count > 5) ? 32 :
                                  (1 << (ch->error_count - 3));
        if (ch->skip_count < skip_threshold) {
            return;
        }
        ch->skip_count = 0;
    }

    ch->last_sample_time = now;

    /* Backpressure: skip if queue is nearly full */
    if (queue_pressure) {
        (*queue_full_count)++;
        return;
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
        const config_template_t *t = config_mgr_get_template(ch->config.template_ids[0]);
        if (t && t->write_data_len > 0) {
            cmd.tx_len = t->write_data_len < CMD_TX_MAX ? t->write_data_len : CMD_TX_MAX;
            memcpy(cmd.tx_data, t->write_data, cmd.tx_len);
            cmd.read_size = t->read_length;  /* P1-8: pass read_length for rx_task metadata */
            if (t->delay_ms > 0) {
                cmd.delay_ms = t->delay_ms;
            }
        }
    } else {
        /* No template — skip this channel.  rx_task handles passive
         * UART RX; SPI/I2C without a template have nothing to do. */
        return;
    }

    cmd.uart_port = derive_uart_port(&ch->config);
    QueueHandle_t target_q = dispatch_queue(&s_queues, &cmd);
    if (!scheduler_queue_is_present(target_q) || xQueueSend(target_q, &cmd, 0) != pdTRUE) {
        (*queue_full_count)++;
    } else {
        (*total_samples)++;
    }
}

/* ── scheduler task (P3-1: dynamic tick — 1ms when fast channels active, 10ms otherwise) ─ */

static void scheduler_task(void *p)
{
    (void)p;
    TickType_t wake = xTaskGetTickCount();
    uint32_t queue_full_count = 0;
    uint32_t total_samples = 0;

    while (s_running) {
        /* P3-1: Dynamic tick — use 1ms when any channel needs <100ms interval,
         * otherwise use 10ms to save CPU.  Checked every iteration because
         * channel configuration may change at runtime. */
        bool has_fast_channel = false;
        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            if (!s_channels[i].active) continue;
            if (s_channels[i].config.interval_ms < 100) {
                has_fast_channel = true;
                break;
            }
            /* Also check per-command intervals in v2 edge_device channels */
            for (int ed = 0; ed < s_channels[i].edge_device_count; ed++) {
                for (int ci = 0; ci < s_channels[i].edge_devices[ed].command_count; ci++) {
                    if (s_channels[i].edge_devices[ed].commands[ci].interval_ms < 100) {
                        has_fast_channel = true;
                        break;
                    }
                }
                if (has_fast_channel) break;
            }
            if (has_fast_channel) break;
        }
        TickType_t tick_ms = has_fast_channel ? 1 : 10;
        vTaskDelayUntil(&wake, pdMS_TO_TICKS(tick_ms));
        TickType_t now = xTaskGetTickCount();

        /* Check queue depth for backpressure — use the busiest queue */
        UBaseType_t min_spaces = CMD_QUEUE_DEPTH;
        min_spaces = min_queue_spaces(min_spaces, queue_spaces_or_depth(s_queues.uart0_cmd_queue));
        min_spaces = min_queue_spaces(min_spaces, queue_spaces_or_depth(s_queues.uart1_cmd_queue));
        min_spaces = min_queue_spaces(min_spaces, queue_spaces_or_depth(s_queues.spi_cmd_queue));
        min_spaces = min_queue_spaces(min_spaces, queue_spaces_or_depth(s_queues.i2c_cmd_queue));
        bool queue_pressure = (min_spaces < (CMD_QUEUE_DEPTH / 4));  /* < 25% free */

        /* Iterate through all active channels */
        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            if (!s_channels[i].active || !s_channels[i].config.enabled)
                continue;
            sched_channel_t *ch = &s_channels[i];

            /* Dispatch to appropriate scheduling strategy */
            {
                static TickType_t s_path_log_time[SCHED_MAX_CHANNELS] = {0};
                TickType_t now2 = xTaskGetTickCount();
                if (s_path_log_time[i] == 0 || now2 - s_path_log_time[i] > pdMS_TO_TICKS(60000)) {
                    ESP_LOGI(TAG, "sched: ch=%lu edge_device_count=%d, choosing %s path",
                             (unsigned long)ch->config.id, ch->edge_device_count,
                             ch->edge_device_count > 0 ? "v2" : "v1");
                    s_path_log_time[i] = now2;
                }
            }
            if (ch->edge_device_count > 0) {
                schedule_v2_channel(ch, now, queue_pressure, &total_samples, &queue_full_count);
            } else {
                schedule_v1_channel(ch, now, queue_pressure, &total_samples, &queue_full_count);
            }
        }

        /* Periodic performance logging (every 10 seconds) */
        static uint32_t last_log = 0;
        if (now - last_log > pdMS_TO_TICKS(10000)) {
            if (total_samples > 0 || queue_full_count > 0) {
                ESP_LOGI(TAG, "Stats: samples=%" PRIu32 " full=%" PRIu32 " min_free=%d",
                         total_samples, queue_full_count, (int)min_spaces);
            }
            last_log = now;
            total_samples = 0;
            queue_full_count = 0;
        }
    }

    vTaskDelete(NULL);
}
