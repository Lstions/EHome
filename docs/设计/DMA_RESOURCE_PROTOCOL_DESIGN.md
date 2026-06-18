# DMA 资源管理协议设计方案

**版本**: v1.0  
**日期**: 2026-06-16  
**状态**: 部分已实现。协议字段 (ResourceReport field 8, ConfigManifest field 5) 已完成编解码，dma_pool 组件已落地 (三级分配策略)，前端 DMA 面板已对接 (展示+开关)，后端 API 已实现 (GET /nodes/:id/dma-channels, PUT /nodes/:id/dma-config)。剩余: DMA 冲突自动降级、Linux 平台适配。
**最近更新**: 2026-06-18 — 同步代码实现状态。关联 commit: 3974ce7, 92121b5, 952cc81, f5d9f36, 8a9bddc

---

## 1. 背景与问题

### 1.1 硬件事实

| 平台 | DMA 通道数 | UART DMA 约束 | SPI DMA 约束 | I2C DMA |
|------|-----------|--------------|-------------|---------|
| ESP32-C6 | 3 (GDMA CH0/1/2) | CH0/CH2 通用, 共享 | CH1 SPI2专用 | 不支持 |
| ESP32-S3 | 6 (GDMA CH0-5) | CH0-4 通用 | CH5 SPI2专用 | 不支持 |
| Linux RK3576 | N (DTS绑定) | DTS 配置 | DTS 配置 | DTS 配置 |
| Linux x86 | 0 | 不支持 | 不支持 | 不支持 |

### 1.2 核心矛盾

- **ESP32-C6**: UART0/1 支持 DMA 但共享 GDMA 通道，同时只能 1 个 UART 使用 DMA
- **ESP32-S3**: 通道较多但仍有限
- **Linux**: 可能完全没有 DMA
- **跨平台**: 协议必须统一，但硬件能力差异巨大

### 1.3 设计原则

1. **协议层定义意愿，节点层决定现实** — 协议表达用户期望，节点根据硬件约束做最终决策
2. **DMA 通道是一等公民** — 作为独立资源上报和管理，与总线是多对多映射
3. **节点默认开启 DMA** — DMA 优先分配，不可用时自动降级 polled
4. **能力上报先行** — 节点上报 DMA 能力，前端据此展示和控制

---

## 2. 协议设计

### 2.1 ResourceReport 扩展 — 新增 Field 8: dma_channels

在现有 `MSG_RESOURCE_REPORT (0x19)` 中新增 DMA 通道描述：

```
DmaChannel (嵌套消息, field 8, 可重复):
  field 1 (varint): dma_id           — DMA 通道 ID (平台唯一)
  field 2 (bytes):  name             — 名称 "GDMA_CH0"
  field 3 (varint): dma_type         — 0=GDMA, 1=EDMA, 2=DMA2D
  field 4 (varint): capabilities     — bit mask: bit0=TX, bit1=RX, bit2=burst
  field 5 (varint): max_burst        — 最大突发传输长度 (bytes)
  field 6 (varint): state            — 0=free, 1=allocated, 2=disabled
  field 7 (bytes):  bound_to         — 当前绑定的硬件资源 "UART1" / "" (空=未绑定)
  field 8 (varint): compatible_bus   — bit mask: bit0=UART, bit1=I2C, bit2=SPI
```

#### 编码示例 (ESP32-C6)

```
ResourceReport:
  field 8 (DmaChannel):
    F1=0  (dma_id=0)
    F2="GDMA_CH0"
    F3=0  (GDMA)
    F4=3  (TX|RX)
    F5=4095
    F6=0  (free)
    F7=""
    F8=5  (UART|SPI)
  
  field 8 (DmaChannel):
    F1=1  (dma_id=1)
    F2="GDMA_CH1"
    F3=0  (GDMA)
    F4=1  (TX only)
    F5=4095
    F6=1  (allocated)
    F7="SPI2"
    F8=4  (SPI only)
  
  field 8 (DmaChannel):
    F1=2  (dma_id=2)
    F2="GDMA_CH2"
    F3=0  (GDMA)
    F4=3  (TX|RX)
    F5=4095
    F6=0  (free)
    F7=""
    F8=5  (UART|SPI)
```

