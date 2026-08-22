# EHomeSystem ESP32 节点架构设计文档

**版本**: v2.7  
**日期**: 2026-07-30  
**分支**: main (codex/multi-bus-event-driven-plan 已合入)  
**状态**: 全部已实现。事件驱动 RX、控制器租约、控制/采样队列分离、异步报告路径、ChannelCmdV2、host 测试覆盖均已落地。  
**最近更新**: 2026-07-30 — 同步 multi-bus 事件驱动架构合入。关联 merge: 87767c7

---

## 1. 架构总览

### 1.1 设计目标

1. **多芯片兼容**: ESP32-S3 / ESP32-C6 统一代码，通过 `CONFIG_IDF_TARGET_*` 条件编译扩展
2. **DMA 资源管理**: DMA 通道作为一等公民，协议层定义意愿，节点层决定现实
3. **UART0 双用**: 固件烧录 + 用户数据串口，通过 USB console 解放 UART0
4. **依赖注入**: 零全局单例，所有依赖通过 `app_state_t` 聚合注入

### 1.2 组件拓扑

```
                        ┌─────────────┐
                        │   app_main  │  (main.c)
                        │   启动编排   │
                        └──────┬──────┘
                               │
              ┌────────────────┼────────────────┐
              ▼                ▼                ▼
     ┌────────────┐  ┌─────────────┐  ┌──────────────┐
     │ app_state  │  │ config_mgr  │  │ msg_handler  │
     │ (聚合根)    │  │ (配置管理)   │  │ (消息分发)    │
     └─────┬──────┘  └──────┬──────┘  └──────┬───────┘
           │                │                │
     ┌─────┴──────┐        │                │
     ▼            ▼        ▼                ▼
┌─────────┐ ┌─────────┐ ┌──────────┐ ┌────────────┐
│dma_pool │ │bus_mgr  │ │hw_profile│ │frame_codec │
│(DMA管理) │ │(总线管理)│ │(硬件描述) │ │(二进制协议) │
└─────────┘ └────┬────┘ └──────────┘ └────────────┘
                 │
                 ▼
           ┌──────────┐     ┌──────────────┐
           │ bus_dma  │────→│ bus_worker   │
           │(驱动层)   │     │(事件驱动RX/TX)│
           └──────────┘     └──────┬───────┘
                                   │
                            ┌──────┴───────┐
                            │  scheduler   │
                            │(定时采样调度) │
                            └──────────────┘
```

### 1.3 组件依赖图 (无环)

```
frame_codec         ← 叶子 (无依赖)
dma_pool            ← frame_codec
bus_dma             ← freertos, driver
uart0_boot          ← driver, rgb_led
hw_profile          ← frame_codec, config_mgr, dma_pool
config_mgr          ← frame_codec, nvs_flash, dma_pool
bus_worker          ← bus_dma, cmd_queue, scheduler, frame_codec, freertos
scheduler           ← config_mgr, freertos, driver(uart)
msg_handler         ← frame_codec, config_mgr, hw_profile, dma_pool, bus_worker, scheduler, ...
bus_manager (main)  ← dma_pool, bus_dma, config_mgr, hw_profile, bus_lease_policy
app_state (main)    ← dma_pool, hw_profile, bus_dma, config_mgr, bus_worker, scheduler
main.c              ← 所有组件 (编排层)
```

---

## 2. 核心设计模式

### 2.1 聚合根模式 (app_state_t)

`app_state_t` 是整个系统的聚合根，持有所有子系统的引用：

