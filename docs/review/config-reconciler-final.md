# EHomeSystem Config Reconciler — 最终设计方案

> **Status:** Final (after 3-expert debate)
> **Date:** 2026-06-21
> **Experts:** 嵌入式系统架构师, 实时系统专家, 软件设计模式专家

---

## 一、问题本质

**声明式协议（ConfigManifest）+ 命令式执行（全量 teardown/rebuild）= 不相关变更破坏运行中的资源。**

开关一个未绑定的 GDMA_CH0 → UART1/Modbus 采样中断 1.2s → 丢失 2-3 个采样点。

---

## 二、三位专家方案的核心分歧与共识

### 共识（三位一致）

1. **解析与执行必须分离**：`config_mgr_apply_manifest()` 既做解析又改 DMA pool，违反原子性
2. **必须增量更新**：全量重建不可接受，只重建实际变更的 channel
3. **DMA pool 归 bus_manager 管理**：config_mgr 只存意图，不执行
4. **scheduler 不需要 stop/start**：增量 add/remove channel 即可

### 分歧

| 议题 | 架构师 | 实时专家 | 模式专家 | 最终裁定 |
|------|--------|---------|---------|---------|
| **变更检测方式** | config_diff_t（新旧 manifest 对比） | bus_change_set_t（运行时对比） | Runtime Fingerprint（desired vs actual） | **Fingerprint** |
| **是否需要 prev_manifest** | 是（~2KB） | 否（直接对比运行时） | 否（fingerprint ~64B） | **否** |
| **同步机制** | 通道级 suspended 标志 | bus_change_set + per-bus 挂起 | 未深入 | **suspended 标志** |
| **config 数据一致性** | 双缓冲（+2KB RAM） | 未深入 | 未深入 | **双缓冲** |

---

## 三、最终方案：Config Reconciler

### 3.1 核心原则

**配置是声明式的，执行是对比式的，操作是增量式的，结果是幂等的。**

不是 K8s Controller 的照搬，而是其精髓（desired vs actual → diff → action）在 ESP32 上的简化重生：

```
K8s Controller = Informer + WorkQueue + Reconcile Loop + LeaderElection + CRD
EHome Reconciler = MQTT Event + SyncGate + Runtime Fingerprint
```

### 3.2 架构

```
ConfigManifest 到达
  │
  ├─ 1. config_mgr_parse(data, len)          ← 纯函数，零副作用
  │     → 填充 parse_result.manifest
  │     → 计算 desired fingerprint
  │     → 与 actual fingerprint 对比
  │     → 输出 reconcile_actions
  │
  ├─ 2. actions == NONE? → return            ← 快速路径，零开销
  │
  ├─ 3. bus_dma_suspend(affected channels)    ← 通道级，非全局
  │     → 设置 ctx.suspended = true
  │     → rx_task/cmd_task 自动跳过
  │
  ├─ 4. config_mgr_commit(&parse_result)      ← 原子：双缓冲切换（<1μs）
  │
  ├─ 5. bus_manager_reconcile(actions)        ← 增量操作
  │     → teardown removed/changed 通道
  │     → setup added/changed 通道
  │     → apply DMA config changes
  │
  ├─ 6. scheduler_reconcile(actions)          ← 增量，不停止 task
  │     → remove/add/update channels
  │
  ├─ 7. bus_dma_resume(affected channels)     ← 通道级恢复
  │
  └─ 8. update actual_fingerprint             ← 记录新状态
```

### 3.3 Runtime Fingerprint（核心创新）

**不存 prev_manifest，而是对比 desired fingerprint vs actual runtime fingerprint。**

这是方案的关键创新点——不需要额外 2KB 存储 prev_manifest，只需 ~64B 的 fingerprint：

```c
typedef struct {
    struct {
        uint8_t  bus_type;       // UART/SPI/I2C
        uint8_t  bus_id;        // UART0/UART1...
        uint32_t config_hash;   // CRC32(bus_config bytes)
        bool     dma_enabled;   // 当前 DMA 模式
    } bus[SCHED_MAX_CHANNELS];
    uint8_t bus_count;
    
    uint32_t sched_hash;        // CRC32(interval_ms × channel_id)
    uint32_t dma_hash;          // CRC32(dma_configs)
} runtime_fingerprint_t;        // 约 72 bytes
```

**为什么 fingerprint 而不是 manifest diff：**

| 维度 | Manifest Diff | Runtime Fingerprint |
|------|--------------|-------------------|
| 额外 RAM | ~2KB (prev_manifest) | ~72B (fingerprint) |
| 对比对象 | new config vs old config | desired vs actual running state |
| 首次配置 | 需要特殊处理（prev=NULL） | 自动全量（actual 全空） |
| 状态漂移 | 检测不到（只看配置，不看运行时） | 自动修复（fingerprint 反映真实状态） |
| 幂等性 | 部分幂等（同配置可能产生不同 diff） | 完全幂等（fingerprint 相同→零操作） |

### 3.4 Reconcile Actions

