# 外设控制重构设计 — GPIO 与 PWM 从通道系统中剥离

> **版本**: v2.0 (2026-07-09)
> **状态**: 设计方案（未实现）
> **分支**: feat/gpio-control
> **关联**: [00-术语表.md](../00-术语表.md) | [通道/详细设计.md](通道/详细设计.md) | [ESP32架构设计.md](ESP32架构设计.md)

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

GPIO 被当作一种"通道"，与 UART/SPI/I2C 走完全相同的数据路径：

```
前端按钮 → POST /channels/:id/terminal/write {data_hex:"01"}
→ sender.go WriteCmd(0x06) → MQTT
→ C6 bus_manager → gpio_cmd_queue → bus_worker(spi_i2c_cmd_loop)
→ bus_dma_transact → gpio_write → gpio_set_level(pin, 1)
```

**6 跳，只为了翻转一个引脚电平。**

具体问题：

| 问题 | 说明 |
|------|------|
| 控制路径过长 | `gpio_set_level()` 一个函数调用，经过 6 跳 |
| 语义错位 | gpio_write 把 1 字节当"总线事务"，实际只取 `data[0] & 0x01` — 8 位只用 1 位 |
| 语义错位 | 前端用 hex "01"/"00" 通过 terminal write API 控制 GPIO — 在"终端"里发 hex 来点灯 |
| 语义错位 | ChannelTerminal 为 GPIO 显示 TX/RX 双面板、hex 输入框、read_size — 全部无意义 |
| 资源浪费 | GPIO 占据 channel 表、ConfigManifest 条目、gpio_cmd_queue、bus_worker 任务、scheduler 槽位、driver registry |
| 概念污染 | Channel 注释写"物理总线实例" — GPIO 不是总线；scheduler 对 GPIO 做 interval 轮询 — GPIO 不是轮询指令 |

### 1.3 PWM 当前状态

**PWM 目前仅有前端 UI 占位，后端和固件零实现。**

| 层 | 现状 | 文件/行号 |
|----|------|-----------|
| 前端 | `hardware_type: 'pwm'` 类型声明 | `api/channel.ts:8` |
| 前端 | 通道筛选器 PWM 选项 | `ChannelList.vue:59` |
| 前端 | 通道类型标签映射 `pwm: 'PWM'` | `ChannelTerminal.vue:295`, `ChannelList.vue:301` |
| 前端 | 标签颜色 `pwm: 'info'` / `pwm: 'primary'` | `ChannelPanel.vue:331`, `ChannelTerminal.vue:310` |
| 前端 | 设备类型 `gpio.pwm` | `deviceType.ts:33` |
| 前端 | DeviceConfig 硬件类型选项 | `DeviceConfigList.vue:72` |
| 后端 | **无** | 无 PWM 模型、无 API、无 driver、无消息类型 |
| 固件 | **无** | 无 BUS_TYPE_PWM、无 hw_pwm、无 LEDC/MCPWM 集成 |
| 固件 | 板载 RGB LED (WS2812) | `components/rgb_led/` — 用 RMT 驱动，与 PWM 控制无关 |

PWM 的优势：**从零开始设计，无旧代码迁移负担**。可以直接按正确架构实现，避免重蹈 GPIO 覆辙。

---

## 2. 设计目标

1. **GPIO 和 PWM 从通道系统中完全剥离** — 不复用 Channel 模型、通道 API、bus_dma/bus_manager/scheduler/bus_worker
2. **建立统一的外设控制路径** — GPIO 和 PWM 共享相似的架构模式（独立模型、API、消息类型、UI 组件、固件模块）
3. **保持硬件资源上报** — GPIO/PWM 引脚资源仍在 ResourceReport 中上报，但不创建通道
4. **GPIO 向后兼容** — 迁移期间旧 GPIO 通道可逐步清理
5. **PWM 全新实现** — 基于正确的架构从零构建

---

## 3. 统一架构设计

### 3.1 概念划分

