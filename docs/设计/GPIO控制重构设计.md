# 外设控制重构设计 — GPIO 与 PWM 从通道系统中剥离

> **版本**: v3.0 (2026-07-09, 专家评审修订版)
> **状态**: 实施与最终验收中（ResourceReport 独立 GPIO/PWM 资源、后端/前端与固件实现已落盘；须待最新 fail-closed 复审、C6 OTA 与实机回归通过后标记完成）
> **分支**: feat/gpio-control
> **关联**: [00-术语表.md](../00-术语表.md) | [通道/详细设计.md](通道/详细设计.md) | [ESP32架构设计.md](ESP32架构设计.md)
> **评审**: [评审报告(嵌入式专家)](GPIO控制重构设计-评审报告.md) | [评审报告(后端+前端专家)](../评审/GPIO_PWM重构设计评审报告.md)

---

## 修订日志

| 版本 | 日期 | 修订内容 |
|------|------|----------|
| v1.0 | 2026-07-09 | 初版：GPIO 从通道剥离 |
| v2.0 | 2026-07-09 | 合并 PWM，统一外设控制方案 |
| v3.0 | 2026-07-09 | 专家评审修订：修复 7 致命 + 14 重要问题 |

---

## 1. 问题陈述

### 1.1 核心观点

**GPIO 和 PWM 不应该使用通道（Channel），只有可以进行数据交互的才支持通道。**

| 类别 | 本质 | 是否通道 | 说明 |
|------|------|----------|------|
| UART | 通信总线 | ✅ 是 | 有协议帧、有外部设备、有收发时序 |
| I2C | 通信总线 | ✅ 是 | 有地址、有协议、多设备共享 |
| SPI | 通信总线 | ✅ 是 | 有 CS 寻址、有协议、有时钟 |
| GPIO | 引脚直控 | ❌ 否 | 设置/读取高/低电平，MCU 外设控制 |
| PWM | 波形输出 | ❌ 否 | 设置频率/占空比，MCU 外设控制 |
| ADC | 模拟量采集 | ⚠️ 待定 | 有采样间隔概念，更接近数据采集，后续单独评估 |

### 1.2 GPIO 当前架构问题

GPIO 被当作一种"通道"，走完整通道路径（6跳）：前端→channel API→WriteCmd(0x06)→MQTT→bus_manager→gpio_cmd_queue→bus_worker→bus_dma_transact→gpio_write→gpio_set_level。语义错位、资源浪费、概念污染（详见 v2.0 文档）。

### 1.3 PWM 当前状态

**PWM 仅有前端 UI 占位，后端和固件零实现。** 从零设计，无迁移负担。

---

## 2. 统一协议消息

### 2.1 消息类型

```
MsgPeriphCmd = 0x1B  (中心端 → 节点)
MsgPeriphRsp = 0x1C  (节点 → 中心端)
```

当前协议最大消息类型 0x1A (QueryResources)，0x1B/0x1C 可用。需同步更新 `frame.go`、`frame_codec.h` 和协议文档。

### 2.2 PeriphCmd 字段定义

```
field 1: request_id  (varint, uint32) — 用于匹配响应
field 2: periph_type (varint, uint8)  — 1=GPIO, 2=PWM
field 3: resource_id (varint, uint8)  — GPIO=pin；PWM=LEDC channel
field 4: action      (varint, uint8)  — 操作类型（见下表）
field 5: value       (varint, uint32) — 操作参数（如 duty/freq）
field 6: config      (bytes)          — 配置参数（二进制编码）
```

wire type 标注：field 1-5 为 varint (wire_type=0)，field 6 为 length-delimited (wire_type=2)。

### 2.3 PeriphRsp 字段定义

```
field 1: request_id  (varint, uint32) — 匹配 PeriphCmd
field 2: success     (varint, bool)   — 操作是否成功
field 3: value       (varint, uint32) — 返回值（如 read 的 level/duty）
field 4: error_code  (varint, uint8)  — 错误码
field 5: periph_type (varint, uint8)  — [可选] 类型标识，用于异步事件场景
field 6: resource_id (varint, uint8)  — [可选] GPIO=pin；PWM=LEDC channel
```

> **评审修订**: 增加 field 5/6 可选字段，为未来异步事件推送预留。同步响应用 request_id 匹配即可。

### 2.4 GPIO action 枚举

| 值 | 名称 | value 含义 | config 含义 | 响应 |
|----|------|-----------|------------|------|
| 0 | SET_LOW | — | — | success |
| 1 | SET_HIGH | — | — | success |
| 2 | READ | — | — | success + value(0/1) |
| 3 | CONFIG | — | [direction:1B, initial_level:1B] | success |
| 4 | DECONFIG | — | — | success |
| 5 | TOGGLE | — | — | success + value(新电平) |

> **评审修订**: 新增 action 5=TOGGLE，固件内 gpio_get_level→gpio_set_level(!level) 原子执行，避免 READ→判断→SET 两次往返。

GPIO direction 编码：0=INPUT, 1=OUTPUT, 2=INPUT_PULLUP, 3=INPUT_PULLDOWN

