# GPIO+PWM 外设控制重构设计方案 — 评审报告

> **评审人**: 嵌入式系统+通信协议专家
> **评审日期**: 2026-07-09
> **方案版本**: v2.0 (2026-07-09)
> **方案文件**: `docs/设计/GPIO控制重构设计.md`

---

## 评审结论摘要

方案的核心架构决策——GPIO/PWM 从通道系统剥离——方向正确，语义合理，路径简化（6跳→3跳）显著。但在协议消息设计、线程安全、LEDC资源管理、向后兼容等方面存在若干需修正的问题。

| 严重程度 | 数量 |
|---------|------|
| 🔴 致命 | 3 |
| 🟡 重要 | 6 |
| 🔵 建议 | 5 |

---

## 1. 协议消息设计

### 1.1 🔴 致命 — PWM SET_FREQ 操作缺少 resolution 上下文，频率切换可能导致 duty 溢出

**问题**: PWM action `SET_FREQ` (value=新频率) 只传频率值，但 LEDC 的 duty 寄存器值与 (频率, 分辨率) 绑定。当频率变化时，相同的 duty 百分比对应的 LEDC 原始值会改变。`pwm_ctrl_set_freq()` 调用 `ledc_set_freq()` 后，如果不重新计算并更新 duty 原始值，输出占空比会漂移。

**更严重的问题**: 频率变更可能导致 LEDC 定时器分频器需要重新配置。`ledc_set_freq()` 仅调整分频器，但如果新频率超出当前分辨率支持的范围（低频需要更低分辨率，高频需要更高分辨率），需要 `ledc_timer_config()` 重新配置定时器，这会中断同定时器上所有通道的输出。

**建议**:
- `SET_FREQ` 应同时携带新分辨率（或至少校验当前分辨率是否支持新频率），必要时重新配置定时器
- 或将 `SET_FREQ` 的 config 字段定义为 `[freq:4B, resolution:1B]`，与 START 的 config 格式对齐
- 文档中应明确：频率变更是否允许跨分辨率，以及同定时器其他通道的影响

### 1.2 🔴 致命 — PeriphRsp 缺少 periph_type 和 pin 字段，无法异步匹配

**问题**: PeriphRsp (0x1C) 只有 `request_id`、`success`、`value`、`error_code` 四个字段。虽然 `request_id` 可匹配请求，但如果需要异步事件推送（方案 §10 待确认事项 #2 提到 GPIO 中断事件），没有 `periph_type` 和 `pin` 字段就无法标识事件来源。

更重要的是：如果未来扩展 ADC 等其他外设类型到 PeriphCmd/Rsp 消息族，响应中缺少类型信息会让调试和日志变得困难——只能靠 request_id 查表。

**实现纠正（2026-07-15）**：PeriphRsp field 5/6 最终采用 `periph_type + resource_id`；其中 GPIO 的 `resource_id=pin`，PWM 的 `resource_id=LEDC channel`。PWM 输出 pin 由持久化配置关联，不作为 PWM 资源身份。

### 1.3 🟡 重要 — 统一消息 + type 字段 vs 分离消息类型的权衡

**问题**: 将 GPIO 和 PWM 合并为 `PeriphCmd(0x1B)` + `periph_type` 字段，从协议效率角度可行（节省消息类型号），但有以下隐患：

- **action 枚举语义重叠但不可混用**: GPIO action 0=SET_LOW 和 PWM action 0=SET_DUTY 完全不同，解析器必须先判断 periph_type 再解释 action，增加认知负担和出错可能
- **value 字段类型不一致**: GPIO 的 value 几乎不用（SET_LOW/SET_HIGH/READ 都不需要 value），PWM 的 value 是 duty(0-10000) 或 freq(Hz)，语义差异大
- **config 编码按外设类型区分**: GPIO config 是 `[direction:1B, initial_level:1B]`；PWM START config 是 `[pin:1B, freq:4B LE, duty:2B LE, resolution:1B]`。PWM pin 仅为输出路由，资源身份仍是 PeriphCmd field 3 的 LEDC channel。

**评估**: 当前 GPIO+PWM 两种类型用统一消息尚可接受，但如果后续 ADC/I2C-expander 等更多外设加入，统一消息会变得臃肿。

**建议**: 
- 短期可保留统一消息设计，但应在协议文档中明确标注 periph_type 的命名空间分配表
- 长期建议预留分离消息类型的迁移路径：当 periph_type 超过 4 种时拆分

### 1.4 🔵 建议 — GPIO action 缺少 TOGGLE 操作

