/**
 * @file bus_worker.h
 * @brief Unified bus transaction worker — consumes cmd_queue, drives bus_dma_transact.
 */

#ifndef BUS_WORKER_H
#define BUS_WORKER_H

#include "app_state.h"

#ifdef __cplusplus
extern "C" {
#endif

void bus_worker_start(app_state_t *state);

#ifdef __cplusplus
}
#endif

#endif /* BUS_WORKER_H */
