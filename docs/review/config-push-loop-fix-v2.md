# EHomeSystem 配置推送自激循环 — 破坏性重设计方案 v2

**状态**: 终稿  
**日期**: 2026-06-21  
**设计原则**: 单信号、零状态、纯函数判断  

---

## 一、问题定义

### 1.1 现象

ESP 节点每隔 ~98s 收到一次 `0x04 (ConfigManifest)`，触发 scheduler 停止→bus 拆除→bus 重建→scheduler 重启，导致传感器采集周期性中断。

### 1.2 根因

**`BuildManifestID()` 每次生成新时间戳**，即使配置内容完全未变。`SyncGate` 的三轴判断中，manifest_id 永远不匹配，形成自激推送循环：

```
1. ESP 发 Hello (last_manifest=旧时间戳)
2. 后端 OnHello 轴3: hello.LastManifest != serverHash.ManifestID → push
3. sender 推送后写 DB config_version=新时间戳
4. ESP 收到 0x04 → 热重载 → 采集中断
5. 下次 Hello last_manifest=刚收到的时间戳
6. 后端 BuildManifestID() 又生成新时间戳 → 又不匹配 → 回到步骤 2
```

### 1.3 日志佐证

```
I (38062421) MSG: Received message type=0x04, len=158
  manifest_id=v2-1781967242824, epoch=1781283093227
I (38160214) MSG: Received message type=0x04, len=158
  manifest_id=v2-1781967340635, epoch=1781283093227
```

- 间隔 97,793ms ≈ 98s（与 Hello 周期同步检查吻合）
- **epoch 相同**（1781283093227）但 **manifest_id 不同**
- 证明配置内容没变，只是 manifest_id 被重新生成

---

## 二、核心设计

### 2.1 原则

**配置的版本号 = CRC32(内容)**。设备和服务的唯一判断依据是 `serverHash == deviceHash`。

### 2.2 为什么单信号优于三信号

当前系统有三个独立信号：

```
epoch         ← Publish() 递增（API CRUD → bus → epochGen.Next()）
manifest_id   ← BuildManifestID() 时间戳（每次调用变化）
content_hash  ← ShouldSendConfig() 内存态（重启丢失）
```

这三个值对"设备是否落后"可以给出三种不同答案。SyncGate 本质上是在 3×3=9 种排列组合上写 if-else，每个 if-else 又有自己的边界条件（首次见、重启、legacy 格式迁移），补丁叠补丁。

重设计后：配置变了 hash 就变，hash 变了就推。逻辑退化为一行比较。

### 2.3 decide() — 唯一决策点

```go
func (g *SyncGate) decide(deviceID string, deviceHash string, nvsEmpty bool,
    deviceChannelCount uint64) SyncDecision {

    // Guard 1: factory reset
    if nvsEmpty {
        return push("nvs_empty")
    }

    serverHash := g.mgr.CalcConfigHashForDevice(deviceID)
    if serverHash.Hash == "" {
        return skip("no_server_config")
    }

    // Core logic: single-axis comparison
    if deviceHash == serverHash.ManifestID {
        // Guard 2: hash matches but device has 0 channels (stale/broken config)
        if deviceChannelCount == 0 && serverHash.ChannelCount > 0 {
            return push("zero_channels_stale_config")
        }
        return skip("hash_match")
    }

    return push("hash_mismatch")
}
```

两个例外守卫：

| 守卫 | 场景 | 为什么 hash 比较不够 |
|------|------|---------------------|
| `nvsEmpty` | 工厂重置后 NVS 无配置，hash 为空 | hash 为空时无法比较 |
| `channelCount=0 && serverChannels>0` | 设备 NVS 配置 hash 匹配但运行时 0 channel（引脚错误、硬件初始化失败） | hash 相同，但设备处于损坏状态，必须重推 |

### 2.4 七个入口

