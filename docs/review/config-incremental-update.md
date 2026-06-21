# Config Incremental Update Design

> **Status:** Draft
> **Date:** 2026-06-21
> **Problem:** `handle_config_applied()` 全量 teardown/rebuild，即使只改了未绑定的 DMA 通道开关，也会销毁正在运行的 UART1

## 1. Problem Statement

Current `handle_config_applied()` flow:

```
suspend workers → stop scheduler → cleanup ALL buses → setup ALL buses → start scheduler → resume workers
```

This is a sledgehammer approach. When only GDMA_CH0 (unbound) is toggled, it:
1. Stops the scheduler (unnecessary)
2. Deletes UART1 driver (unnecessary, disrupts active Modbus polling)
3. Releases GDMA_CH1 from UART1 (unnecessary)
4. Re-allocates GDMA_CH1 to UART1 (same config, wasted work)
5. Re-initializes UART1 driver (same config, wasted work)
6. Restarts scheduler (unnecessary)

**Impact:** ~10ms of bus downtime per config push, even for irrelevant changes. Modbus reads miss during this window.

## 2. Design: Incremental Diff-and-Patch

### 2.1 Core Principle

Compare old manifest with new manifest. Only teardown/rebuild channels whose config actually changed. Leave untouched channels running.

### 2.2 Diff Granularity

A channel needs rebuild if ANY of these changed:
- `enabled` flag
- `bus_type`
- `bus_config` (any byte)
- `interval_ms` (scheduler timing change)
- `edge_devices` structure

A DMA-only change (e.g., CH0 enabled toggle with no bind_to) that doesn't affect any active bus channel requires NO bus rebuild.

### 2.3 Architecture

```
config_mgr_apply_manifest()
  → stores new manifest, applies DMA pool config
  → returns diff info (what changed)

handle_config_applied(s, diff)
  → if no bus-relevant changes: return immediately
  → if only scheduler-relevant changes: update scheduler in-place
  → if bus channel changes:
      suspend workers (only affected channels)
      teardown changed channels only
      rebuild changed channels only
      update scheduler incrementally
      resume workers
```

### 2.4 Implementation: config_diff_t

```c
typedef struct {
    /* Channels that need teardown+rebuild */
    uint32_t changed_channel_ids[MAX_CHANNELS];
    uint8_t  changed_channel_count;
    
    /* Channels that were removed (existed in old, not in new) */
    uint32_t removed_channel_ids[MAX_CHANNELS];
    uint8_t  removed_channel_count;
    
    /* Channels that were added (exist in new, not in old) */
    uint32_t added_channel_ids[MAX_CHANNELS];
    uint8_t  added_channel_count;
    
    /* True if any DMA config changed that affects bound channels */
    bool     dma_binding_changed;
    
    /* True if only scheduler timing changed (no bus rebuild needed) */
    bool     scheduler_only_change;
    
    /* True if nothing relevant changed at all */
    bool     no_op;
} config_diff_t;
```

### 2.5 Diff Algorithm

```c
static void compute_config_diff(const config_manifest_t *old_m,
                                 const config_manifest_t *new_m,
                                 config_diff_t *diff)
{
    memset(diff, 0, sizeof(*diff));
    
    for (int i = 0; i < new_m->channel_count; i++) {
        const config_channel_t *new_ch = &new_m->channels[i];
        const config_channel_t *old_ch = find_channel(old_m, new_ch->id);
        
        if (!old_ch) {
            /* New channel → add */
            diff->added_channel_ids[diff->added_channel_count++] = new_ch->id;
        } else if (channel_config_changed(old_ch, new_ch)) {
            /* Existing channel with changed config → rebuild */
            diff->changed_channel_ids[diff->changed_channel_count++] = new_ch->id;
        }
        /* else: unchanged, skip */
    }
    
    for (int i = 0; i < old_m->channel_count; i++) {
        const config_channel_t *old_ch = &old_m->channels[i];
        if (!find_channel(new_m, old_ch->id)) {
            /* Old channel not in new → remove */
            diff->removed_channel_ids[diff->removed_channel_count++] = old_ch->id;
        }
    }
    
    /* Check DMA binding changes */
    diff->dma_binding_changed = dma_bindings_changed(old_m, new_m);
    
    /* Determine if this is a no-op */
    diff->no_op = (diff->changed_channel_count == 0 &&
                   diff->removed_channel_count == 0 &&
                   diff->added_channel_count == 0 &&
                   !diff->dma_binding_changed);
}
```