```
通道 (Channel) — 数据交互总线
  ├── UART: 有协议帧、有外部设备、有收发时序
  ├── I2C:  有地址、有协议、多设备共享
  └── SPI:  有 CS 寻址、有协议、有时钟

外设控制 (Peripheral Control) — MCU 外设直控
  ├── GPIO: 数字引脚控制 (OUTPUT: 高/低电平 | INPUT: 读高/低电平)
  └── PWM:  波形输出 (LEDC: 频率 + 占空比)

ADC 采样 — 模拟量采集 [后续单独评估]
```

### 3.2 统一协议消息

GPIO 和 PWM 共享消息类型族，通过 action 字段区分具体操作。

```
MsgPeriphCmd = 0x1B  (中心端 → 节点)
  field 1: request_id  (varint, uint32) — 用于匹配响应
  field 2: periph_type (varint, uint8)  — 1=GPIO, 2=PWM
  field 3: pin         (varint, uint8)  — 引脚号
  field 4: action      (varint, uint8)  — 操作类型（见下表）
  field 5: value       (varint, uint32) — 操作参数（如 level/duty/freq）
  field 6: config      (bytes)          — 配置参数（如 direction/mode，二进制编码）

MsgPeriphRsp = 0x1C  (节点 → 中心端)
  field 1: request_id (varint, uint32) — 匹配 PeriphCmd
  field 2: success    (varint, bool)   — 操作是否成功
  field 3: value      (varint, uint32) — 返回值（如 read 的 level）
  field 4: error_code (varint, uint8)  — 错误码（0=OK）
```

#### GPIO action 枚举

| 值 | 名称 | value 含义 | config 含义 | 响应 |
|----|------|-----------|------------|------|
| 0 | SET_LOW | — | — | success |
| 1 | SET_HIGH | — | — | success |
| 2 | READ | — | — | success + value(0/1) |
| 3 | CONFIG | — | [direction:1B, initial_level:1B] | success |
| 4 | DECONFIG | — | — | success |

GPIO direction 编码：0=INPUT, 1=OUTPUT, 2=INPUT_PULLUP, 3=INPUT_PULLDOWN

#### PWM action 枚举

| 值 | 名称 | value 含义 | config 含义 | 响应 |
|----|------|-----------|------------|------|
| 0 | SET_DUTY | duty (0-10000 = 0.00%-100.00%, 精度 0.01%) | — | success |
| 1 | SET_FREQ | frequency (Hz) | — | success |
| 2 | START | — | [freq:4B, duty:2B, resolution:1B] | success |
| 3 | STOP | — | — | success |
| 4 | READ | — | — | success + value(当前 duty) |

PWM config 编码（START action 的 config bytes）：
- byte 0-3: frequency (uint32, little-endian, Hz)
- byte 4-5: duty (uint16, little-endian, 0-10000 = 0.00%-100.00%)
- byte 6: resolution_bits (4-20, 默认 13)

### 3.3 ConfigManifest 扩展

GPIO 和 PWM 配置不混在 channels 数组中，各自独立字段：

```
field 11 (repeated): gpio_configs
  sub-field 1: pin           (varint, uint8)
  sub-field 2: direction     (varint, uint8)  — 0=INPUT, 1=OUTPUT, 2=INPUT_PULLUP, 3=INPUT_PULLDOWN
  sub-field 3: initial_level (varint, uint8)  — OUTPUT 时的初始电平

field 12 (repeated): pwm_configs
  sub-field 1: pin           (varint, uint8)   — GPIO 引脚号
  sub-field 2: frequency     (varint, uint32)  — 频率 (Hz)
  sub-field 3: duty          (varint, uint16)  — 占空比 (0-10000 = 0.00%-100.00%)
  sub-field 4: resolution    (varint, uint8)   — 分辨率位数 (默认 13)
```

固件收到 ConfigManifest 后：
- channels 数组 → `bus_manager` / `config_mgr`（现有逻辑不变）
- gpio_configs 数组 → `gpio_ctrl_init()`（新路径）
- pwm_configs 数组 → `pwm_ctrl_init()`（新路径）

---

## 4. 后端设计

### 4.1 数据模型

#### GPIOConfig

