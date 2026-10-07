#ifndef ESP_TIMER_H
#define ESP_TIMER_H

#include <stdint.h>
#include <stdbool.h>
#include "esp_err.h"

/* Host-test controllable time.  Set g_test_time_us before calling code
 * that reads esp_timer_get_time(); the stub returns this value. */
extern int64_t g_test_time_us;

/* task-5: opt-in virtual-clock step.  Default behaviour is byte-for-byte the
 * old frozen clock (the macro is only defined for bus_worker_batch_tests).
 *
 * Why it is needed: uart_collect_response() spins until either its frame
 * predicate fires or the caller's timeout elapses, and the host
 * ulTaskNotifyTake() stub returns immediately.  With a frozen clock the
 * pre-fix code can never reach its timeout, so a test for the new
 * expected-length fast path would HANG instead of failing cleanly when the
 * fast path is mutated away.  A 1 ms step lets the timeout path run, turning
 * "fast path missing" into a normal red assertion. */
#ifdef EHOME_TEST_CLOCK_STEP
extern int64_t g_test_time_step_us;
#define EHOME_TEST_CLOCK_TICK() (g_test_time_us += g_test_time_step_us)
#else
#define EHOME_TEST_CLOCK_TICK() ((void)0)
#endif

static inline int64_t esp_timer_get_time(void)
{
    EHOME_TEST_CLOCK_TICK();
    return g_test_time_us;
}

/* === 一次性定时器的最小替身（2026-10-08 加）===
 *
 * 为什么加：sync_manager.c 用 esp_timer 做"配置接收超时"，而本头文件原先只提供
 * esp_timer_get_time() ⇒ 任何把 sync_manager.c 编进宿主的目标都会因缺类型而编译失败。
 * ⇒ 补上类型与函数声明；**实现**放在各自的 *_stubs.c 里（需要它的目标自己提供），
 *   以免所有测试目标都被迫链接一个"假装在跑"的定时器。
 *
 * ⚠ 语义提醒：替身是 no-op —— 它**不会**让超时真的触发。
 *    凡依赖"超时该触发"的用例必须自己驱动，不能指望这里。 */
typedef struct esp_timer *esp_timer_handle_t;
typedef void (*esp_timer_cb_t)(void *arg);

typedef struct {
    esp_timer_cb_t callback;
    void          *arg;
    const char    *name;
    bool           skip_unhandled_events;
    void          *dispatch_method;
} esp_timer_create_args_t;

esp_err_t esp_timer_create(const esp_timer_create_args_t *args, esp_timer_handle_t *out);
esp_err_t esp_timer_stop(esp_timer_handle_t t);
esp_err_t esp_timer_start_once(esp_timer_handle_t t, uint64_t us);

#endif
