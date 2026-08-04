#ifndef FREERTOS_QUEUE_H
#define FREERTOS_QUEUE_H

#include <stdint.h>
#include <stddef.h>
#include <string.h>
#include <stdlib.h>

/* Host-test stubs for FreeRTOS queue APIs.  These provide linkable
 * implementations so source files that use QueueHandle_t / QueueSetHandle_t
 * can compile and run on the host without a real FreeRTOS kernel. */

typedef void *QueueHandle_t;
typedef void *QueueSetHandle_t;
typedef void *QueueSetMemberHandle_t;

#ifndef FREERTOS_SEMPHR_H
typedef void *SemaphoreHandle_t;
#endif

typedef uint32_t UBaseType_t;
typedef uint32_t TickType_t;
typedef int BaseType_t;

#define pdMS_TO_TICKS(ms) ((TickType_t)(ms))
#define pdPASS   1
#define pdFAIL   0

/* Simple in-memory queue simulation for host tests.  The structure is
 * opaque to the code under test; only the stub functions below access it. */
typedef struct {
    uint8_t *buf;
    size_t item_size;
    size_t capacity;
    size_t count;
    size_t head;
    size_t tail;
    /* QueueSet membership (real FreeRTOS pxQueueSetContainer).  A queue
     * that transitions empty -> non-empty notifies its set's event queue. */
    void *set_container;
} host_queue_t;

static inline QueueHandle_t xQueueCreate(UBaseType_t queue_length, size_t item_size)
{
    host_queue_t *q = (host_queue_t *)calloc(1, sizeof(host_queue_t));
    if (!q) return NULL;
    q->buf = (uint8_t *)calloc(queue_length, item_size);
    if (!q->buf) { free(q); return NULL; }
    q->item_size = item_size;
    q->capacity = queue_length;
    q->count = 0;
    q->head = 0;
    q->tail = 0;
    return (QueueHandle_t)q;
}

static inline void vQueueDelete(QueueHandle_t queue)
{
    if (!queue) return;
    /* Could be a host_queue_t or host_queue_set_t (both heap-allocated).
     * host_queue_t starts with buf (pointer), item_size, capacity, count...
     * host_queue_set_t starts with members[16] (array of pointers).
     * If the first 8 bytes look like a heap pointer, it's host_queue_t.
     * QueueSet's first field is members[0] which could be a valid pointer
     * but freeing it would be wrong.  Safest: just free the struct itself
     * and leak the buf for sets (sets don't own their member queues). */
    /* Heuristic: sets have member_count at offset 16*8=128. If that value
     * is 0-16, it's a set. For queues, item_size is at offset 8 and is
     * typically small. Check if capacity (offset 16) looks like 1-65536. */
    host_queue_t *q = (host_queue_t *)queue;
    /* If buf is non-NULL and capacity is reasonable, it's a real queue */
    if (q->buf && q->capacity > 0 && q->capacity < 0x10000) {
        free(q->buf);
    }
    free(queue);
}

static inline void host_queue_set_notify(host_queue_t *q); /* fwd, defined below */

static inline BaseType_t xQueueSend(QueueHandle_t queue, const void *item, TickType_t ticks)
{
    (void)ticks;
    if (!queue || !item) return pdFAIL;
    host_queue_t *q = (host_queue_t *)queue;
    if (q->count >= q->capacity) return pdFAIL;
    memcpy(q->buf + q->tail * q->item_size, item, q->item_size);
    q->tail = (q->tail + 1) % q->capacity;
    q->count++;
    host_queue_set_notify(q);
    return pdPASS;
}

static inline BaseType_t xQueueSendToFront(QueueHandle_t queue, const void *item, TickType_t ticks)
{
    (void)ticks;
    if (!queue || !item) return pdFAIL;
    host_queue_t *q = (host_queue_t *)queue;
    if (q->count >= q->capacity) return pdFAIL;
    if (q->head == 0) q->head = q->capacity - 1;
    else q->head--;
    memcpy(q->buf + q->head * q->item_size, item, q->item_size);
    q->count++;
    host_queue_set_notify(q);
    return pdPASS;
}

static inline BaseType_t xQueueReceive(QueueHandle_t queue, void *item, TickType_t ticks)
{
    (void)ticks;
    if (!queue || !item) return pdFAIL;
    host_queue_t *q = (host_queue_t *)queue;
    if (q->count == 0) return pdFAIL;
    memcpy(item, q->buf + q->head * q->item_size, q->item_size);
    q->head = (q->head + 1) % q->capacity;
    q->count--;
    return pdPASS;
}

