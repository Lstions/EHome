# GPIO 控制重构设计 — 从通道系统中剥离

> **版本**: v1.0 (2026-07-09)
> **状态**: 设计方案（未实现）
> **分支**: feat/gpio-control
> **关联**: [00-术语表.md](../00-术语表.md) | [通道/详细设计.md](通道/详细设计.md) | [ESP32架构设计.md](ESP32架构设计.md)

---

## 1. 问题陈述

### 1.1 核心观点

**GPIO 不应该使用通道（Channel），只有可以进行数据交互的才支持通道。**

UART/SPI/I2C 是通信总线 — 与外部设备交换数据，有协议、帧结构、地址、多设备共享、时序要求。
GPIO 是引脚直控 — 设置高/低电平，读取高/低电平。没有协议、没有帧、没有外部设备、没有总线。
GPIO 是 MCU 外设控制，不是通信。

### 1.2 当前架构问题

当前设计中 GPIO 被当作一种"通道"，与 UART/SPI/I2C 走完全相同的数据路径：

```
前端按钮 → POST /channels/:id/terminal/write → sender.go WriteCmd(0x06) → MQTT
→ C6 bus_manager → gpio_cmd_queue → bus_worker(spi_i2c_cmd_loop)
→ bus_dma_transact → gpio_write → gpio_set_level
```

**6 跳，只为了翻转一个引脚电平。**

具体问题：

| 问题 | 说明 |
|------|------|
| 控制路径过长 | `gpio_set_level()` 一个函数调用，经过 channel API → MQTT → bus_manager → 队列 → worker → transact 6 跳 |
| 语义错位 | gpio_write 把 1 字节当"总线事务"处理，实际只取 `data[0] & 0x01` — 8 位数据只用 1 位 |
| 语义错位 | 前端用 hex "01"/"00" 通过 terminal write API 控制 GPIO — 在"终端"里发 hex 来点灯 |
| 语义错位 | ChannelTerminal 为 GPIO 显示 TX/RX 双面板、hex 输入框、read_size — 全部无意义 |
| 资源浪费 | GPIO 占据 channel 表行、ConfigManifest 条目、gpio_cmd_queue 队列、bus_worker 任务、scheduler 槽位、driver registry 条目 |
| 概念污染 | Channel 模型注释写"一个 Channel = 一个物理总线实例" — GPIO 不是总线 |
| 概念污染 | scheduler 对 GPIO 做 interval 轮询调度 — GPIO 不是轮询指令 |
| 概念污染 | driver registry 中 gpio.digital 与真实协议驱动并列 — GPIO 不是协议设备 |

---

## 2. 设计目标

1. **GPIO 从通道系统中完全剥离** — 不再复用 Channel 模型、通道 API、bus_dma/bus_manager/scheduler/bus_worker
2. **建立独立的控制路径** — 独立数据模型、API、MQTT 消息类型、UI 组件、固件模块
3. **保持硬件资源上报** — GPIO 引脚资源仍在 ResourceReport 中上报，但不创建通道
4. **保持向后兼容** — 迁移期间旧 GPIO 通道可逐步清理，不影响 UART/SPI/I2C 通道

---

## 3. 重新设计

### 3.1 概念划分

```
通道 (Channel) — 数据交互总线
  ├── UART: 有协议帧、有外部设备、有收发时序
  ├── I2C:  有地址、有协议、多设备共享
  └── SPI:  有 CS 寻址、有协议、有时钟

GPIO 控制 (GPIO Control) — 引脚直控
  ├── OUTPUT: 设置高/低电平（控制继电器、LED 等）
  └── INPUT: 读取高/低电平（检测开关、传感器状态）

ADC 采样 (ADC Sampling) — 模拟量采集 [后续单独评估]
```

### 3.2 后端设计

#### 3.2.1 数据模型

新增 `GPIOConfig` 模型（不复用 Channel）：

