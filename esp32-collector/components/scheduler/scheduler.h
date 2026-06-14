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

/**
 * Execute a bus transaction for a given channel (used by WriteCommand).
 * Returns BUS_OK on success, or error code.
 * rx_buf receives response data, rx_len set to bytes read.
 */
int scheduler_execute_write(uint32_t channel_id, const uint8_t *tx_data, size_t tx_len,
                           uint32_t read_size, uint8_t *rx_buf, size_t *rx_len);

#ifdef __cplusplus
}
#endif

#endif
