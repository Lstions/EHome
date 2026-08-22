# 实时系统架构师评审：ESP32-C6 多总线并行优化方案

**评审人**: RTOS Architect  
**日期**: 2026-06-23  
**目标**: 从 FreeRTOS 调度、DMA、中断、时序约束角度评审方案 A/B/C，给出最优方案

---

## 一、现状量化分析

### 1.1 硬件资源约束（ESP32-C6）

| 资源 | 数量 | 约束 |
|------|------|------|
| HP UART | 2 (UART0+UART1) | UART0 保留给 boot/download，实际可用 1 个 |
| UHCI (DMA) | 1 | 两个 UART 共享，同时刻只有 1 个可用 DMA |
| HP I2C | 1 (I2C_NUM_0) | 第二条独立 I2C 总线初始化必然失败 |
| SPI | 1 (SPI2) | 多设备通过 CS 分时复用 |
| GDMA | 3 对 (CH0/CH1/CH2) | CH1=UART+SPI, CH0/CH2=SPI only |
| SRAM | ~512KB | FreeRTOS 堆约 300KB 可用 |

### 1.2 当前任务模型

```
scheduler_task (prio 5, 4096 stack) — 10ms tick, 生产 cmd_queue
cmd_task       (prio 7, 4096 stack) — 消费 cmd_queue, 串行处理所有总线
rx_task        (prio 8, 4096 stack) — 5ms 轮询 UART RX
```

**关键瓶颈**: cmd_task 是单消费者，UART 的 `vTaskDelay(delay_ms)` 阻塞整个任务，SPI/I2C 命令排队等待。

### 1.3 时序量化

- 2 UART × 15 命令/周期 × 平均 75ms delay = **2.25s 纯等待/60s 周期**
- I2C transact 100ms 超时 × 故障设备 = **100ms 阻塞**
- rx_task 5ms 轮询 8 slot，75% 无效 = **CPU 浪费**

---

## 二、核心辩论

### 2.1 delay_ms 是否可以完全消除？

**结论：不能完全消除，但可以替换为非阻塞等待。**

#### Modbus RTU Turn-Around Time 分析

Modbus RTU 协议规定：主站发送请求后，必须等待至少 **3.5 个字符时间** 的静默间隔才能发送下一个请求。这个间隔的作用是：
1. 让从站识别帧结束（帧间分隔符）
2. 给从站处理时间

**计算 @4800 baud**:
```
1 char = 11 bits (1 start + 8 data + 1 parity + 1 stop)
3.5 char = 38.5 bits
38.5 / 4800 = 8.02ms
```

**计算 @9600 baud**:
```
3.5 char = 38.5 bits
38.5 / 9600 = 4.01ms
```

**计算 @115200 baud**:
```
3.5 char = 38.5 bits
38.5 / 115200 = 0.33ms
```

#### 当前 delay_ms 的真实语义

查看 `bus_worker.c:168-170`:
```c
if (cmd.delay_ms > 0) {
    vTaskDelay(pdMS_TO_TICKS(cmd.delay_ms));
}
```

这个 delay_ms 来自 `config_template_t.delay_ms`，由后端配置。它**不是** Modbus turn-around time 的精确实现——它是一个粗略的"给设备处理时间"的等待。

#### 为什么不能简单删除

1. **Modbus 协议合规性**: 如果 cmd_task 连续发送两个 Modbus 请求到**同一个 UART 端口**，中间没有 3.5 char 的间隔，从站会认为这是同一个帧的延续，导致帧解析错误。

2. **但当前实现本身就有问题**: 
   - `vTaskDelay(pdMS_TO_TICKS(75))` 实际等待 75ms，远超 8ms 的 turn-around time
   - delay 期间 rx_task 已经在异步拾取响应，delay 不影响 RX
   - delay 的唯一作用是防止**同一 UART 端口**上的连续 TX 过快

3. **关键洞察**: delay_ms 只在**同一 UART 端口**上才需要。不同总线类型之间完全不需要等待。

#### 正确做法：用 uart_wait_tx_done + 精确 turn-around 替代