```go
// GPIOConfig — GPIO 引脚配置（独立于 Channel）
type GPIOConfig struct {
    ID           uint      `gorm:"primaryKey" json:"id"`
    NodeID       string    `gorm:"column:node_id;type:varchar(32);index;not null" json:"node_id"`
    Pin          int       `gorm:"not null" json:"pin"`              // GPIO 引脚号
    Direction    uint8     `gorm:"not null" json:"direction"`         // 0=INPUT, 1=OUTPUT, 2=INPUT_PULLUP, 3=INPUT_PULLDOWN
    InitialLevel uint8     `gorm:"default:0" json:"initial_level"`    // OUTPUT 时的初始电平
    Label        string    `gorm:"size:64" json:"label"`              // 用户自定义标签（如"继电器1"）
    Enabled      bool      `gorm:"default:true" json:"enabled"`
    CreatedAt    time.Time `json:"created_at"`
    UpdatedAt    time.Time `json:"updated_at"`
    DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}
```

**与 Channel 的区别**：
- 无 `bus_type` / `bus_config` / `hardware_id` / `template_ids` / `interval_ms` / `dma_enabled`
- 无关联 `EdgeDevice`（GPIO 上不挂设备）
- 无关联 `DeviceConfig` / `ConfigTemplate`（GPIO 无协议）

#### 3.2.2 API 端点

独立路由组 `/nodes/:node_id/gpio`，不复用 `/channels`：

| 方法 | 路径 | 功能 |
|------|------|------|
| GET | `/nodes/:node_id/gpio` | 列出节点所有 GPIO 配置 |
| POST | `/nodes/:node_id/gpio` | 配置 GPIO（pin + direction + label） |
| PUT | `/nodes/:node_id/gpio/:pin` | 更新 GPIO 配置 |
| DELETE | `/nodes/:node_id/gpio/:pin` | 取消 GPIO 配置（reset_pin） |
| POST | `/nodes/:node_id/gpio/:pin/set` | 设置输出电平 `{level: 0\|1}` |
| POST | `/nodes/:node_id/gpio/:pin/read` | 读取引脚电平 → `{level: 0\|1}` |

#### 3.2.3 MQTT 消息类型

新增两个协议消息类型（当前已用到 0x1A，0x15-0x18 和 0x1B+ 可用）：

```
MsgGPIOCmd = 0x1B  (中心端 → 节点)
  field 1: request_id (varint, uint32) — 用于匹配响应
  field 2: pin        (varint, uint8)  — GPIO 引脚号
  field 3: action     (varint, uint8)  — 0=set_low, 1=set_high, 2=read
  field 4: direction  (varint, uint8)  — 配置用: 0=INPUT, 1=OUTPUT, 2=INPUT_PULLUP, 3=INPUT_PULLDOWN
  field 5: initial_level (varint, uint8) — OUTPUT 时的初始电平

MsgGPIORsp = 0x1C  (节点 → 中心端)
  field 1: request_id (varint, uint32) — 匹配 GPIOCmd
  field 2: success    (varint, bool)   — 操作是否成功
  field 3: level      (varint, uint8)  — read 操作返回值 (0/1)
```

**action 枚举**：

| 值 | 名称 | 说明 | 响应 |
|----|------|------|------|
| 0 | SET_LOW | 设置输出低电平 | success |
| 1 | SET_HIGH | 设置输出高电平 | success |
| 2 | READ | 读取引脚电平 | success + level |
| 3 | CONFIG | 配置引脚方向 (需 direction 字段) | success |
| 4 | DECONFIG | 取消配置 (gpio_reset_pin) | success |

#### 3.2.4 ConfigManifest 扩展

GPIO 配置不再混在 channels 数组中。ConfigManifest 新增 field 11: `gpio_configs`（repeated sub-structure）：

```
field 11 (repeated): gpio_config
  sub-field 1: pin           (varint, uint8)
  sub-field 2: direction     (varint, uint8)  — 0=INPUT, 1=OUTPUT, 2=INPUT_PULLUP, 3=INPUT_PULLDOWN
  sub-field 3: initial_level (varint, uint8)  — OUTPUT 时的初始电平
```

固件收到 ConfigManifest 后，GPIO 部分走独立的 `gpio_ctrl_init()`，不经过 `bus_dma_init()`。

