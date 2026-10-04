#ifndef HOST_LIFECYCLE_TASK_H
#define HOST_LIFECYCLE_TASK_H
#include "freertos/FreeRTOS.h"
typedef void (*TaskFunction_t)(void *);
BaseType_t xTaskCreate(TaskFunction_t task, const char *name, uint32_t stack_depth,
                       void *arg, unsigned priority, TaskHandle_t *out_task);

/* 静态创建：返回句柄，失败返回 NULL（与 ESP-IDF 语义一致）。
 * log_stream 用它替代 xTaskCreate，避免满负载下因堆不足创建失败。 */
TaskHandle_t xTaskCreateStatic(TaskFunction_t task, const char *name,
                               uint32_t stack_depth, void *arg, unsigned priority,
                               StackType_t *stack, StaticTask_t *tcb);
uint32_t ulTaskNotifyTake(BaseType_t clear_on_exit, TickType_t wait_ticks);
void xTaskNotifyGive(TaskHandle_t task);
TickType_t xTaskGetTickCount(void);
void host_task_yield(void);
void vTaskDelay(TickType_t ticks);
void vTaskDelete(TaskHandle_t task);
#define taskYIELD() host_task_yield()
#endif