**问题**: GPIO action 只有 SET_LOW(0)、SET_HIGH(1)、READ(2)、CONFIG(3)、DECONFIG(4)，缺少 TOGGLE。虽然前端可以通过 READ→判断→SET 实现，但这需要两次往返。对于继电器等需要快速翻转的场景（如脉冲控制），TOGGLE 是常用操作。

**建议**: 增加 action 5=TOGGLE，固件内 `gpio_get_level()` → `gpio_set_level(!level)` 原子执行。

### 1.5 🔵 建议 — PWM action 缺少 SET_RESOLUTION 操作

**问题**: PWM 只能在 START 时设置 resolution，运行期间无法动态调整分辨率。如果用户需要从低频高精度切换到高频低精度，只能 STOP→START，开销大。

**建议**: 增加 action 5=SET_RESOLUTION，value=resolution_bits (4-20)。

---

## 2. ConfigManifest 扩展

### 2.1 🔴 致命 — ConfigManifest field 11/12 与现有字段编号体系不兼容

**问题**: 方案为 gpio_configs 分配 field 11，pwm_configs 分配 field 12。但现有 ConfigManifest 消息的字段编号为：
- field 1: manifest_id (string)
- field 3: templates (repeated bytes)
- field 4: channels (repeated bytes)
- field 5: dma_channel_configs (repeated bytes)
- **field 2 被跳过**（未使用，预留）

field 6-10 在当前协议中未定义但属于 ChannelConfig 子消息的通用字段范围（field 5-9 是 ChannelConfig 的字段，field 9 是 edge_device_groups）。在 protobuf 风格的编码中，顶层消息的字段编号与子消息字段编号是独立的，所以 field 11/12 在顶层消息中**理论上可用**。

但问题在于：**方案没有检查 field 6-10 是否已被其他扩展预留**。根据现有文档，field 6-10 在 ConfigManifest 顶层未被使用，但方案应明确声明这一点，并确认没有其他正在进行的扩展计划使用这些字段号。

**更关键的问题**: 方案没有定义 ConfigRslt (0x05) 如何报告 gpio_configs/pwm_configs 的应用结果。现有 ConfigRslt 只有 `success/error_code/error_msg` 三个字段，是全局成功/失败。如果 GPIO 配置成功但 PWM 配置失败，现有结构无法表达部分成功。

**建议**:
- 明确声明 field 6-10 在 ConfigManifest 顶层预留状态，并分配 field 11/12 的理由（跳过 6-10 留给未来通道系统扩展）
- ConfigRslt 扩展：增加可选的 `field 4: periph_results (repeated)` 子消息，包含 `{periph_type, pin, success, error_code}`，用于报告每个外设配置项的应用结果
- 或简化方案：gpio_configs 和 pwm_configs 的应用失败不影响整体 ConfigRslt（仍报 success=true），单个引脚配置失败通过 PeriphRsp 异步报告

### 2.2 🟡 重要 — gpio_configs/pwm_configs 的 sub-field 编码未定义 wire type

**问题**: 方案描述了 gpio_configs 的 sub-field 1=pin(varint), 2=direction(varint), 3=initial_level(varint)，但没有明确定义这些子消息的编码方式。在现有协议中，templates 和 channels 都是作为 `field N (bytes)` 传输的嵌套子消息——外层是 length-delimited (wire_type=2)，内层再按 protobuf 风格编码。

方案应明确：
- gpio_configs 在 ConfigManifest 中是 `field 11, wire_type=2 (bytes)` 的 repeated 嵌套子消息
- 每个 gpio_config 子消息内部按 field 1/2/3 编码
- 后端 sender.go 编码和固件 handler_config.c 解析需要同步实现这套嵌套编解码

**建议**: 在方案中补充完整的编码示例（类似现有 ChannelConfig 子消息的定义格式），包括 wire_type 标注。

### 2.3 🔵 建议 — pwm_configs 缺少 speed_mode 字段

**问题**: ESP32-C6 只有高速模式 (LEDC_HIGH_SPEED_MODE)，但 ESP32-S3 有低速模式。pwm_configs 的 sub-field 没有包含 speed_mode，如果未来方案要支持 S3 节点，需要扩展。

**建议**: 可暂不添加（C6-only），但在方案中注明 S3 兼容时需要增加 speed_mode 字段。

---

## 3. gpio_ctrl 模块

### 3.1 🟡 重要 — "不需要 mutex" 的论断需要限定条件