#### 3.2.5 驱动层

- 删除 `drivers/gpio/` 目录 — GPIO 不是协议设备，不需要 driver
- 删除 `drivers/gpio_adapter.go`
- 删除 `drivers/gpio_driver_test.go`
- 修改 `drivers/builtin.go` — 移除 GPIO driver 注册

#### 3.2.6 后端影响文件清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `models/models.go` | 新增 GPIOConfig 模型 | Channel 模型不变，但不再接受 gpio 类型 |
| `api/handler_gpio.go` | 新增 | GPIO 独立 API 处理器 |
| `api/routes.go` | 修改 | 新增 GPIO 路由组 |
| `api/handler_device.go` | 修改 | channel CRUD 排除 gpio 类型（可选：加校验拒绝） |
| `api/handler_terminal.go` | 修改 | terminal write 排除 gpio 通道（可选：加校验拒绝） |
| `api/handler_node.go` | 保留 | GPIO 硬件资源上报保留（ResourceReport） |
| `nodemgr/sender.go` | 修改 | 新增 `SendGPIOCmd()`，ConfigManifest 中 GPIO 独立字段 |
| `frame/frame.go` | 修改 | 新增 `MsgGPIOCmd=0x1B`, `MsgGPIORsp=0x1C` |
| `drivers/gpio/` | 删除 | 整个目录 |
| `drivers/gpio_adapter.go` | 删除 | |
| `drivers/gpio_driver_test.go` | 删除 | |
| `drivers/builtin.go` | 修改 | 移除 GPIO driver 注册 |

### 3.3 前端设计

#### 3.3.1 新增 GPIOControl.vue 组件

不复用 ChannelTerminal，独立组件：

```
┌─────────────────────────────────────────────────────┐
│  GPIO 控制                              [刷新] [配置] │
├─────────────────────────────────────────────────────┤
│                                                      │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐          │
│  │ GPIO 5   │  │ GPIO 6   │  │ GPIO 7   │          │
│  │ OUTPUT   │  │ INPUT    │  │ OUTPUT   │          │
│  │ 继电器1  │  │ 门磁传感器│  │ LED灯    │          │
│  │ [ON][OFF]│  │ [READ]   │  │ [ON][OFF]│          │
│  │  ● HIGH  │  │  ○ LOW   │  │  ● HIGH  │          │
│  └──────────┘  └──────────┘  └──────────┘          │
│                                                      │
└─────────────────────────────────────────────────────┘
```

- 以引脚卡片网格展示节点所有 GPIO
- 每个卡片显示：引脚号、方向标签、用户标签、当前电平指示灯
- OUTPUT 方向：显示 ON/OFF 按钮 + 电平指示灯
- INPUT 方向：显示 READ 按钮 + 电平指示灯
- 配置界面：选择方向、设置标签、设置初始电平
- **不需要**：TX/RX 日志面板、hex 输入框、read_size、命令历史、displayMode (HEX/ASCII)

#### 3.3.2 前端影响文件清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `components/gpio/GPIOControl.vue` | 新增 | GPIO 独立控制组件 |
| `api/gpio.ts` | 新增 | GPIO API 模块 |
| `components/channel/ChannelTerminal.vue` | 修改 | 移除所有 GPIO 相关代码（约 60 行） |
| `components/node/ChannelPanel.vue` | 修改 | 从 busType 列表移除 'gpio' |

#### 3.3.3 ChannelTerminal.vue 移除清单

| 行号 | 内容 | 操作 |
|------|------|------|
| L59-74 | GPIO 控制按钮组 HTML | 删除 |
| L233-241 | `gpioDirection` computed | 删除 |
| L244 | `isGpioOutput` computed | 删除 |
| L295 | channelGroups 中 `gpio: 'GPIO'` | 删除 |
| L310 | getTagType 中 `gpio: 'info'` | 删除 |
| L456-504 | `sendGpioCommand()` + `gpioSetOn/SetOff/Read` | 删除 |

#### 3.3.4 ChannelPanel.vue 移除清单

