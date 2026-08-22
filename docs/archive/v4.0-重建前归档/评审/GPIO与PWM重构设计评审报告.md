# GPIO + PWM 外设控制重构设计方案 — 结构化评审报告

> **评审日期**: 2026-07-09  
> **评审身份**: 后端架构 + 前端工程专家  
> **方案文件**: `docs/设计/GPIO控制重构设计.md` v2.0  
> **项目路径**: `/home/sun/workspace/EHomeSystem`

---

## 评审总结

方案整体架构方向正确 — 将 GPIO/PWM 从通道系统中剥离是合理的领域驱动设计决策。但在数据模型完整性、API 设计细节、迁移 SQL 正确性、前端清理完整性、WebSocket 实时推送、测试策略等方面存在多处需要修正的问题。

| 严重程度 | 数量 |
|----------|------|
| 🔴 致命 | 4 |
| 🟡 重要 | 8 |
| 🔵 建议 | 6 |

---

## 一、数据模型设计

### 1.1 🔴 致命 — GPIOConfig/PWMConfig 缺少唯一约束

**问题**: `GPIOConfig` 和 `PWMConfig` 均缺少 `(node_id, pin)` 复合唯一索引。同一个节点的同一个引脚可以被重复配置多次，导致数据不一致和操作歧义。

**当前方案**:
```go
// GPIOConfig — 仅有 NodeID 单字段索引，Pin 无索引
NodeID string `gorm:"column:node_id;type:varchar(32);index;not null"`
Pin    int    `gorm:"not null" json:"pin"`
```

**建议**: 添加复合唯一索引：
```go
// GPIOConfig
NodeID string `gorm:"column:node_id;type:varchar(32);index:idx_gpio_node_pin,unique;not null"`
Pin    int    `gorm:"index:idx_gpio_node_pin,unique;not null"`

// PWMConfig — 同理
NodeID string `gorm:"column:node_id;type:varchar(32);index:idx_pwm_node_pin,unique;not null"`
Pin    int    `gorm:"index:idx_pwm_node_pin,unique;not null"`
```

**验证**: 当前 `Channel` 模型（models.go:113-130）也未对 `(node_id, hardware_id)` 做唯一约束，但 GPIO/PWM 场景下一个引脚只能有一个配置，约束更为关键。

---

### 1.2 🟡 重要 — PWMConfig.Running 字段不应持久化

**问题**: `PWMConfig.Running` 字段用 `gorm:"default:false"` 持久化到数据库，但「是否正在输出」是运行时状态，不是配置。设备重启后数据库中 `running=true` 但实际 PWM 未启动，造成状态不一致。

**建议**: 
- 移除 `Running` 字段，改为运行时内存状态（或 Redis 缓存）
- 如需持久化「期望运行状态」，改名为 `AutoStart bool` 并在 ConfigManifest 下发时由固件自动启动

---

### 1.3 🟡 重要 — 缺少与 EdgeDevice/Channel 的关联清理

**问题**: 方案声明 GPIO/PWM 不关联 EdgeDevice，但未说明如何处理现有 GPIO Channel 上已挂载的 EdgeDevice 记录。

**验证**: `EdgeDevice` 模型（models.go:73-102）有 `ChannelID` 外键。迁移删除 GPIO Channel 后，这些 EdgeDevice 的 `ChannelID` 将变成悬空引用。

**建议**: 迁移 SQL 中增加：
```sql
-- 先清理 GPIO Channel 上的 EdgeDevice
DELETE FROM edge_devices WHERE channel_id IN (SELECT id FROM channels WHERE hardware_type = 'gpio');
-- 再删除 GPIO Channel
DELETE FROM channels WHERE hardware_type = 'gpio';
```

---

### 1.4 🔵 建议 — GPIOConfig 缺少 PullUp/PullDown 电阻状态字段

**问题**: GPIO `direction` 枚举包含 `INPUT_PULLUP=2` 和 `INPUT_PULLDOWN=3`，但没有独立的电阻状态字段。某些场景需要在运行时切换上拉/下拉电阻而不改变方向。

