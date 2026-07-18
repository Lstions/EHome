#ifndef HELLO_HOST_FREERTOS_H
#define HELLO_HOST_FREERTOS_H

#include <stdint.h>

typedef int BaseType_t;
typedef unsigned UBaseType_t;
typedef uint32_t TickType_t;
typedef void *QueueHandle_t;
typedef void *TaskHandle_t;
typedef void (*TaskFunction_t)(void *);

#define pdTRUE 1
#define pdFALSE 0
#define pdPASS 1
#define portMAX_DELAY UINT32_MAX
#define pdMS_TO_TICKS(ms) ((TickType_t)(ms))

#endif