```c
// 替代 vTaskDelay(delay_ms) 的正确方案：
// 1. 等待 TX 完成（硬件层面）
uart_wait_tx_done(ctx->cfg.uart.port, pdMS_TO_TICKS(100));

// 2. 等待精确的 turn-around time（3.5 char @ current baud）
uint32_t turnaround_us = (38500000UL / ctx->cfg.uart.baud);  // 微秒
esp_timer_spin_wait(turnaround_us);  // 忙等，精确但短（<10ms）
```

**为什么用 esp_timer_spin_wait 而非 vTaskDelay？**
- turn-around time 在 4800 baud 时仅 8ms，在 9600+ 时 <5ms
- `vTaskDelay` 的最小粒度是 1 tick (10ms @100Hz)，无法精确表达 <10ms 的等待
- 忙等 8ms 在优先级 7 的任务中是可接受的——不会影响 rx_task (prio 8)
- 如果拆分 cmd_task，这个忙等只阻塞 UART cmd_task，不影响 SPI/I2C

### 2.2 拆分 cmd_task：3 个 per-bus-type vs per-channel vs 保持单 task

**结论：推荐 per-bus-type 拆分（方案 A2），但需调整优先级。**

#### 方案对比

| 方案 | 任务数 | 额外栈 | 调度复杂度 | 并行度 | 推荐度 |
|------|--------|--------|-----------|--------|--------|
| 单 task (现状) | 1 | 0 | 最低 | 0 | ❌ |
| per-bus-type (A2) | 3 | +8KB | 低 | UART↔SPI↔I2C 完全并行 | ✅ |
| per-channel | 5-8 | +16-28KB | 中 | 同类型总线也并行 | ⚠️ 过度 |

#### per-bus-type 拆分的优势

1. **天然隔离**: UART/SPI/I2C 的操作模型完全不同（UART=fire-and-forget, SPI/I2C=transactional），拆分后每个任务逻辑清晰
2. **UART delay 不影响 SPI/I2C**: 这是最大的收益
3. **内存开销可控**: 3 × 4096 = 12KB 栈，ESP32-C6 512KB SRAM 完全可承受
4. **调度延迟**: FreeRTOS 在 ESP32-C6 上支持最多 32 个优先级，3 个同优先级任务用 Round-Robin 调度，每次时间片 1 tick (10ms)，对实时性影响可忽略

#### per-channel 拆分的问题

1. **C6 只有 2 个 UART、1 个 I2C、1 个 SPI**: 5 个 channel 中可能有 2 个共享同一个 UART 端口，per-channel 任务会导致同一端口的并发 TX，需要额外互斥
2. **内存浪费**: 5-8 个任务 × 4096 = 20-32KB 栈
3. **调度抖动**: 8 个同优先级任务的 Round-Robin 延迟 = 7 × 10ms = 70ms 最坏情况

#### 推荐的任务优先级设计

```
rx_task_uart    (prio 8) — 最高，不能丢数据
cmd_task_uart   (prio 7) — UART TX + turn-around wait
cmd_task_spi    (prio 6) — SPI transact (通常 <5ms)
cmd_task_i2c    (prio 6) — I2C transact (可能 100ms 超时)
scheduler_task  (prio 5) — 生产命令
```

**为什么 cmd_task_uart 优先级高于 cmd_task_spi/i2c？**
- UART 的 turn-around time 是协议硬约束，必须及时发送下一个请求
- SPI/I2C transact 是原子的，晚几毫秒不影响协议正确性
- rx_task 必须最高，否则 DMA ring buffer 可能溢出

### 2.3 FreeRTOS 任务数量增加的影响

**量化分析**:

| 指标 | 当前 (3 task) | 拆分后 (5 task) | 增量 |
|------|--------------|----------------|------|
| 栈内存 | 12KB | 20KB | +8KB (2.7% of 300KB heap) |
| TCB 开销 | ~240B | ~400B | +160B |
| 调度延迟 | 2 × 10ms | 4 × 10ms (同优先级) | +20ms worst case |
| 上下文切换 | ~3μs/次 | ~3μs/次 | 无变化 |

**结论**: 8KB 栈增量在 300KB 堆中占比 2.7%，完全可接受。调度延迟增加可通过优先级差异化缓解。

