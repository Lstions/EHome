━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  P0-P3 架构改进方案 — v2.1
  (基于 v2.0 三方评审 + 代码验证 + 架构审核修正)
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

> **v2.0 → v2.1 变更摘要**:
> 1. P0-3 binary semaphore 并发 bug 修正 → counting semaphore(1,1)
> 2. P1-2 功能码禁令误杀 change-address → 白名单模式
> 3. P1-3 deviceMutexMap 内存泄漏 → 加清理机制
> 4. P1-4 无限制 goroutine spawn → 加 backpressure
> 5. 代码行号/文件名修正（pendingwrite.go, CalcConfigHash 行号 245, change-address 校验描述）
> 6. 新增 P1-6: ESP32 RX 超时机制（原评审"断点1"遗漏项）
> 7. 新增 P2-7: EdgeDevice.Type 冗余字段处理策略
> 8. P1-1 Modbus 异常匹配增加 slave addr + func code 精确匹配
> 9. P3-2 帧分隔升级为完整四种模式并提升至 P1-7（GB3024/BMS 即将接入）
> 10. P3-4 Redis 持久化改为 SQLite WAL 替代

---

## 一、评审总览

### 三方一致同意的结论

| # | 结论 | 说明 |
|---|------|------|
| 1 | P0-1 **不是 goroutine 泄漏** | channel 是 buffered(1)，迟到响应不会阻塞。真正问题是竞态条件 |
| 2 | P0-1 **绝不能 close(channel)** | close 会导致 panic — 迟到响应写入已关闭 channel 必崩 |
| 3 | P0-2 需要 timeout 上限 + context 传播 | 无上限 timeout 是唯一真实风险 |
| 4 | P1-1 Modbus 异常匹配是真实 bug | 三方一致，分歧在修复位置（ESP32 vs 后端）|
| 5 | P2-6 **不能丢弃传感器数据** | IoT 系统数据不可丢失，应扩大 buffer + 增加 worker |
| 6 | 当前方案遗漏了 5 个关键故障模式 | 见下文"新增项" |

### 关键分歧与仲裁

| 议题 | 嵌入式 | 后端 | 安全 | **最终决策** |
|------|--------|------|------|-------------|
| P0-1 修复方式 | select+default | sync.Once | select+default | **sync.Once**（最优雅，无 panic 风险）|
| P0-3 修复方式 | suspend/resume | mutex | mutex | **suspend/resume**（嵌入式专家方案更彻底）|
| P1-1 修复位置 | ESP32 固件 | 后端防御 | 后端 | **ESP32 主修 + 后端防御**（双保险）|
| P1-3 是否需要 | 不需要 | REPEATABLE READ | 需要 | **降为 P2**（自愈机制兜底）|
| P1-4 verify_read | 过度设计 | 用操作对 | 幂等操作 | **降为 P2，用操作对替代**（不新增 schema）|
| P2-1 双队列 | 过度设计 | 跳过 | 保留现状 | **删除**（当前规模不需要）|
| P2-2 拆表 | 跳过 | Go helpers | 迁移风险 | **删除**（用 Go helpers 替代）|
| P2-3 解析器注册 | YAGNI | 同意 | 同意 | **保留但降低优先级** |
| P2-4 turnaround 配置 | 不需要 | 跳过 | 需要 | **保留**（增加 bounds check）|
| P2-6 优先级 | MEDIUM | 需改 | **提升到 P1** | **提升到 P1**（MQTT 阻塞风险）|

### v2.1 代码验证结果

| 检查项 | 结果 | 备注 |
|--------|------|------|
| pendingwrite.go Entry struct buffered(1) | ✓ | v2.0 误写为 write.go，已修正 |
| pendingwrite.go 超时路径未 close channel | ✓ | 第 95-97 行 |
| handler_edge_device.go /execute 同步阻塞 | ✓ | 第 632 行 |
| handler_edge_device.go change-address "无校验" | ✗→修正 | 实际有 1-247 范围校验(第696-698行)，描述改为"校验不充分" |
| template_engine.go 只支持 2 种解析器 | ✓ | 第 237-262 行 |
| manager.go CalcConfigHashForDevice 无事务 | ✓ | 行号修正: 69→245 |
| handler_data.go MQTT 回调 DB 查询 | ✓ | 第 67-71 行 |
| models.go TemplateIDs 逗号分隔 | ✓ | 第 120 行 |
| models.go EdgeDevice.Type 冗余 | ✓ | 第 74 行 |
| bus_worker.c O(n) drain | ✓ | 第 367-406 行 |
| bus_worker.c turnaround 硬编码 | ✓ | 第 112/179 行 |
| cmd_queue.h read_size 字段 | ✓ | 第 57 行 |

---

## 二、最终优先级排序

### P0 — 本周内修复（系统稳定性）

| # | 项目 | 影响 | 工作量 |
|---|------|------|--------|
| P0-1 | pendingWrite 竞态修复 (sync.Once) | goroutine 安全 | 2h |
| P0-2 | /execute timeout 上限 + context 传播 | 连接池保护 | 3h |
| P0-3 | bus_manager suspend/resume (counting semaphore) | 并发安全 | 2h |

### P1 — 两周内修复（功能正确性）

| # | 项目 | 影响 | 工作量 |
|---|------|------|--------|
| P1-1 | Modbus 异常匹配 (ESP32 + 后端双修, 含精确匹配) | 读操作不再误报超时 | 4h |
| P1-2 | change-address 安全校验 (白名单模式) | 防广播攻击 | 2h |
| P1-3 | 并发 /execute 互斥 (含清理机制) | 防数据错误归因 | 3h |
| P1-4 | handleDataReport MQTT 回调中 DB 查询外移 (含 backpressure) | 防 MQTT 阻塞 | 2h |
| P1-5 | worker pool 扩容 + 缓存 | 吞吐量 10x | 4h |
| P1-6 | **v2.1 新增** ESP32 RX 超时机制 | 传感器无响应时返回明确错误 | 3h |
| P1-7 | **v2.1 提升** 协议无关帧分隔（完整四种模式） | GB30+BMS 即将接入 | 8h |

### P2 — 一个月内改进（可维护性）

| # | 项目 | 影响 | 工作量 |
|---|------|------|--------|
| P2-1 | CalcConfigHash REPEATABLE READ | 哈希一致性 | 2h |
| P2-2 | turnaround_us 配置化 + bounds check | 协议通用性 | 3h |
| P2-3 | ParseModbusResponse 注册表 | 可扩展性 | 3h |
| P2-4 | write 操作用操作对验证 | 端到端确认 | 4h |
| P2-5 | pendingWrite 优雅关闭 | 停机安全 | 2h |
| P2-6 | buildHashData 排序确定性 | 哈希一致性 | 1h |
|| P2-7 | **v2.1 新增** EdgeDevice.Type 冗余字段处理 | 数据一致性 | 2h |
|| P2-8 | **v2.3 新增** bus_worker/manager 迁移 components/ | 架构一致性+可测试性 | 4h |
|| P2-9 | **v2.3 新增** bus_type 存入 ctx 替代 manifest 查询 | 解耦运行时依赖 | 2h |

### P3 — 远期优化

| # | 项目 | 说明 |
|---|------|------|
| P3-1 | 调度器高频采集（硬件定时器） | 当前 10ms tick 够用 |
| P3-2 | 帧分隔机制（timeout + fixed 先行） | v2.1 简化：先实现两种，其余按需 |
| P3-3 | 解析器 JSON 定义 | 当前 parser 够用 |
| P3-4 | pending write 持久化 (SQLite WAL) | v2.1 改为 SQLite，不引入 Redis |
|| P3-5 | MQTT QoS 2 for critical ops | 当前 QoS 1 够用 |
|| P3-6 | **v2.3 新增** derive_uart_port 去重 (hw_tables 公共函数) | DRY |

---

## 三、P0 最终方案

### P0-1: pendingWrite 竞态修复 (sync.Once)

**问题本质**: 不是 goroutine 泄漏（buffered channel 防止了），而是 HandleDataReportAck 和 timeout 路径的竞态。

**代码位置**: `pendingwrite.go` (非 write.go)

**最终方案 — sync.Once 保证恰好一次投递**:

```go
// pendingwrite.go

type Entry struct {
    Response chan *Response  // buffered(1)
    once     sync.Once       // 保证恰好一次写入
}

// resolve 是唯一投递路径 — 无论谁先调用，只执行一次
func (e *Entry) resolve(resp *Response) {
    e.once.Do(func() {
        e.Response <- resp  // buffer=1, 永不阻塞
    })
}

// SendWriteCommand — 等待响应
func (m *Manager) SendWriteCommand(ctx context.Context, deviceID string,
    channelID uint32, data []byte, readSize uint32, timeout time.Duration) (*Response, error) {

    reqID := m.nextID()
    entry := &Entry{Response: make(chan *Response, 1)}
    m.addEntry(reqID, entry)
    defer m.removeEntry(reqID)

    // 发送 MQTT WriteCommand...
    m.sender.SendWriteCommand(deviceID, channelID, data, readSize, reqID)

    select {
    case resp := <-entry.Response:
        return resp, nil
    case <-time.After(timeout):
        entry.resolve(&Response{Success: false, ErrorMsg: "timeout"})  // 占住 Once
        return nil, fmt.Errorf("timeout after %v", timeout)
    case <-ctx.Done():
        entry.resolve(&Response{Success: false, ErrorMsg: "cancelled"})
        return nil, ctx.Err()
    }
}

// HandleDataReportAck — 迟到响应安全投递
func (m *Manager) HandleDataReportAck(reqID uint32, rawData []byte) {
    entry := m.getEntry(reqID)
    if entry == nil {
        metrics.Counter("pendingwrite_late_response").Inc()
        return
    }
    entry.resolve(&Response{Success: true, RawData: rawData})  // Once 保证不重复
}

// HandleResponse — WriteRsp 错误路径
func (m *Manager) HandleResponse(reqID uint32, success bool, errCode uint32, errMsg string) {
    entry := m.getEntry(reqID)
    if entry == nil { return }
    if !success {
        entry.resolve(&Response{Success: false, ErrorCode: errCode, ErrorMsg: errMsg})
    }
}
```

