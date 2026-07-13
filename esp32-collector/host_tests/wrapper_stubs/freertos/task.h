#ifndef HOST_TEST_FREERTOS_TASK_H
#define HOST_TEST_FREERTOS_TASK_H
#include "freertos/FreeRTOS.h"
BaseType_t xPortInIsrContext(void);
TickType_t xTaskGetTickCount(void);
void host_task_yield(void);
void vTaskDelay(TickType_t ticks);
#define taskYIELD() host_task_yield()
#endif