```go
// GPIOConfig — GPIO 引脚配置（独立于 Channel）
type GPIOConfig struct {
    ID           uint           `gorm:"primaryKey" json:"id"`
    NodeID       string         `gorm:"column:node_id;type:varchar(32);index;not null" json:"node_id"`
    Pin          int            `gorm:"not null" json:"pin"`
    Direction    uint8          `gorm:"not null" json:"direction"`      // 0=INPUT, 1=OUTPUT, 2=INPUT_PULLUP, 3=INPUT_PULLDOWN
    InitialLevel uint8          `gorm:"default:0" json:"initial_level"` // OUTPUT 时的初始电平
    Label        string         `gorm:"size:64" json:"label"`           // 用户自定义标签
    Enabled      bool           `gorm:"default:true" json:"enabled"`
    CreatedAt    time.Time      `json:"created_at"`
    UpdatedAt    time.Time      `json:"updated_at"`
    DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}
```

#### PWMConfig

```go
// PWMConfig — PWM 输出配置（独立于 Channel）
type PWMConfig struct {
    ID           uint           `gorm:"primaryKey" json:"id"`
    NodeID       string         `gorm:"column:node_id;type:varchar(32);index;not null" json:"node_id"`
    Pin          int            `gorm:"not null" json:"pin"`              // GPIO 引脚号
    Frequency    uint32         `gorm:"not null" json:"frequency"`        // 频率 (Hz)
    Duty         uint16         `gorm:"default:0" json:"duty"`            // 占空比 (0-10000 = 0.00%-100.00%)
    Resolution   uint8          `gorm:"default:13" json:"resolution"`     // 分辨率位数 (4-20)
    Label        string         `gorm:"size:64" json:"label"`             // 用户自定义标签
    Enabled      bool           `gorm:"default:true" json:"enabled"`
    Running      bool           `gorm:"default:false" json:"running"`     // 是否正在输出
    CreatedAt    time.Time      `json:"created_at"`
    UpdatedAt    time.Time      `json:"updated_at"`
    DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}
```

**与 Channel 的区别**：
- 无 `bus_type` / `bus_config` / `hardware_id` / `template_ids` / `interval_ms` / `dma_enabled`
- 无关联 `EdgeDevice`（GPIO/PWM 上不挂设备）
- 无关联 `DeviceConfig` / `ConfigTemplate`（无协议）

### 4.2 API 端点

#### GPIO API

| 方法 | 路径 | 功能 |
|------|------|------|
| GET | `/nodes/:node_id/gpio` | 列出节点所有 GPIO 配置 |
| POST | `/nodes/:node_id/gpio` | 配置 GPIO（pin + direction + label） |
| PUT | `/nodes/:node_id/gpio/:pin` | 更新 GPIO 配置 |
| DELETE | `/nodes/:node_id/gpio/:pin` | 取消 GPIO 配置（reset_pin） |
| POST | `/nodes/:node_id/gpio/:pin/set` | 设置输出电平 `{level: 0\|1}` |
| POST | `/nodes/:node_id/gpio/:pin/read` | 读取引脚电平 → `{level: 0\|1}` |

#### PWM API

| 方法 | 路径 | 功能 |
|------|------|------|
| GET | `/nodes/:node_id/pwm` | 列出节点所有 PWM 配置 |
| POST | `/nodes/:node_id/pwm` | 配置 PWM（pin + frequency + duty + label） |
| PUT | `/nodes/:node_id/pwm/:pin` | 更新 PWM 配置 |
| DELETE | `/nodes/:node_id/pwm/:pin` | 取消 PWM 配置（停止输出 + release timer） |
| POST | `/nodes/:node_id/pwm/:pin/start` | 启动 PWM 输出 |
| POST | `/nodes/:node_id/pwm/:pin/stop` | 停止 PWM 输出 |
| POST | `/nodes/:node_id/pwm/:pin/duty` | 设置占空比 `{duty: 0-10000}` |
| POST | `/nodes/:node_id/pwm/:pin/freq` | 设置频率 `{frequency: Hz}` |
| GET | `/nodes/:node_id/pwm/:pin/state` | 读取当前状态 → `{running, duty, frequency}` |