**问题**: 方案声明"不需要 mutex — `gpio_set_level()` 线程安全"。这个论断**部分正确但需要限定**：

- `gpio_set_level()` 本身是线程安全的（ESP-IDF 内部有 GPIO bus 互斥）
- `gpio_get_level()` 也是线程安全的
- **但 `gpio_config()` / `gpio_reset_pin()` 不是线程安全的**——如果 CONFIG 和 SET_LEVEL 并发执行，可能出现竞态

方案中的 gpio_ctrl 需要处理以下并发场景：
1. MQTT 回调线程执行 SET_HIGH/SET_LOW/READ（高频，来自 handler_periph_process）
2. ConfigManifest 应用时执行 gpio_ctrl_init() → gpio_config()（低频，来自 handler_config_process_manifest）
3. 两者运行在不同的任务上下文中

如果 ConfigManifest 重新应用时 gpio_ctrl_init() 先 deconfig 所有引脚再重新配置，而同时 MQTT 回调在 SET_LEVEL，会导致对已 reset 的引脚操作。

**建议**:
- 对 CONFIG/DECONFIG 操作加 mutex（保护 gpio_config/gpio_reset_pin）
- SET_HIGH/SET_LOW/READ 可以不加 mutex（依赖 ESP-IDF GPIO 驱动的线程安全）
- 或更简单的方案：gpio_ctrl_init() 在重新配置前设置一个 "reconfiguring" 标志，SET/READ 操作检查此标志并拒绝

### 3.2 🟡 重要 — MQTT 回调中直接执行 gpio_ctrl 的时序分析

**问题**: 从代码分析，MQTT 消息回调运行在 MQTT 事件任务 (`mqtt_event_handler`) 中。`msg_handler_process()` → `handler_periph_process()` → `gpio_ctrl_set()` → `gpio_set_level()` 在此任务上下文执行。

`gpio_set_level()` 执行时间约 1-5μs，可接受。但需考虑：
- MQTT 事件任务是单线程的，一条消息处理完才能处理下一条。如果 GPIO 操作快（μs 级），不影响 MQTT 吞吐
- **但如果 PeriphCmd 处理中发送 PeriphRsp**（`msg_handler_send_periph_rsp`），这会调用 `esp_mqtt_client_publish()`，在 MQTT 事件任务中调用 MQTT publish 有死锁风险（ESP-IDF 文档明确警告：不要在 MQTT event handler 中调用 `esp_mqtt_client_publish()`）

查看现有代码，`ehome_mqtt.c` L220 注释明确写道："Don't hold mutex during event processing - MQTT events run in the MQTT task and blocking here can deadlock with esp_mqtt client API calls"。

现有 WriteCmd 处理通过 `on_write_cmd_received` → `bus_manager` → 队列 → bus_worker 在独立任务中执行，响应从 bus_worker 任务发送，避开了此问题。

**建议**:
- PeriphRsp 不能直接在 MQTT 回调中发送，需要通过队列转发到独立任务，或使用 `esp_mqtt_client_publish()` 的非阻塞模式并验证线程安全
- 或者：GPIO 快速操作可在 MQTT 回调中执行，但 PeriphRsp 的发送需要异步化（投递到专用发送队列）
- 方案应明确 PeriphRsp 的发送路径和线程上下文

### 3.3 🔵 建议 — gpio_ctrl 缺少引脚有效性校验

**问题**: gpio_ctrl_set(pin, level) 没有校验 pin 是否在 hw_gpios[] 表中、是否已通过 gpio_ctrl_config 配置为 OUTPUT。直接调用 `gpio_set_level()` 对未配置的引脚操作虽然 ESP-IDF 会返回 ESP_FAIL，但应在 gpio_ctrl 层做前置校验，返回明确的错误码。

**建议**: gpio_ctrl 维护一个 `s_gpio_state[MAX_PIN]` 数组，记录每个引脚的配置状态，SET/READ 操作前校验。

---

## 4. pwm_ctrl 模块

### 4.1 🔴 致命 — ESP32-C6 LEDC 定时器数量可能有误

**问题**: 方案 §9 声明 ESP32-C6 LEDC 有 "6 通道 / 4 定时器"。但根据 ESP32-C6 技术参考手册，ESP32-C6 的 LEDC 控制器有 **6 个通道和 3 个定时器**（不是 4 个）。ESP32-S3 才有 4 个定时器。

> ESP32-C6 TRM: "The LEDC controller has six channels and three timers."

