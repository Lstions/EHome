# handle_config_applied 增量配置应用 — 架构方案

> 状态：待审核 | 分支：feat/multi-command-collection | 日期：2026-06-21

## 1. 问题

`handle_config_applied()` 收到 ConfigManifest 后无条件执行全量 teardown/rebuild：

```
bus_worker_suspend → scheduler_stop → bus_manager_cleanup_all → bus_manager_setup_from_manifest → scheduler_start → bus_worker_resume
```

即使只改了一个不相关的 DMA 通道（如 GDMA_CH0 enabled→disabled），UART1 的 DMA 也被释放重建，期间丢失数据。

### 1.1 根因

`config_mgr_apply_manifest()` 先 `clear_manifest()` 再 `parse_manifest()`，新旧 manifest 的比较窗口被销毁。`handle_config_applied` 没有旧状态可比较，只能全量重建。

### 1.2 当前分支状态

`feat/multi-command-collection` 相对于 master 的改动集中在 scheduler 多指令采集、Modbus 扫描、edge_device 等上层功能。`app_callbacks.c`、`bus_manager.c`、`dma_pool/` 三个核心文件与 master 完全一致，全量重建问题未受影响。

## 2. 设计原则

- **单信号替代多信号**：用一个版本号替代 N 字段比较
- **零状态替代有状态**：决策函数是纯函数，输入新旧 manifest，输出操作列表
- **最小停机**：只重建变化的 bus，不 touch 无关 bus

## 3. 方案：分层版本号 + 增量操作

### 3.1 核心思路

在 `config_channel_t` 中增加一个 `config_seq`（单调递增版本号）。`config_mgr_apply_manifest()` 解析新 manifest 时，为每个 channel 分配新 seq。`handle_config_applied` 比较当前运行 bus 的 seq 与新 manifest 的 seq，只重建变化的 channel。

### 3.2 为什么只需要一个 seq

回顾 `bus_dma_ctx_t` 的结构：它保存了 `bus_type`、`dma_enabled`、以及完整的硬件配置（UART port/baud/pins、SPI host/freq/pins 等）。这些全部来自 `config_channel_t` 的 `bus_type` + `bus_config` + DMA 绑定。

**任何导致 bus 需要重建的变更，都会体现在 `bus_config` 或 DMA 绑定上。** 而 `bus_config` 是 `config_channel_t` 的字段，DMA 绑定由 `dma_pool_apply_config()` 在 parse 阶段处理。

因此：**只要 `config_channel_t` 的任何字段变了，seq 就递增，bus 就重建。** 不需要区分 "bus 相关字段" 和 "调度相关字段"——在当前架构下，bus 重建的开销（~50ms）远小于全量重建（~1.2s），且 channel 数量 ≤ 8，即使全重建也只需 ~400ms。

如果未来需要更精细的控制（如只改 `interval_ms` 不重建 bus），可以引入第二个 seq（`bus_seq`），但当前阶段不需要。

### 3.3 数据结构变更

```c
// config_mgr.h — config_channel_t 增加 config_seq
typedef struct {
    // ... existing fields ...
    uint32_t config_seq;   // 单调递增，任何字段变化都递增
} config_channel_t;

// config_mgr.h — config_manifest_t 增加全局 seq 计数器
typedef struct {
    // ... existing fields ...
    uint32_t seq_counter;  // 每次 apply_manifest 递增，分配给 channel
} config_manifest_t;
```

### 3.4 流程变更

#### config_mgr_apply_manifest（修改）