### 4.3 后端影响文件清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `models/models.go` | 新增 GPIOConfig + PWMConfig 模型 | Channel 模型不变，不再接受 gpio/pwm 类型 |
| `api/handler_periph.go` | 新增 | GPIO + PWM 独立 API 处理器 |
| `api/routes.go` | 修改 | 新增 `/nodes/:node_id/gpio` 和 `/nodes/:node_id/pwm` 路由组 |
| `api/handler_device.go` | 修改 | channel CRUD 排除 gpio/pwm 类型 |
| `api/handler_terminal.go` | 修改 | terminal write 排除 gpio/pwm 通道 |
| `api/handler_node.go` | 保留 | GPIO/PWM 硬件资源上报保留（ResourceReport） |
| `nodemgr/sender.go` | 修改 | 新增 `SendPeriphCmd()`，ConfigManifest 中 GPIO/PWM 独立字段 |
| `frame/frame.go` | 修改 | 新增 `MsgPeriphCmd=0x1B`, `MsgPeriphRsp=0x1C` |
| `drivers/gpio/` | 删除 | 整个目录 — GPIO 不是协议设备 |
| `drivers/gpio_adapter.go` | 删除 | |
| `drivers/gpio_driver_test.go` | 删除 | |
| `drivers/builtin.go` | 修改 | 移除 GPIO driver 注册 |

---

## 5. 前端设计

### 5.1 新增 PeripheralControl.vue 组件

GPIO + PWM 统一在"外设控制"面板中展示，以卡片网格排列：