```c
typedef struct {
    char        node_id[32];                    // 身份标识
    bus_dma_ctx_t bus_ctx[SCHED_MAX_CHANNELS];  // 总线上下文池
    uint32_t      bus_ch[SCHED_MAX_CHANNELS];    // 通道ID映射
    struct dma_pool_t *dma_pool;                // DMA资源池 (DIP注入)
    SemaphoreHandle_t config_mutex;              // 配置锁
    transport_t *tcp_transport;                  // TCP传输
    QueueHandle_t cmd_queue;                     // 命令队列 (legacy, 保留兼容)
    QueueHandle_t uart0_sample_queue;            // UART0 采样队列 (scheduler)
    QueueHandle_t uart0_control_queue;           // UART0 控制队列 (WriteCmd/V2)
    QueueHandle_t uart1_sample_queue;            // UART1 采样队列
    QueueHandle_t uart1_control_queue;           // UART1 控制队列
    QueueHandle_t uart2_sample_queue;            // UART2 采样队列
    QueueHandle_t uart2_control_queue;           // UART2 控制队列
    QueueHandle_t spi_sample_queue;              // SPI 采样队列
    QueueHandle_t spi_control_queue;             // SPI 控制队列
    QueueHandle_t i2c_sample_queue;              // I2C 采样队列
    QueueHandle_t i2c_control_queue;             // I2C 控制队列
    uint32_t    uptime_sec;
    bool        config_received;
    // ...
} app_state_t;
```

**设计原则**: 所有模块通过 `app_state_t*` 参数获取依赖，不依赖全局单例。

### 2.2 依赖注入 (DIP)

**设计原则**: 依赖通过 setter 注入，而非模块内部创建。被注入的组件存储在模块级静态变量中，但所有权和生命周期由 `app_state_t` 管理。

```
┌──────────────────────────────────────────────┐
│                  app_state_init()             │
│                                              │
│  s_dma_pool ← dma_pool_init(hw_dmas, count)  │
│  s.dma_pool = &s_dma_pool                    │
│                                              │
│  config_mgr_set_dma_pool(s.dma_pool)  ──────→ config_mgr内部存储 │
│  msg_handler_set_dma_pool(s.dma_pool) ──────→ msg_handler内部存储 │
└──────────────────────────────────────────────┘
```

**DIP 符合度说明**:
- ✅ 依赖不从模块内部创建 (符合 DIP)
- ✅ 依赖通过 app_state_t 集中管理 (聚合根模式)
- ⚠️ 被注入的组件存储在模块级静态变量中 (C 语言的实现限制)
- ✅ 模块不直接依赖具体实现，而是依赖注入的接口指针

这种模式在嵌入式 C 中是标准做法，平衡了架构纯净性和实现简洁性。

被注入的组件:
- `config_mgr`: 解析 DmaChannelConfig 时调用 `dma_pool_apply_config()`
- `msg_handler`: 编码 ResourceReport 时调用 `dma_pool_serialize()`
- `bus_manager`: 分配通道时调用 `dma_pool_allocate()`

### 2.3 策略模式 (DMA 分配)

```c
// dma_pool_allocate 三级策略:
// 1. 已绑定 → 直接返回 (幂等)
// 2. 空闲 + 兼容 + TX+RX → 优先分配
// 3. 空闲 + 兼容 (部分能力) → 降级分配
// 4. 无可用 → ESP_ERR_NOT_FOUND → 调用方自动降级 polled
```

### 2.4 模板方法 (多芯片硬件表)

每个芯片在 `hw_profile.c` 中定义静态 const 表：

```c
#ifdef CONFIG_IDF_TARGET_ESP32S3
  const hw_uart_t hw_uarts[3] = { ... };  // 3 UART
  const hw_dma_t  hw_dmas[6]  = { ... };  // 6 GDMA
#elif defined(CONFIG_IDF_TARGET_ESP32C6)
  const hw_uart_t hw_uarts[2] = { ... };  // 2 UART
  const hw_dma_t  hw_dmas[3]  = { ... };  // 3 GDMA
#endif
```

运行时通过 `dma_pool_init(pool, hw_dmas, HW_DMA_COUNT)` 传入，dma_pool 本身不包含任何芯片特定的代码。

### 2.5 事件驱动总线架构 (v2.7)

v2.7 将总线事务从"共享队列轮询"升级为"事件驱动 + 控制器租约 + 控制/采样分离"：

**RX 事件驱动**: rx_task 不再每 5ms 轮询 UART FIFO，而是通过 UART 驱动 event queue + QueueSet 等待事件。250ms 周期唤醒仅用于生命周期管理和超时检查。CPU 占用大幅降低。

