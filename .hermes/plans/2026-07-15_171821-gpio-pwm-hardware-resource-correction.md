# GPIO/PWM 硬件资源逻辑修正 Implementation Plan

> **For Hermes:** Use subagent-driven-development skill to implement this plan task-by-task.

**Goal:** 以 `esp32-collector/components/hw_profile/hw_tables.c` 为唯一硬件资源事实源，把 GPIO 引脚资源与 PWM/LEDC 通道资源分开建模、上报、校验和展示；PWM 配置必须先选择一个真实 PWM 硬件通道，再将其路由到一个可用 GPIO 引脚。

**Architecture:** GPIO 资源是 `hw_gpios[]` 中的物理引脚，PWM 资源是新增/补全的 `hw_pwms[]` 中的 LEDC 输出通道，两者不是同一种资源，也不能再把“每个 GPIO 都可启用 PWM”当作 PWM 资源。节点通过 ResourceReport 上报两套能力；后端保存并依据节点上报能力执行 fail-closed 校验；前端分别展示 GPIO 与 PWM 资源。PWM 配置的主身份从 `pin` 改为 `hardware_id/channel`，`pin` 仅表示 PWM 通道当前路由到的输出引脚。

**Tech Stack:** ESP-IDF 6/C、二进制 frame 协议、Go/Gin/GORM/PostgreSQL、Vue 3/TypeScript/Element Plus/Vitest、MQTT、Chromium CDP、ESP32-C6 实机 OTA。

---

## 1. 当前上下文与已确认根因

### 1.1 当前工作区

- Worktree：`/home/sun/workspace/EHomeSystem-gpio`
- 分支：`feat/gpio-control`
- 当前 HEAD：`bea84a5 fix(firmware): track GPIO output levels for readback`
- 当前已有未提交修改，执行时必须保留并逐行审查，禁止 `reset --hard`/`clean`：
  - `backend/internal/nodemgr/handler_resources.go`
  - `backend/internal/nodemgr/handler_resources_test.go`
- 上述修改是一次未完成的 PWM ResourceReport 解码 tracer bullet；测试文件因 `gofmt` 带入了无业务意义的对齐噪声，实施时只恢复这些无关格式变化，保留新增测试和解码逻辑。

### 1.2 根因证据

1. `esp32-collector/components/hw_profile/hw_tables.c` 当前只枚举 `hw_gpios[]`，没有独立 `hw_pwms[]`。
2. `esp32-collector/components/hw_profile/hw_profile.c` 的 ResourceReport buses blob 只编码 UART/I2C/SPI/GPIO/ADC，不编码 PWM。
3. `backend/internal/nodemgr/handler_resources.go` 原先只解码 GPIO，不解码 PWM。
4. `frontend-shared/src/components/periph/PinResourceList.vue` 以 `hardwareGpio` 为基础，把 GPIOConfig 和 PWMConfig 合并成一个 pin 行。
5. `frontend-shared/src/components/node/ChannelPanel.vue` 的 `openPwmDialogFromRow()` 从 `hardware.gpio` 取资源；这把 GPIO 引脚错误地当成 PWM 资源。
6. `backend/internal/models/models.go` 的 PWMConfig 只有 `pin`，没有 PWM `hardware_id/channel`；API 路径也以 `:pin` 标识 PWM。
7. C6 芯片真实能力为 `SOC_LEDC_CHANNEL_NUM=6`、`SOC_LEDC_TIMER_NUM=4`；S3 为 8 通道、4 定时器。PWM 通道和 GPIO 引脚是两类硬件资源，LEDC 通道可以通过 GPIO matrix 路由到合法输出引脚。

### 1.3 不变约束

- GPIO/PWM 都不进入 Channel/Bus/DMA/Scheduler 系统。
- PWM 通道是硬件资源；GPIO pin 是 PWM 的输出路由参数。
- `hw_tables.c` 是节点硬件能力唯一事实源；后端不得提供任何硬编码 fallback。节点尚未上报 ResourceReport 时，API 返回空资源集并由前端显示“等待节点上报硬件资源”。
- 后端不得接受节点未报告的 GPIO/PWM 资源。
- GPIO 与 PWM 对同一物理 pin 互斥；两个 PWM 通道也不得绑定同一 pin。
- C6：6 个 PWM 通道、4 个共享 timer；S3：8 个 PWM 通道、4 个共享 timer。
- timer 由固件按频率/分辨率共享分配，不作为用户手工绑定资源。
- 开发验证只使用 `ehome-dev`、后端 8082、前端 5174、EMQX 1884；禁止操作 `/opt/EHomeSystem` 或任何生产 `ehome-*` 服务。
- 该功能仍在 `feat/gpio-control`，采用干净的新契约，不保留错误的“PWM 以 pin 为资源 ID”兼容接口。

---

## 2. 目标数据模型与协议契约

### 2.1 固件硬件表

在 `hw_tables.h/.c` 增加：