```c
bool config_mgr_apply_manifest(const uint8_t *data, size_t len)
{
    // 不再 clear_manifest()——保留旧 manifest 用于比较
    // 改为：先暂存旧 manifest 的关键信息，再解析新的

    config_manifest_t old;
    memcpy(&old, &s_manifest, sizeof(old));  // 保存旧状态

    clear_manifest();                        // 清空
    s_manifest.seq_counter = old.seq_counter + 1;  // 递增全局 seq

    if (!parse_manifest(data, len)) {        // 解析新 manifest
        // 解析失败，恢复旧 manifest
        memcpy(&s_manifest, &old, sizeof(s_manifest));
        return false;
    }

    // 为每个 channel 分配 seq
    for (int i = 0; i < s_manifest.channel_count; i++) {
        // 查找旧 manifest 中同 id 的 channel
        const config_channel_t *old_ch = NULL;
        for (int j = 0; j < old.channel_count; j++) {
            if (old.channels[j].id == s_manifest.channels[i].id) {
                old_ch = &old.channels[j];
                break;
            }
        }

        if (old_ch == NULL) {
            // 新 channel：分配新 seq
            s_manifest.channels[i].config_seq = s_manifest.seq_counter;
        } else if (memcmp(&s_manifest.channels[i], old_ch,
                          offsetof(config_channel_t, config_seq)) == 0) {
            // 内容完全相同（不含 seq 字段）：保留旧 seq
            s_manifest.channels[i].config_seq = old_ch->config_seq;
        } else {
            // 内容变化：分配新 seq
            s_manifest.channels[i].config_seq = s_manifest.seq_counter;
        }
    }

    s_manifest.applied = true;
    return true;
}
```

**关键点**：`memcmp` 比较的是 struct 中 `config_seq` 之前的所有字段（id, hardware_id, template_ids, interval_ms, enabled, bus_type, bus_config, edge_devices）。这些字段在 `config_seq` 之前，且 struct 是 packed 的（ESP-IDF 默认），所以 `offsetof(config_channel_t, config_seq)` 精确覆盖所有业务字段。

#### handle_config_applied（重写）

```c
static void handle_config_applied(app_state_t *s)
{
    const config_manifest_t *m = config_mgr_get_manifest();
    if (!m || !m->applied) return;

    bus_worker_suspend();
    app_state_lock_config();

    // Phase 1: 找出需要移除的 bus（在运行但不在新 manifest 中）
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (!s->bus_ctx[i].initialized) continue;
        uint32_t ch_id = s->bus_ch[i];

        const config_channel_t *new_ch = NULL;
        for (int j = 0; j < m->channel_count; j++) {
            if (m->channels[j].id == ch_id && m->channels[j].enabled) {
                new_ch = &m->channels[j];
                break;
            }
        }

        if (new_ch == NULL) {
            // Channel 被删除或禁用 → 释放
            bus_manager_unreg_channel(s, i);
            scheduler_remove_channel(ch_id);
        }
    }

    // Phase 2: 找出需要重建或新增的 bus
    bool scheduler_changed = false;
    for (int j = 0; j < m->channel_count; j++) {
        const config_channel_t *new_ch = &m->channels[j];
        if (!new_ch->enabled) continue;

        // 查找当前运行的 bus
        int slot = -1;
        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            if (s->bus_ch[i] == new_ch->id && s->bus_ctx[i].initialized) {
                slot = i;
                break;
            }
        }

        if (slot < 0) {
            // 新 channel → 注册 bus + scheduler
            bus_manager_reg_channel(s, new_ch);
            scheduler_add_channel(new_ch);
            scheduler_changed = true;
        } else {
            // 已存在的 channel → 比较 seq
            // 需要从 scheduler 获取当前运行的 seq
            const sched_channel_t *sc = scheduler_find_channel(new_ch->id);
            if (sc == NULL || sc->config_seq != new_ch->config_seq) {
                // seq 变化 → 重建 bus，更新 scheduler
                bus_manager_unreg_channel(s, slot);
                bus_manager_reg_channel(s, new_ch);
                scheduler_update_channel(new_ch);
                scheduler_changed = true;
            }
            // seq 相同 → 跳过，什么都不做
        }
    }

    // Phase 3: 如果 scheduler 有变化，需要重启
    if (scheduler_changed && scheduler_is_running()) {
        scheduler_stop();
        scheduler_start(s->cmd_queue);
    }

    app_state_unlock_config();
    bus_worker_resume();
    rgb_led_set_state(LED_STATE_RUNNING);
}
```

### 3.5 需要新增的 API