```
┌─────────────────────────────────────────────────────────────┐
│  外设控制                                    [刷新] [配置]   │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  ── GPIO ──                                                  │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐                  │
│  │ GPIO 5   │  │ GPIO 6   │  │ GPIO 7   │                  │
│  │ OUTPUT   │  │ INPUT    │  │ OUTPUT   │                  │
│  │ 继电器1  │  │ 门磁传感器│  │ LED灯    │                  │
│  │[ON][OFF] │  │ [READ]   │  │[ON][OFF] │                  │
│  │  ● HIGH  │  │  ○ LOW   │  │  ● HIGH  │                  │
│  └──────────┘  └──────────┘  └──────────┘                  │
│                                                              │
│  ── PWM ──                                                   │
│  ┌────────────────┐  ┌────────────────┐                    │
│  │ PWM (GPIO 15)  │  │ PWM (GPIO 16)  │                    │
│  │ 风扇调速       │  │ LED 调光       │                    │
│  │ 1000 Hz        │  │ 5000 Hz        │                    │
│  │ Duty: ████░░ 65%│  │ Duty: ██░░░░ 30%│                    │
│  │ [启动][停止]   │  │ [启动][停止]   │                    │
│  │ ──●━━━━━━ 65%  │  │ ──●━━━━━━ 30%  │                    │
│  │ Freq: 1000 Hz  │  │ Freq: 5000 Hz  │                    │
│  └────────────────┘  └────────────────┘                    │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

**GPIO 卡片**：
- 引脚号、方向标签、用户标签
- OUTPUT：ON/OFF 按钮 + 电平指示灯（●/○）
- INPUT：READ 按钮 + 电平指示灯

**PWM 卡片**：
- 引脚号、用户标签
- 频率显示 + 频率输入框
- 占空比滑块（0-100%，精度 0.01%）
- 启动/停止按钮
- 运行状态指示

**不需要**：TX/RX 日志面板、hex 输入框、read_size、命令历史、displayMode (HEX/ASCII)

### 5.2 前端影响文件清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `components/periph/PeripheralControl.vue` | 新增 | GPIO + PWM 统一控制组件 |
| `components/periph/GPIOPinCard.vue` | 新增 | GPIO 引脚卡片子组件 |
| `components/periph/PWMChannelCard.vue` | 新增 | PWM 通道卡片子组件 |
| `api/periph.ts` | 新增 | GPIO + PWM API 模块 |
| `components/channel/ChannelTerminal.vue` | 修改 | 移除所有 GPIO 相关代码（约 60 行） |
| `components/node/ChannelPanel.vue` | 修改 | 从 busType 列表移除 'gpio' 和 'pwm' |
| `api/channel.ts` | 修改 | hardware_type 联合类型移除 'gpio' 和 'pwm' |
| `utils/deviceType.ts` | 修改 | 移除 'gpio.digital' 和 'gpio.pwm' 设备类型 |
| `views/channel/ChannelList.vue` | 修改 | 筛选器移除 GPIO 和 PWM 选项 |
| `views/config/DeviceConfigList.vue` | 修改 | 硬件类型选项移除 PWM |

### 5.3 ChannelTerminal.vue 移除清单

| 行号 | 内容 | 操作 |
|------|------|------|
| L59-74 | GPIO 控制按钮组 HTML | 删除 |
| L233-241 | `gpioDirection` computed | 删除 |
| L244 | `isGpioOutput` computed | 删除 |
| L295 | channelGroups 中 `gpio: 'GPIO'`, `pwm: 'PWM'` | 删除 |
| L310 | getTagType 中 `gpio: 'info'`, `pwm: 'primary'` | 删除 |
| L456-504 | `sendGpioCommand()` + `gpioSetOn/SetOff/Read` | 删除 |

### 5.4 其他前端文件移除清单

**ChannelPanel.vue**:
| 行号 | 内容 | 操作 |
|------|------|------|
| L36 | busType 列表中 `'gpio'` | 移除 |
| L313, L331 | `gpio`/`pwm` 相关映射 | 移除 |
| L585 | `nameToHardwareId` 中 GPIO 分支 | 移除 |
| L593 | `getBusTypeName` 中 `gpio` | 移除 |
| L614 | PIN_ROLE_LABELS 中 `9: 'GPIO'` | **保留**（引脚角色标签仍需要） |

**channel.ts**:
| 行号 | 内容 | 操作 |
|------|------|------|
| L8 | `hardware_type: 'uart' \| 'i2c' \| 'spi' \| 'gpio' \| 'adc' \| 'pwm'` | 改为 `'uart' \| 'i2c' \| 'spi' \| 'adc'` |

**deviceType.ts**:
| 行号 | 内容 | 操作 |
|------|------|------|
| L32 | `{ value: 'gpio.digital', ... }` | 删除 |
| L33 | `{ value: 'gpio.pwm', ... }` | 删除 |

**ChannelList.vue**:
| 行号 | 内容 | 操作 |
|------|------|------|
| L57-59 | GPIO/PWM 筛选选项 | 删除 |
| L289, L301 | `pwm` 标签/颜色映射 | 删除 |

**DeviceConfigList.vue**:
| 行号 | 内容 | 操作 |
|------|------|------|
| L72 | PWM 硬件类型选项 | 删除 |
| L355 | `pwm` 图标映射 | 删除 |

**ParserBrowser.vue**:
| 行号 | 内容 | 操作 |
|------|------|------|
| L198 | `pwm: 'info'` | 删除 |

---

## 6. 固件设计

### 6.1 从 bus 系统中移除 GPIO

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
| `bus_manager.c` | L330 | GPIO 队列路由 | 删除 |
| `bus_worker.c` | L336-338 | `cmd_task_gpio` | 删除 |
| `bus_worker.h` | L63 | `gpio_cmd_queue` 字段 | 删除 |
| `scheduler.c` | L47, L500-501 | GPIO 队列映射和背压 | 删除 |
| `scheduler.h` | L81 | `gpio_cmd_queue` 字段 | 删除 |

### 6.2 新增 gpio_ctrl 模块

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

#define GPIO_DIR_INPUT        0
#define GPIO_DIR_OUTPUT       1
#define GPIO_DIR_INPUT_PULLUP 2
#define GPIO_DIR_INPUT_PULLDN 3

typedef struct {
    uint8_t pin;
    uint8_t direction;
    uint8_t initial_level;
} gpio_config_entry_t;

esp_err_t gpio_ctrl_init(const gpio_config_entry_t *configs, int count);
esp_err_t gpio_ctrl_set(int pin, int level);
int gpio_ctrl_read(int pin);
esp_err_t gpio_ctrl_config(int pin, int direction, int initial_level);
esp_err_t gpio_ctrl_deconfig(int pin);
```

**设计要点**：
- 不需要队列 — GPIO 操作即时执行
- 不需要 worker task — `gpio_set_level()` / `gpio_get_level()` 非阻塞
- 不需要 DMA — GPIO 无 DMA
- 不需要 mutex — `gpio_set_level()` 线程安全
- 不需要 scheduler — GPIO 不是轮询指令