```c
typedef struct {
    const char *id;              /* "PWM0" ... */
    uint8_t channel;             /* LEDC channel index */
    uint8_t timer_count;         /* target-wide shared timer count */
    uint8_t max_resolution_bits; /* SOC_LEDC_TIMER_BIT_WIDTH */
} hw_pwm_t;
```

目标表：

```c
/* C6 */
#define HW_PWM_COUNT 6
const hw_pwm_t hw_pwms[HW_PWM_COUNT] = {
    { .id = "PWM0", .channel = 0, .timer_count = 4, .max_resolution_bits = 20 },
    /* ... PWM1-PWM5 ... */
};

/* S3 */
#define HW_PWM_COUNT 8
/* PWM0-PWM7, timer_count=4, max_resolution_bits=14 */
```

`HW_RESOURCE_COUNT` 必须包含 `HW_PWM_COUNT`。

### 2.2 ResourceReport

沿用 buses/resources 子消息，新增 field 6：

```text
Buses field 6: pwm_entry repeated
  sub-field 1: id                  string  (PWM0)
  sub-field 2: channel             varint
  sub-field 3: timer_count         varint
  sub-field 4: max_resolution_bits varint
```

GPIO 继续由 field 4 的 `gpio_entry` 独立上报。前后端不得通过 GPIO 数量推导 PWM 数量。

### 2.3 后端持久化配置

PWMConfig 改为：

```go
type PWMConfig struct {
    ID         uint      `gorm:"primaryKey" json:"id"`
    NodeID     string    `gorm:"column:node_id;type:varchar(32);index:idx_pwm_node_hw,unique;index:idx_pwm_node_pin,unique;not null" json:"node_id"`
    HardwareID string    `gorm:"column:hardware_id;size:16;index:idx_pwm_node_hw,unique;not null" json:"hardware_id"`
    Channel    uint8     `gorm:"not null" json:"channel"`
    Pin        int       `gorm:"index:idx_pwm_node_pin,unique;not null" json:"pin"`
    Frequency  uint32    `gorm:"not null" json:"frequency"`
    Duty       uint16    `gorm:"default:0" json:"duty"`
    Resolution uint8     `gorm:"default:14" json:"resolution"`
    AutoStart  bool      `gorm:"default:false" json:"auto_start"`
    Label      string    `gorm:"size:64" json:"label"`
    Enabled    bool      `gorm:"default:true" json:"enabled"`
    CreatedAt  time.Time `json:"created_at"`
    UpdatedAt  time.Time `json:"updated_at"`
}
```

唯一性：

- `(node_id, hardware_id)`：同一 PWM 通道只能有一个配置。
- `(node_id, pin)`：同一输出 pin 只能被一个 PWM 通道绑定。
- 另由 service/API 事务检查 GPIOConfig 与 PWMConfig 跨表 pin 冲突。

### 2.4 API

PWM 资源以 hardware ID 定位：

```text
GET    /api/v1/nodes/:id/pwm
POST   /api/v1/nodes/:id/pwm
PUT    /api/v1/nodes/:id/pwm/:hardware_id
DELETE /api/v1/nodes/:id/pwm/:hardware_id
POST   /api/v1/nodes/:id/pwm/:hardware_id/start
POST   /api/v1/nodes/:id/pwm/:hardware_id/stop
POST   /api/v1/nodes/:id/pwm/:hardware_id/duty
POST   /api/v1/nodes/:id/pwm/:hardware_id/freq
GET    /api/v1/nodes/:id/pwm/:hardware_id/state
```

POST 请求示例：

```json
{
  "hardware_id": "PWM0",
  "pin": 6,
  "frequency": 1500,
  "duty": 3000,
  "resolution": 14,
  "auto_start": false,
  "label": "风扇"
}
```

### 2.5 PeriphCmd/ConfigManifest

为 PWM 显式携带 channel 和 pin：

- PeriphCmd 公共 field 3 改名为 `resource_id`；GPIO 时值为 pin，PWM 时值为 LEDC channel。
- PWM START config：`[pin:1B, freq:4B LE, duty:2B LE, resolution:1B]`。
- PWM 的 DUTY/FREQ/READ/STOP 通过 channel 定位，不再通过 pin 查找。
- PeriphRsp field 6 同样表示 `resource_id`；WebSocket payload 同时返回 `hardware_id/channel/pin`（pin 可从配置关联得到）。
- ConfigManifest PWM 子消息采用：

```text
sub 1: channel
sub 2: pin
sub 3: frequency
sub 4: duty
sub 5: resolution
sub 6: auto_start
```

由于当前功能未发布，不保留错误字段布局；后端与固件必须在同一提交序列中升级，并重新 OTA。

---

## 3. 分步实施计划

### Task 1: 清理当前未完成 tracer bullet 的无关 diff

**Objective:** 保留已经完成的 PWM 解码 tracer bullet，只移除 `gofmt` 对旧测试注释对齐产生的无关变更。

