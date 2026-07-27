/**
 * @file scheduler.h
 * @brief Channel Scheduler - periodic sampling via per-controller queues
 *
 * The scheduler is a pure timer.  All bus transactions (UART/SPI/I2C) are
 * dispatched through the active controller's independent command queue; the
 * worker selected by the dynamic lease performs the transaction.
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
#include "driver/uart.h"
#include "config_mgr.h"

/* ESP-IDF omits UART_NUM_2 on targets that expose only HP UART0/1 (for
 * example C6's third UART is LP-only).  Keep the dynamic route value
 * representable; the hardware planner still rejects unavailable resources. */
#ifndef UART_NUM_2
#define UART_NUM_2 ((uart_port_t)2)
#endif

#ifdef __cplusplus
extern "C" {
#endif

/* === Limits (must match config_mgr.h) === */
#define SCHED_MAX_CHANNELS  8
#define SCHED_TASK_STACK    4096
#define SCHED_TASK_PRIORITY 5
#define SCHED_TASK_CORE     0

/* ESP-IDF's task creation API has no "create suspended" primitive. The task
 * therefore waits in a dormant prepared state until scheduler_activate().
 * A finite timeout prevents a failed/cancelled transaction from leaking it. */
#define SCHED_PREPARE_TIMEOUT_MS 5000

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

/* === Per-bus queue set (injected by caller, avoids app_state_t coupling) === */
/* The scheduler asks the active runtime lease for the physical UART.  The
 * fallback remains pin-derived for compatibility with callers that do not
 * provide a route callback. */
typedef uart_port_t (*scheduler_uart_route_fn)(void *route_ctx,
                                               uint32_t channel_id);

typedef struct {
    QueueHandle_t uart0_cmd_queue;
    QueueHandle_t uart1_cmd_queue;
    QueueHandle_t uart2_cmd_queue;
    QueueHandle_t spi_cmd_queue;
    QueueHandle_t i2c_cmd_queue;
    scheduler_uart_route_fn uart_route;
    void *route_ctx;
} scheduler_queues_t;

/* === Public API === */
void scheduler_init(void);
sched_err_t scheduler_start(const scheduler_queues_t *queues);
sched_err_t scheduler_start_manifest(const scheduler_queues_t *queues,
                                     const config_manifest_t *manifest);
/* Prepare channels and create a dormant task. scheduler_activate() is a
 * no-fail publication step used only after the manifest commit succeeds. */
sched_err_t scheduler_prepare(const scheduler_queues_t *queues,
                              const config_manifest_t *manifest);
void scheduler_activate(void);
sched_err_t scheduler_stop(void);
sched_err_t scheduler_add_channel(const config_channel_t *channel);
sched_err_t scheduler_remove_channel(uint32_t channel_id);
sched_err_t scheduler_update_channel(const config_channel_t *channel);  /* v2.4: in-place update */
sched_err_t scheduler_pause(void);   /* v2.4: pause task loop, preserve channel state */
sched_err_t scheduler_resume(const scheduler_queues_t *queues);  /* v2.4: resume after pause */
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

/* Bounded runtime observability for performance gates.  All values are
 * measured since boot/config start; zero means the corresponding task has not
 * been created yet. */
typedef struct {
    uint32_t min_queue_spaces;
    uint32_t stack_high_water_words;
} scheduler_performance_t;
void scheduler_get_performance(scheduler_performance_t *out);

#define SCHED_QUEUE_METRIC_COUNT 5
typedef struct {
    uint32_t current_spaces[SCHED_QUEUE_METRIC_COUNT];
    uint32_t high_water_used[SCHED_QUEUE_METRIC_COUNT];
    uint32_t sample_skipped[SCHED_QUEUE_METRIC_COUNT];
    uint32_t sample_rejected[SCHED_QUEUE_METRIC_COUNT];
} scheduler_queue_metrics_t;
void scheduler_get_queue_metrics(scheduler_queue_metrics_t *out);

#ifdef __cplusplus
}
#endif

#endif