**建议**: 可在后续版本扩展，当前方案可接受。

---

## 二、API 设计

### 2.1 🔴 致命 — 路由参数命名不一致导致路由冲突

**问题**: 设计方案使用 `/nodes/:node_id/gpio`，但现有节点路由使用 `/nodes/:id`（handler_node.go:99-115）。Gin 路由器中 `:node_id` 和 `:id` 是不同的参数名，在同一路由级别混用会导致 **panic** 或路由匹配异常。

**验证**: 
```go
// handler_node.go:99 — 现有路由
v1.GET("/nodes/:id", ...)

// 设计方案 — 新路由  
GET /nodes/:node_id/gpio
```

Gin 不允许同层级有不同参数名的路由段（`/nodes/:id` vs `/nodes/:node_id/gpio` 会触发 panic）。

**建议**: 统一使用 `:id` 参数名，与现有节点路由保持一致：
```
GET    /nodes/:id/gpio
POST   /nodes/:id/gpio
PUT    /nodes/:id/gpio/:pin
DELETE /nodes/:id/gpio/:pin
...
```

或者使用嵌套路由组：
```go
nodeGroup := v1.Group("/nodes/:id")
nodeGroup.GET("/gpio", ...)
nodeGroup.GET("/pwm", ...)
```

---

### 2.2 🟡 重要 — API 路径缺少 `/api/v1` 前缀

**问题**: 方案中 API 路径写为 `/nodes/:node_id/gpio`，但实际项目所有路由都在 `/api/v1` 前缀下（routes.go:50-90）。

**建议**: 明确标注完整路径为 `/api/v1/nodes/:id/gpio`。

---

### 2.3 🟡 重要 — 缺少权限控制设计

**问题**: 方案未提及 GPIO/PWM API 的权限控制。现有路由全部在 `v1.Use(JWTAuth())` 中间件下（routes.go:51），但 GPIO/PWM 操作涉及硬件控制，可能需要更细粒度的权限（如只读 vs 读写）。

**建议**: 至少明确声明 GPIO/PWM API 继承 JWT 认证。如有角色权限体系，考虑增加「外设控制」权限组。

---

### 2.4 🔵 建议 — GPIO/PWM 配置变更应触发 ConfigManifest 同步

**问题**: GPIO/PWM 的 POST/PUT/DELETE 操作修改了配置，但方案未说明是否需要触发 ConfigManifest 下发到节点。现有 Channel CRUD 通过 `EventBus` 触发配置同步（handler_device.go 中有 `EmitConfigChange`）。

**建议**: GPIO/PWM 配置变更后应同样触发 ConfigManifest 下发（包含新的 field 11/12），确保节点固件感知配置变化。

---

## 三、后端 Sender 修改

### 3.1 🟡 重要 — SendPeriphCmd() 未明确 QoS 策略

**问题**: 方案新增 `SendPeriphCmd()` 但未说明使用何种 MQTT QoS。现有 `SendWriteCommand()` 使用 `PublishQoS2`（QoS 2, exactly-once）（sender.go:73），注释写明「P3-5: Uses QoS 2 for critical write operations」。

GPIO 设置电平和 PWM 设置占空比的操作可靠性要求与 UART 写操作相同 — 丢失一次命令可能导致设备状态错误。

**建议**: `SendPeriphCmd()` 应使用 `PublishQoS2`，与 `SendWriteCommand` 保持一致。方案中应明确标注 QoS 策略。

---

### 3.2 🟡 重要 — PeriphRsp 响应处理未纳入 manager.go 消息分发

**问题**: 方案新增 `MsgPeriphRsp = 0x1C`，但未在 `manager.go` 的 `switch msgType` 分发器中添加处理分支。

**验证**: manager.go:137-163 的消息分发 switch 中，当前处理 0x01-0x19 的消息类型，但没有 0x1C 的处理分支。如果 `PeriphRsp` 不被处理，后端将无法感知 GPIO/PWM 操作结果。