**为什么不用 close(channel)**:
- close 后 HandleDataReportAck 写入 → panic: send on closed channel
- 网络重复包、ESP32 重试、MQTT broker 重发都可能触发
- sync.Once 无 panic 风险，语义更清晰

**为什么不用 atomic.Bool**:
- 需要两个原子操作（CAS + close），比 sync.Once 复杂
- sync.Once 是标准库，经过充分验证

---

### P0-2: /execute timeout 上限 + context 传播

**代码位置**: `handler_edge_device.go` 第 632 行

**最终方案 — 三重防护**:

```go
// handler_edge_device.go

// 1. 全局并发限制
var executeLimiter = make(chan struct{}, 20)

func (h *Handler) handleExecute(c *gin.Context) {
    // ...existing validation...

    if opConfig.Type == "read" {
        // 并发限制
        select {
        case executeLimiter <- struct{}{}:
            defer func() { <-executeLimiter }()
        default:
            Error(c, http.StatusServiceUnavailable, "too many concurrent operations")
            return
        }

        // Timeout 上限 30s
        timeout := 10 * time.Second
        if opConfig.TimeoutMs > 0 {
            configured := time.Duration(opConfig.TimeoutMs) * time.Millisecond
            if configured <= 30*time.Second {
                timeout = configured
            }
        }

        // Context 传播（支持客户端断开取消）
        ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
        defer cancel()

        start := time.Now()
        resp, err := h.nodeMgr.PendingWrite().SendWriteCommand(ctx, deviceID, chID, writeData, readSize, timeout)
        duration := time.Since(start)
        metrics.Histogram("execute_read_duration").Observe(duration.Seconds())

        if err != nil {
            if errors.Is(err, context.DeadlineExceeded) {
                metrics.Counter("execute_timeout").Inc()
                Error(c, http.StatusGatewayTimeout, "device did not respond")
            } else if errors.Is(err, context.Canceled) {
                Error(c, 499, "client disconnected")
            } else {
                Error(c, http.StatusInternalServerError, err.Error())
            }
            return
        }

        // 解析响应（含 Modbus 异常检测）
        value, unit, err := ParseResponse(opConfig.ResponseParser, resp.RawData, opConfig.Unit)
        if err != nil {
            Error(c, http.StatusInternalServerError, err.Error())
            return
        }

        c.JSON(200, gin.H{
            "status": "success", "value": value, "unit": unit,
            "raw_hex": hex.EncodeToString(resp.RawData),
        })
    }
}
```

```go
// pendingwrite.go — SendWriteCommand 接受 context.Context
func (m *Manager) SendWriteCommand(ctx context.Context, deviceID string,
    channelID uint32, data []byte, readSize uint32, timeout time.Duration) (*Response, error) {
    // ... see P0-1 implementation above ...
    select {
    case resp := <-entry.Response:
        return resp, nil
    case <-time.After(timeout):
        entry.resolve(&Response{Success: false, ErrorMsg: "timeout"})
        return nil, fmt.Errorf("timeout")
    case <-ctx.Done():
        entry.resolve(&Response{Success: false, ErrorMsg: "cancelled"})
        return nil, ctx.Err()
    }
}
```

---

### P0-3: bus_manager suspend/resume (counting semaphore 修正版)

**v2.0 问题**: binary semaphore 初始为空，cmd_task 的 `Take(0)+Give()` 会意外释放 suspend 持有的 sem，导致并发 bug。

**v2.1 修正**: 使用 counting semaphore(1,1)，初始值=1（可用状态）。

```c
// bus_worker.h — 新增暂停/恢复 API
void bus_worker_suspend(void);
void bus_worker_resume(void);

// bus_worker.c — 实现
static SemaphoreHandle_t s_suspend_sem = NULL;  // NULL = 未初始化

// 初始化：counting semaphore, maxCount=1, initialCount=1 (可用)
static void ensure_suspend_sem(void) {
    if (s_suspend_sem == NULL) {
        s_suspend_sem = xSemaphoreCreateCounting(1, 1);
    }
}

// suspend: 获取 sem → 阻塞 cmd_task
// 调用者: config apply 任务（唯一调用点）
void bus_worker_suspend(void) {
    ensure_suspend_sem();
    xSemaphoreTake(s_suspend_sem, portMAX_DELAY);  // 获取 = 暂停，阻塞直到 cmd_task 释放
}

// resume: 释放 sem → 恢复 cmd_task
void bus_worker_resume(void) {
    if (s_suspend_sem != NULL) {
        xSemaphoreGive(s_suspend_sem);  // 释放 = 恢复
    }
}

// 在每个 cmd_task 循环头部检查暂停
// counting semaphore(1,1) 保证:
//   - Give 不会超过 maxCount=1，不会意外多释放
//   - cmd_task 的 Take(0)+Give() 是原子探针，不会偷走 suspend 持有的 sem
static void uart_cmd_loop(app_state_t *s, QueueHandle_t queue, const char *tag) {
    while (1) {
        // 暂停检查：如果配置正在应用，等待恢复
        // Take(0) 非阻塞尝试获取 — 如果 suspend 持有 sem，Take 失败，任务阻塞
        // 如果 sem 可用（=未暂停），Take 成功，立即 Give 归还
        if (s_suspend_sem != NULL) {
            if (xSemaphoreTake(s_suspend_sem, 0) == pdTRUE) {
                // sem 可用 = 未暂停，归还
                xSemaphoreGive(s_suspend_sem);
            } else {
                // sem 被占用 = 已暂停，阻塞等待
                // 注意: 这里不能 portMAX_DELAY，否则 config apply 期间
                // cmd_task 永远阻塞。应该短暂等待后重试。
                vTaskDelay(pdMS_TO_TICKS(10));
                continue;  // 重新检查
            }
        }

        bus_cmd_t cmd;
        if (!xQueueReceive(queue, &cmd, portMAX_DELAY)) continue;
        // ...existing code...
    }
}
```

```c
// app_callbacks.c — 配置应用时暂停总线
static void on_config_manifest_received(const uint8_t *data, size_t len) {
    // 1. 暂停所有总线任务（防止读写 stale context）
    bus_worker_suspend();

    // 2. 应用配置（可能 reg/unreg channels）
    app_state_lock_config();
    config_mgr_apply_manifest(data, len);
    app_state_unlock_config();

    // 3. 恢复总线任务
    bus_worker_resume();
}
```

**v2.1 修正要点**:
1. `xSemaphoreCreateCounting(1, 1)` 替代 `xSemaphoreCreateBinary()` — 初始值=1 表示可用
2. counting semaphore 的 Give 不会超过 maxCount=1 — 消除了 cmd_task 意外释放 suspend 持有 sem 的 bug
3. cmd_task 暂停检查改为 `Take(0) + Give()` 探针 + `vTaskDelay(10ms)` 重试 — 不用 portMAX_DELAY 避免死锁

**优势 vs 加锁方案**:
1. 零热路径开销 — cmd_task 正常执行时只做一次非阻塞 Take+Give（~1μs）
2. 消除竞态根因 — 配置应用期间没有并发访问
3. 简单可靠 — 只有 config apply 一个调用点
4. 嵌入式标准模式 — stop-the-world 是 RTOS 配置切换的标准做法

**暂停时长**: ~50-200ms（manifest 解析 + 总线重新初始化），对 5s 采集周期无影响。

---

## 四、P1 最终方案

### P1-1: Modbus 异常匹配 (ESP32 + 后端双保险, 含精确匹配)

**ESP32 主修 — rx_task 异常检测（v2.1 增加精确匹配）**:

```c
// bus_worker.c rx_task — 在现有匹配逻辑后添加
// 位置：line 406 之后，fallback 之前

if (!found && n >= 5 && (rx[1] & 0x80)) {
    // Modbus 异常响应 (addr + func|0x80 + exception_code + CRC = 5 bytes)
    // v2.1: 精确匹配 — 用 slave addr + func code 避免多 pending 时错配
    uint8_t resp_addr = rx[0];
    uint8_t resp_func = rx[1] & 0x7F;  // 去掉异常标志位

    int best_idx = -1;
    for (int j = 0; j < drained_count; j++) {
        if (drained[j].read_size > 0) {
            // 精确匹配: 比较 pending 命令的 slave addr 和 func code
            // pending 命令的 data[0] = slave addr, data[1] = func code
            if (drained[j].cmd_data_len >= 2 &&
                drained[j].cmd_data[0] == resp_addr &&
                drained[j].cmd_data[1] == resp_func) {
                best_idx = j;
                break;  // 精确匹配，立即退出
            }
            // 降级: 如果没有精确匹配，取第一个 read_size > 0 的
            if (best_idx < 0) {
                best_idx = j;
            }
        }
    }

    if (best_idx >= 0) {
        best_match = drained[best_idx];
        // 从 drained 数组移除
        for (int k = best_idx; k < drained_count - 1; k++) {
            drained[k] = drained[k + 1];
        }
        drained_count--;
        found = true;
        ESP_LOGI(TAG_RX, "matched Modbus exception (addr=0x%02X func=0x%02X) to reqID=%lu",
                 resp_addr, resp_func, (unsigned long)best_match.request_id);
    }
}
```

**注意**: `cmd_data` 字段需要在 `pending_cmd_t` 中新增，存储原始命令的前 N 字节用于匹配。如果不想增加 pending_cmd_t 大小，降级方案是取第一个 `read_size > 0` 的条目（v2.0 原方案），在当前 ≤5 设备/通道规模下可接受。

**后端防御 — ParseModbusResponse 已有异常检测**:
```go
// template_engine.go — 现有代码已处理
if rawData[1]&0x80 != 0 {
    return 0, "", fmt.Errorf("modbus exception: func=0x%02X code=0x%02X (%s)",
        rawData[1], rawData[2], modbusExceptionMessage(rawData[2]))
}
```

