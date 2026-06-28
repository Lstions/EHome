# 嘉佰达 BMS 审核问题修复方案

> 基于 2026-06-28 全量代码审核（30 文件, +3726/-2719 行）制定的修复方案
> 状态：待审核 | 分支：jiabaida-bms-driver

---

## 问题总览

| # | 严重级别 | 问题 | 影响 |
|---|---------|------|------|
| S1 | 关键 | ConfigParser 静默产生错误 BMS 数据 | BMS 传感器数据全部错误 |
| S2 | 高 | rx_task 在部分帧时消耗 pending cmd | 命令关联丢失，无帧重组 |
| S3 | 高 | 后端无帧重组能力 | 部分帧被拒绝后丢弃 |
| S4 | 高 | PeripheralAssignForm 设备名退化 | 用户体验退化 |
| S5 | 中 | ChannelManager bus_config 向后不兼容 | 编辑旧通道表单为空 |
| S6 | 中 | 三份重复测试计划 + 两份设计文档 | 文档混乱 |
| S7 | 低 | 死代码 / 注释错误 / 空行 | 代码整洁 |

---

## S1 — 关键：ConfigParser 静默数据损坏

### 根因

`jiabaida_seed.sql` 为 DeviceConfig 定义了 9 个字段的 parser JSONB：

```json
{"data_format": "jiabaida_binary", "fields": [
  {"name": "total_voltage", "offset": 0, ...},
  {"name": "current",       "offset": 2, ...},
  ...
]}
```

字段 offset 值相对于 **数据负载**（29 字节，从帧头之后开始）。

但 `ConfigParser.Parse()` 对不认识的 `data_format`（"jiabaida_binary"）走 default 分支：
```go
// parser.go:107-108
default:
    data = raw
```

它收到的 `raw` 是**完整帧**（36 字节，含 0xDD 头部），导致所有字段偏移错位 4 字节。

ConfigParser 解析**成功**（9 个字段在范围内，类型匹配），`sensorData != nil`，
Driver 回退永远不被触发 → **静默数据损坏**。

### 模拟验证

```
total_voltage @offset 0 读取 [0xDD,0x03] = 0xDD03 = 56579 → 565.79V (正确: 521.30V)
current       @offset 2 读取 [0x00,0x1D] = 29     → 0.29A   (正确: 50.00A)
remaining     @offset 4 读取 [0xCB,0xB2] = 52146  → 521.46Ah (正确: 500.00Ah)
... 所有 9 个字段均错误
```

### 方案：移除 parser JSONB，让 Driver 处理

**原则**：复杂协议（BMS 变长字段、自定义校验和）应由 Go Driver 解析，不应走简单固定偏移的 ConfigParser。

**修改文件**：`backend/internal/drivers/jiabaida_seed.sql`

**修改内容**：将 parser 字段 JSONB 设为空 `{}`：

```sql
-- 前：
'{"data_format": "jiabaida_binary", "fields": [...]}'::jsonb,

-- 后：
'{}'::jsonb,
```

**数据流修复后**：
```
parseAndStoreData(rawData)
  ├── ConfigParser ← parser JSONB 为空 {} → NewConfigParser 报错 → sensorData = nil
  └── Driver 回退 ← drivers.Get("jiabaida_bms") → ParseData(rawData) → 正确解析
```

**验证方法**：
1. `go test ./backend/internal/drivers/ -count=1` 全部 PASS
2. 确认 BMS 实际数据上报后 `unified_data` 表数值正确
3. 前端 BMS 详情页显示正确的电压/电流/SOC 值

---

## S2 — 高：rx_task 部分帧时命令消耗

### 根因

P1-8 新 rx_task 逻辑（`bus_worker.c:396-410`）：

```c
if (s->len > 0 && has_pending_cmd(rt, i)) {
    pending_cmd_t pcmd;
    if (xQueuePeek(rt->pending_queues[i], &pcmd, 0) == pdTRUE) {
        s_data_rpt_cb(..., s->buffer, s->len, 0, pcmd.request_id, ...);
        xQueueReceive(rt->pending_queues[i], &pcmd, 0);  // 立即弹出!
        hits++;
    }
    s->len = 0;
}
```

UART 是通用传输层，设备响应长度从 5 字节（Modbus 异常）到 100+ 字节（BMS 0x0F 综合信息）不等。
BMS 36 字节响应在 9600bps 下传输 ≈ 37.5ms，rx_task 每 5ms 轮询，跨 7-8 个 DMA 读取周期。

第一次 DMA 读到 12 字节 → 立即弹出 pending cmd 并上报部分帧。
后续 DMA 读取没有 pending cmd 关联 → 字节累积在流缓冲区，等下一个调度命令。

