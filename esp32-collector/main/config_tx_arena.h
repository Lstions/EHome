/**
 * @file config_tx_arena.h
 * @brief WS-E: fixed-size bump arena for the configuration transaction.
 *
 * Why a fixed arena (design: docs/设计/配置事务确定性设计-2026-10-05.md §2):
 *   The config transaction used to allocate its rollback ctx from the general
 *   heap (`calloc(manifest_tx_ctx_t)`), so a transaction could fail purely
 *   because the heap was fragmented at that instant (S3 field data:
 *   `apply_buses` left free=876 / largest=832).  The arena removes the heap
 *   from that decision: one 4 KiB block is reserved up front and the
 *   transaction only moves a cursor inside it.
 *
 * Contract:
 *   - Single writer.  The caller serialises with `app_state_lock_config()` +
 *     `periph_owner_transaction_begin()`; no internal locking is provided.
 *   - `begin()`/`end()` bracket exactly one transaction.  `end()` only resets
 *     the cursor; memory is never freed (4 KiB, resident for process life).
 *   - Every `alloc()` is 16-byte aligned.  A failed `alloc()` returns NULL and
 *     does NOT move the cursor, so the caller can keep using previous blocks.
 *   - A 4-byte canary sits at the end of the block.  `end()` returns false if
 *     it was overwritten (out-of-bounds write), which the caller must treat as
 *     unrecoverable (`CONFIG_APPLY_FATAL`), not as a normal rollback.
 *
 * Placement:
 *   - PSRAM models (COLLECTOR_MEM_PSRAM_ENABLED): `collector_mem_alloc_pref_psram`
 *     at first use (PSRAM first, internal fallback).  If both fail the arena is
 *     unavailable and the transaction is refused before touching the runtime.
 *   - Internal-only models: a static .bss block (4 KiB, no runtime allocation).
 *
 * Definitions live in config_tx_arena_impl.h, which is included by exactly one
 * translation unit (`main/config_apply_transaction.c`).  That keeps this file
 * a pure interface and avoids adding a source file to main/CMakeLists.txt
 * (owned by another workstream).  Host tests include the impl header directly.
 */
#ifndef CONFIG_TX_ARENA_H
#define CONFIG_TX_ARENA_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/** Total arena size.  See design §2.2: sole consumer is manifest_tx_ctx_t
 *  (792 B measured) plus alignment/headroom, so 4 KiB is a 3.8x margin. */
#define CONFIG_TX_ARENA_BYTES        4096u
/** Minimum bytes a transaction must be able to reserve after begin(): the
 *  transaction context plus alignment/future-field headroom. */
#define CONFIG_TX_ARENA_BUMP_RESERVE 1152u
/** Allocation alignment (largest scalar alignment we place here). */
#define CONFIG_TX_ARENA_ALIGN        16u
/** Canary written at the last 4 bytes on begin(); checked on end(). */
#define CONFIG_TX_ARENA_CANARY       0xC0FFEE00u

/** Allocate the backing block if needed.  Idempotent.
 *  @return false when PSRAM+internal allocation both failed (arena unusable). */
bool config_tx_arena_init(void);

/** Start a transaction: reset cursor, arm canary.
 *  @return false if not initialised, already in a transaction, or the arena
 *  cannot even hold CONFIG_TX_ARENA_BUMP_RESERVE bytes. */
bool config_tx_arena_begin(void);

/** End a transaction: verify canary, reset cursor, clear in-transaction state.
 *  Idempotent (calling it outside a transaction returns true).
 *  @return false only when the canary was overwritten (memory corruption). */
bool config_tx_arena_end(void);

/** Allocate @p n bytes, 16-byte aligned.  NULL when n==0, not in a
 *  transaction, or the arena is exhausted; the cursor is not moved on failure. */
void *config_tx_arena_alloc(size_t n);

/** Maximum bytes allocated inside any single transaction since init(). */
size_t config_tx_arena_high_water(void);

/** Static preflight: can one transaction reserve @p n bytes (plus canary)?
 *  Call this BEFORE begin()/suspend so refusal does not mutate runtime state. */
bool config_tx_arena_can_reserve(size_t n);

/** Total capacity in bytes (== CONFIG_TX_ARENA_BYTES). */
size_t config_tx_arena_capacity(void);

#ifdef CONFIG_TX_ARENA_TESTING
/** Host-test hook: raw backing storage.  Never used by firmware. */
uint8_t *config_tx_arena_test_storage(void);
/** Host-test hook: clear the canary-fault latch so one test can continue.
 *  Firmware deliberately has no such recovery: a detected out-of-bounds write
 *  keeps the arena unavailable until reboot (fail-safe). */
void config_tx_arena_test_clear_fault(void);
#endif

#ifdef __cplusplus
}
#endif

#endif /* CONFIG_TX_ARENA_H */