**建议**: 
```go
// manager.go — 消息分发 switch 中新增
case frame.MsgPeriphRsp:
    m.handlePeriphResponse(deviceID, payload)
```

并实现 `handlePeriphResponse()` 方法，将结果通过 WebSocket 推送到前端。

---

### 3.3 🟡 重要 — ConfigManifest 编码遗漏 GPIO/PWM 配置

**问题**: 方案要求 ConfigManifest 新增 field 11 (gpio_configs) 和 field 12 (pwm_configs)，但 `SendConfigManifestWithDecision()`（sender.go:219-467）的编码逻辑中未提及如何加载和编码 GPIO/PWM 配置。

**验证**: sender.go:241-242 只查询 `channels` 表：
```go
var channels []models.Channel
m.db.Where("node_id = ? AND node.NodeID).Find(&channels)
```

方案需新增 GPIO/PWM 配置查询和编码逻辑。

**建议**: 在 `SendConfigManifestWithDecision()` 中增加：
```go
var gpioConfigs []models.GPIOConfig
m.db.Where("node_id = ? AND enabled = ?", node.NodeID, true).Find(&gpioConfigs)
for _, gc := range gpioConfigs {
    subEnc := frame.SubEncoder()
    subEnc.EncodeVarint(1, uint64(gc.Pin))
    subEnc.EncodeVarint(2, uint64(gc.Direction))
    subEnc.EncodeVarint(3, uint64(gc.InitialLevel))
    enc.EncodeSubFrame(11, subEnc.Bytes())
}
// 同理编码 pwm_configs → field 12
```

---

## 四、WebSocket 推送

### 4.1 🔴 致命 — GPIO/PWM 操作结果未通过 WebSocket 推送

**问题**: 方案完全遗漏了 WebSocket 实时状态更新。当前系统的所有操作结果都通过 WebSocket 推送到前端（如 PingResult、ScanResult、ChannelData 等，见 events.go:7-30）。GPIO/PWM 操作结果（尤其 GPIO READ 的返回值、PWM 状态变化）需要实时推送到前端以更新 UI。

**验证**: events.go 中没有 GPIO/PWM 相关事件定义。handler_response.go 中 WriteResponse 通过 `pendingWrite.HandleResponse()` 处理，但没有独立的 WebSocket 推送。

**建议**:
1. 新增事件常量：
```go
// events.go
PeriphResult = "periph_result"  // GPIO/PWM 操作结果
PeriphState  = "periph_state"   // GPIO/PWM 状态变更
```

2. 在 `handlePeriphResponse()` 中推送结果：
```go
m.wsHub.BroadcastEvent(events.PeriphResult, map[string]interface{}{
    "node_id":  deviceID,
    "periph_type": periphType,
    "pin":      pin,
    "action":   action,
    "success":  success,
    "value":    resultValue,
})
```

3. 前端 `PeripheralControl.vue` 监听 `periph_result` 事件更新卡片状态。

---

### 4.2 🟡 重要 — GPIO INPUT 读取结果无实时推送机制

**问题**: GPIO READ 操作通过 HTTP 请求-响应模式返回结果，但如果需要 GPIO 电平变化的中断通知（方案第 10 节待确认事项 #2 提到），需要从固件主动推送。

**建议**: 当前方案可先用 HTTP 同步读取，但应在 `MsgPeriphRsp` 协议中预留 `periph_type` 和 `action` 的扩展值，以便后续实现异步中断通知。

---

## 五、前端组件设计

### 5.1 🔴 致命 — PWM 占空比滑块拖动时频繁 API 调用性能问题

**问题**: 方案中 PWM 卡片有占空比滑块（0-100%），用户拖动滑块时如果每次变化都触发 `POST /nodes/:id/pwm/:pin/duty` API 调用，将产生大量 MQTT 消息和固件 LEDC 操作，导致：
- 网络请求风暴（每秒可能 30-60 次请求）
- MQTT 消息队列积压
- 固件 LEDC 频繁重配置

