#ifndef FREERTOS_SEMPHR_H
#define FREERTOS_SEMPHR_H

#include <stdint.h>
typedef void *SemaphoreHandle_t;
SemaphoreHandle_t xSemaphoreCreateMutex(void);
/* ⭐ 2026-10-10：命令 fence 的事件等待用二值信号量（替代 5ms 轮询）。
 * host 测试需要它的声明 + 一个行为等价（可 take/give/超时）的实现，
 * 见 stubs/freertos/semphr_stub.c。 */
SemaphoreHandle_t xSemaphoreCreateBinary(void);
int xSemaphoreTake(SemaphoreHandle_t semaphore, uint32_t ticks);
int xSemaphoreGive(SemaphoreHandle_t semaphore);
void vSemaphoreDelete(SemaphoreHandle_t semaphore);

#endif
