#ifndef HOST_LIFECYCLE_ESP_TASK_WDT_H
#define HOST_LIFECYCLE_ESP_TASK_WDT_H

#include "esp_err.h"

/* 与 stubs/esp_task_wdt.h 同源：返回类型对齐真实 IDF（esp_err_t），
 * 不再用 void。本目标（log_stream_lifecycle_tests）当前并未调用这些
 * 符号，但对齐可避免后续接线时踩到与 uart_flush_input 同一类漂移。 */
esp_err_t esp_task_wdt_add(void *task);
esp_err_t esp_task_wdt_reset(void);
esp_err_t esp_task_wdt_delete(void *task);

#endif
