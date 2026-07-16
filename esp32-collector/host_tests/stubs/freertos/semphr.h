#ifndef FREERTOS_SEMPHR_H
#define FREERTOS_SEMPHR_H

#include <stdint.h>
typedef void *SemaphoreHandle_t;
SemaphoreHandle_t xSemaphoreCreateMutex(void);
int xSemaphoreTake(SemaphoreHandle_t semaphore, uint32_t ticks);
int xSemaphoreGive(SemaphoreHandle_t semaphore);

#endif
