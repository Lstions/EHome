# DMA 资源管理协议设计方案

**版本**: v2.0
**日期**: 2026-06-20
**状态**: 已完整实现。协议字段 (ResourceReport field 8, ConfigManifest field 5) 已完成编解码，dma_pool 组件已落地 (三级分配策略)，前端 DMA 面板已对接 (通道列表页 inline toggle + 详情页面板)，后端 API 已实现 (GET /nodes/:id/dma-channels, PUT /nodes/:id/dma-config)。hw_id 全链路格式已统一为 `bus_type/hw_id` (如 `uart/UART1`)。SPI derive_hw_id 按 MOSI/MISO/SCLK 总线级匹配。
**最近更新**: 2026-06-20 — hw_id 统一协议、C6 GDMA TRM 验证、代码审查闭环。关联 commit: 09f135c, ...

---

## 1. 背景与问题

### 1.1 硬件事实

| 平台 | DMA 通道数 | UART DMA 约束 | SPI DMA 约束 | I2C DMA |
|------|-----------|--------------|-------------|---------|
| ESP32-C6 | 3 对 (6 独立通道: 3 TX + 3 RX) | UART0/1 通过单一 UHCI 接口共享 GDMA | SPI2 独立 | 不支持 |
| ESP32-S3 | 5 对 (GDMA CH0-5) | CH0-4 通用 | CH5 SPI2 专用 | 不支持 |
| Linux RK3576 | N (DTS 绑定) | DTS 配置 | DTS 配置 | DTS 配置 |
| Linux x86 | 0 | 不支持 | 不支持 | 不支持 |

参见 **ESP32-C6 TRM v1.2, Chapter 4, pp.122-123** 获取 GDMA 控制器详细架构。

### 1.2 C6 GDMA 架构 (TRM v1.2 第4章)

```
GDMA 控制器共有 6 个独立的通道:
  TX Channel 0, TX Channel 1, TX Channel 2   (3 个独立发送通道)
  RX Channel 0, RX Channel 1, RX Channel 2   (3 个独立接收通道)

支持 GDMA 的外设: SPI2, UHCI(UART0/UART1), I2S, AES, SHA, ADC, PARLIO
每个通道通过 "外设选择器" (Peri Select) 独立连接到不同外设。
UART0 与 UART1 共用一个 UHCI 接口 → UHCI 在 GDMA 外设选择矩阵中只占一个槽位。
```

### 1.3 核心矛盾

- **ESP32-C6**: UART0/1 支持 DMA 但共享 UHCI 接口，同时只能 1 个 UART 使用 DMA
- **ESP32-S3**: 通道较多但仍有限
- **Linux**: 可能完全没有 DMA
- **跨平台**: 协议必须统一，但硬件能力差异巨大

### 1.4 设计原则

1. **协议层定义意愿，节点层决定现实** — 协议表达用户期望，节点根据硬件约束做最终决策
2. **DMA 通道是一等公民** — 作为独立资源上报和管理，与总线是多对多映射
3. **节点默认开启 DMA** — DMA 优先分配，不可用时自动降级 polled
4. **能力上报先行** — 节点上报 DMA 能力，前端据此展示和控制
5. **hw_id 全链路统一** — bind_to / hw_id / derive_hw_id 使用同一格式 `bus_type/hw_id`

---

## 2. 协议设计

### 2.1 ResourceReport 扩展 — 新增 Field 8: dma_channels

```
DmaChannel (嵌套消息, field 8, 可重复):
  field 1 (varint): dma_id           — DMA 通道 ID (平台唯一)
  field 2 (bytes):  name             — 名称 "GDMA_CH0"
  field 3 (varint): dma_type         — 0=GDMA, 1=EDMA, 2=DMA2D
  field 4 (varint): capabilities     — bit mask: bit0=TX, bit1=RX, bit2=burst
  field 5 (varint): max_burst        — 最大突发传输长度 (bytes)
  field 6 (varint): state            — 0=free, 1=allocated, 2=disabled
  field 7 (bytes):  bound_to         — 绑定的硬件资源 "uart/UART1" / ""
  field 8 (varint): compatible_bus   — bit mask: bit0=UART, bit1=I2C, bit2=SPI
```

#### ESP32-C6 上报示例 (当前实现)