### 2.5 PWM action 枚举

| 值 | 名称 | value 含义 | config 含义 | 响应 |
|----|------|-----------|------------|------|
| 0 | SET_DUTY | duty (0-10000) | — | success |
| 1 | SET_FREQ | frequency (Hz) | [resolution:1B] | success |
| 2 | START | — | [pin:1B, freq:4B LE, duty:2B LE, resolution:1B] | success |
| 3 | STOP | — | — | success |
| 4 | READ | — | — | success + value(当前 duty) |
| 5 | SET_RESOLUTION | resolution_bits (4-20) | — | success |

> **评审修订**:
> - SET_FREQ 的 config 字段增加 [resolution:1B]，频率切换时校验/调整分辨率，避免 duty 溢出
> - 新增 action 5=SET_RESOLUTION，支持运行时动态调整分辨率

PWM START 的 config 编码：
- byte 0: 输出路由 GPIO pin
- byte 1-4: frequency (uint32, little-endian, Hz)
- byte 5-6: duty (uint16, little-endian, 0-10000 = 0.00%-100.00%)
- byte 7: resolution_bits (4-20，并受节点上报 PWM 资源上限约束)

PWM 的资源身份始终是 field 3 的 LEDC channel；START config 中的 pin 只表示该 channel 的 GPIO matrix 输出路由。

### 2.6 error_code 枚举

| 值 | 名称 | 说明 |
|----|------|------|
| 0 | OK | 成功 |
| 1 | INVALID_PIN | 引脚号无效或不可用 |
| 2 | INVALID_ACTION | 操作类型不支持 |
| 3 | INVALID_PARAM | 参数错误（如 duty 超范围） |
| 4 | RESOURCE_EXHAUSTED | 资源耗尽（如 LEDC 定时器/通道已满） |
| 5 | NOT_CONFIGURED | 引脚未配置（SET/READ 前需 CONFIG） |
| 6 | HW_ERROR | 硬件错误（gpio_set_level 等返回非 OK） |
| 7 | PIN_CONFLICT | 引脚已被其他模块占用 |

> **评审修订**: 新增 error_code 枚举定义，handler_periph_process 做完整参数校验。

---

## 3. ConfigManifest 扩展

### 3.1 字段分配

现有 ConfigManifest 顶层字段：1=manifest_id, 2=removed, 3=templates, 4=channels, 5=dma_configs。field 6-10 未使用，预留给通道系统扩展。

新增：
- **field 11**: gpio_configs (repeated bytes, wire_type=2)
- **field 12**: pwm_configs (repeated bytes, wire_type=2)

> **评审修订**: 明确声明 field 6-10 预留状态和分配理由。

### 3.2 gpio_configs 子消息

每个 gpio_config 子消息（length-delimited）内部编码：
- sub-field 1: pin (varint, uint8)
- sub-field 2: direction (varint, uint8) — 0=INPUT, 1=OUTPUT, 2=INPUT_PULLUP, 3=INPUT_PULLDOWN
- sub-field 3: initial_level (varint, uint8) — OUTPUT 时的初始电平

### 3.3 pwm_configs 子消息

每个 pwm_config 子消息（length-delimited）内部编码：
- sub-field 1: pin (varint, uint8) — GPIO 引脚号
- sub-field 2: frequency (varint, uint32) — 频率 (Hz)
- sub-field 3: duty (varint, uint16) — 占空比 (0-10000)
- sub-field 4: resolution (varint, uint8) — 分辨率位数 (默认 14)
- sub-field 5: auto_start (varint, bool) — 是否在 ConfigManifest 应用后自动启动

> **评审修订**: 
> - resolution 默认值 13→14（16384 级 > 10000 级 duty，消除虚标）
> - 新增 auto_start 字段替代 Running 持久化字段

### 3.4 ConfigRslt 部分成功处理

gpio_configs/pwm_configs 的应用失败**不影响整体 ConfigRslt**（仍报 success=true）。单个引脚配置失败通过 PeriphRsp 异步报告。

> **评审修订**: 明确 ConfigRslt 不扩展，外设配置失败通过 PeriphRsp 异步报告，保持现有协议简单性。

### 3.5 协议版本兼容

只对 `protocol_version >= 2.4` 的节点下发 field 11/12。旧版本固件忽略未知字段（C 端 frame_decoder 会跳过未识别的 field_num）。

---

## 4. 后端设计

### 4.1 数据模型

#### GPIOConfig

```go
type GPIOConfig struct {
    ID           uint           `gorm:"primaryKey" json:"id"`
    NodeID       string         `gorm:"column:node_id;type:varchar(32);index:idx_gpio_node_pin,unique;not null" json:"node_id"`
    Pin          int            `gorm:"index:idx_gpio_node_pin,unique;not null" json:"pin"`
    Direction    uint8          `gorm:"not null" json:"direction"`
    InitialLevel uint8          `gorm:"default:0" json:"initial_level"`
    Label        string         `gorm:"size:64" json:"label"`
    Enabled      bool           `gorm:"default:true" json:"enabled"`
    CreatedAt    time.Time      `json:"created_at"`
    UpdatedAt    time.Time      `json:"updated_at"`
    DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}
```

