#ifndef LOG_CAPTURE_ESP_H
#define LOG_CAPTURE_ESP_H

#include "log_capture.h"

#ifdef __cplusplus
extern "C" {
#endif

void log_capture_esp_attach(log_capture_t *capture);

/**
 * Stop new wrapper readers and wait a bounded time for existing readers.
 * Must be called from task context. Returns false on ISR use or timeout.
 */
bool log_capture_esp_detach(void);

#ifdef __cplusplus
}
#endif
#endif
