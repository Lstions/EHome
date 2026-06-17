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
                              struct dma_pool_t *dma_pool);

#ifdef __cplusplus
}
#endif

#endif /* HW_PROFILE_H */