| 行号 | 内容 | 操作 |
|------|------|------|
| L36 | busType 列表中 `'gpio'` | 移除 |
| L313 | `gpio: []` 硬件分组 | 移除 |
| L326 | `gpio: 'info'` 标签颜色 | 移除 |
| L347 | 清空函数中 `'gpio'` | 移除 |
| L434 | 同步函数中 `'gpio'` | 移除 |
| L585 | `nameToHardwareId` 中 GPIO 分支 | 移除 |
| L593 | `getBusTypeName` 中 `gpio` | 移除 |
| L614 | PIN_ROLE_LABELS 中 `9: 'GPIO'` | 保留（引脚角色标签仍需要） |
| L633 | GPIO 配色主题 | 保留（引脚标签仍需要） |

### 3.4 固件设计

#### 3.4.1 从 bus 系统中移除 GPIO

| 文件 | 行号 | 内容 | 操作 |
|------|------|------|------|
| `bus_dma.h` | L34 | `#define BUS_TYPE_GPIO 4` | 删除 |
| `bus_dma.h` | L59 | `BUS_TYPE_GPIO: return false` | 删除 |
| `bus_dma.h` | L101-104 | `bus_dma_ctx_t.gpio` 结构 | 删除 |
| `bus_dma.c` | L1036-1093 | `gpio_init()` | 删除 |
| `bus_dma.c` | L1095-1108 | `gpio_write()` | 删除 |
| `bus_dma.c` | L1110-1115 | `gpio_read()` | 删除 |
| `bus_dma.c` | L1117-1121 | `gpio_deinit()` | 删除 |
| `bus_dma.c` | L1144 | `bus_dma_init` 中 `BUS_TYPE_GPIO` case | 删除 |
| `bus_dma.c` | L1211-1220 | `bus_dma_transact` 中 `BUS_TYPE_GPIO` case | 删除 |
| `bus_dma.c` | L1238 | `bus_dma_deinit` 中 `BUS_TYPE_GPIO` case | 删除 |
| `bus_manager.c` | L89-96 | `derive_hw_id` 中 GPIO 分支 | 删除 |
| `bus_manager.c` | L330 | `bus_manager_on_write_cmd` 中 GPIO 队列路由 | 删除 |
| `bus_worker.c` | L336-338 | `cmd_task_gpio` | 删除 |
| `bus_worker.h` | L63 | `gpio_cmd_queue` 字段 | 删除 |
| `scheduler.c` | L47 | `BUS_TYPE_GPIO` 队列映射 | 删除 |
| `scheduler.c` | L500-501 | `gpio_cmd_queue` 背压检查 | 删除 |
| `scheduler.h` | L81 | `gpio_cmd_queue` 字段 | 删除 |

#### 3.4.2 新增 GPIO 控制模块

```
components/gpio_ctrl/
├── include/
│   └── gpio_ctrl.h
└── gpio_ctrl.c
```

**gpio_ctrl.h**:

```c
#pragma once
#include <stdint.h>
#include <stdbool.h>
#include "esp_err.h"

// GPIO 方向枚举
#define GPIO_DIR_INPUT        0
#define GPIO_DIR_OUTPUT       1
#define GPIO_DIR_INPUT_PULLUP 2
#define GPIO_DIR_INPUT_PULLDN 3

// GPIO action 枚举（与协议一致）
#define GPIO_ACTION_SET_LOW   0
#define GPIO_ACTION_SET_HIGH  1
#define GPIO_ACTION_READ      2
#define GPIO_ACTION_CONFIG    3
#define GPIO_ACTION_DECONFIG  4

typedef struct {
    uint8_t pin;
    uint8_t direction;
    uint8_t initial_level;
} gpio_config_entry_t;

// 初始化：解析 ConfigManifest 中的 gpio_configs，配置引脚
esp_err_t gpio_ctrl_init(const gpio_config_entry_t *configs, int count);

// 设置引脚电平
esp_err_t gpio_ctrl_set(int pin, int level);

// 读取引脚电平
int gpio_ctrl_read(int pin);

// 配置单个引脚
esp_err_t gpio_ctrl_config(int pin, int direction, int initial_level);

// 取消配置（reset_pin）
esp_err_t gpio_ctrl_deconfig(int pin);

// 处理 GPIOCmd 消息（在 MQTT 接收回调中直接调用）
// 返回: success + level（通过指针）
esp_err_t gpio_ctrl_handle_cmd(uint8_t pin, uint8_t action,
                                uint8_t direction, uint8_t initial_level,
                                bool *success, uint8_t *level);
```

