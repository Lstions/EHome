# ESP32-C6 多总线并行工作优化方案 (Final v2)

**版本**: v2.0 — 三方评审修正版  
**日期**: 2026-06-23  
**评审**: 系统架构师 ✓ | 固件开发员 ✓ | 产品经理 ✓

---

## v1 → v2 修正记录

| # | 修正项 | 来源 | 原方案 | 修正后 |
|---|--------|------|--------|--------|
| 1 | esp_timer_spin_wait 不安全 | 开发员 | 用 esp_timer_spin_wait | 改用 ets_delay_us |
| 2 | 任务优先级调整 | 架构师 | rx=8, uart=7, spi/i2c=6 | rx=7, 全部 cmd_task=6 |
| 3 | 修改7已存在 | 开发员 | rx_task 只轮询 UART | 移除此项（代码已有） |
| 4 | bus_cmd_t 已有 bus_type | 开发员 | "需要新增" | 无需新增，已有字段 |
| 5 | delay_ms 向后兼容 | 产品经理 | 固件忽略 delay_ms | 保留字段但固件自动计算覆盖 |
| 6 | I2C 校验提前 | 产品经理 | Phase 4 | 提前到 Phase 1a |
| 7 | pending 深度调整 | 架构师 | 17 | 10（覆盖 95% 场景） |
| 8 | 补充 WriteCommand 分发 | 架构师+开发员 | 未提及 | 明确路径 |
| 9 | scheduler API 签名变更 | 开发员 | 未提及 | 补充说明 |
| 10 | cmd_task 命令间等待 | 架构师 | 仅靠 turn-around | turn-around + response_sem（可选） |

---

## 一、修改清单（7 项，原 8 项减去已实现的修改 7）

### 修改 1：拆分 cmd_task 为 per-UART-port 四任务

**目标**：UART0/UART1/SPI/I2C 完全并行，互不阻塞。

**任务模型**（优先级已调整）：
```
rx_task        (prio 7, 4096 stack) — UART RX 轮询
cmd_task_uart0 (prio 6, 4096 stack) — UART0 TX + turn-around wait
cmd_task_uart1 (prio 6, 4096 stack) — UART1 TX + turn-around wait
cmd_task_spi   (prio 6, 3072 stack) — SPI transact
cmd_task_i2c   (prio 6, 3072 stack) — I2C transact
scheduler_task (prio 5, 4096 stack) — 生产命令到各队列
```

**优先级调整说明**（架构师评审）：
- rx_task 降为 7：避免与 FreeRTOS 中断子系统竞争，7 仍高于所有 cmd_task
- cmd_task 全部 6：同优先级公平调度（Round-Robin），避免 UART 抢占 SPI/I2C

**队列分配**：
```c
// app_state.h 新增
QueueHandle_t uart0_cmd_queue;  // depth=16, UART0 CMD_SAMPLE + CMD_WRITE
QueueHandle_t uart1_cmd_queue;  // depth=16, UART1 CMD_SAMPLE + CMD_WRITE
QueueHandle_t spi_cmd_queue;    // depth=8,  SPI CMD_SAMPLE + CMD_WRITE
QueueHandle_t i2c_cmd_queue;    // depth=8,  I2C CMD_SAMPLE + CMD_WRITE
// 保留 cmd_queue 作为 WriteCommand 的通用入口（兼容期过渡）
```

**WriteCommand 分发路径**（开发员评审补充）：
```
MQTT/Alexa WriteCommand
  → bus_manager_on_write_cmd() 写入 cmd_queue
  → 新增 dispatcher_task (prio 5) 消费 cmd_queue
  → 按 cmd.bus_type + cmd.channel_id 查 port
  → 转发到 uart0_cmd_queue / uart1_cmd_queue / spi_cmd_queue / i2c_cmd_queue
```

或者更简单：直接在 `bus_manager_on_write_cmd` 中按 bus_type + port 写入对应队列，不经过 cmd_queue。这样 cmd_queue 可以在稳定后废弃。

**scheduler API 变更**（开发员评审补充）：
```c
// 当前：scheduler_start(QueueHandle_t cmd_queue)
// 改为：scheduler_start(const app_state_t *s)
// 内部从 s 获取各队列句柄
// 影响：app_callbacks.c 中 scheduler_start/resume 调用需适配
```