#### PWMConfig

```go
type PWMConfig struct {
    ID           uint           `gorm:"primaryKey" json:"id"`
    NodeID       string         `gorm:"column:node_id;type:varchar(32);index:idx_pwm_node_pin,unique;not null" json:"node_id"`
    Pin          int            `gorm:"index:idx_pwm_node_pin,unique;not null" json:"pin"`
    Frequency    uint32         `gorm:"not null" json:"frequency"`
    Duty         uint16         `gorm:"default:0" json:"duty"`
    Resolution   uint8          `gorm:"default:14" json:"resolution"`
    AutoStart    bool           `gorm:"default:false" json:"auto_start"`
    Label        string         `gorm:"size:64" json:"label"`
    Enabled      bool           `gorm:"default:true" json:"enabled"`
    CreatedAt    time.Time      `json:"created_at"`
    UpdatedAt    time.Time      `json:"updated_at"`
    DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}
```

> **评审修订**:
> - 添加 `(node_id, pin)` 复合唯一索引
> - `Running` 字段移除，改为 `AutoStart` — 运行时状态不持久化
> - Resolution 默认值 13→14

#### AutoMigrate 注册

```go
// database/gorm.go — AutoMigrate() 中添加
&models.GPIOConfig{},
&models.PWMConfig{},

// testutil/db.go — allModels 中添加
&models.GPIOConfig{},
&models.PWMConfig{},
```

### 4.2 API 端点

统一使用 `:id` 参数名（与现有 `/nodes/:id` 路由一致），避免 Gin panic。

#### GPIO API (完整路径含 /api/v1 前缀)

| 方法 | 路径 | 功能 |
|------|------|------|
| GET | `/api/v1/nodes/:id/gpio` | 列出节点所有 GPIO 配置 |
| POST | `/api/v1/nodes/:id/gpio` | 配置 GPIO（pin + direction + label） |
| PUT | `/api/v1/nodes/:id/gpio/:pin` | 更新 GPIO 配置 |
| DELETE | `/api/v1/nodes/:id/gpio/:pin` | 取消 GPIO 配置 |
| POST | `/api/v1/nodes/:id/gpio/:pin/set` | 设置输出电平 `{level: 0\|1}` |
| POST | `/api/v1/nodes/:id/gpio/:pin/read` | 读取引脚电平 |

#### PWM API

| 方法 | 路径 | 功能 |
|------|------|------|
| GET | `/api/v1/nodes/:id/pwm` | 列出节点所有 PWM 配置 |
| POST | `/api/v1/nodes/:id/pwm` | 配置 PWM |
| PUT | `/api/v1/nodes/:id/pwm/:hardware_id` | 更新 PWM 通道配置 |
| DELETE | `/api/v1/nodes/:id/pwm/:hardware_id` | 取消 PWM 通道配置 |
| POST | `/api/v1/nodes/:id/pwm/:hardware_id/start` | 启动 PWM 输出 |
| POST | `/api/v1/nodes/:id/pwm/:hardware_id/stop` | 停止 PWM 输出 |
| POST | `/api/v1/nodes/:id/pwm/:hardware_id/duty` | 设置占空比 `{duty: 0-10000}` |
| POST | `/api/v1/nodes/:id/pwm/:hardware_id/freq` | 设置频率 `{frequency: Hz}` |
| GET | `/api/v1/nodes/:id/pwm/:hardware_id/state` | 读取当前状态 |

> **评审修订**: `:node_id` → `:id`，避免 Gin 路由冲突 panic。

#### 路由注册

```go
// handler_periph.go 中注册
n := v1.Group("/nodes")
n.GET("/:id/gpio", ...)
n.POST("/:id/gpio", ...)
// ...
n.GET("/:id/pwm", ...)
n.POST("/:id/pwm", ...)
```

继承现有 JWT 认证中间件（v1.Use(JWTAuth())）。

### 4.3 ConfigEventBus 扩展

新增配置变更事件类型：

```go
// config_event_bus.go
CfgChangeGPIO ConfigChangeType = "gpio"
CfgChangePWM  ConfigChangeType = "pwm"
```

GPIO/PWM CRUD 操作调用 `EmitConfigChange` 触发 SyncGate 重新计算 config hash 并下发 ConfigManifest。

### 4.4 Config Hash 计算

`CalcConfigHashForDevice` 和 `buildHashData` 需要包含 GPIO/PWM 配置：

```go
// manager.go — CalcConfigHashForDevice 中新增查询
var gpioConfigs []models.GPIOConfig
tx.Order("pin ASC").Where("node_id = ? AND enabled = ?", node.NodeID, true).Find(&gpioConfigs)
var pwmConfigs []models.PWMConfig
tx.Order("pin ASC").Where("node_id = ? AND enabled = ?", node.NodeID, true).Find(&pwmConfigs)

// buildHashData 中新增序列化
for _, g := range gpioConfigs {
    buf = append(buf, []byte(fmt.Sprintf("g:%d:%d:%d:%v:", g.Pin, g.Direction, g.InitialLevel, g.Enabled))...)
}
for _, p := range pwmConfigs {
    buf = append(buf, []byte(fmt.Sprintf("p:%d:%d:%d:%d:%v:", p.Pin, p.Frequency, p.Duty, p.Resolution, p.Enabled))...)
}
```