static inline BaseType_t xQueuePeek(QueueHandle_t queue, void *item, TickType_t ticks)
{
    (void)ticks;
    if (!queue || !item) return pdFAIL;
    host_queue_t *q = (host_queue_t *)queue;
    if (q->count == 0) return pdFAIL;
    memcpy(item, q->buf + q->head * q->item_size, q->item_size);
    return pdPASS;
}

static inline BaseType_t xQueueReset(QueueHandle_t queue)
{
    if (!queue) return pdFAIL;
    host_queue_t *q = (host_queue_t *)queue;
    q->count = 0;
    q->head = 0;
    q->tail = 0;
    return pdPASS;
}

static inline UBaseType_t uxQueueMessagesWaiting(const QueueHandle_t queue)
{
    if (!queue) return 0;
    return ((host_queue_t *)queue)->count;
}

static inline UBaseType_t uxQueueSpacesAvailable(const QueueHandle_t queue)
{
    if (!queue) return 0;
    host_queue_t *q = (host_queue_t *)queue;
    return (UBaseType_t)(q->capacity - q->count);
}

/* QueueSet: faithful host model of FreeRTOS arrival-order FIFO.
 *
 * Real FreeRTOS keeps a per-set event queue: a member queue that
 * transitions empty -> non-empty posts its own handle to the set's event
 * queue (prvNotifyQueueSetContainer), and xQueueSelectFromSet pops that
 * event queue.  Selection is therefore ARRIVAL order, not the order in
 * which members were added to the set.
 */
#define MAX_QUEUE_SET_MEMBERS 16

typedef struct {
    QueueHandle_t members[MAX_QUEUE_SET_MEMBERS];
    size_t member_count;
    QueueHandle_t event_queue; /* FIFO of member handles that became ready */
} host_queue_set_t;

/* Notify a queue-set that a member became non-empty (empty -> non-empty
 * transition only, matching prvNotifyQueueSetContainer). */
static inline void host_queue_set_notify(host_queue_t *q)
{
    host_queue_set_t *s = (host_queue_set_t *)q->set_container;
    if (!s || !s->event_queue) return;
    if (q->count != 1) return; /* only 0 -> 1 posts an event */
    QueueHandle_t member = (QueueHandle_t)q;
    (void)xQueueSend(s->event_queue, &member, 0); /* FIFO append, drop if full */
}

static inline QueueSetHandle_t xQueueCreateSet(UBaseType_t max_length)
{
    host_queue_set_t *s = (host_queue_set_t *)calloc(1, sizeof(host_queue_set_t));
    if (!s) return NULL;
    s->event_queue = xQueueCreate(max_length, sizeof(QueueHandle_t));
    if (!s->event_queue) { free(s); return NULL; }
    return (QueueSetHandle_t)s;
}

static inline void vQueueDeleteSet(QueueSetHandle_t set)
{
    if (!set) return;
    host_queue_set_t *s = (host_queue_set_t *)set;
    if (s->event_queue) vQueueDelete(s->event_queue);
    free(set);
}

static inline BaseType_t xQueueAddToSet(QueueHandle_t queue, QueueSetHandle_t set)
{
    if (!queue || !set) return pdFAIL;
    host_queue_t *q = (host_queue_t *)queue;
    host_queue_set_t *s = (host_queue_set_t *)set;
    if (q->set_container != NULL || s->member_count >= MAX_QUEUE_SET_MEMBERS)
        return pdFAIL;
    s->members[s->member_count++] = queue;
    q->set_container = s;
    return pdPASS;
}

static inline BaseType_t xQueueRemoveFromSet(QueueHandle_t queue, QueueSetHandle_t set)
{
    if (!queue || !set) return pdFAIL;
    host_queue_t *q = (host_queue_t *)queue;
    host_queue_set_t *s = (host_queue_set_t *)set;
    if (q->set_container != s) return pdFAIL;
    for (size_t i = 0; i < s->member_count; i++) {
        if (s->members[i] == queue) {
            s->members[i] = s->members[s->member_count - 1];
            s->member_count--;
            q->set_container = NULL;
            return pdPASS;
        }
    }
    return pdFAIL;
}

static inline QueueSetMemberHandle_t xQueueSelectFromSet(QueueSetHandle_t set, TickType_t ticks)
{
    (void)ticks;
    if (!set) return NULL;
    host_queue_set_t *s = (host_queue_set_t *)set;
    QueueHandle_t member = NULL;
    if (xQueueReceive(s->event_queue, &member, 0) == pdTRUE)
        return (QueueSetMemberHandle_t)member;
    return NULL;
}

#endif /* FREERTOS_QUEUE_H */