**Files:**
- Modify: `backend/internal/nodemgr/handler_resources_test.go`
- Preserve: `backend/internal/nodemgr/handler_resources.go`

**Step 1: 审查当前 diff**

Run:

```bash
git diff -- backend/internal/nodemgr/handler_resources.go backend/internal/nodemgr/handler_resources_test.go
```

Expected: `decodePWMEntry`/`pwmEntry` 是业务修改；旧测试中的纯空格变化是噪声。

**Step 2: 用精确 patch 恢复无关格式行**

只恢复旧测试注释对齐，不使用 checkout 覆盖整个文件。

**Step 3: 运行 tracer bullet**

```bash
cd backend
go test ./internal/nodemgr -run TestDecodePWMEntry -count=1
```

Expected: PASS。

**Step 4: Commit**

暂不单独提交；与 Task 4 的完整后端 ResourceReport 解码一起提交。

---

### Task 2: 在 hw_tables 中建立独立 PWM 硬件资源表

**Objective:** 让 GPIO pin 与 PWM/LEDC channel 都由目标平台静态表明确枚举。

**Files:**
- Modify: `esp32-collector/components/hw_profile/include/hw_tables.h`
- Modify: `esp32-collector/components/hw_profile/hw_tables.c`
- Test/Create: `esp32-collector/components/hw_profile/test/test_hw_tables.c`（若组件 test app 不便接入，则使用编译期 `_Static_assert` 加 IDF 双目标构建）

**Step 1: 写失败测试/编译断言**

断言：

```c
#ifdef CONFIG_IDF_TARGET_ESP32C6
_Static_assert(HW_GPIO_COUNT == 8, "C6 GPIO resource count mismatch");
_Static_assert(HW_PWM_COUNT == 6, "C6 PWM resource count mismatch");
#elif defined(CONFIG_IDF_TARGET_ESP32S3)
_Static_assert(HW_GPIO_COUNT == 12, "S3 GPIO resource count mismatch");
_Static_assert(HW_PWM_COUNT == 8, "S3 PWM resource count mismatch");
#endif
```

Run:

```bash
source /home/sun/env/esp-idf/export.sh
idf.py build
```

Expected: FAIL — `HW_PWM_COUNT`/`hw_pwms` 尚未定义。

**Step 2: 添加 `hw_pwm_t`、`HW_PWM_COUNT` 和 extern**

在 `hw_tables.h` 加入目标结构和：

```c
extern const hw_pwm_t hw_pwms[HW_PWM_COUNT];
```

**Step 3: 分目标定义资源**

- C6：PWM0-PWM5、4 timers、20-bit max。
- S3：PWM0-PWM7、4 timers、14-bit max。
- `HW_RESOURCE_COUNT` 加 `HW_PWM_COUNT`。

**Step 4: 双目标构建**

```bash
idf.py set-target esp32c6 && idf.py build
idf.py set-target esp32s3 && idf.py build
```

Expected: 两个目标均成功；结束后恢复 worktree 原目标对应的 `sdkconfig`，不得删除两套 `sdkconfig.defaults.*`/partition 文件。

**Step 5: Commit**

```bash
git add esp32-collector/components/hw_profile/include/hw_tables.h \
        esp32-collector/components/hw_profile/hw_tables.c \
        esp32-collector/components/hw_profile/test/test_hw_tables.c
git commit -m "feat(firmware): define GPIO and PWM hardware resources"
```

---

### Task 3: 编码 PWM ResourceReport

**Objective:** 节点 ResourceReport 同时、独立上报 GPIO 和 PWM 能力。

**Files:**
- Modify: `esp32-collector/components/hw_profile/hw_profile.c`
- Modify: `docs/协议/二进制帧协议.md`
- Test: `backend/internal/nodemgr/handler_resources_test.go`（跨语言 wire 契约）

**Step 1: 扩展失败的 buses blob 测试**

在 `TestDecodeBusesBlob` 中加入 PWM 子帧 field 6，并断言 PWM0 的 channel/timer/max-resolution。先运行：

```bash
cd backend
go test ./internal/nodemgr -run TestDecodeBusesBlob -count=1
```

Expected: 在固件尚未上报前，后端合成测试可通过解码；下一步用实机/固件产物验证真实编码。

**Step 2: 添加 `encode_pwm_entry()`**

```c
static bool encode_pwm_entry(uint8_t *out, size_t cap, size_t *out_len,
                             const hw_pwm_t *p)
{
    frame_encoder_t enc;
    frame_encoder_init(&enc, out, cap, 0);
    if (frame_encode_string(&enc, 1, p->id) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 2, p->channel) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 3, p->timer_count) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 4, p->max_resolution_bits) != FRAME_OK) return false;
    *out_len = frame_encoder_size(&enc);
    return true;
}
```

**Step 3: 在 `build_buses_blob()` 编码 field 6**

遍历 `HW_PWM_COUNT`，不能遍历 GPIO 表或通过 GPIO 数量生成 PWM。

**Step 4: 更新协议文档**