### 2.2 ConfigManifest 扩展 — 新增 Field 5: dma_channel_configs

在现有 `MSG_CONFIG_MFST (0x04)` 中新增 DMA 通道配置：

```
DmaChannelConfig (嵌套消息, field 5, 可重复):
  field 1 (varint): dma_id    — 要配置的 DMA 通道 ID
  field 2 (varint): enabled   — 1=启用, 0=禁用
  field 3 (bytes):  bind_to   — 绑定到哪个硬件资源 "UART1" / "SPI2" / "" (自动)
```

#### 编码示例: 用户禁用 GDMA_CH0，绑定 CH2 到 UART1

```
ConfigManifest:
  field 5 (DmaChannelConfig):
    F1=0  (dma_id=0)
    F2=0  (disabled)
    F3=""
  
  field 5 (DmaChannelConfig):
    F1=2  (dma_id=2)
    F2=1  (enabled)
    F3="UART1"
```

### 2.3 bus_config.flags 语义简化

flags 回归简单语义，只表达**当前实际运行状态**（只读回传）：

```c
// bus_config.flags (1 byte)
#define FLAG_DMA_ACTIVE   0x01   // bit 0: 当前使用 DMA (只读回传)
// bit 1-7: 保留

// 节点默认行为: DMA 优先 (自动分配)
// 用户通过 DmaChannelConfig 控制哪些 DMA 通道可用
// 不需要在 bus_config 中表达 dma_mode
```

---

## 3. 各平台上报示例

### 3.1 ESP32-C6

```json
{
  "dma_channels": [
    {
      "dma_id": 0,
      "name": "GDMA_CH0",
      "dma_type": "GDMA",
      "capabilities": "TX|RX",
      "max_burst": 4095,
      "state": "free",
      "bound_to": "",
      "compatible_bus": "UART|SPI"
    },
    {
      "dma_id": 1,
      "name": "GDMA_CH1",
      "dma_type": "GDMA",
      "capabilities": "TX",
      "max_burst": 4095,
      "state": "allocated",
      "bound_to": "SPI2",
      "compatible_bus": "SPI"
    },
    {
      "dma_id": 2,
      "name": "GDMA_CH2",
      "dma_type": "GDMA",
      "capabilities": "TX|RX",
      "max_burst": 4095,
      "state": "free",
      "bound_to": "",
      "compatible_bus": "UART|SPI"
    }
  ]
}
```

### 3.2 ESP32-S3

```json
{
  "dma_channels": [
    {"dma_id": 0, "name": "GDMA_CH0", "compatible_bus": "UART|SPI|I2S", "state": "free"},
    {"dma_id": 1, "name": "GDMA_CH1", "compatible_bus": "UART|SPI|I2S", "state": "free"},
    {"dma_id": 2, "name": "GDMA_CH2", "compatible_bus": "UART|SPI|I2S", "state": "free"},
    {"dma_id": 3, "name": "GDMA_CH3", "compatible_bus": "UART|SPI|I2S", "state": "free"},
    {"dma_id": 4, "name": "GDMA_CH4", "compatible_bus": "UART|SPI|I2S", "state": "free"},
    {"dma_id": 5, "name": "GDMA_CH5", "compatible_bus": "SPI", "state": "free"}
  ]
}
```

### 3.3 Linux (RK3576)

```json
{
  "dma_channels": [
    {"dma_id": 0, "name": "DMA2D_CH0", "compatible_bus": "UART|SPI|I2C",
     "state": "allocated", "bound_to": "UART2"},
    {"dma_id": 1, "name": "DMA2D_CH1", "compatible_bus": "UART|SPI|I2C",
     "state": "free", "bound_to": ""}
  ]
}
```

### 3.4 Linux (x86)

```json
{
  "dma_channels": []
}
```

---

