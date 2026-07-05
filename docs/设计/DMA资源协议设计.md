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


---

# 统一总线 DMA 架构设计（合并）

# 统一总线 DMA 架构设计

> **版本**: v2.4
> **状态**: 设计中
> **关联**: [DMA资源协议设计.md §统一总线DMA](#统一总线-dma-架构设计合并) | [通道/详细设计.md](通道/详细设计.md)

## 1. 概述

将 ESP32 的 UART/SPI/I2C 三种总线通信统一为异步队列驱动模型，支持 **动态 DMA 开关** 配置。每种总线提供 DMA（高性能）和非 DMA（兼容/低功耗）两条路径，由 bus_config flags 字节在运行时选择。

### 1.1 解决的问题

| 问题 | 根因 | 本设计解决方式 |
|------|------|---------------|
| MQTT 回调阻塞 500ms | UART 事务在 MQTT 回调线程同步执行 | 命令队列 + Worker Task，MQTT 回调 <1μs |
| SPI 未实现 | bus_transact 返回 OK 但 rx_len=0 | bus_dma 完整实现 SPI DMA + polled 双路径 |
| I2C 并发竞态 | scheduler 和 WriteCommand 同时操作同一 I2C 总线 | 统一 Worker Task 串行处理 + mutex |
| UART 端口硬编码 | 全部 UART_NUM_1，多通道冲突 | hw_profile 声明可用端口，bus_config 指定 |
| uart_flush_input 丢数据 | 每次事务前清空 RX buffer | DMA 路径不 flush，循环读取 |
| uart_cmd_t 重复定义 | main.c 和 scheduler.c 各自定义 | 抽取到 cmd_queue.h 共享 |
| scheduler 反向依赖 | extern g_cmd_queue + scheduler_register_uart | cmd_queue.h 声明，回调注入 |
| DMA 无开关 | 固定使用 ESP-IDF 内置 DMA | bus_config flags bit0 控制 DMA/非 DMA |

### 1.2 ESP32-C6 各总线 DMA 能力

| 总线 | DMA 机制 | ESP-IDF v6 API | 非 DMA 方案 |
|------|----------|----------------|-------------|
| **UART** | DMA ring buffer 自动搬运 | `uart_driver_install(port, 1024, 256, ...)` | 小 buffer + 轮询 |
| **SPI** | SPI DMA 控制器 | `spi_bus_initialize(host, &cfg, SPI_DMA_CH_AUTO)` | `SPI_DMA_DISABLED` 轮询模式 |
| **I2C** | v6 新 API 内部队列化 | `i2c_new_master_bus()` + `i2c_master_transmit_receive()` | 旧 API `i2c_driver_install()` 阻塞 |

> 注意: I2C 在 ESP32-C6 上没有真正的 DMA 通道，但 v6 新 `i2c_master` API 是非阻塞队列化的，比旧 API 高效。"DMA enabled" 对 I2C 的含义是：使用新 API vs 旧 API。

## 2. 架构

```
┌─────────────────────────────────────────────────────────────────┐
│  MQTT Event Task (永不阻塞)                                       │
│  on_mqtt_msg() → msg_handler_process()                           │
│  WriteCommand → xQueueSend(g_cmd_queue, non-blocking) <1μs      │
│  ConfigManifest → config_mgr_apply()                              │
└───────────────────────────┬─────────────────────────────────────┘
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│  Command Queue (xQueue, 深度 16, bus_cmd_t)                       │
│  CMD_WRITE (高优) / CMD_SAMPLE (低优)                             │
│  统一处理 UART/SPI/I2C 所有总线类型                                 │
└───────────────────────────┬─────────────────────────────────────┘
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│  Bus Worker Task (priority=8, 最高优先级)                         │
│                                                                  │
│  xQueueReceive → 按 bus_type 分发:                                │
│    UART → uart_transact (DMA / polled)                            │
│    SPI  → spi_transact (DMA / polled)                             │
│    I2C  → i2c_transact (new API / old API)                        │
│                                                                  │
│  → msg_handler_send_write_rsp / send_data_report                 │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│  Scheduler Task (priority=5, 纯定时器)                            │
│  vTaskDelayUntil(10ms) 精确周期                                   │
│  所有总线类型 → xQueueSend(CMD_SAMPLE)                            │
│  不再直接操作任何硬件                                               │
└─────────────────────────────────────────────────────────────────┘
```

## 3. 组件设计

### 3.1 命令队列 (cmd_queue.h)

```c
/* 共享命令队列类型 — 消除 main.c / scheduler.c 重复定义 */
#define CMD_QUEUE_DEPTH  16
#define CMD_TX_MAX       128

typedef enum {
    CMD_WRITE  = 0,   /* WriteCommand: 服务端下发写入 */
    CMD_SAMPLE = 1,   /* 周期性采样 */
} cmd_type_t;

typedef struct {
    uint32_t   request_id;      /* WriteResponse 原样返回 */
    uint32_t   channel_id;      /* 目标通道 */
    uint8_t    bus_type;        /* 1=UART, 2=I2C, 3=SPI */
    uint8_t    tx_data[CMD_TX_MAX];
    size_t     tx_len;
    uint32_t   read_size;       /* 期望读取字节数 */
    uint32_t   timeout_ms;      /* 读取超时 */
    cmd_type_t type;
} bus_cmd_t;

/* 全局命令队列 — 在 main.c 中创建 */
extern QueueHandle_t g_cmd_queue;
```

### 3.2 统一 DMA 引擎 (bus_dma)

#### 3.2.1 接口 (bus_dma.h)

```c
typedef struct {
    uint8_t  bus_type;        /* 1=UART, 2=I2C, 3=SPI */
    bool     dma_enabled;     /* 运行时 DMA 开关 */
    bool     initialized;
    SemaphoreHandle_t mutex;  /* 保护并发访问 */
    union {
        struct {
            uart_port_t port;
            uint32_t baud;
            int tx_pin, rx_pin;
        } uart;
        struct {
            spi_host_device_t host;
            spi_device_handle_t dev;
            int cs_pin;
            uint32_t freq;
            uint8_t mode;
        } spi;
        struct {
            i2c_port_t port;
            uint8_t addr;
            uint32_t freq;
            int sda_pin, scl_pin;
            /* DMA (新 API) 时使用 */
            void *bus_handle;   /* i2c_master_bus_handle_t */
            void *dev_handle;   /* i2c_master_dev_handle_t */
        } i2c;
    } cfg;
} bus_dma_ctx_t;

esp_err_t bus_dma_init(bus_dma_ctx_t *ctx, uint8_t bus_type, bool dma_enabled,
                       const uint8_t *config, size_t config_len);
esp_err_t bus_dma_transact(bus_dma_ctx_t *ctx,
                           const uint8_t *tx, size_t tx_len, uint32_t timeout_ms,
                           uint8_t *rx, size_t rx_size, size_t *rx_len);
void bus_dma_deinit(bus_dma_ctx_t *ctx);
```

#### 3.2.2 DMA 开关实现

```c
esp_err_t bus_dma_init(bus_dma_ctx_t *ctx, uint8_t bus_type, bool dma_enabled, ...) {
    ctx->bus_type = bus_type;
    ctx->dma_enabled = dma_enabled;
    ctx->mutex = xSemaphoreCreateMutex();

    switch (bus_type) {
    case BUS_TYPE_UART:
        /* DMA: ring buffer 1024B + rx_timeout 4字符 */
        /* 非DMA: 小 buffer 256B, 无 rx_timeout */
        uart_driver_install(port, dma_enabled ? 1024 : 256,
                            dma_enabled ? 256 : 0, 0, NULL, 0);
        if (dma_enabled) uart_set_rx_timeout(port, 4);
        break;

    case BUS_TYPE_SPI:
        /* DMA: SPI_DMA_CH_AUTO 自动通道分配 */
        /* 非DMA: SPI_DMA_DISABLED 轮询模式 */
        spi_bus_initialize(SPI2_HOST, &bus_cfg,
                           dma_enabled ? SPI_DMA_CH_AUTO : SPI_DMA_DISABLED);
        break;

    case BUS_TYPE_I2C:
        if (dma_enabled) {
            /* v6 新 API: 非阻塞, 内部队列化 */
            i2c_new_master_bus(&bus_cfg, &bus_handle);
            i2c_master_bus_add_device(bus_handle, &dev_cfg, &dev_handle);
        } else {
            /* 旧 API: 阻塞式 cmd_begin */
            i2c_driver_install(port, I2C_MODE_MASTER, 0, 0, 0);
        }
        break;
    }
}
```

#### 3.2.3 事务分发

```c
esp_err_t bus_dma_transact(bus_dma_ctx_t *ctx, ...) {
    xSemaphoreTake(ctx->mutex, pdMS_TO_TICKS(500));
    esp_err_t ret;

    switch (ctx->bus_type) {
    case BUS_TYPE_UART:
        ret = ctx->dma_enabled
            ? uart_dma_transact(ctx, ...)    /* 循环读取, 不 flush */
            : uart_polled_transact(ctx, ...); /* 单次读取 */
        break;
    case BUS_TYPE_SPI:
        ret = spi_transact(ctx, ...);  /* DMA/非DMA 由 spi_bus_initialize 决定 */
        break;
    case BUS_TYPE_I2C:
        ret = ctx->dma_enabled
            ? i2c_new_api_transact(ctx, ...)  /* i2c_master_transmit_receive */
            : i2c_old_api_transact(ctx, ...); /* i2c_cmd_link + cmd_begin */
        break;
    }

    xSemaphoreGive(ctx->mutex);
    return ret;
}
```

### 3.3 UART DMA 事务（增强版）

```c
/* DMA 路径: 循环读取, 不 flush, 帧分隔靠 rx_timeout */
static esp_err_t uart_dma_transact(bus_dma_ctx_t *ctx, const uint8_t *tx, size_t tx_len,
                                    uint32_t timeout_ms, uint8_t *rx, size_t rx_sz, size_t *rx_len)
{
    *rx_len = 0;

    /* TX */
    if (tx && tx_len > 0) {
        uart_write_bytes(ctx->cfg.uart.port, tx, tx_len);
        uart_wait_tx_done(ctx->cfg.uart.port, pdMS_TO_TICKS(100));
    }

    /* RX: 首次等首字节 (500ms), 然后循环读取直到 4字符间隔 */
    int total = 0;
    int first = uart_read_bytes(ctx->cfg.uart.port, rx, rx_sz,
                                pdMS_TO_TICKS(500));
    if (first <= 0) return ESP_ERR_TIMEOUT;
    total = first;

    /* 继续读取直到帧间隔 (rx_timeout=4字符) */
    while (total < (int)rx_sz) {
        int more = uart_read_bytes(ctx->cfg.uart.port, rx + total,
                                   rx_sz - total, pdMS_TO_TICKS(50));
        if (more <= 0) break;
        total += more;
    }

    *rx_len = total;
    return ESP_OK;
}

/* 非 DMA 路径: 单次阻塞读取 */
static esp_err_t uart_polled_transact(bus_dma_ctx_t *ctx, ...) {
    *rx_len = 0;
    uart_flush_input(ctx->cfg.uart.port);  /* 非 DMA 允许 flush */
    if (tx && tx_len > 0) {
        uart_write_bytes(ctx->cfg.uart.port, tx, tx_len);
        uart_wait_tx_done(ctx->cfg.uart.port, pdMS_TO_TICKS(100));
    }
    int bytes = uart_read_bytes(ctx->cfg.uart.port, rx, rx_sz,
                                pdMS_TO_TICKS(timeout_ms));
    if (bytes > 0) { *rx_len = bytes; return ESP_OK; }
    return ESP_ERR_TIMEOUT;
}
```

### 3.4 SPI 事务

```c
static esp_err_t spi_transact(bus_dma_ctx_t *ctx, const uint8_t *tx, size_t tx_len,
                               uint8_t *rx, size_t rx_sz, size_t *rx_len)
{
    *rx_len = 0;
    spi_transaction_t txn = {
        .length = tx_len * 8,          /* 单位: bit */
        .tx_buffer = tx,
        .rxlength = rx_sz * 8,
        .rx_buffer = rx,
    };
    esp_err_t err = spi_device_transmit(ctx->cfg.spi.dev, &txn);
    if (err == ESP_OK) {
        *rx_len = txn.rxlength / 8;
    }
    return err;
}
```

### 3.5 I2C 事务

```c
/* DMA (新 API) 路径 */
static esp_err_t i2c_new_api_transact(bus_dma_ctx_t *ctx, const uint8_t *tx, size_t tx_len,
                                       uint8_t *rx, size_t rx_sz, size_t *rx_len)
{
    *rx_len = 0;
    i2c_master_dev_handle_t dev = ctx->cfg.i2c.dev_handle;

    if (tx_len > 0 && rx_sz > 0) {
        /* 写+读 (combined) */
        esp_err_t err = i2c_master_transmit_receive(dev, tx, tx_len, rx, rx_sz, pdMS_TO_TICKS(100));
        if (err == ESP_OK) *rx_len = rx_sz;
        return err;
    } else if (tx_len > 0) {
        return i2c_master_transmit(dev, tx, tx_len, pdMS_TO_TICKS(100));
    } else if (rx_sz > 0) {
        esp_err_t err = i2c_master_receive(dev, rx, rx_sz, pdMS_TO_TICKS(100));
        if (err == ESP_OK) *rx_len = rx_sz;
        return err;
    }
    return ESP_OK;
}

/* 非 DMA (旧 API) 路径 */
static esp_err_t i2c_old_api_transact(bus_dma_ctx_t *ctx, const uint8_t *tx, size_t tx_len,
                                       uint8_t *rx, size_t rx_sz, size_t *rx_len)
{
    *rx_len = 0;
    i2c_port_t port = ctx->cfg.i2c.port;
    uint8_t addr = ctx->cfg.i2c.addr;

    /* Write phase */
    if (tx_len > 0) {
        i2c_cmd_handle_t cmd = i2c_cmd_link_create();
        i2c_master_start(cmd);
        i2c_master_write_byte(cmd, (addr << 1) | I2C_MASTER_WRITE, true);
        i2c_master_write(cmd, tx, tx_len, true);
        i2c_master_stop(cmd);
        esp_err_t err = i2c_master_cmd_begin(port, cmd, pdMS_TO_TICKS(100));
        i2c_cmd_link_delete(cmd);
        if (err != ESP_OK) return err;
    }

    /* Read phase */
    if (rx_sz > 0) {
        i2c_cmd_handle_t cmd = i2c_cmd_link_create();
        i2c_master_start(cmd);
        i2c_master_write_byte(cmd, (addr << 1) | I2C_MASTER_READ, true);
        if (rx_sz > 1) i2c_master_read(cmd, rx, rx_sz - 1, I2C_MASTER_ACK);
        i2c_master_read_byte(cmd, rx + rx_sz - 1, I2C_MASTER_NACK);
        i2c_master_stop(cmd);
        esp_err_t err = i2c_master_cmd_begin(port, cmd, pdMS_TO_TICKS(100));
        i2c_cmd_link_delete(cmd);
        if (err == ESP_OK) *rx_len = rx_sz;
        return err;
    }
    return ESP_OK;
}
```

### 3.6 Scheduler 改造

```c
/* scheduler_task: 所有总线类型走队列 */
static void scheduler_task(void *p) {
    TickType_t wake = xTaskGetTickCount();
    while (s_running) {
        vTaskDelayUntil(&wake, pdMS_TO_TICKS(10));
        TickType_t now = xTaskGetTickCount();

        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            if (!s_channels[i].active || !s_channels[i].config.enabled) continue;
            if (now - s_channels[i].last_sample_time < pdMS_TO_TICKS(s_channels[i].config.interval_ms)) continue;
            s_channels[i].last_sample_time = now;

            /* 所有总线类型统一入队 */
            bus_cmd_t cmd = {
                .channel_id = s_channels[i].config.id,
                .bus_type   = s_channels[i].config.bus_type,
                .tx_len     = 0,
                .read_size  = 0,
                .timeout_ms = 30,
                .type       = CMD_SAMPLE,
            };

            /* 从 template 填充 tx_data */
            if (s_channels[i].config.template_count > 0) {
                const config_template_t *t = config_mgr_get_template(
                    s_channels[i].config.template_ids[0]);
                if (t && t->write_data_len > 0) {
                    cmd.tx_len = t->write_data_len < CMD_TX_MAX
                               ? t->write_data_len : CMD_TX_MAX;
                    memcpy(cmd.tx_data, t->write_data, cmd.tx_len);
                    cmd.read_size = t->read_length;
                }
            }
            xQueueSend(g_cmd_queue, &cmd, 0);  /* 非阻塞 */
        }
    }
    vTaskDelete(NULL);
}
```

### 3.7 Bus Worker Task (main.c)

```c
static void bus_worker_task(void *p) {
    bus_cmd_t cmd;
    uint8_t rx[256];

    while (1) {
        if (!xQueueReceive(g_cmd_queue, &cmd, portMAX_DELAY)) continue;

        bus_dma_ctx_t *ctx = find_ctx(cmd.channel_id);
        if (!ctx) {
            if (cmd.type == CMD_WRITE)
                msg_handler_send_write_rsp(cmd.request_id, false, 4, "no ctx");
            continue;
        }

        size_t rl = 0;
        esp_err_t e = bus_dma_transact(ctx, cmd.tx_data, cmd.tx_len,
                                        cmd.timeout_ms ? cmd.timeout_ms : 50,
                                        rx, sizeof(rx), &rl);

        if (cmd.type == CMD_WRITE) {
            bool ok = (e == ESP_OK);
            msg_handler_send_write_rsp(cmd.request_id, ok, ok ? 0 : (uint32_t)e,
                                       ok ? NULL : "bus err");
            if (rl > 0) {
                uint64_t ts = esp_timer_get_time();
                msg_handler_send_data_report(cmd.channel_id, ts, 0, rx, rl, 0, cmd.request_id);
            }
        }
        if (cmd.type == CMD_SAMPLE && rl > 0) {
            uint64_t ts = esp_timer_get_time();
            msg_handler_send_data_report(cmd.channel_id, ts, 0, rx, rl, 0, 0);
        }
    }
}
```

## 4. bus_config 二进制格式

```
═══════════════════════════════════════════════════════════════
 bus_config 二进制格式 (big-endian)
═══════════════════════════════════════════════════════════════

UART (bus_type=1):
  标准: [tx_pin, rx_pin, baud×4(BE)]           = 6 bytes
  扩展: [tx_pin, rx_pin, baud×4(BE), flags]    = 7 bytes

I2C (bus_type=2):
  标准: [sda, scl, addr, freq×4(BE)]           = 7 bytes
  扩展: [sda, scl, addr, freq×4(BE), flags]    = 8 bytes

SPI (bus_type=3):
  标准: [cs, mode, freq×4(BE)]                 = 6 bytes
  扩展: [cs, mode, freq×4(BE), flags]          = 7 bytes

GPIO (bus_type=4): [pin, direction]             = 2 bytes
ADC  (bus_type=5): [unit, channel, attenuation] = 3 bytes

flags 字节:
  bit 0: dma_enabled (1=DMA/新API, 0=轮询/旧API)
  bits 1-7: reserved

向后兼容: config_len 不含 flags 字节时, 默认 dma_enabled=1
═══════════════════════════════════════════════════════════════
```

解析辅助函数:
```c
/* config_mgr.h */
bool bus_config_get_dma_enabled(const config_channel_t *ch);
int  bus_config_get_uart_port(const config_channel_t *ch);
```

## 5. 文件变更清单

```
新增:
  components/hw_profile/
    include/hw_profile.h          芯片能力常量 + build 函数声明
    hw_profile.c                  ESP32-C6 静态 profile + 二进制编码
    CMakeLists.txt                REQUIRES frame, config_mgr

  components/bus_dma/             ← 重命名自 uart_dma
    include/cmd_queue.h           统一 bus_cmd_t 类型 + g_cmd_queue extern
    include/bus_dma.h             统一 DMA 引擎接口
    bus_dma.c                     UART/SPI/I2C DMA + 非DMA 实现
    CMakeLists.txt                REQUIRES esp_driver_uart/spi/i2c

修改:
  components/frame/frame_codec.h
    + #define MSG_RESOURCE_REPORT  0x19
    + #define MSG_QUERY_RESOURCES  0x1A

  components/msg_handler/
    msg_handler.h   + msg_handler_send_resource_report(void)
    msg_handler.c   + case MSG_QUERY_RESOURCES → 触发上报
                    + HelloAck 处理末尾设置标志

  components/scheduler/
    scheduler.c     重写: #include "cmd_queue.h", 所有总线上队列
    scheduler.h     删除 scheduler_execute_write

  components/config_mgr/
    config_mgr.h    + bus_config_get_dma_enabled()
    config_mgr.c    实现 flags 解析

  main/
    main.c          重写: bus_worker_task + 注册所有总线到 bus_dma
    CMakeLists.txt  uart_dma → bus_dma, + hw_profile

删除:
  components/uart_dma/            (整体移入 bus_dma)
```

## 6. 内存估算

```
bus_cmd_t:          ~148B × 16 depth = 2.4KB (队列)
bus_dma_ctx_t:      ~80B × 8 channels = 640B (上下文)
SPI DMA buffer:     4KB (spi_bus max_transfer_sz)
I2C new API:        ~500B internal
总计额外:           ~7.5KB RAM (ESP32-C6 有 512KB SRAM)
```

## 7. 性能对比

| 指标 | 旧实现 (v2.3) | 新实现 (v2.4) |
|------|---------------|---------------|
| MQTT 回调阻塞 | UART 500ms, I2C 200ms | <1μs (仅 xQueueSend) |
| SPI 支持 | 未实现 | DMA + polled 双路径 |
| I2C 并发安全 | mutex + 错误路径 bug | Worker Task 串行 + mutex |
| UART 帧分隔 | flush_input + 单次读取 | rx_timeout=4字符 + 循环读取 |
| DMA 可配置 | 固定 DMA | flags bit0 运行时开关 |
| 吞吐 (全双工) | ~15Hz (UART only) | ~80Hz (所有总线) |
| 功耗 | 高 (持续轮询) | 低 (事件驱动) |

## 8. 实施步骤

1. **Phase 1**: frame_codec.h 添加消息类型常量
2. **Phase 2**: hw_profile 组件 (常量表 + 二进制编码)
3. **Phase 3**: bus_dma 组件 (UART/SPI/I2C DMA + 非DMA)
4. **Phase 4**: cmd_queue.h (统一命令类型)
5. **Phase 5**: scheduler 重写 (所有总线上队列)
6. **Phase 6**: main.c 重构 (bus_worker_task + 总线注册)
7. **Phase 7**: msg_handler 添加 ResourceReport + QueryResources
8. **Phase 8**: config_mgr bus_config flags 解析
9. **Phase 9**: Go 后端 handler_resources 二进制解码
10. **Phase 10**: Go 后端 query-resources POST 实现

**工作量估算**: ~800 行新代码, ~200 行修改, 3-4 小时。


---

# 节点资源上报 详细设计（合并）

# 节点资源上报 (ResourceReport) — 详细设计

> **版本**: v2.4
> **状态**: 设计中
> **关联**: [节点/详细设计.md](../节点/详细设计.md) | [通道/详细设计.md](../通道/详细设计.md) | [DMA资源协议设计.md](../DMA资源协议设计.md) | [00-术语表.md](../00-术语表.md)

## 1. 功能概述

v2.2 删除了 v1 的 ReportResources 消息，导致前端"硬件资源与通道关联"页面始终为空。v2.4 重新引入该功能，采用 **frame_codec 纯二进制编码**，让 ESP32 节点在启动时主动上报其可用硬件资源（I2C/SPI/UART/GPIO/ADC 的端口、引脚、参数、DMA 能力），中心端解码后存入 DB 供前端展示和通道配置。

### 1.1 为什么要重新加入

| 维度 | v2.2 现状 | 问题 |
|------|-----------|------|
| capabilities API | 后端硬编码 `getDefaultESP32C6Buses()` | ESP32 从未上报，前端展示的是假数据 |
| hardware_info 字段 | DB 为 `{}` | 从未被写入 |
| 前端 ChannelPanel | 期望 `{buses: {uart:[...], i2c:[...]}}` | 拿到硬编码默认值 |
| query-resources POST | 返回假 request_id | 空壳，不发消息到 ESP32 |
| ESP32 固件 | 无 ResourceReport 实现 | 无 cJSON，无 0x19 消息类型 |

### 1.2 与 v1/v2.3 的区别

| 版本 | 编码方式 | 问题 |
|------|----------|------|
| v1 | protobuf ReportResources/ReportChannels | 类型多、字段复杂、已删除 |
| v2.3 (废弃) | JSON 字符串 | ESP32 需 cJSON (~2KB flash)，服务端需 json.Unmarshal |
| **v2.4 (本设计)** | **frame_codec 纯二进制** | **复用已有编解码器，零依赖，~400B/帧** |

### 1.3 设计原则

- **二进制优先**: 复用 frame_codec (protobuf-compatible varint framing)，不引入 cJSON
- **芯片级静态能力**: 硬件资源由 hw_profile 组件编译期声明，运行时只读
- **DMA 状态关联**: 上报内容包含 dma_supported (硬件静态) 和 dma_enabled (运行时配置)
- **极低频**: 仅 Hello 握手成功后发一次，带宽不是瓶颈

## 2. 协议设计

### 2.1 消息类型分配

| type | 消息 | 方向 | 说明 |
|------|------|------|------|
| 0x01-0x18 | (已分配) | - | 见 requirements.md |
| **0x19** | **ResourceReport** | **ESP→SVR** | **v2.4 新增: 硬件资源上报** |
| **0x1A** | **QueryResources** | **SVR→ESP** | **v2.4 新增: 服务端主动请求上报** |

### 2.2 ResourceReport 消息格式 (type=0x19)

采用 frame_codec 纯二进制编码，嵌套子消息结构：

```
═══════════════════════════════════════════════════════════════
 ResourceReport (type=0x19) — frame_codec 二进制编码
═══════════════════════════════════════════════════════════════

顶层消息:
  Field 1: platform          (string)  "ESP32C6"
  Field 2: resource_count    (varint)  buses 中所有条目总数
  Field 3: buses_blob        (bytes)   嵌套子消息: 所有硬件资源
  Field 4: channels_blob     (bytes)   嵌套子消息: 所有活跃通道
```

### 2.3 buses_blob 子消息

```
───────────────────────────────────────────────────────────────
 buses_blob 子消息:
───────────────────────────────────────────────────────────────
  Field 1: uart_entry  (bytes, repeated)  每个 UART 端口一条
  Field 2: i2c_entry   (bytes, repeated)  每个 I2C 端口一条
  Field 3: spi_entry   (bytes, repeated)  每个 SPI host 一条
  Field 4: gpio_entry  (bytes, repeated)  每个 GPIO pin 一条
  Field 5: adc_entry   (bytes, repeated)  每个 ADC channel 一条

  uart_entry 子消息:
    Field 1: id             (string)  "UART0"
    Field 2: port           (varint)  0
    Field 3: default_tx_pin (varint)  16
    Field 4: default_rx_pin (varint)  17
    Field 5: max_baud       (varint)  5000000
    Field 6: flags          (varint)  bit0=dma_supported

  i2c_entry 子消息:
    Field 1: id              (string)  "I2C0"
    Field 2: port            (varint)  0
    Field 3: default_sda_pin (varint)  21
    Field 4: default_scl_pin (varint)  22
    Field 5: max_freq_hz     (varint)  1000000
    Field 6: flags           (varint)  bit0=dma_supported

  spi_entry 子消息:
    Field 1: id               (string)  "SPI2"
    Field 2: port             (varint)  2
    Field 3: default_mosi_pin (varint)  23
    Field 4: default_miso_pin (varint)  19
    Field 5: default_sclk_pin (varint)  18
    Field 6: default_cs_pin   (varint)  5
    Field 7: max_freq_hz      (varint)  40000000
    Field 8: flags            (varint)  bit0=dma_supported

  gpio_entry 子消息:
    Field 1: id   (string)  "GPIO0"
    Field 2: pin  (varint)  0

  adc_entry 子消息:
    Field 1: id       (string)  "ADC1_CH0"
    Field 2: unit     (varint)  1
    Field 3: channel  (varint)  0
    Field 4: pin      (varint)  0
    Field 5: max_bits (varint)  12
```

### 2.4 channels_blob 子消息

```
───────────────────────────────────────────────────────────────
 channels_blob 子消息:
───────────────────────────────────────────────────────────────
  Field 1: channel_entry (bytes, repeated)  每个活跃通道一条

  channel_entry 子消息:
    Field 1: id           (varint)          通道 ID
    Field 2: bus_type     (varint)          1=UART,2=I2C,3=SPI,4=GPIO,5=ADC
    Field 3: hardware_id  (varint)          硬件标识
    Field 4: interval_ms  (varint)          采样间隔
    Field 5: enabled      (varint/bool)     是否启用
    Field 6: bus_config   (bytes)           原始 bus_config 字节
    Field 7: template_ids (repeated varint) 关联的模板 ID
    Field 8: dma_enabled  (varint/bool)     运行时 DMA 开关状态
```

### 2.5 QueryResources 消息 (type=0x1A)

```
QueryResources (SVR→ESP):
  Field 1: request_id (string)  可选, 用于匹配响应

ESP32 收到后:
  构建完整 ResourceReport (0x19) 并发送
```

### 2.6 DMA flags 编码

`flags` 字段是一个 varint，按位编码：

| bit | 含义 | 说明 |
|-----|------|------|
| 0 | dma_supported | 1=该硬件支持 DMA / 新 API，0=仅轮询/旧 API |
| 1-7 | reserved | 预留扩展 |

`dma_supported` 是硬件静态能力（hw_profile 提供），`dma_enabled` 是运行时状态（来自当前 channel 的 bus_config flags 位）。

### 2.7 bus_config 二进制格式

```
═══════════════════════════════════════════════════════════════
 bus_config 二进制格式 (big-endian)
═══════════════════════════════════════════════════════════════

UART (bus_type=1):
  标准: [tx_pin, rx_pin, baud×4(BE)]           = 6 bytes
  扩展: [tx_pin, rx_pin, baud×4(BE), flags]    = 7 bytes

I2C (bus_type=2):
  标准: [sda, scl, addr, freq×4(BE)]           = 7 bytes
  扩展: [sda, scl, addr, freq×4(BE), flags]    = 8 bytes

SPI (bus_type=3):
  标准: [cs, mode, freq×4(BE)]                 = 6 bytes
  扩展: [cs, mode, freq×4(BE), flags]          = 7 bytes

GPIO (bus_type=4):
  [pin, direction]                              = 2 bytes

ADC (bus_type=5):
  [unit, channel, attenuation]                  = 3 bytes

flags 字节:
  bit 0: dma_enabled (1=DMA/新API, 0=轮询/旧API)
  bits 1-7: reserved

向后兼容: config_len 不含 flags 字节时, 默认 dma_enabled=1
═══════════════════════════════════════════════════════════════
```

### 2.8 帧大小估算

```
platform "ESP32C6"        ~12B
resource_count             ~2B
buses_blob:
  2×UART entry × ~25B    ~55B
  1×I2C entry × ~20B     ~24B
  1×SPI entry × ~30B     ~34B
  8×GPIO entry × ~8B     ~68B
  3×ADC entry × ~12B     ~40B
  sub total              ~221B
channels_blob:
  8×channel entry × ~40B ~324B
frame overhead            ~20B
──────────────────────────
Total                    ~600B
```

### 2.9 为什么选二进制而非 JSON

| 方案 | 优点 | 缺点 |
|------|------|------|
| **二进制 (选定)** | 复用 frame_codec，零额外依赖，~600B/帧，编解码一致 | 服务端需实现二进制解码 |
| JSON (v2.3 废弃) | 可读、前端直接用 | 需 cJSON (~2KB flash)、字符串解析开销、帧膨胀到 ~2KB |

**决策**: 二进制。理由:
1. frame_codec 已有完整编解码实现，零额外依赖
2. 帧体积 ~600B vs JSON ~2KB
3. 与其他消息类型 (Hello/DataReport/ConfigManifest) 编码风格完全一致
4. 服务端 frame decoder 已有，只需添加 buses/channels 解码逻辑

## 3. 交互流程

### 3.1 上报时序

**核心原则**: ResourceReport 必须在 Hello 握手成功（收到 HelloAck）后发送。此时 MQTT 通道已确认双向可达，服务端已为该节点创建/更新 DB 记录。

```
ESP32                            Server                          Frontend
  │                                │                                │
  │── Hello (0x01) ──────────────→│                                │
  │                                │  UPSERT nodes 表               │
  │                                │  SyncGate 决策                  │
  │←── HelloAck (0x12) ──────────│  ← 握手成功                     │
  │                                │                                │
  │  (构建 ResourceReport 二进制帧) │                                │
  │── ResourceReport (0x19) ─────→│                                │
  │   platform="ESP32C6"          │  decode_binary_report()        │
  │   buses_blob={binary}         │  → buses → capabilities JSONB  │
  │   channels_blob={binary}      │  → channels upsert             │
  │                                │  DB UPDATE                     │
  │                                │  WS push: node_resources_updated│
  │                                │                                │
  │←── ConfigManifest (0x04) ────│  (如果 SyncGate 决策需要下发)    │
  │── ConfigResult (0x05) ──────→│                                │
  │                                │                                │
  │                                │←── GET /api/v1/nodes/1/capabilities
  │                                │─── {buses: {uart:[...], ...}} ─│ ← 真实数据
  │                                │                                │
  │                                │                   渲染硬件资源卡片 ✓
```

### 3.2 ESP32 端触发条件

| 触发条件 | 时机 | 说明 |
|----------|------|------|
| **首次握手** | MQTT 连接后 Hello → HelloAck 成功 | main.c hello_handshake_task 成功后调用 |
| **重连握手** | MQTT 断线重连后 Hello → HelloAck 成功 | 同上，每次重连都重新上报 |
| **手动查询** | 收到 QueryResources (0x1A) | 服务端主动请求刷新（前端点"同步硬件"按钮） |

**不会触发的场景**:
- ❌ Hello 发出但未收到 HelloAck — 握手未完成，不发
- ❌ 周期性 StatusReport — 资源不随时间变化，无需重复上报
- ❌ ConfigManifest 接收后 — 硬件资源是固有属性，但因 channels 部分会变化，ConfigManifest 后重新上报一次

### 3.3 服务端处理

1. 用 frame_decoder 解码 ResourceReport 二进制帧
2. 解码 buses_blob 子消息 → 构建 `{buses: {...}}` JSON 对象
3. 解码 channels_blob 子消息 → 构建 channel records
4. `capabilities` JSONB = buses JSON
5. `hardware_info` JSONB = 完整 JSON (buses + channels)
6. Upsert channels 到 channels 表 (以 node_id + hardware_id 为唯一键)
7. WebSocket 推送 `node_resources_updated` 事件

### 3.4 query-resources POST 实现

```
前端点击"同步硬件"
  → POST /api/v1/nodes/:id/query-resources
    → 后端构建 QueryResources (0x1A) 帧
    → 通过 MQTT sender 发送到 ESP32
    → ESP32 收到 0x1A → 构建完整 ResourceReport (0x19) 并发送
    → 后端解码 → DB UPDATE → WS push
  → 前端等待 WS push 后刷新
```

## 4. API 变更

### 4.1 GET /api/v1/nodes/:id/capabilities

**v2.2 (当前)**: 硬编码 `getDefaultESP32C6Buses()` 作为 fallback

**v2.4 (新)**: 从 DB 读取，DB 为空时仍保留 fallback 向后兼容

```
Response:
{
  "code": 200,
  "data": {
    "capabilities": ["adc", "gpio", "i2c", "spi", "uart"],
    "buses": {
      "uart": [
        {"id":"UART0","port":0,"default_tx":16,"default_rx":17,
         "max_baud":5000000,"dma_supported":true,"pins":[...]},
        ...
      ],
      "i2c": [...],
      "spi": [...],
      "gpio": [...],
      "adc": [...]
    }
  }
}
```

### 4.2 GET /api/v1/nodes/:id/hardware/config

**v2.4 (新)**: 从 DB `hardware_info` 字段读取，包含 dma_enabled 状态

### 4.3 POST /api/v1/nodes/:id/query-resources

**v2.2 (当前)**: 返回假 request_id，不发 MQTT 消息

**v2.4 (新)**: 构建 QueryResources (0x1A) 帧，通过 sender 发送到节点

```
Response:
{
  "code": 200,
  "data": {"request_id": "res-1-1718456789"}
}
```

## 5. 数据库变更

**无需 schema 变更**。`nodes.capabilities` 和 `nodes.hardware_info` 已是 JSONB 类型，当前为 `{}`。只需后端解码后实际写入即可。

存储策略:
- `capabilities`: 解码 buses_blob 后构建的 JSON (含 dma_supported 字段)
- `hardware_info`: 完整 JSON (buses + channels + dma_enabled 状态)

## 6. ESP32-C6 默认硬件资源

当 DB 为空且节点离线时，capabilities API 返回以下默认值 (后端硬编码 fallback):

```json
{
  "buses": {
    "uart": [
      {"id":"UART0","port":0,"default_tx":16,"default_rx":17,"max_baud":5000000,
       "dma_supported":true,"pins":[{"pin":16,"role":1},{"pin":17,"role":2}]},
      {"id":"UART1","port":1,"default_tx":21,"default_rx":20,"max_baud":5000000,
       "dma_supported":true,"pins":[{"pin":21,"role":1},{"pin":20,"role":2}]}
    ],
    "i2c": [
      {"id":"I2C0","port":0,"default_sda":21,"default_scl":22,"max_freq_hz":1000000,
       "dma_supported":true,"pins":[{"pin":21,"role":3},{"pin":22,"role":4}]}
    ],
    "spi": [
      {"id":"SPI2","port":2,"default_mosi":23,"default_miso":19,"default_sclk":18,
       "default_cs":5,"max_freq_hz":40000000,"dma_supported":true,
       "pins":[{"pin":23,"role":5},{"pin":19,"role":6},{"pin":18,"role":7},{"pin":5,"role":8}]}
    ],
    "gpio": [{"id":"GPIO0","pin":0,"pins":[{"pin":0,"role":9}]}, ...],
    "adc": [{"id":"ADC1_CH0","unit":1,"channel":0,"max_bits":12,"pins":[{"pin":0,"role":10}]}, ...]
  }
}
```

## 7. 实现清单

### 7.1 ESP32 固件 (C)

| 文件 | 变更 | 说明 |
|------|------|------|
| `components/frame/frame_codec.h` | 新增常量 | `MSG_RESOURCE_REPORT 0x19`, `MSG_QUERY_RESOURCES 0x1A` |
| `components/hw_profile/include/hw_profile.h` | **新建** | 芯片能力常量结构体 + build 函数声明 |
| `components/hw_profile/hw_profile.c` | **新建** | ESP32-C6 静态 profile + frame_codec 二进制编码 |
| `components/hw_profile/CMakeLists.txt` | **新建** | REQUIRES frame, config_mgr |
| `components/msg_handler/msg_handler.h` | 新增声明 | `msg_handler_send_resource_report(void)` |
| `components/msg_handler/msg_handler.c` | 新增逻辑 | case MSG_QUERY_RESOURCES → 触发上报 |
| `main/main.c` | 修改 | HelloAck 成功后调用 `msg_handler_send_resource_report()` |

### 7.2 后端 (Go)

| 文件 | 变更 | 说明 |
|------|------|------|
| `pkg/frame/frame.go` | 新增常量 | `MsgResourceReport = 0x19`, `MsgQueryResources = 0x1A` |
| `internal/nodemgr/handler_resources.go` | **重写** | 二进制解码 (替代原 JSON 解码) |
| `internal/api/handler_node.go` | 修改 | query-resources POST 真正发 0x1A 帧 |

### 7.3 前端 (Vue3/TS)

| 文件 | 变更 | 说明 |
|------|------|------|
| `src/components/channel/ChannelManager.vue` | 新增 | DMA 开关 (el-switch)，读取 dma_supported 控制状态 |

## 8. 向后兼容

- **ESP32 未升级**: 旧固件不发 ResourceReport，capabilities API 返回硬编码默认值
- **前端未更新**: 旧前端仍用 capabilities 列表模式，不影响
- **协议兼容**: 0x19/0x1A 是新 type，旧服务端会 `Unknown msg type` 并忽略（无副作用）
- **bus_config 兼容**: flags 字节是扩展字段，旧 config_len 不含 flags 时默认 dma_enabled=1

## 9. 测试矩阵

| # | 场景 | 预期 |
|---|------|------|
| T1 | ESP32 Hello → HelloAck → ResourceReport → DB 写入 | capabilities/hardware_info 非空，channels 表有记录 |
| T2 | ESP32 Hello → 无 HelloAck → 不发 ResourceReport | 不发，DB 无更新 |
| T3 | GET /api/v1/nodes/1/capabilities | 返回 `{capabilities:[...], buses: {...}}`，含 dma_supported |
| T4 | 前端节点详情页 → 硬件资源与通道关联 | 显示 UART/I2C/SPI/GPIO/ADC 卡片，含 DMA 能力标识 |
| T5 | 节点离线 → GET capabilities | 返回 ESP32-C6 默认值 (fallback) |
| T6 | POST /api/v1/nodes/1/query-resources | 发送 0x1A 帧到节点，ESP32 回复 0x19 |
| T7 | 前端点击"同步硬件" → WS push | 硬件资源更新，DMA 状态可见 |
| T8 | 旧固件不发 ResourceReport | capabilities API 返回默认值，前端不报错 |
| T9 | MQTT 重连 → Hello → HelloAck → 重新上报 | capabilities/hardware_info 更新为最新 |
| T10 | ResourceReport 含 channels + dma_enabled | channels 表 dma_enabled 字段正确 |

## 10. channels 同步策略

ResourceReport 中的 `channels` 数组反映 ESP32 NVS 中**当前正在运行**的通道配置。服务端收到后：

1. **比对**: 以 `(node_id, hardware_id)` 为唯一键，与 DB 中现有 channels 比较
2. **新增**: NVS 中有但 DB 无 → INSERT
3. **更新**: 两边都有但 config 不同 → UPDATE（以 NVS 为准，节点是 truth）
4. **删除**: DB 有但 NVS 无 → 不自动删除（可能是用户手动创建的未激活通道）
