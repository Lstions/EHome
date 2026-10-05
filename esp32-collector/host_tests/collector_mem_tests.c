/*
 * collector_mem_tests.c — host proof of the WS-C PSRAM placement contract.
 *
 * The firmware-only branch (CONFIG_COLLECTOR_PSRAM=1) is compiled here with a
 * controllable heap stub.  It proves the three outcomes the components rely
 * on:
 *   1. PSRAM preferred: a SPIRAM allocation is attempted FIRST and returned.
 *   2. PSRAM exhausted: the INTERNAL fallback is attempted and returned.
 *   3. Both exhausted: NULL is returned (callers fail closed; none of the
 *      components fall back to a hidden static reserve).
 *   4. size==0 short-circuits to NULL without touching the heap.
 *   5. collector_mem_free() routes to heap_caps_free() exactly once.
 *
 * Non-PSRAM models compile the static-array branches instead; that path is
 * exercised by every other host test and by the s3/c6 firmware builds.
 */

#include <stdio.h>
#include <stdint.h>

#define CONFIG_COLLECTOR_PSRAM 1
#include "collector_mem.h"

int  g_psram_fail;
int  g_internal_fail;
int  g_psram_calls;
int  g_internal_calls;
int  g_free_calls;
void *g_last_freed;

#define FAKE_PSRAM_ADDR ((void *)(uintptr_t)0x3C000000u)
#define FAKE_INTERNAL_ADDR ((void *)(uintptr_t)0x3FC00000u)

void *heap_caps_malloc(size_t size, int caps)
{
    if ((caps & MALLOC_CAP_SPIRAM) != 0) {
        g_psram_calls++;
        return g_psram_fail ? NULL : FAKE_PSRAM_ADDR;
    }
    if ((caps & MALLOC_CAP_INTERNAL) != 0) {
        g_internal_calls++;
        return g_internal_fail ? NULL : FAKE_INTERNAL_ADDR;
    }
    (void)size;
    return NULL;
}

void heap_caps_free(void *ptr)
{
    g_free_calls++;
    g_last_freed = ptr;
}

static int failures;

#define CHECK(cond, msg) do { \
    if (!(cond)) { fprintf(stderr, "FAIL %s:%d: %s\n", __func__, __LINE__, msg); failures++; } \
} while (0)

static void reset(void)
{
    g_psram_fail = g_internal_fail = 0;
    g_psram_calls = g_internal_calls = g_free_calls = 0;
    g_last_freed = NULL;
}

static void test_prefers_psram(void)
{
    reset();
    void *p = collector_mem_alloc_pref_psram(4096);
    CHECK(p == FAKE_PSRAM_ADDR, "PSRAM must be preferred when available");
    CHECK(g_psram_calls == 1, "exactly one SPIRAM allocation attempt");
    CHECK(g_internal_calls == 0, "no internal allocation when PSRAM succeeded");
}

static void test_falls_back_to_internal(void)
{
    reset();
    g_psram_fail = 1;
    void *p = collector_mem_alloc_pref_psram(4096);
    CHECK(p == FAKE_INTERNAL_ADDR, "internal fallback must be used when PSRAM fails");
    CHECK(g_psram_calls == 1, "PSRAM attempted first");
    CHECK(g_internal_calls == 1, "internal attempted second");
}

static void test_both_exhausted_returns_null(void)
{
    reset();
    g_psram_fail = 1;
    g_internal_fail = 1;
    void *p = collector_mem_alloc_pref_psram(8192);
    CHECK(p == NULL, "both heaps exhausted must yield NULL (callers fail closed)");
    CHECK(g_psram_calls == 1 && g_internal_calls == 1, "both capabilities attempted");
}

static void test_zero_size_short_circuits(void)
{
    reset();
    void *p = collector_mem_alloc_pref_psram(0);
    CHECK(p == NULL, "size 0 must return NULL");
    CHECK(g_psram_calls == 0 && g_internal_calls == 0, "no heap call for size 0");
}

static void test_free_routes_to_heap_caps_free(void)
{
    reset();
    collector_mem_free(FAKE_PSRAM_ADDR);
    CHECK(g_free_calls == 1, "collector_mem_free must call heap_caps_free once");
    CHECK(g_last_freed == FAKE_PSRAM_ADDR, "freed pointer must be the allocated one");
    collector_mem_free(NULL);
    CHECK(g_free_calls == 1, "freeing NULL must be a no-op");
}

int main(void)
{
    test_prefers_psram();
    test_falls_back_to_internal();
    test_both_exhausted_returns_null();
    test_zero_size_short_circuits();
    test_free_routes_to_heap_caps_free();
    if (failures != 0) {
        fprintf(stderr, "collector_mem_tests: %d failure(s)\n", failures);
        return 1;
    }
    puts("collector_mem_tests: all tests passed");
    return 0;
}