补充 field 6 和每个 PWM 字段；将 GPIO/PWM 明确标为不同资源类型。

**Step 5: 构建验证**

```bash
cd esp32-collector
source /home/sun/env/esp-idf/export.sh
idf.py build
```

Expected: C6 build PASS，ResourceReport buffer 未溢出。

**Step 6: Commit**

```bash
git add esp32-collector/components/hw_profile/hw_profile.c docs/协议/二进制帧协议.md
git commit -m "feat(firmware): report PWM hardware resources"
```

---

### Task 4: 完成后端 PWM ResourceReport 解码和 API 透传

**Objective:** 后端保存节点真实 PWM 能力，并通过 capabilities/hardware API 原样返回。

**Files:**
- Modify: `backend/internal/nodemgr/handler_resources.go`
- Modify: `backend/internal/nodemgr/handler_resources_test.go`
- Modify: `backend/internal/api/handler_node.go`
- Test: `backend/internal/api/handler_test.go`

**Step 1: 完成 RED 用例**

覆盖：

- `decodePWMEntry`
- `decodeBusesBlob` field 6
- ResourceReport JSON 含 `buses.pwm`
- `/nodes/:id/capabilities` 返回 `pwm`
- 节点尚未上报时 `/nodes/:id/capabilities` 返回空 `buses`/空 capabilities，不生成任何平台默认资源

Run:

```bash
cd backend
go test ./internal/nodemgr ./internal/api -run 'PWM|Resource|Capabilities' -count=1
```

Expected: 空资源/API 测试先 FAIL（当前后端仍伪造默认 ESP32-C6 资源）。

**Step 2: 完成解码与日志**

日志改成：

```go
logger.Infof("[%s] ResourceReport decoded: %d uart, %d i2c, %d spi, %d gpio, %d pwm, %d adc, ...",
    deviceID, ..., len(buses.GPIO), len(buses.PWM), len(buses.ADC), ...)
```

**Step 3: 删除所有服务端硬件资源 fallback**

删除 `getDefaultESP32C6Buses()` 及其平台引脚/PWM 硬编码。节点 capabilities 为空或尚未收到 ResourceReport 时返回空资源集；前端必须显示“等待节点上报硬件资源”，不能猜测节点型号或资源。

**Step 4: fail-safe 类型解析**

`handler_node.go` 不得使用未经检查的：

```go
buses = b.(map[string]interface{})
```

改为 checked assertion，异常 capabilities 返回空资源集或明确错误，不能 panic，也不能回退到硬编码资源。

**Step 5: 验证 GREEN**

```bash
go test ./internal/nodemgr ./internal/api -count=1
go test ./... -count=1
```

Expected: 全部 PASS。

**Step 6: Commit**

```bash
git add backend/internal/nodemgr/handler_resources.go \
        backend/internal/nodemgr/handler_resources_test.go \
        backend/internal/api/handler_node.go \
        backend/internal/api/handler_test.go
git commit -m "feat(backend): expose reported PWM hardware resources"
```

---

### Task 5: 把 PWMConfig 主键语义从 pin 改为 PWM hardware resource

**Objective:** PWM0/PWM1 等硬件通道成为配置身份，pin 仅是输出路由。

**Files:**
- Modify: `backend/internal/models/models.go`
- Modify: `backend/internal/database/gorm.go`
- Modify: `backend/internal/database/migrate_gpio.go`（建议重命名为 `migrate_peripherals.go`，若重命名需同步测试）
- Modify: `backend/internal/testutil/db.go`
- Modify: `backend/internal/nodemgr/manager.go`
- Modify: `backend/internal/nodemgr/sender.go`
- Test: `backend/internal/database/migrate_gpio_test.go`
- Test: `backend/internal/nodemgr/*_test.go`

**Step 1: 写模型/迁移失败测试**

断言：

- PWMConfig 必须有 `hardware_id` 和 `channel`。
- `(node, hardware_id)` 重复失败。
- `(node, pin)` 重复失败。
- GPIO/PWM 同 pin 由 API/service 拒绝。
- 旧开发表非空时迁移不得静默猜测 channel。

**Step 2: 实施干净迁移**

由于错误模型尚未发布：

- 在开发/测试迁移中重建 `pwm_configs` 正确索引。
- 如果检测到旧 PWM 行，迁移标记 `migration_required` 并输出行 ID/pin；不自动按顺序映射 PWM0，避免制造错误硬件归属。
- 本次 C6 开发数据库中的临时 PWM 行此前已删除，可安全迁移。

**Step 3: 更新 config hash**

PWM hash 必须包含：

```text
hardware_id + channel + pin + frequency + duty + resolution + auto_start + enabled
```

**Step 4: 更新 ConfigManifest 编码**

使用新 sub-field 1-6 布局；顺序按 channel/hardware_id 稳定排序。

**Step 5: 测试**

```bash
go test ./internal/database ./internal/models ./internal/nodemgr -count=1
go test ./... -count=1
```