```json
{
  "dma_channels": [
    {
      "dma_id": 0,
      "name": "GDMA_CH0",
      "capabilities": "TX|RX",
      "max_burst": 4095,
      "state": "free",
      "bound_to": "",
      "compatible_bus": "SPI"
    },
    {
      "dma_id": 1,
      "name": "GDMA_CH1",
      "capabilities": "TX|RX",
      "max_burst": 4095,
      "state": "allocated",
      "bound_to": "uart/UART1",
      "compatible_bus": "UART|SPI"
    },
    {
      "dma_id": 2,
      "name": "GDMA_CH2",
      "capabilities": "TX|RX",
      "max_burst": 4095,
      "state": "free",
      "bound_to": "",
      "compatible_bus": "SPI"
    }
  ]
}
```

**关键设计**: CH0/CH2 compatible_bus = SPI only (0x04)。只有 CH1 标记为 UART|SPI (0x05)。dma_pool 自然保证两个 UART 不能同时使用 DMA。

### 2.2 ConfigManifest 扩展 — 新增 Field 5: dma_channel_configs

```
DmaChannelConfig (嵌套消息, field 5, 可重复):
  field 1 (varint): dma_id    — 要配置的 DMA 通道 ID
  field 2 (varint): enabled   — 1=启用, 0=禁用
  field 3 (bytes):  bind_to   — 绑定到 "uart/UART1" / "spi/SPI2" / "" (自动)
```

### 2.3 hw_id 格式规范 (v2.0)

全链路统一使用 `bus_type/hw_id` 格式:

| 环节 | 格式 | 示例 |
|------|------|------|
| derive_hw_id (固件) | `bus/hw_id` | `uart/UART1`, `spi/SPI2` |
| dma_pool_apply_config bind_to | `bus/hw_id` | 同上 |
| dma_pool_allocate hw_id | `bus/hw_id` | 同上 |
| dma_pool step 1 精确匹配 | `strcmp(bound_to, hw_id)` | 精确比较 |
| dma_pool step 1b 模糊匹配 | 按 bus 前缀 | C6 UHCI 共享场景 |
| 前端 toggleDmaForHardware | `bus/hw_id` | `uart/UART1` |
| 前端 isBoundToHardware | case-insensitive | `boundTo.toLowerCase() === ...` |

### 2.4 derive_hw_id 算法 (bus_manager.c)

通过 `bus_config` 引脚查 `hw_tables` 获取规范硬件 ID:

```
UART: 匹配 TX/RX 引脚 → hw_uarts[id] → "uart/UART0"
SPI:   匹配 MOSI/MISO/SCLK 引脚 (总线级，非 CS 设备级) → hw_spis[id] → "spi/SPI2"
I2C:   匹配 SDA/SCL 引脚 → hw_i2cs[id] → "i2c/I2C0"

Fallback: 查表失败时生成唯一标识 "bus/UNKNOWN_TX_RX"
  → 确保 dma_pool_release_by_hw 不会误释放其他通道
```

---

## 3. 节点端实现

### 3.1 dma_pool 三级分配算法

```c
esp_err_t dma_pool_allocate(pool, bus_type, hw_id, &out_dma_id)
{
    // Step 1: 精确匹配 — bound_to == hw_id?
    //   同一硬件重复请求 DMA (如 UART1 重启后重新注册)

    // Step 1b: 模糊匹配 — 同一 bus 类型已分配通道?
    //   C6 UHCI 共享场景: UART0 拿到 CH1 后, UART1 也请求 DMA
    //   → 复用 CH1 (覆盖 bound_to)
    //   参见 ESP32-C6 TRM v1.2 Ch.4: UHCI 只有一个 peri-select 槽

    // Step 2: 空闲分配 — 找到 FREE + compatible + TX|RX 的通道

    // Step 3: Fallback — 任意 FREE + compatible 通道
}
```

### 3.2 C6 GDMA 通道描述 (hw_tables.c)

```c
/* ESP32-C6 TRM v1.2, Chapter 4, pp.122-123
 * GDMA: 6 independent channels (3 TX + 3 RX)
 * Peripherals: SPI2, UHCI(UART0/UART1), I2S, AES, SHA, ADC, PARLIO
 * UHCI occupies ONE peri-select slot
 * We model as 3 pairs; only CH1 is UART-compatible */
const hw_dma_t hw_dmas[HW_DMA_COUNT] = {
    { .dma_id = 0, .compatible_bus = 0x04 },  // SPI only
    { .dma_id = 1, .compatible_bus = 0x05 },  // UART|SPI
    { .dma_id = 2, .compatible_bus = 0x04 },  // SPI only
};
```