> **自查遗漏修复**: 不加入 hash 计算，SyncGate 无法检测 GPIO/PWM 配置变化。

### 4.5 ConfigManifest 编码

`SendConfigManifestWithDecision` 中新增 GPIO/PWM 配置编码：

```go
// sender.go — 在 dma_configs 编码后新增
var gpioConfigs []models.GPIOConfig
m.db.Where("node_id = ? AND enabled = ?", node.NodeID, true).Order("pin ASC").Find(&gpioConfigs)
for _, gc := range gpioConfigs {
    subEnc := frame.SubEncoder()
    subEnc.EncodeVarint(1, uint64(gc.Pin))
    subEnc.EncodeVarint(2, uint64(gc.Direction))
    subEnc.EncodeVarint(3, uint64(gc.InitialLevel))
    enc.EncodeSubFrame(11, subEnc.Bytes())
}

var pwmConfigs []models.PWMConfig
m.db.Where("node_id = ? AND enabled = ?", node.NodeID, true).Order("pin ASC").Find(&pwmConfigs)
for _, pc := range pwmConfigs {
    subEnc := frame.SubEncoder()
    subEnc.EncodeVarint(1, uint64(pc.Pin))
    subEnc.EncodeVarint(2, uint64(pc.Frequency))
    subEnc.EncodeVarint(3, uint64(pc.Duty))
    subEnc.EncodeVarint(4, uint64(pc.Resolution))
    subEnc.EncodeBool(5, pc.AutoStart)
    enc.EncodeSubFrame(12, subEnc.Bytes())
}
```

### 4.6 SendPeriphCmd

```go
func (m *Manager) SendPeriphCmd(deviceID string, periphType uint8, resourceID uint8,
    action uint8, value uint32, config []byte) error {
    requestID := atomic.AddUint32(&nextRequestID, 1)
    enc := frame.NewEncoder(frame.MsgPeriphCmd)
    enc.EncodeVarint(1, uint64(requestID))
    enc.EncodeVarint(2, uint64(periphType))
    enc.EncodeVarint(3, uint64(resourceID))
    enc.EncodeVarint(4, uint64(action))
    if value > 0 {
        enc.EncodeVarint(5, uint64(value))
    }
    if len(config) > 0 {
        enc.EncodeBytes(6, config)
    }
    topic := mqtt.TopicForNode(deviceID)
    return m.mqtt.PublishQoS1(topic, enc.Bytes()) // QoS 1；request_id 负责业务关联
}
```

> **实机修订**: 使用 QoS 1，避免 QoS 2 inflight 阻塞；request_id/PeriphRsp 提供命令关联。

### 4.7 PeriphRsp 处理

```go
// manager.go — 消息分发 switch 新增
case frame.MsgPeriphRsp:
    m.handlePeriphResponse(deviceID, payload)
```

```go
// handler_response.go — 新增
func (m *Manager) handlePeriphResponse(deviceID string, payload []byte) {
    // 解码 PeriphRsp: request_id, success, value, error_code, [periph_type, pin]
    // ...
    // WebSocket 推送
    m.wsHub.BroadcastEvent(events.PeriphResult, map[string]interface{}{
        "node_id":     deviceID,
        "request_id":  requestID,
        "success":     success,
        "value":       value,
        "error_code":  errorCode,
    })
}
```

### 4.8 WebSocket 事件

```go
// events.go 新增
PeriphResult = "periph_result"  // GPIO/PWM 操作结果
PeriphState  = "periph_state"   // GPIO/PWM 状态变更
```

> **评审修订**: 补充遗漏的 WebSocket 实时推送。前端 PeripheralControl.vue 监听 periph_result 事件更新卡片状态。

### 4.9 后端影响文件清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `models/models.go` | 新增 | GPIOConfig + PWMConfig 模型（含唯一索引） |
| `database/gorm.go` | 修改 | AutoMigrate 注册新模型 |
| `testutil/db.go` | 修改 | allModels 注册新模型 |
| `api/handler_periph.go` | 新增 | GPIO + PWM API 处理器 |
| `api/routes.go` | 修改 | 注册 GPIO/PWM 路由 |
| `api/handler_device.go` | 修改 | channel CRUD 排除 gpio/pwm |
| `api/handler_terminal.go` | 修改 | terminal write 排除 gpio/pwm |
| `nodemgr/sender.go` | 修改 | SendPeriphCmd + ConfigManifest field 11/12 编码 |
| `nodemgr/manager.go` | 修改 | CalcConfigHashForDevice + buildHashData + PeriphRsp 分发 |
| `nodemgr/handler_response.go` | 修改 | 新增 handlePeriphResponse + WebSocket 推送 |
| `nodemgr/config_event_bus.go` | 修改 | 新增 CfgChangeGPIO/CfgChangePWM |
| `events/events.go` | 修改 | 新增 PeriphResult/PeriphState |
| `frame/frame.go` | 修改 | 新增 MsgPeriphCmd/Rsp 常量 |
| `drivers/gpio/` | 删除 | 整个目录 |
| `drivers/gpio_adapter.go` | 删除 | |
| `drivers/gpio_driver_test.go` | 删除 | |
| `drivers/builtin.go` | 修改 | 移除 GPIO driver 注册 |