### 2.4 UART FIFO 中断模式 vs 软件 ring buffer

**结论：DMA 模式用硬件 ring buffer，FIFO 中断模式必须加软件 ring buffer。**

#### 当前代码分析

`bus_dma.c:395-410`:
```c
if (ctx->dma_enabled) {
    // DMA mode: TX buffer 1024, RX ring buffer 256
    r = uart_driver_install(ctx->cfg.uart.port, 1024, 256, 1024, NULL, 0);
    uart_set_rx_timeout(ctx->cfg.uart.port, 4);
} else {
    // Polled mode: TX buffer 256, NO RX ring buffer
    r = uart_driver_install(ctx->cfg.uart.port, 256, 0, 256, NULL, 0);
}
```

#### 问题

1. **C6 只有 1 个 UHCI**: 第二个 UART 必然降级为 polled 模式，rx_buffer_size=0
2. **Polled 模式下 rx_task 的 mutex timeout=0**: `bus_dma.c:1033` — `xSemaphoreTake(ctx->mutex, 0)`
3. **如果 cmd_task_uart 正在写（持有 mutex），rx_task 拿不到 mutex，直接返回 0** — 数据留在 FIFO 中
4. **FIFO 只有 128 字节**: @4800 baud，128 字节填满需要 128×11/4800 = 293ms。如果 cmd_task_uart 的 turn-around wait + 下一次 TX 超过 293ms，数据丢失

#### 解决方案

**方案 1（推荐）: 为 polled UART 添加软件 ring buffer**

```c
// bus_dma_ctx_t 中增加：
typedef struct {
    uint8_t  sw_rx_buf[256];     // 软件 ring buffer
    volatile uint16_t sw_rx_head;
    volatile uint16_t sw_rx_tail;
} uart_sw_ring_t;

// 在 bus_dma_ctx_t 的 uart union 中添加：
struct {
    uart_port_t port;
    uint32_t    baud;
    int         tx_pin;
    int         rx_pin;
    uart_sw_ring_t *sw_ring;  // NULL for DMA mode, non-NULL for polled
} uart;
```

**方案 2: 用 UART 事件中断替代轮询**

```c
// 为 polled UART 安装事件队列
r = uart_driver_install(ctx->cfg.uart.port, 256, 256, 256, &ctx->cfg.uart.event_queue, 0);
// rx_task 改为 xQueueReceive(event_queue, ...) 驱动
```

**方案 2 更优**：ESP-IDF 的 uart_driver_install 在 rx_buffer_size>0 时自动启用 RX-FIFO 满中断和 rx_timeout 中断，数据由 ISR 搬入 ring buffer，不依赖 rx_task 的轮询频率。

**但方案 2 有一个限制**: C6 的第二个 UART 没有 UHCI DMA，`uart_driver_install` 的 rx_buffer_size 参数在非 DMA 模式下仍然有效——它分配的是**软件 ring buffer**，由 UART RX-FIFO 中断填充。所以方案 2 实际上就是让 ESP-IDF 驱动做方案 1 的事情。

**最终推荐**: 统一使用 `uart_driver_install(port, 256, 256, ...)` — 即使没有 DMA，256 字节的软件 ring buffer 也足够防止数据丢失。

### 2.5 rx_task mutex timeout=0 是否应该改为短超时？

**结论：应该改为短超时，但更好的方案是消除 mutex 竞争。**

#### 当前问题

`bus_dma.c:1033`:
```c
if (xSemaphoreTake(ctx->mutex, 0) != pdTRUE)  /* don't wait */
    return 0;
```

如果 cmd_task_uart 正在执行 `uart_write_bytes` + `uart_wait_tx_done`（持有 mutex 最长 100ms），rx_task 拿不到 mutex 就直接放弃读取。在 polled 模式下（无 ring buffer），FIFO 中的数据可能溢出。

#### 修改建议

```c
// 短超时：给 cmd_task 一个释放 mutex 的机会
if (xSemaphoreTake(ctx->mutex, pdMS_TO_TICKS(5)) != pdTRUE) {
    // 5ms 内拿不到，记录警告但不阻塞
    return 0;
}
```

