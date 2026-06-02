/**
 * @file scheduler.h
 * @brief Channel Scheduler - periodic data acquisition via bus layer
 */

#ifndef SCHEDULER_H
#define SCHEDULER_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
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
void scheduler_start(void);
void scheduler_stop(void);
sched_err_t scheduler_add_channel(const config_channel_t *channel);
sched_err_t scheduler_remove_channel(uint32_t channel_id);
bool scheduler_is_running(void);
uint8_t scheduler_get_channel_count(void);

#ifdef __cplusplus
}
#endif

#endif