**核心错误**：rx_task 把"有数据到达"等同于"响应已完成"，在数据还在 UART 线路上传输时就消耗了
pending command。无论什么设备、什么协议，只要响应超过一次 DMA 读取的长度，关联就断裂。

### 方案：不弹出 — rx_task 只 Peek 不下发，由 P1-6 超时统一清理

**原则**：
- rx_task 只负责两件事：① 累积 DMA 字节到流缓冲区，② 用 peek 获取关联元数据上报。
- **不弹出 pending cmd**。pending cmd 留在队列里，下次有数据继续关联。
- pending cmd 的退出只有一个路径：P1-6 超时（1000ms 无数据 → 发送 err=0x01 → 弹出）。
- 调度器下一个 CMD_SAMPLE 入队时，如果队列已满 → `xQueueSend(..., 0)` 非阻塞返回
  → 丢弃新条目（旧条目仍在，关联正确），仅打 WARNING 日志。无延迟。

```
rx_task 每 5ms:
  for each UART channel:
    n = bus_dma_read()     ← 读到 12 字节（部分 BMS 帧）
    追加到 s_streams[i]
    if s->len > 0 && has_pending_cmd?
      peek pending_cmd     ← 只读元数据
      DataReport(全部缓冲字节, request_id, edge_device_id)
      不弹出! s->len = 0  ← 清缓冲但命令保留

    --- 5ms 后 ---
    n = bus_dma_read()     ← 读到 24 字节（剩余帧）
    追加到 s_streams[i]
    if s->len > 0 && has_pending_cmd?
      peek 同一个 pending_cmd  ← 关联正确!
      DataReport(24 字节, 同一个 request_id)
      不弹出!

后端 stream_buffer (S3) 累积两个 DataReport → ParseData → 成功解析完整帧
```

**关键性质**：
- **零延迟**：不增加任何等待。数据到达即上报，不判断帧长、不等空闲超时。
- **通用**：3 字节还是 300 字节响应都适用。Modbus 异常（5 字节）一次 DMA 读完→一次上报→后端一次解析。
  BMS（36 字节）多次 DMA 读→多次上报→后端累积→一次解析。
- **零状态**：rx_task 不维护 per-channel 计时器、帧检测器、长度计数器。唯一的"状态"是
  P1-6 超时计时（原本就有）。
- **pending cmd 生命周期**：入队（cmd_task 发 TX 后）→ 被 rx_task peek 多次 →
  P1-6 超时弹出一一次（或 never，被下一个 CMD_SAMPLE 的 xQueueSend 满丢弃替代，但
  不 pop 旧的不影响正确性）。

**并发正确性**：同一通道多个 edge_device 共享 pending_queue。调度器按 channel 轮流发出
CMD_SAMPLE。第一个设备的 pending cmd 在队列头，后续设备的入队追加到队列尾。
rx_task 总是 peek 队头 → 始终关联第一个待响应设备。该设备超时后弹出，下一个设备
的 pending cmd 成为队头。FIFO 顺序天然正确。

**对现有行为的影响**：
- CMD_SAMPLE（request_id=0）：每次 DMA 读取上报一次 DataReport。后端累积后解析。
  同一个调度周期内多次 DMA 读取 = 多次 DataReport = 后端累积。
- CMD_WRITE（request_id≠0）：`worker_pool.go:72` 中 `HandleDataReportAck` 接收第一次
  DataReport 后立即 ack。后续 DataReport（部分帧的剩余字节）request_id 相同，但
  pendingWrite 已移除该 entry → `late DataReportAck` 日志（已在代码中处理，行 205）。

**修改文件**：`esp32-collector/components/bus_worker/bus_worker.c`
**修改内容**：删除第 406 行的 `xQueueReceive` 调用。仅此一行。

**补充**：配合 S3 后端流缓冲区，部分帧累积后正确解析。

---

## S3 — 高：后端无帧重组

### 当前行为

`parseAndStoreData()` 接收 `rawData` 后直接解析，无状态：
```go
func (m *Manager) parseAndStoreData(..., rawData []byte) ... {
    // ConfigParser.Parse(rawData) or driver.ParseData(rawData)
    // 失败 → return nil → 数据被丢弃
}
```

如果 rx_task 上报部分帧，`ParseData` 返回错误，数据被丢弃。下一个 DataReport 携带后续字节，但后端没有关联上下文。

### 方案：Node 级流缓冲区

在 `Manager` 中为每个 Node 维护一个 `[]byte` 流缓冲区：

