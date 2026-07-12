# 通道数据流事件总线架构 — 设计方案

## 0. 需求说明

### 0.1 背景与问题

当前通道数据流（DataReport）在后端 `processDataReportJob` 中是同步串行处理，所有数据都经过完整的重型流水线：

```
MQTT handler → dataCh → worker pool → processDataReportJob:
  1. termMgr.RecordRX          (终端记录)
  2. lookupCollectorID         (DB 查询)
  3. db.Create DeviceData      (DB 写入)
  4. parseAndStoreData:        (重量级)
     a. findEdgeDeviceByChannelID  (2次 DB 查询)
     b. DeviceConfig.Parser 查询   (DB 查询)
     c. Driver 解析
     d. unified_data 批量写入      (DB 写入)
     e. edge_device 状态更新       (DB 写入)
     f. HA publish
     g. DataUpdate WS broadcast
  5. findEdgeDeviceByChannelID (又一次 DB 查询)
  6. BroadcastEvent ChannelData (WebSocket 推送)
```

问题：
- 被动数据（request_id=0，通道终端裸字节）也走完整流水线，执行 5+ 次 DB 查询和 3+ 次 DB 写入，但这些操作对终端显示毫无意义
- 传感器解析、DB 存储、HA 推送、WebSocket 推送耦合在一个函数中，无法独立启停或按数据类型跳过
- 高频数据场景下 DB IO 成为瓶颈，拖慢 WebSocket 推送延迟
- 新增消费需求（如数据导出 API、外部 Webhook）需要修改 processDataReportJob，违反开闭原则

### 0.2 功能需求

| 编号 | 需求 | 优先级 | 说明 |
|------|------|--------|------|
| R1 | 生产者消费者解耦 | P0 | MQTT handler 只负责解析帧并投递总线，不关心下游消费方式 |
| R2 | 消费者可插拔 | P0 | 新增消费者不改现有代码（开闭原则） |
| R3 | 消费者独立启停 | P0 | 每个消费者可按数据类型决定是否处理 |
| R4 | 被动数据快速通道 | P0 | request_id=0 的被动数据跳过传感器解析和 DB 存储，只推终端 |
| R5 | 消费者隔离 | P0 | 单个消费者 panic 或慢处理不影响其他消费者 |
| R6 | 背压控制 | P0 | 数据洪峰时丢弃旧数据保留新数据 |
| R7 | 现有功能不变 | P0 | 终端记录、传感器解析、DB 存储、HA 推送、WS 推送等现有行为保持一致 |

### 0.3 约束条件

- 不改变 ESP32 固件协议（DataReport 0x03 帧格式不变）
- 不改变前端 WebSocket 事件格式（channel_data / data_update 事件不变）
- 复用现有 worker pool 机制（dataCh + goroutine），在其内部重构
- 传感器解析依赖 DB 查询（需查 edge_device + device_config），无法完全消除 DB IO，但可对被动数据跳过

## 1. 整体架构

```
┌───────────────────────────────────────────────────────────────────┐
│                         前端 (Vue3)                                │
│  ChannelTerminal (channel_data) / Dashboard (data_update)         │
└──────────────────────┬────────────────────────────────────────────┘
                       │ WebSocket
┌──────────────────────┴────────────────────────────────────────────┐
│                        后端 (Go)                                   │
│                                                                    │
│  ┌───────────────┐                                                │
│  │ MQTT Handler  │  DataReport 0x03                               │
│  │ (生产者入口)  │──────┐                                         │
│  └───────────────┘      │                                         │
│                         ▼                                         │
│              ┌──────────────────────┐                             │
│              │  DataEventBus        │  ← 数据事件总线              │
│              │  (中间件)            │                             │
│              │                      │                             │
│              │  Publish(dataEvent)  │                             │
│              │  Register(consumer)  │                             │
│              └──┬───┬───┬───┬───────┘                             │
│                 │   │   │   │                                      │
│     ┌───────────┘   │   │   └─────────────┐                       │
│     ▼               ▼   ▼                 ▼                       │
│ ┌────────┐  ┌──────────┐ ┌──────────┐ ┌────────────┐             │
│ │Terminal│  │WS Push   │ │Sensor    │ │DB Persist  │             │
│ │Consumer│  │Consumer  │ │Parser    │ │Consumer    │             │
│ │        │  │          │ │Consumer  │ │            │             │
│ │RecordRX│  │channel_  │ │          │ │DeviceData  │             │
│ │+ WS    │  │data事件  │ │parse+    │ │写入        │             │
│ │term_   │  │          │ │store+    │ │            │             │
│ │event   │  │          │ │HA+WS     │ │            │             │
│ └────────┘  └──────────┘ └──────────┘ └────────────┘             │
│  始终活跃     始终活跃    仅调度数据    仅调度数据                   │
│  所有数据     所有数据    request_id≠0  request_id≠0               │
│              快速通道    edge_dev≠0    error_code=0                │
│                                                                    │
│  消费者各自判断是否处理当前数据事件                                 │
│  被动数据(request_id=0)只走 Terminal + WS Push → 跳过 DB IO       │
└──────────────────────────────────────────────────────────────────-─┘
```

