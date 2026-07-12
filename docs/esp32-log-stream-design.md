# ESP32 系统日志远程查看 — 架构设计方案 v3

## 0. 需求说明

### 0.1 背景与问题

EHomeSystem 生产环境中，ESP32-C6 采集器运行时的系统日志（ESP-IDF `ESP_LOGI/W/E/D/V` 输出）只能通过物理串口（UART monitor）查看。运维人员需要远程查看采集器运行状态、诊断异常原因时，必须到现场连接串口，效率低且无法对多台采集器同时监控。

现有日志通过 UART monitor 输出，包含 MQTT 连接状态、RX_TASK 统计、调度器决策、配置同步等关键信息，但这些信息无法被后端和前端获取，导致远程诊断困难。

### 0.2 功能需求

| 编号 | 需求 | 优先级 | 说明 |
|------|------|--------|------|
| R1 | 远程查看实时日志 | P0 | 前端可实时查看 ESP32 系统日志流，替代串口 monitor |
| R2 | 日志开关控制 | P0 | 前端可远程开关日志采集功能，默认关闭，开启时才产生流量 |
| R3 | 日志级别控制 | P0 | 前端可选择日志级别（ERROR/WARN/INFO/DEBUG/VERBOSE），动态调整 |
| R4 | 日志持久化 | P0 | 前端可控制是否将日志写入数据库，支持事后查询历史日志 |
| R5 | 持久化独立开关 | P0 | 持久化开关独立于日志流开关，可只推不存、只存不推、既推又存 |
| R6 | 历史日志查询 | P0 | 支持按时间范围、级别、tag、关键词分页查询已持久化的日志 |
| R7 | 日志清理 | P1 | 支持手动清理指定时间之前的日志，支持自动过期清理 |
| R8 | 多消费者架构 | P1 | 后端架构支持多消费者（WS 推送、DB 持久化、未来 Webhook/API 等），可插拔注册 |
| R9 | 零默认开销 | P0 | 日志功能关闭时 ESP32 无额外任务、内存分配、MQTT 流量 |
| R10 | 日志导出 | P2 | 前端可导出当前实时日志或查询结果 |

### 0.3 约束条件

- ESP32-C6 SRAM 256KB，日志功能开启时内存增量不超过 5KB
- 日志功能不能影响现有通道采集、调度器、MQTT 通信等核心路径性能
- 复用现有 MQTT 通信通道和 config sync 机制，不新增独立通信链路
- ESP32 固件不感知后端消费方式（持久化、推送等），保持生产者职责单一
- 后端消费者架构可扩展，新增消费者不改现有代码（开闭原则）

### 0.4 术语

| 术语 | 含义 |
|------|------|
| 日志流 | ESP32 通过 MQTT 实时推送的系统日志数据流 |
| 生产者 | ESP32 固件的 log_stream 模块，采集并发送日志 |
| 消费者 | 后端消费日志批次的组件（WS 推送、DB 持久化等） |
| LogEventBus | 后端事件总线中间件，协调生产者与消费者 |
| stream_enabled | ESP32 日志采集开关，控制是否产生日志流 |
| persist_enabled | 后端持久化开关，控制 DB 消费者是否活跃 |

## 1. 设计目标

- ESP32 只负责产生日志，不感知下游消费方式
- 后端事件总线协调生产者与消费者，支持多消费者独立注册
- 消费者可插拔：WebSocket 推送、DB 持久化、未来 API 调用等
- 每个消费者可独立启停，互不影响
- ESP32 默认零开销，开启时资源可控

## 2. 整体架构