**Step 6: Commit**

```bash
git add backend/internal/models backend/internal/database backend/internal/testutil \
        backend/internal/nodemgr
git commit -m "refactor(backend): bind PWM configs to hardware channels"
```

---

### Task 6: 修改 PeriphCmd/PeriphRsp 与固件 PWM 控制器为 channel 定位

**Objective:** PWM 命令操作真实 LEDC channel；GPIO 命令仍操作 GPIO pin。

**Files:**
- Modify: `backend/internal/api/handler_periph.go`
- Modify: `backend/internal/nodemgr/sender.go`
- Modify: `backend/internal/nodemgr/handler_response.go`
- Modify: `esp32-collector/components/msg_handler/include/msg_handler_internal.h`
- Modify: `esp32-collector/components/msg_handler/handler_periph.c`
- Modify: `esp32-collector/components/pwm_ctrl/include/pwm_ctrl.h`
- Modify: `esp32-collector/components/pwm_ctrl/pwm_ctrl.c`
- Modify: `esp32-collector/components/config_mgr/include/config_mgr.h`
- Modify: `esp32-collector/components/config_mgr/config_mgr.c`
- Test: corresponding backend API/nodemgr tests
- Test/Create: PWM controller component tests where feasible

**Step 1: 写 API 失败测试**

覆盖：

- `POST /pwm` 缺 hardware_id → 400。
- hardware_id 不在 Node `buses.pwm` → 422。
- pin 不在 Node `buses.gpio` → 422。
- GPIOConfig 已占 pin → 409。
- 另一个 PWM 已占 pin → 409。
- `/pwm/PWM0/start` 发出 resource_id=channel 0，config 中 pin 正确。

**Step 2: 改 API 路径和查询键**

所有 PWM CRUD/control 以 `hardware_id` 查询；响应保留 channel 和 pin。

**Step 3: 改 wire 编码/解码**

- GPIO：resource_id=pin。
- PWM：resource_id=channel。
- PWM START config 首字节为 pin。
- ConfigManifest 解析保存 channel+pin。

**Step 4: 改 `pwm_ctrl` API**

建议接口：

```c
esp_err_t pwm_ctrl_start(uint8_t channel, int pin, uint32_t freq,
                         uint16_t duty, uint8_t resolution);
esp_err_t pwm_ctrl_stop(uint8_t channel);
esp_err_t pwm_ctrl_set_duty(uint8_t channel, uint16_t duty);
esp_err_t pwm_ctrl_set_freq(uint8_t channel, uint32_t freq, uint8_t resolution);
uint16_t pwm_ctrl_get_duty(uint8_t channel);
```

- channel 必须存在于 `hw_pwms[]`。
- 不再动态 `find_free_channel()`；使用资源指定 channel。
- timer 仍按 freq+resolution 共享分配。
- pin 冲突检查必须验证 `hw_gpios[]`，不能仅用 `0 <= pin < 30`。

**Step 5: 修正 GPIO 合法性校验**

`gpio_ctrl.is_valid_pin()` 同样改为查询 `hw_gpios[]`，确保 API/固件只能操作硬件表已报告 GPIO，而不是任意数字范围。

**Step 6: 测试和构建**

```bash
cd backend
go test ./internal/api ./internal/nodemgr -count=1
cd ../esp32-collector
source /home/sun/env/esp-idf/export.sh
idf.py build
```

**Step 7: Commit**

```bash
git add backend/internal/api/handler_periph.go backend/internal/nodemgr \
        esp32-collector/components/msg_handler \
        esp32-collector/components/pwm_ctrl \
        esp32-collector/components/gpio_ctrl \
        esp32-collector/components/config_mgr
git commit -m "refactor(periph): address PWM by hardware channel"
```

---

### Task 7: 前端类型/API 改为独立 GPIO/PWM 资源

**Objective:** 前端数据契约不再把 PWM 能力投影为 GPIO pin。

**Files:**
- Modify: `frontend-shared/src/api/node.ts`
- Modify: `frontend-shared/src/api/periph.ts`
- Test: `frontend-shared/src/api/__tests__/periph.spec.ts`

**Step 1: 写失败类型/API 测试**

新增：

```ts
export interface PWMHardwareResource {
  id: string
  channel: number
  timer_count: number
  max_resolution_bits: number
}
```

`Capabilities.buses.pwm` 必须是该类型。PWMConfig 必须包含 `hardware_id`、`channel`、`pin`。

**Step 2: 修改 API 方法签名**

```ts
start(nodeId: string, hardwareId: string)
stop(nodeId: string, hardwareId: string)
setDuty(nodeId: string, hardwareId: string, duty: number)
```

**Step 3: 运行测试/typecheck**

```bash
pnpm exec vitest run src/api/__tests__/periph.spec.ts
pnpm typecheck
```

Expected: API 测试 PASS；typecheck 会列出所有尚待迁移的旧 pin 调用点，作为 Task 8 输入。

