#ifndef HOST_LIFECYCLE_TASK_H
#define HOST_LIFECYCLE_TASK_H
#include "freertos/FreeRTOS.h"
typedef void (*TaskFunction_t)(void *);
BaseType_t xTaskCreate(TaskFunction_t task, const char *name, uint32_t stack_depth,
                       void *arg, unsigned priority, TaskHandle_t *out_task);
uint32_t ulTaskNotifyTake(BaseType_t clear_on_exit, TickType_t wait_ticks);
void xTaskNotifyGive(TaskHandle_t task);
TickType_t xTaskGetTickCount(void);
void host_task_yield(void);
void vTaskDelay(TickType_t ticks);
void vTaskDelete(TaskHandle_t task);
#define taskYIELD() host_task_yield()
#endif