**bus_cmd_t 字段**（开发员评审确认）：
- `bus_type` 字段已存在（cmd_queue.h:42），无需新增
- 建议新增 `uart_port_t uart_port` 字段（1 byte），在 scheduler 构造命令时从 config_channel_t 填入，避免分发时再查 bus_manager_find_ctx

**bus_worker_suspend/resume**：
拆分后 5 个任务需全部管理，但 bus_worker.h 接口不变（内部化 TaskHandle）。

### 修改 2：替换 delay_ms 为精确 Modbus turn-around time

**目标**：从 50-100ms 降到协议最小要求，同时保证 Modbus RTU 合规。

**关键修正**（开发员评审）：`esp_timer_spin_wait` 不能在 FreeRTOS 任务中使用（会关中断），改用 `ets_delay_us`。

```c
// cmd_task_uart0/1 的 CMD_SAMPLE UART 路径
// 删除原 vTaskDelay(delay_ms)
// 替换为精确 turn-around time：
if (ctx->bus_type == BUS_TYPE_UART) {
    uint32_t turnaround_us = 38500000UL / ctx->cfg.uart.baud;
    if (turnaround_us > 10000) {
        // >10ms: 用 vTaskDelay（让出 CPU，节省功耗）
        vTaskDelay(pdMS_TO_TICKS((turnaround_us + 999) / 1000));
    } else if (turnaround_us > 1000) {
        // 1-10ms: 用 ets_delay_us（不关中断的忙等，ROM 函数）
        ets_delay_us(turnaround_us);
    }
    // <1ms (@115200+): 不需要额外等待
}
```

**ets_delay_us vs esp_timer_spin_wait**：
- `ets_delay_us`：ROM 函数，NOP 循环忙等，**不关中断**，FreeRTOS 调度器正常工作
- `esp_timer_spin_wait`：关闭中断后忙等，**不适合任务上下文**

**向后兼容**（产品经理评审，关键）：
- ConfigTemplate.delay_ms 字段**不删除**，后端继续下发
- 新固件行为：忽略 delay_ms 值，使用基于波特率自动计算的 turn-around time
- 旧固件行为：继续使用 delay_ms 值（75ms）
- 后端无需修改，sender.go 编码逻辑不变
- 升级后采样效率自动提升，无需用户操作

| 波特率 | 当前 delay_ms | 新固件自动值 | 节省 |
|--------|-------------|------------|------|
| 4800 | 50-100ms | 8ms | 84-92% |
| 9600 | 50-100ms | 4ms | 92-96% |
| 115200 | 50-100ms | 0ms | 100% |

**命令间等待机制**（架构师评审补充）：

Modbus RTU 协议要求主站收到上一个响应后才发下一个请求。当前方案仅靠 turn-around time 间隔，不等响应到达。这在单设备场景下没问题（响应在 turn-around 时间内到达），但在同端口多设备快速轮询时可能出问题。

**方案**：在 cmd_task_uart 中增加可选的 response_sem 等待：

```c
// cmd_task_uart 发送命令后：
// 1. 等精确 turn-around time
ets_delay_us(turnaround_us);
// 2. 可选：等 rx_task 通知响应已收到（最多 200ms）
if (xSemaphoreTake(s->response_sem[slot], pdMS_TO_TICKS(200)) != pdTRUE) {
    // 超时：设备无响应，跳过等待，处理下一个命令
    ESP_LOGW(TAG, "UART%d response timeout, slot %d", port, slot);
}
```

**这个 response_sem 是可选的**，初始版本可以先不加，仅用 turn-around time。如果实测出现响应归属错位，再加此机制。rx_task 在消费 pending_cmd_t 后 give semaphore。

### 修改 3：统一 UART rx_buffer_size=256

**目标**：第二个 UART（polled/FIFO 模式）也有软件 ring buffer。

**修改文件**：bus_dma.c

```c
// uart_init() 中，仅修改 polled 模式分支
// 当前：
r = uart_driver_install(ctx->cfg.uart.port, 256, 0, 256, NULL, 0);
//             RX buffer=0 ← 问题！
// 改为：
r = uart_driver_install(ctx->cfg.uart.port, 256, 256, 0, NULL, 0);
//             RX ring buffer=256 ↑  event queue=0 ↓（节省内存）
// DMA 模式分支不变
```

