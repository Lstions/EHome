#ifndef FREERTOS_TASK_H
#define FREERTOS_TASK_H

#include <stdint.h>
#include <stddef.h>

typedef void *TaskHandle_t;
typedef uint32_t UBaseType_t;
typedef uint32_t TickType_t;

/* 静态任务创建所需的类型（ESP-IDF 在 FreeRTOS.h/task.h 里定义）。
 *
 * 2026-10-04：log_stream 的 log_tx_task 改为 xTaskCreateStatic（实机上满负载时
 * xTaskCreate 会因堆不足创建失败，导致"日志开关 200 成功但一帧都不推"）。
 * 宿主机测试用的是这个精简 stub，缺少这两个类型导致 host tests 编译失败，
 * 所以在此补齐 —— 让 stub 与真实 FreeRTOS 的接口面保持一致。
 *
 * StackType_t 在 Xtensa 上是 4 字节（一个栈单元），必须与真实目标一致：
 * log_stream 用 `StackType_t s_log_tx_stack[LOG_TX_STACK / sizeof(StackType_t)]`
 * 声明栈数组，若这里定义成别的宽度，宿主机与目标的栈尺寸会不一致，
 * 那种偏差在宿主机上看不出来、只会在实机上炸。 */
typedef uint32_t StackType_t;
typedef struct { uint32_t dummy; } StaticTask_t;

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

/* xTaskCreateStatic: 同样的语义，但不依赖堆。
 * 与 xTaskCreate 一样返回非 NULL 表示成功（静态创建的返回即句柄，失败为 NULL）。
 * 必须检查 buf 非空：调用方用 NULL 判断失败，若这里无条件返回句柄，
 * 宿主机就永远测不出"创建失败"这条分支。 */
static inline TaskHandle_t xTaskCreateStatic(void (*task_fn)(void *), const char *name,
                                             uint32_t stack_depth, void *param,
                                             UBaseType_t prio, StackType_t *stack,
                                             StaticTask_t *tcb)
{
    (void)task_fn; (void)name; (void)stack_depth; (void)param; (void)prio;
    if (stack == NULL || tcb == NULL) return NULL;
    return (TaskHandle_t)1;
}

/* Task notification stubs */
static inline uint32_t ulTaskNotifyTake(int clear, TickType_t ticks)
{
    (void)clear; (void)ticks;
    return 0;
}

static inline void xTaskNotifyGive(TaskHandle_t task) { (void)task; }

#endif /* FREERTOS_TASK_H */