这意味着"不同频率各占一个定时器（最多 4 个不同频率）"的约束应改为"最多 3 个不同频率"。如果代码按 4 个定时器实现，第 4 个定时器的分配会导致未定义行为。

**建议**:
- 确认 ESP32-C6 LEDC 定时器数为 3（查 ESP32-C6 TRM 或 ESP-IDF `soc/ledc_reg.h`）
- 修正 §9 的约束表：定时器数 4→3，"最多 4 个不同频率"→"最多 3 个不同频率"
- `pwm_ctrl.c` 中定时器分配逻辑的边界条件相应调整

### 4.2 🟡 重要 — LEDC 通道/定时器分配释放的边界问题

**问题**: 方案描述"同频率共享定时器"，但缺少以下关键边界场景的处理：

1. **定时器引用计数**: 多个通道共享一个定时器时，STOP 一个通道不应释放定时器（其他通道仍在用）。需要引用计数机制。
2. **频率变更导致定时器重分配**: SET_FREQ 时，如果新频率与当前定时器不匹配，需要将通道迁移到另一个定时器（或新建定时器）。如果所有定时器都被占用且无匹配频率，应返回错误。
3. **定时器资源耗尽**: 3 个定时器都被不同频率占用时，新建不同频率的 PWM 应返回 `ESP_ERR_NOT_FOUND`，方案未描述此错误路径。
4. **通道释放后定时器残留**: 所有通道都 STOP 后，定时器是否自动 deinit？如果不 deinit，定时器会继续运行消耗功率。
5. **deconfig 与 stop 的关系**: `pwm_ctrl_deconfig(pin)` 应先 stop 再释放资源，但如果 pin 未启动直接 deconfig，是否安全？

**建议**: 
- 补充定时器引用计数管理逻辑设计
- 补充资源耗尽错误处理路径
- 补充频率变更时的定时器迁移流程
- 补充 deconfig 的幂等性保证

### 4.3 🟡 重要 — duty 精度 0.01% 与 LEDC 硬件分辨率的映射存在精度损失

**问题**: 方案定义 duty 范围 0-10000（0.01% 精度），LEDC 硬件分辨率 4-20 bit（默认 13 bit = 8192 级）。

映射公式: `ledc_duty = (duty * 2^resolution) / 10000`

以默认 13 bit 为例：
- duty=1 (0.01%) → ledc_duty = 1 * 8192 / 10000 = 0 (截断) → **实际输出 0%**
- duty=2 (0.02%) → ledc_duty = 2 * 8192 / 10000 = 1 → 实际输出 0.012%
- duty=5000 (50%) → ledc_duty = 5000 * 8192 / 10000 = 4096 → 实际输出 50.00% ✓
- duty=9999 (99.99%) → ledc_duty = 9999 * 8192 / 10000 = 8190 → 实际输出 99.97%

**问题核心**: 0.01% 精度（10000 级）在 13 bit（8192 级）分辨率下是**虚标**——硬件分辨率低于声称的精度。低 duty 值（1-2）会被截断为 0。

**建议**:
- 方案中明确说明 duty 精度受 resolution 约束：`实际精度 = 100 / 2^resolution %`
- 13 bit 时实际精度约 0.0122%，与 0.01% 接近但非完全匹配
- 建议默认 resolution 提高到 14 bit（16384 级），此时 10000 级 duty 可完整映射
- 或方案中注明：duty=1 在 13bit 下可能输出 0，如需精确低占空比请提高 resolution

---

## 5. 消息处理

### 5.1 🟡 重要 — PWM START 操作在 MQTT 回调中的耗时

**问题**: `ledc_timer_config()` + `ledc_channel_config()` 涉及：
- 定时器分频器配置（寄存器写入 + 等待稳定）
- 通道配置（GPIO matrix 路由 + 寄存器写入）
- 可能的定时器使能等待

这些操作通常在 100-500μs 范围，在 MQTT 回调中执行**勉强可接受**，但存在与 §3.2 相同的问题：MQTT 事件任务中阻塞会影响后续消息处理。

更重要的是，如果 PeriphRsp 在同一回调中发送（`msg_handler_send_periph_rsp`），`esp_mqtt_client_publish()` 可能耗时 1-10ms（取决于 MQTT QoS 和 TCP 缓冲），加上 LEDC 配置的 500μs，总耗时可能达到 10ms+，显著影响 MQTT 消息吞吐。

