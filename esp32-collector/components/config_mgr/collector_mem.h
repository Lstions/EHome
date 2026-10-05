/**
 * @file collector_mem.h
 * @brief WS-C: PSRAM-preferred allocation for large, non-DMA data blocks.
 *
 * Policy (Lead 2026-10-05):
 *   - PSRAM models (CONFIG_COLLECTOR_PSRAM=y) keep big, purely CPU-accessed
 *     data arrays in PSRAM: manifests, scheduler channels, report payload
 *     blocks, per-channel stream buffers.
 *   - Everything that must stay in internal RAM is NOT routed through this
 *     helper: task stacks, DMA buffers, OTA read buffers and ISR paths.
 *   - If the PSRAM allocation fails we fall back to internal RAM at runtime,
 *     never to a static .bss reserve (that would silently defeat the whole
 *     point of the model split).
 *
 * The function is a static inline on purpose: it is tiny, it keeps component
 * CMake dependencies unchanged, and every caller wants the same two lines.
 */
#ifndef COLLECTOR_MEM_H
#define COLLECTOR_MEM_H

#include <stddef.h>
#include <stdbool.h>

/* Exposed so callers can compile the pointer-vs-array declaration branch
 * without repeating the Kconfig symbol test.
 *
 * Deliberately keyed on CONFIG_COLLECTOR_PSRAM (the model switch), NOT on
 * CONFIG_IDF_TARGET_ESP32S3: host tests link the same component sources
 * without any sdkconfig.h, and a target-based branch would make the host
 * layout differ from what they assert. */
#if defined(CONFIG_COLLECTOR_PSRAM) && CONFIG_COLLECTOR_PSRAM
#define COLLECTOR_MEM_PSRAM_ENABLED 1
#else
#define COLLECTOR_MEM_PSRAM_ENABLED 0
#endif

#if COLLECTOR_MEM_PSRAM_ENABLED
#include "esp_heap_caps.h"

/**
 * Allocate @p size bytes, preferring external PSRAM, falling back to internal
 * RAM.  Returns NULL only when both capabilities are exhausted.
 *
 * MALLOC_CAP_8BIT is OR'ed in so the result is byte-addressable on targets
 * where PSRAM also exposes wider-only mappings.  The returned pointer is
 * always freed with heap_caps_free() (works for either capability).
 */
static inline void *collector_mem_alloc_pref_psram(size_t size)
{
    if (size == 0) return NULL;
    void *p = heap_caps_malloc(size, MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT);
    if (p == NULL) {
        p = heap_caps_malloc(size, MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT);
    }
    return p;
}
#else
/**
 * Non-PSRAM models keep their static arrays in internal RAM; this stub exists
 * so the common call sites stay branch-free and the host build does not need
 * heap_caps_malloc() in its stubs.
 */
static inline void *collector_mem_alloc_pref_psram(size_t size)
{
    (void)size;
    return NULL;
}
#endif

/** Free a pointer returned by collector_mem_alloc_pref_psram(), if any. */
static inline void collector_mem_free(void *ptr)
{
#if COLLECTOR_MEM_PSRAM_ENABLED
    if (ptr) heap_caps_free(ptr);
#else
    (void)ptr;
#endif
}

#endif /* COLLECTOR_MEM_H */
