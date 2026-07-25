#ifndef FREERTOS_TASK_H
#define FREERTOS_TASK_H

#include <stdint.h>
#include <stddef.h>

typedef void *TaskHandle_t;
typedef uint32_t UBaseType_t;
typedef uint32_t TickType_t;

#ifndef pdMS_TO_TICKS
#define pdMS_TO_TICKS(ms) ((TickType_t)(ms))
#endif
#define tskIDLE_PRIORITY 0

static inline void vTaskDelay(TickType_t ticks) { (void)ticks; }
static inline void vTaskDelete(TaskHandle_t task) { (void)task; }
static inline TaskHandle_t xTaskGetCurrentTaskHandle(void) { return (TaskHandle_t)1; }
static inline UBaseType_t uxTaskPriorityGet(const TaskHandle_t task) { (void)task; return 5; }
static inline UBaseType_t uxTaskGetStackHighWaterMark(const TaskHandle_t task) { (void)task; return 4096; }
static inline TickType_t xTaskGetTickCount(void) { return 0; }

/* xTaskCreate: returns pdPASS without creating a real task */
static inline int xTaskCreate(void (*task_fn)(void *), const char *name,
                              uint32_t stack, void *param, UBaseType_t prio,
                              TaskHandle_t *handle)
{
    (void)task_fn; (void)name; (void)stack; (void)param; (void)prio;
    if (handle) *handle = (TaskHandle_t)1;
    return 1; /* pdPASS */
}

/* Task notification stubs */
static inline uint32_t ulTaskNotifyTake(int clear, TickType_t ticks)
{
    (void)clear; (void)ticks;
    return 0;
}

static inline void xTaskNotifyGive(TaskHandle_t task) { (void)task; }

#endif /* FREERTOS_TASK_H */
