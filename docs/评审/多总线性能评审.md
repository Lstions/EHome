# ESP32-C6 多总线并行优化方案 — 性能工程师评审报告

> 评审人: 嵌入式性能工程师 | 日期: 2026-06-23 | 版本: v1

---

## 1. 硬件资源基线 (ESP32-C6)

| 资源 | 数值 | 来源 |
|------|------|------|
| SRAM 总量 | 512 KB | ESP32-C6 datasheet |
| 可用 RAM (app) | ~160 KB | 扣除 IDF 系统预留 + WiFi/BT stack ~320KB |
| PSRAM | 无 | sdkconfig 未启用 |
| CPU | 160 MHz, 单核 | CONFIG_ESP_DEFAULT_CPU_FREQ_MHZ=160 |
| GDMA 通道 | 3 对 (6 物理: 3TX+3RX) | hw_tables.c |
| UART | 2 HP (UART0=boot, UART1=data) | hw_tables.c |
| I2C | 1 HP (I2C0, **无DMA** flags=0x00) | hw_tables.c |
| SPI | 1 (SPI2, DMA) | hw_tables.c |
| UHCI | 1 (UART0/UART1 共享) | TRM Ch.4 pp.122-123 |

### DMA 通道分配现状 (hw_tables.c)

| 通道 | 兼容总线 | 当前绑定 |
|------|---------|---------|
| CH0 | SPI only (0x04) | SPI2 |
| CH1 | UART\|SPI (0x05) | 第一个 UART (DMA 模式) |
| CH2 | SPI only (0x04) | 空闲 |

> **关键约束**: 同时刻只有 1 个 UART 可用 DMA。第二个 UART 自动降级为 FIFO 中断模式。

---

## 2. 当前内存预算审计

### 2.1 已有任务栈开销 (从源码提取)

| 任务 | 栈大小 | 来源行 |
|------|--------|--------|
| main_task | 16,384 B | sdkconfig |
| cmd_task | 4,096 B | bus_worker.c:37 WORKER_STACK |
| rx_task | 4,096 B | bus_worker.c:37 WORKER_STACK |
| scheduler | 4,096 B | scheduler.h:30 SCHED_TASK_STACK |
| status_task | 3,072 B | main.c:258 |
| sync_task | 3,072 B | main.c:259 |
| hello_task | 4,096 B | hello_handshake.c:124 |
| mqtt_start | 8,192 B | app_callbacks.c:296 |
| tcp_accept | 4,096 B | ehome_tcp.c:211 |
| tcp_client | 4,096 B | ehome_tcp.c:22 TCP_CLIENT_STACK_SIZE |
| rgb_led | 2,048 B | rgb_led.c:325 |
| factory_reset | 3,072 B | factory_reset.c:103 |
| ota_task | 16,384 B | ota.c:612 (临时任务) |
| uart0_dl | 2,048 B | bus_dma.c:161 (条件创建) |
| FreeRTOS timer | 2,048 B | sdkconfig |
| FreeRTOS idle | 1,536 B | sdkconfig |
| **合计** | **~78,800 B** | |

### 2.2 关键数据结构开销

| 数据结构 | 大小 | 说明 |
|----------|------|------|
| bus_ctx[8] | 8 × ~80B = ~640B | bus_dma_ctx_t 含 union |
| pending_queues[8] | 8 × (96B + 4×9B) = ~1,056B | depth=4, item=pending_cmd_t 9B |
| cmd_queue | ~2,464B | depth=16, item=bus_cmd_t ~148B |
| dma_pool_t | ~360B | 3 channels + mutex |
| UART driver (DMA) | 2,304B/port | RX buf 1024 + TX buf 256 + event queue 1024 |
| UART driver (polled) | 512B/port | 无 ring buffer |
| SPI DMA driver | ~1,200B | DMA descriptor ring |
| I2C driver | ~400B | new master API |

**当前总内存估算**: ~78.8KB 栈 + ~8KB 数据结构 + ~5KB 驱动缓冲 + IDF/WiFi ~320KB ≈ **~412KB**
**剩余可用**: 512KB - 412KB ≈ **~100KB** (含堆碎片)

---

## 3. 三个方案的定量评审