无需额外后端改动 — ESP32 修复后 request_id 正确传递，后端已有的异常解析自动生效。

---

### P1-2: change-address 安全校验 (白名单模式)

**v2.0 问题**: `funcCode >= 0x05` 禁令会禁止 change-address 自身（FC06 = 写单个寄存器）。

**v2.1 修正**: 改为功能码白名单 + 目标地址校验。

**代码位置**: `handler_edge_device.go` 第 696-698 行已有 `1-247` 范围校验，以下为增强版：

```go
// handler_edge_device.go

if req.Command != "" {
    data, err := hex.DecodeString(req.Command)
    if err != nil {
        Error(c, http.StatusBadRequest, "invalid hex format")
        return
    }

    // 长度限制
    if len(data) < 4 || len(data) > 64 {
        Error(c, http.StatusBadRequest, "command must be 4-64 bytes")
        return
    }

    // Modbus 地址校验 — 防广播攻击
    slaveAddr := data[0]
    if slaveAddr == 0x00 {
        Error(c, http.StatusBadRequest, "broadcast address 0x00 not allowed")
        return
    }
    if slaveAddr > 247 {
        Error(c, http.StatusBadRequest, "Modbus address must be 1-247")
        return
    }

    // 目标地址匹配 — 命令必须发给当前设备
    if oldAddr > 0 && slaveAddr != oldAddr {
        Error(c, http.StatusBadRequest,
            fmt.Sprintf("command must target device address 0x%02X", oldAddr))
        return
    }

    // CRC 校验
    if len(data) >= 4 {
        expectedCRC := ModbusCRC16(data[:len(data)-2])
        actualCRC := binary.LittleEndian.Uint16(data[len(data)-2:])
        if expectedCRC != actualCRC {
            Error(c, http.StatusBadRequest, "invalid Modbus CRC")
            return
        }
    }

    // v2.1: 功能码白名单（替代 v2.0 的 funcCode >= 0x05 禁令）
    // 允许: FC01-04(读) + FC05/06(写单寄存器/线圈, change-address 需要)
    // 禁止: FC0F/10(写多寄存器/线圈) + FC2x/3x/4x/5x/6x/7x(诊断/文件/等)
    funcCode := data[1] & 0x7F
    switch funcCode {
    case 0x01, 0x02, 0x03, 0x04:
        // 读操作 — 允许
    case 0x05, 0x06:
        // 写单个寄存器/线圈 — 允许（change-address 使用 FC06）
        // 额外校验: 写操作的目标寄存器地址应在允许范围内
        if len(data) >= 6 {
            regAddr := uint16(data[2])<<8 | uint16(data[3])
            // 允许的寄存器范围由 DeviceConfig 定义，这里用默认范围
            // change-address 通常写寄存器地址 0x0001-0x00FF
            if regAddr > 0xFF {
                Error(c, http.StatusForbidden,
                    fmt.Sprintf("write to register 0x%04X not allowed (max 0x00FF)", regAddr))
                return
            }
        }
    default:
        Error(c, http.StatusForbidden,
            fmt.Sprintf("function code 0x%02X not allowed (allowed: 01-06)", funcCode))
        return
    }

    writeData = data
}
```

**v2.1 vs v2.0 差异**:
- v2.0: `funcCode >= 0x05` → 禁止 FC05/06，change-address 自身被误杀
- v2.1: 白名单 `FC01-06` → 允许 change-address (FC06)，禁止 FC0F/10 等批量写操作
- 新增: 写操作目标寄存器地址范围校验

---

### P1-3: 并发 /execute 互斥（含清理机制）

**v2.0 问题**: `locks` map 只增不减，设备删除后旧 key 永远残留。

**v2.1 修正**: 加定期清理 + 引用计数。

```go
// handler_edge_device.go — 设备级互斥（含清理）
type deviceMutexEntry struct {
    mu    sync.Mutex
    last  time.Time  // 最后使用时间
    count int64      // 引用计数（原子操作）
}

type deviceMutexMap struct {
    mu      sync.Mutex
    locks   map[string]*deviceMutexEntry
    cleanup time.Time  // 上次清理时间
}

var deviceLocks = &deviceMutexMap{locks: make(map[string]*deviceMutexEntry)}

func (dm *deviceMutexMap) lock(deviceKey string) {
    dm.mu.Lock()
    // 每 1000 次加锁清理一次过期条目
    if time.Since(dm.cleanup) > 10*time.Minute {
        for k, v := range dm.locks {
            if atomic.LoadInt64(&v.count) == 0 && time.Since(v.last) > 30*time.Minute {
                delete(dm.locks, k)
            }
        }
        dm.cleanup = time.Now()
    }
    if dm.locks[deviceKey] == nil {
        dm.locks[deviceKey] = &deviceMutexEntry{}
    }
    entry := dm.locks[deviceKey]
    atomic.AddInt64(&entry.count, 1)
    dm.mu.Unlock()

    entry.mu.Lock()
    entry.last = time.Now()
}

func (dm *deviceMutexMap) unlock(deviceKey string) {
    dm.mu.Lock()
    entry := dm.locks[deviceKey]
    dm.mu.Unlock()
    if entry != nil {
        atomic.AddInt64(&entry.count, -1)
        entry.mu.Unlock()
    }
}

// 在 handleExecute 中：
deviceKey := fmt.Sprintf("%s:%d", deviceID, edge.ChannelID)
deviceLocks.lock(deviceKey)
defer deviceLocks.unlock(deviceKey)
```

---

### P1-4: handleDataReport MQTT 回调中 DB 查询外移（含 backpressure）

**v2.0 问题**: worker pool 满时 `go m.processDataReportJob(job)` 无限制 spawn goroutine。

**v2.1 修正**: 加全局 goroutine 计数限制。

**代码位置**: `handler_data.go` 第 67-71 行

```go
// handler_data.go — 修复前
func (m *Manager) handleDataReport(deviceID string, payload []byte) {
    // ... decode frame ...

    // 问题：DB 查询在 MQTT 回调线程
    var node models.Node
    m.db.Where("node_id = ?", deviceID).First(&node)  // ← 阻塞 MQTT！

    // 推入 worker pool
    job := dataReportJob{channelID: chID, rawData: raw, ...}
    select {
    case m.dataCh <- job:
    default:
        m.processDataReportJob(job)  // 同步回退也阻塞
    }
}

// v2.1 修复后 — 将 nodeID 查询移入 worker + backpressure
var overflowGoroutines atomic.Int64
const maxOverflowGoroutines = 50  // 最多 50 个溢出 goroutine

func (m *Manager) handleDataReport(deviceID string, payload []byte) {
    // ... decode frame (纯内存操作) ...

    job := dataReportJob{
        deviceID: deviceID,  // 传递 deviceID，让 worker 查询
        channelID: chID,
        rawData:   raw,
        // ...
    }
    select {
    case m.dataCh <- job:
        metrics.Gauge("worker_pool_queue_size").Set(float64(len(m.dataCh)))
    default:
        // v2.1: backpressure — 限制溢出 goroutine 数量
        current := overflowGoroutines.Load()
        if current >= maxOverflowGoroutines {
            // 极端过载 — 记录指标但不丢弃数据
            // 阻塞 MQTT 回调是最后的手段（比丢数据好）
            metrics.Counter("worker_pool_backpressure_block").Inc()
            log.Error("[%s] CRITICAL: overflow limit reached (%d), blocking MQTT callback",
                      deviceID, current)
            m.processDataReportJob(job)  // 同步回退，阻塞 MQTT
        } else {
            metrics.Counter("worker_pool_overflow").Inc()
            overflowGoroutines.Add(1)
            go func() {
                defer overflowGoroutines.Add(-1)
                m.processDataReportJob(job)
            }()
        }
    }
}

// worker_pool.go — processDataReportJob 内部查询 nodeID
func (m *Manager) processDataReportJob(job dataReportJob) {
    // 在 worker goroutine 中查询 DB（不阻塞 MQTT）
    var node models.Node
    if err := m.db.Where("node_id = ?", job.deviceID).First(&node).Error; err == nil {
        job.collectorID = node.ID
    }
    // ...rest of processing...
}
```

---

### P1-5: worker pool 扩容 + 边缘设备缓存（原 P2-6 提升）

```go
// worker_pool.go — 扩容
const (
    WorkerCount = 8      // 从 4 → 8
    BufferSize  = 1024   // 从 128 → 1024（10 秒缓冲）
)
```

```go
// edge_device_cache.go — LRU 缓存（消除每消息 2-3 次 DB 查询）
type edgeDeviceCache struct {
    mu      sync.RWMutex
    entries map[string]*cachedEdge  // key: "channelID"
    ttl     time.Duration
}

type cachedEdge struct {
    edge    models.EdgeDevice
    expires time.Time
}

func (c *edgeDeviceCache) Get(channelID uint) (*models.EdgeDevice, bool) {
    c.mu.RLock()
    defer c.mu.RUnlock()
    key := strconv.FormatUint(uint64(channelID), 10)
    if e, ok := c.entries[key]; ok && time.Now().Before(e.expires) {
        return &e.edge, true
    }
    return nil, false
}

// TTL 60s，配置变更时 invalidate
```

**预期效果**:
- 吞吐量: 50 msg/s → 500+ msg/s（缓存消除 DB 瓶颈）
- 延迟: P99 从 200ms → <50ms

---

### P1-6: ESP32 RX 超时机制（v2.1 新增）

**问题来源**: 评审"四、断点1" — WriteRsp 只确认 TX，不确认传感器响应。传感器无响应时，后端 pendingWrite 只能等超时，前端看到"超时"而非"传感器无响应"。

**方案 — ESP32 端 RX 超时后发带 error_code 的 DataReport**:

