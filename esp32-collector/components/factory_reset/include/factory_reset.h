#pragma once

#include <stdbool.h>

/**
 * Initialize factory reset button monitor.
 * If BOOT button is held for >5 seconds, erases NVS and reboots.
 * BOOT GPIO: S3=GPIO0, C6=GPIO9 (per-chip conditional in .c).
 */
void factory_reset_init(void);

/**
 * Check if factory reset is currently in progress.
 */
bool factory_reset_in_progress(void);

/**
 * Trigger factory reset immediately (erases NVS and reboots).
 * Called by WriteCmd handler when channel_id=0, data=[0xFC, 0x00].
 */
void factory_reset_trigger(void);