| 模块 | 函数 | 说明 |
|------|------|------|
| bus_manager | `bus_manager_unreg_channel(s, slot)` | 释放单个 bus slot 的 DMA + deinit |
| bus_manager | `bus_manager_reg_channel(s, ch)` | 注册单个 channel（从现有 reg_bus_channel 提取） |
| scheduler | `scheduler_find_channel(id)` | 按 channel_id 查找运行中的 sched_channel_t |
| scheduler | `scheduler_update_channel(ch)` | 更新 channel 的 config（不改变 active 状态） |
| scheduler | `scheduler_channel_seq` 字段 | sched_channel_t 增加 config_seq 字段 |

### 3.6 边界情况

| 场景 | 处理 |
|------|------|
| manifest 为空（0 channel） | Phase 1 清理所有 bus，Phase 2 跳过 |
| 新 channel 的 DMA 分配失败 | reg_bus_channel 降级为 polled，不影响已有 channel |
| 旧 channel 和新 channel id 相同但 bus_type 不同 | seq 不同 → 重建（unreg + reg） |
| DMA bind_to 变更（如 UART1→UART0） | dma_pool_apply_config 在 parse 阶段已处理；bus 重建时重新分配 DMA |
| 只改 interval_ms | config_seq 变化 → bus 重建（开销 ~50ms，可接受） |
| 只改 template 内容 | config_seq 变化 → bus 重建（同上） |

### 3.7 为什么"只改 interval_ms 也重建 bus"是可接受的

1. **当前全量重建 ~1.2s**，增量重建单个 channel ~50ms。即使 8 个 channel 全重建也只需 ~400ms，远好于现状。
2. **interval_ms 变更极少发生**——这是配置参数，不是运行时频繁变更的数据。
3. **复杂度换收益不成比例**——引入 `bus_seq` 分层需要额外 ~60 行代码和更复杂的比较逻辑，但节省的只是极少发生的 ~50ms。
4. **未来可扩展**——如果后续确实需要，可以在 `config_channel_t` 中增加 `bus_seq` 字段，不影响现有架构。

## 4. 实施计划

### Phase 1：核心增量逻辑（~120 行）

**文件变更：**

| 文件 | 操作 | 行数 |
|------|------|------|
| `config_mgr.h` | config_channel_t 增加 config_seq，manifest 增加 seq_counter | +4 |
| `config_mgr.c` | apply_manifest 改为保留旧 manifest + 分配 seq | +40 |
| `app_callbacks.c` | 重写 handle_config_applied | +30/-20 |
| `bus_manager.c` | 新增 unreg_channel，提取 reg_channel 公共函数 | +25 |
| `bus_manager.h` | 声明 unreg_channel, reg_channel | +5 |
| `scheduler.c` | 新增 find_channel, update_channel | +20 |
| `scheduler.h` | sched_channel_t 增加 config_seq，声明新 API | +6 |

**总计：~130 行新增，~20 行删除**

### Phase 2：验证

1. 编译：`idf.py build`（0 errors, 0 warnings）
2. 烧写测试：发送 ConfigManifest，确认只改 DMA enabled 时 UART1 不受影响
3. 边界测试：manifest 为空、channel 增减、DMA bind_to 变更

## 5. 风险与缓解

| 风险 | 缓解 |
|------|------|
| `memcmp` 比较 struct 可能因 padding 不准确 | ESP-IDF 默认 `-fpack-struct`，且 `config_seq` 放在 struct 末尾，`offsetof` 精确覆盖所有字段 |
| 旧 manifest 暂存占用栈空间（~2KB） | `config_manifest_t` 约 2KB，在 `apply_manifest` 的栈上分配，函数返回即释放 |
| `scheduler_update_channel` 在 scheduler 运行中修改状态 | scheduler 的 `s_channels` 是 static 数组，scheduler task 只在 `vTaskDelayUntil` 间隙读取；在 `bus_worker_suspend` 后修改是安全的 |

## 6. 与现有架构的兼容性

- `dma_pool_apply_config()` 已经是增量的，不需要改
- `scheduler_add_channel()` / `scheduler_remove_channel()` 已存在
- `bus_manager` 的 `reg_bus_channel` 逻辑只需提取为公共函数
- `config_mgr_apply_manifest()` 的 `clear_manifest()` 改为"先保存再清空"
- 不影响 `msg_handler`、`sync_manager`、`hello_handshake` 等其他模块