```
┌─────────────────────────────────────────────────────────────────┐
│                           前端 (Vue3)                            │
│  NodeDetail → "系统日志" Tab                                    │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │  控制栏:                                                │    │
│  │    [日志流] 开关   [级别] INFO ▼                        │    │
│  │    [持久化] 开关 (控制 DB 消费者, 独立于日志流)         │    │
│  │                                                        │    │
│  │  实时区: WebSocket node_log 事件 → 终端滚动显示          │    │
│  │  历史区: GET /api/nodes/:id/logs → 分页查询             │    │
│  └─────────────────────────────────────────────────────────┘    │
└──────────────────────┬──────────────────────────────────────────┘
                       │ WebSocket + HTTP
┌──────────────────────┴──────────────────────────────────────────┐
│                        后端 (Go)                                 │
│                                                                  │
│  ┌───────────────┐                                              │
│  │ MQTT Handler  │  MsgLogStream 0x1D                           │
│  │ (生产者入口)  │──────┐                                       │
│  └───────────────┘      │                                       │
│                         ▼                                       │
│              ┌──────────────────────┐                           │
│              │   LogEventBus        │  ← 内部事件总线            │
│              │   (中间件)            │                           │
│              │                      │                           │
│              │  Publish(logBatch)   │                           │
│              │  Subscribe(consumer) │                           │
│              │  Unsubscribe(consumer)│                          │
│              └──────┬───┬───┬───────┘                           │
│                     │   │   │                                    │
│          ┌──────────┘   │   └──────────────┐                    │
│          ▼              ▼                   ▼                    │
│  ┌──────────────┐ ┌──────────┐  ┌──────────────────┐           │
│  │ WS Consumer  │ │ DB       │  │ Future Consumer  │           │
│  │ (推送前端)    │ │ Consumer │  │ (Webhook/API...) │           │
│  │              │ │ (持久化)  │  │                  │           │
│  │ 始终活跃      │ │ 可启停   │  │ 按需注册          │           │
│  └──────────────┘ └──────────┘  └──────────────────┘           │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  Consumer Manager:                                        │   │
│  │  PUT /api/nodes/:id/log-config                            │   │
│  │    stream_enabled  → config sync → ESP32 启停             │   │
│  │    persist_enabled → 启停 DB Consumer                     │   │
│  │    level           → config sync → ESP32 日志级别          │   │
│  └──────────────────────────────────────────────────────────┘   │
└──────────────────────┬──────────────────────────────────────────┘
                       │ MQTT
┌──────────────────────┴──────────────────────────────────────────┐
│                      ESP32-C6 固件                               │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │  log_stream 模块 (独立组件)                               │    │
│  │                                                         │    │
│  │  关闭时: 无vprintf hook, 无task, 无buffer → 零开销      │    │
│  │  开启时:                                                 │    │
│  │  ┌──────────┐   ┌───────────┐   ┌──────────────┐       │    │
│  │  │vprintf   │──→│ Ring Buffer│──→│ log_tx_task  │──→MQTT│   │
│  │  │hook      │   │ (4KB)     │   │ 批量打包      │       │    │
│  │  └──────────┘   └───────────┘   └──────────────┘       │    │
│  └─────────────────────────────────────────────────────────┘    │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │  Config Manifest:                                        │    │
│  │  log_stream.enabled (bool) → 启停模块                    │    │
│  │  log_stream.level  (uint8) → 日志级别                     │    │
│  │  (无 persist 字段 — ESP32 不感知持久化)                   │    │
│  └─────────────────────────────────────────────────────────┘    │
└────────────────────────────────────────────────────────────────-─┘
```

## 3. ESP32 固件设计

### 3.1 设计原则

ESP32 是纯粹的生产者，只负责：
- 按 config manifest 的 enabled/level 两个字段启停日志采集
- 采集到的日志通过 MQTT 批量上报
- **不感知下游有几个消费者、是否持久化、谁在消费**

### 3.2 新组件 `log_stream`

独立于 bus_worker/scheduler，仅依赖 ESP-IDF log 系统 + MQTT client。

### 3.3 Structured diagnostic capture (no global hook)

