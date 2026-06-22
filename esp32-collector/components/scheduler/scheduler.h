/**
 * @file scheduler.h
 * @brief Channel Scheduler - periodic sampling via unified command queue
 *
 * The scheduler is a pure timer.  All bus transactions (UART/SPI/I2C) are
 * dispatched through the shared command queue consumed by the bus worker.
 *
 * v2.3: supports per-edge-device per-command independent timing.
 * When a channel has edge_devices, the scheduler walks a three-level loop
 * (channel → edge_device → command) with independent intervals.
 * Channels without edge_devices fall back to the legacy template_ids[0] path.
 */

#ifndef SCHEDULER_H
#define SCHEDULER_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "config_mgr.h"

#ifdef __cplusplus
extern "C" {
#endif

/* === Limits (must match config_mgr.h) === */
#define SCHED_MAX_CHANNELS  8
#define SCHED_TASK_STACK    4096
#define SCHED_TASK_PRIORITY 5
#define SCHED_TASK_CORE     0

/* === Error codes === */
typedef enum {
    SCHED_OK              =  0,
    SCHED_ERR_INVALID     = -1,
    SCHED_ERR_DUPLICATE   = -2,
    SCHED_ERR_FULL        = -3,
    SCHED_ERR_NOT_FOUND   = -4,
    SCHED_ERR_BUS         = -5,
    SCHED_ERR_NOT_INIT    = -6,
} sched_err_t;

/* === v2.3: per-command scheduler state === */
typedef struct {
    uint32_t template_id;
    uint32_t interval_ms;
    bool     enabled;
    TickType_t last_run_ms;     /* independent timing per command */
    uint32_t error_count;       /* consecutive errors for health reporting */
} sched_command_t;

/* === v2.3: per-edge-device scheduler state === */
typedef struct {
    uint32_t edge_device_id;
    uint32_t hardware_id;
    sched_command_t commands[MAX_COMMANDS_PER_DEVICE];
    uint8_t  command_count;
} sched_edge_device_t;

/* === per-channel scheduler state === */
typedef struct {
    config_channel_t config;
    uint32_t         last_sequence;
    TickType_t       last_sample_time;  /* kept for v1 compat path */
    bool             active;
    uint32_t         error_count;       /* backoff for v1 path */
    uint32_t         skip_count;        /* backoff for v1 path */
    /* v2.3: per edge_device + command scheduling */
    sched_edge_device_t edge_devices[MAX_EDGE_DEVICES_PER_CH];
    uint8_t             edge_device_count;
} sched_channel_t;

/* === Public API === */
void scheduler_init(void);
void scheduler_start(QueueHandle_t cmd_queue);
void scheduler_stop(void);
sched_err_t scheduler_add_channel(const config_channel_t *channel);
sched_err_t scheduler_remove_channel(uint32_t channel_id);
sched_err_t scheduler_update_channel(const config_channel_t *channel);  /* v2.4: in-place update */
void scheduler_pause(void);   /* v2.4: pause task loop, preserve channel state */
void scheduler_resume(QueueHandle_t cmd_queue);  /* v2.4: resume after pause */
bool scheduler_is_running(void);
uint8_t scheduler_get_channel_count(void);

/* === v2.3: scheduler state snapshot for StatusReport === */
typedef struct {
    const sched_channel_t *channels;
    uint8_t                channel_count;
} scheduler_state_t;

/**
 * @brief Get a snapshot of the scheduler state (read-only).
 * The returned pointer is to a static struct — do NOT free.
 * Valid only while scheduler is running or stopped (channels array persists).
 */
const scheduler_state_t *scheduler_get_state(void);

/* === Performance tracking === */
void scheduler_notify_channel_error(uint32_t channel_id);
void scheduler_notify_channel_success(uint32_t channel_id);

#ifdef __cplusplus
}
#endif

#endif