### 6.3 新增 pwm_ctrl 模块

```
components/pwm_ctrl/
├── include/
│   └── pwm_ctrl.h
└── pwm_ctrl.c
```

**pwm_ctrl.h**:

```c
#pragma once
#include <stdint.h>
#include <stdbool.h>
#include "esp_err.h"

// PWM 占空比范围: 0-10000 (0.00% - 100.00%, 精度 0.01%)
#define PWM_DUTY_MIN     0
#define PWM_DUTY_MAX     10000
#define PWM_RES_DEFAULT  13  // LEDC 默认分辨率位数

typedef struct {
    uint8_t  pin;
    uint32_t frequency;
    uint16_t duty;
    uint8_t  resolution;
} pwm_config_entry_t;

// 初始化：解析 ConfigManifest 中的 pwm_configs，配置 LEDC 通道
esp_err_t pwm_ctrl_init(const pwm_config_entry_t *configs, int count);

// 启动 PWM 输出
esp_err_t pwm_ctrl_start(int pin, uint32_t freq, uint16_t duty, uint8_t resolution);

// 停止 PWM 输出
esp_err_t pwm_ctrl_stop(int pin);

// 设置占空比 (0-10000)
esp_err_t pwm_ctrl_set_duty(int pin, uint16_t duty);

// 设置频率 (Hz)
esp_err_t pwm_ctrl_set_freq(int pin, uint32_t freq);

// 读取当前占空比
uint16_t pwm_ctrl_get_duty(int pin);

// 取消配置（停止 + 释放 LEDC 定时器/通道）
esp_err_t pwm_ctrl_deconfig(int pin);
```

**pwm_ctrl.c 实现要点**：

基于 ESP-IDF LEDC 驱动：

```c
#include "driver/ledc.h"

// 每个 PWM 通道占用一个 LEDC 通道
// ESP32-C6: LEDC 有 6 通道 (timer + channel)
// 使用 ledc_timer_config() + ledc_channel_config() 初始化
// 使用 ledc_set_duty() + ledc_update_duty() 调整占空比
// 使用 ledc_set_freq() 调整频率

typedef struct {
    int pin;
    ledc_timer_t timer;
    ledc_channel_t channel;
    uint32_t frequency;
    uint16_t duty;
    uint8_t resolution;
    bool running;
} pwm_instance_t;

static pwm_instance_t s_pwm_instances[LEDC_CHANNEL_MAX] = {0};
```

**设计要点**：
- 不需要队列 — PWM 操作即时执行
- 不需要 worker task — `ledc_set_duty()` 非阻塞
- 不需要 DMA — LEDC 硬件自动生成波形
- 不需要 scheduler — PWM 持续输出，不需要轮询
- 需要 LEDC 资源管理 — ESP32-C6 有 6 个 LEDC 通道，需要分配/释放
- 占空比精度 0.01% — duty 范围 0-10000，LEDC 硬件分辨率映射

### 6.4 消息处理集成

`msg_handler.c` 新增 PeriphCmd 分发：

```c
case MSG_PERIPH_CMD:
    handler_periph_process(&dec);
    break;
```

新增 `handler_periph.c`:

```c
void handler_periph_process(frame_decoder_t *dec)
{
    uint32_t request_id = 0;
    uint8_t periph_type = 0, pin = 0, action = 0;
    uint32_t value = 0;
    const uint8_t *config = NULL;
    size_t config_len = 0;
    // ... 解码 fields ...

    bool success = false;
    uint32_t result_value = 0;

    switch (periph_type) {
    case 1: // GPIO
        gpio_ctrl_handle_cmd(pin, action, config, config_len,
                             &success, &result_value);
        break;
    case 2: // PWM
        pwm_ctrl_handle_cmd(pin, action, value, config, config_len,
                            &success, &result_value);
        break;
    default:
        success = false;
        break;
    }

    msg_handler_send_periph_rsp(request_id, success, result_value, 0);
}
```

### 6.5 ConfigManifest 解析修改