---

## 5. 前端设计

### 5.1 PeripheralControl.vue

GPIO + PWM 统一卡片网格展示（UI 设计同 v2.0，略）。

### 5.2 PWM 滑块防抖

```typescript
// PWMChannelCard.vue — 使用 @input 实时更新本地显示，@change 发送 API
const localDuty = ref(0)

const onDutyInput = (val: number) => {
  localDuty.value = val  // 仅更新本地 UI
}

const onDutyChange = debounce(async (val: number) => {
  await pwmApi.setDuty(nodeId, pin, val)
}, 300)

// <el-slider :model-value="localDuty" @input="onDutyInput" @change="onDutyChange" />
```

> **评审修订**: 明确防抖策略，避免拖动时 API 请求风暴。

### 5.3 前端影响文件清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `components/periph/PeripheralControl.vue` | 新增 | 统一控制组件 |
| `components/periph/GPIOPinCard.vue` | 新增 | GPIO 卡片 |
| `components/periph/PWMChannelCard.vue` | 新增 | PWM 卡片（含防抖） |
| `api/periph.ts` | 新增 | GPIO + PWM API |
| `components/channel/ChannelTerminal.vue` | 修改 | 移除 GPIO 代码（~60行） |
| `components/channel/ChannelManager.vue` | 修改 | 移除 GPIO 硬件类型选项和参数表单 |
| `components/node/ChannelPanel.vue` | 修改 | 移除 gpio/pwm busType |
| `components/forms/DeviceConfigForm.vue` | 修改 | 移除 GPIO 参数和选项 |
| `api/channel.ts` | 修改 | hardware_type 移除 'gpio'/'pwm' |
| `api/deviceConfig.ts` | 修改 | hardware_type 移除 'gpio' |
| `utils/deviceType.ts` | 修改 | 移除 gpio.digital/gpio.pwm，保留 fallback 标签 |
| `views/channel/ChannelList.vue` | 修改 | 移除 GPIO/PWM 筛选选项 |
| `views/config/DeviceConfigList.vue` | 修改 | 移除 PWM 硬件类型选项 |
| `components/parser/ParserBrowser.vue` | 修改 | 移除 gpio/pwm 选项和颜色映射 |

> **评审修订**: 补充遗漏的 ChannelManager.vue、DeviceConfigForm.vue、deviceConfig.ts、ParserBrowser.vue。

### 5.4 deviceType.ts 影响处理

`getDeviceTypeLabel()` 中保留硬编码 fallback：
```typescript
const fallbackLabels: Record<string, string> = {
  'gpio.digital': 'GPIO 控制',
  'gpio.pwm': 'PWM 输出',
}
```
确保已存在的 `gpio.digital` 类型 EdgeDevice 记录仍能正确显示标签。

---

## 6. 固件设计

### 6.1 从 bus 系统中移除 GPIO

（同 v2.0，删除 bus_dma/bus_manager/bus_worker/scheduler 中所有 BUS_TYPE_GPIO 相关代码。详见 v2.0 文档 §6.1。）

### 6.2 gpio_ctrl 模块

```c
// gpio_ctrl.h
typedef struct {
    uint8_t pin;
    uint8_t direction;
    uint8_t initial_level;
} gpio_config_entry_t;

typedef enum {
    GPIO_ACTION_SET_LOW   = 0,
    GPIO_ACTION_SET_HIGH  = 1,
    GPIO_ACTION_READ      = 2,
    GPIO_ACTION_CONFIG    = 3,
    GPIO_ACTION_DECONFIG  = 4,
    GPIO_ACTION_TOGGLE    = 5,
} gpio_action_t;

esp_err_t gpio_ctrl_init(const gpio_config_entry_t *configs, int count);
esp_err_t gpio_ctrl_set(int pin, int level);
int gpio_ctrl_read(int pin);
esp_err_t gpio_ctrl_config(int pin, int direction, int initial_level);
esp_err_t gpio_ctrl_deconfig(int pin);
esp_err_t gpio_ctrl_toggle(int pin);
```

**并发安全设计**：
- `gpio_set_level()` / `gpio_get_level()` 线程安全，SET/READ/TOGGLE 不需要 mutex
- `gpio_config()` / `gpio_reset_pin()` 不是线程安全，CONFIG/DECONFIG 加 mutex
- `gpio_ctrl_init()` 重新配置时设置 `s_reconfiguring` 标志，SET/READ 检查并拒绝
- 维护 `s_gpio_state[]` 数组记录引脚配置状态，SET/READ 前校验

> **评审修订**: 明确并发安全策略，区分 SET/READ（无锁）和 CONFIG/DECONFIG（加锁）。

### 6.3 pwm_ctrl 模块