```c
// bus_worker.c — rx_task 增加超时检测

// 在 pending_cmd_t 入队时记录超时时间
typedef struct {
    uint32_t request_id;
    uint32_t edge_device_id;
    uint8_t  command_index;
    uint32_t read_size;
    int64_t  tx_timestamp;    // v2.1 新增: TX 完成时间
    uint32_t rx_timeout_ms;   // v2.1 新增: RX 超时时间（从 DeviceConfig 读取，默认 1000ms）
} pending_cmd_t;

// rx_task — 超时检测（在每次 RX 轮询中检查）
static void rx_task(void *pv) {
    app_state_t *s = (app_state_t *)pv;

    while (1) {
        // ...existing RX logic...

        // v2.1: 检查 pending 队列中的超时条目
        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            if (!s->pending_queues[i]) continue;

            // drain pending 队列检查超时
            pending_cmd_t drained[10];
            int count = 0;
            while (xQueueReceive(s->pending_queues[i], &drained[count], 0) == pdTRUE) {
                count++;
                if (count >= 10) break;
            }

            for (int j = 0; j < count; j++) {
                int64_t now = esp_timer_get_time();
                int64_t elapsed_ms = (now - drained[j].tx_timestamp) / 1000;

                if (elapsed_ms > drained[j].rx_timeout_ms) {
                    // RX 超时 — 发送带 error_code 的 DataReport
                    ESP_LOGW(TAG_RX, "RX timeout for reqID=%lu (waited %lldms, limit %lums)",
                             (unsigned long)drained[j].request_id,
                             elapsed_ms, (unsigned long)drained[j].rx_timeout_ms);

                    if (s_data_rpt_cb) {
                        // error_code = 0x01 表示 RX 超时
                        s_data_rpt_cb(s->bus_ch[i], now, 0x01, NULL, 0,
                                     drained[j].request_id, drained[j].edge_device_id,
                                     drained[j].command_index);
                    }
                    // 不推回队列 — 已超时处理
                } else {
                    // 未超时 — 推回队列
                    xQueueSend(s->pending_queues[i], &drained[j], 0);
                }
            }
        }

        vTaskDelay(pdMS_TO_TICKS(RX_POLL_MS));
    }
}
```

**后端处理**:
```go
// handler_data.go — DataReport error_code 处理
func (m *Manager) handleDataReport(deviceID string, payload []byte) {
    // ... decode frame ...
    errorCode := frame.ErrorCode()  // v2.1: 读取 error_code 字段

    if errorCode == 0x01 {
        // RX 超时 — 通知 pendingWrite
        m.pendingWrite.HandleResponse(requestID, false, 0x01, "sensor RX timeout")
        return  // 不走正常数据处理流程
    }

    // ...existing normal processing...
}
```

**收益**:
- 前端看到"传感器无响应"而非"操作超时" — 用户体验提升
- 超时时间可配置（不同传感器响应速度不同）
- 后端可区分"ESP32 离线"（pendingWrite 超时）和"传感器无响应"（DataReport error_code=0x01）

---

### P1-7: 协议无关帧分隔（完整四种模式，v2.1 从 P3-2 提升）

**提升原因**: GB3024 光伏逆变器和嘉佰达 BMS 即将接入，两种协议都不是 Modbus RTU：

| 设备 | 接口 | 协议 | 帧分隔方式 | 特征 |
|------|------|------|-----------|------|
| Modbus 传感器 | RS-485 | Modbus RTU | **timeout** (3.5字符间隔) | 固定响应长度，半双工 |
| GB3024 光伏逆变器 | RS-232/TTL | ASCII 文本 | **delimiter** (`\r`) | 命令`CMD<crc16>\r`，响应`(DATA\r`，全双工 |
| 嘉佰达 BMS | RS-485/RS-232/UART | 二进制 | **length_field + 起止标记** | `0xDD+cmd+status+len+data+checksum+0x77` |

四种模式全部必需，不能简化。

**DeviceConfig.Protocol 配置**:

```json
// Modbus RTU (RS-485 半双工)
{
    "protocol": {
        "name": "modbus_rt",
        "frame_delimiter": {
            "type": "timeout",
            "timeout_chars": 3.5
        }
    }
}

// GB3024 光伏逆变器 (RS-232/TTL ASCII)
{
    "protocol": {
        "name": "gb3024_ascii",
        "frame_delimiter": {
            "type": "delimiter",
            "delimiter_bytes": [13]       // 0x0D = '\r'
        }
    }
}

// 嘉佰达 BMS (二进制, 起止标记+长度字段)
{
    "protocol": {
        "name": "jiabaida_binary",
        "frame_delimiter": {
            "type": "start_stop",
            "start_byte": 221,            // 0xDD
            "stop_byte": 119,             // 0x77
            "length_field_offset": 3,     // status(1)之后是长度
            "length_field_size": 1,       // 1字节长度
            "length_includes_header": false // 长度不含帧头+命令+状态+长度本身
        }
    }
}

// 固定长度协议
{
    "protocol": {
        "name": "fixed_length",
        "frame_delimiter": {
            "type": "fixed",
            "fixed_length": 16
        }
    }
}
```

**ESP32 固件实现**:

```c
// bus_worker.h — 帧分隔类型
typedef enum {
    FRAME_DELIM_TIMEOUT,       // 基于时间间隔（Modbus RTU）
    FRAME_DELIM_DELIMITER,     // 基于分隔符（GB3024 ASCII）
    FRAME_DELIM_START_STOP,    // 起止标记+长度字段（嘉佰达 BMS）
    FRAME_DELIM_FIXED,         // 固定长度
} frame_delim_type_t;

typedef struct {
    frame_delim_type_t type;
    union {
        // FRAME_DELIM_TIMEOUT: 帧间静默超时
        struct {
            uint64_t timeout_us;         // 超时微秒（自动从 baud*chars 计算）
        } timeout;

        // FRAME_DELIM_DELIMITER: 分隔符结尾（如 \r）
        struct {
            uint8_t  bytes[4];           // 分隔符字节序列（最多4字节）
            uint8_t  len;                // 分隔符长度
        } delimiter;

        // FRAME_DELIM_START_STOP: 起止标记+长度字段
        struct {
            uint8_t  start_byte;         // 帧头（如 0xDD）
            uint8_t  stop_byte;          // 帧尾（如 0x77）
            int      length_field_offset;// 长度字段在帧头之后的偏移
            int      length_field_size;  // 长度字段大小（1或2字节）
            bool     length_includes_header; // 长度是否含帧头
            int      header_size;        // 帧头+命令+状态+长度字段的总字节数
        } start_stop;

        // FRAME_DELIM_FIXED: 固定长度
        struct {
            size_t   length;             // 固定帧长度
        } fixed_len;
    };
} frame_delim_config_t;

typedef struct {
    uint8_t  buffer[512];       // GB3024 响应可能很长
    size_t   len;
    uint64_t last_rx_time;
    bool     frame_started;     // start_stop 模式: 已检测到帧头
    int      pending_slot;
    frame_delim_config_t delim_cfg;
} stream_rx_t;

static stream_rx_t s_streams[SCHED_MAX_CHANNELS];
```

**帧分隔检测函数**:

```c
// bus_worker.c — 协议无关帧分隔检测

// 检查 buffer 末尾是否匹配分隔符
static bool ends_with_delimiter(const uint8_t *buf, size_t len,
                                 const uint8_t *delim, uint8_t delim_len) {
    if (len < delim_len) return false;
    return memcmp(buf + len - delim_len, delim, delim_len) == 0;
}

// 从 start_stop 帧中读取长度字段
static int32_t read_length_field(const uint8_t *buf, size_t buf_len,
                                  const frame_delim_config_t *cfg) {
    int offset = cfg->start_stop.length_field_offset;
    int size   = cfg->start_stop.length_field_size;
    if ((int)buf_len < offset + size) return -1;

    if (size == 1) {
        return buf[offset];
    } else if (size == 2) {
        return (buf[offset] << 8) | buf[offset + 1];  // 大端
    }
    return -1;
}

static bool is_frame_complete(stream_rx_t *stream) {
    switch (stream->delim_cfg.type) {

    case FRAME_DELIM_TIMEOUT: {
        // Modbus RTU: 帧间静默超过阈值
        if (stream->len == 0) return false;
        uint64_t now = esp_timer_get_time();
        return (now - stream->last_rx_time) > stream->delim_cfg.timeout.timeout_us;
    }

    case FRAME_DELIM_DELIMITER: {
        // GB3024 ASCII: 检查末尾是否为 \r
        return ends_with_delimiter(stream->buffer, stream->len,
                                   stream->delim_cfg.delimiter.bytes,
                                   stream->delim_cfg.delimiter.len);
    }

    case FRAME_DELIM_START_STOP: {
        // 嘉佰达 BMS: 0xDD + cmd + status + len + data + checksum + 0x77
        if (stream->len < 1) return false;

        // 等待帧头
        if (!stream->frame_started) {
            if (stream->buffer[0] == stream->delim_cfg.start_stop.start_byte) {
                stream->frame_started = true;
            } else {
                // 不是帧头，丢弃
                stream->len = 0;
                return false;
            }
        }

        // 已有帧头，检查长度字段是否可读
        int32_t payload_len = read_length_field(stream->buffer, stream->len,
                                                 &stream->delim_cfg);
        if (payload_len < 0) return false;  // 长度字段还没收到

        int header = stream->delim_cfg.start_stop.header_size;
        int expected = header + payload_len + 2 + 1;  // data + checksum(2) + stop(1)
        if (stream->delim_cfg.start_stop.length_includes_header) {
            expected = payload_len + 2 + 1;  // 长度含帧头
        }

        if (stream->len >= expected) {
            // 检查帧尾
            if (stream->buffer[expected - 1] == stream->delim_cfg.start_stop.stop_byte) {
                return true;
            }
            // 帧尾不匹配 — 可能数据损坏，重置
            ESP_LOGW(TAG_RX, "stop byte mismatch: expected 0x%02X, got 0x%02X",
                     stream->delim_cfg.start_stop.stop_byte,
                     stream->buffer[expected - 1]);
            stream->len = 0;
            stream->frame_started = false;
            return false;
        }
        return false;  // 还没收满
    }

    case FRAME_DELIM_FIXED: {
        return stream->len >= stream->delim_cfg.fixed_len.length;
    }

    default:
        return false;
    }
}
```

