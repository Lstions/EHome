/**
 * @file config_tx_arena_impl.h
 * @brief WS-E transaction arena implementation (header-only, single TU include).
 *
 * Included by exactly one firmware translation unit
 * (`main/config_apply_transaction.c`) and directly by the host tests.  Kept as
 * a header so no new file is added to main/CMakeLists.txt (another workstream
 * owns that file) while the host tests can still drive the real code.
 *
 * See main/config_tx_arena.h for the public contract and
 * docs/设计/配置事务确定性设计-2026-10-05.md §2 for the sizing rationale.
 */

#ifndef CONFIG_TX_ARENA_IMPL_H
#define CONFIG_TX_ARENA_IMPL_H

#include "config_tx_arena.h"
#include "collector_mem.h"

#include <string.h>

#if CONFIG_TX_ARENA_BYTES < CONFIG_TX_ARENA_BUMP_RESERVE
#error "config_tx_arena must be at least CONFIG_TX_ARENA_BUMP_RESERVE bytes"
#endif
#if (CONFIG_TX_ARENA_BYTES % CONFIG_TX_ARENA_ALIGN) != 0
#error "config_tx_arena size must be a multiple of CONFIG_TX_ARENA_ALIGN"
#endif

#if COLLECTOR_MEM_PSRAM_ENABLED
/* PSRAM model: allocate the single block at first use; never freed (4 KiB). */
static uint8_t *s_tx_arena_mem;
#else
/* Internal-only model: resident .bss block, no runtime allocation at all. */
static uint8_t s_tx_arena_mem[CONFIG_TX_ARENA_BYTES]
    __attribute__((aligned(CONFIG_TX_ARENA_ALIGN)));
#endif

static size_t s_tx_arena_cursor;      /* bytes handed out in current transaction */
static size_t s_tx_arena_high_water;  /* max cursor observed, for diagnostics */
static bool   s_tx_arena_in_tx;       /* re-entry guard */
static bool   s_tx_arena_ready;       /* init succeeded */
/* Latched when the end-of-block canary is damaged: an out-of-bounds write has
 * already happened, so the arena must not be handed out again until reboot.
 * init() deliberately does NOT clear this. */
static bool   s_tx_arena_faulted;

static inline void config_tx_arena_store_id(uint8_t *p, uint32_t v)
{
    /* memcpy avoids unaligned uint32_t* stores on the last 4 bytes. */
    memcpy(p, &v, sizeof(v));
}

static inline size_t config_tx_arena_capacity_internal(void)
{
    return (size_t)CONFIG_TX_ARENA_BYTES;
}

bool config_tx_arena_init(void)
{
    if (s_tx_arena_faulted) return false;   /* fail-safe: no silent healing */
    if (s_tx_arena_ready) return true;
#if COLLECTOR_MEM_PSRAM_ENABLED
    if (s_tx_arena_mem == NULL) {
        s_tx_arena_mem = (uint8_t *)collector_mem_alloc_pref_psram(CONFIG_TX_ARENA_BYTES);
    }
    if (s_tx_arena_mem == NULL) return false;
#endif
    memset(s_tx_arena_mem, 0, config_tx_arena_capacity_internal());
    s_tx_arena_cursor = 0;
    s_tx_arena_high_water = 0;
    s_tx_arena_in_tx = false;
    s_tx_arena_ready = true;
    return true;
}

bool config_tx_arena_begin(void)
{
    if (!s_tx_arena_ready || s_tx_arena_in_tx) return false;
    s_tx_arena_cursor = 0;
    /* Canary lives in the last 4 bytes.  The usable region is therefore
     * [0, capacity-4); alloc() enforces that. */
    config_tx_arena_store_id(&s_tx_arena_mem[config_tx_arena_capacity_internal() - sizeof(uint32_t)],
                             CONFIG_TX_ARENA_CANARY);
    s_tx_arena_in_tx = true;
    return true;
}

bool config_tx_arena_end(void)
{
    if (!s_tx_arena_ready) return true;
    if (!s_tx_arena_in_tx) return true;

    uint32_t canary = 0;
    memcpy(&canary, &s_tx_arena_mem[config_tx_arena_capacity_internal() - sizeof(uint32_t)],
           sizeof(canary));
    s_tx_arena_in_tx = false;
    s_tx_arena_cursor = 0;
    if (canary != CONFIG_TX_ARENA_CANARY) {
        /* An out-of-bounds write corrupted the canary.  Something already
         * wrote past its allocation, so the arena must never be handed out
         * again: latch it unavailable (init() does not clear the latch) until
         * the device reboots.  A later transaction fails loudly instead of
         * silently reusing corrupted memory. */
        s_tx_arena_ready = false;
        s_tx_arena_faulted = true;
        return false;
    }
    return true;
}

void *config_tx_arena_alloc(size_t n)
{
    if (!s_tx_arena_ready || !s_tx_arena_in_tx || n == 0) return NULL;

    size_t aligned = (n + (CONFIG_TX_ARENA_ALIGN - 1)) & ~(size_t)(CONFIG_TX_ARENA_ALIGN - 1);
    size_t usable = config_tx_arena_capacity_internal() - sizeof(uint32_t);  /* keep canary */
    if (s_tx_arena_cursor > usable || aligned > usable - s_tx_arena_cursor) {
        return NULL;  /* exhausted; cursor deliberately unchanged */
    }

    void *p = &s_tx_arena_mem[s_tx_arena_cursor];
    s_tx_arena_cursor += aligned;
    if (s_tx_arena_cursor > s_tx_arena_high_water) s_tx_arena_high_water = s_tx_arena_cursor;
    return p;
}

size_t config_tx_arena_high_water(void)
{
    return s_tx_arena_high_water;
}

bool config_tx_arena_can_reserve(size_t n)
{
    if (!s_tx_arena_ready || n == 0) return false;
    size_t aligned = (n + (CONFIG_TX_ARENA_ALIGN - 1)) & ~(size_t)(CONFIG_TX_ARENA_ALIGN - 1);
    /* Preflight runs before begin(); only the static capacity matters here.
     * (begin() also requires BUMP_RESERVE, checked by the caller contract.) */
    return aligned <= config_tx_arena_capacity_internal() - sizeof(uint32_t);
}

size_t config_tx_arena_capacity(void)
{
    return config_tx_arena_capacity_internal();
}

#ifdef CONFIG_TX_ARENA_TESTING
uint8_t *config_tx_arena_test_storage(void)
{
    return s_tx_arena_mem;
}

void config_tx_arena_test_clear_fault(void)
{
    /* Host-test isolation only.  Firmware has no equivalent: a detected
     * out-of-bounds write is unrecoverable until reboot. */
    s_tx_arena_faulted = false;
    s_tx_arena_ready = false;
}
#endif

#endif /* CONFIG_TX_ARENA_IMPL_H */
