# EHomeSystem docs/ 文档整理方案 v2

> **For Hermes:** Use subagent-driven-development skill to implement this plan task-by-task.
> **分析日期:** 2026-06-17
> **代码基线:** master (v2.2 命名规范) + feat/dma-resource-protocol (v2.4 设计中)

**Goal:** 消除 docs/ 的术语混乱、版本混淆、内容过时、分类错误，建立与代码严格对齐的权威文档体系。

**核心发现:** 问题不在文件组织层面，而在于 —— 代码已全员 v2.2 术语（Node/EdgeDevice/DeviceConfig），但 14 份设计文档仍用 v2.0/v2.1 旧术语（采集器/设备/设备模板）。单纯移动文件不解决任何问题。

---

## 一、代码侧术语基准（不可变，文档向代码对齐）

代码是唯一真相来源。文档必须与以下代码命名完全一致：

| 概念 | 代码命名 | 禁止使用的旧名 |
|------|---------|---------------|
| ESP32 设备 | **Node** (struct `Node`, 表 `nodes`, API `/api/v1/nodes`) | 采集器、Collector、Device |
| 物理传感器/执行器实例 | **EdgeDevice** (struct `EdgeDevice`, 表 `edge_devices`, API `/api/v1/edge-devices`) | Device、设备 |
| 设备型号定义 | **DeviceConfig** (struct `DeviceConfig`, 表 `device_configs`, API `/api/v1/device-configs`) | DeviceTemplate、设备模板、Driver |
| 总线通信端口 | **Channel** (struct `Channel`, 表 `channels`, API `/api/v1/channels`) | 不变 |
| Go 后端 | **Center** (术语表定义) | Server、服务端 |
| MQTT topic | `nodes/{id}/up`, `nodes/{id}/down` | `devices/{id}/up/v2` (仅兼容期) |
| WebSocket 事件 | `node_status`, `edge_device_status`, `data_update` 等 | `device_status`, `collector_status` |
| Prometheus 指标 | `ehome_node_online_count` | `ehome_collector_online_count` |

### v2.4 新增实体（feat 分支，仅文档中存在）

| 概念 | 代码/协议命名 | 状态 |
|------|-------------|------|
| 硬件资源描述 | **HwProfile** (hw_profile.h, 未实现) | 设计中 |
| 资源上报消息 | **MSG_RESOURCE_REPORT 0x19** (frame_codec.h 已定义) | 设计中 |
| 资源查询消息 | **MSG_QUERY_RESOURCES 0x1A** (frame_codec.h 已定义) | 设计中 |

---

## 二、文档完整度审计结果

### 2.1 设计/ 目录（30 个 .md）逐份审计

