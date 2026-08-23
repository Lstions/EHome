#ifndef HOST_TEST_WIFI_STUBS_ESP_EVENT_H
#define HOST_TEST_WIFI_STUBS_ESP_EVENT_H

/*
 * Minimal esp_event.h stub for host-side unit tests of wifi_mgr.c.
 */

#include "esp_err.h"
#include <stdint.h>

typedef const char *esp_event_base_t;

#define ESP_EVENT_ANY_ID (-1)

esp_err_t esp_event_loop_create_default(void);
esp_err_t esp_event_handler_instance_register(esp_event_base_t event_base,
                                              int32_t event_id,
                                              void *event_handler,
                                              void *event_handler_arg,
                                              void *instance);

#endif /* HOST_TEST_WIFI_STUBS_ESP_EVENT_H */