**但更根本的解决方案**: 拆分 UART 的 TX/RX mutex。UART 硬件是全双工的，TX 和 RX 不需要互斥。

```c
// bus_dma_ctx_t 中：
SemaphoreHandle_t tx_mutex;  // 保护 TX 路径
SemaphoreHandle_t rx_mutex;  // 保护 RX 路径（实际上 UART RX 不需要 mutex）
```

**UART RX 为什么不需要 mutex？**
- `uart_read_bytes` 是线程安全的（ESP-IDF 内部有 ring buffer 的并发保护）
- rx_task 是唯一的 RX 消费者
- 唯一的竞争场景是 `uart_driver_delete`，但那发生在 deinit 时，此时 rx_task 已暂停

**推荐**: 拆分 TX/RX mutex，rx_task 完全不需要获取 mutex。

### 2.6 是否应该引入 UART TX 完成回调替代 vTaskDelay？

**结论：应该引入，但不是用回调，而是用 uart_wait_tx_done + 信号量通知。**

#### 当前问题

`bus_dma.c:459`:
```c
uart_wait_tx_done(port, pdMS_TO_TICKS(100));
```

这是**阻塞等待**，在 cmd_task 中执行时，整个任务被阻塞。虽然 `uart_wait_tx_done` 内部是 task notification 实现（不消耗 CPU），但它仍然占用 cmd_task 的时间片。

#### 替代方案：TX 完成信号量

```c
// 在 bus_dma_ctx_t 中添加：
SemaphoreHandle_t tx_done_sem;

// uart_write 改为异步：
static esp_err_t uart_write_async(bus_dma_ctx_t *ctx, const uint8_t *data, size_t len)
{
    uart_write_bytes(ctx->cfg.uart.port, data, len);
    // 不等待 TX 完成，直接返回
    return ESP_OK;
}

// cmd_task_uart 的 CMD_SAMPLE 流程：
bus_dma_write(ctx, cmd.tx_data, cmd.tx_len);  // 异步 TX
// ... enqueue pending_cmd_t ...

// 等待 TX 完成 + turn-around time
xSemaphoreTake(ctx->tx_done_sem, pdMS_TO_TICKS(100));
uint32_t turnaround_us = (38500000UL / ctx->cfg.uart.baud);
if (turnaround_us > 1000) {
    vTaskDelay(pdMS_TO_TICKS(turnaround_us / 1000));
} else {
    esp_timer_spin_wait(turnaround_us);
}
```

**但这里有一个关键问题**: ESP-IDF 的 `uart_write_bytes` + `uart_wait_tx_done` 已经是最高效的方式。`uart_write_bytes` 只是把数据放入 TX ring buffer，`uart_wait_tx_done` 等待硬件 FIFO 排空。两者之间 cmd_task 可以做其他事情（比如 enqueue pending_cmd_t），但**不能发送下一个命令**（因为 Modbus turn-around time）。

**所以 TX 完成信号量的收益有限**——cmd_task_uart 在发送一个命令后，必须等待 turn-around time 才能发送下一个，无论用不用信号量。

**真正的收益来自拆分 cmd_task**: cmd_task_uart 等待 turn-around time 时，cmd_task_spi 和 cmd_task_i2c 可以独立工作。

---

## 三、最优方案：方案 A+（改进版方案 A）

### 3.1 方案概述

在方案 A 的基础上，做以下调整：

1. **A1 改进**: 不删除 delay_ms，而是替换为精确的 turn-around time 等待
2. **A2 保留**: 拆分为 3 个 per-bus-type cmd_task
3. **A3 保留**: scheduler 按 bus_type 分发
4. **A4 保留**: 增大 pending_queues 深度
5. **新增 A5**: 统一 UART rx_buffer_size=256（polled 模式也加 ring buffer）
6. **新增 A6**: 拆分 UART TX/RX mutex，rx_task 不需要获取 mutex
7. **新增 A7**: I2C transact 超时从 100ms 降为 50ms，失败快速返回
8. **新增 A8**: rx_task 只轮询 UART channel，跳过 SPI/I2C

### 3.2 具体代码修改

#### A1: 替换 delay_ms 为精确 turn-around time