| 文档 | 术语 | 版本标注 | 状态标记 | SQL/API 与代码一致性 | 结论 |
|------|------|---------|---------|---------------------|------|
| 00-术语表.md | ✅ v2.2 | ✅ v2.2 | ✅ 权威 | N/A | 保留，权威 |
| 00-概念模型.md | ✅ v2.2 | ✅ v2.2 | ✅ 权威 | ✅ | 保留，权威 |
| README.md | ✅ v2.2 | ❌ 无 | ❌ 进行中(模糊) | ⚠️ 完整度表标注全🟢但实际未完成 | **修复：更新状态** |
| 总体设计.md | ✅ v2.2 | ✅ v2.2 | ✅ 实施中 | ✅ | 保留，权威 |
| 命名迁移设计.md | ✅ v2.2 | ✅ v2.2 | ❌ 草案 | ⚠️ 任务清单大部分未勾选 | **修复：更新进度** |
| 统一总线DMA架构.md | ✅ v2.2 | ✅ v2.4 | ❌ 设计中 | ❌ bus_dma/hw_profile 未实现 | **标注未实现** |
| ESP32_ARCHITECTURE_DESIGN.md | ✅ v2.2 | ❌ v2.5 | ❌ 已实现(不实) | ❌ hw_profile/dma_pool 未实现 | **从 docs/ 移入 + 修正状态** |
| DMA_RESOURCE_PROTOCOL_DESIGN.md | ✅ v2.2 | ❌ v1.0 | ❌ 待评审 | ❌ 完全未实现 | **从 docs/ 移入 + 修正状态** |
| **设备/详细设计.md** | ❌ 旧: Device/采集器 | ❌ 无 | ❌ 无 | ❌ devices 表已废弃 | **整个目录废弃，改为重定向占位** |
| **设备/验收标准.md** | ❌ 旧 | ❌ 无 | ❌ 无 | ❌ 旧端点 | **整个目录废弃** |
| 节点/详细设计.md | ✅ v2.2 | ✅ v2.2 | ❌ 无 | ⚠️ 包名仍为 collector | **修复：标注包名兼容** |
| 节点/验收标准.md | ⚠️ §三标题"采集器端" | ✅ v2.2 | ❌ 无 | ⚠️ 术语混用 | **修复：§三标题改为"节点端"** |
| 节点资源上报/详细设计.md | ✅ v2.2 | ✅ v2.4 | ✅ 设计中 | ❌ 未实现 | 保留，标注未实现 |
| **数据采集/详细设计.md** | ❌ 旧: CollectorID/Device | ❌ 无 | ❌ 无 | ❌ unified_data 引用 devices(id) | **重写 SQL 模型段** |
| **数据采集/验收标准.md** | ❌ 旧: 采集器 | ❌ 无 | ❌ 无 | ❌ 11-driver 引用过时路径 | **重写术语** |
| 通道/详细设计.md | ✅ v2.2 | ✅ v2.2 | ❌ 无 | ⚠️ NodeID vs collector_id | **修复：确认字段名** |
| 通道/验收标准.md | ❌ §三"采集器端" | ❌ 无 | ❌ 无 | N/A | **修复：§三标题** |
| 边缘设备/详细设计.md | ✅ v2.2 | ✅ v2.2 | ❌ 无 | ✅ | 保留 |
| 边缘设备/验收标准.md | ⚠️ §三"采集器 (节点)" | ✅ v2.2 | ❌ 无 | N/A | **修复：§三标题** |
| 设备配置/详细设计.md | ✅ v2.2 | ✅ v2.2 | ❌ 无 | ⚠️ JSONB 字段未实施 | **标注未实施项** |
| 设备配置/验收标准.md | ✅ v2.2 | ✅ v2.2 | ❌ 无 | N/A | 保留 |
| **认证授权/详细设计.md** | ❌ 旧: 采集器 | ❌ 无 | ❌ 无 | ⚠️ MQTT topic 用 devices/ 旧路径 | **重写术语 + 修复 topic** |
| **认证授权/验收标准.md** | ❌ 旧: 采集器 | ❌ 无 | ❌ 无 | ❌ devices/ topic | **重写术语 + 修复 topic** |
| **系统监控/详细设计.md** | ⚠️ 混用 | ❌ 无 | ❌ 无 | ⚠️ 指标名与代码不一致 | **修复：指标名对齐代码** |
| **系统监控/验收标准.md** | ❌ 旧: 采集器/设备 | ❌ 无 | ❌ 无 | N/A | **重写术语** |
| **固件OTA/详细设计.md** | ❌ 旧: CollectorID | ❌ 无 | ❌ 无 | ❌ ota_tasks.CollectorID | **重写: CollectorID→NodeID** |
| **固件OTA/验收标准.md** | ❌ 旧: 采集器/collectors | ❌ 无 | ❌ 无 | ❌ collectors.firmware_version | **重写术语** |
| **通知中心/详细设计.md** | ❌ 旧: 设备(单独) | ❌ 无 | ❌ 无 | ⚠️ device:{id}→edge_device:{id} | **重写术语 + 修复 Source** |
| **通知中心/验收标准.md** | ❌ 旧: 采集器 | ❌ 无 | ❌ 无 | N/A | **重写术语** |
| **同步机制/详细设计.md** | ❌ 旧: collector/deviceID | ⚠️ v2.1 | ❌ 草案 | ⚠️ 包名 collector 保留 | **加说明：内部包名保留** |
| **同步机制/验收标准.md** | ❌ 旧: Collector/device | ❌ v2.1 | ❌ 无 | ❌ 组件名过时 | **重写术语** |