fingerprint 对比后，输出结构化的 action 列表：

```c
typedef enum {
    ACTION_NONE = 0,
    ACTION_BUS_TEARDOWN,     // 通道被删除或硬件配置变更
    ACTION_BUS_SETUP,        // 通道被新增或硬件配置变更
    ACTION_SCHED_UPDATE,     // 仅调度参数变更（interval/template）
    ACTION_DMA_RECONFIG,     // DMA 配置变更（可能级联到 bus）
} reconcile_action_type_t;

typedef struct {
    reconcile_action_type_t type;
    uint32_t channel_id;
    union {
        struct { uint32_t new_interval_ms; } sched;
        struct { uint32_t dma_id; bool enabled; char bind_to[16]; } dma;
    } detail;
} reconcile_action_t;

typedef struct {
    reconcile_action_t actions[MAX_CHANNELS * 2 + MAX_DMA_CONFIGS];
    uint8_t count;
    bool no_op;               // 快速路径标志
} reconcile_plan_t;
```

### 3.5 两阶段提交：Parse + Commit

#### Parse（纯函数，零副作用）

```c
reconcile_plan_t config_mgr_parse(const uint8_t *data, size_t len,
                                   const runtime_fingerprint_t *actual_fp) {
    reconcile_plan_t plan = {0};
    config_manifest_t desired = {0};
    
    // 1. 纯解析：填充 desired manifest
    if (!parse_manifest_data(data, len, &desired)) {
        plan.no_op = true;  // 解析失败，不做任何事
        return plan;
    }
    
    // 2. 计算 desired fingerprint
    runtime_fingerprint_t desired_fp = compute_fingerprint(&desired);
    
    // 3. 与 actual fingerprint 对比 → 生成 actions
    compute_reconcile_plan(&desired_fp, actual_fp, &desired, &plan);
    
    return plan;
}
```

#### Commit（原子操作）

```c
bool config_mgr_commit(const config_manifest_t *desired) {
    // 双缓冲切换：s_active_idx 翻转（单字节原子赋值）
    uint8_t new_idx = 1 - s_active_idx;
    s_manifests[new_idx] = *desired;
    s_active_idx = new_idx;  // <1μs，无锁
    return true;
}
```

### 3.6 DMA Pool 生命周期归 bus_manager

**删除 config_mgr 对 dma_pool 的所有写操作。**

```
之前：config_mgr → dma_pool_apply_config()  // 解析时就改 DMA
之后：bus_manager → dma_pool_apply_config()  // reconcile 时才改
```

config_mgr 只做一件事：把 `dma_configs[]` 存入 manifest。执行全归 bus_manager。

### 3.7 通道级挂起替代全局 vTaskSuspend

```c
// bus_dma_ctx_t 新增字段
typedef struct {
    // ...existing fields...
    volatile bool suspended;  // 通道级挂起标志
} bus_dma_ctx_t;

// rx_task 循环中
for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
    if (s->bus_ctx[i].suspended || !s->bus_ctx[i].initialized) continue;
    bus_dma_read(&s->bus_ctx[i], ...);
}

// cmd_task 中
bus_dma_ctx_t *ctx = bus_manager_find_ctx(s, cmd.channel_id);
if (!ctx || ctx->suspended) {
    // 丢弃命令，通知 scheduler
    scheduler_notify_channel_error(cmd.channel_id);
    continue;
}
```

**优势：**
- 不需要 vTaskSuspend（避免死锁风险）
- 只影响变更的通道，其他通道继续运行
- suspended 是 volatile bool，ESP32-C6 上原子读写

### 3.8 Scheduler 增量更新

```c
void scheduler_reconcile(const reconcile_plan_t *plan,
                          const config_manifest_t *manifest) {
    for (int i = 0; i < plan->count; i++) {
        const reconcile_action_t *a = &plan->actions[i];
        switch (a->type) {
        case ACTION_BUS_TEARDOWN:
            scheduler_remove_channel(a->channel_id);
            break;
        case ACTION_BUS_SETUP: {
            const config_channel_t *ch = find_channel(manifest, a->channel_id);
            if (ch) scheduler_add_channel(ch);
            break;
        }
        case ACTION_SCHED_UPDATE:
            // 直接修改 s_channels[slot].config.interval_ms
            // scheduler task 下次 tick 自动使用新参数
            scheduler_update_channel(a->channel_id, a->detail.sched.new_interval_ms);
            break;
        default:
            break;
        }
    }
    // 不需要 scheduler_stop/start，10ms 内生效
}
```

---

## 四、你日志中的场景——方案如何处理

```
输入：DmaChannelConfig: id=0 enabled=1 bind_to=''
      DmaChannelConfig: id=1 enabled=1 bind_to='uart/UART1'  (没变)
      DmaChannelConfig: id=2 enabled=0 bind_to=''            (没变)
```

**Parse 阶段：**
- 解析 desired manifest
- 计算 desired_fingerprint
- 与 actual_fingerprint 对比：
  - bus[0] (UART1): config_hash 相同，dma_enabled 相同 → 无变化
  - dma_hash: CH0 从 disabled→enabled，但 CH0 未绑定任何 bus → 无 bus 级联影响