```c
// pwm_ctrl.h
#define PWM_DUTY_MIN     0
#define PWM_DUTY_MAX     10000
#define PWM_RES_DEFAULT  14

typedef struct {
    uint8_t  pin;
    uint32_t frequency;
    uint16_t duty;
    uint8_t  resolution;
    bool     auto_start;
} pwm_config_entry_t;

esp_err_t pwm_ctrl_init(const pwm_config_entry_t *configs, int count);
esp_err_t pwm_ctrl_start(int channel, int pin, uint32_t freq, uint16_t duty, uint8_t resolution);
esp_err_t pwm_ctrl_stop(int channel);
esp_err_t pwm_ctrl_set_duty(int channel, uint16_t duty);
esp_err_t pwm_ctrl_set_freq(int channel, uint32_t freq, uint8_t resolution);
esp_err_t pwm_ctrl_set_resolution(int channel, uint8_t resolution);
esp_err_t pwm_ctrl_get_duty(int channel, uint16_t *duty);
esp_err_t pwm_ctrl_deconfig(int channel);
```

**LEDC 资源管理**：
- ESP32-C6: 6 通道，4 定时器（`SOC_LEDC_TIMER_NUM=4`）
- 同频率通道共享定时器，引用计数管理
- STOP 时递减引用计数，归零时 deinit 定时器
- 资源耗尽返回 `ESP_ERR_NOT_FOUND`（error_code=4）
- `deconfig` 幂等：先 stop 再释放，未启动时直接释放安全

**duty 精度说明**：
- 14 bit (16384 级) > 10000 级 duty，消除虚标
- 映射公式：`ledc_duty = (duty * 2^resolution) / 10000`
- 14 bit 时实际精度 = 100/16384 ≈ 0.0061%，优于 0.01%

**频率-分辨率联合约束**：
- `freq * 2^resolution ≤ APB_CLK / 2` (APB_CLK = 80MHz)
- pwm_ctrl_start() 中校验 (freq, resolution) 组合可行性
- 不可行时返回 `ESP_ERR_INVALID_ARG`（error_code=3）

> **评审修订**: 
> - 定时器引用计数管理
> - 资源耗尽错误处理
> - duty 精度虚标问题（默认 14bit）
> - 频率-分辨率联合约束
> - SET_FREQ 携带 resolution 参数

### 6.4 PeriphRsp 发送线程模型

**不能在 MQTT 回调中直接调用 `esp_mqtt_client_publish()`**（ESP-IDF 文档明确警告死锁风险）。

设计方案：
- GPIO 操作（μs 级）在 MQTT 回调中直接执行
- PWM START/STOP（百 μs 级）通过轻量队列转发到 `periph_worker` 任务
- PeriphRsp 通过专用发送队列异步发送（`periph_rsp_queue` → 独立任务 → `esp_mqtt_client_publish()`）

```
MQTT回调 → handler_periph_process
  ├── GPIO: 直接执行 gpio_ctrl_set/read → 结果投递到 periph_rsp_queue
  └── PWM: 命令投递到 periph_cmd_queue → periph_worker 执行 → 结果投递到 periph_rsp_queue

periph_rsp_task: 从 periph_rsp_queue 取结果 → esp_mqtt_client_publish(PeriphRsp)
```

> **评审修订**: 解决 PeriphRsp 在 MQTT 回调中发送的死锁风险。这是最关键的架构修改。

### 6.5 引脚冲突检测

`gpio_ctrl_config()` 和 `pwm_ctrl_start()` 中校验 pin 不与其他模块冲突：
- 检查 pin 不在已配置的 UART/I2C/SPI 引脚范围内
- 检查 pin 不在已配置的 GPIO/PWM 引脚范围内
- 不可用引脚：GPIO12/13 (USB), GPIO8 (RGB LED)

### 6.6 ConfigManifest 解析

固件 `config_manifest_t` 结构体扩展：

```c
// config_mgr.h 新增
#define MAX_GPIO_CONFIGS  12
#define MAX_PWM_CONFIGS   6

typedef struct {
    uint8_t pin;
    uint8_t direction;
    uint8_t initial_level;
} config_gpio_t;

typedef struct {
    uint8_t  pin;
    uint32_t frequency;
    uint16_t duty;
    uint8_t  resolution;
    bool     auto_start;
} config_pwm_t;

// config_manifest_t 新增字段
config_gpio_t gpio_configs[MAX_GPIO_CONFIGS];
uint8_t       gpio_config_count;
config_pwm_t  pwm_configs[MAX_PWM_CONFIGS];
uint8_t       pwm_config_count;
```

`parse_manifest()` 新增 `parse_field_gpio_config()` 和 `parse_field_pwm_config()`，处理 field 11/12。

