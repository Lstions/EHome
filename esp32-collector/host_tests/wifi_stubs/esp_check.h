#ifndef HOST_TEST_WIFI_STUBS_ESP_CHECK_H
#define HOST_TEST_WIFI_STUBS_ESP_CHECK_H

/*
 * Minimal ESP_ERROR_CHECK for host tests: abort on non-ESP_OK.
 */

#include "esp_err.h"
#include <stdio.h>
#include <stdlib.h>

#define ESP_ERROR_CHECK(x) do {                                         \
        esp_err_t rc_ = (x);                                            \
        if (rc_ != ESP_OK) {                                            \
            fprintf(stderr, "ESP_ERROR_CHECK failed: %d at %s:%d\n",    \
                    (int)rc_, __FILE__, __LINE__);                      \
            abort();                                                    \
        }                                                               \
    } while (0)

#endif /* HOST_TEST_WIFI_STUBS_ESP_CHECK_H */
