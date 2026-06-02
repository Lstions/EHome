/**
 * @file ota.h
 * @brief OTA Upgrade Manager
 */

#ifndef OTA_H
#define OTA_H

#include <stdint.h>
#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

void ota_init(void);
void ota_start(const char *ota_id, const char *url, const char *checksum, uint64_t size, const char *version);
bool ota_is_upgrading(void);

#ifdef __cplusplus
}
#endif

#endif