`esp_log_set_vprintf` is **not used**: on the ESP32-C6/IDF v6 collector it can
re-enter the logging path and destabilize runtime tasks. Instead critical modules
emit selected structured events through `LOG_STREAM_E/W/I/D/V` wrappers. UART
`ESP_LOGx` output remains unchanged. The remote level is a stream threshold only
and does not call `esp_log_level_set("*", ...)`.

Covered diagnostic boundaries include MQTT lifecycle/publish errors, config
manifest parsing and sync timeout, scheduler queue failures, bus-worker pending
queue/RX overflow/RX timeout, channel transaction failures, and OTA start,
partition safety, NVS, and download failures.

### 3.4 log_tx_task 逻辑

```
log_tx_task:
  loop:
    delay 200ms
    lock ring buffer
    取出可用行 (最多 LOG_BATCH_MAX 行)
    unlock
    if 行数 > 0:
      打包为 MsgLogStream 协议帧
      调用注入的 MQTT publish callback 发布
```

### 3.5 启停控制

```c
void log_stream_start(uint8_t level);
  → 分配小型 ring buffer + 互斥锁
  → 设置远程日志阈值（不修改全局 ESP_LOG 级别）
  → 创建低优先级 log_tx_task

void log_stream_stop(void);
  → 通知 log_tx_task 退出，等待删除
  → 释放 ring buffer + 互斥锁

void log_stream_set_level(uint8_t level);
  → 更新远程结构化日志阈值
```

内存预算：ring 约 608B、静态编码缓冲约 1.5KB、task stack 1.5KB；启用额外内存保持在 5KB 内。

### 3.7 Config Manifest 字段

| 字段 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `log_stream.enabled` | bool | false | 日志采集开关 |
| `log_stream.level` | uint8 | 2 (INFO) | 最低日志级别 |

**无 persist 相关字段** — ESP32 不感知持久化。

## 4. 协议设计

### 4.1 消息类型

| 消息 | 值 | 方向 | 说明 |
|------|----|------|------|
| MsgLogStream | 0x1D | ESP32→后端 | 批量日志上报 |

### 4.2 MsgLogStream 帧格式

```
[ msg_type:  1B = 0x1D ]
[ count:     1B ]           — 本帧日志条数 (0-16)
[ seq:       2B ]           — 序列号 (溢出回绕)
  重复 count 次:
    [ level:   1B ]         — 0=ERROR 1=WARN 2=INFO 3=DEBUG 4=VERBOSE
    [ ts:     8B ]         — 微秒时间戳 (esp_timer_get_time)
    [ tag_len: 1B ]         — tag 字符串长度
    [ tag:    N B ]         — tag 字符串
    [ msg_len: 2B ]         — 消息长度
    [ msg:    M B ]         — 消息内容
```

**帧中无 persist_flag** — 是否持久化是后端消费者的事，与协议无关。

## 5. 后端设计 — 事件总线架构

### 5.1 LogEventBus 中间件

这是本方案的核心——一个内部事件总线，解耦生产者和消费者。

#### 接口定义

```go
// LogEntry 单条系统日志
type LogEntry struct {
    NodeID  string    // 采集器 device_id
    Level   int       // 0=ERROR 1=WARN 2=INFO 3=DEBUG 4=VERBOSE
    Ts      int64     // ESP32 微秒时间戳
    Tag     string    // 日志标签
    Message string    // 日志内容
    Seq     int       // 帧序列号
}

// LogBatch 一批日志（来自一个 MsgLogStream 帧）
type LogBatch struct {
    NodeID string
    Seq    int
    Logs   []LogEntry
}

// LogConsumer 消费者接口
type LogConsumer interface {
    Name() string                          // 消费者名称（用于管理）
    Consume(batch LogBatch)                // 处理一批日志
    IsActive() bool                        // 是否活跃
}

// LogEventBus 事件总线
type LogEventBus struct {
    mu         sync.RWMutex
    consumers  []LogConsumer
    logChan    chan LogBatch               // 有缓冲通道，背压控制
    wg         sync.WaitGroup
}

// Publish 生产者发布日志批次
func (bus *LogEventBus) Publish(batch LogBatch)

// Register 注册消费者
func (bus *LogEventBus) Register(consumer LogConsumer)

// Unregister 注销消费者
func (bus *LogEventBus) Unregister(name string)
```

