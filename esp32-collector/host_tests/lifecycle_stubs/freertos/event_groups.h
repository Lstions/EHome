#ifndef HOST_LIFECYCLE_EVENT_GROUPS_H
#define HOST_LIFECYCLE_EVENT_GROUPS_H
#include "freertos/FreeRTOS.h"
EventGroupHandle_t xEventGroupCreateStatic(StaticEventGroup_t *storage);
EventBits_t xEventGroupClearBits(EventGroupHandle_t group, EventBits_t bits);
EventBits_t xEventGroupSetBits(EventGroupHandle_t group, EventBits_t bits);
EventBits_t xEventGroupWaitBits(EventGroupHandle_t group, EventBits_t bits,
                                BaseType_t clear_on_exit, BaseType_t wait_for_all,
                                TickType_t wait_ticks);
#endif