## 2. 核心数据结构

### 2.1 DataEvent

```go
// DataEvent 表示一条从 ESP32 收到的数据报告事件
// 由 MQTT handler 解析帧后创建，投递到 DataEventBus
type DataEvent struct {
    DeviceID     string    // 采集器 node_id
    ChannelID    uint64    // 通道 ID
    Timestamp    uint64    // ESP32 微秒时间戳
    Sequence     uint64    // 序列号
    RawData      []byte    // 原始数据
    ErrorCode    uint64    // 错误码 (0=正常, 0x01=RX超时)
    RequestID    uint64    // 请求 ID (0=被动数据/终端模式)
    EdgeDeviceID uint64    // 边缘设备 ID (0=未知)
    CommandIndex uint64    // 命令索引
    ReceivedAt   time.Time // 后端收到时间
}

// IsPassive 判断是否为被动数据（通道终端裸字节，无对应命令）
func (e *DataEvent) IsPassive() bool {
    return e.RequestID == 0 && e.EdgeDeviceID == 0
}

// IsCommandResponse 判断是否为命令响应（调度器发出的采集命令的响应）
func (e *DataEvent) IsCommandResponse() bool {
    return e.RequestID != 0
}

// IsError 判断是否为错误报告
func (e *DataEvent) IsError() bool {
    return e.ErrorCode != 0
}
```

### 2.2 DataConsumer 接口

```go
// DataConsumer 是所有数据消费者的统一接口
type DataConsumer interface {
    Name() string              // 消费者名称（唯一标识）
    ShouldHandle(evt DataEvent) bool  // 判断是否处理该事件
    Handle(evt DataEvent)      // 处理事件
}
```

### 2.3 DataEventBus 中间件

```go
// DataEventBus 解耦 MQTT handler（生产者）和数据消费者。
// 与 LogEventBus 架构一致：通道缓冲 + dispatch goroutine + 消费者隔离。
type DataEventBus struct {
    mu        sync.RWMutex
    consumers []DataConsumer
    dataChan  chan DataEvent     // 有缓冲通道
    wg        sync.WaitGroup
    dropped   atomic.Uint64
    stopCh    chan struct{}
}
```

## 3. 消费者设计

### 3.1 Terminal Consumer — 终端记录 + WebSocket 推送

**职责**：记录 RX 数据到终端管理器，推送 terminal 事件到 WebSocket。

**处理条件**：`ShouldHandle` 始终返回 true（所有数据都记录到终端）。

```go
type TerminalConsumer struct {
    termMgr *terminal.Manager
    wsHub   *websocket.Hub
}

func (c *TerminalConsumer) ShouldHandle(evt DataEvent) bool { return true }

func (c *TerminalConsumer) Handle(evt DataEvent) {
    // 1. 记录到终端管理器（已有逻辑，含 terminal WS event）
    c.termMgr.RecordRX(evt.DeviceID, uint(evt.ChannelID), evt.RawData)
}
```

**特点**：最轻量，无 DB 操作。被动数据和命令数据都处理。

### 3.2 WS Push Consumer — channel_data 事件推送

**职责**：构建 channel_data WebSocket 事件并广播到前端。

**处理条件**：始终返回 true（所有数据都推 channel_data 事件）。