**建议**: 实现节流（throttle）+ 停止拖动时提交：
```typescript
// PWMChannelCard.vue
import { debounce } from 'lodash-es' // 或自定义 throttle

// 滑块拖动时只更新本地显示
const onDutyInput = (val: number) => {
  localDuty.value = val
}

// 拖动结束后提交（el-slider 的 @change 事件在松开时触发）
const onDutyChange = debounce(async (val: number) => {
  await pwmApi.setDuty(nodeId, pin, val)
}, 300)

// <el-slider :model-value="localDuty" @input="onDutyInput" @change="onDutyChange" />
```

使用 `@input` 实时更新本地 UI，`@change`（松开时触发）发送 API 请求，并加 300ms debounce 防抖。

---

### 5.2 🟡 重要 — PeripheralControl.vue 缺少加载状态和错误处理设计

**问题**: 方案的卡片网格设计未描述：
- 初始加载时的 loading skeleton
- API 调用失败时的错误提示和重试机制
- 节点离线时的 UI 状态（按钮禁用 + 离线标识）
- GPIO READ 操作的 loading 状态

**建议**: 补充前端交互状态设计，特别是节点离线时 GPIO/PWM 卡片应显示禁用状态。

---

### 5.3 🔵 建议 — GPIO/PWM 卡片应支持引脚资源可用性过滤

**问题**: 方案未说明卡片如何与 `ResourceReport` 中的可用 GPIO/PWM 引脚列表联动。用户可能配置了固件不支持的引脚号。

**建议**: ConfigManifest 下发前应校验引脚号在 `hw_gpios[]` 资源列表中。前端配置表单应限制可选项。

---

## 六、前端清理完整性

### 6.1 🟡 重要 — 移除清单遗漏 ChannelManager.vue

**问题**: 方案的前端影响文件清单（5.2 节）未包含 `ChannelManager.vue`，但该文件有大量 GPIO 相关代码：

**验证**:
- `ChannelManager.vue:25` — `<el-option label="GPIO" value="gpio" />` 硬件类型选项
- `ChannelManager.vue:161-162` — `<template v-if="form.hardware_type === 'gpio'">` GPIO 参数表单
- `ChannelManager.vue:280` — `gpio: 'GpioCaps'` 能力映射
- `ChannelManager.vue:315` — `case 'gpio':` 分支
- `ChannelManager.vue:394` — `else if (form.hardware_type === 'gpio')` 配置生成

**建议**: 将 `ChannelManager.vue` 加入移除清单，移除 GPIO 作为可选硬件类型的所有代码。注意 `ChannelManager.vue:131` 中 `CS${p} (GPIO${p})` 是 SPI CS 引脚标签，**应保留**。

---

### 6.2 🟡 重要 — 移除清单遗漏 DeviceConfigForm.vue

**问题**: 方案未包含 `DeviceConfigForm.vue`，但该文件有 GPIO 相关代码：

**验证**:
- `DeviceConfigForm.vue:169-170` — `<!-- GPIO 参数 -->` + `<template v-if="form.hardware_type === 'gpio'">`
- `DeviceConfigForm.vue:254` — `{ value: 'gpio', label: 'GPIO' }` 硬件类型选项

**建议**: 将 `DeviceConfigForm.vue` 加入移除清单。

---

### 6.3 🟡 重要 — deviceType.ts 移除 gpio.digital/gpio.pwm 影响范围分析不足

**问题**: 方案提到移除 `deviceType.ts` 中的 `gpio.digital` 和 `gpio.pwm`，但未分析影响范围。

**验证**: `deviceType.ts` 的 `deviceTypeOptions` 被以下组件引用：
- `PeripheralAssignForm.vue:76` — 外设分配表单的设备类型选择器
- `DeviceConfigList.vue:256` — 设备配置列表的筛选器
- `EdgeDeviceList.vue:574` — 边缘设备列表的筛选器
- `NodeDetail.vue:379` — 节点详情中 `getDeviceTypeLabel()`

