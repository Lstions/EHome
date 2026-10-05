/* Host-test stub for collector_mem.h's PSRAM branch.
 *
 * Deliberately separate from host_tests/stubs/esp_heap_caps.h: that file only
 * declares free/largest/minimum queries (no malloc), because no existing host
 * test needed allocation.  This stub adds a controllable allocator so the
 * WS-C "prefer PSRAM, fall back to internal, else NULL" contract can be
 * proven on the host without an ESP32.
 */
#ifndef HOST_TEST_COLLECTOR_ESP_HEAP_CAPS_H
#define HOST_TEST_COLLECTOR_ESP_HEAP_CAPS_H

#include <stddef.h>

#define MALLOC_CAP_8BIT      (1 << 2)
#define MALLOC_CAP_SPIRAM    (1 << 10)
#define MALLOC_CAP_INTERNAL  (1 << 11)

/* Test-controlled allocation state (defined in collector_mem_tests.c). */
extern int  g_psram_fail;      /* nonzero -> SPIRAM malloc returns NULL */
extern int  g_internal_fail;   /* nonzero -> INTERNAL malloc returns NULL */
extern int  g_psram_calls;     /* number of SPIRAM allocation attempts */
extern int  g_internal_calls;  /* number of INTERNAL allocation attempts */
extern int  g_free_calls;      /* number of heap_caps_free calls */
extern void *g_last_freed;     /* last pointer passed to heap_caps_free */

void *heap_caps_malloc(size_t size, int caps);
void  heap_caps_free(void *ptr);

#endif /* HOST_TEST_COLLECTOR_ESP_HEAP_CAPS_H */