**控制/采样队列分离**: 每个控制器拥有两条独立队列——sample queue（scheduler 定时采样）和 control queue（WriteCommand/ChannelCmdV2 控制命令）。遥测背压无法消耗控制容量，控制命令始终有 2 个保留槽位。

**控制器租约 (lease)**: manifest 应用前由 bus_lease_policy 为每个 channel 选定具体控制器（UART port / SPI host / I2C port），而非运行时动态分配。bus_manager_snapshot_leases() 在事务前快照当前租约，失败回滚时恢复原始绑定。

**异步报告路径**: 独立 report_task（优先级 5）通过三个内存池（critical 4块 / emergency 1块 / telemetry 12块，每块 1024B）异步编码发布。关键报告（error_code≠0 或 request_id≠0）使用保留池，遥测压力不挤占关键响应。

**生命周期加固**: suspend 用 EventGroup 逐任务确认，2s 超时返回 bool。suspend 失败时中止配置事务，不删除活跃队列。

---

## 3. DMA 资源协议

### 3.1 协议字段定义

**ResourceReport (MSG_RESOURCE_REPORT = 0x19) 新增 field 8:**

```
DmaChannel (嵌套消息, field 8, 可重复):
  F1 (varint): dma_id          — 平台唯一ID
  F2 (bytes):  name            — "GDMA_CH0"
  F3 (varint): dma_type        — 0=GDMA
  F4 (varint): capabilities    — bit0=TX, bit1=RX, bit2=burst
  F5 (varint): max_burst       — 最大突发长度
  F6 (varint): state           — 0=free, 1=allocated, 2=disabled
  F7 (bytes):  bound_to        — "UART1" / "" (空=未绑定)
  F8 (varint): compatible_bus  — bit0=UART, bit1=I2C, bit2=SPI
```

**ConfigManifest (MSG_CONFIG_MFST = 0x04) 新增 field 5:**

```
DmaChannelConfig (嵌套消息, field 5, 可重复):
  F1 (varint): dma_id    — 目标DMA通道ID
  F2 (varint): enabled   — 1=启用, 0=禁用
  F3 (bytes):  bind_to   — "UART1" / "" (空=自动)
```

### 3.2 数据流

```
前端/后端                      ESP32 节点
    │                            │
    │  ① ConfigManifest          │
    │  (含 DmaChannelConfig)     │
    │ ─────────────────────────→ │
    │                            │ config_mgr 解析 field 5
    │                            │ → dma_pool_apply_config()
    │                            │
    │  ② ResourceReport          │
    │  (含 dma_channels)         │
    │ ←───────────────────────── │
    │                            │ hw_profile_build_report()
    │                            │ → dma_pool_serialize()
    │                            │
    │  ③ bus_setup               │
    │  (channel需要DMA)          │
    │ ─────────────────────────→ │
    │                            │ bus_manager → dma_pool_allocate()
    │                            │ → bus_dma_init(dma=true/false)
```

### 3.3 bus_config.flags 语义

```c
// flags byte (1 byte)
#define FLAG_DMA_ACTIVE  0x01  // bit 0: 当前实际使用DMA (只读回传)
// bit 1-7: 保留

// 默认行为: DMA优先自动分配, 不可用时降级polled
// 用户通过 DmaChannelConfig 控制哪些DMA通道可用/禁用/绑定
```

---

## 4. 多芯片硬件配置

### 4.1 ESP32-S3

| 资源 | 数量 | DMA | 备注 |
|------|------|-----|------|
| UART0 | TX43/RX44 | ✓ | ROM下载口 + 用户数据 |
| UART1 | TX20/RX21 | ✓ | CH340 (开发板) |
| UART2 | TX1/RX2 | ✓ | 通用 |
| I2C0 | SDA8/SCL9 | ✓ | |
| I2C1 | SDA47/SCL48 | ✓ | |
| SPI2 | MOSI11/MISO13/SCLK12/CS10 | ✓ | |
| SPI3 | MOSI35/MISO37/SCLK36/CS34 | ✓ | |
| GDMA | 6通道 (CH0-4通用, CH5 SPI专用) | | |
| GPIO | 12个可用 | | |
| ADC | 5通道 (ADC1_CH0-4) | | |