#### 运行机制

```
MQTT Handler (生产者)               LogEventBus (中间件)              Consumers (消费者)
     │                                    │                                 │
     │  Publish(batch)                    │                                 │
     ├──────────────────────────────────-→│                                 │
     │                                    │  logChan <- batch               │
     │                                    │                                 │
     │                              ┌──────│ dispatch goroutine ──────────────┐
     │                              │      │                                 │
     │                              │      │  for _, c := range consumers:   │
     │                              │      │    if c.IsActive():             │
     │                              │      │      c.Consume(batch)  ──────────→  WS Consumer
     │                              │      │                                 │
     │                              │      │                                 │  DB Consumer
     │                              │      │                                 │
     │                              │      │                                 │  Future Consumer
     │                              └──────┴─────────────────────────────────-┘
```

#### 背压控制

```go
const (
    LogChanBufferSize = 64    // 通道缓冲 64 批次
)

func (bus *LogEventBus) Publish(batch LogBatch) {
    select {
    case bus.logChan <- batch:
        // 正常入队
    default:
        // 通道满 — 丢弃最旧批次，保留最新
        // 并记录 metric: log_bus_dropped_total
        select {
        case <-bus.logChan:        // 丢弃最旧
        default:
        }
        bus.logChan <- batch
    }
}
```

消费者各自处理异常，单个消费者 panic 不会影响其他消费者（recover 保护）。

#### 消费者隔离

```go
func (bus *LogEventBus) dispatch() {
    for batch := range bus.logChan {
        bus.mu.RLock()
        consumers := make([]LogConsumer, len(bus.consumers))
        copy(consumers, bus.consumers)
        bus.mu.RUnlock()

        for _, c := range consumers {
            if !c.IsActive() {
                continue
            }
            // 每个消费者在独立 goroutine 中执行，互不阻塞
            bus.wg.Add(1)
            go func(consumer LogConsumer, b LogBatch) {
                defer bus.wg.Done()
                defer func() {
                    if r := recover(); r != nil {
                        logger.Errorf("log consumer %s panic: %v", consumer.Name(), r)
                    }
                }()
                consumer.Consume(b)
            }(c, batch)
        }
    }
}
```

### 5.2 内置消费者

#### WS Consumer — WebSocket 推送

```go
type WSLogConsumer struct {
    wsHub *ws.Hub
}

func (c *WSLogConsumer) Name() string { return "websocket" }
func (c *WSLogConsumer) IsActive() bool { return true }  // 始终活跃

func (c *WSLogConsumer) Consume(batch LogBatch) {
    event := map[string]interface{}{
        "event": "node_log",
        "data": map[string]interface{}{
            "node_id": batch.NodeID,
            "lines":   toEventLines(batch.Logs),
        },
    }
    c.wsHub.BroadcastToNode(batch.NodeID, event)
}
```

特点：
- 始终活跃（日志流开启时才有数据到达，无需额外开关）
- 无状态，无副作用
- 前端有 WebSocket 连接且订阅了该 node 才会收到

#### DB Consumer — 数据库持久化

```go
type DBLogConsumer struct {
    db      *gorm.DB
    active  atomic.Bool       // 原子开关
    nodeCtl map[string]*atomic.Bool  // 按节点控制（可选扩展）
}

func (c *DBLogConsumer) Name() string { return "database" }
func (c *DBLogConsumer) IsActive() bool { return c.active.Load() }

func (c *DBLogConsumer) Consume(batch LogBatch) {
    rows := make([]models.NodeLog, len(batch.Logs))
    for i, log := range batch.Logs {
        rows[i] = models.NodeLog{
            NodeID:  log.NodeID,
            Level:   log.Level,
            Ts:      log.Ts,
            Tag:     log.Tag,
            Message: log.Message,
            Seq:     batch.Seq,
        }
    }
    // 批量插入
    c.db.CreateInBatches(rows, len(rows))
}

// 外部控制
func (c *DBLogConsumer) SetActive(active bool) {
    c.active.Store(active)
}
```