```c
// bus_worker.c — cmd_task_uart 的 CMD_SAMPLE UART 路径

// 当前代码 (删除):
// if (cmd.delay_ms > 0) {
//     vTaskDelay(pdMS_TO_TICKS(cmd.delay_ms));
// }

// 替换为：
if (ctx->bus_type == BUS_TYPE_UART) {
    esp_err_t e = bus_dma_write(ctx, cmd.tx_data, cmd.tx_len);
    if (e == ESP_OK) {
        scheduler_notify_channel_success(cmd.channel_id);
        // enqueue pending_cmd_t ...
        
        // 精确 turn-around time: 3.5 char @ current baud
        uint32_t turnaround_us = 38500000UL / ctx->cfg.uart.baud;
        if (turnaround_us > 10000) {
            // >10ms: 用 vTaskDelay (节省 CPU)
            vTaskDelay(pdMS_TO_TICKS((turnaround_us + 999) / 1000));
        } else if (turnaround_us > 1000) {
            // 1-10ms: 用 esp_timer_spin_wait (精确)
            esp_timer_spin_wait(turnaround_us);
        }
        // <1ms: 不需要等待，uart_wait_tx_done 已经包含了传输时间
    }
}
```

**收益**: @4800 baud 从 75ms 降到 8ms，@9600 baud 从 75ms 降到 4ms。每周期节省 ~2s。

#### A2: 拆分 cmd_task

```c
// app_state.h — 增加多队列
typedef struct {
    // ... existing fields ...
    QueueHandle_t cmd_queue;          // 保留，用于 CMD_WRITE (WriteCommand)
    QueueHandle_t uart_cmd_queue;     // 新增：UART CMD_SAMPLE
    QueueHandle_t spi_cmd_queue;      // 新增：SPI CMD_SAMPLE
    QueueHandle_t i2c_cmd_queue;      // 新增：I2C CMD_SAMPLE
    // ...
} app_state_t;

// app_state.c — 初始化
s_app.cmd_queue      = xQueueCreate(CMD_QUEUE_DEPTH, sizeof(bus_cmd_t));
s_app.uart_cmd_queue = xQueueCreate(CMD_QUEUE_DEPTH, sizeof(bus_cmd_t));
s_app.spi_cmd_queue  = xQueueCreate(8, sizeof(bus_cmd_t));   // SPI 命令少
s_app.i2c_cmd_queue  = xQueueCreate(8, sizeof(bus_cmd_t));   // I2C 命令少
```

```c
// bus_worker.c — 三个独立任务

#define UART_CMD_PRIO  7
#define SPI_CMD_PRIO   6
#define I2C_CMD_PRIO   6
#define RX_PRIO        8
#define WORKER_STACK   4096

static void cmd_task_uart(void *pv)
{
    app_state_t *s = (app_state_t *)pv;
    bus_cmd_t cmd;
    while (1) {
        // 先检查 CMD_WRITE (来自 WriteCommand)
        if (xQueueReceive(s->cmd_queue, &cmd, 0) == pdTRUE) {
            if (cmd.bus_type == BUS_TYPE_UART) {
                process_uart_cmd(s, &cmd);
                continue;
            }
            // 非 UART 的 CMD_WRITE 放回对应队列
            // (或由 scheduler 直接分发，见 A3)
        }
        // 再检查 UART CMD_SAMPLE
        if (xQueueReceive(s->uart_cmd_queue, &cmd, pdMS_TO_TICKS(10)) == pdTRUE) {
            process_uart_cmd(s, &cmd);
        }
    }
}

static void cmd_task_spi(void *pv)
{
    app_state_t *s = (app_state_t *)pv;
    bus_cmd_t cmd;
    while (1) {
        if (xQueueReceive(s->spi_cmd_queue, &cmd, portMAX_DELAY) == pdTRUE) {
            process_spi_cmd(s, &cmd);
        }
    }
}

static void cmd_task_i2c(void *pv)
{
    app_state_t *s = (app_state_t *)pv;
    bus_cmd_t cmd;
    while (1) {
        if (xQueueReceive(s->i2c_cmd_queue, &cmd, portMAX_DELAY) == pdTRUE) {
            process_i2c_cmd(s, &cmd);
        }
    }
}
```