**Step 4: Commit**

与 Task 8 一起提交，避免中间提交长期处于前端不可构建状态。

---

### Task 8: 将统一 PinResourceList 拆为独立 GPIO/PWM 资源视图

**Objective:** UI 分别展示真实 GPIO 资源和真实 PWM 通道资源；PWM 行显示“PWM0 → GPIO6”映射，而不是把 GPIO6 显示成 PWM 资源。

**Files:**
- Create: `frontend-shared/src/components/periph/GPIOResourceList.vue`
- Create: `frontend-shared/src/components/periph/PWMResourceList.vue`
- Modify: `frontend-shared/src/components/periph/PeripheralControl.vue`
- Modify: `frontend-shared/src/components/node/ChannelPanel.vue`
- Remove after migration: `frontend-shared/src/components/periph/PinResourceList.vue`
- Modify/remove tests: `frontend-shared/src/components/periph/__tests__/PinResourceList.spec.ts`
- Create tests:
  - `frontend-shared/src/components/periph/__tests__/GPIOResourceList.spec.ts`
  - `frontend-shared/src/components/periph/__tests__/PWMResourceList.spec.ts`
  - update `PeripheralControl.spec.ts`
  - update ChannelPanel tests

**Step 1: 写 GPIOResourceList RED 测试**

断言：

- 只从 `hardwareGpio` 生成 GPIO 行。
- 已被 PWM pin 占用时显示 `PWM0 占用`，不可配置 GPIO。
- GPIO config 不在硬件表时显示“无效配置”，不能伪造成正常资源。

**Step 2: 最小实现 GPIOResourceList**

保留现有 GPIO 控制交互，但移除所有 PWM 分支。

**Step 3: 写 PWMResourceList RED 测试**

断言：

- `hardwarePwm=[PWM0..PWM5]` 必须渲染 6 行，与 GPIO 数量无关。
- 未配置 PWM 通道显示“未配置”。
- 已配置显示 `PWM0 → GPIO6`、频率、分辨率、duty、状态。
- 配置动作 emit `configure-pwm('PWM0', channel)`，不是 pin。
- 输出 pin 由对话框从“未被 GPIO/PWM/总线占用的 GPIO 资源”中选择。

**Step 4: 最小实现 PWMResourceList**

PWM 行的 identity 为 `PWM n`；GPIO pin 只在 routing/configuration 区显示。

**Step 5: 修改 ChannelPanel**

- `refreshBuses()` 类型循环加入 `pwm`。
- 删除 `openPwmDialogFromRow(pin)` 从 `hardware.gpio` 取资源的逻辑。
- PWM 对话框字段：只读 hardware_id/channel + GPIO pin select。
- 汇总分别显示：GPIO N 个引脚、PWM M 个通道。
- 不再显示“共 GPIO N · 已配置 GPIO+PWM”。

**Step 6: 修改 PeripheralControl**

新增 `hardwareGpio`/`hardwarePwm` props 或由父层传入真实 capabilities；没有硬件能力时只能显示配置为 stale/invalid，不得伪造资源。

**Step 7: 删除旧统一组件和过时规格**

- 代码完全迁移后删除 `PinResourceList.vue`。
- 更新 `docs/设计/GPIO_PWM_UI重设计规格.md`：删除“每个物理 pin 只出现一次并同时代表 PWM 资源”的错误规则，改成两套资源列表及 routing 关系。

**Step 8: 前端验证**

```bash
pnpm exec vitest run \
  src/components/periph/__tests__/GPIOResourceList.spec.ts \
  src/components/periph/__tests__/PWMResourceList.spec.ts \
  src/components/periph/__tests__/PeripheralControl.spec.ts
pnpm typecheck
pnpm build
```

Expected: 全部 PASS；无 `PinResourceList` 引用残留。

**Step 9: Commit**

```bash
git add frontend-shared/src/api frontend-shared/src/components/periph \
        frontend-shared/src/components/node/ChannelPanel.vue \
        frontend-shared/src/components/node/__tests__ \
        docs/设计/GPIO_PWM_UI重设计规格.md
git commit -m "refactor(frontend): separate GPIO pins from PWM channels"
```

---

### Task 9: 更新设计文档与 skill，消除错误架构描述

**Objective:** 代码、协议、设计文档和可复用技能对资源模型保持一致。

**Files:**
- Modify: `docs/设计/GPIO控制重构设计.md`
- Modify: `docs/设计/GPIO_PWM_UI重设计规格.md`
- Modify: `docs/协议/二进制帧协议.md`
- Patch skill after implementation verification: `ehome-system/references/gpio-redesign.md` via `skill_manage`

**Step 1: 更新设计**

明确：

- GPIO = pin resource。
- PWM = LEDC channel resource。
- PWM channel → GPIO pin 是配置关系。
- timer 是共享内部资源。
- API/DB/protocol 都以 hardware_id/channel 定位 PWM。

**Step 2: 扫描错误描述**