**统计:**
- 术语完全正确: 12/30
- 术语部分过时需修复: 6/30
- 术语严重过时需重写: 10/30
- 整个目录可废弃: 2/30 (设计/设备/)

### 2.2 实现/ 目录（10 个 .md）逐份审计

| 文档 | 术语 | 代码路径准确性 | 结论 |
|------|------|---------------|------|
| **README.md** | ❌ 旧: 设备/设备模板 | ❌ 引用不存在的文件名 | **重写模块索引表** |
| 节点.md | ✅ v2.2 | ✅ | 保留 |
| 边缘设备.md | ✅ v2.2 | ✅ | 保留 |
| 设备配置.md | ✅ v2.2 | ✅ | 保留 |
| **数据采集.md** | ❌ 旧: 采集器 | ✅ | **重写术语** |
| **通道.md** | ❌ 旧: 采集器 | ⚠️ 前端路径旧 | **重写术语 + 修复路径** |
| 认证授权.md | ✅ (无关) | ✅ | 保留 |
| 固件OTA.md | ✅ (Kconfig用COLLECTOR_是正确内部名) | ✅ | 保留 |
| **通知中心.md** | ❌ 旧: 采集器 | ✅ | **重写术语** |
| **系统监控.md** | ❌ 旧: 采集器/设备 | ✅ | **重写术语** |

---

## 三、完整整理方案

### Phase 1: 文件组织（归位）

#### 1a. 新目录
```bash
mkdir -p docs/archive/v2.0 docs/评审 docs/验证 docs/发布 docs/操作
```

#### 1b. v2.0 历史文档归档（9 个文件 → archive/v2.0/）
```
git mv docs/STATUS.md                          → docs/archive/v2.0/
git mv docs/REQUIREMENTS_CHECK.md              → docs/archive/v2.0/
git mv docs/FEATURE_CHECKLIST.md               → docs/archive/v2.0/
git mv docs/FINAL_DELIVERY.md                  → docs/archive/v2.0/
git mv docs/VERIFICATION_REPORT.md             → docs/archive/v2.0/
git mv docs/功能实现情况详细分析报告.md           → docs/archive/v2.0/
git mv docs/IMPLEMENTATION_SUMMARY.md          → docs/archive/v2.0/
git mv docs/IMPLEMENTATION_SUMMARY_v2.md       → docs/archive/v2.0/
git mv docs/PROGRESS_REPORT.md                 → docs/archive/v2.0/
```

#### 1c. 错放文档归位（7 个文件）
```
git mv docs/DMA_RESOURCE_PROTOCOL_DESIGN.md     → docs/设计/
git mv docs/ESP32_ARCHITECTURE_DESIGN.md        → docs/设计/
git mv docs/架构评审报告.md                      → docs/评审/
git mv docs/SPI_BMP280_VERIFICATION.md          → docs/验证/
git mv docs/v2.2-release-report.md             → docs/发布/
git mv docs/UART_DMA_OPTIMIZATION.md            → docs/实现/
git mv docs/ESP32_FLASH_GUIDE.md               → docs/操作/
```

#### 1d. 废弃目录处理
设计/设备/ 整个目录被设计/边缘设备/+设计/设备配置/ 完全取代：
- `设计/设备/详细设计.md` → 替换为重定向占位文件（内容："本设计已迁移至 [边缘设备/详细设计.md](../边缘设备/详细设计.md) 和 [设备配置/详细设计.md](../设备配置/详细设计.md)"）
- `设计/设备/验收标准.md` → 删除（内容已被 边缘设备/验收标准.md + 设备配置/验收标准.md 覆盖）

### Phase 2: 术语统一重写（核心工作）

#### 2a. 批量术语替换（14 份文档）

以下文档需要全文术语替换。替换规则：