```go
// backend/internal/nodemgr/stream_buffer.go

type streamBuffer struct {
    mu     sync.Mutex
    buf    []byte        // 累积的原始字节
    maxLen int           // 硬上限（防止内存泄漏）
    lastRx time.Time     // 上次接收时间
}

// Append 追加原始字节，返回合并后的缓冲区
func (sb *streamBuffer) Append(data []byte) []byte {
    sb.mu.Lock()
    defer sb.mu.Unlock()
    
    // 超时清理：超过 2 秒无数据 → 视为旧帧丢弃
    if time.Since(sb.lastRx) > 2*time.Second {
        sb.buf = nil
    }
    sb.lastRx = time.Now()
    
    sb.buf = append(sb.buf, data...)
    if len(sb.buf) > sb.maxLen {
        sb.buf = sb.buf[len(sb.buf)-sb.maxLen:]  // 保留尾部
    }
    return sb.buf
}

// Consume 移除已解析的前缀字节（调用方确认帧已完整解析后调用）
func (sb *streamBuffer) Consume(n int) {
    sb.mu.Lock()
    defer sb.mu.Unlock()
    if n > 0 && n <= len(sb.buf) {
        sb.buf = sb.buf[n:]
    }
}

// Reset 清空缓冲区
func (sb *streamBuffer) Reset() {
    sb.mu.Lock()
    defer sb.mu.Unlock()
    sb.buf = nil
}
```

**Manager 集成**：

```go
// backend/internal/nodemgr/manager.go
type Manager struct {
    // ...
    streamBufs map[string]*streamBuffer  // key = deviceID (node_id)
}

// worker_pool.go: processDataReportJob()
func (m *Manager) processDataReportJob(job dataReportJob) {
    // ... existing code ...
    
    if job.errorCode == 0 && job.rawData != nil {
        // 追加到 Node 流缓冲区
        sb := m.getOrCreateStreamBuffer(job.deviceID)
        merged := sb.Append(job.rawData)
        
        // 用合并后的数据解析（可能包含完整帧）
        parsedData := m.parseAndStoreData(collectorID, job.deviceID, 
            job.channelID, job.edgeDeviceID, merged)
        
        if parsedData != nil {
            // 解析成功 → 消耗已解析的字节
            // Driver 应报告消耗了多少字节（新增 API）
            // 临时方案：解析成功时清空整个缓冲区（单帧假设）
            sb.Reset()
        }
        // 解析失败 → 保留字节在缓冲区，等待下一次追加
    }
}
```

**Driver 接口扩展**（后续）：

```go
// SensorData 添加 ConsumedBytes 字段
type SensorData struct {
    Name          string
    Value         float64
    Unit          string
    ConsumedBytes int  // 此解析消耗的输入字节数
}
```

**优点**：
- 透明支持部分帧追加 → 完整帧解析
- 与 ESP32 P1-8 透明管道互补
- 向后兼容：流缓冲区为空时行为不变
- 2 秒空闲超时防止内存泄漏

**修改文件**：
- 新建 `backend/internal/nodemgr/stream_buffer.go`
- 修改 `backend/internal/nodemgr/manager.go`
- 修改 `backend/internal/nodemgr/worker_pool.go`

---

## S4 — 高：PeripheralAssignForm 设备名退化

### 根因

`PeripheralAssignForm.vue` 移除了 `deviceTypeNames` 映射表，`handleDeviceTypeChange` 改为：
```ts
form.device_name = v  // "jiabaida_bms" 而非 "BMS 电池管理系统"
```

### 方案：恢复 deviceTypeNames 映射

```ts
// PeripheralAssignForm.vue
const deviceTypeNames: Record<string, string> = {
  wind_speed: '风速传感器',
  wind_direction: '风向传感器',
  rain: '雨量传感器',
  light: '光照传感器',
  temp_humidity: '温湿度传感器',
  battery: '电池保护板',
  jiabaida_bms: 'BMS 电池管理系统',
  inverter: '光伏逆变器',
}

const handleDeviceTypeChange = async (v: string) => {
  form.template_id = undefined
  if (v && !form.device_name) {
    form.device_name = deviceTypeNames[v] || v  // 中文名优先
  }
  // ...
}
```

**修改文件**：`frontend-shared/src/components/forms/PeripheralAssignForm.vue`

---

## S5 — 中：ChannelManager bus_config 向后兼容

### 根因

`ChannelManager.vue` 编辑解析要求 `hex.length >= 22`（11 字节新格式），旧格式 10 个十六进制字符（5 字节）会导致表单字段为空。

### 方案：长度自适应解析

