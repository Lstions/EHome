#pragma once

#include <stdbool.h>

/**
 * Initialize factory reset button monitor.
 * If BOOT button (GPIO9) is held for >5 seconds, erases NVS and reboots.
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