特点：
- 通过 `SetActive(true/false)` 动态启停
- 启停由 Consumer Manager 根据 API 请求控制
- 批量插入，最小化 DB IO

#### 未来消费者示例 — Webhook 转发

```go
type WebhookLogConsumer struct {
    url    string
    active atomic.Bool
    levels int          // 位掩码: 关心的日志级别
}

func (c *WebhookLogConsumer) Consume(batch LogBatch) {
    // 过滤级别，POST 到外部 URL
    // 可用于集成 Slack/钉钉/企业微信告警
}
```

### 5.3 Consumer Manager — 消费者管理

```go
type ConsumerManager struct {
    bus     *LogEventBus
    dbConsumer *DBLogConsumer
    // 未来: webhookConsumer *WebhookLogConsumer
}

// SetPersistence 启停 DB 消费者
func (m *ConsumerManager) SetPersistence(nodeID string, enabled bool) {
    m.dbConsumer.SetActive(enabled)
}

// GetPersistenceStatus 查询当前状态
func (m *ConsumerManager) GetPersistenceStatus() bool {
    return m.dbConsumer.IsActive()
}
```

### 5.4 MQTT Handler 对接

```go
// msg_handler.go — 新增 case 0x1D
func handleMsgLogStream(mgr *Manager, deviceID string, payload []byte) {
    batch := parseLogStreamFrame(deviceID, payload)
    // 投递到事件总线，不关心谁消费
    mgr.logBus.Publish(batch)
}
```

MQTT handler 极其简单——解析帧、投递总线、结束。不直接调用 WS 推送或 DB 写入。

### 5.5 数据模型

```sql
CREATE TABLE node_logs (
    id          BIGSERIAL PRIMARY KEY,
    node_id     VARCHAR(64)  NOT NULL,
    level       SMALLINT     NOT NULL,
    ts          BIGINT       NOT NULL,     -- ESP32 微秒时间戳
    tag         VARCHAR(64)  NOT NULL,
    message     TEXT         NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    seq         INTEGER      NOT NULL DEFAULT 0
);

CREATE INDEX idx_node_logs_node_time ON node_logs (node_id, ts DESC);
CREATE INDEX idx_node_logs_node_level ON node_logs (node_id, level, ts DESC);
```

### 5.6 自动清理

```yaml
# config.yaml
log_stream:
  max_age_hours: 72              # 超过 72 小时自动删除
  cleanup_interval: 3600         # 清理检查间隔 (秒)
```

后端低优先级 goroutine 定期执行 `DELETE FROM node_logs WHERE created_at < NOW() - INTERVAL '72 hours'`。

### 5.7 API 端点

#### 日志流控制（ESP32 侧）

```
PUT /api/nodes/:id/log-config
Body: { "stream_enabled": true, "level": 2 }

→ 更新 config DB 的 log_stream 字段
→ 触发 config sync → ESP32 启停日志采集
```

#### 持久化控制（后端侧）

```
PUT /api/nodes/:id/log-persist
Body: { "enabled": true }

→ ConsumerManager.SetPersistence(nodeID, true)
→ 启停 DB Consumer
→ 不触发 config sync（ESP32 无感知）
```

#### 查询历史日志

```
GET /api/nodes/:id/logs?from=&to=&level=&tag=&q=&page=&size=

→ 查询 node_logs 表
→ 分页返回
```

#### 清理日志

```
DELETE /api/nodes/:id/logs?before=<timestamp>
DELETE /api/nodes/:id/logs
```

## 6. 前端设计

### 6.1 控制栏