**注意**: CMD_WRITE (WriteCommand) 也需要按 bus_type 分发。最简单的做法是 `bus_manager_on_write_cmd` 直接根据 `find_bus_type` 结果发送到对应队列。

#### A3: scheduler 按 bus_type 分发

```c
// scheduler.c — schedule_v2_channel 和 schedule_v1_channel 中

// 当前代码:
// if (xQueueSend(s_cmd_queue, &bcmd, 0) != pdTRUE) { ... }

// 替换为:
QueueHandle_t target_q = s_cmd_queue;  // 默认
if (bcmd.type == CMD_SAMPLE) {
    switch (bcmd.bus_type) {
    case BUS_TYPE_UART: target_q = s_uart_cmd_queue; break;
    case BUS_TYPE_SPI:  target_q = s_spi_cmd_queue;  break;
    case BUS_TYPE_I2C:  target_q = s_i2c_cmd_queue;  break;
    }
}
if (xQueueSend(target_q, &bcmd, 0) != pdTRUE) { ... }
```

**scheduler 需要持有所有队列的句柄**。修改 `scheduler_start` 接口：

```c
// scheduler.h
typedef struct {
    QueueHandle_t cmd_queue;       // CMD_WRITE
    QueueHandle_t uart_cmd_queue;  // UART CMD_SAMPLE
    QueueHandle_t spi_cmd_queue;   // SPI CMD_SAMPLE
    QueueHandle_t i2c_cmd_queue;   // I2C CMD_SAMPLE
} scheduler_queues_t;

void scheduler_start(const scheduler_queues_t *queues);
void scheduler_resume(const scheduler_queues_t *queues);
```

#### A4: 增大 pending_queues 深度

```c
// app_state.c
#define PENDING_QUEUE_DEPTH  (MAX_EDGE_DEVICES_PER_CH * MAX_COMMANDS_PER_DEVICE + 2)  // = 17

for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
    s_app.pending_queues[i] = xQueueCreate(PENDING_QUEUE_DEPTH, sizeof(pending_cmd_t));
}
```

内存增量: 17 × 8 bytes × 8 slots = 1088 bytes（从 4 × 8 × 8 = 256 bytes），增加 832 bytes，可忽略。

#### A5: 统一 UART rx_buffer_size

```c
// bus_dma.c — uart_init()

// 当前:
if (ctx->dma_enabled) {
    r = uart_driver_install(ctx->cfg.uart.port, 1024, 256, 1024, NULL, 0);
} else {
    r = uart_driver_install(ctx->cfg.uart.port, 256, 0, 256, NULL, 0);  // 无 RX buffer!
}

// 修改为:
// 无论 DMA 与否，都分配 RX ring buffer
r = uart_driver_install(ctx->cfg.uart.port, 
                        256,    // TX buffer
                        256,    // RX ring buffer (软件 ring，ISR 填充)
                        0,      // 事件队列 (不用)
                        NULL, 0);
if (ctx->dma_enabled) {
    uart_set_rx_timeout(ctx->cfg.uart.port, 4);
}
```

**为什么 TX buffer 从 1024 降到 256？**
- cmd_task_uart 是唯一的 TX 生产者，不会并发写
- 256 字节足够容纳一个 Modbus RTU 帧（最大 256 字节）
- 节省 768 字节 × 2 UART = 1.5KB

#### A6: 拆分 UART TX/RX mutex

```c
// bus_dma.h — bus_dma_ctx_t
typedef struct {
    uint8_t  bus_type;
    bool     dma_enabled;
    bool     initialized;
    SemaphoreHandle_t tx_mutex;  // TX 路径互斥
    // RX 路径不需要 mutex — uart_read_bytes 内部线程安全
    // ...
} bus_dma_ctx_t;

// bus_dma.c — bus_dma_write
esp_err_t bus_dma_write(bus_dma_ctx_t *ctx, const uint8_t *data, size_t len)
{
    if (xSemaphoreTake(ctx->tx_mutex, pdMS_TO_TICKS(100)) != pdTRUE)
        return ESP_ERR_TIMEOUT;
    esp_err_t r = uart_write(ctx, data, len);
    xSemaphoreGive(ctx->tx_mutex);
    return r;
}

// bus_dma.c — bus_dma_read (不再需要 mutex!)
size_t bus_dma_read(bus_dma_ctx_t *ctx, uint8_t *buf, size_t buf_size)
{
    // 直接调用，无需 mutex
    return uart_read(ctx, buf, buf_size);
}
```

