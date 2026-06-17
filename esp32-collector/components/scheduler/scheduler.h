/**
 * @file scheduler.h
 * @brief Channel Scheduler - periodic sampling via unified command queue
 *
 * The scheduler is a pure timer.  All bus transactions (UART/SPI/I2C) are
 * dispatched through the shared command queue consumed by the bus worker.
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

/* === Limits === */
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

/* === Public API === */
void scheduler_init(void);
void scheduler_start(QueueHandle_t cmd_queue);
void scheduler_stop(void);
sched_err_t scheduler_add_channel(const config_channel_t *channel);
sched_err_t scheduler_remove_channel(uint32_t channel_id);
bool scheduler_is_running(void);
uint8_t scheduler_get_channel_count(void);

/* === Performance tracking === */
void scheduler_notify_channel_error(uint32_t channel_id);
void scheduler_notify_channel_success(uint32_t channel_id);

#ifdef __cplusplus
}
#endif

#endif