| 旧术语 | 新术语 | 应用范围 |
|--------|--------|---------|
| 采集器 | 节点 | 在指 ESP32 设备时 |
| 采集器 (esp32-node) | 节点 (ESP32) | 在文档标题/章节中 |
| 设备模板 | 设备配置 | 在指型号定义时 |
| 设备 (单独，非"边缘设备") | 边缘设备 | 在指物理设备实例时 |
| Collector | Node | SQL 字段名/Go 变量（仅文档层面） |
| CollectorID | NodeID | SQL 字段名 |
| Device (DB 表) | EdgeDevice | SQL 模型 |
| device_id (MQTT topic) | node_id | MQTT topic |
| devices/{id}/up/v2 | nodes/{id}/up | MQTT topic |
| devices/{id}/down/v2 | nodes/{id}/down | MQTT topic |
| collector_online_count | node_online_count | Prometheus 指标 |
| Collector (包名) | 保留 collector（标注"内部包名，v2.3 前兼容"） | 同步机制/ | 

**涉及文档清单:**
1. 数据采集/详细设计.md
2. 数据采集/验收标准.md
3. 通道/验收标准.md
4. 认证授权/详细设计.md
5. 认证授权/验收标准.md
6. 系统监控/详细设计.md
7. 系统监控/验收标准.md
8. 固件OTA/详细设计.md
9. 固件OTA/验收标准.md
10. 通知中心/详细设计.md
11. 通知中心/验收标准.md
12. 同步机制/详细设计.md（加说明，不全文替换）
13. 同步机制/验收标准.md
14. 实现/README.md
15. 实现/数据采集.md
16. 实现/通道.md
17. 实现/通知中心.md
18. 实现/系统监控.md
19. 节点/验收标准.md（仅§三标题）
20. 边缘设备/验收标准.md（仅§三标题）

#### 2b. 同步机制/详细设计.md 特殊处理

Go 代码包名 `internal/collector` 仍保留旧名（v2.2 命名迁移未完成到包级）。此文档包含大量 Go 代码示例使用 `package collector`、`deviceID`、`CfgChangeCollector`。**不替换这些代码引用**（它们与当前代码一致），而是在文档开头添加说明：

```markdown
> ⚠️ **术语说明**: 本文档中的 Go 代码仍使用 `collector` 包名、`deviceID` 参数名等 v2.1 内部命名。
> 这是代码现状（v2.2 命名迁移尚未到包级），不影响外部 API。
> 阅读时请注意: `collector` 包 = Node 管理逻辑, `deviceID` = nodeID, `CfgChangeCollector` = 节点配置变更。
```

### Phase 3: 内容修复（逐文档）

#### 3a. 数据采集/详细设计.md — SQL 模型重写

当前错误（引用已废弃的 `devices` 表）：
```
unified_data.device_id → devices(id)
```

修正为 v2.2 模型：
```
unified_data.edge_device_id → edge_devices(id)
unified_data.node_id → nodes(id)   -- v2.2 新增冗余
```

同时修正 DataReport 处理流中的 `device_id` → `edge_device_id`。

#### 3b. 固件OTA/详细设计.md — 字段名修正

当前:
```
ota_tasks.collector_id → nodes(id)
```

修正为:
```
ota_tasks.node_id → nodes(id)   -- DB 列名仍为 collector_id (v2.3 兼容)
```

实际 DB 列名仍为 `collector_id`，这是 GORM tag 决定的。文档需说明此兼容状态。

#### 3c. 通知中心/详细设计.md — Source 字段修正

当前:
```
Source: "device:123"
```

修正为:
```
Source: "edge_device:123"
```

同时禁止在无前缀时单独使用"设备"一词（术语表规则）。

#### 3d. 系统监控/详细设计.md — 指标名对齐

当前:
```
ehome_nodes_online
```

修正为（与代码 `metrics.go` 一致）:
```
ehome_node_online_count
```

#### 3e. 实现/README.md — 模块索引完全重写

当前错误:
```
| 3 | **设备** | [设备.md](设备.md) | ...views/device/...
| 5 | **设备模板** | [设备模板.md](设备模板.md) | ...
```