### 4.2 ESP32-C6

| 资源 | 数量 | DMA | 备注 |
|------|------|-----|------|
| UART0 | TX16/RX17 | ✓ | ROM下载口 + 用户数据 |
| UART1 | TX20/RX21 | ✓ | |
| I2C0 | SDA21/SCL22 | ✓ | |
| SPI2 | MOSI23/MISO19/SCLK18/CS5 | ✓ | |
| GDMA | 3通道 (CH0/CH2通用, CH1 SPI专用TX) | | |
| GPIO | 8个可用 | | |
| ADC | 3通道 (ADC1_CH0-2) | | |

> **v2.7 变更**: LP_UART0 (port=2, GPIO5/4) 已从 hw_uarts 表移除。
> 该端口无 DMA、仅 16B FIFO，不适合生产数据采集。

### 4.3 UART0 双用机制

```
┌─────────────────────────────────────────────┐
│              USB Serial/JTAG                 │
│         console日志 + 固件烧录 (默认)         │
└─────────────────────────────────────────────┘

┌─────────────────────────────────────────────┐
│              UART0 (TX/RX)                   │
│                                             │
│  正常模式:  用户数据串口 (DMA默认开启)         │
│  下载模式:  ROM bootloader UART0下载          │
│                                             │
│  进入下载模式:                                │
│    ① 硬件: 按住BOOT + 按RESET               │
│    ② 开机检测: BOOT按下 → 跳过UART0初始化     │
│    ③ 软件: uart0_boot_enter_download()       │
│         → GPIO0 hold low + esp_restart      │
└─────────────────────────────────────────────┘
```

**UART0 可用性由编译配置决定:**

```c
// bus_dma.c
#if defined(CONFIG_ESP_CONSOLE_USB_SERIAL_JTAG)
  #define UART0_START_INDEX  0   // Console在USB — UART0可用
#else
  #define UART0_START_INDEX  1   // Console在UART0 — 跳过
#endif
```

---

## 5. 启动流程

```
app_main()
  │
  ├── nvs_flash_init()
  ├── ota_confirm_valid()
  │
  ├── app_state_init()
  │   ├── generate_node_id()          ← MAC地址 → 12字符hex
  │   ├── dma_pool_init(hw_dmas, HW_DMA_COUNT)  ← 芯片DMA表
  │   ├── s_app.dma_pool = &s_dma_pool
  │   └── 创建 5×2 控制/采样队列对 (UART0/1/2, SPI, I2C)
  │
  ├── uart0_boot_init()               ← BOOT检测
  │   └── [BOOT按下] → download_wait_task (阻塞)
  │
  ├── config_mgr_init()
  ├── config_mgr_set_dma_pool(s->dma_pool)     ← DIP注入
  ├── msg_handler_set_dma_pool(s->dma_pool)    ← DIP注入
  │
  ├── config_mgr_load_from_nvs()
  │   └── [有DmaChannelConfig] → dma_pool_apply_config()
  │
  ├── bus_manager_init(rt)
  ├── bus_manager_snapshot_leases(rt)  ← 快照当前租约
  ├── bus_manager_setup_from_manifest(rt)
  │   └── 每个channel:
  │       ├── bus_lease_policy 选定控制器
  │       ├── dma_pool_allocate(pool, bus_type, hw_id, &dma_id)
  │       │   ├── ESP_OK → bus_dma_init_preferred(dma=true, controller_id)
  │       │   └── ESP_ERR_NOT_FOUND → bus_dma_init_preferred(dma=false)
  │       ├── hw_profile_runtime_set()  ← 记录运行时租约
  │       └── [init失败] → dma_pool_release_by_hw()
  │
  ├── bus_worker_set_callbacks(wr_cb, dr_cb)
  ├── bus_worker_set_channel_cmd_v2_final_cb(v2_cb)
  ├── bus_worker_start(rt)            ← 事件驱动 RX + 5 cmd_task + report_task
  ├── scheduler_init()
  ├── scheduler_resume(queues)        ← 注入 5 条采样队列 + uart_route 回调
  ├── transport_manager_init()
  ├── wifi_mgr_start()
  └── hw_profile_send_resource_report()  ← Hello 后上报资源
```

