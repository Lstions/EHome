/**
 * @file bus_manager.h
 * @brief Bus DMA context pool manager.
 *
 * Owns the bus_dma_ctx_t pool lifecycle: register, find, cleanup.
 * Called by app_callbacks (config applied) and bus_worker (lookup).
 *
 * P2-8: Decoupled from app_state_t — uses bus_runtime_t for dependency injection.
 */

#ifndef BUS_MANAGER_H
#define BUS_MANAGER_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include "bus_worker.h"   /* bus_runtime_t, write_rsp_cb_t */
#include "config_mgr.h"
#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

void bus_manager_set_write_rsp_cb(write_rsp_cb_t cb);

void bus_manager_init(bus_runtime_t *rt);
void bus_manager_snapshot_leases(bus_runtime_t *rt);
/* Release every channel lease and the DMA allocations it owns.
 *
 * WS-E install-once: this no longer deletes UART drivers.  The controller
 * objects stay resident so a manifest rebuild reconfigures them instead of
 * paying the ~3 KB/UART install cost; bus_manager_prune_unused_uarts() frees
 * the ones the new manifest does not use. */
esp_err_t bus_manager_cleanup_all(bus_runtime_t *rt);
esp_err_t bus_manager_setup_from_manifest(bus_runtime_t *rt);
esp_err_t bus_manager_apply_manifest(bus_runtime_t *rt, const config_manifest_t *manifest);

/**
 * @brief Install/reconfigure the UART controllers this manifest will lease.
 *
 * Call before bus_worker_suspend() so the one-time driver allocation happens
 * as a separately gated step instead of inside apply_buses.  Performs the same
 * pin/controller preflight as apply_manifest and refuses a manifest that does
 * not pass it.  Takes no leases; those are taken by apply_manifest.
 *
 * @return ESP_OK, or the first driver/preflight error.  On error the resident
 *         runtime is untouched (this function only adds idle controllers).
 */
esp_err_t bus_manager_preinstall_uarts(bus_runtime_t *rt, const config_manifest_t *manifest);

/**
 * @brief Tear down UART controllers installed but no longer leased.
 *
 * Call only after a SUCCESSFUL apply_manifest().  On a failed/rolled-back
 * apply the old manifest still needs its controllers, so pruning there would
 * delete a live driver.
 *
 * @return ESP_OK, or the first teardown error.
 */
esp_err_t bus_manager_prune_unused_uarts(bus_runtime_t *rt);

/* v2.4: Incremental config apply — checked single-channel lifecycle. */
esp_err_t bus_manager_reg_channel(bus_runtime_t *rt, const config_channel_t *ch);
esp_err_t bus_manager_unreg_channel(bus_runtime_t *rt, uint32_t channel_id);

/**
 * @brief Find a bus_dma_ctx_t by channel id.
 * @return Pointer to context, or NULL if not found.
 */
bus_dma_ctx_t *bus_manager_find_ctx(bus_runtime_t *rt, uint32_t channel_id);

/** Resolve the active physical UART lease for scheduler dispatch. */
uart_port_t bus_manager_get_uart_port(void *runtime, uint32_t channel_id);

/**
 * @brief Called by msg_handler when a WriteCommand (0x06) is received.
 * Constructs a bus_cmd_t and posts it to the command queue.
 */
void bus_manager_on_write_cmd(bus_runtime_t *rt, uint32_t request_id,
                               uint32_t channel_id,
                               const uint8_t *data, size_t len,
                               uint32_t read_size, uint32_t edge_device_id,
                               uint32_t rx_timeout_ms);

bool bus_manager_on_channel_cmd_v2(bus_runtime_t *rt, uint32_t channel_id,
                                   const uint8_t *data, size_t len, uint32_t read_size,
                                   uint32_t rx_timeout_ms, uint32_t post_tx_delay_ms,
                                   const uint8_t *plan_data, size_t plan_len,
                                   uint8_t plan_step_count,
                                   uint8_t control_slot);

#ifdef __cplusplus
}
#endif

#endif /* BUS_MANAGER_H */
