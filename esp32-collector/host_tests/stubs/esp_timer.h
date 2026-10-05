#ifndef ESP_TIMER_H
#define ESP_TIMER_H

#include <stdint.h>

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

#endif