---

## 6. 设计原则符合度

| 原则 | 评分 | 实现方式 |
|------|------|---------|
| **SRP** 单一职责 | ★★★★☆ | dma_pool只管DMA，bus_dma只管驱动，hw_profile描述+编码 |
| **OCP** 开闭 | ★★★★★ | 新芯片只需加#elif分支和const表，核心模块零修改 |
| **LSP** 里氏替换 | ★★★★☆ | bus_dma_ctx_t union统一接口，transport_t接口可互换 |
| **ISP** 接口隔离 | ★★★★★ | API精简：dma_pool 7个函数，bus_dma 3个函数 |
| **DIP** 依赖倒置 | ★★★★★ | dma_pool通过app_state注入，零全局单例 |
| **LoD** 迪米特 | ★★★★★ | hw_id统一语义(UART_CH3)，frame_encoder_append_raw封装 |
| **CARP** 组合复用 | ★★★★★ | 纯组合架构，app_state聚合所有子系统 |

### 6.1 关键设计决策

| 决策 | 理由 |
|------|------|
| dma_pool无全局getter | DIP: 依赖通过app_state注入，不隐藏全局依赖 |
| hw_id含bus_type前缀 | LoD: "UART_CH3"比"CH3"更清晰反映实际资源 |
| frame_encoder_append_raw | LoD: 封装enc.pos操作，调用方不直接访问内部状态 |
| hw_dma_t定义在dma_pool.h | 避免循环依赖: hw_profile.h → dma_pool.h (单向) |
| const表 + 运行时池 | 静态描述(只读) vs 运行时状态(可变) 分离 |
| 三级分配策略 | TX+RX优先 → 部分能力降级 → 不可用polled |

---

## 7. 扩展指南

### 7.1 添加新芯片 (如ESP32-H2)

```
1. hw_profile.h:
   #elif defined(CONFIG_IDF_TARGET_ESP32H2)
     #define HW_UART_COUNT  2
     #define HW_DMA_COUNT   3
     ...

2. hw_profile.c:
   const hw_uart_t hw_uarts[2] = { ... };
   const hw_dma_t  hw_dmas[3]  = { ... };
   ...

3. sdkconfig.defaults.esp32h2:
   CONFIG_IDF_TARGET="esp32h2"
   CONFIG_ESP_CONSOLE_USB_SERIAL_JTAG=y
   ...

无需修改: dma_pool, bus_dma, config_mgr, bus_manager
```

### 7.2 添加新总线类型 (如I2S)

```
1. bus_dma.h:
   #define BUS_TYPE_I2S  4
   添加 i2s 联合体成员

2. dma_pool.h:
   #define DMA_BUS_I2S  (1 << 3)

3. bus_dma.c:
   实现 i2s_init / i2s_transact / i2s_deinit

4. hw_profile.c:
   添加 hw_i2s_t 表和 HW_I2S_COUNT
```

---

## 8. 文件清单

### 新建文件

| 文件 | 职责 |
|------|------|
| `components/dma_pool/include/dma_pool.h` | DMA资源管理器接口 + hw_dma_t类型 |
| `components/dma_pool/dma_pool.c` | 分配/释放/序列化实现 |
| `components/uart0_boot/include/uart0_boot.h` | UART0下载模式管理接口 |
| `components/uart0_boot/uart0_boot.c` | BOOT检测 + GPIO hold + 重启 |
| `sdkconfig.defaults.esp32s3` | S3特有默认配置 |
| `sdkconfig.defaults.esp32c6` | C6特有默认配置 |

### 修改文件