**设计要点**：

- **不需要队列** — GPIO 操作是即时的，在 MQTT 接收回调中直接执行
- **不需要 worker task** — `gpio_set_level()` / `gpio_get_level()` 是非阻塞的
- **不需要 DMA** — GPIO 无 DMA
- **不需要 mutex** — `gpio_set_level()` 是线程安全的原子操作
- **不需要 scheduler** — GPIO 不是轮询指令，不需要 interval 调度

#### 3.4.3 消息处理集成

`msg_handler.c` 中新增 GPIOCmd 分发：

```c
// msg_handler.c — dispatch 新增
case MSG_GPIO_CMD:
    handler_gpio_process(&dec);
    break;
```

新增 `handler_gpio.c`:

```c
// handler_gpio.c
void handler_gpio_process(frame_decoder_t *dec)
{
    uint32_t request_id = 0;
    uint8_t pin = 0, action = 0, direction = 0, initial_level = 0;
    // ... 解码 fields ...

    bool success = false;
    uint8_t level = 0;
    esp_err_t r = gpio_ctrl_handle_cmd(pin, action, direction,
                                        initial_level, &success, &level);
    // 发送 GPIORsp
    msg_handler_send_gpio_rsp(request_id, success, level);
}
```

#### 3.4.4 ConfigManifest 解析修改

`handler_config.c` 中，ConfigManifest 解析新增 field 11 (gpio_configs) 处理：

```c
// 在 handler_config_process_manifest 中
case 11: // gpio_configs (repeated sub-structure)
    // 解析 sub-fields: pin, direction, initial_level
    // 存入 gpio_config_entry_t 数组
    break;
```

应用配置时：
- channels 数组 → `bus_manager` / `config_mgr`（现有逻辑不变）
- gpio_configs 数组 → `gpio_ctrl_init()`（新路径）

#### 3.4.5 硬件资源上报

`hw_tables.c` 中 `hw_gpios[]` **保留** — 仍用于 ResourceReport 上报可用引脚资源。
但 `bus_manager.c` 中用 `hw_gpios[]` 做通道路由的代码删除。

#### 3.4.6 固件影响文件清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `components/bus_dma/bus_dma.h` | 修改 | 移除 BUS_TYPE_GPIO 及相关结构 |
| `components/bus_dma/bus_dma.c` | 修改 | 移除 gpio_init/write/read/deinit 及 case 分支 |
| `components/bus_manager/bus_manager.c` | 修改 | 移除 GPIO 路由 |
| `components/bus_worker/bus_worker.c` | 修改 | 移除 cmd_task_gpio |
| `components/bus_worker/include/bus_worker.h` | 修改 | 移除 gpio_cmd_queue |
| `components/scheduler/scheduler.c` | 修改 | 移除 GPIO 队列映射和背压 |
| `components/scheduler/scheduler.h` | 修改 | 移除 gpio_cmd_queue |
| `components/gpio_ctrl/` | 新增 | 独立 GPIO 控制模块 |
| `components/msg_handler/msg_handler.c` | 修改 | 新增 MSG_GPIO_CMD 分发 |
| `components/msg_handler/handler_gpio.c` | 新增 | GPIOCmd 处理器 |
| `components/frame/frame_codec.h` | 修改 | 新增 MSG_GPIO_CMD/RSP 定义 |
| `components/hw_profile/hw_tables.c` | 保留 | hw_gpios[] 仍用于资源上报 |
| `components/hw_profile/hw_profile.c` | 保留 | ResourceReport 仍上报 GPIO 引脚 |
| `main/main.c` | 修改 | 移除 on_write_cmd_received 中 GPIO 路由，新增 GPIO callback |

---

## 4. 数据流对比

