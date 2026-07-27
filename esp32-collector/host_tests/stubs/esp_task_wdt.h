#ifndef ESP_TASK_WDT_H
#define ESP_TASK_WDT_H

static inline void esp_task_wdt_add(void *handle) { (void)handle; }
static inline void esp_task_wdt_reset(void) {}
static inline void esp_task_wdt_delete(void *handle) { (void)handle; }

#endif