**rx_task 集成**:

```c
// bus_worker.c — 统一 rx_task（替代原有 Modbus 专用逻辑）
static void rx_task(void *pv) {
    app_state_t *s = (app_state_t *)pv;
    uint8_t rx[128];

    while (1) {
        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            if (!s->bus_ctx[i].initialized) continue;
            if (s->bus_ctx[i].bus_type != BUS_TYPE_UART) continue;

            stream_rx_t *stream = &s_streams[i];
            size_t n = bus_dma_read(&s->bus_ctx[i], rx, sizeof(rx));
            if (n > 0) {
                // 追加到流缓冲区
                if (stream->len + n <= sizeof(stream->buffer)) {
                    memcpy(stream->buffer + stream->len, rx, n);
                    stream->len += n;
                } else {
                    // 缓冲区溢出 — 丢弃并重置
                    ESP_LOGW(TAG_RX, "ch%d buffer overflow (%d+%d > %d), resetting",
                             i, stream->len, n, sizeof(stream->buffer));
                    stream->len = 0;
                    stream->frame_started = false;
                    continue;
                }
                stream->last_rx_time = esp_timer_get_time();
            }

            // 检查帧是否完整
            if (stream->len > 0 && is_frame_complete(stream)) {
                uint32_t ch = s->bus_ch[i];

                // 匹配 pending 条目（复用现有匹配逻辑）
                uint32_t rid = 0, eid = 0;
                uint8_t cidx = 0;
                // ...existing pending match logic...

                uint64_t ts = esp_timer_get_time();
                if (s_data_rpt_cb) {
                    s_data_rpt_cb(ch, ts, 0, stream->buffer, stream->len,
                                 0, rid, eid, cidx);
                }

                // 重置流状态
                stream->len = 0;
                stream->frame_started = false;
            }
        }

        vTaskDelay(pdMS_TO_TICKS(RX_POLL_MS));
    }
}
```

**嘉佰达 BMS 特殊处理 — 唤醒序列**:

嘉佰达协议要求：发送命令前先发 `00 00 00 00` 延时 100ms 再发实际数据。

```c
// bus_worker.c — cmd_task 唤醒序列
static void uart_cmd_loop(app_state_t *s, QueueHandle_t queue, const char *tag) {
    while (1) {
        // ...suspend check (P0-3)...

        bus_cmd_t cmd;
        if (!xQueueReceive(queue, &cmd, portMAX_DELAY)) continue;

        // turnaround delay (P2-2 配置化)
        // ...

        // v2.1: 唤醒序列（嘉佰达 BMS 需要）
        if (s->bus_ctx[cmd.channel].wakeup_required) {
            uint8_t wakeup[] = {0x00, 0x00, 0x00, 0x00};
            uart_write_bytes(s->bus_ctx[cmd.channel].uart_port, wakeup, sizeof(wakeup));
            vTaskDelay(pdMS_TO_TICKS(100));  // 嘉佰达要求 100ms
        }

        // 发送实际命令
        uart_write_bytes(s->bus_ctx[cmd.channel].uart_port, cmd.data, cmd.len);

        // ...existing TX complete + pending enqueue...
    }
}
```

**DeviceConfig.Connection 扩展**:

```json
{
    "uart": {
        "baud_rate": 9600,
        "physical_layer": "rs232",
        "duplex_mode": "full",
        "turnaround_us": -1,
        "wakeup_sequence": "00000000",
        "wakeup_delay_ms": 100
    }
}
```

**预期效果**:
- Modbus RTU: 无改动（timeout 模式兼容现有逻辑）
- GB3024 逆变器: delimiter('\r') 模式，全双工无 turnaround
- 嘉佰达 BMS: start_stop 模式，支持唤醒序列
- 扩展性: 新协议只需配置 JSON，不改固件

---

## 五、P2 最终方案

### P2-1: CalcConfigHash REPEATABLE READ

**代码位置**: `manager.go` 第 245 行（非第 69 行）

```go
func (m *Manager) CalcConfigHashForDevice(deviceID string) (ConfigHashResult, error) {
    var result ConfigHashResult
    err := m.db.Transaction(func(tx *gorm.DB) error {
        tx.Exec("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ")
        // ... 4 queries using tx instead of m.db ...
        // 所有查询加 ORDER BY id 确保确定性
        return nil
    })
    return result, err
}
```

**注意**: 如果后端用 SQLite（非 MySQL），REPEATABLE READ 不支持，改用 `BEGIN IMMEDIATE` 获取写锁保证一致性。

### P2-2: turnaround_us 配置化 + bounds check

```c
uint32_t turnaround_us = ctx->cfg.uart.turnaround_us;
if (turnaround_us == 0) {
    turnaround_us = 38500000UL / ctx->cfg.uart.baud;  // Modbus 自动
} else if (turnaround_us == (uint32_t)-1) {
    turnaround_us = 0;  // 全双工无延迟
} else if (turnaround_us > 100000) {
    ESP_LOGW(TAG, "turnaround_us=%lu > 100ms, capping", turnaround_us);
    turnaround_us = 100000;  // 上限 100ms
}
```

### P2-3: ParseModbusResponse 注册表

```go
type ResponseParser interface {
    Parse(rawData []byte) (value float64, unit string, err error)
}

var registry = map[string]ResponseParser{}

func RegisterParser(name string, p ResponseParser) { registry[name] = p }

func init() {
    RegisterParser("modbus_uint16", &ModbusUint16Parser{})
    RegisterParser("modbus_uint16_div10", &ModbusUint16Div10Parser{})
    RegisterParser("modbus_int16", &ModbusInt16Parser{})
    RegisterParser("modbus_uint32", &ModbusUint32Parser{})
    RegisterParser("modbus_float32", &ModbusFloat32Parser{})
}
```

### P2-4: write 操作用操作对验证（替代 verify_read schema）

```json
{
    "clear_rainfall": {
        "type": "write",
        "command_template": "{addr:02X}0601000000{crc}",
        "read_size": 0,
        "verify_operation": "read_rainfall"
    },
    "read_rainfall": {
        "type": "read",
        "command_template": "{addr:02X}0301000001{crc}",
        "read_size": 7,
        "response_parser": "modbus_uint16",
        "unit": "mm"
    }
}
```

后端 write 成功后自动触发 verify_operation，无需新 schema。

### P2-5: pendingWrite 优雅关闭

```go
func (m *Manager) Shutdown(timeout time.Duration) {
    m.mu.Lock()
    for reqID, entry := range m.pending {
        entry.resolve(&Response{Success: false, ErrorMsg: "server shutting down"})
        delete(m.pending, reqID)
    }
    m.mu.Unlock()
}
```

### P2-6: buildHashData 排序确定性

```go
// manager.go — 所有 Find 查询加 ORDER BY
tx.Order("id ASC").Where("node_id = ?", nodeID).Find(&channels)
tx.Order("id ASC").Where("node_id = ? AND enabled = true", nodeID).Find(&edgeDevices)
tx.Order("id ASC").Where("node_id = ?", node.NodeID).Find(&templates)
```

### P2-7: EdgeDevice.Type 冗余字段处理（v2.1 新增）

**问题**: `models.go` 第 74 行 `Type string` 注释标注"从 DeviceConfig.DeviceType 同步"，但无同步机制。如果 DeviceConfig.DeviceType 变更，EdgeDevice.Type 可能不一致。

**方案 — 去掉冗余字段，改为 JOIN 查询**:

```go
// models.go — 标记 Type 字段为 deprecated
type EdgeDevice struct {
    // ...
    Type string `json:"type" gorm:"column:type"` // deprecated: use DeviceConfig.DeviceType via JOIN
    // ...
}

// 查询时 JOIN 获取真实 type
func (m *Manager) GetEdgeDeviceWithConfig(id uint) (*EdgeDeviceWithConfig, error) {
    var result EdgeDeviceWithConfig
    err := m.db.Table("edge_devices").
        Select("edge_devices.*, device_configs.device_type as config_device_type").
        Joins("LEFT JOIN device_configs ON edge_devices.device_config_id = device_configs.id").
        Where("edge_devices.id = ?", id).
        Scan(&result).Error
    return &result, err
}

// 迁移策略:
// 1. 短期: 保留 Type 字段，在 DeviceConfig 更新时同步更新 EdgeDevice.Type
// 2. 中期: 前端改用 config_device_type，Type 字段标记 deprecated
// 3. 长期: 确认无引用后删除 Type 字段
```

---

## 六、P3 远期优化（详细方案）

### P3-1: 调度器高频采集优化

**现状分析**（scheduler.c:474）：
```c
vTaskDelayUntil(&wake, pdMS_TO_TICKS(10));  // 10ms tick
```

**问题**：
- 最小调度间隔 10ms，无法支持 <10ms 的高频采集
- **错误假设**：之前认为 UART 受 Modbus RTU 物理限制（~24ms/事务）
- **实际情况**：UART 是通用物理层（TTL/RS-485/RS-232），协议特性应从配置读取

**UART 物理层 vs 协议层**：
```
物理层（UART）:
  - TTL UART: 全双工，无 turnaround，可高频采集
  - RS-232: 全双工，无 turnaround，可高频采集
  - RS-485: 半双工，需要 turnaround（仅 Modbus RTU 等协议需要）

协议层（Modbus RTU / 自定义）:
  - turnaround delay: 协议特性，非物理层特性
  - 帧间隔: 协议特性，非物理层特性
```

**改进方案 — 配置驱动的通用调度器**：

