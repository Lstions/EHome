#ifndef HOST_LIFECYCLE_FREERTOS_H
#define HOST_LIFECYCLE_FREERTOS_H
#include <stdint.h>
typedef int BaseType_t;
typedef uint32_t TickType_t;
typedef void *TaskHandle_t;
typedef uint32_t EventBits_t;
typedef struct { uint32_t opaque; } StaticEventGroup_t;
typedef StaticEventGroup_t *EventGroupHandle_t;
#define pdTRUE 1
#define pdFALSE 0
#define pdPASS 1
#define portMAX_DELAY UINT32_MAX
#define BIT0 ((EventBits_t)1U)
#define pdMS_TO_TICKS(ms) ((TickType_t)(ms))
#endif
