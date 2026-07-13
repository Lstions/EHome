#ifndef HOST_LIFECYCLE_ESP_TASK_WDT_H
#define HOST_LIFECYCLE_ESP_TASK_WDT_H
void esp_task_wdt_add(void *task);
void esp_task_wdt_reset(void);
void esp_task_wdt_delete(void *task);
#endif
