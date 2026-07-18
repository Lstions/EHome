#ifndef HELLO_HOST_CONFIG_MGR_H
#define HELLO_HOST_CONFIG_MGR_H
#include <stdbool.h>
#include <stdint.h>
bool config_mgr_has_manifest(void);
uint8_t config_mgr_get_active_channel_count(void);
uint64_t config_mgr_get_epoch(void);
const char *config_mgr_get_last_known_manifest_id(void);
typedef struct config_manifest config_manifest_t;
#endif