移除后影响：
- `PeripheralAssignForm.vue` — 如果该表单仍用于其他设备类型，移除 GPIO/PWM 选项是正确的
- `EdgeDeviceList.vue` — 如果已有 EdgeDevice 的 device_type 为 `gpio.digital`，label 查询将返回原始值而非中文标签
- `getDeviceTypeLabel()` 和 `getDeviceTypeIcon()` — 对已存在的 `gpio.digital` 类型记录将返回 fallback 值

**建议**: 
1. 确认是否有现存 EdgeDevice 记录使用 `gpio.digital`/`gpio.pwm` 作为 device_type
2. 如有，需要数据迁移将这些记录的 device_type 改为其他值或删除
3. `getDeviceTypeLabel()` 中保留硬编码 fallback：`'gpio.digital': 'GPIO 控制'`

---

### 6.4 🔵 建议 — ParserBrowser.vue 移除不完整

**问题**: 方案提到移除 `ParserBrowser.vue:198` 的 `pwm: 'info'`，但遗漏了：
- `ParserBrowser.vue:23` — `<el-option label="GPIO" value="gpio" />` 硬件类型选项
- `ParserBrowser.vue:196` — `gpio: 'success'` 颜色映射

**建议**: 一并移除 GPIO 相关选项（如果 GPIO 不再需要解析器配置）。

---

### 6.5 🔵 建议 — channel.ts 中 hardware_type 联合类型修改后需要同步修改 deviceConfig.ts

**问题**: 方案提到修改 `channel.ts:8` 的 `hardware_type` 联合类型移除 `'gpio'` 和 `'pwm'`，但 `deviceConfig.ts` 中也有类似的联合类型包含 `'gpio'`：

**验证**:
- `deviceConfig.ts:34` — `hardware_type: 'uart' | 'i2c' | 'spi' | 'gpio' | 'adc'`
- `deviceConfig.ts:63` — 同上
- `deviceConfig.ts:74` — 同上

**建议**: `deviceConfig.ts` 中的 `hardware_type` 也应移除 `'gpio'`（PWM 从未在此类型中）。

---

## 七、迁移策略

### 7.1 🔴 致命 — 迁移 SQL 使用了错误的列名和字段格式

**问题**: 迁移 SQL（8.1 节）存在多个严重错误：

**错误 1**: `WHERE hardware_type = 'gpio'` — 但 `channels` 表中存储 GPIO 的字段可能是 `bus_type = 'GPIO'`（大写）。Channel 模型（models.go:119）中 `BusType` 默认值为 `'I2C'`（大写），`HardwareType`（models.go:116）才是小写 `'gpio'`。两个字段都可能有 GPIO 值。

**验证**: sender.go:305-311 中 `busTypeMap` 使用大写键 `"GPIO": 4`，说明 `bus_type` 字段存储大写值。但 seed SQL（v22_seed_test_data.sql:42）写入 `hardware_type = 'i2c'`（小写）和 `bus_type = 'I2C'`（大写）。

**建议**: 迁移 SQL 应同时检查两个字段：
```sql
WHERE hardware_type = 'gpio' OR bus_type = 'GPIO'
```

**错误 2**: `config::jsonb->>'direction'` — Channel 模型的 `Config` 字段类型是 `text`（models.go:122），不是 `jsonb`。直接 `::jsonb` 转换可能因非法 JSON 而失败。且 `BusConfig` 字段（models.go:120）存储的是 hex 编码的二进制配置，不是 JSON。

**验证**: sender.go:317-334 显示 `BusConfig` 可能是 hex 字符串（`hex.DecodeString`）或 `\x` 前缀的 PostgreSQL bytea 格式，不是 JSON。GPIO 的方向信息可能编码在二进制 `bus_config` 中而非 JSON `config` 字段中。

**建议**: 需要先检查现有 GPIO Channel 的 `config` 和 `bus_config` 字段的实际存储格式，再编写正确的迁移 SQL。可能需要使用 Go 脚本而非纯 SQL 来解析二进制配置。

**错误 3**: `SUBSTRING(hardware_id FROM 'GPIO([0-9]+)')` — 假设 `hardware_id` 格式为 `GPIO5`，但实际格式未验证。如果 `hardware_id` 存储的是纯数字（如 `"5"`），此正则将返回 NULL。

