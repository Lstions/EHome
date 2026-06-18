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

void bus_worker_start(app_state_t *state);

#ifdef __cplusplus
}
#endif

#endif /* BUS_WORKER_H */
