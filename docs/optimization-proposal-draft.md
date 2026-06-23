# ESP32-C6 多总线并行工作优化方案 (Draft v1)

## 问题清单

### P0 — 架构瓶颈

1. **cmd_task 单任务串行化所有总线**
   - 所有 5 条总线的命令共享 1 个 cmd_queue，由 1 个 cmd_task 串行消费
   - UART 的 vTaskDelay(delay_ms) 期间，SPI/I2C 命令全部排队等待
   - 量化：2 个 UART × 15 命令/周期 × 平均 75ms delay = 2.25s 纯等待/60s 周期

2. **UART delay_ms 不必要且有害**
   - rx_task 已经以 5ms 间隔异步轮询 UART RX
   - delay_ms 不等待响应到达，只是"给设备处理时间"
   - 响应会被 rx_task 异步拾取，无论 cmd_task 是否在 sleep
   - delay_ms 实际效果 = 白白浪费 wall time，阻塞其他总线

### P1 — 硬件约束适配

3. **UART0/UART1 共享 UHCI0 DMA，第二个 UART 降级为 FIFO 中断模式**
   - C6 只有 1 个 UHCI，同时刻只有 1 个 UART 可用 DMA
   - 降级的 UART 无 ring buffer，rx_task mutex timeout=0 时可能丢数据

4. **C6 只有 1 个 HP I2C 控制器**
   - 硬编码 i2c_port = I2C_NUM_0，第二条独立 I2C 总线初始化必然失败
   - LP_I2C 属于低功耗域，驱动 API 不兼容

5. **pending_queues 深度=4 不够**
   - MAX_EDGE_DEVICES_PER_CH=5 × MAX_COMMANDS_PER_DEVICE=3 = 15 命令/通道/周期
   - Queue 深度 4 在多设备场景下会溢出

### P2 — 效率改进

6. **rx_task 轮询所有 channel 包含非 UART**
   - 每次循环 8 个 slot，只有 2 个 UART 有效，75% 无效轮询

7. **I2C transact 100ms 超时可能阻塞 cmd_task**
   - I2C 设备故障时，单次 transact 阻塞 100ms

---

## 优化方案

### 方案 A：消除 delay_ms + 拆分 cmd_task（激进）

#### A1: 消除 UART CMD_SAMPLE 的 delay_ms

```c
// bus_worker.c CMD_SAMPLE UART 路径
// 当前：
if (cmd.delay_ms > 0) {
    vTaskDelay(pdMS_TO_TICKS(cmd.delay_ms));
}

// 修改为：直接删除 vTaskDelay
// rx_task 已经异步拾取响应，不需要等
// pending_queue 中的 entry 会在 rx_task 下次读到数据时被消费
```

#### A2: 拆分 cmd_task 为 3 个独立任务

```
cmd_task_uart (prio 7)  — 消费 uart_cmd_queue，处理 UART TX
cmd_task_spi  (prio 7)  — 消费 spi_cmd_queue，处理 SPI transact
cmd_task_i2c  (prio 7)  — 消费 i2c_cmd_queue，处理 I2C transact
```

- 每个任务独立的 cmd_queue，互不阻塞
- UART 的 delay_ms（即使保留）只阻塞 uart cmd_task
- SPI/I2C transact 独立执行，不被 UART 阻塞

#### A3: scheduler 按 bus_type 分发到不同 queue

```c
// scheduler.c
if (ctx->bus_type == BUS_TYPE_UART) {
    xQueueSend(s->uart_cmd_queue, &cmd, 0);
} else if (ctx->bus_type == BUS_TYPE_SPI) {
    xQueueSend(s->spi_cmd_queue, &cmd, 0);
} else {
    xQueueSend(s->i2c_cmd_queue, &cmd, 0);
}
```

#### A4: 增大 pending_queues 深度

```c
// app_state.c
#define PENDING_QUEUE_DEPTH (MAX_EDGE_DEVICES_PER_CH * MAX_COMMANDS_PER_DEVICE + 2)
s_app.pending_queues[i] = xQueueCreate(PENDING_QUEUE_DEPTH, sizeof(pending_cmd_t));
```

从 4 改为 17。

### 方案 B：仅消除 delay_ms（保守）

只做 A1（消除 delay_ms），不拆分 cmd_task。

- 收益：每周期节省 1-2s wall time
- 风险：如果设备确实需要 delay_ms 才能正确响应（虽然从物理层分析不需要）
- 优点：改动最小，1 处代码修改

### 方案 C：拆分 cmd_task 但保留 delay_ms（折中）

做 A2+A3，但保留 UART 的 delay_ms。

- UART cmd_task 自己 delay，不影响 SPI/I2C cmd_task
- 但 UART 多设备时仍然串行 delay
- 比 A 少改 1 处，但没解决 UART 内部的 delay 浪费

---

## 待专家辩论的问题

1. delay_ms 是否可以完全消除？Modbus RTU 协议的 turn-around time 要求是什么？
2. 拆分 cmd_task 为 3 个任务是否过度设计？内存和调度开销是否值得？
3. 是否应该用 per-channel cmd_task 而非 per-bus-type？
4. LP_I2C 是否值得适配？还是应该在配置层面限制为 1 个 I2C 总线？
5. pending_queues 深度应该多大？是否需要动态调整？
6. UART FIFO 中断模式是否足够可靠？是否需要软件 ring buffer？
7. rx_task 的 mutex timeout=0 是否应该改为短超时？
8. 是否应该引入 UART TX 完成回调机制替代 vTaskDelay？