```go
type WSPushConsumer struct {
    wsHub *websocket.Hub
}

func (c *WSPushConsumer) ShouldHandle(evt DataEvent) bool { return true }

func (c *WSPushConsumer) Handle(evt DataEvent) {
    event := map[string]interface{}{
        "device_id":      evt.DeviceID,
        "node_id":        evt.DeviceID,
        "channel_id":     evt.ChannelID,
        "raw_hex":        fmt.Sprintf("%x", evt.RawData),
        "timestamp":      time.Now().Unix(),
        "error_code":     evt.ErrorCode,
        "request_id":     evt.RequestID,
        "edge_device_id": evt.EdgeDeviceID,
        "command_index":  evt.CommandIndex,
    }
    c.wsHub.BroadcastEvent(events.ChannelData, event)
}
```

**特点**：无 DB 操作，纯内存。这是被动数据的快速通道——终端用户看到的数据只经过 Terminal Consumer + WS Push Consumer 两个轻量消费者。

### 3.3 Sensor Parser Consumer — 传感器解析 + 存储 + HA 推送

**职责**：解析传感器数据，写入 unified_data，更新 edge_device 状态，推送 data_update 事件，HA publish。

**处理条件**：仅处理命令响应数据（request_id≠0）且无错误。

```go
type SensorParserConsumer struct {
    db    *gorm.DB
    wsHub *websocket.Hub
    ha    *homeassistant.Integration
    // reassembler 需要 Manager 注入或共享
}

func (c *SensorParserConsumer) ShouldHandle(evt DataEvent) bool {
    // 被动数据跳过传感器解析
    // 错误数据跳过
    // 只处理命令响应（调度器采集数据）
    return evt.RequestID != 0 && evt.ErrorCode == 0 && len(evt.RawData) > 0
}

func (c *SensorParserConsumer) Handle(evt DataEvent) {
    // 原 parseAndStoreData 逻辑搬到这里：
    // 1. findEdgeDeviceByChannelID
    // 2. DeviceConfig.Parser / Driver 解析
    // 3. unified_data 批量写入
    // 4. edge_device 状态更新
    // 5. HA publish
    // 6. DataUpdate WS broadcast
    // 7. 返回 parsedData（通过 event 传递给 WS Push Consumer 补充）
}
```

**特点**：最重量级，包含 5+ DB 查询和 3+ DB 写入。但只对调度器采集的命令响应数据执行，被动数据完全跳过。

### 3.4 DB Persist Consumer — 原始数据持久化

**职责**：将原始数据写入 device_data 表（审计/历史记录）。

**处理条件**：仅处理命令响应数据（被动数据不需要持久化到 device_data）。

```go
type DBPersistConsumer struct {
    db *gorm.DB
}

func (c *DBPersistConsumer) ShouldHandle(evt DataEvent) bool {
    return evt.RequestID != 0  // 被动数据不持久化
}

func (c *DBPersistConsumer) Handle(evt DataEvent) {
    dataJSON, _ := json.Marshal(map[string]interface{}{
        "raw":            fmt.Sprintf("%x", evt.RawData),
        "channel":        evt.ChannelID,
        "sequence":       evt.Sequence,
        "error_code":     evt.ErrorCode,
        "request_id":     evt.RequestID,
        "edge_device_id": evt.EdgeDeviceID,
        "command_index":  evt.CommandIndex,
    })
    c.db.Create(&models.DeviceData{
        NodeID:    evt.DeviceID,
        DataJSON:  string(dataJSON),
        Timestamp: evt.ReceivedAt,
    })
}
```

### 3.5 PendingWrite Consumer — 命令响应路由

**职责**：将命令响应路由到 pendingWrite 管理器（用于 WriteCmd 确认和设备初始化）。

**处理条件**：仅处理有 request_id 的数据或错误报告。

```go
type PendingWriteConsumer struct {
    pendingWrite *pendingwrite.Manager
    deviceInit   *deviceinit.Orchestrator
    db           *gorm.DB
}

func (c *PendingWriteConsumer) ShouldHandle(evt DataEvent) bool {
    return evt.RequestID != 0
}

func (c *PendingWriteConsumer) Handle(evt DataEvent) {
    if evt.ErrorCode == 0x01 {
        // RX 超时
        c.pendingWrite.HandleResponse(uint32(evt.RequestID), false, 0x01, "sensor RX timeout")
        return
    }
    c.pendingWrite.HandleDataReportAck(uint32(evt.RequestID), evt.RawData)
    // device init 通知逻辑...
}
```

## 4. 数据流对比

### 4.1 当前架构（串行同步）