## 4. 节点端实现

### 4.1 DMA 资源管理器

```c
// dma_pool.h

typedef struct {
    uint32_t dma_id;
    char     name[16];          // "GDMA_CH0"
    uint8_t  dma_type;          // 0=GDMA
    uint8_t  capabilities;      // bit0=TX, bit1=RX
    uint32_t max_burst;
    uint8_t  state;             // 0=free, 1=allocated, 2=disabled
    char     bound_to[16];      // "UART1" or ""
    uint8_t  compatible_bus;    // bit0=UART, bit1=I2C, bit2=SPI
} dma_channel_info_t;

typedef struct {
    dma_channel_info_t channels[8];  // 最多 8 个 DMA 通道
    uint8_t count;
    SemaphoreHandle_t mutex;
} dma_pool_t;
```

### 4.2 核心 API

```c
// 初始化 (根据芯片型号填充通道列表)
void dma_pool_init(dma_pool_t *pool);

// 自动分配: 根据 bus_type 找一个兼容且空闲的通道
esp_err_t dma_pool_allocate(dma_pool_t *pool, uint8_t bus_type,
                             const char *hw_id, uint32_t *out_dma_id);

// 释放通道
void dma_pool_release(dma_pool_t *pool, uint32_t dma_id);

// 用户配置: 启用/禁用通道
esp_err_t dma_pool_set_enabled(dma_pool_t *pool, uint32_t dma_id, bool enabled);

// 用户配置: 绑定通道到指定硬件
esp_err_t dma_pool_bind(dma_pool_t *pool, uint32_t dma_id, const char *hw_id);

// 序列化: 生成 ResourceReport 的 dma_channels 字段
size_t dma_pool_serialize(dma_pool_t *pool, uint8_t *buf, size_t buf_size);
```

### 4.3 平台通道描述

```c
// hw_profile.c

#if CONFIG_IDF_TARGET_ESP32C6
static const dma_channel_info_t c6_dma_channels[] = {
    {0, "GDMA_CH0", 0, 0x03, 4095, 0, "", 0x05},  // TX|RX, UART|SPI
    {1, "GDMA_CH1", 0, 0x01, 4095, 0, "", 0x04},  // TX only, SPI only
    {2, "GDMA_CH2", 0, 0x03, 4095, 0, "", 0x05},  // TX|RX, UART|SPI
};

#elif CONFIG_IDF_TARGET_ESP32S3
static const dma_channel_info_t s3_dma_channels[] = {
    {0, "GDMA_CH0", 0, 0x03, 4095, 0, "", 0x07},  // UART|SPI|I2S
    {1, "GDMA_CH1", 0, 0x03, 4095, 0, "", 0x07},
    {2, "GDMA_CH2", 0, 0x03, 4095, 0, "", 0x07},
    {3, "GDMA_CH3", 0, 0x03, 4095, 0, "", 0x07},
    {4, "GDMA_CH4", 0, 0x03, 4095, 0, "", 0x07},
    {5, "GDMA_CH5", 0, 0x01, 4095, 0, "", 0x04},  // SPI only
};
#endif
```

### 4.4 自动分配算法

```c
esp_err_t dma_pool_allocate(dma_pool_t *pool, uint8_t bus_type,
                             const char *hw_id, uint32_t *out_dma_id)
{
    xSemaphoreTake(pool->mutex, portMAX_DELAY);

    // 1. 检查是否已经有绑定的通道
    for (int i = 0; i < pool->count; i++) {
        if (pool->channels[i].state == 1 &&  // allocated
            strcmp(pool->channels[i].bound_to, hw_id) == 0) {
            *out_dma_id = pool->channels[i].dma_id;
            xSemaphoreGive(pool->mutex);
            return ESP_OK;  // 已分配
        }
    }

    // 2. 找兼容且空闲的通道
    for (int i = 0; i < pool->count; i++) {
        if (pool->channels[i].state == 0 &&  // free
            (pool->channels[i].compatible_bus & (1 << bus_type))) {
            pool->channels[i].state = 1;  // allocated
            strncpy(pool->channels[i].bound_to, hw_id,
                    sizeof(pool->channels[i].bound_to) - 1);
            *out_dma_id = pool->channels[i].dma_id;
            xSemaphoreGive(pool->mutex);
            return ESP_OK;
        }
    }

    xSemaphoreGive(pool->mutex);
    return ESP_ERR_NOT_FOUND;  // 无可用通道
}
```