`msg_handler_internal.h` 新增枚举：
```c
/* PeriphCmd (0x1B) */
typedef enum {
    PERIPH_CMD_F_REQUEST_ID  = 1,
    PERIPH_CMD_F_PERIPH_TYPE = 2,
    PERIPH_CMD_F_PIN         = 3,
    PERIPH_CMD_F_ACTION      = 4,
    PERIPH_CMD_F_VALUE       = 5,
    PERIPH_CMD_F_CONFIG      = 6,
} periph_cmd_field_t;

/* PeriphRsp (0x1C) */
typedef enum {
    PERIPH_RSP_F_REQUEST_ID  = 1,
    PERIPH_RSP_F_SUCCESS     = 2,
    PERIPH_RSP_F_VALUE       = 3,
    PERIPH_RSP_F_ERROR_CODE  = 4,
    PERIPH_RSP_F_PERIPH_TYPE = 5,
    PERIPH_RSP_F_PIN         = 6,
} periph_rsp_field_t;

/* ConfigManifest GPIO/PWM sub-fields */
typedef enum {
    GPIO_CFG_F_PIN           = 1,
    GPIO_CFG_F_DIRECTION     = 2,
    GPIO_CFG_F_INITIAL_LEVEL = 3,
} gpio_config_field_t;

typedef enum {
    PWM_CFG_F_PIN        = 1,
    PWM_CFG_F_FREQUENCY  = 2,
    PWM_CFG_F_DUTY       = 3,
    PWM_CFG_F_RESOLUTION = 4,
    PWM_CFG_F_AUTO_START = 5,
} pwm_config_field_t;
```

### 6.7 固件影响文件清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `components/bus_dma/bus_dma.h/.c` | 修改 | 移除 BUS_TYPE_GPIO |
| `components/bus_manager/bus_manager.c` | 修改 | 移除 GPIO 路由 |
| `components/bus_worker/bus_worker.c/.h` | 修改 | 移除 cmd_task_gpio |
| `components/scheduler/scheduler.c/.h` | 修改 | 移除 GPIO 队列 |
| `components/gpio_ctrl/` | 新增 | GPIO 控制模块（含并发安全） |
| `components/pwm_ctrl/` | 新增 | PWM 控制模块（LEDC + 引用计数） |
| `components/msg_handler/msg_handler.c` | 修改 | 新增 MSG_PERIPH_CMD 分发 |
| `components/msg_handler/handler_periph.c` | 新增 | PeriphCmd 处理 + 异步发送 |
| `components/msg_handler/msg_handler_internal.h` | 修改 | 新增枚举定义 |
| `components/frame/frame_codec.h` | 修改 | 新增 MSG_PERIPH_CMD/RSP |
| `components/config_mgr/config_mgr.h` | 修改 | 新增 config_gpio_t/pwm_t + manifest 扩展 |
| `components/config_mgr/config_mgr.c` | 修改 | parse_manifest 新增 field 11/12 解析 |
| `components/hw_profile/hw_tables.c` | 保留 | hw_gpios[] 仍用于资源上报 |
| `components/hw_profile/hw_profile.c` | 修改 | ResourceReport 新增 PWM 资源 (field 6) |
| `main/main.c` | 修改 | 移除 GPIO 旧路由，新增 periph 回调 + rsp 任务 |
| `components/CMakeLists.txt` | 修改 | 新增 gpio_ctrl/pwm_ctrl 组件 |

---

## 7. 数据流对比

### GPIO（当前 6 跳 → 重新设计 3 跳）

当前：前端→channel API→WriteCmd→bus_manager→队列→worker→transact→gpio_write→gpio_set_level
重新设计：前端→POST /nodes/:id/gpio/:pin/set→PeriphCmd(0x1B)→gpio_ctrl_set→gpio_set_level→PeriphRsp(0x1C)→前端

### PWM（全新 3 跳）

前端→POST /nodes/:id/pwm/:hardware_id/duty→PeriphCmd(resource_id=channel)→periph_worker→pwm_ctrl_set_duty(channel)→ledc_set_duty→PeriphRsp(0x1C)→前端

---

## 8. 迁移策略

### 8.1 GPIO 数据迁移

由于现有 GPIO Channel 的 `bus_config` 是 hex 编码二进制（非 JSON），`config` 字段格式不确定，建议使用 Go 迁移脚本而非纯 SQL：

```go
// migration_gpio.go
func MigrateGPIOChannels(db *gorm.DB) error {
    var channels []models.Channel
    db.Where("hardware_type = 'gpio' OR bus_type = 'GPIO'").Find(&channels)

    for _, ch := range channels {
        // 从 bus_config (hex) 解析 pin 和 direction
        busConfig, _ := hex.DecodeString(ch.BusConfig)
        if len(busConfig) < 2 { continue }
        pin := int(busConfig[0])
        direction := busConfig[1]

        // 从 config (JSON string 或空) 解析 label
        var cfg map[string]interface{}
        json.Unmarshal([]byte(ch.Config), &cfg)
        label, _ := cfg["label"].(string)

        // 插入 GPIOConfig
        db.Create(&models.GPIOConfig{
            NodeID:    ch.NodeID,
            Pin:       pin,
            Direction: direction,
            Label:     label,
            Enabled:   ch.Enabled,
        })

        // 删除关联的 EdgeDevice
        db.Where("channel_id = ?", ch.ID).Delete(&models.EdgeDevice{})
        // 删除 Channel
        db.Delete(&ch)
    }
    return nil
}
```

