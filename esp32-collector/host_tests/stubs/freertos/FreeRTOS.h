#ifndef FREERTOS_FREERTOS_H
#define FREERTOS_FREERTOS_H

#include <stdint.h>

typedef uint32_t TickType_t;
typedef int BaseType_t;
typedef uint32_t UBaseType_t;

#define portMAX_DELAY 0xffffffffu
#define pdTRUE 1
#define pdFALSE 0
#define pdPASS 1
#define pdFAIL 0

#ifndef pdMS_TO_TICKS
#define pdMS_TO_TICKS(ms) ((TickType_t)(ms))
#endif

/* Event group bit definitions used by event_groups.h and consumer code */
#ifndef BIT0
#define BIT0 (1U << 0)
#endif
#ifndef BIT1
#define BIT1 (1U << 1)
#endif
#ifndef BIT2
#define BIT2 (1U << 2)
#endif
#ifndef BIT3
#define BIT3 (1U << 3)
#endif
#ifndef BIT4
#define BIT4 (1U << 4)
#endif
#ifndef BIT5
#define BIT5 (1U << 5)
#endif

#endif
