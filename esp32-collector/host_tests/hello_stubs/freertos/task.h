#ifndef HELLO_HOST_TASK_H
#define HELLO_HOST_TASK_H
#include "freertos/FreeRTOS.h"
BaseType_t xTaskCreate(TaskFunction_t task, const char *name, uint32_t stack_depth,
                       void *arg, UBaseType_t priority, TaskHandle_t *handle);
void vTaskDelete(TaskHandle_t task);
BaseType_t xTaskNotifyGive(TaskHandle_t task);
uint32_t ulTaskNotifyTake(BaseType_t clear_on_exit, TickType_t wait);
#endif
