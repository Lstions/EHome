#ifndef SCHEDULER_QUEUE_GUARD_H
#define SCHEDULER_QUEUE_GUARD_H

#include <stdbool.h>
#include <stddef.h>

/* Pure guard used before invoking FreeRTOS queue APIs. Keeping it independent
 * of FreeRTOS makes the NULL-queue contract host-testable. */
static inline bool scheduler_queue_is_present(const void *queue)
{
    return queue != NULL;
}

#endif /* SCHEDULER_QUEUE_GUARD_H */