### 4.1 当前（GPIO 走通道）

```
前端按钮 ON
  → POST /channels/:id/terminal/write {data_hex: "01"}
  → handler_terminal.go → nodemgr.SendWriteCommand()
  → frame.MsgWriteCmd(0x06) → MQTT publish
  → C6 msg_handler → handler_writecmd_process
  → on_write_cmd_received → bus_manager_on_write_cmd
  → gpio_cmd_queue → bus_worker spi_i2c_cmd_loop
  → bus_dma_transact(BUS_TYPE_GPIO)
  → gpio_write → gpio_set_level(pin, 1)
  → WriteRsp(0x07) → MQTT → 前端
```

### 4.2 重新设计后（GPIO 独立路径）

```
前端按钮 ON
  → POST /nodes/:id/gpio/5/set {level: 1}
  → handler_gpio.go → nodemgr.SendGPIOCmd()
  → frame.MsgGPIOCmd(0x1B) → MQTT publish
  → C6 msg_handler → handler_gpio_process
  → gpio_ctrl_handle_cmd → gpio_set_level(pin, 1)
  → GPIORsp(0x1C) → MQTT → 前端
```

**路径从 6 跳减到 3 跳，每跳语义正确。**

---

## 5. 迁移策略

### 5.1 数据迁移

已创建的 GPIO 类型 Channel 需迁移到 GPIOConfig 表：

```sql
-- 1. 从 channels 表提取 GPIO 配置，插入 gpio_configs 表
INSERT INTO gpio_configs (node_id, pin, direction, initial_level, label, enabled, created_at, updated_at)
SELECT
    node_id,
    CAST(SUBSTRING(hardware_id FROM 'GPIO([0-9]+)') AS INTEGER) AS pin,
    CASE
        WHEN config::jsonb->>'direction' = 'OUTPUT' THEN 1
        WHEN config::jsonb->>'direction' = 'INPUT_PULLUP' THEN 2
        WHEN config::jsonb->>'direction' = 'INPUT_PULLDOWN' THEN 3
        ELSE 0
    END AS direction,
    0 AS initial_level,
    COALESCE(config::jsonb->>'label', '') AS label,
    enabled,
    created_at,
    updated_at
FROM channels
WHERE hardware_type = 'gpio';

-- 2. 删除 GPIO 类型 Channel
DELETE FROM channels WHERE hardware_type = 'gpio';
```

### 5.2 实施顺序

1. **固件**: 新增 `gpio_ctrl` 模块 + `handler_gpio.c` + MSG_GPIO_CMD/RSP（不删除旧路径）
2. **后端**: 新增 `GPIOConfig` 模型 + `handler_gpio.go` + `sender.SendGPIOCmd()` + ConfigManifest field 11
3. **前端**: 新增 `GPIOControl.vue` + `api/gpio.ts`
4. **验证**: 新路径端到端测试通过
5. **迁移**: 数据迁移脚本执行
6. **清理**: 删除旧路径代码（bus_dma GPIO 分支、driver、ChannelTerminal GPIO 代码等）

### 5.3 向后兼容

迁移期间：
- 旧 GPIO Channel 仍可工作（旧路径不删）
- 新 GPIOConfig API 同时可用（新路径）
- 迁移完成后删除旧路径，在 channel CRUD 中拒绝 gpio 类型

---

## 6. 待确认事项

| # | 问题 | 建议 |
|---|------|------|
| 1 | ADC 是否也有同样问题？ | ADC 有采样间隔概念，比 GPIO 更接近"数据交互"。当前先处理 GPIO，ADC 后续单独评估 |
| 2 | GPIO 事件推送（电平变化通知）？ | 当前设计为主动读取。如需电平变化中断通知，后续可新增 MsgGPIOEvent(0x1D) 消息 |
| 3 | GPIO 配置是否在 ConfigManifest 中同步？ | 建议放在 ConfigManifest field 11，保持节点配置统一同步语义，但用独立字段 |
| 4 | PWM 是否也需要独立？ | PWM 与 GPIO 类似是控制不是通信，但更复杂（频率/占空比）。后续单独评估 |