修正为:
```
| 3 | **边缘设备** | [边缘设备.md](边缘设备.md) | `handler_edge_device.go` | `views/edge-device/`
| 4 | **通道** | [通道.md](通道.md) | `handler_device.go` (channel 段) | `components/channel/`
| 5 | **设备配置** | [设备配置.md](设备配置.md) | `handler_device_config.go` | `views/config/DeviceConfigList.vue`
```

同时修正前端路径 `views/collector/` → `views/node/`，节点端组件路径保持不变（`esp32-collector` 是仓库名，正确）。

#### 3f. 操作/ESP32_FLASH_GUIDE.md — 路径修正

全局替换 `/home/sun/workspace/EHomeSystem/` → `/home/bcat/workspace/ehome-system/`

#### 3g. ESP32_ARCHITECTURE_DESIGN.md — 状态修正

当前标注 `状态: 已实现`，但 hw_profile、dma_pool 组件均不存在。
修正为 `状态: 设计中（feat/dma-resource-protocol 分支）`。

#### 3h. DMA_RESOURCE_PROTOCOL_DESIGN.md — 状态修正

当前标注 `状态: 待评审`。
修正为 `状态: 设计中（feat/dma-resource-protocol 分支，未实现）`。

### Phase 4: 统一文档头（版本标注+状态标记）

#### 缺失版本标注的文档（12 份）

为以下文档在其标题后添加统一的 YAML-style 元数据块：
- 数据采集/详细设计.md
- 数据采集/验收标准.md
- 通道/验收标准.md
- 认证授权/详细设计.md
- 认证授权/验收标准.md
- 系统监控/详细设计.md
- 系统监控/验收标准.md
- 固件OTA/详细设计.md
- 固件OTA/验收标准.md
- 通知中心/详细设计.md
- 通知中心/验收标准.md
- ESP32_ARCHITECTURE_DESIGN.md

元数据格式（与 00-术语表.md 对齐）：
```markdown
> **版本**: v2.2
> **状态**: [已实现 / 设计中 / 已过时]
> **关联**: [总体设计.md](../总体设计.md) | [00-术语表.md](../00-术语表.md)
```

#### 统一状态标记规范

| 状态值 | 含义 |
|--------|------|
| 权威 | 不可变的规范文档（术语表、概念模型） |
| 已实现 | 代码已完全实现 |
| 部分实现 | 代码部分实现（标注未实现项） |
| 设计中 | 设计文档，代码尚未实现 |
| 已过时 | 已被新文档取代，保留作历史参考 |

### Phase 5: 新建入口文档

#### 5a. docs/README.md

新建为整个文档体系的根入口。内容必须：
- 使用 v2.2 术语
- 列出所有目录的用途和权威级别
- 提供按角色的阅读路径
- 明确说明哪些是历史归档

#### 5b. docs/requirements.md 加归档标记

顶部加醒目警告，链接到权威设计文档。

### Phase 6: 验证

```bash
# 1. 搜索残留旧术语
grep -r "采集器" docs/ --include="*.md" | grep -v archive/ | grep -v "迁移.*旧名"
grep -r "设备模板" docs/ --include="*.md" | grep -v archive/

# 2. 检查所有相对链接可达
# 3. 确认 git status 干净
# 4. 确认 archive/ 中的文档引用设计/README.md 不再列出
```

---

## 四、最终目录结构