```c
// DeviceConfig.Connection 新增字段
{
    "uart": {
        "baud_rate": 115200,
        "physical_layer": "ttl",      // "ttl", "rs232", "rs485"
        "duplex_mode": "full",        // "full" (TTL/RS-232), "half" (RS-485)
        "turnaround_us": 0,           // 0=auto, >0=manual, -1=none
        "min_interval_ms": 1          // 最小调度间隔（协议层决定）
    }
}

// scheduler.c — 动态 tick 选择（所有总线类型统一）
static void scheduler_task(void *p) {
    TickType_t wake = xTaskGetTickCount();
    
    while (s_running) {
        // 检查是否有快速通道（任何总线类型）
        bool has_fast_channel = false;
        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            if (s_channels[i].active && 
                s_channels[i].config.interval_ms < 100) {
                has_fast_channel = true;
                break;
            }
        }
        
        TickType_t tick_ms = has_fast_channel ? 1 : 10;  // 1ms 或 10ms
        vTaskDelayUntil(&wake, pdMS_TO_TICKS(tick_ms));
        
        // ...existing scheduling logic...
    }
}
```

**turnaround delay 配置化**（bus_worker.c）：
```c
// 从 DeviceConfig.Connection 读取 turnaround_us
uint32_t turnaround_us = ctx->cfg.uart.turnaround_us;

if (turnaround_us == 0) {
    // Auto: 根据 duplex_mode 决定
    if (ctx->cfg.uart.duplex_mode == DUPLEX_HALF) {
        turnaround_us = 38500000UL / ctx->cfg.uart.baud;
    } else {
        turnaround_us = 0;
    }
} else if (turnaround_us == (uint32_t)-1) {
    turnaround_us = 0;
}

if (turnaround_us > 100000) {
    ESP_LOGW(TAG, "turnaround_us=%lu > 100ms, capping", turnaround_us);
    turnaround_us = 100000;
}

if (turnaround_us > 10000) {
    vTaskDelay(pdMS_TO_TICKS((turnaround_us + 999) / 1000));
} else if (turnaround_us > 1000) {
    ets_delay_us(turnaround_us);
}
```

**触发条件**：任何总线需要 <100ms 采集间隔时

**收益**：
- TTL UART / RS-232 全双工：高频采集从 ~100Hz 提升到 ~1000Hz（1ms tick）
- RS-485 半双工：仍受 turnaround 限制，但 turnaround 从配置读取
- 协议无关：UART 作为通用物理层，不假设特定协议

---

### ~~P3-2~~: 帧分隔机制 → 已提升至 P1-7

> **已提升至 P1-7**：GB3024 光伏逆变器（ASCII `\r` 分隔）和嘉佰达 BMS（二进制 `0xDD...0x77` 起止标记+长度字段）即将接入，四种帧分隔模式全部必需。详见 P1-7 方案。

---

### P3-3: JSON 定义解析规则

**现状分析**（template_engine.go:237-262）：
```go
func ParseModbusResponse(rawData []byte, parser string, unit string) (float64, string, error) {
    switch parser {
    case "modbus_uint16":
        // ...
    case "modbus_uint16_div10":
        // ...
    default:
        return 0, "", fmt.Errorf("unknown parser: %s", parser)
    }
}
```

**改进方案 — JSON 驱动解析**：

```go
// template_engine.go — JSON 解析规则
type ParserRule struct {
    Type       string  `json:"type"`         // "modbus_register", "raw_bytes", "ascii"
    ByteOffset int     `json:"byte_offset"`  // 数据起始偏移
    ByteLength int     `json:"byte_length"`  // 数据长度
    DataType   string  `json:"data_type"`    // "uint16", "int32", "float32", "ascii"
    Endian     string  `json:"endian"`       // "big", "little"
    Scale      float64 `json:"scale"`        // 缩放因子（1.0 = 无缩放）
    Offset     float64 `json:"offset"`       // 偏移量（0.0 = 无偏移）
    Unit       string  `json:"unit"`         // 单位
}

func ParseResponse(ruleJSON json.RawMessage, rawData []byte) (float64, string, error) {
    var rule ParserRule
    if err := json.Unmarshal(ruleJSON, &rule); err != nil {
        return 0, "", fmt.Errorf("invalid parser rule: %w", err)
    }
    
    // 边界检查
    if rule.ByteOffset+rule.ByteLength > len(rawData) {
        return 0, "", fmt.Errorf("data too short: need %d bytes, got %d", 
            rule.ByteOffset+rule.ByteLength, len(rawData))
    }
    
    // 提取字节
    data := rawData[rule.ByteOffset : rule.ByteOffset+rule.ByteLength]
    
    // 按类型解析
    var value float64
    switch rule.DataType {
    case "uint16":
        if rule.Endian == "big" {
            value = float64(uint16(data[0])<<8 | uint16(data[1]))
        } else {
            value = float64(uint16(data[1])<<8 | uint16(data[0]))
        }
    case "int16":
        var v int16
        if rule.Endian == "big" {
            v = int16(uint16(data[0])<<8 | uint16(data[1]))
        } else {
            v = int16(uint16(data[1])<<8 | uint16(data[0]))
        }
        value = float64(v)
    case "uint32":
        if rule.Endian == "big" {
            value = float64(uint32(data[0])<<24 | uint32(data[1])<<16 | 
                          uint32(data[2])<<8 | uint32(data[3]))
        } else {
            value = float64(uint32(data[3])<<24 | uint32(data[2])<<16 | 
                          uint32(data[1])<<8 | uint32(data[0]))
        }
    case "float32":
        bits := binary.BigEndian.Uint32(data)
        if rule.Endian == "little" {
            bits = binary.LittleEndian.Uint32(data)
        }
        value = float64(math.Float32frombits(bits))
    case "ascii":
        return 0, string(data), nil
    default:
        return 0, "", fmt.Errorf("unsupported data_type: %s", rule.DataType)
    }
    
    // 应用缩放和偏移
    value = value*rule.Scale + rule.Offset
    
    return value, rule.Unit, nil
}
```

**触发条件**：解析器超过 10 个，或需要支持非 Modbus 协议

---

### P3-4: pending write 持久化 (SQLite WAL)（v2.1 修正）

**v2.0 问题**: 引入 Redis 为极低概率场景（后端重启期间 ESP32 恰好返回响应）增加新依赖，ROI 不高。

**v2.1 修正**: 用 SQLite WAL 替代 Redis，不引入新依赖。

```go
// pendingwrite.go — SQLite WAL 持久化
type Manager struct {
    mu      sync.RWMutex
    pending map[uint32]*Entry
    nextID  uint32
    sender  Sender
    db      *sql.DB  // 复用现有 SQLite 连接（WAL 模式）
}

type PersistedEntry struct {
    RequestID   uint32
    DeviceID    string
    ChannelID   uint32
    Data        []byte
    ReadSize    uint32
    SentAt      time.Time
    TimeoutAt   time.Time
    OperationID string
}

func (m *Manager) persistEntry(entry *PersistedEntry) error {
    _, err := m.db.Exec(`
        INSERT OR REPLACE INTO pending_writes 
        (request_id, device_id, channel_id, data, read_size, sent_at, timeout_at, operation_id)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
        entry.RequestID, entry.DeviceID, entry.ChannelID, entry.Data,
        entry.ReadSize, entry.SentAt, entry.TimeoutAt, entry.OperationID)
    return err
}

func (m *Manager) removePersistedEntry(requestID uint32) error {
    _, err := m.db.Exec(`DELETE FROM pending_writes WHERE request_id = ?`, requestID)
    return err
}

func (m *Manager) recoverPendingEntries() error {
    rows, err := m.db.Query(`
        SELECT request_id, device_id, channel_id, data, read_size, sent_at, timeout_at, operation_id
        FROM pending_writes WHERE timeout_at > ?`, time.Now())
    if err != nil {
        return err
    }
    defer rows.Close()
    
    for rows.Next() {
        var entry PersistedEntry
        if err := rows.Scan(&entry.RequestID, &entry.DeviceID, &entry.ChannelID,
            &entry.Data, &entry.ReadSize, &entry.SentAt, &entry.TimeoutAt, &entry.OperationID); err != nil {
            continue
        }
        
        // 恢复到内存
        m.pending[entry.RequestID] = &Entry{
            Response:  make(chan *Response, 1),
        }
    }
    
    // 清理已超时的条目
    m.db.Exec(`DELETE FROM pending_writes WHERE timeout_at <= ?`, time.Now())
    
    return nil
}
```

**建表 SQL**:
```sql
CREATE TABLE IF NOT EXISTS pending_writes (
    request_id   INTEGER PRIMARY KEY,
    device_id    TEXT NOT NULL,
    channel_id   INTEGER NOT NULL,
    data         BLOB,
    read_size    INTEGER DEFAULT 0,
    sent_at      DATETIME NOT NULL,
    timeout_at   DATETIME NOT NULL,
    operation_id TEXT
);
-- WAL 模式保证读写不互锁
PRAGMA journal_mode=WAL;
```

**优势 vs Redis**:
- 不引入新依赖（后端已有 SQLite）
- WAL 模式读写不互锁，性能足够
- 嵌入式友好（如果后端也跑在边缘设备上）

---

### P3-5: MQTT QoS 2 for critical ops

**现状分析**（mqtt.go:85, sender.go:61）：
```go
// mqtt.go:85
token := c.client.Publish(topic, 1, false, payload)  // QoS 1

// sender.go:61
enc.EncodeVarint(1, uint64(time.Now().UnixNano()))  // request_id
```

**改进方案 — QoS 2 + 应用层 ACK**：

```go
// mqtt.go — QoS 2 发布
func (c *Client) PublishQoS2(topic string, payload []byte) error {
    token := c.client.Publish(topic, 2, false, payload)  // QoS 2
    if !token.WaitTimeout(10 * time.Second) {
        return fmt.Errorf("QoS 2 publish timeout")
    }
    return token.Error()
}