**注意**（开发员评审）：只改 else 分支，DMA 模式不变。ESP-IDF 在非 DMA 模式下 rx_buffer_size > 0 会分配软件 ring buffer，由 UART RX-FIFO 中断填充。

### 修改 4：拆分 UART TX/RX mutex

**目标**：rx_task 读取 UART RX 不被 TX 操作阻塞。

```c
// bus_dma_ctx_t 中
// 当前：SemaphoreHandle_t mutex;  // TX/RX 共用
// 改为：SemaphoreHandle_t tx_mutex;  // 保护 TX + SPI/I2C transact
// RX 不需要 mutex：uart_read_bytes 线程安全（ESP-IDF 内部有 per-port spinlock），rx_task 是唯一消费者
```

**SPI/I2C 并发说明**（架构师评审补充）：
- `bus_dma_transact()`（SPI/I2C）获取 tx_mutex — 串行保护 TX+RX 原子操作
- SPI/I2C 的 RX 是 transact 的一部分，由 tx_mutex 串行保护，无需独立 mutex

### 修改 5：scheduler 按 bus_type + port 分发命令

**目标**：scheduler 和 WriteCommand 投递命令时直接分发到对应队列。

**修改文件**：scheduler.c, bus_manager.c

```c
// 分发函数（scheduler.c 和 bus_manager.c 共用逻辑）
static QueueHandle_t dispatch_cmd_queue(const app_state_t *s, const bus_cmd_t *bcmd)
{
    bus_dma_ctx_t *ctx = bus_manager_find_ctx(s, bcmd->channel_id);
    if (!ctx) return s->uart0_cmd_queue;  // fallback
    switch (ctx->bus_type) {
    case BUS_TYPE_UART:
        return (ctx->cfg.uart.port == UART_NUM_0) ? s->uart0_cmd_queue : s->uart1_cmd_queue;
    case BUS_TYPE_SPI:  return s->spi_cmd_queue;
    case BUS_TYPE_I2C:  return s->i2c_cmd_queue;
    default:            return s->uart0_cmd_queue;
    }
}
```

**bus_cmd_t 已有 bus_type 字段**（开发员评审确认），无需新增。建议新增 `uart_port` 字段避免每次分发都查 bus_manager_find_ctx。

### 修改 6：增大 pending_queues 深度

**目标**：支持多 edge_device 多命令场景。

```c
// 从 4 增大到 10（架构师评审：17 过大，10 覆盖 95% 场景）
#define PENDING_QUEUE_DEPTH 10
s_app.pending_queues[i] = xQueueCreate(PENDING_QUEUE_DEPTH, sizeof(pending_cmd_t));
```

内存增量：10 × 9 × 8 = 720 bytes（vs 当前 288 bytes，增加 432 bytes）

### 修改 7：I2C 配置校验 + 超时优化

**目标**：运行时校验 I2C 总线数量限制，缩短 I2C 超时。

```c
// bus_dma.c i2c_init() 中
// 新增：校验 C6 I2C 总线数量限制
int active_count = 0;
for (int i = 0; i < MAX_I2C_BUSES; i++)
    if (s_i2c_buses[i].bus_handle != NULL) active_count++;
if (active_count >= HW_I2C_COUNT) {
    ESP_LOGE(TAG, "C6 only supports %d I2C bus(es), cannot create more", HW_I2C_COUNT);
    return ESP_ERR_NOT_SUPPORTED;
}

// I2C transact 超时从 100ms 降为 50ms（单操作）
```

**产品文档要求**（产品经理评审）：C6 的总线数量限制必须在产品规格书和用户手册中明确标注。

---

## 二、修改前后对比

### 时间预算（每 60s 采样周期，最坏 15 命令/channel）