### 3.1 方案 A: 消除 delay_ms + 拆分 cmd_task (激进)

#### 3.1.1 新增任务栈 — 精确需求分析

原方案文档假设每个 cmd_task 均 4KB，这是过度的。分析各任务的实际栈需求：

**cmd_task_uart**:
- 调用链: cmd_task → bus_dma_write() → uart_write_bytes() + uart_wait_tx_done()
- 栈上局部变量: bus_cmd_t cmd (~148B), pending_cmd_t pcmd (9B), 循环变量 (~20B)
- **不需要**: rx[256] buffer (那是 rx_task 的事), bus_dma_transact (SPI/I2C 的事)
- **实际需求**: ~1,536B

**cmd_task_spi**:
- 调用链: cmd_task → bus_dma_transact() → spi_transact() → spi_device_transmit()
- 栈上局部变量: bus_cmd_t cmd (~148B), uint8_t rx[256], spi_transaction_t (~40B)
- **实际需求**: ~2,048B (含 rx[256])

**cmd_task_i2c**:
- 调用链: cmd_task → bus_dma_transact() → i2c_transact()
- 栈上局部变量: 同上，含 rx[256]
- **实际需求**: ~2,048B (含 rx[256])

| 任务 | 方案文档假设 | 精确需求 | 节省 |
|------|------------|---------|------|
| cmd_task_uart | 4,096 B | 2,048 B | **-2,048 B** |
| cmd_task_spi | 4,096 B | 2,048 B | **-2,048 B** |
| cmd_task_i2c | 4,096 B | 2,048 B | **-2,048 B** |

> **结论**: SPI/I2C cmd_task 可以缩减到 2KB。UART cmd_task 更可减至 1.5KB。
> 3 × 2KB = 6KB，对比原 1 × 4KB = 净增仅 2KB（而非文档假设的 8KB）。

#### 3.1.2 Queue 开销优化

原 cmd_queue depth=16, item=bus_cmd_t (148B) = 2,464B

拆分后各队列按需深度:

| Queue | 深度 | 理由 | 大小 |
|-------|------|------|------|
| uart_cmd_queue | 16 | UART 设备多，命令密集 | ~2,464 B |
| spi_cmd_queue | 4 | SPI 设备少，transact 快 | ~688 B |
| i2c_cmd_queue | 4 | I2C 设备少，transact 慢 | ~688 B |
| **合计** | — | — | **3,840 B** |

净增量: 3,840 - 2,464 = **+1,376 B**

#### 3.1.3 pending_queues 深度增大

从 depth=4 → 17 (5 devices × 3 commands + 2 margin):

- 每队列增量: (17-4) × 9B + ~30B FreeRTOS overhead = ~147B
- 8 channels × 147B = **~1,176 B** (与方案文档估算 1,224B 基本一致)
- **可接受**: 1.2KB / 160KB = 0.7%

#### 3.1.4 方案 A 总内存增量

| 组件 | 变更 | 增量 |
|------|------|------|
| 删除原 cmd_task 栈 | -4,096 B | -4,096 |
| 新增 cmd_task_uart 栈 | +2,048 B | +2,048 |
| 新增 cmd_task_spi 栈 | +2,048 B | +2,048 |
| 新增 cmd_task_i2c 栈 | +2,048 B | +2,048 |
| 删除原 cmd_queue | -2,464 B | -2,464 |
| 新增 3 个 cmd_queue | +3,840 B | +3,840 |
| pending_queues 增大 | — | +1,176 |
| UART ring buffer (降级) | — | +256 |
| **净增量** | — | **+4,856 B ≈ 4.7 KB** |

> 占 160KB 可用 RAM 的 **3.0%** — **完全可接受**。

### 3.2 方案 B: 仅消除 delay_ms (保守)

- 内存增量: **0 B**
- CPU 收益: 节省 2,250ms wall time/60s 周期 = 3.75%
- **关键缺陷**: I2C 100ms 超时仍阻塞所有总线; pending_queues 仍可能溢出

### 3.3 方案 C: 拆分 cmd_task 但保留 delay_ms (折中)

- 内存增量: 与方案 A 相同 (~4.7 KB)
- 收益: SPI/I2C 不受 UART 阻塞 (故障隔离)
- **关键缺陷**: UART 多设备时仍串行 delay (2,250ms/周期浪费在 uart cmd_task 内部)