```
DataReport 到达
  │
  ▼
processDataReportJob (1个goroutine，串行执行全部):
  ├── termMgr.RecordRX         ~1μs
  ├── pendingWrite 处理        ~1μs (如果 request_id≠0 则 return)
  ├── lookupCollectorID        ~500μs (DB查询)
  ├── db.Create DeviceData     ~1ms (DB写入)
  ├── parseAndStoreData        ~5-20ms (5+ DB查询 + 3+ DB写入 + 解析 + HA)
  ├── findEdgeDeviceByChannelID ~1ms (DB查询)
  └── BroadcastEvent            ~100μs
  
  被动数据总延迟: ~8-22ms (但被排在worker队列中，前面可能有调度数据排队)
  WebSocket 推送在最后才执行 → 前端看到数据晚
```

### 4.2 新架构（并行消费者）

```
DataReport 到达
  │
  ▼
DataEventBus.Publish (投递通道，<1μs)
  │
  ├──→ Terminal Consumer     ~1μs (RecordRX)
  ├──→ WS Push Consumer      ~100μs (BroadcastEvent channel_data)
  ├──→ PendingWrite Consumer ~1μs (如果 ShouldHandle=true)
  ├──→ DB Persist Consumer   ~1ms (如果 ShouldHandle=true)
  └──→ Sensor Parser Consumer ~5-20ms (如果 ShouldHandle=true)
  
  各消费者独立 goroutine 并行执行
  
  被动数据 (request_id=0):
    Terminal + WS Push = ~101μs → 前端几乎实时看到
    PendingWrite / DB Persist / Sensor Parser 全部跳过 (ShouldHandle=false)
    
  调度数据 (request_id≠0):
    Terminal + WS Push = ~101μs → 前端实时看到原始数据
    Sensor Parser = ~5-20ms → 解析后的传感器数据稍后通过 data_update 事件补充
    各消费者并行，互不阻塞
```

### 4.3 延迟改善

| 场景 | 当前延迟 | 新架构延迟 | 改善 |
|------|---------|-----------|------|
| 被动数据→终端显示 | 8-22ms + 队列等待 | ~101μs | 100x+ |
| 调度数据→channel_data | 8-22ms + 队列等待 | ~101μs | 100x+ |
| 调度数据→data_update | 8-22ms | 5-20ms (独立并行) | 不变但不再阻塞终端 |

## 5. 与现有 worker pool 的关系

### 5.1 保留 worker pool 做帧解析

现有 `dataCh + 8 workers` 机制保留，但 worker 的职责从"全流程处理"缩减为"帧解析 + 投递总线"：

```go
// 改造前: processDataReportJob 做全部事情
// 改造后: processDataReportJob 只解析帧并投递总线
func (m *Manager) processDataReportJob(job dataReportJob) {
    evt := DataEvent{
        DeviceID:     job.deviceID,
        ChannelID:    job.channelID,
        // ... 其他字段
        ReceivedAt:   time.Now(),
    }
    m.dataBus.Publish(evt)  // 投递后立即返回，worker 可处理下一个
}
```

### 5.2 Worker pool 不再阻塞

当前 worker 在 `parseAndStoreData` 中执行 5-20ms 的 DB 操作，8 个 worker 在高频数据场景下会全部被占满，导致 dataCh 排队。

改造后 worker 只做帧解析（<100μs），立即投递总线后处理下一个，dataCh 不会积压。

## 6. 消费者注册顺序

注册顺序不影响执行（各消费者并行），但影响日志可读性：

```go
// 在 NewManager 中初始化
m.dataBus = logstream.NewDataEventBus()

// 1. Terminal — 最高优先级，所有数据
m.dataBus.Register(NewTerminalConsumer(m.termMgr))

// 2. WS Push — 所有数据，快速通道
m.dataBus.Register(NewWSPushConsumer(m.wsHub))

// 3. PendingWrite — 命令响应路由
m.dataBus.Register(NewPendingWriteConsumer(m.pendingWrite, m.deviceInit, m.db))

// 4. DB Persist — 原始数据持久化
m.dataBus.Register(NewDBPersistConsumer(m.db))

// 5. Sensor Parser — 最重量级，最后注册
m.dataBus.Register(NewSensorParserConsumer(m.db, m.wsHub, m.ha, m.reassembler))
```