`handler_config.c` 中新增 field 11 (gpio_configs) 和 field 12 (pwm_configs) 处理：

```c
case 11: // gpio_configs (repeated sub-structure)
    // 解析 sub-fields: pin, direction, initial_level
    // 存入 gpio_config_entry_t 数组
    break;
case 12: // pwm_configs (repeated sub-structure)
    // 解析 sub-fields: pin, frequency, duty, resolution
    // 存入 pwm_config_entry_t 数组
    break;
```

应用配置时：
- channels → `bus_manager` / `config_mgr`（不变）
- gpio_configs → `gpio_ctrl_init()`（新路径）
- pwm_configs → `pwm_ctrl_init()`（新路径）

### 6.6 硬件资源上报

`hw_tables.c` 中 `hw_gpios[]` **保留** — 仍用于 ResourceReport。

PWM 资源上报：在 ResourceReport 中新增 PWM 可用通道数信息。ESP32-C6 的 LEDC 有 6 通道，可上报为：
```json
"pwm": {
  "channel_count": 6,
  "max_resolution": 20,
  "supported_freq_range": [1, 40000000]
}
```

### 6.7 固件影响文件清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `components/bus_dma/bus_dma.h` | 修改 | 移除 BUS_TYPE_GPIO 及相关结构 |
| `components/bus_dma/bus_dma.c` | 修改 | 移除 gpio_init/write/read/deinit 及 case |
| `components/bus_manager/bus_manager.c` | 修改 | 移除 GPIO 路由 |
| `components/bus_worker/bus_worker.c` | 修改 | 移除 cmd_task_gpio |
| `components/bus_worker/include/bus_worker.h` | 修改 | 移除 gpio_cmd_queue |
| `components/scheduler/scheduler.c` | 修改 | 移除 GPIO 队列映射和背压 |
| `components/scheduler/scheduler.h` | 修改 | 移除 gpio_cmd_queue |
| `components/gpio_ctrl/` | 新增 | GPIO 控制模块 |
| `components/pwm_ctrl/` | 新增 | PWM 控制模块（LEDC 驱动） |
| `components/msg_handler/msg_handler.c` | 修改 | 新增 MSG_PERIPH_CMD 分发 |
| `components/msg_handler/handler_periph.c` | 新增 | PeriphCmd 处理器 |
| `components/frame/frame_codec.h` | 修改 | 新增 MSG_PERIPH_CMD/RSP 定义 |
| `components/hw_profile/hw_tables.c` | 保留 | hw_gpios[] 仍用于资源上报 |
| `components/hw_profile/hw_profile.c` | 修改 | ResourceReport 新增 PWM 资源信息 |
| `main/main.c` | 修改 | 移除 on_write_cmd_received 中 GPIO 路由，新增 Periph callback |
| `components/CMakeLists.txt` | 修改 | 新增 gpio_ctrl 和 pwm_ctrl 组件 |

---

## 7. 数据流对比

### 7.1 GPIO — 当前 vs 重新设计

**当前**（GPIO 走通道，6 跳）：
```
前端 → POST /channels/:id/terminal/write {data_hex:"01"}
→ handler_terminal → SendWriteCommand → WriteCmd(0x06) → MQTT
→ C6 msg_handler → bus_manager → gpio_cmd_queue → bus_worker
→ bus_dma_transact → gpio_write → gpio_set_level(pin, 1)
→ WriteRsp(0x07) → MQTT → 前端
```

**重新设计后**（GPIO 独立路径，3 跳）：
```
前端 → POST /nodes/:id/gpio/5/set {level:1}
→ handler_periph → SendPeriphCmd(GPIO, SET_HIGH) → PeriphCmd(0x1B) → MQTT
→ C6 msg_handler → handler_periph → gpio_ctrl_set(pin, 1) → gpio_set_level
→ PeriphRsp(0x1C) → MQTT → 前端
```

### 7.2 PWM — 全新路径（3 跳）

```
前端 → POST /nodes/:id/pwm/15/duty {duty:6500}
→ handler_periph → SendPeriphCmd(PWM, SET_DUTY, value=6500) → PeriphCmd(0x1B) → MQTT
→ C6 msg_handler → handler_periph → pwm_ctrl_set_duty(pin, 6500) → ledc_set_duty
→ PeriphRsp(0x1C) → MQTT → 前端
```