**建议**:
- GPIO 操作（μs 级）可在 MQTT 回调中直接执行
- PWM START/STOP 操作（百 μs 级）建议通过轻量队列转发到 pwm_worker 任务
- PeriphRsp 发送必须从 MQTT 回调线程解耦
- 方案应量化各操作的预期耗时并给出线程模型设计

### 5.2 🟡 重要 — handler_periph.c 缺少错误恢复和参数校验设计

**问题**: `handler_periph_process` 的伪代码中只有基本的 switch-case 分发，缺少：
- 协议字段缺失时的错误处理（如 PWM SET_DUTY 缺少 value 字段）
- pin 范围校验（pin >= HW_GPIO_COUNT 时拒绝）
- action 范围校验（action > 4 时拒绝）
- config 长度校验（PWM START 的 config 必须 ≥7 字节）
- gpio_ctrl/pwm_ctrl 返回错误时的 error_code 映射

方案中 PeriphRsp 有 `error_code` 字段但未定义错误码枚举。

**建议**:
- 定义 error_code 枚举：0=OK, 1=INVALID_PIN, 2=INVALID_ACTION, 3=INVALID_PARAM, 4=RESOURCE_EXHAUSTED, 5=NOT_CONFIGURED, 6=HW_ERROR
- handler_periph_process 增加完整的参数校验逻辑
- 补充错误场景测试用例

---

## 6. 向后兼容

### 6.1 🟡 重要 — 迁移期间 WriteCmd(0x06) 和 PeriphCmd(0x1B) 同时处理 GPIO 的冲突

**问题**: §8.5 迁移期间"旧 GPIO Channel 仍可工作（旧路径不删）"+"新 GPIOConfig API 同时可用"。但同一物理引脚可能被两条路径同时操作：

- 旧路径: GPIO Channel (bus_type=4) 通过 WriteCmd(0x06) → bus_dma → gpio_write → gpio_set_level(pin, level)
- 新路径: GPIOConfig 通过 PeriphCmd(0x1B) → handler_periph → gpio_ctrl_set(pin, level)

如果同一个 pin 被旧路径配置为 GPIO Channel，同时被新路径配置为 GPIOConfig，两条路径都会调用 `gpio_config()` 和 `gpio_set_level()`，导致：
- 双重 `gpio_config()` 可能不报错但行为未定义
- 两套路径各自维护引脚状态，SET_LEVEL 互相覆盖
- DECONFIG（新路径）和 Channel DELETE（旧路径）不同步

**建议**:
- 迁移期间在 gpio_ctrl_config() 中检查 pin 是否已被 bus_dma GPIO 通道占用，拒绝双重配置
- 或在迁移期间禁用旧路径 GPIO Channel 的创建（只保留已有 Channel 的控制功能）
- 数据迁移脚本应确保一个 pin 只存在于 gpio_configs 或 channels 之一，不能同时存在
- 方案应明确迁移期间的引脚所有权策略

### 6.2 🔵 建议 — 迁移 SQL 缺少 hardware_id 格式校验

**问题**: §8.1 的迁移 SQL 使用 `SUBSTRING(hardware_id FROM 'GPIO([0-9]+)')` 提取 pin 号。但如果 hardware_id 格式不是 "GPIO5" 而是其他格式（如 "gpio5"、"GPIO_5"、空值），CAST 会失败或返回 NULL。

**建议**:
- 迁移前先 `SELECT DISTINCT hardware_id FROM channels WHERE hardware_type='gpio'` 确认格式
- SQL 增加 WHERE 条件过滤 NULL pin
- 补充回滚脚本

---

## 7. ESP32-C6 特有约束

### 7.1 🔴 致命 — LEDC 定时器数 4→3（见 §4.1）

已在 §4.1 详述。这是方案中的硬事实错误，会影响资源管理逻辑的正确性。

### 7.2 🟡 重要 — PWM 可用引脚约束未说明

**问题**: 方案中 pwm_configs 的 pin 字段是任意 uint8，但 ESP32-C6 的 LEDC 输出可路由到大部分 GPIO，但有例外：
- GPIO12/13 = USB D-/D+（已预留，不可用）
- GPIO8 = RGB LED（已预留，不可用）
- GPIO0-7 是 hw_gpios[] 中列出的可用引脚
- GPIO14-23 理论上也可用于 LEDC（ESP32-C6 有 GPIO0-23 共 24 个 GPIO），但 hw_gpios[] 只列了 0-7

方案没有说明 PWM 可以使用哪些引脚，也没有校验 pin 是否与 UART/I2C/SPI 引脚冲突。