### 2.6 New handle_config_applied Flow

```c
static void handle_config_applied(app_state_t *s)
{
    config_diff_t diff;
    const config_manifest_t *new_m = config_mgr_get_manifest();
    
    compute_config_diff(&s->prev_manifest, new_m, &diff);
    
    /* Save new manifest as prev for next diff */
    s->prev_manifest = *new_m;
    
    if (diff.no_op) {
        ESP_LOGI(TAG, "Config applied: no bus-relevant changes, skip rebuild");
        return;
    }
    
    /* Suspend workers only if bus rebuild needed */
    bool bus_rebuild_needed = (diff.changed_channel_count > 0 ||
                                diff.removed_channel_count > 0 ||
                                diff.added_channel_count > 0 ||
                                diff.dma_binding_changed);
    
    if (bus_rebuild_needed) {
        bus_worker_suspend();
        app_state_lock_config();
        
        /* Teardown changed + removed channels */
        for (int i = 0; i < diff.changed_channel_count; i++)
            bus_manager_teardown_channel(s, diff.changed_channel_ids[i]);
        for (int i = 0; i < diff.removed_channel_count; i++)
            bus_manager_teardown_channel(s, diff.removed_channel_ids[i]);
        
        /* Rebuild changed + added channels */
        for (int i = 0; i < diff.changed_channel_count; i++)
            bus_manager_setup_channel(s, diff.changed_channel_ids[i]);
        for (int i = 0; i < diff.added_channel_count; i++)
            bus_manager_setup_channel(s, diff.added_channel_ids[i]);
        
        app_state_unlock_config();
        bus_worker_resume();
    }
    
    /* Update scheduler incrementally */
    scheduler_apply_diff(&diff, new_m);
    
    rgb_led_set_state(LED_STATE_RUNNING);
}
```

### 2.7 New bus_manager API

```c
/* Teardown a single channel by id */
void bus_manager_teardown_channel(app_state_t *s, uint32_t channel_id);

/* Setup a single channel from current manifest */
void bus_manager_setup_channel(app_state_t *s, uint32_t channel_id);
```

### 2.8 New scheduler API

```c
/* Apply incremental diff to running scheduler */
void scheduler_apply_diff(const config_diff_t *diff, const config_manifest_t *manifest);
```

This replaces the current stop-all/add-all/start-all pattern with:
- Remove removed channels
- Remove then re-add changed channels
- Add new channels

No need to stop/restart the scheduler timer.

## 3. Edge Cases

### 3.1 First config (no prev_manifest)
`prev_manifest.applied = false` → treat as all channels being "added" → full setup (same as current behavior, but only once).

### 3.2 DMA binding change affecting active channel
If `dma_pool_apply_config` changed a bound DMA channel (e.g., CH1 bind_to changed from UART1 to SPI2), the corresponding bus channel must be rebuilt. `dma_binding_changed` flag handles this.

### 3.3 DMA enable/disable of unbound channel
GDMA_CH0 enabled=1, bind_to='' → no bus rebuild needed. The DMA pool state is already updated by `config_mgr_apply_manifest`. This is the exact scenario from the bug report.

### 3.4 Multiple rapid config pushes
The diff is computed against `prev_manifest` which is updated after each apply. Rapid pushes are handled correctly — each push diffs against the last applied state.

## 4. Changes Required

| File | Change |
|------|--------|
| `app_state.h` | Add `config_manifest_t prev_manifest` field |
| `app_state.c` | Initialize `prev_manifest.applied = false` |
| `app_callbacks.c` | Rewrite `handle_config_applied` with diff logic |
| `bus_manager.h` | Add `bus_manager_teardown_channel`, `bus_manager_setup_channel` |
| `bus_manager.c` | Implement single-channel teardown/setup |
| `scheduler.h` | Add `scheduler_apply_diff` |
| `scheduler.c` | Implement incremental scheduler update |
| `config_mgr.h` | Add `config_diff_t` type definition |

## 5. Verification

Test scenarios:
1. Toggle unbound DMA channel (CH0) → no bus rebuild, no scheduler restart
2. Change UART1 baud rate → only UART1 channel rebuilt
3. Add new channel → only new channel initialized
4. Remove channel → only removed channel torn down
5. First boot (no prev manifest) → full setup (backward compatible)
6. DMA binding change → affected channel rebuilt