### 4.5 默认行为: DMA 自动分配

```c
// bus_manager.c — 当 channel 需要 DMA 时:

esp_err_t result = dma_pool_allocate(&s_dma_pool, bus_type, hw_id, &dma_id);

if (result == ESP_OK) {
    // DMA 分配成功, 使用 DMA 模式
    bus_dma_init(ctx, bus_type, true, config, config_len);
    ESP_LOGI(TAG, "ch=%s bound to DMA %s", hw_id,
             s_dma_pool.channels[dma_id].name);
} else {
    // DMA 不可用 (被禁用/已满/不支持), 自动降级 polled
    bus_dma_init(ctx, bus_type, false, config, config_len);
    ESP_LOGW(TAG, "ch=%s DMA unavailable, using polled mode", hw_id);
}
```

### 4.6 启动流程

```
app_main()
    │
    ├── dma_pool_init()           // 根据芯片型号初始化 DMA 通道列表
    │   ├── ESP32-C6: 3 channels
    │   ├── ESP32-S3: 6 channels
    │   └── Linux:    DTS 解析
    │
    ├── config_mgr_load_from_nvs()
    │   └── 如果有 DmaChannelConfig → 应用用户配置 (启用/禁用/绑定)
    │
    ├── bus_manager_setup_from_manifest()
    │   └── 每个 channel: dma_pool_allocate() → bus_dma_init()
    │
    └── msg_handler_send_resource_report()
        └── 包含 dma_channels 当前状态
```

---

## 5. 前端交互设计

### 5.1 DMA 资源面板

```
┌─ DMA 资源 ── ESP32-C6 (3 通道) ──────────────────────┐
│                                                       │
│  GDMA_CH0  🟢 启用  未绑定       兼容: UART, SPI     │
│              [绑定到 ▼]  [禁用]                       │
│                                                       │
│  GDMA_CH1  🟢 启用  → SPI2      兼容: SPI           │
│              (SPI 专用通道)                            │
│                                                       │
│  GDMA_CH2  🔴 禁用               兼容: UART, SPI     │
│              [启用]                                    │
│                                                       │
│  ⚠ UART0 和 UART1 共享 GDMA_CH0/CH2，启用第二个      │
│    将需要抢占已绑定的通道                              │
└───────────────────────────────────────────────────────┘
```

### 5.2 通道配置中的 DMA 状态 (只读回显)

```
┌─ UART1 ──────────────────────────────────────────┐
│  TX: GPIO20  RX: GPIO21  波特率: 9600            │
│                                                   │
│  DMA: 🟢 GDMA_CH0 (自动分配)                      │
│        [?] 此通道正在使用 DMA 加速                 │
└───────────────────────────────────────────────────┘
```

### 5.3 互斥冲突解决

```
用户操作: 启用 GDMA_CH2 → 绑定到 UART2
    │
    ▼
前端检查: GDMA_CH2 是否被其他通道占用?
    │
    ├── 否 → 直接发送 DmaChannelConfig
    └── 是 → 弹窗:
         "GDMA_CH2 当前被 [UART1] 使用。
          切换将导致 UART1 降级为 polled 模式。继续？"
         │
         ├── [取消] → 恢复原状态
         └── [确认切换] → 发送:
              DmaChannelConfig: {dma_id: 2, enabled: 1, bind_to: "UART2"}
              (UART1 下次重新分配时自动降级)
```

### 5.4 前端数据驱动

前端不需要硬编码平台 DMA 能力，完全从 ResourceReport 数据驱动：