| 入口 | 行为 | 理由 |
|------|------|------|
| `OnHello` | `decide(hello.LastManifest, !hello.NvsHasConfig, hello.ChannelCount)` | field 7（last_manifest）天然承载迁移 — 旧固件上报时间戳，新服务端算出哈希，不匹配 → 推一次 → 设备存新哈希 → 此后匹配 |
| `OnStatusReport` | `rpt.ConfigHash == "" ? skip : decide(rpt.ConfigHash, false, rpt.ChannelCount)` | **致命守卫**: 旧固件永远不发 field 6 config_hash，`"" != serverHash` 永远为真，不短路会导致每 5 秒推送一次（比原来 98s 循环严重 20 倍） |
| `OnConfigChange` | 无条件 push | API CRUD 触发，配置确实变了 |
| `OnServerStartup` | 不推，返回空 | 等设备 5s 内发 StatusReport，届时 decide() 自动判断。无信息缺口时不盲目推送 |
| `OnConfigQuery` | `decide(q.CurrentManifestID, false, 0)` | 设备主动查询配置状态 |
| `OnOfflineReconnect` | 无条件 push | 设备离线期间可能错过了配置变更 |
| `OnFactoryReset` | 无条件 push | NVS 已清空 |

### 2.5 零状态

```go
// ConfigHashManager — 纯函数，无锁，无 map，无状态
type ConfigHashManager struct{}

func NewConfigHashManager() *ConfigHashManager { return &ConfigHashManager{} }

func (m *ConfigHashManager) CalcConfigHash(hashData []byte) string {
    return fmt.Sprintf("%08x", crc32.ChecksumIEEE(hashData))
}
```

- 服务端重启：无状态需要恢复 → 设备发 Hello/StatusReport → 重新计算 hash → 比较 → 正确决策
- 并发安全：纯函数，无需 mutex
- 无 dedup window、无"首次见"判断、无 legacy 格式迁移逻辑 — 全部不需要

---

## 三、协议变更

### 3.1 Hello (0x01) — 不改

| Field | 名称 | 类型 | 说明 |
|-------|------|------|------|
| 2 | firmware_version | string | 不变 |
| 3 | model | string | 不变 |
| 4 | channel_count | varint | 不变 |
| 5 | config_epoch | varint | **保留** — 旧固件仍发，新服务端忽略即可 |
| 6 | nvs_has_config | bool | 不变 |
| 7 | last_manifest | string | **即 config_hash**。旧固件存时间戳 → 迁移时自动推送一次 |
| 8 | protocol_version | string | 不变 |

**迁移路径**：旧固件 field 7 = `"v2-1781967242824"`（时间戳），新服务端 `CalcConfigHashForDevice()` = `"v2-a1b2c3d4"`（哈希）。不匹配 → 推送一次 → 固件收到 ConfigManifest，field 1 = `"v2-a1b2c3d4"` → 存入 NVS → 下次 Hello field 7 = `"v2-a1b2c3d4"` → 匹配。迁移完成。

### 3.2 StatusReport (0x02) — 新增 field 6

| Field | 名称 | 类型 | 说明 |
|-------|------|------|------|
| 1 | uptime_sec | varint | 不变 |
| 2 | status | string | 不变 |
| 3 | channel_count | varint | 不变 |
| 4 | config_epoch | varint | **保留但不用** |
| 5 | sync_state | varint | 保留 |
| 6 | **config_hash** | **string** | **新增** — 内容哈希。旧固件不发 → `GetString` 返回 `""` → OnStatusReport 短路 skip |

### 3.3 ConfigManifest (0x04) — 删 2 个 field

| Field | 名称 | 类型 | 说明 |
|-------|------|------|------|
| 1 | manifest_id | string | **即 config_hash**（值不变，语义正确了） |
| ~~2~~ | ~~config_epoch~~ | ~~varint~~ | **删除** |
| 3 | templates | repeated | 不变 |
| 4 | channels | repeated | 不变 |
| 5 | dma_configs | repeated | 不变 |
| 8 | sync_id | string | **保留** — 用于 ConfigResult 回传做全链路关联 |
| ~~9~~ | ~~sync_reason~~ | ~~string~~ | **删除** |

---

## 四、buildHashData 加入 DMA configs

**问题**：原 `buildHashData` 只序列化 templates 和 channels。如果只改 DMA 配置而其他不变，hash 不变 → 设备通过 Hello 路径永远收不到 DMA 更新。

**修正**：

