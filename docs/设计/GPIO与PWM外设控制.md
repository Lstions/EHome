# GPIO与PWM外设控制

> **状态**: 已实现（v3.0 交付：外设从通道系统剥离，PeriphCmd/PeriphRsp 独立协议直控；两轮评审闭环）
> **版本**: v3.0
> **日期**: 2026-08-22
> **关联**: [总体设计.md](总体设计.md) | [术语表.md](术语表.md) | [../协议/二进制帧协议.md](../协议/二进制帧协议.md) | [边缘设备.md](边缘设备.md)

## 1. 背景与核心决策

**GPIO/PWM 不走通道系统**（v3.0 裁决）：

- 通道 = 可数据交互、有协议帧的物理总线端点（UART/I2C/SPI/ADC）。
- GPIO 是引脚直控、PWM 是 LEDC 通道直控，无协议帧语义，塞进通道系统造成"链路 6 跳 → 3 跳"的复杂度冗余。
- 剥离后：外设通过独立 PeriphCmd(0x1B)/PeriphRsp(0x1C) 直控；前端为行式资源控制面板。
- 硬件资源仍来自 ResourceReport 上报（GPIO entry = 物理引脚，PWM entry = LEDC 通道），**不得由 GPIO 数量推导 PWM 数量**。

## 2. 协议契约（PeriphCmd 0x1B / PeriphRsp 0x1C）

### 2.1 PeriphCmd（中心端 → 节点）

```
field 1: request_id  (varint uint32) — 匹配响应
field 2: periph_type (varint uint8)  — 1=GPIO, 2=PWM
field 3: resource_id (varint uint8)  — GPIO=pin；PWM=LEDC channel
field 4: action      (varint uint8)  — 操作类型
field 5: value       (varint uint32) — 操作参数（level/duty/freq）
field 6: config      (bytes)         — 配置参数（如 [direction:1B, initial_level:1B]）
```

### 2.2 PeriphRsp（节点 → 中心端）

```
field 1: request_id  (varint uint32)
field 2: success     (varint bool)
field 3: value       (varint uint32) — 返回（read 的 level/duty 等）
field 4: error_code  (varint uint8)  — 0=OK; 1=INVALID_PIN; 2=INVALID_ACTION; ...
field 5: periph_type (varint uint8)  — [可选] 异步场景标识
field 6: resource_id (varint uint8)  — [可选]
```

### 2.3 Action 枚举

| periph_type | action | 含义 |
|-------------|--------|------|
| GPIO (1) | 0 SET_LOW / 1 SET_HIGH / 2 READ / 3 CONFIG / 4 DECONFIG | 直控电平 |
| PWM (2) | 0 SET_DUTY / 1 SET_FREQ / 2 START / 3 STOP / 4 STATE | LEDC 控制 |

### 2.4 配置下发（ConfigManifest field 11/12）

- field 11: gpio_configs（pin/direction/initial_level）
- field 12: pwm_configs（LEDC channel、pin、frequency、duty 0-10000、resolution、auto_start）
- **仅对 protocol_version ≥ 2.4 的节点下发**；外设配置失败通过 PeriphRsp 异步报告，不影响整体 ConfigRslt。

## 3. 固件实现（gpio_ctrl / pwm_ctrl / periph_owner）

```
components/
├── gpio_ctrl/        # GPIO 引脚状态管理 + 直控（set/read/config）
├── pwm_ctrl/         # LEDC 定时器/通道管理（引用计数、频率-分辨率联合约束）
├── periph_owner/     # 外设资源所有权（防 GPIO/PWM 与总线引脚冲突）
└── msg_handler/handler_periph.c  # PeriphCmd 分发 + PeriphRsp 异步发送
```

关键并发模型（评审结论落地）：

- SET/READ 等快速操作在 MQTT 回调内执行（μs 级）；CONFIG/DECONFIG 加互斥（gpio_config/gpio_reset_pin 非线程安全）。
- PeriphRsp 发送从 MQTT 回调解耦（经队列转发独立任务）。
- LEDC 定时器引用计数：多条通道共享定时器，STOP 不释放被其他通道占用的定时器；资源耗尽返回明确错误。

## 4. 后端实现

| 关注点 | 位置 |
|--------|------|
| GPIO/PWM API | `internal/api/handler_periph.go`（/nodes/:id/gpio、/nodes/:id/pwm 系列） |
| 命令发送 | PeriphCmd 编码 + MQTT QoS2 发布 |
| 响应处理 | PeriphRsp 解码 → request_id 关联 → WS 推送 |
| 配置文案 | ConfigManifest 编码 gpio_configs/pwm_configs（field 11/12） |
| 权限 | 单主体模式：登录即可（与业务写操作同策略） |

## 5. 前端实现

- `components/periph/`：GPIOResourceList / PWMResourceList 行式资源控制面板。
- 交互规格：统一行容器、DOM 信息顺序、操作层级、未配置/已配置同屏策略、响应式（桌面/移动）。
- GPIO 行：引脚 ID + 方向 + 当前电平 + SET/READ 按钮（READ 显示 loading）。
- PWM 行：通道 + 频率 + 占空比滑块（拖动结束提交，防 API 风暴）+ 启停 + 状态。

## 6. 验收标准

- [ ] GPIO SET_HIGH/LOW/READ 三操作闭环（实机验证：引脚电平变化 + READ 返回）。
- [ ] PWM START（freq/duty/resolution 合法组合）→ 波形输出；STOP 停止。
- [ ] PWM 占空比滑块：拖动过程不发 API、松开才提交。
- [ ] 未配置外设操作 → 明确错误提示。
- [ ] 配置下发后外设面板显示正确占用状态。
- [ ] 后端：PeriphCmd 编码单测 + PeriphRsp 错误码映射。
- [ ] 固件：gpio_ctrl/pwm_ctrl host tests（并发安全 + 边界）。

## 7. 已知限制

- 引脚冲突：GPIO/PWM 与总线引脚冲突由 periph_owner + ResourceReport 校验，但 S3/C6 各自 LEDC 定时器数不同（C6=3 定时器/6 通道；S3=4 定时器），频率种类有硬件上限。
- PWM fade 渐变未实现（后续可扩展 action）。
- 无 GPIO 中断事件推送（异步事件场景预留了 periph_type/resource_id 可选字段，未实现）。