### 方案对比矩阵

| 维度 | 方案 A | 方案 B | 方案 C |
|------|--------|--------|--------|
| 内存增量 | +4.7 KB | 0 | +4.7 KB |
| wall time 节省 | 2,250 ms/周期 | 2,250 ms/周期 | 0 (UART 内部) |
| 故障隔离 | ✅ 完全隔离 | ❌ 无 | ⚠️ 跨总线隔离 |
| 代码改动量 | ~150 行 | ~5 行 | ~130 行 |
| UART 多设备效率 | ✅ 最优 | ✅ 优 | ❌ 仍串行 delay |
| SPI/I2C 响应延迟 | ✅ 独立 | ❌ 受 UART 影响 | ✅ 独立 |

---

## 4. DMA 与 UART 降级分析

### 4.1 降级 UART 的 ring buffer 需求

**现状**: 降级 UART 使用 `uart_driver_install(port, 256, 0, 256, NULL, 0)` — 无 RX ring buffer

**风险分析**:
- C6 UART FIFO 深度 = 128 bytes
- Modbus RTU @9600bps 最大帧 256 bytes; @19200bps 128 bytes
- rx_task 5ms 轮询间隔: @9600bps 可累积 ~6 bytes, @19200bps ~12 bytes
- FIFO 溢出条件: 128 bytes / (baud/10) < 5ms → 仅 @256kbps+ 才溢出
- **正常场景下不溢出**, 但以下情况有风险:
  - rx_task 被高优先级任务抢占 >5ms
  - 连续大帧无间隔

**建议**:
```c
// 降级 UART: 加 256B RX ring buffer 作为安全网
uart_driver_install(port, 256, 256, 0, NULL, 0);
```
- 额外内存: **+256 B**
- 同时将 rx_task mutex timeout 从 0 改为 `pdMS_TO_TICKS(2)`

### 4.2 I2C 无 DMA 的影响

C6 I2C0 flags=0x00 — 硬件限制，I2C 控制器不连接 GDMA。

**中断频率估算** (I2C @100kHz):
- 每个 byte 传输触发 1 次 ACK 中断
- 典型传感器读 (write 1 reg + read 2 bytes): 4 次中断
- 中断处理时间: ~2-5 μs/次
- **CPU 占用**: ~20 μs/transact × 典型 1 次/s = **~0.002%** — 完全可忽略

**真正风险**: I2C transact 的 100ms 超时 (bus_dma.c:919)
- 设备无应答 → 阻塞 100ms
- 方案 A 中只阻塞 i2c_cmd_task → **隔离成功**
- 建议增加 I2C 错误退避: 连续失败 3 次后跳过该设备 30s

---

## 5. rx_task 优化分析

**当前**: 每次循环遍历 8 slot, 只有 2 个 UART 有效, 75% 无效轮询

**定量**:
- 无效轮询开销: 6 × (条件判断 + continue) ≈ 30 条指令/次 × 6 = 180 条
- @160MHz: ~1.1 μs/循环
- 5ms 间隔: 200 次/s × 1.1 μs = **0.22 ms/s = 0.022% CPU**

**结论**: 收益极小, 不值得单独改。如要做, 用 UART channel bitmap 而非遍历:

```c
// 优化方案: 维护 UART slot bitmap
static uint8_t s_uart_slots = 0;  // bit i = slot i is UART
// rx_task 只遍历 set bits
```

---

## 6. LP_I2C 适配分析

| 维度 | 评估 |
|------|------|
| 适配工作量 | +200 行代码 + 测试 (~3 人天) |
| LP_I2C 速率限制 | 通常 ≤100kHz, LP 域调度延迟不可控 |
| API 兼容性 | lp_i2c_master_* 与 i2c_master_* 完全不兼容 |
| 使用场景 | 低频传感器 (BME280 等), 与实时采样矛盾 |
| **建议** | **配置层面限制为 1 个 I2C 总线** |