// sender.go — 关键操作使用 QoS 2
func (m *Sender) SendWriteCommand(deviceID string, channelID uint32, 
    data []byte, readSize uint32, critical bool) error {
    
    enc := frame.NewEncoder(frame.MsgWriteCmd)
    enc.EncodeVarint(1, uint64(time.Now().UnixNano()))
    enc.EncodeVarint(2, uint64(channelID))
    enc.EncodeBytes(3, data)
    if readSize > 0 {
        enc.EncodeVarint(4, uint64(readSize))
    }
    
    topic := fmt.Sprintf("nodes/%s/down", deviceID)
    if critical {
        return m.mqtt.PublishQoS2(topic, enc.Bytes())
    }
    return m.mqtt.Publish(topic, enc.Bytes())
}

// handler_edge_device.go — 关键操作标记
func (h *Handler) handleExecute(c *gin.Context) {
    // ...
    critical := opConfig.Critical || opConfig.Type == "write"
    err := h.nodeMgr.SendWriteCommand(deviceID, chID, writeData, readSize, critical)
    // ...
}
```

**ESP32 应用层 ACK**（可选）：
```c
// msg_handler.c — 收到 WriteCmd 后立即发送 WriteRsp
static void handle_write_cmd(const uint8_t *data, size_t len) {
    // ...解析...
    
    // 立即发送 WriteRsp（应用层 ACK）
    send_write_rsp(request_id, true, 0, NULL);
    
    // 然后执行总线操作
    bus_manager_on_write_cmd(...);
}
```

**触发条件**：写入操作需要幂等保证（金融、医疗场景）

**注意**：QoS 2 增加延迟（4 次握手），仅用于 critical 操作

---

### P3 总结

| # | 项目 | 触发条件 | 工作量 | 收益 |
|---|------|---------|--------|------|
| P3-1 | 调度器分级定时器 | SPI/I2C <100ms 采集 | 4h | 高频采集 10x |
| P3-2 | ~~帧分隔机制~~ → 已提升至 P1-7 | — | — | — |
| P3-3 | JSON 解析规则 | 解析器 >10 个 | 6h | 配置驱动 |
| P3-4 | pending write 持久化 (SQLite WAL) | 多实例部署 | 6h | 重启不丢失 |
| P3-5 | MQTT QoS 2 | 关键操作幂等 | 4h | exactly-once |

**总工作量**：~20h（2.5 天）

**实施顺序建议**：
1. P3-3（JSON 解析）— 扩展性最好
2. P3-1（分级定时器）— 仅 SPI/I2C 受益
3. P3-4（SQLite 持久化）— 多实例时需要
4. P3-5（QoS 2）— 关键场景才需要

---

---

## 七、ESP32 固件架构分析（v2.3 新增）

> 基于 esp32-collector/components/ + main/ 全量源码审读，结合当前业务需求（Modbus RTU + GB3024 光伏逆变器 + 嘉佰达 BMS）评估架构合理性。

### 7.0 现状架构拓扑

```
                         ┌──────────────┐
                         │  scheduler   │  components/scheduler/
                         │  纯定时器     │  523行, 10ms tick
                         └──────┬───────┘
                                │ xQueueSend(bus_cmd_t)
                    ┌───────────┼───────────┬──────────┐
                    ▼           ▼           ▼          ▼
              uart0_q      uart1_q      spi_q      i2c_q
                    │           │           │          │
        ┌───────────┴───────────┴───────────┴──────────┐
        │              bus_worker (main/)                │ 476行
        │  4×cmd_task + 1×rx_task                       │
        │  uart_cmd_loop / spi_i2c_cmd_loop / rx_task   │
        └───────────────────────┬───────────────────────┘
                                │ bus_dma_write/read/transact
                        ┌───────┴───────┐
                        │   bus_dma     │  components/bus_dma/
                        │   驱动层       │ 1100行
                        └───────────────┘

        ┌───────────────────────────────────────────────┐
        │           bus_manager (main/)                  │ 311行
        │  ctx池管理 + WriteCommand入队 + DMA分配        │
        │  derive_hw_id / reg_bus_channel / find_ctx     │
        └───────────────────────────────────────────────┘
```

### 7.1 合理之处

1. **三层解耦**: scheduler(调度) → bus_worker(执行) → bus_dma(驱动)，通过 bus_cmd_t + FreeRTOS Queue 连接，符合 SRP
2. **per-bus 独立队列+task**: UART0/UART1/SPI/I2C 各有 cmd_queue 和 cmd_task，不同总线不会互相阻塞
3. **pending_queue 解决多 edge_device 归属**: 旧版 per-channel 单 slot → 新版 FreeRTOS Queue，smart match 区分 CMD_WRITE(read_size>0) 和 CMD_SAMPLE(read_size=0)
4. **bus_dma_ctx_t union 统一三种总线**: UART/SPI/I2C 公用 init/write/read/transact API，调用方无需关心底层差异
5. **总线共享 ref_count 注册表**: UART/SPI/I2C 各自独立注册表，多 channel 共享物理端口时引用计数

### 7.2 需改进问题（7 项，按严重度排序）

---

#### 问题1 [P0]: bus_worker_suspend/resume 用 vTaskSuspend — 并发不安全

**严重度**: 高 | **对应方案**: P0-3

**代码位置**: `main/bus_worker.c:451-467`

**现状**:
```c
void bus_worker_suspend(void) {
    if (s_rx_task_h)  vTaskSuspend(s_rx_task_h);
    if (s_cmd_u0_h)   vTaskSuspend(s_cmd_u0_h);
    // ...
}
```

**风险**:
- vTaskSuspend 可能在 cmd_task 持有 tx_mutex 时触发 → **死锁**（mutex 永不释放）
- vTaskSuspend 可能在 rx_task 正处理 DMA buffer 时触发 → **数据丢失**（DMA 环形缓冲区溢出）
- vTaskResume 不保证恢复顺序，rx_task 可能先于 cmd_task 恢复，导致 RX 数据无 pending 可匹配

**与 P0-3 方案对齐**: counting semaphore(1,1) 或让 task 自行检查 suspend 标志，不使用 vTaskSuspend/Resume。

---

#### 问题2 [P1]: UART 帧分隔硬编码为 Modbus RTU — 无法支持 GB3024/BMS

**严重度**: 高 | **对应方案**: P1-7

**代码位置**: `main/bus_worker.c:332-428` (rx_task)

**现状**: rx_task 对 UART 数据没有任何帧分隔逻辑：
- 每次 `bus_dma_read` 返回什么就当一帧上报（`s_data_rpt_cb(ch, ts, 0, rx, n, ...)`)
- turnaround 硬编码 `38500000UL / baud`（Modbus RTU 3.5 字符间隔）
- 无法区分半帧/整帧/多帧粘包

**即将接入协议不兼容**:
- GB3024: ASCII `\r` 分隔，全双工无 turnaround → 当前逻辑会误切割或粘包
- 嘉佰达 BMS: `0xDD...0x77` 起止标记+长度字段 → 当前逻辑完全无法识别帧边界

**与 P1-7 方案对齐**: 需要协议无关帧分隔（timeout/delimiter/start_stop/fixed 四种模式），stream_rx_t 缓冲 + is_frame_complete() 检测。

---

#### 问题3 [P1]: turnaround delay 硬编码在 bus_worker — 不可配置

**严重度**: 中 | **对应方案**: P2-2

**代码位置**: `main/bus_worker.c:110-118, 176-188`

**现状**:
```c
uint32_t turnaround_us = 38500000UL / baud;
```
此公式仅对 Modbus RTU 成立。实际需求：
- Modbus RTU: `38500000 / baud`（3.5 字符间隔）— 现有
- GB3024: **0**（全双工，无需 turnaround）— 当前会错误延时
- 嘉佰达 BMS: **唤醒序列** 00000000 + 100ms — 当前不支持

**与 P2-2 方案对齐**: turnaround_us 应为 per-channel 配置项，从 DeviceConfig.Connection 读取，支持 0（全双工）、-1（自动计算）、正值（自定义）三种语义。

---

#### 问题4 [P1]: rx_task 无帧边界 → 数据误归属风险

**严重度**: 中 | **依赖**: 问题2 先解决

**代码位置**: `main/bus_worker.c:367-410`

**现状**: rx_task 收到 n 字节后，drain pending_queue 做 smart match。问题在于：
1. **粘包**: 一次 bus_dma_read 可能读到多帧数据，却只匹配一个 pending → 后续帧丢失或错误归因
2. **半帧**: 可能读到半帧数据，就当作完整帧上报 → 后端解析失败
3. **性能**: drain+requeue 算法 O(PENDING_QUEUE_DEPTH)，虽然 depth=10 可接受，但每 5ms 轮询一次，高频场景有开销

**根本解决**: 先做帧分隔（问题2），再逐帧匹配 pending。is_frame_complete() 返回一帧后再 drain pending_queue 匹配。

---

#### 问题5 [P2]: bus_worker/bus_manager 放在 main/ 而非 components/ — 架构一致性破坏

**严重度**: 中 | **影响**: 编译耦合、可测试性

**代码位置**: `main/bus_worker.c/h` + `main/bus_manager.c/h`（共 787 行）

**现状**: bus_dma 是 component，但消费它的 bus_worker 和管理它的 bus_manager 却在 main/。导致：
- main/ 包含 787 行业务逻辑，无法独立编译测试
- bus_worker.h 直接 `#include "app_state.h"`，将 worker 和全局状态绑定
- 设计文档（ESP32_ARCHITECTURE_DESIGN.md）的组件拓扑明确把 bus_manager 画在 components 层

**改进**: 将 bus_worker.c/h + bus_manager.c/h 迁移到 components/ 下，和 bus_dma 同级。bus_worker 只依赖 bus_dma + cmd_queue + scheduler 类型定义，不依赖 app_state_t 全局。

---

#### 问题6 [P2]: bus_manager_on_write_cmd 用 manifest 查 bus_type — 依赖过重

**严重度**: 低 | **影响**: 配置更新时序

**代码位置**: `main/bus_manager.c:100-106` (find_bus_type), `279` (调用处)

**现状**: `find_bus_type()` 遍历 manifest 找 bus_type，`bus_manager_on_write_cmd` 也调 `find_bus_type`。manifest 是运行时可变的（config_mgr 双缓冲），WriteCommand 到达时 manifest 刚切换可能拿到旧值。