```bash
rg -n "复用 GPIO 引脚列表|每个物理 pin 只出现一次|PWM.*:pin|PWM GPIO" docs frontend-shared backend esp32-collector
```

Expected: 仅保留合理的“PWM 输出路由 pin”描述，不再把 pin 当 PWM resource ID。

**Step 3: Commit**

```bash
git add docs
git commit -m "docs: correct GPIO and PWM hardware resource model"
```

---

### Task 10: 全量软件门禁与独立 fail-closed 复审

**Objective:** 在接触硬件前证明 tracked+untracked 代码无软件回归。

**Files:** all modified files

**Step 1: 后端全量门禁**

```bash
cd backend
go test ./... -count=1
go vet ./...
```

**Step 2: 前端全量门禁**

```bash
cd frontend-shared
pnpm exec vitest run --maxWorkers=2
pnpm typecheck
pnpm build
```

**Step 3: 固件双目标门禁**

```bash
cd esp32-collector
source /home/sun/env/esp-idf/export.sh
idf.py set-target esp32c6 && idf.py build
idf.py set-target esp32s3 && idf.py build
```

恢复目标配置并确认两套 defaults/partitions 均保留。

**Step 4: 静态门禁**

```bash
git diff --check
git status --short
```

禁止提交 `build/`、coverage、临时固件、日志、token。

**Step 5: 并行独立复审**

用 2 个 subagent（当前并发上限）分别审查：

1. 固件+协议：hw_tables 事实源、channel/pin、timer、ResourceReport、ConfigManifest、PeriphCmd。
2. 后端+前端：模型/迁移/API fail-closed、独立资源列表、冲突逻辑、测试反模式。

要求最终 diff `APPROVED`；若复审后源码变化，重新跑全量门禁并重新复审。

---

### Task 11: 开发数据库迁移和真实 ResourceReport 验证

**Objective:** 只在隔离开发环境应用新 schema，并确认 DB 能力来自 C6 实际上报。

**Files:** no production files beyond migration code

**Step 1: 确认隔离环境**

必须检查：PostgreSQL 5435、Redis 6380、EMQX 1884、后端 8082、前端 5174。禁止触碰生产。

**Step 2: 应用迁移**

在 dev DB 执行应用启动/显式迁移；查询 `pwm_configs` schema 和唯一索引。

**Step 3: 启动新后端并 QueryResources**

后端必须从 GPIO worktree 启动。下发 QueryResources 后查询节点 capabilities：

Expected C6：

```text
gpio: GPIO0..GPIO7 (8)
pwm:  PWM0..PWM5  (6)
```

不得出现“PWM 8 个（按 GPIO 数量生成）”。

**Step 4: 验证 API fail-closed**

- 创建 PWM6 → 拒绝。
- PWM0 pin=99 → 拒绝。
- PWM0 pin=GPIO0（已有 GPIO 配置）→ 409。
- PWM0 pin=GPIO6 → 成功。
- PWM1 再绑定 GPIO6 → 409。

---

### Task 12: OTA、C6 实机和浏览器 E2E

**Objective:** 用真实 C6 证明资源上报、PWM channel routing 和 GPIO 控制闭环正确。

**Files:** no source mutation except bug fixes discovered through TDD

**Step 1: 构建开发固件**

临时把 MQTT 指向开发 EMQX 1884，构建后立刻恢复源码配置；确认 `git diff` 不含 broker 改动。

**Step 2: OTA 固件**

沿已验证 OTA 路径上传并升级。必须记录实际 HTTP/OTA/Hello 输出，不手工伪造 OTA success。

**Step 3: ResourceReport 实机验证**

串口、后端日志、DB、API 四处一致：C6 8 GPIO + 6 PWM。

**Step 4: GPIO E2E**

选择硬件表中的安全 GPIO6：CONFIG → LOW → READ 0 → HIGH → READ 1 → TOGGLE → READ 0；记录 PeriphCmd/PeriphRsp request_id。

**Step 5: PWM E2E**

配置 `PWM0 → GPIO6`：START 1500Hz/30% → READ → DUTY → FREQ → STOP。

- 固件日志必须显示指定 LEDC channel=0，不是动态“第一个空闲 channel”。
- 如有逻辑分析仪/示波器，测 GPIO6 的实际频率/占空比。
- 若无物理测量设备，只能报告协议/驱动闭环通过，物理波形标记为未测，不能写 PASS。

**Step 6: 浏览器 CDP 验收**

页面必须显示：

- GPIO 资源：8。
- PWM 资源：6。
- PWM0 行显示路由 GPIO6。
- PWM1-PWM5 仍是独立未配置资源。
- 不再出现“每个 GPIO 行都有启用 PWM”按钮。
- 配置/启停/刷新后状态一致，无 console error。

**Step 7: 清理**

删除 E2E 临时 PWM/GPIO 配置，恢复原 GPIO0，确认 manifest hash 回归正确状态；删除 dev firmware DB/file 测试记录（仅 dev）；源码工作区干净。

