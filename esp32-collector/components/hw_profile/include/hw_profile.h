/**
 * @file hw_profile.h
 * @brief Hardware Profile — report encoding for ResourceReport
 *
 * Static hardware tables and type definitions are in hw_tables.h.
 * This header provides hw_profile_build_report() to encode a binary
 * ResourceReport (MSG_RESOURCE_REPORT = 0x19) via frame_codec.
 */

#ifndef HW_PROFILE_H
#define HW_PROFILE_H

#include "hw_tables.h"
#include "config_mgr.h"  /* for config_channel_t type only */

#ifdef __cplusplus
extern "C" {
#endif

/* === Report builder === */

/**
 * @brief Build a binary ResourceReport frame (MSG_RESOURCE_REPORT = 0x19).
 *
 * Encodes the full hardware profile + enabled config channels into a
 * frame_codec binary frame ready for transmission.
 *
 * @param buf      Output buffer (caller-allocated).
 * @param sz       Capacity of buf in bytes.
 * @param out_len  On success, set to the encoded frame length.
 * @return true on success, false on buffer overflow or encoding error.
 */
struct dma_pool_t;
bool hw_profile_build_report(uint8_t *buf, size_t sz, size_t *out_len,
                              struct dma_pool_t *dma_pool,
                              const config_channel_t *channels, uint8_t channel_count);

/* Set once during app startup. The ResourceReport owns a copied value. */
void hw_profile_set_boot_id(const char *boot_id);

/** Runtime lease snapshot used to enrich ResourceReport channel entries. */
void hw_profile_runtime_set(uint32_t channel_id, uint8_t bus_type,
                            uint32_t controller_id, bool dma_requested,
                            bool dma_allocated, uint32_t generation);
void hw_profile_runtime_remove(uint32_t channel_id);
void hw_profile_runtime_clear(void);

#ifdef __cplusplus
}
#endif

#endif /* HW_PROFILE_H */