**建议**:
- 定义 PWM 可用引脚列表（或 GPIO matrix 任意可路由的引脚范围）
- pwm_ctrl_start() 中校验 pin 不与已配置的 UART/I2C/SPI 引脚冲突
- 在 ResourceReport 的 PWM 资源信息中上报可用引脚列表
- GPIO 和 PWM 共用引脚池，需要跨模块的引脚冲突检测

### 7.3 🔵 建议 — LEDC 频率范围与分辨率的约束公式缺失

**问题**: 方案声明频率范围 1 Hz - 40 MHz，但 LEDC 的实际频率范围取决于分辨率和 APB 时钟：
- `freq = APB_CLK / (2^resolution * divisor)`
- ESP32-C6 APB 时钟 80 MHz
- 13 bit 分辨率下：最低频率约 80M / (8192 * 2) ≈ 4883 Hz，最高约 80M / 8192 ≈ 9765 Hz

等等，这意味着 13 bit 下频率范围只有约 5-10 kHz，远非 1 Hz - 40 MHz。要实现 1 Hz 需要约 20 bit 分辨率（2^20 = 1048576，80M/1048576 ≈ 76 Hz，还不够低），需要使用 low-speed 定时器或 ref tick 源。

**实际上 ESP-IDF LEDC 驱动会自动选择分频器**，但频率和分辨率有联合约束：`freq * 2^resolution ≤ APB_CLK / 2`（近似）。

方案中"频率范围 1 Hz - 40 MHz"的说法过于乐观，实际可达频率取决于分辨率配置。

**建议**:
- 补充 LEDC 频率-分辨率联合约束公式
- pwm_ctrl_start() 中校验 (freq, resolution) 组合的可行性
- 在 ResourceReport PWM 资源中上报 APB 时钟频率，让后端可以预校验

---

## 8. 其他发现

### 8.1 🔵 建议 — PWM 资源上报在 ResourceReport 中的字段编号未定义

**问题**: §6.6 描述了 PWM 资源上报的 JSON 结构（channel_count/max_resolution/supported_freq_range），但 ResourceReport (0x19) 是二进制帧协议，不是 JSON。现有 ResourceReport 字段为 field 1-4, 8，PWM 资源应该分配新的 field 编号（如 field 9 或 field 6）。

**建议**: 明确 PWM 资源在 ResourceReport 中的 field 编号和子消息结构。

### 8.2 🔵 建议 — 缺少 PWM 渐变（fade）功能的设计考量

**问题**: LEDC 硬件支持 duty 渐变（fade），ESP-IDF 提供 `ledc_set_fade_with_time()` API。对于 LED 调光场景，渐变是常见需求。方案未提及是否支持 fade，也未明确声明不支持。

**建议**: 在待确认事项中增加 "PWM fade 功能是否需要？"，或在方案中明确声明 v1 不支持 fade，后续可扩展 action 6=FADE。

---

## 评审总结

### 必须修复（致命，阻塞实施）:
1. **§4.1** ESP32-C6 LEDC 定时器数 4→3，修正约束表和资源管理逻辑
2. **§1.1** PWM SET_FREQ 缺少 resolution 上下文，频率切换会导致 duty 溢出
3. **§2.1** ConfigManifest field 11/12 分配理由不充分，ConfigRslt 无法报告部分成功

### 强烈建议修复（重要，不修则有运行时风险）:
4. **§3.2 + §5.1** PeriphRsp 在 MQTT 回调中发送有死锁风险，必须解耦
5. **§3.1** gpio_ctrl CONFIG/DECONFIG 与 SET/READ 的并发竞态
6. **§4.2** LEDC 定时器引用计数和资源耗尽处理
7. **§4.3** duty 0.01% 精度在 13bit 下的虚标问题
8. **§6.1** 迁移期间新旧路径引脚冲突
9. **§7.2** PWM 可用引脚约束和跨模块引脚冲突检测

### 可后续改进（建议）:
10. **§1.4** GPIO TOGGLE action
11. **§1.5** PWM SET_RESOLUTION action
12. **§8.1** PWM 资源在 ResourceReport 中的 field 编号
13. **§8.2** PWM fade 功能考量
14. **§5.2** error_code 枚举定义和参数校验

---

> **评审人注**: 方案整体架构方向正确，GPIO/PWM 从通道剥离是正确的架构演进。上述问题主要集中在实现细节层面——协议字段完备性、ESP-IDF API 的线程安全约束、LEDC 硬件约束的准确表述。建议修复致命问题后进入实施阶段，重要问题在实施过程中同步解决。
