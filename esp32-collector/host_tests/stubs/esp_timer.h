#ifndef ESP_TIMER_H
#define ESP_TIMER_H

#include <stdint.h>

/* Host-test controllable time.  Set g_test_time_us before calling code
 * that reads esp_timer_get_time(); the stub returns this value. */
extern int64_t g_test_time_us;

static inline int64_t esp_timer_get_time(void) { return g_test_time_us; }

#endif
