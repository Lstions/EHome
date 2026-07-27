#ifndef FREERTOS_EVENT_GROUPS_H
#define FREERTOS_EVENT_GROUPS_H

#include <stdint.h>
#include <stddef.h>

typedef uint32_t EventBits_t;
typedef void *EventGroupHandle_t;

#ifndef pdMS_TO_TICKS
#define pdMS_TO_TICKS(ms) ((uint32_t)(ms))
#endif
#define portMAX_DELAY 0xffffffffu
#define pdTRUE 1
#define pdFALSE 0
#define pdPASS 1
#define pdFAIL 0

typedef int BaseType_t;

static inline EventGroupHandle_t xEventGroupCreate(void) { return (EventGroupHandle_t)1; }
static inline void vEventGroupDelete(EventGroupHandle_t eg) { (void)eg; }
static inline EventBits_t xEventGroupSetBits(EventGroupHandle_t eg, const EventBits_t bits)
{
    (void)eg; return bits;
}
static inline EventBits_t xEventGroupClearBits(EventGroupHandle_t eg, const EventBits_t bits)
{
    (void)eg; (void)bits; return 0;
}
static inline EventBits_t xEventGroupWaitBits(EventGroupHandle_t eg,
                                              const EventBits_t bits, int clearOnExit,
                                              int waitForAll, TickType_t ticks)
{
    (void)eg; (void)bits; (void)clearOnExit; (void)waitForAll; (void)ticks;
    return 0xffffffffu;
}

#endif /* FREERTOS_EVENT_GROUPS_H */