理由:
1. C6 定位是低成本采集器, 1 条 I2C 已覆盖绝大多数场景
2. LP_I2C 的非确定性延迟与实时采样需求矛盾
3. 投入产出比低: 3 人天工作量 vs 极少用户需要第二条 I2C
4. 如未来需要, 可作为独立 feature branch 开发

---

## 7. 最优方案: 方案 A (带优化调整)

### 选择理由

1. **消除 delay_ms 是零风险高收益**: Modbus RTU turn-around time 由调度器间隔保证 (3.5 字符时间 @9600bps = 3.65ms, 远小于调度间隔), cmd_task 不需要 sleep
2. **拆分 cmd_task 是架构正确性**: C6 有 3 种不同事务模型的总线, 串行化是架构缺陷
3. **故障隔离是刚需**: I2C 100ms 超时是真实风险, 必须隔离
4. **内存代价可控**: 净增 4.7KB, 占可用 RAM 3.0%

### 优化调整清单

1. **栈大小精确分配**: uart=2KB, spi=2KB, i2c=2KB (非文档假设的各 4KB)
2. **Queue 按需深度**: uart=16, spi=4, i2c=4 (节省 3.5KB vs 全 16)
3. **降级 UART 加 ring buffer**: 256B + mutex timeout=2ms
4. **pending_queues 用宏定义**: `#define PENDING_QUEUE_DEPTH (MAX_EDGE_DEVICES_PER_CH * MAX_COMMANDS_PER_DEVICE + 2)`
5. **I2C 错误退避**: 连续失败 3 次后跳过 30s (防止单设备拖垮 i2c_cmd_task)
6. **rx_task 优化**: 低优先级, 可后续迭代

### 最终内存预算表

| 组件 | 当前 | 方案A优化后 | 增量 |
|------|------|------------|------|
| cmd_task_uart 栈 | 4,096 (共享) | 2,048 | — |
| cmd_task_spi 栈 | — | 2,048 | +2,048 |
| cmd_task_i2c 栈 | — | 2,048 | +2,048 |
| uart_cmd_queue | 0 | 2,464 | +2,464 |
| spi_cmd_queue | 0 | 688 | +688 |
| i2c_cmd_queue | 0 | 688 | +688 |
| 原 cmd_queue (删除) | 2,464 | 0 | -2,464 |
| pending_queues[8] | 1,056 | 2,232 | +1,176 |
| UART ring buffer | 0 | 256 | +256 |
| **净增量** | — | — | **+4,856 B** |

### 性能预测

| 指标 | 当前 | 方案A优化后 | 提升 |
|------|------|------------|------|
| SPI/I2C 命令延迟 | 受 UART delay 阻塞 | 独立执行 | **消除 ~2.25s/周期** |
| UART 命令吞吐 | 串行+delay | 串行无delay | **+3.75% wall time** |
| I2C 故障影响 | 阻塞所有总线 100ms | 仅阻塞 i2c_cmd_task | **0 跨总线影响** |
| pending_queue 溢出 | depth=4 易满 | depth=17 充裕 | **消除** |
| 最大并发总线数 | 1 (串行) | 3 (并行) | **3×** |
| CPU 占用 (rx_task) | ~0.05% | ~0.05% | 不变 |
| I2C 中断 CPU | ~0.002% | ~0.002% | 不变 |

---

## 8. 待辩论问题回答

| # | 问题 | 回答 |
|---|------|------|
| 1 | delay_ms 是否可完全消除? | **是**。Modbus RTU turn-around time 由调度器间隔保证。 |
| 2 | 3 个 cmd_task 是否过度设计? | **否**。3 种总线事务模型不同, 拆分是架构正确性。 |
| 3 | per-channel vs per-bus-type? | **per-bus-type**。per-channel 8 个 task 内存不可接受。 |
| 4 | LP_I2C 是否值得适配? | **否**。配置层面限制为 1 个 I2C 总线。 |
| 5 | pending_queues 深度? | **17 = 5×3+2**, 用宏定义。 |
| 6 | UART FIFO 中断模式可靠性? | **需加 256B ring buffer + mutex timeout=2ms**。 |
| 7 | rx_task mutex timeout? | **从 0 改为 pdMS_TO_TICKS(2)**。 |
| 8 | UART TX 完成回调? | **不需要**。消除 delay_ms 后 cmd_task 不再等待。 |
