#ifndef HOST_LIFECYCLE_FREERTOS_H
#define HOST_LIFECYCLE_FREERTOS_H
#include <stdint.h>
typedef int BaseType_t;
typedef uint32_t TickType_t;
typedef void *TaskHandle_t;
typedef uint32_t EventBits_t;
/* 静态任务创建所需的类型（与真实 FreeRTOS 接口面对齐）。
 *
 * 2026-10-04：log_stream 的 log_tx_task 改为 xTaskCreateStatic —— 实机满负载
 * （5 总线 + 6 PWM + 全部模板）时空闲堆仅约 13.7KB，xTaskCreate 分配 4096 字节
 * 栈会失败，表现为"日志开关返回 200 但一帧都不推"。改用静态内存后不再依赖堆。
 *
 * StackType_t 必须是目标上的栈单元宽度（Xtensa 为 4 字节）：log_stream 用
 * `StackType_t s_log_tx_stack[LOG_TX_STACK / sizeof(StackType_t)]` 声明栈数组，
 * 宿主机的宽度若与目标不同，宿主机测出来的栈尺寸就是错的，而那种偏差
 * 在宿主机上完全看不出来。 */
typedef uint32_t StackType_t;
typedef struct { uint32_t opaque; } StaticTask_t;
typedef struct { uint32_t opaque; } StaticEventGroup_t;
typedef StaticEventGroup_t *EventGroupHandle_t;
#define pdTRUE 1
#define pdFALSE 0
#define pdPASS 1
#define portMAX_DELAY UINT32_MAX
#define BIT0 ((EventBits_t)1U)
#define pdMS_TO_TICKS(ms) ((TickType_t)(ms))
#endif
