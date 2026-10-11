/* freertos_sem_stub.c —— 宿主可用的**真**信号量实现（pthread 支撑）。
 *
 * 为什么不能再用 "return (SemaphoreHandle_t)1" 那种桩：
 * 2026-10-11 修的缺陷是**两个任务并发释放同一个 handle**（真机每 60s 崩溃 9 次）。
 * 若桩把 take/give 做成空操作或恒成功，那么"只 close 一次""不 free 在用的句柄"
 * 这些断言在单线程下**照样全绿**，测的就不是并发语义 —— 属于仓库反复记的
 * "测试绿灯但没测东西"。⇒ 这里用 pthread_mutex 给出真实互斥，
 * 并用 pthread 线程构造真并发用例。
 *
 * 语义对齐 FreeRTOS 二值/互斥信号量在**单核调度**下的可观察行为：
 *   · xSemaphoreTake 阻塞直到可用（不模拟 tick 超时 —— 用例里不依赖超时）
 *   · 递归 take 不模拟（FreeRTOS 互斥量本可递归，本仓调用点全是平铺的）
 */
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"

#include <pthread.h>
#include <stdlib.h>

struct host_sem {
    pthread_mutex_t mtx;
};

SemaphoreHandle_t xSemaphoreCreateMutex(void)
{
    struct host_sem *s = (struct host_sem *)calloc(1, sizeof(*s));
    if (s == NULL) return NULL;
    if (pthread_mutex_init(&s->mtx, NULL) != 0) {
        free(s);
        return NULL;
    }
    return (SemaphoreHandle_t)s;
}

SemaphoreHandle_t xSemaphoreCreateBinary(void)
{
    /* 二值信号量与互斥量在本桩里同为"可重入的锁"；
     * 本仓的 take 语义是"取到就继续"，不区分初值。 */
    return xSemaphoreCreateMutex();
}

int xSemaphoreTake(SemaphoreHandle_t semaphore, uint32_t ticks)
{
    (void)ticks;
    struct host_sem *s = (struct host_sem *)semaphore;
    if (s == NULL) return 0;
    return pthread_mutex_lock(&s->mtx) == 0 ? 1 : 0;
}

int xSemaphoreGive(SemaphoreHandle_t semaphore)
{
    struct host_sem *s = (struct host_sem *)semaphore;
    if (s == NULL) return 0;
    return pthread_mutex_unlock(&s->mtx) == 0 ? 1 : 0;
}

void vSemaphoreDelete(SemaphoreHandle_t semaphore)
{
    struct host_sem *s = (struct host_sem *)semaphore;
    if (s == NULL) return;
    pthread_mutex_destroy(&s->mtx);
    free(s);
}