```go
func (m *Manager) buildHashData(
    templates []models.ConfigTemplate,
    channels []models.Channel,
    dmaConfigs []models.DmaChannelConfig,
) []byte {
    var buf []byte
    for _, t := range templates {
        buf = append(buf, []byte(fmt.Sprintf("t:%d:%s:%d:%d:",
            t.ID, t.WriteData, t.ReadLength, t.DelayMs))...)
    }
    for _, c := range channels {
        buf = append(buf, []byte(fmt.Sprintf("c:%d:%s:%s:%d:%v:%s:",
            c.ID, c.HardwareID, c.TemplateIDs, c.IntervalMs, c.Enabled, c.BusConfig))...)
    }
    for _, d := range dmaConfigs {
        buf = append(buf, []byte(fmt.Sprintf("d:%d:%v:%s:",
            d.DmaID, d.Enabled, d.BindTo))...)
    }
    return buf
}
```

### CalcConfigHashForDevice 配套改动

```go
func (m *Manager) CalcConfigHashForDevice(deviceID string) ConfigHashResult {
    var node models.Node
    if err := m.db.Where("node_id = ?", deviceID).First(&node).Error; err != nil {
        return ConfigHashResult{}
    }

    var templates []models.ConfigTemplate
    m.db.Where("node_id = ?", node.NodeID).Find(&templates)
    var channels []models.Channel
    m.db.Where("node_id = ?", node.NodeID).Find(&channels)

    // 从 node.Config JSON 解析 DMA configs
    var dmaConfigs []models.DmaChannelConfig
    if node.Config != "" {
        var cfg map[string]interface{}
        if err := json.Unmarshal([]byte(node.Config), &cfg); err == nil {
            if dc, ok := cfg["dma_configs"]; ok {
                if dcJSON, err := json.Marshal(dc); err == nil {
                    json.Unmarshal(dcJSON, &dmaConfigs)
                }
            }
        }
    }

    hashData := m.buildHashData(templates, channels, dmaConfigs)
    hash := m.hashMgr.CalcConfigHash(hashData)

    // ManifestID = 内容哈希，不 fallback DB 旧值
    manifestID := fmt.Sprintf("v2-%s", hash)

    return ConfigHashResult{
        Hash:         hash,
        ManifestID:   manifestID,
        ChannelCount: len(channels),
    }
}
```

`SyncDecision.ManifestID` 保留 — 由 `decide()` 填充 `serverHash.ManifestID`，避免 sender 重复查 DB。

---

## 五、SyncDecision 精简

```go
type SyncDecision struct {
    Action     SyncAction
    Reason     string   // "nvs_empty" | "hash_mismatch" | "hash_match" | ...
    SyncID     string   // 保留 — correlation ID，全链路日志关联
    ManifestID string   // 保留 — decide() 已计算，避免 sender 重复查 DB
    DeviceID   string
    // 删除: Epoch
}
```

---

## 六、ConfigEventBus 保留为纯通知通道

```go
type ConfigEventBus struct {
    ch chan ConfigChangeEvent
    // 删除: epochGen *EpochGenerator
}

func (b *ConfigEventBus) Publish(evt ConfigChangeEvent) {
    // 不再递增 epoch
    select {
    case b.ch <- evt:
    default:
        logger.Warnf("bus full, dropping event")
    }
}
```

**理由**：16 个 `EmitConfigChange` 调用点分布在 4 个 API handler 文件中，这些 handler 只有 `*ConfigEventBus` 引用，没有 `*Manager` 引用。保留事件总线避免 API 层改动。

---

## 七、改动清单

| 文件 | 操作 | 净行数 |
|------|------|--------|
| `nodemgr/epoch.go` | **删除整个文件** | -82 |
| `nodemgr/config_hash.go` | 删 hashes/lastSent/mutex/dedupWindow/ShouldSendConfig/Reset/UpdateLastSent，只保留 CalcConfigHash 纯函数 | -48 |
| `nodemgr/sync_gate.go` | 三轴 + 分支 → decide() + 7 入口 | -170, +45 |
| `nodemgr/manager.go` | 删 BuildManifestID()；删 eventBus 初始化中的 epochGen；buildHashData 加 dmaConfigs 参数；CalcConfigHashForDevice 解析 DMA configs + ManifestID 改为哈希 | -15, +12 |
| `nodemgr/sender.go` | 删 field 2 epoch 编码 + field 9 sync_reason 编码；保留 field 8 sync_id | -3 |
| `nodemgr/handler_config.go` | 删 config_epoch 写入 | -3 |
| `nodemgr/handler_status.go` | 新增 field 6 config_hash 解析 | +6 |
| `nodemgr/config_event_bus.go` | 删 epochGen 字段 + Next() 调用 | -5 |
| `nodemgr/event_helper.go` | **不改** | 0 |
| 4 个 API handler 文件 | **不改** | 0 |
| ESP 固件 | StatusReport 加 field 6 config_hash | +5 |
| **总计** | | **-321, +68** |