```
┌────────────────────────────────────────────────────────────────┐
│  [日志流] ●关闭   [级别] INFO ▼   [持久化] ●关闭              │
│                                                                │
│  [日志流] → 控制 ESP32 是否产生日志 (stream_enabled)           │
│  [持久化] → 控制后端是否写 DB (persist_enabled)                │
│  两个开关完全独立                                              │
└────────────────────────────────────────────────────────────────┘
```

### 6.2 联动提示

| 日志流 | 持久化 | 界面 |
|:-:|:-:|------|
| 关 | 关 | "日志功能已关闭" |
| 开 | 关 | 实时区可用，历史区隐藏 |
| 关 | 开 | "持久化已开启但日志流未开启，ESP32 不会产生日志" |
| 开 | 开 | 实时区 + 历史区均可用 |

### 6.3 实时区

- WebSocket `node_log` 事件 → 终端风格 monospace 滚动显示
- 级别颜色：ERROR 红 / WARN 黄 / INFO 白 / DEBUG 灰 / VERBOSE 暗
- 工具栏：暂停滚动、清屏、搜索过滤、导出

### 6.4 历史区

- 时间范围 + 级别多选 + tag 过滤 + 关键词搜索
- 分页表格，按时间倒序
- 删除操作有二次确认

## 7. 资源开销

### ESP32 (enabled=false)

| 资源 | 开销 |
|------|------|
| Task / 内存 / CPU / MQTT | 全部为零 |

### ESP32 (enabled=true)

| 资源 | 真实预算 |
|------|---------|
| Task stack | 1536 B |
| Ring heap | 608 B (`4 × sizeof(log_entry_t)`) |
| Static TX buffers | 1248 B (`4 × 152` batch + `768` frame buffer + `224` sub-frame) |
| Mutex/control state | <256 B |
| **总计** | **<3.7 KB** (under the 5 KB design limit) |

The bounded ring drops oldest entries; the low-priority task publishes at most four
structured entries every 200 ms.

### 后端

| 组件 | 开销 |
|------|------|
| LogEventBus | 1 个 goroutine + 64 批次通道缓冲 |
| WS Consumer | 无额外资源（复用 wsHub） |
| DB Consumer | 批量 INSERT，按 persist 开关启停 |
| 自动清理 | 每小时 1 次 DELETE |

## 8. 关键设计决策

1. **ESP32 不感知消费方式**：ESP32 只管产日志和发 MQTT，帧中无 persist_flag。是否持久化、推送给谁、怎么处理，全部由后端决定。ESP32 协议和代码保持简洁稳定。

2. **LogEventBus 中间件**：生产者（MQTT handler）和消费者（WS/DB/未来）之间通过事件总线解耦。生产者不关心有多少消费者，消费者不关心日志从哪来。新增消费者只需实现 LogConsumer 接口并 Register，零侵入。

3. **消费者接口标准化**：`LogConsumer` 接口只要求 `Name()`/`Consume()`/`IsActive()` 三个方法。任何符合接口的组件都可以注册为消费者——DB 写入、WS 推送、Webhook 转发、Slack 告警等。

4. **消费者隔离**：每个消费者在独立 goroutine 中执行，带 recover 保护。单个消费者 panic 或慢处理不会影响其他消费者，不会阻塞事件总线。

5. **背压控制**：事件总线通道满时丢弃最旧批次保留最新。系统日志是诊断数据，实时性优于完整性。通过 metric 暴露丢弃计数。

6. **persist 开关在后端**：`persist_enabled` 控制 DB Consumer 的 `IsActive()`，不经过 config sync，不通知 ESP32。开关响应即时，无网络往返。

7. **config manifest 仅两个 ESP32 字段**：`log_stream.enabled` + `log_stream.level`。persist 开关存在后端 config（API 层管理），不进入 ESP32 manifest。

8. **批量发送 + 批量写入**：ESP32 每 100ms 批量打包发送，DB Consumer 批量 INSERT。端到端批量处理，最大化吞吐最小化开销。