- reconcile_plan: `no_op = true`（DMA 配置变更不影响运行中的 bus 通道）

**结果：直接返回，UART1 继续运行，0ms 中断。**

DMA pool 的状态更新在 commit 阶段完成（双缓冲切换时 `dma_configs[]` 已更新），实际的 `dma_pool_apply_config(CH0, true, "")` 由 bus_manager 在下次 channel 创建时自动处理。

---

## 五、API 变更清单

| 组件 | 新增 | 删除 | 修改 |
|------|------|------|------|
| **config_mgr** | `config_mgr_parse()`, `config_mgr_commit()`, `runtime_fingerprint_t`, `reconcile_plan_t` | `config_mgr_apply_manifest()`, `config_mgr_set_dma_pool()`, `s_dma_pool` | `config_mgr_get_manifest()` → 双缓冲 |
| **bus_manager** | `bus_manager_reconcile()`, `bus_manager_teardown_channel()`, `bus_manager_setup_channel()` | — | `cleanup_all()` 保留仅用于 factory_reset |
| **bus_dma** | `bus_dma_suspend()`, `bus_dma_resume()`, `ctx.suspended` | — | — |
| **scheduler** | `scheduler_reconcile()`, `scheduler_update_channel()` | — | — |
| **bus_worker** | — | `bus_worker_suspend()`, `bus_worker_resume()` | rx_task/cmd_task 检查 suspended |
| **app_state** | — | `config_mutex` | — |

---

## 六、内存预算

| 项目 | 现状 | 方案 | 增量 |
|------|------|------|------|
| config_manifest 双缓冲 | 2KB (单) | 4KB (双) | +2KB |
| runtime_fingerprint_t | 0 | 72B | +72B |
| bus_dma_ctx_t.suspended | 0 | 8×1B | +8B |
| config_mutex | ~80B | 0 (删除) | -80B |
| reconcile_plan_t | 0 | ~200B (栈上临时) | 0 |
| **净增量** | | | **+2KB (0.59%)** |

---

## 七、实时性改善

| 场景 | 全量更新 | Reconciler | 改善 |
|------|---------|-----------|------|
| 只改 DMA enabled（无关 bus） | 1.2s | **0ms** | ∞ |
| 只改 interval_ms | 1.2s | **<1ms** | 1200x |
| 改 baudrate | 1.2s | **~200ms** | 6x |
| 新增/删除 channel | 1.2s | **50-150ms** | 8-24x |
| 首次配置（无 actual） | 1.2s | 1.2s | 1x (退化) |

---

## 八、幂等性保证

**定理**：如果 `fingerprint(desired) == fingerprint(actual)`，则 reconcile plan 为空。

**推论**：重复下发同一 ConfigManifest → fingerprint 不变 → 零操作 → 幂等。

**状态漂移自愈**：如果运行时状态意外偏离（如 DMA 意外释放），fingerprint 不同 → 自动 reconcile 修复。

---

## 九、实施路线

| Phase | 内容 | 代码量 | 风险 |
|-------|------|--------|------|
| **P1** | config_mgr 拆分：parse + commit，删除 dma_pool setter | ~80行 | 低 |
| **P2** | bus_dma 通道级挂起：suspended 字段，修改 bus_worker | ~60行 | 低 |
| **P3** | config_mgr 双缓冲 | ~50行 | 中 |
| **P4** | runtime_fingerprint + reconcile_plan | ~150行 | 中 |
| **P5** | bus_manager 增量操作 + reconcile | ~200行 | 中 |
| **P6** | scheduler_reconcile | ~80行 | 低 |
| **P7** | handle_config_applied 重写 | ~60行 | 高 (集成) |
| **P8** | 删除旧 API (bus_worker_suspend/resume, config_mutex) | -40行 | 中 |

P1-P3 可并行，P4-P6 依赖 P1，P7 是集成点，P8 是清理。

**预计总代码变更：~640行新增，~200行删除**

---

## 十、为什么这个方案是"根本解决"而非"缝补"

| 缝补方案 | 本方案 | 区别 |
|---------|--------|------|
| 在 handle_config_applied 中加 if 判断跳过 | Config Reconciler 架构 | 前者是补丁，后者是范式 |
| 存 prev_manifest 做 diff | Runtime Fingerprint (desired vs actual) | 前者对比配置历史，后者对比运行时真实状态 |
| vTaskSuspend 全局挂起 | 通道级 suspended 标志 | 前者影响所有通道，后者只影响变更的 |
| config_mutex 保护整个重建过程 | 双缓冲（<1μs 切换） | 前者持锁 1.2s，后者无锁 |
| config_mgr 直接改 DMA pool | bus_manager 独占 DMA 操作 | 前者多修改者无协调，后者单一职责 |
| scheduler stop/start | scheduler_reconcile (增量) | 前者 1s 停机，后者 10ms 生效 |