### 3.3 ESP32-S3 (对比)

```c
const hw_dma_t hw_dmas[HW_DMA_COUNT] = {
    { .dma_id = 0, .compatible_bus = 0x05 },  // UART|SPI
    { .dma_id = 1, .compatible_bus = 0x05 },
    { .dma_id = 2, .compatible_bus = 0x05 },
    { .dma_id = 3, .compatible_bus = 0x05 },
    { .dma_id = 4, .compatible_bus = 0x05 },
};
```

S3 没有 UHCI 共享限制，所有通道都支持 UART。

---

## 4. 前端交互设计

### 4.1 DMA Toggle (通道列表页 UART 面板内联)

每个硬件资源旁显示兼容的 DMA 通道 toggle:

```
UART0  [GDMA_CH1: OFF/disabled]  ← 已分配给 UART1，不可操作
UART1  [GDMA_CH1: ON/enabled]    ← 当前绑定到 UART1，可关闭
```

语义:
- **model-value**: `isDmaBoundTo(dma, busType, hw)` — 仅当 DMA 绑定到当前硬件时显示 ON
- **disabled**: `!canToggleDma(dma, busType, hw)` — DMA 已分配给其他硬件时 disabled

### 4.2 SPI CS 处理

SPI 作为主机有多个 CS 引脚，取决于 hw_profile 配置。前端从 `availableHardwareList` 获取 `cs_pins` 列表。bus_config 编码格式:

```
[CS(1B)][MODE(1B)][FREQ(4B BE)][MOSI(1B)][MISO(1B)][SCLK(1B)]  = 9 bytes
```

固件 `spi_init` 在 `len >= 9` 时解析 MOSI/MISO/SCLK；`derive_hw_id` 按这三个引脚 (总线级) 匹配 hw_spis 表，不按 CS (设备级) 匹配。

---

## 5. 数据流

```
前端 DMA Toggle → PUT /dma-config → ConfigManifest (field 5)
    → 固件 dma_pool_apply_config → bound_to = "uart/UART1"
    → bus_manager_setup_from_manifest
        → derive_hw_id 查表 → "uart/UART1"
        → dma_pool_allocate("uart/UART1")
            → Step 1 精确匹配 → reuse CH1 ✓
    → ResourceReport (field 8) → 前端更新 DMA 面板
```

---

## 6. 向后兼容性

| 组件 | 改动 | 兼容性 |
|------|------|--------|
| ResourceReport 新增 field 8 | 新增字段 | ✅ 旧前端忽略未知字段 |
| ConfigManifest 新增 field 5 | 新增字段 | ✅ 旧节点忽略未知字段 |
| bound_to 格式从 "UART1" → "uart/UART1" | 格式变更 | ✅ 新格式后端兼容旧数据 |
| dma_channel_infos_t.compatible_bus | CH0/CH2 0x05→0x04 | ✅ S3 不受影响 |

---

## 7. 改动范围评估

| 层级 | 组件 | 改动内容 | 状态 |
|------|------|---------|------|
| **协议** | `frame_codec` | DMA Channel 字段编解码 | ✅ 已实现 |
| **节点** | `dma_pool.c` | 三级分配 + step 1b + apply_config | ✅ 已实现 |
| **节点** | `bus_manager.c` | derive_hw_id 重写 + dma_pool 集成 | ✅ 已实现 |
| **节点** | `hw_tables.c` | C6 DMA 通道描述修正 | ✅ 已实现 |
| **节点** | `config_mgr.c` | DmaChannelConfig 解析 | ✅ 已实现 |
| **后端** | `handler_node.go` | DMA config API + manifest 编码 | ✅ 已实现 |
| **后端** | `sender.go` | ConfigManifest 推送 | ✅ 已实现 |
| **前端** | `ChannelPanel.vue` | DMA toggle (isDmaBoundTo + canToggleDma) | ✅ 已实现 |
| **前端** | `ChannelManager.vue` | bus_config 编码 (9B SPI + I2C radix) | ✅ 已实现 |
| **前端** | `NodeDetail.vue` | @click.stop + DMA 面板 | ✅ 已实现 |

---

## 8. 参考资料

- ESP32-C6 Technical Reference Manual v1.2, Chapter 4: GDMA Controller, pp.122-123
- 项目源码: `esp32-collector/components/dma_pool/`, `esp32-collector/main/bus_manager.c`
- 前端 DMA toggle: `frontend-shared/src/components/node/ChannelPanel.vue`