## 7. SensorParserConsumer 的 data_update 问题

当前 `processDataReportJob` 在 `parseAndStoreData` 后将解析结果合并到 `channel_data` 事件中。新架构中 WS Push Consumer 和 Sensor Parser Consumer 是并行独立的，无法共享数据。

解决方案：**Sensor Parser Consumer 独立推送 `data_update` 事件**。

当前已有两个 WS 事件：
- `channel_data` — 原始 hex 数据（WS Push Consumer 推送，所有数据）
- `data_update` — 解析后的传感器值（Sensor Parser Consumer 推送，仅调度数据）

前端已有两个独立的订阅：
- ChannelTerminal 订阅 `channel_data` → 显示原始 hex
- Dashboard 订阅 `data_update` → 显示传感器值

两个事件本就是独立推送的，拆分后行为不变。当前代码中 `channel_data` 携带 `data` 字段（解析结果）只是锦上添花，前端 ChannelTerminal 不使用该字段——只使用 `raw_hex`。

## 8. 背压控制

```go
const dataChanBufferSize = 256  // 数据事件通道缓冲

func (bus *DataEventBus) Publish(evt DataEvent) {
    select {
    case bus.dataChan <- evt:
        // 正常入队
    default:
        // 通道满 — 丢弃最旧事件，保留最新
        select {
        case <-bus.dataChan:
        default:
        }
        bus.dropped.Add(1)
        bus.dataChan <- evt
    }
}
```

数据通道缓冲 256（比 logstream 的 64 大，因为数据事件更高频且更重要）。丢弃策略与 LogEventBus 一致：丢旧保新。通过 Prometheus metric `data_bus_dropped_total` 暴露丢弃计数。

## 9. 资源开销

| 组件 | 开销 |
|------|------|
| DataEventBus dispatch goroutine | 1 个 |
| 通道缓冲 | 256 × sizeof(DataEvent) ≈ 256 × 128B = 32KB |
| Terminal Consumer | 无额外资源（复用 termMgr） |
| WS Push Consumer | 无额外资源（复用 wsHub） |
| PendingWrite Consumer | 无额外资源（复用 pendingWrite） |
| DB Persist Consumer | 无额外资源（复用 db） |
| Sensor Parser Consumer | 无额外资源（复用 db/wsHub/ha） |
| 每事件 fanout | 5 个 goroutine（并行消费者），各自 <20ms 完成 |

总增量：1 goroutine + 32KB 通道缓冲。与 LogEventBus 模式一致。

## 10. 关键设计决策

1. **DataEvent 携带完整上下文**：包含 request_id / edge_device_id / error_code 等全部字段，消费者自行判断是否处理。生产者不做任何过滤。

2. **ShouldHandle 在 dispatch 中调用**：dispatch goroutine 先调 `ShouldHandle`，只有返回 true 的消费者才 spawn goroutine 执行 `Handle`。避免为不需要处理的消费者创建 goroutine。

3. **被动数据快速通道**：Terminal Consumer 和 WS Push Consumer 的 `ShouldHandle` 始终返回 true，且 Handle 中无 DB 操作。被动数据（request_id=0）只被这两个消费者处理，终端显示延迟 <1ms。

4. **消费者并行而非串行**：每个消费者的 Handle 在独立 goroutine 中执行。WS Push（100μs）不被 Sensor Parser（20ms）阻塞。前端几乎实时看到原始数据，解析后的传感器值稍后通过 data_update 事件补充。

5. **保留 worker pool 做帧解析**：worker pool 仍有价值——帧解码（varint 解析、字段提取）是 CPU 密集型，需要多 worker 并行。改造后 worker 职责从"全流程"缩减为"帧解析+投递总线"，吞吐量提升 100x。

6. **与 LogEventBus 架构一致**：相同的接口模式（Name/ShouldHandle/Handle）、相同的背压策略、相同的消费者隔离机制。后端形成统一的事件总线模式，降低维护成本。

7. **不改变 ESP32 协议和前端事件格式**：DataReport 0x03 帧不变，channel_data / data_update WebSocket 事件格式不变。纯后端内部重构。

8. **渐进式迁移**：可以先实现 DataEventBus + Terminal/WS Push 两个轻量消费者，验证被动数据延迟改善后再迁移 Sensor Parser / DB Persist / PendingWrite。每步都可独立验证。