**建议**: 验证现有 GPIO Channel 的 `hardware_id` 字段实际格式，或使用更健壮的提取逻辑。

---

### 7.2 🟡 重要 — 迁移缺少 EdgeDevice 和 ConfigTemplate 级联清理

**问题**: 迁移 SQL 只处理了 `channels` 表，但 GPIO Channel 可能关联了：
- `edge_devices` — 通过 `channel_id` 外键
- `config_templates` — 通过 `node_id` 关联（但模板是节点级别，不直接关联 channel）
- `device_configs` — 通过 `hardware_type = 'gpio'`

**建议**: 迁移脚本应完整清理：
```sql
-- 1. 删除 GPIO Channel 上的 EdgeDevice
DELETE FROM edge_devices WHERE channel_id IN (
    SELECT id FROM channels WHERE hardware_type = 'gpio' OR bus_type = 'GPIO'
);
-- 2. 可选：删除 GPIO 专用的 DeviceConfig
DELETE FROM device_configs WHERE hardware_type = 'gpio' AND device_type IN ('gpio.digital');
-- 3. 删除 GPIO Channel
DELETE FROM channels WHERE hardware_type = 'gpio' OR bus_type = 'GPIO';
```

---

### 7.3 🔵 建议 — 迁移缺少事务保护和回滚策略

**问题**: 迁移 SQL 未包含在事务中执行，失败时无法回滚。

**建议**:
```sql
BEGIN;
-- 迁移操作...
-- 验证数据完整性
SELECT count(*) FROM gpio_configs; -- 确认数据已迁移
-- 如果验证通过
COMMIT;
-- 如果验证失败
-- ROLLBACK;
```

---

## 八、测试策略

### 8.1 🔴 致命 — 方案完全缺少测试计划

**问题**: 方案 8.4 节的「验证」步骤仅写「新路径端到端测试（GPIO: C6 物理引脚 | PWM: 示波器/万用表测量波形）」，缺少具体的测试用例设计。这是涉及协议变更、数据迁移、前后端重构的重大变更，必须有完整的测试策略。

**建议**: 补充以下测试计划：

#### 8.1.1 后端单元测试
- `handler_periph_test.go` — GPIO/PWM API CRUD 测试
  - 创建/更新/删除 GPIOConfig/PWMConfig
  - 重复 pin 创建应返回 409 Conflict（验证唯一约束）
  - 不存在的 node_id 应返回 404
- `sender_test.go` — `SendPeriphCmd()` 编码测试
  - GPIO SET_HIGH 命令编码正确性
  - PWM SET_DUTY 命令编码正确性
  - 验证使用 QoS 2 发布
- `handler_response_test.go` — `handlePeriphResponse()` 测试
  - PeriphRsp 解码和 WebSocket 推送验证
- ConfigManifest 编码测试 — 验证 field 11/12 编码正确

#### 8.1.2 后端集成测试
- GPIO 配置创建后触发 ConfigManifest 下发
- PWM 占空比设置端到端流程
- 迁移脚本测试（使用 testutil.OpenTestDB）

#### 8.1.3 前端组件测试
- `PeripheralControl.vue` — 卡片渲染、按钮点击、API 调用
- `PWMChannelCard.vue` — 滑块节流行为验证
- `GPIOPinCard.vue` — OUTPUT 模式 ON/OFF、INPUT 模式 READ