```
docs/
├── README.md                              # ★ 新建：根入口
├── requirements.md                        # 保留，顶部加归档标记
│
├── 设计/                                  # 权威设计文档
│   ├── README.md                          # 修复：状态标记
│   ├── 00-术语表.md                       # 保留
│   ├── 00-概念模型.md                     # 保留
│   ├── 命名迁移设计.md                    # 修复：进度更新
│   ├── 总体设计.md                        # 保留
│   ├── 统一总线DMA架构.md                 # 标注：未实现
│   ├── ESP32_ARCHITECTURE_DESIGN.md       # ★ 移入 + 修正状态
│   ├── DMA_RESOURCE_PROTOCOL_DESIGN.md    # ★ 移入 + 修正状态
│   ├── 设备/                              # 废弃：两个文件改为重定向占位
│   ├── 节点/                              # 修复：验收标准 §三
│   ├── 节点资源上报/                      # 保留（标注未实现）
│   ├── 设备配置/                          # 标注未实施项
│   ├── 边缘设备/                          # 修复：验收标准 §三
│   ├── 通道/                              # 修复：验收标准 §三
│   ├── 认证授权/                          # ★ 重写：术语 + topic
│   ├── 数据采集/                          # ★ 重写：SQL模型 + 术语
│   ├── 固件OTA/                           # ★ 重写：字段名 + 术语
│   ├── 通知中心/                          # ★ 重写：术语 + Source
│   ├── 系统监控/                          # ★ 重写：指标名 + 术语
│   └── 同步机制/                          # ★ 修复：加兼容说明 + 术语
│
├── 实现/                                  # 实现文档
│   ├── README.md                          # ★ 重写：模块索引表
│   ├── 节点.md                            # 保留
│   ├── 设备配置.md                        # 保留
│   ├── 边缘设备.md                        # 保留
│   ├── 通道.md                            # ★ 重写术语 + 路径
│   ├── 认证授权.md                        # 保留
│   ├── 数据采集.md                        # ★ 重写术语
│   ├── 固件OTA.md                         # 保留
│   ├── 通知中心.md                        # ★ 重写术语
│   ├── 系统监控.md                        # ★ 重写术语
│   └── UART_DMA_OPTIMIZATION.md           # ★ 移入
│
├── 评审/                                  # ★ 新建
│   └── 架构评审报告.md                    # ★ 移入
│
├── 验证/                                  # ★ 新建
│   └── SPI_BMP280_VERIFICATION.md         # ★ 移入
│
├── 发布/                                  # ★ 新建
│   └── v2.2-release-report.md            # ★ 移入
│
├── 操作/                                  # ★ 新建
│   └── ESP32_FLASH_GUIDE.md              # ★ 移入 + 修复路径
│
├── protocol/
│   └── binary-frame.md                    # 保留
│
└── archive/                               # ★ 新建
    └── v2.0/
        ├── STATUS.md
        ├── REQUIREMENTS_CHECK.md
        ├── FEATURE_CHECKLIST.md
        ├── FINAL_DELIVERY.md
        ├── VERIFICATION_REPORT.md
        ├── 功能实现情况详细分析报告.md
        ├── IMPLEMENTATION_SUMMARY.md
        ├── IMPLEMENTATION_SUMMARY_v2.md
        └── PROGRESS_REPORT.md
```

---

## 五、实施优先级

| 优先级 | 内容 | 文件数 | 工作量 |
|--------|------|--------|--------|
| P0 | Phase 1 文件归位（git mv + mkdir） | 16 次移动 | 10 min |
| P0 | Phase 5 新建入口文档 | 2 个新建 | 10 min |
| **P1** | **Phase 3c 通知中心 Source 字段修正** | 1 文件 | 5 min |
| **P1** | **Phase 3e 实现/README.md 模块索引重写** | 1 文件 | 10 min |
| P1 | Phase 2a 术语替换（18 份文档） | 18 文件 | 30 min |
| P1 | Phase 3a-3d 内容修正（SQL/字段/指标） | 4 文件 | 20 min |
| P2 | Phase 1d 废弃设备/目录 | 2 文件 | 5 min |
| P2 | Phase 4 统一文档头（12 份） | 12 文件 | 20 min |
| P2 | Phase 3f-3h 路径/状态修正 | 3 文件 | 10 min |

---

## 六、不处理的内容（明确排除）

1. **v2.4 设计文档内容** — 统一总线DMA架构.md、节点资源上报/详细设计.md、DMA_RESOURCE_PROTOCOL_DESIGN.md、ESP32_ARCHITECTURE_DESIGN.md 的技术内容不动（feat 分支进行中，代码未完成）
2. **代码本身** — 不修改任何 .go/.c/.vue 文件
3. **命名迁移的实际执行** — 不执行 DB 迁移、API 重命名等代码变更
4. **ESP32 Kconfig** — `CONFIG_COLLECTOR_*` 前缀保留（内部命名，不影响文档术语）
