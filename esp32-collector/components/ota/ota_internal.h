/**
 * @file ota_internal.h
 * @brief Internal OTA seams shared with future transports (BLE OTA).
 *
 * Not a user-facing header: it exists so the HTTP download path and a future
 * BLE GATT transfer can share ONE write path and ONE RAM policy without
 * pulling esp_ota_ops.h into ota.h (host tests include ota.h).
 *
 * WS-C memory contract (Lead 2026-10-05):
 *   - The chunk buffer (s_ota_io_buf, 4 KiB) is a single static .bss buffer
 *     reused serially by the HTTP download phase and the SHA-256 verify phase.
 *   - It MUST stay in internal RAM: esp_ota_write() runs with the flash cache
 *     disabled for part of the operation, so touching PSRAM from this path
 *     would fault.  Never allocate this buffer through
 *     collector_mem_alloc_pref_psram(), and never relocate it.
 *   - A future BLE transport must call ota_write_chunk() with its own chunk
 *     (GATT writes are <= MTU-3, well under the 4096 bound) instead of keeping
 *     a second OTA buffer.  The function performs the handle/bounds checks;
 *     the caller owns retry/flow control.
 */
#ifndef OTA_INTERNAL_H
#define OTA_INTERNAL_H

#include <stddef.h>
#include <stdint.h>
#include "esp_err.h"
#include "esp_ota_ops.h"

#ifdef __cplusplus
extern "C" {
#endif

/* Largest single write accepted by ota_write_chunk(); equals the shared buffer
 * size, so a full HTTP read and a BLE chunk both go through one code path. */
#define OTA_WRITE_CHUNK_MAX 4096

/**
 * Write one firmware payload chunk to the running OTA update handle.
 *
 * @param handle  Handle returned by esp_ota_begin() (0 is rejected).
 * @param data    Payload bytes (NULL only when len==0 is allowed by esp_ota).
 * @param len     Byte count; must be <= OTA_WRITE_CHUNK_MAX.
 * @return ESP_OK on success, ESP_ERR_INVALID_ARG on a bad call, or the
 *         esp_ota_write() error otherwise.
 */
esp_err_t ota_write_chunk(esp_ota_handle_t handle,
                          const uint8_t *data, size_t len);

#ifdef __cplusplus
}
#endif

#endif /* OTA_INTERNAL_H */