---

## 八、迁移兼容性

### 8.1 旧固件 + 新服务端

| 路径 | 行为 |
|------|------|
| Hello | field 7 上报旧时间戳 → `decide()` 中 `!= serverHash` → push 一次。设备存新哈希。下次 Hello 匹配，不再推。无循环 |
| StatusReport | 无 field 6 → `rpt.ConfigHash == ""` → OnStatusReport 短路 skip。等 Hello 触发比较 |

### 8.2 新固件 + 新服务端

原生哈希比较，无循环。所有路径统一走 `decide()`。

### 8.3 DB 兼容

- `nodes.config_epoch` — 保留列，不再写入（旧值自然过期）
- `nodes.config_version` — 继续写入，值变为 `v2-<hash>` 格式
- `nodes.config_sync_state` — 保留，由 ConfigResult handler 更新
- 不需要 migration

---

## 九、测试计划

### 9.1 单元测试

| # | 测试 | 预期 |
|---|------|------|
| 1 | `TestCalcConfigHash_Deterministic` | 相同配置 → 相同 hash |
| 2 | `TestCalcConfigHash_DifferentConfig` | 不同配置 → 不同 hash |
| 3 | `TestCalcConfigHash_IncludesDmaConfigs` | DMA 变更 → hash 变化 |
| 4 | `TestDecide_HashMatch` | deviceHash == serverHash → skip |
| 5 | `TestDecide_HashMismatch` | deviceHash != serverHash → push |
| 6 | `TestDecide_NvsEmpty` | nvsEmpty=true → push（无视 hash） |
| 7 | `TestDecide_ZeroChannelsStaleConfig` | hash 匹配但 channelCount=0, serverChannels>0 → push |
| 8 | `TestDecide_NoServerConfig` | DB 无配置 → skip |
| 9 | `TestOnStatusReport_EmptyConfigHash` | ConfigHash="" → skip（旧固件短路） |
| 10 | `TestOnStatusReport_ConfigHashMatch` | ConfigHash 匹配 → skip |
| 11 | `TestOnStatusReport_ConfigHashMismatch` | ConfigHash 不匹配 → push |

### 9.2 集成测试

| # | 场景 | 预期 |
|---|------|------|
| 1 | 正常运行（无配置变更），观察 10 分钟 | 0 次 0x04 推送 |
| 2 | 修改 channel 配置 | 1 次推送，之后停止 |
| 3 | 修改 DMA 配置 | 1 次推送，之后停止 |
| 4 | 服务端重启（配置未变） | 0 次推送（等 StatusReport → hash 匹配） |
| 5 | 设备离线期间改配置，重新上线 | 1 次推送（Hello hash 不匹配） |
| 6 | 工厂重置后上线 | 1 次推送（nvs_empty） |
| 7 | 旧固件 + 新服务端 | Hello 触发 1 次迁移推送，之后停止；StatusReport 不触发推送 |

---

## 十、实施顺序

| Phase | 内容 | 依赖 |
|-------|------|------|
| 1 | `epoch.go` 删除 + `config_event_bus.go` 删 epochGen | 无 |
| 2 | `config_hash.go` 瘦身为纯函数 | Phase 1 |
| 3 | `manager.go` 改动（删 BuildManifestID, buildHashData+DMA, CalcConfigHashForDevice） | Phase 2 |
| 4 | `sync_gate.go` 重写（decide() + 7 入口） | Phase 3 |
| 5 | `sender.go` 删 field 2/field 9 | Phase 3 |
| 6 | `handler_config.go` 删 config_epoch 写入 | Phase 3 |
| 7 | `handler_status.go` 新增 field 6 解析 | Phase 3 |
| 8 | ESP 固件 StatusReport 加 field 6 | 独立（可并行） |
| 9 | 单元测试 | Phase 1-7 |
| 10 | 集成测试 | Phase 1-9 |
