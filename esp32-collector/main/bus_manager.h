/**
 * @file bus_manager.h
 * @brief Bus DMA context pool manager.
 *
 * Owns the bus_dma_ctx_t pool lifecycle: register, find, cleanup.
 * Called by app_callbacks (config applied) and bus_worker (lookup).
 */

#ifndef BUS_MANAGER_H
#define BUS_MANAGER_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include "app_state.h"

#ifdef __cplusplus
extern "C" {
#endif

void bus_manager_init(app_state_t *state);
void bus_manager_cleanup_all(app_state_t *state);
void bus_manager_setup_from_manifest(app_state_t *state);

/**
 * @brief Find a bus_dma_ctx_t by channel id.
 * @return Pointer to context, or NULL if not found.
 */
bus_dma_ctx_t *bus_manager_find_ctx(app_state_t *state, uint32_t channel_id);

/**
 * @brief Called by msg_handler when a WriteCommand (0x06) is received.
 * Constructs a bus_cmd_t and posts it to the command queue.
 */
void bus_manager_on_write_cmd(app_state_t *state, uint32_t request_id,
                               uint32_t channel_id,
                               const uint8_t *data, size_t len,
                               uint32_t read_size);

#ifdef __cplusplus
}
#endif

#endif /* BUS_MANAGER_H */