**改进**: channel 注册时把 bus_type 存入 bus_dma_ctx_t（已有 bus_type 字段），后续直接查 ctx，不依赖 manifest。同时消除 scheduler.c 和 bus_manager.c 中重复的 `derive_uart_port` 逻辑（见问题7）。

---

#### 问题7 [P3]: derive_uart_port 重复实现 — 违反 DRY

**严重度**: 低 | **影响**: 代码维护

**代码位置**: `components/scheduler/scheduler.c:53-69` 和 `main/bus_manager.c:110-126`

**现状**: 两个函数逻辑几乎相同，都是查 hw_tables 映射 tx/rx pin → UART port。

**改进**: 在 hw_tables 组件中提供 `hw_tables_derive_uart_port(tx_pin, rx_pin)` 公共函数，两处调用。

### 7.3 改进优先级与方案映射

| 优先级 | 问题 | 工作量 | 映射方案 | 依赖 |
|--------|------|--------|---------|------|
| **P0** | 问题1: vTaskSuspend 并发安全 | 2h | P0-3 | 无 |
| **P1** | 问题2: 帧分隔机制 | 8h | P1-7 | 无 |
| **P1** | 问题3: turnaround 配置化 | 3h | P2-2 | 无 |
| **P1** | 问题4: rx_task 逐帧匹配 | 含在 P1-7 | P1-7 | 问题2 |
| **P2** | 问题5: bus_worker/manager 迁移 components/ | 4h | 新增 | 问题2先定接口 |
| **P2** | 问题6: bus_type 存入 ctx | 2h | 新增 | 问题5 |
| **P3** | 问题7: derive_uart_port 去重 | 1h | 新增 | 无 |

**新增项说明**:
- 问题5（迁移 components/）和问题6（bus_type 存 ctx）在原方案中未提及，属于架构改进新增项
- 问题7（DRY 违反）是低优先级代码质量改进

### 7.4 组件依赖关系图

```
main.c
 ├── app_state ──→ bus_dma, config_mgr, scheduler, transport, cmd_queue, dma_pool
 ├── app_callbacks ──→ bus_manager, bus_worker, scheduler, config_mgr, sync_manager, msg_handler
 ├── bus_manager ──→ bus_dma, config_mgr, dma_pool, hw_tables     ← main/
 ├── bus_worker  ──→ bus_manager, bus_dma, cmd_queue, scheduler, app_state  ← main/
 ├── hello_handshake ──→ msg_handler, config_mgr, sync_manager
 └── on_write_cmd → bus_manager_on_write_cmd

scheduler ──→ config_mgr, cmd_queue, bus_dma, hw_tables           ← components/
bus_dma    ──→ rgb_led, hw_tables, ESP-IDF drivers                ← components/
config_mgr ──→ frame_codec, dma_pool                              ← components/
```

**关键耦合问题**: bus_worker 依赖 app_state_t 全局，bus_manager 依赖 config_mgr manifest 查询。迁移到 components/ 需要先解耦这两个依赖（通过依赖注入或回调函数指针）。

---

## 八、新增防御机制

### 8.1 可观测性指标（全部 P 项共用）

```
# pendingWrite
pendingwrite_active_entries          — gauge: 当前等待中的 write 数
pendingwrite_timeout_total           — counter: 超时次数
pendingwrite_late_response_total     — counter: 迟到响应次数
pendingwrite_duration_seconds        — histogram: 响应延迟分布

# /execute
execute_request_total{type, status}  — counter: 请求计数
execute_read_duration_seconds        — histogram: read 操作延迟
execute_concurrent_active            — gauge: 当前并发数
execute_rate_limit_rejected_total    — counter: 限流拒绝数

# worker pool
worker_pool_queue_size               — gauge: 队列深度
worker_pool_overflow_total           — counter: 溢出次数
worker_pool_backpressure_block_total — counter: v2.1: backpressure 阻塞次数
worker_pool_process_duration_seconds — histogram: 处理延迟

# ESP32
pending_queue_depth_max              — gauge: pending 队列最大深度
rx_match_iterations                  — gauge: 匹配遍历次数
cmd_task_watchdog_reset_total        — counter: watchdog 重置次数
rx_timeout_total                     — counter: v2.1: RX 超时次数
```

### 8.2 /execute 限流

```go
// 设备级: 10 req/s
// 用户级: 100 req/min
// 全局: 20 并发 read
```

### 8.3 cmd_task watchdog（嵌入式新增）

```c
// main.c — 启用 watchdog
esp_task_wdt_init(10, true);  // 10 秒超时

// bus_worker.c — 每个 cmd_task 循环重置
while (1) {
    esp_task_wdt_reset();  // 防止硬件挂起导致任务卡死
    // ...existing loop...
}
```

---

## 九、实施路线图

### 第 1 周: P0
```
Day 1:  P0-1 pendingWrite sync.Once
Day 2:  P0-2 /execute timeout + context
Day 3:  P0-3 bus_worker suspend/resume (counting semaphore)
Day 4:  集成测试
Day 5:  回归测试 + 固件刷写验证
```

### 第 2 周: P1
```
Day 1:  P1-1 Modbus 异常匹配 (ESP32 + 精确匹配)
Day 2:  P1-2 change-address 安全校验 (白名单) + P1-3 并发互斥 (含清理)
Day 3:  P1-4 DB 查询外移 (含 backpressure) + P1-5 worker pool 扩容
Day 4:  P1-6 ESP32 RX 超时机制
Day 5:  P1-7 帧分隔 (delimiter + start_stop 模式 + 唤醒序列)
Day 6-7: 集成测试 + GB3024/BMS 接入验证
```

### 第 3-4 周: P2
```
Week 3: P2-1 ~ P2-4 + P2-8 bus_worker/manager 迁移 components/
Week 4: P2-5 ~ P2-7 + P2-9 bus_type 存入 ctx + 文档更新
```

---

## 十、回滚计划

每个 P 级别独立提交，支持单独回滚:
```
P0: feat(p0): pendingWrite sync.Once + execute timeout + bus suspend
P1: feat(p1): modbus exception + security + concurrency + worker pool + rx timeout
P2: feat(p2): maintainability improvements
```

回滚: `git revert <commit-hash>`

---

## 十一、变更明细

| # | 变更 | 原因 |
|---|------|------|
| 1 | P0-3 binary semaphore → counting semaphore(1,1) | v2.0 binary sem 有并发 bug：cmd_task Take+Give 会意外释放 suspend 持有的 sem |
| 2 | P1-2 `funcCode >= 0x05` 禁令 → 白名单 FC01-06 | v2.0 禁令误杀 change-address 自身（FC06） |
| 3 | P1-3 deviceMutexMap 加清理机制 | v2.0 locks map 只增不减，长期运行内存泄漏 |
| 4 | P1-4 无限制 goroutine → backpressure (max 50) | v2.0 无限制 spawn goroutine 可能 OOM |
| 5 | 文件名 write.go → pendingwrite.go | 代码验证：实际文件名 |
| 6 | CalcConfigHashForDevice 行号 69 → 245 | 代码验证：实际行号 |
| 7 | change-address "无校验" → "校验不充分" | 代码验证：实际有 1-247 范围校验 |
| 8 | 新增 P1-6 ESP32 RX 超时机制 | 评审"断点1"遗漏项：传感器无响应时前端只看到超时 |
| 9 | 新增 P2-7 EdgeDevice.Type 冗余字段处理 | 初稿提到但最终方案删除了，需明确处理策略 |
| 10 | P1-1 增加 slave addr + func code 精确匹配 | 多 pending 时避免异常响应错配 |
|| 11 | P3-2 四种帧分隔 → timeout + fixed 先行 | 降低实现复杂度，其余按需添加 |
|| 12 | P3-4 Redis → SQLite WAL | 不引入新依赖，ROI 更高 |
|| 13 | P3-2 提升至 P1-7（完整四种帧分隔） | GB3024/BMS 即将接入，四种模式全部必需 |
|| 14 | 新增嘉佰达 BMS 唤醒序列支持 | 协议要求 00000000 + 100ms |
|| 15 | 新增第七章 ESP32 固件架构分析 | 全量源码审读，7 项问题按严重度排序 |
|| 16 | 新增 P2 项: bus_worker/manager 迁移 components/ | 架构一致性，可测试性 |
|| 17 | 新增 P2 项: bus_type 存入 ctx 替代 manifest 查询 | 解耦 config_mgr 运行时依赖 |
|| 18 | 新增 P3 项: derive_uart_port 去重 | DRY 原则 |

---

**文档版本**: v2.3  
**基于**: v2.2 + ESP32 固件架构全量审读（scheduler/bus_dma/bus_worker/bus_manager）  
**关键改进 vs v2.0**: 
1. P0-3 并发 bug 修正（binary → counting semaphore）
2. P1-2 自相矛盾修正（禁令 → 白名单）
3. P1-3/P1-4 资源泄漏修正（清理机制 + backpressure）
4. 3 处代码行号/文件名修正
5. 2 个遗漏项补入（P1-6 RX 超时、P2-7 Type 冗余）
6. P3 简化（帧分隔先行2种、SQLite 替代 Redis）

**v2.2 → v2.3 变更**:
10. 新增第七章"ESP32 固件架构分析"：基于全量源码审读，7 项问题按严重度排序
11. 问题1(P0): vTaskSuspend 并发不安全 → 对齐 P0-3
12. 问题2(P1): 帧分隔硬编码 Modbus RTU → 对齐 P1-7
13. 问题3(P1): turnaround 不可配置 → 对齐 P2-2
14. 问题4(P1): rx_task 无帧边界 → 依赖 P1-7
15. 问题5(P2): bus_worker/manager 在 main/ 应迁移 components/ → 新增项
16. 问题6(P2): bus_type 依赖 manifest 查询应存入 ctx → 新增项
17. 问题7(P3): derive_uart_port 重复实现 → 新增项