| 文件 | 改动 |
|------|------|
| `main/app_state.h` | +dma_pool_t* 字段, +forward decl |
| `main/app_state.c` | +dma_pool_init in init |
| `main/main.c` | +setter calls, -dma_pool局部init |
| `main/bus_manager.c` | +dma_pool_allocate集成, +derive_hw_id |
| `components/frame/frame_codec.h` | +frame_encoder_append_raw |
| `components/frame/frame_codec.c` | +append_raw实现 |
| `components/config_mgr/config_mgr.h` | +set_dma_pool setter |
| `components/config_mgr/config_mgr.c` | +field 5解析, +DMA注入 |
| `components/msg_handler/msg_handler.h` | +set_dma_pool setter |
| `components/msg_handler/msg_handler.c` | +DMA注入, 传参build_report |
| `components/hw_profile/include/hw_profile.h` | +S3/C6条件表, +DMA表 |
| `components/hw_profile/hw_profile.c` | +DMA表, +field 8编码 |
| `components/bus_dma/bus_dma.c` | +UART0_START_INDEX, +GPIO_PIN_MAX |
| `components/factory_reset/factory_reset.c` | +S3/C6 BOOT GPIO适配 |
| `sdkconfig.defaults` | console改为USB Serial/JTAG |

---

## 9. 向后兼容性

| 改动 | 兼容性 |
|------|--------|
| ResourceReport新增field 8 | ✅ 旧前端忽略未知字段 |
| ConfigManifest新增field 5 | ✅ 旧节点忽略未知字段 |
| bus_config.flags bit0=DMA_ACTIVE | ✅ 只影响回传，不影响下发 |
| UART0可用性编译时决定 | ✅ 运行时行为不变 |
| app_state_t新增dma_pool字段 | ✅ 内存布局向后兼容(NVS blob除外) |

---

## 10. 已知技术债务

### 10.1 全局变量 `g_cmd_queue` (app_state.c) — ✅ 已解决

**原状**: `scheduler.c` 通过全局变量访问命令队列。

**解决**: v2.7 引入 `scheduler_queues_t` 结构体，通过 `scheduler_resume(queues)` 注入 5 条采样队列 + uart_route 回调，彻底消除全局依赖。

### 10.2 NVS Blob 前向兼容性

**现状**: `config_mgr.c` 中 `config_mgr_save_to_nvs()` 直接序列化整个 `config_manifest_t` 结构体。

**风险**: 结构体字段变化会导致旧 NVS 数据不兼容，可能引发解析错误。

**建议改进**:
1. 添加版本号字段: `uint32_t config_version`
2. 或使用 protobuf-c 序列化替代裸结构体
3. 加载时检查版本号，不兼容时清除旧数据

**优先级**: 中 (当前结构体稳定，但未来扩展需注意)

### 10.3 单元测试缺失 — ✅ 已解决

**原状**: `dma_pool`、`bus_manager`、`scheduler` 缺少自动化测试。

**解决**: v2.7 新增 ~6000 行 host 测试，覆盖：
- bus_dma (933行)、bus_worker_rx (838行)、bus_worker_report (877行)
- bus_manager_manifest (506行)、scheduler_route (505行)
- handler_data (478行)、bus_dma_lease (319行)、app_state (250行)
- channel_cmd_v2_decoder (235行)、frame_codec_boundary (216行)
- writecmd_decoder (209行)、perf_metrics_encode (183行)
- hw_profile_resource_report (107行)、dma_pool_contract (89行)

测试位于 `esp32-collector/host_tests/`，含完整 FreeRTOS/ESP-IDF stub 层。

### 10.4 hw_profile 职责过重 — ✅ 已解决

**原状**: `hw_profile.c` 同时负责硬件描述、协议编码、config 查询、DMA 序列化。

**解决**: v2.7 拆分为：
- `hw_tables.c` — 纯硬件描述表 (const 数据) + 控制器推导函数
- `hw_profile.c` — 协议编码逻辑 (ResourceReport) + 运行时租约跟踪

新增 `hw_derive_spi_host()` 和 `hw_derive_i2c_port()`，与 UART 推导对齐。