### 7.3 启动 PWM 输出（3 跳）

```
前端 → POST /nodes/:id/pwm/15/start
→ handler_periph → SendPeriphCmd(PWM, START, config=[freq:4B,duty:2B,res:1B])
→ PeriphCmd(0x1B) → MQTT
→ C6 msg_handler → handler_periph → pwm_ctrl_start(pin, 1000, 6500, 13)
→ ledc_timer_config + ledc_channel_config → 硬件持续输出 PWM 波形
→ PeriphRsp(0x1C) → MQTT → 前端
```

---

## 8. 迁移策略

### 8.1 GPIO 数据迁移

```sql
-- 从 channels 表提取 GPIO 配置，插入 gpio_configs 表
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

-- 删除 GPIO 类型 Channel
DELETE FROM channels WHERE hardware_type = 'gpio';
```

### 8.2 PWM 数据迁移

**无** — PWM 无现有数据（后端和固件从未实现）。

### 8.3 前端 PWM 占位清理

直接删除前端所有 PWM 在通道系统中的占位代码（筛选器、标签映射、类型声明等），替换为 PeripheralControl.vue 中的 PWM 卡片。

### 8.4 实施顺序

1. **固件**: 新增 `gpio_ctrl` + `pwm_ctrl` 模块 + `handler_periph.c` + MSG_PERIPH_CMD/RSP（不删除旧 GPIO 路径）
2. **后端**: 新增 `GPIOConfig` + `PWMConfig` 模型 + `handler_periph.go` + `sender.SendPeriphCmd()` + ConfigManifest field 11/12
3. **前端**: 新增 `PeripheralControl.vue` + `api/periph.ts`，移除 PWM 通道占位
4. **验证**: 新路径端到端测试（GPIO: C6 物理引脚 | PWM: 示波器/万用表测量波形）
5. **迁移**: GPIO 数据迁移脚本执行
6. **清理**: 删除旧路径代码（bus_dma GPIO 分支、driver、ChannelTerminal GPIO 代码、前端 PWM 通道占位）

### 8.5 向后兼容

迁移期间：
- 旧 GPIO Channel 仍可工作（旧路径不删）
- 新 GPIOConfig/PWMConfig API 同时可用（新路径）
- 迁移完成后删除旧路径，在 channel CRUD 中拒绝 gpio/pwm 类型

---

## 9. ESP32-C6 LEDC 资源约束

| 参数 | ESP32-C6 | 说明 |
|------|----------|------|
| LEDC 通道数 | 6 | 6 个独立 PWM 通道 |
| 定时器数 | 4 | 4 个定时器，可共享频率 |
| 最大分辨率 | 20 bit | 1,048,576 级 |
| 频率范围 | 1 Hz - 40 MHz | 取决于分辨率 |
| 默认配置 | 13 bit, 5000 Hz | 8192 级 duty |

资源管理策略：
- 同频率的 PWM 通道共享一个定时器（最多 6 通道 / 4 定时器）
- `pwm_ctrl` 维护 pin → LEDC channel/timer 映射表
- 启动时自动分配空闲 LEDC 通道和定时器
- 停止时释放 LEDC 通道（定时器在有其他通道使用时保留）

---

## 10. 待确认事项

| # | 问题 | 建议 |
|---|------|------|
| 1 | ADC 是否也需独立？ | ADC 有采样间隔概念，更接近数据采集。后续单独评估 |
| 2 | GPIO 中断事件推送？ | 当前设计为主动读取。如需电平变化中断通知，后续可扩展 PeriphRsp 或新增事件消息 |
| 3 | PWM 占空比精度？ | 0.01% (0-10000) 应满足绝大多数场景。如需更高精度可改用 LEDC 原始 duty 值 |
| 4 | 多定时器频率共享？ | 同频率 PWM 共享定时器，不同频率各占一个定时器（最多 4 个不同频率） |
| 5 | PeriphCmd 消息类型号 | 0x1B/0x1C — 当前协议最大 0x1A (QueryResources)，0x1B+ 均可用 |
