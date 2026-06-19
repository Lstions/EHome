/**
 * @file bus_worker.h
 * @brief Unified bus transaction worker — consumes cmd_queue, drives bus_dma_transact.
 */

#ifndef BUS_WORKER_H
#define BUS_WORKER_H

#include "app_state.h"
#include <stdint.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/** Inject msg_handler callbacks (call before bus_worker_start) */
void bus_worker_set_callbacks(write_rsp_cb_t wr_cb, data_rpt_cb_t dr_cb);

/** Start rx_task and cmd_task (called once at boot) */
void bus_worker_start(app_state_t *state);

/** Suspend rx_task + cmd_task — call before bus_manager_cleanup_all */
void bus_worker_suspend(void);

/** Resume rx_task + cmd_task — call after bus_manager_setup_from_manifest */
void bus_worker_resume(void);

#ifdef __cplusplus
}
#endif

#endif /* BUS_WORKER_H */