```ts
if (form.hardware_type === 'uart' && typeof editingChannel.value.bus_config === 'string') {
  const hex = editingChannel.value.bus_config
  if (!hex.startsWith('{')) {
    try {
      // 新格式 (>= 22 chars = 11 bytes): 含 data_bits/stop_bits/parity/flow
      // 旧格式 (>= 10 chars = 5 bytes):  仅 tx/rx/baud
      const tx = parseInt(hex.substring(0, 2), 16)
      const rx = parseInt(hex.substring(2, 4), 16)
      if (!form.config.tx_pin) form.config.tx_pin = tx
      if (!form.config.rx_pin) form.config.rx_pin = rx
      
      if (hex.length >= 12) {
        const baud = parseInt(hex.substring(4, 12), 16)
        if (!form.config.baud_rate) form.config.baud_rate = baud
      }
      // 新格式有额外字段，旧格式使用默认值
    } catch { /* ignore */ }
  }
}
// 始终为旧格式提供默认值
if (form.hardware_type === 'uart') {
  if (!form.config.data_bits) form.config.data_bits = 8
  if (!form.config.stop_bits) form.config.stop_bits = 1
  if (!form.config.parity) form.config.parity = 'none'
  if (!form.config.flow_control) form.config.flow_control = 'none'
}
```

**修改文件**：`frontend-shared/src/components/channel/ChannelManager.vue`

---

## S6 — 中：文档整合

### 问题

三份测试计划内容重叠：
- `TEST_IMPROVEMENT_PLAN.md`（182 行，详细评分体系）
- `test-improvement-plan.md`（151 行，10 维度评分）
- `docs/plans/test-improvement-plan.md`（78 行，执行清单）

两份架构设计文档：
- `docs/plans/2026-06-27-device-architecture-centralization-design.md`（v1, 614 行）
- `docs/plans/2026-06-27-device-architecture-centralization-design-v2.md`（v2, 780 行）

### 方案

1. 以 `TEST_IMPROVEMENT_PLAN.md` 为权威版本，合并其他两份内容
2. 删除 `test-improvement-plan.md` 和 `docs/plans/test-improvement-plan.md`
3. 删除 v1 设计文档，保留 v2 作为权威版本
4. 在 v2 文档开头添加 `> 取代 v1 版本` 声明

**执行**：
```bash
# 合并测试计划（手动编辑后）
git rm test-improvement-plan.md
git rm docs/plans/test-improvement-plan.md
git rm docs/plans/2026-06-27-device-architecture-centralization-design.md
```

---

## S7 — 低：代码整洁

| 文件 | 行号 | 问题 | 修复 |
|------|------|------|------|
| `bus_dma.c` | 1057 | `return ESP_OK;` 前多余空格 | 删除空格 |
| `scheduler.c` | 360 | 注释 "P1-7" | 改为 "P1-8" |
| `bus_worker.h` | 104 | `MAX_FRAME_SIZE 2048` 未使用 | 直接删除 |
| `App.vue` | 2 | 移除 `:theme="theme"` 后暗色模式 | 需浏览器验证；应恢复为显式设置 |

### App.vue 主题修复

移除 `:theme` 后如果暗色模式不工作：

```vue
<!-- App.vue -->
<el-config-provider :locale="locale" :theme="themeMode">
```

```ts
const themeMode = computed(() => themeStore.mode === 'dark' ? 'dark' : undefined)
```

---

## 执行优先级

| 优先级 | 问题 | 预估工作量 | 依赖 |
|--------|------|-----------|------|
| P0 | S1 ConfigParser 数据损坏 | 10 min（改 1 个 SQL 值） | 无 |
| P0 | S4 PeripheralAssignForm 名称退化 | 15 min | 无 |
| P1 | S2 rx_task 不弹出 pending cmd | 5 min（删 1 行） | 需 ESP32 编译环境 |
| P1 | S3 后端流缓冲区 | 2 hr | 无 |
| P2 | S5 ChannelManager 向后兼容 | 20 min | 无 |
| P2 | S6 文档整合 | 15 min | 无 |
| P3 | S7 代码整洁 | 10 min | 无 |

---

## 验证标准

| 问题 | 验证方法 |
|------|---------|
| S1 | `go test ./backend/internal/drivers/ -count=1` 全部 PASS；BMS 设备上报数据在 `unified_data` 表中数值正确 |
| S2 | ESP32 日志中不再出现部分帧的 DataReport（帧长为 7 以下的报告消失） |
| S3 | 模拟 rx_task 分两次发送 BMS 帧 → 后端合并后正确解析 |
| S4 | 打开外设分配表单，选择 "BMS 电池管理系统" → 设备名自动填充中文 |
| S5 | 编辑旧格式 bus_config 的 UART 通道 → 表单字段正常显示默认值 |
| S6 | `find docs/ -name "test-improvement-plan.md"` 只有 1 个结果 |
| S7 | `grep "MAX_FRAME_SIZE" esp32-collector/ -r` 只有 1 个定义处且有使用 |
