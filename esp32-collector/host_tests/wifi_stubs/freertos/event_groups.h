#ifndef HOST_TEST_WIFI_STUBS_FREERTOS_EVENT_GROUPS_H
#define HOST_TEST_WIFI_STUBS_FREERTOS_EVENT_GROUPS_H

/*
 * Minimal event_groups.h stub for host-side unit tests of wifi_mgr.c.
 * Declarations only; the test file provides the definitions so it can
 * track bits deterministically.
 */

#include <stdint.h>

typedef uint32_t EventBits_t;
typedef void *EventGroupHandle_t;

EventGroupHandle_t xEventGroupCreate(void);
EventBits_t xEventGroupWaitBits(EventGroupHandle_t eg, EventBits_t bits,
                                int clear_on_exit, int wait_for_all,
                                uint32_t ticks);
EventBits_t xEventGroupClearBits(EventGroupHandle_t eg, EventBits_t bits);
EventBits_t xEventGroupSetBits(EventGroupHandle_t eg, EventBits_t bits);

#endif /* HOST_TEST_WIFI_STUBS_FREERTOS_EVENT_GROUPS_H */