**Step 8: 最终提交与报告**

按功能域保留上述小提交；最终报告区分：

- 软件门禁。
- ResourceReport 实机能力。
- API/MQTT/串口闭环。
- 浏览器验收。
- 物理波形测量（有/无）。

---

## 4. 预计修改文件汇总

### Firmware

- `esp32-collector/components/hw_profile/include/hw_tables.h`
- `esp32-collector/components/hw_profile/hw_tables.c`
- `esp32-collector/components/hw_profile/hw_profile.c`
- `esp32-collector/components/pwm_ctrl/include/pwm_ctrl.h`
- `esp32-collector/components/pwm_ctrl/pwm_ctrl.c`
- `esp32-collector/components/gpio_ctrl/gpio_ctrl.c`
- `esp32-collector/components/msg_handler/handler_periph.c`
- `esp32-collector/components/msg_handler/include/msg_handler_internal.h`
- `esp32-collector/components/config_mgr/include/config_mgr.h`
- `esp32-collector/components/config_mgr/config_mgr.c`

### Backend

- `backend/internal/nodemgr/handler_resources.go`
- `backend/internal/nodemgr/handler_resources_test.go`
- `backend/internal/nodemgr/sender.go`
- `backend/internal/nodemgr/handler_response.go`
- `backend/internal/nodemgr/manager.go`
- `backend/internal/api/handler_node.go`
- `backend/internal/api/handler_periph.go`
- `backend/internal/api/handler_test.go`
- `backend/internal/models/models.go`
- `backend/internal/database/migrate_gpio.go` or renamed migration file
- relevant database/nodemgr/API tests

### Frontend

- `frontend-shared/src/api/node.ts`
- `frontend-shared/src/api/periph.ts`
- `frontend-shared/src/components/node/ChannelPanel.vue`
- `frontend-shared/src/components/periph/PeripheralControl.vue`
- Create `frontend-shared/src/components/periph/GPIOResourceList.vue`
- Create `frontend-shared/src/components/periph/PWMResourceList.vue`
- Remove `frontend-shared/src/components/periph/PinResourceList.vue` after migration
- corresponding Vitest files

### Docs/Skill

- `docs/设计/GPIO控制重构设计.md`
- `docs/设计/GPIO_PWM_UI重设计规格.md`
- `docs/协议/二进制帧协议.md`
- `ehome-system` skill `references/gpio-redesign.md`（实现验证后通过 skill tool patch）

---

## 5. 风险与控制

| 风险 | 控制 |
|---|---|
| 改 wire 布局导致旧固件不兼容 | 功能未发布，后端与固件原子升级；OTA 前不启用新配置；明确版本门槛 |
| PWM channel 与 timer 混淆 | 用户选择 channel；timer 只由固件共享池管理 |
| GPIO/PWM 跨表 pin 冲突 | API 事务 fail-closed + DB 各表唯一索引 + 固件二次校验 |
| 服务端伪造硬件资源 | 删除所有平台 fallback；空/异常 capabilities 返回空资源或明确错误；API 测试禁止出现未上报资源 |
| S3/C6 能力不同 | 两套表分别定义；双目标编译；不使用 C6 常量硬编码到共用控制器 |
| ResourceReport buffer 增长 | 构建时检查编码返回值；实机抓包确认完整 6 PWM 条目 |
| 现有未提交修改被覆盖 | 禁止 reset/checkout 整文件；Task 1 精确 patch 并先保存 diff |
| UI 再次把 route pin 当 resource | 组件拆分；PWM 测试按 hardwarePwm 数量断言，与 hardwareGpio 数量故意设置不同 |
| 只验证软件回包未验证硬件 | 串口+MQTT+API+浏览器；有测量设备时追加波形，缺设备明确标未测 |

## 6. 最终验收标准

- [ ] `hw_tables.c` 分别存在 `hw_gpios[]` 和 `hw_pwms[]`。
- [ ] C6 ResourceReport 上报 8 GPIO、6 PWM；S3 静态定义 12 GPIO、8 PWM。
- [ ] `HW_RESOURCE_COUNT` 包含 PWM。
- [ ] 后端 capabilities/hardware API 返回独立 `buses.gpio` 与 `buses.pwm`。
- [ ] PWMConfig 以 hardware_id/channel 为资源身份，pin 仅为路由。
- [ ] GPIO/PWM/总线 pin 冲突均 fail-closed。
- [ ] PeriphCmd PWM 通过 channel 定位，START 明确携带 pin。
- [ ] 前端分别显示 GPIO 和 PWM 两套资源；PWM 数量不再由 GPIO 数量推导。
- [ ] C6 实机日志显示 `PWM0/channel0 → GPIO6`，不是动态空闲通道。
- [ ] 后端、前端、C6/S3 固件全量门禁通过。
- [ ] 最新 diff 获得独立 fail-closed APPROVED。
- [ ] 开发临时配置清理，源码 broker 配置恢复，工作区无构建产物。