```javascript
// 从 ResourceReport 解析 DMA 能力
const dmaChannels = resourceReport.dma_channels || [];

// 渲染 DMA 面板
dmaChannels.map(ch => ({
    name: ch.name,
    enabled: ch.state !== 2,
    bound: ch.bound_to || '未绑定',
    compatible: decodeBusMask(ch.compatible_bus),
}));

// 互斥检测 (前端计算)
function checkConflict(targetDmaId, targetHwId) {
    const ch = dmaChannels.find(c => c.dma_id === targetDmaId);
    if (ch.bound_to && ch.bound_to !== targetHwId) {
        return { conflict: true, currentOwner: ch.bound_to };
    }
    return { conflict: false };
}
```

---

## 6. 数据流全景

```
                    ┌──────────────┐
                    │   前端 UI     │
                    │  DMA 面板     │
                    └──────┬───────┘
                           │ ① 查询 DMA 资源
                           ▼
                    ┌──────────────┐
                    │   后端 API    │
                    │  REST/GraphQL │
                    └──────┬───────┘
                           │ ② ConfigManifest (含 dma_channel_configs)
                           ▼
                    ┌──────────────┐
                    │  ESP32 节点   │
                    │  config_mgr   │
                    └──────┬───────┘
                           │ ③ dma_pool 应用配置 (启用/禁用/绑定)
                           ▼
                    ┌──────────────┐
                    │  dma_pool     │
                    │  分配/释放    │
                    └──────┬───────┘
                           │ ④ bus_dma_init 根据分配结果
                           ▼
                    ┌──────────────┐
                    │  bus_dma      │
                    │  DMA / polled │
                    └──────┬───────┘
                           │ ⑤ ResourceReport (含 dma_channels 实际状态)
                           ▼
                    ┌──────────────┐
                    │   前端 UI     │
                    │  更新面板     │
                    └──────────────┘
```

---

## 7. 向后兼容性

| 组件 | 改动 | 兼容性 |
|------|------|--------|
| ResourceReport 新增 field 8 | 新增字段 | ✅ 旧前端忽略未知字段 |
| ConfigManifest 新增 field 5 | 新增字段 | ✅ 旧节点忽略未知字段 |
| bus_config.flags 语义简化 | bit0=DMA_ACTIVE (回传) | ✅ 只影响回传，不影响下发 |
| bus_dma_init 参数 | `dma_enabled` 由 dma_pool 决定 | ✅ 内部逻辑不变 |

---

## 8. 改动范围评估

| 层级 | 组件 | 改动内容 | 工作量 |
|------|------|---------|--------|
| **协议** | `frame_codec.h` | 新增 DMA Channel 字段定义 | 15 min |
| **节点** | `dma_pool.c/h` | 新建 DMA 资源管理器 | 2h |
| **节点** | `bus_manager.c` | 集成 dma_pool_allocate | 1h |
| **节点** | `hw_profile.c` | 各平台 DMA 通道描述 | 1h |
| **节点** | `msg_handler.c` | ResourceReport 增加 dma_channels | 30 min |
| **节点** | `config_mgr.c` | 解析 DmaChannelConfig | 30 min |
| **后端** | DB model | dma_channel 表 | 30 min |
| **后端** | API | DMA 资源查询 + 配置下发 | 1h |
| **前端** | DMA 面板 | 资源列表 + 开关 + 绑定 | 2h |
| **前端** | 互斥 UI | 冲突检测 + 解决对话框 | 1h |

**总工作量**: ~2 天

---

## 9. 评审要点

1. **DMA 通道作为独立资源上报** — 前端完全数据驱动，无需硬编码平台能力
2. **compatible_bus bitmask** — 是否足够描述 DMA 通道与硬件的映射？
3. **默认 DMA 优先** — 节点默认开启 DMA，自动分配，不可用时降级
4. **前端互斥计算** — 冲突检测在前端 vs 节点端，哪种更合适？
5. **flags 简化** — bus_config.flags 只保留 DMA_ACTIVE 回传，控制通过 DmaChannelConfig