#### 8.1.4 端到端测试场景
| 场景 | 操作 | 预期结果 |
|------|------|----------|
| GPIO 输出设置 | POST /gpio/5/set {level:1} | C6 引脚 5 输出高电平 |
| GPIO 输入读取 | POST /gpio/6/read | 返回当前电平值 |
| GPIO 方向切换 | PUT /gpio/5 {direction:0} | 引脚切换为输入模式 |
| PWM 启动 | POST /pwm/15/start | 示波器测量到 PWM 波形 |
| PWM 占空比调整 | POST /pwm/15/duty {duty:5000} | 占空比变为 50% |
| PWM 频率调整 | POST /pwm/15/freq {frequency:2000} | 频率变为 2kHz |
| PWM 停止 | POST /pwm/15/stop | 波形停止 |
| 重复 pin 配置 | POST /gpio/5（已存在 pin 5） | 返回 409 Conflict |
| 节点离线操作 | POST /gpio/5/set（节点离线） | 返回超时或节点不可达错误 |
| ConfigManifest 同步 | 创建 GPIOConfig 后 | 节点收到 ConfigManifest 包含 field 11 |
| 数据迁移 | 执行迁移脚本 | GPIO Channel 数据正确迁移到 gpio_configs 表 |
| 旧路径兼容 | 迁移期间旧 GPIO Channel 仍可用 | 旧路径和新路径并行无冲突 |

---

## 九、其他问题

### 9.1 🟡 重要 — AutoMigrate 注册遗漏

**问题**: 新增的 `GPIOConfig` 和 `PWMConfig` 模型需要在 `AutoMigrate()` 中注册才能自动创建表。

**验证**: `database/gorm.go:45-73` 的 `AutoMigrate()` 函数和 `testutil/db.go:31-53` 的 `allModels` 列表都需要添加新模型。

**建议**:
```go
// database/gorm.go — AutoMigrate() 中添加
&models.GPIOConfig{},
&models.PWMConfig{},

// testutil/db.go — allModels 中添加
&models.GPIOConfig{},
&models.PWMConfig{},
```

---

### 9.2 🔵 建议 — 协议消息类型号 0x1B/0x1C 需更新协议文档

**问题**: 方案新增 `MsgPeriphCmd=0x1B` 和 `MsgPeriphRsp=0x1C`，但需同步更新 `docs/协议/二进制帧协议.md` 中的消息类型表（当前最大 0x1A）和 `frame.go` 中的消息类型常量。

**建议**: 在实施步骤中明确包含协议文档更新。

---

### 9.3 🔵 建议 — GPIO/PWM ConfigManifest field 11/12 需考虑协议版本兼容

**问题**: ConfigManifest field 11/12 是新增字段，旧版本固件（protocol_version < 2.3）可能不识别。方案未说明如何处理版本兼容。

**验证**: sender.go:255 中 `useV2 := parseProtocolVersion(node.ProtocolVersion) >= 2.3`，现有代码已有版本分支逻辑。

**建议**: 只对支持外设控制的协议版本下发 field 11/12，旧版本固件忽略这些字段（protobuf 兼容性保证未知字段不会被解析，但需确认 C 端解码器能正确跳过未知 field）。

---

## 评审结论

### 必须修复的致命问题（实施前必须解决）
1. **数据模型缺少唯一约束** — `(node_id, pin)` 复合唯一索引
2. **路由参数命名冲突** — `:node_id` vs `:id` 会导致 Gin panic
3. **WebSocket 推送完全遗漏** — GPIO/PWM 操作结果无实时推送
4. **迁移 SQL 列名和字段格式错误** — `hardware_type` vs `bus_type`、JSON vs hex

### 建议修复的重要问题（实施时应解决）
1. `PWMConfig.Running` 不应持久化
2. EdgeDevice 级联清理遗漏
3. `SendPeriphCmd()` QoS 策略未明确
4. `PeriphRsp` 消息分发未纳入 manager.go
5. ConfigManifest 编码逻辑遗漏
6. 前端清理遗漏 `ChannelManager.vue` 和 `DeviceConfigForm.vue`
7. `deviceType.ts` 移除影响分析不足
8. AutoMigrate 注册遗漏

### 可后续改进的建议
1. GPIO 电阻状态字段
2. API 权限控制细化
3. 引脚资源可用性校验
4. 迁移事务保护
5. 协议文档同步更新
6. 协议版本兼容处理

**总体评价**: 方案架构方向正确，核心设计合理，但在实施细节上存在多处关键遗漏和错误，特别是迁移 SQL、路由设计、WebSocket 推送和前端清理完整性方面。建议修复所有致命和重要问题后再进入实施阶段。