> **评审修订**: 
> - 使用 Go 脚本替代纯 SQL（正确处理 hex 编码的 bus_config）
> - 同时检查 hardware_type='gpio' 和 bus_type='GPIO'
> - 先清理 EdgeDevice 再删除 Channel
> - 迁移在事务中执行

### 8.2 PWM 数据迁移

无 — PWM 无现有数据。

### 8.3 迁移期间引脚冲突防护

迁移期间旧路径和新路径并存时：
- 新路径 `gpio_ctrl_config()` 检查 pin 是否已被 bus_dma GPIO 通道占用
- 迁移脚本确保一个 pin 只存在于 gpio_configs 或 channels 之一
- 迁移完成后在 channel CRUD 中拒绝 gpio 类型

### 8.4 实施顺序

1. **固件**: gpio_ctrl + pwm_ctrl + handler_periph + periph_rsp_task（不删旧路径）
2. **后端**: 模型 + API + sender + ConfigManifest 编码 + config hash + WebSocket 事件
3. **前端**: PeripheralControl.vue + periph.ts，移除 PWM 通道占位
4. **验证**: 端到端测试（见 §9）
5. **迁移**: Go 迁移脚本执行
6. **清理**: 删除旧路径代码

---

## 9. 测试策略

### 9.1 后端单元测试

| 测试文件 | 测试内容 |
|----------|----------|
| `handler_periph_test.go` | GPIO/PWM CRUD、重复 pin 返回 409、不存在 node 返回 404 |
| `sender_test.go` | SendPeriphCmd 编码正确性、QoS1 + request_id 关联验证 |
| `handler_response_test.go` | handlePeriphResponse 解码 + WebSocket 推送 |
| `manager_test.go` | CalcConfigHashForDevice 包含 GPIO/PWM |
| ConfigManifest 编码测试 | field 11/12 编码正确性 |

### 9.2 端到端测试

| 场景 | 操作 | 预期结果 |
|------|------|----------|
| GPIO 输出设置 | POST /gpio/5/set {level:1} | C6 引脚 5 高电平 |
| GPIO 输入读取 | POST /gpio/6/read | 返回当前电平 |
| GPIO TOGGLE | POST /gpio/5/set (toggle) | 电平翻转 |
| PWM 启动 | POST /pwm/15/start | 示波器测量到波形 |
| PWM 占空比 | POST /pwm/15/duty {duty:5000} | 占空比 50% |
| PWM 频率 | POST /pwm/15/freq {frequency:2000} | 频率 2kHz |
| PWM 停止 | POST /pwm/15/stop | 波形停止 |
| 重复 pin | POST /gpio/5（已存在） | 409 Conflict |
| 节点离线 | POST /gpio/5/set（离线） | 超时错误 |
| ConfigManifest 同步 | 创建 GPIOConfig | 节点收到 field 11 |
| 数据迁移 | 执行迁移脚本 | Channel→GPIOConfig 正确迁移 |

### 9.3 固件测试

| 测试 | 说明 |
|------|------|
| gpio_ctrl 单元测试 | SET/READ/TOGGLE/CONFIG/DECONFIG 正确性 |
| pwm_ctrl 单元测试 | START/STOP/SET_DUTY/SET_FREQ 正确性 |
| LEDC 资源耗尽 | 4 个不同频率后第 5 个返回 RESOURCE_EXHAUSTED |
| 引脚冲突检测 | 已占用 pin 返回 PIN_CONFLICT |
| PeriphRsp 异步发送 | MQTT 回调不阻塞、Rsp 正确送达 |
| ConfigManifest field 11/12 | 解析 GPIO/PWM 配置并应用 |

---

## 10. ESP32-C6 LEDC 约束

| 参数 | 值 | 来源 |
|------|-----|------|
| LEDC 通道数 | 6 | `SOC_LEDC_CHANNEL_NUM=6` |
| LEDC 定时器数 | 4 | `SOC_LEDC_TIMER_NUM=4` |
| 最大分辨率 | 20 bit | 1,048,576 级 |
| APB 时钟 | 80 MHz | 固定 |
| 频率约束 | `freq * 2^resolution ≤ 40MHz` | APB_CLK/2 |
| 不可用引脚 | GPIO12/13 (USB), GPIO8 (RGB LED) | 硬件预留 |

> **评审修订**: 确认 ESP32-C6 LEDC 定时器数为 4（`soc_caps.h` 验证），专家评审报告中说 3 是错误的。补充频率-分辨率联合约束公式。

---

## 11. 待确认事项

| # | 问题 | 建议 |
|---|------|------|
| 1 | ADC 是否也需独立？ | 后续单独评估 |
| 2 | GPIO 中断事件推送？ | PeriphRsp field 5/6 已预留，后续可扩展异步事件 |
| 3 | PWM fade 功能？ | v1 不支持，后续可扩展 action 6=FADE |
| 4 | PWM speed_mode？ | C6 只有高速模式，S3 兼容时需增加 |
| 5 | 统一消息 vs 分离消息？ | 当前 2 种类型可接受，超过 4 种时考虑拆分 |