| 总线 | 当前耗时 | 优化后耗时 | 改善 |
|------|---------|-----------|------|
| SPI2 @10MHz | 7.5ms（串行等待） | 7.5ms（与 UART 并行） | 并行执行 |
| I2C0 @100kHz | 30ms（串行等待） | 30ms（与 UART 并行） | 并行执行 |
| UART1 @9600 | 870ms（串行+75ms delay） | 60ms（精确 4ms turn-around） | 93% ↓ |
| UART0 @4800 | 1,755ms（串行+75ms delay） | 120ms（精确 8ms turn-around） | 93% ↓ |
| **总 wall time** | **2,693ms**（串行累加） | **~120ms**（并行取最长） | **95% ↓** |

### 内存预算

| 项目 | 当前 | 优化后 | 增量 |
|------|------|--------|------|
| cmd_task 栈 | 4,096 B × 1 | 4,096×2 + 3,072×2 = 14,336 B | +10,240 B |
| cmd_queue | 158 × 16 = 2,528 B × 1 | 158 × (16+16+8+8) = 7,584 B | +5,056 B |
| pending_queues | 9 × 4 × 8 = 288 B | 9 × 10 × 8 = 720 B | +432 B |
| UART ring buffer | 0 | 256 B | +256 B |
| TCB + 开销 | ~120 B | ~360 B | +240 B |
| **总增量** | | | **~16.2KB** |

16.2KB / 300KB heap = 5.4%，可接受。

---

## 三、实施计划（调整后）

| 阶段 | 修改项 | 预估工时 | 说明 |
|------|--------|---------|------|
| **Phase 1a** | 修改 3 (ring buffer) + 修改 6 (pending depth) + 修改 7 (I2C 校验) | 1h | 数据安全基线，低风险 |
| **Phase 1b** | 修改 1 (拆分 cmd_task) + 修改 5 (scheduler 分发) | 6-8h | 核心重构，含 scheduler API 变更、bus_cmd_t uart_port 字段、WriteCommand 分发 |
| **Phase 2** | 修改 2 (精确 turn-around) + 修改 4 (mutex 拆分) | 3-4h | 依赖 Phase 1b 的新任务模型 |
| **总计** | | **10-13h** | |

Phase 1a 可独立合入，Phase 1b 是核心变更需完整 E2E 测试后合入。

---

## 四、风险与缓解

| 风险 | 概率 | 缓解 |
|------|------|------|
| 拆分 cmd_task 后命令分发到错误队列 | 中 | 运行时断言校验 bus_type 匹配；每个 cmd_task 遇到不匹配的 bus_type 报错跳过 |
| ets_delay_us 忙等影响系统性能 | 低 | 仅 1-8ms 范围内使用，CPU 占用 <0.01% |
| UART0 turn-around 期间 UART1 也被影响 | 无 | per-UART-port 拆分彻底消除此问题 |
| I2C 超时 50ms 对某些设备不够 | 低 | 初期可保持 100ms，实测后调整；未来可改为 per-device 可配置 |
| OTA 升级后行为变化（delay_ms 忽略） | 中 | 纯性能改善（更快采样），不改变功能正确性；灰度发布：先推 1 个节点验证 |

---

## 五、不做什么

| 项目 | 原因 |
|------|------|
| LP_I2C 适配 | 低功耗域驱动 API 不兼容，配置层面限制即可 |
| pending_queue 超时清理 | 增加复杂度，设备无响应应由后端超时处理 |
| UART TX 完成回调/信号量 | 收益有限——cmd_task_uart 发送后必须等 turn-around time |
| 总线注册表模式 | 当前 4 种总线固定，动态注册增加复杂度，留待未来扩展 |
| HAL 抽象层 | 当前仅支持 C6，迁移到 S3 需重新评估 DMA 路径，不在本次范围 |
| response_sem（命令间响应等待） | 初版不加，仅用 turn-around time。如实测出现响应归属错位再加 |

---

## 六、未来扩展

| 方向 | 说明 |
|------|------|
| CAN 总线 | 新增 BUS_TYPE_CAN + cmd_task_can + can_cmd_queue，switch-case 扩展 |
| ESP32-S3 | 3 个 UART、2 个 I2C、2 个 SPI、2 个 UHCI — per-UART-port 拆分仍适用，DMA 全部可用 |
| 总线注册表 | 从 switch-case 硬编码改为 bus_worker_t 注册表数组，运行时动态构建 |
| per-device 可配置超时 | I2C/SPI 超时从全局常量改为 ConfigTemplate 字段 |