**收益**: rx_task 永远不会被 cmd_task 的 TX 操作阻塞，消除了 mutex 竞争导致的 RX 数据丢失风险。

#### A7: I2C transact 超时降低

```c
// bus_dma.c — i2c_transact()
// 当前: int tmo = 100;
// 修改为:
int tmo = 50;  // 50ms 超时，快速失败
```

**理由**: I2C 设备在 100kHz 下，一次 transact 通常 <5ms。50ms 已经是 10 倍余量。100ms 超时在设备故障时阻塞 cmd_task_i2c 太久。

#### A8: rx_task 只轮询 UART

```c
// bus_worker.c — rx_task (当前已经跳过非 UART，但可以更高效)

static void rx_task(void *pv)
{
    app_state_t *s = (app_state_t *)pv;
    uint8_t rx[256];
    
    while (1) {
        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            if (!s->bus_ctx[i].initialized) continue;
            if (s->bus_ctx[i].bus_type != BUS_TYPE_UART) continue;  // 已有，保留
            
            size_t n = bus_dma_read(&s->bus_ctx[i], rx, sizeof(rx));
            if (n > 0) {
                // ... 处理 RX 数据 ...
            }
        }
        vTaskDelay(pdMS_TO_TICKS(RX_POLL_MS));
    }
}
```

**进一步优化**: 如果有 UART 事件队列，可以改为事件驱动：

```c
// 优化版 rx_task (事件驱动)
static void rx_task(void *pv)
{
    app_state_t *s = (app_state_t *)pv;
    uint8_t rx[256];
    uart_event_t event;
    
    while (1) {
        // 等待任意 UART 的事件（需要合并多个事件队列或轮询）
        bool got_data = false;
        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            if (!s->bus_ctx[i].initialized) continue;
            if (s->bus_ctx[i].bus_type != BUS_TYPE_UART) continue;
            
            size_t n = bus_dma_read(&s->bus_ctx[i], rx, sizeof(rx));
            if (n > 0) {
                got_data = true;
                // ... 处理 RX ...
            }
        }
        // 有数据时立即再读（burst 模式），无数据时才 sleep
        if (!got_data) {
            vTaskDelay(pdMS_TO_TICKS(RX_POLL_MS));
        }
    }
}
```

---

## 四、方案选择与理由

### 4.1 最终推荐：方案 A+（改进版方案 A）

| 改动项 | 方案 A | 方案 B | 方案 C | 方案 A+ |
|--------|--------|--------|--------|---------|
| 消除/替换 delay_ms | ✅ 删除 | ✅ 删除 | ❌ 保留 | ✅ 精确替换 |
| 拆分 cmd_task | ✅ 3 task | ❌ | ✅ 3 task | ✅ 3 task |
| pending_queue 加深 | ✅ | ❌ | ❌ | ✅ |
| UART RX ring buffer | ❌ | ❌ | ❌ | ✅ |
| 拆分 TX/RX mutex | ❌ | ❌ | ❌ | ✅ |
| I2C 超时降低 | ❌ | ❌ | ❌ | ✅ |

### 4.2 为什么不选方案 B

方案 B 只删除 delay_ms，不拆分 cmd_task。问题：
1. **I2C 100ms 超时仍然阻塞所有总线**
2. **SPI transact 仍然被 UART 操作串行化**
3. **delay_ms 删除后，Modbus turn-around time 无保证** — 可能导致协议违规

### 4.3 为什么不选方案 C

方案 C 拆分 cmd_task 但保留 delay_ms。问题：
1. **UART 内部仍然浪费 75ms/命令** — @4800 baud 只需 8ms
2. **没有解决 UART RX 数据丢失风险**（polled 模式无 ring buffer）
3. **没有解决 mutex 竞争问题**

### 4.4 方案 A+ 的预期收益

| 指标 | 当前 | 方案 A+ | 改善 |
|------|------|---------|------|
| UART delay/命令 | 75ms | 8ms (@4800) | -89% |
| SPI/I2C 被 UART 阻塞 | 是 | 否 | 完全消除 |
| I2C 故障阻塞 | 100ms | 50ms | -50% |
| UART RX 丢数据风险 | 高 (polled) | 低 (ring buffer) | 消除 |
| rx_task mutex 竞争 | 有 | 无 (拆分 mutex) | 消除 |
| pending_queue 溢出 | 可能 (depth=4) | 不会 (depth=17) | 消除 |
| 额外内存 | 0 | +8KB 栈 + 1.5KB queue | 3.2% of heap |

---

## 五、实施优先级

### Phase 1（立即，低风险）
1. **A4**: pending_queue depth 4→17 — 1 行代码修改
2. **A5**: UART rx_buffer_size 统一为 256 — 2 行代码修改
3. **A7**: I2C 超时 100ms→50ms — 1 行代码修改

### Phase 2（核心，中风险）
4. **A6**: 拆分 TX/RX mutex — bus_dma.h + bus_dma.c 修改
5. **A2+A3**: 拆分 cmd_task + scheduler 分发 — bus_worker.c + scheduler.c 重构

### Phase 3（优化，低风险）
6. **A1**: 替换 delay_ms 为精确 turn-around time — bus_worker.c 修改
7. **A8**: rx_task burst 模式优化 — bus_worker.c 修改

### Phase 4（未来，需验证）
8. UART 事件驱动 rx_task（需要 ESP-IDF 事件队列集成）
9. LP_I2C 适配（如果需要第二条 I2C 总线）

---

## 六、风险与缓解

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| 精确 turn-around time 不够，设备响应异常 | 低 | 高 | 保留 delay_ms 作为可配置上限，turn-around time 取 max(3.5char, delay_ms) |
| 3 个 cmd_task 的队列分发逻辑出错 | 中 | 中 | 充分测试 WriteCommand + CMD_SAMPLE 混合场景 |
| 拆分 mutex 后 RX 路径出现新竞争 | 低 | 高 | uart_read_bytes 本身线程安全，只需确保 deinit 时暂停 rx_task |
| 内存不足（+10KB） | 极低 | 高 | C6 512KB SRAM，300KB 可用堆，10KB 占 3.3% |

---

## 七、关于 LP_I2C 的建议

**结论：不建议适配 LP_I2C，应在配置层面限制为 1 个 I2C 总线。**

理由：
1. LP_I2C 属于低功耗域，时钟源、API、时序特性与 HP I2C 完全不同
2. LP_I2C 主要用于 deep sleep 期间的传感器轮询，不适合实时采集
3. 适配 LP_I2C 需要新的 bus_dma_ctx_t 分支、新的 transact 实现、新的中断处理
4. 收益有限：C6 的典型场景是 1 个 I2C 总线挂多个设备（不同地址），不需要第二条总线

**建议**: 在 `hw_tables.c` 中 `HW_I2C_COUNT=1` 已经正确反映了这个约束。在 `bus_manager.c` 的 `i2c_init` 中增加显式检查：

```c
// bus_dma.c — i2c_init() 开头增加
if (i2c_find_bus(sda, scl) == NULL && i2c_alloc_bus() == NULL) {
    ESP_LOGE(TAG, "C6 only supports 1 I2C bus — cannot create second bus on SDA=%d SCL=%d", sda, scl);
    return ESP_ERR_NOT_SUPPORTED;
}
```

---

## 八、总结

**最优方案是 A+**，核心改动是：

1. **拆分 cmd_task 为 3 个 per-bus-type 任务** — 消除跨总线阻塞
2. **替换 delay_ms 为精确 Modbus turn-around time** — 从 75ms 降到 8ms
3. **统一 UART RX ring buffer** — 消除 polled 模式数据丢失风险
4. **拆分 TX/RX mutex** — 消除 rx_task 被 TX 阻塞的问题
5. **pending_queue 加深到 17** — 消除多设备场景下的溢出

这些改动的总内存增量约 10KB（3.3% of heap），总代码修改量约 200 行，风险可控，收益显著。
