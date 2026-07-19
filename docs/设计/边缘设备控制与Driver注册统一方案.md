# 边缘设备控制与 Driver 注册统一方案

> **文档性质**：完整设计方案，三文档统筹。仅定义架构、协议、模型、接口、修复方案与验收标准，不代表授权实现。
>
> **统筹来源**：
> 1. `边缘设备控制架构设计.md` — 边缘设备控制架构（主文档）
> 2. `docs/后端Driver注册单一化与Techfine元数据修复方案.md` — Driver 注册修复
> 3. `嘉佰达软件板通用协议20220509.pdf_by_PaddleOCR-VL-1.6.md` — 嘉佰达 V19 协议 OCR 来源；当前仓库未包含该文件，Phase 0-B 必须归档原 PDF/OCR、来源和校验摘要后才能作为可复核协议证据
>
> **适用项目**：EHomeSystem
>
> **设计基线**：`origin/master@9a20c63`（2026-07-19，包含 `100609c`、`9a20c63` 两项最新修复）；当前实现分支已合入该基线，合并提交为 `72cb38e`。
>
> **重新评估状态**：已按上述 master 代码重新核对。架构方向保留，但实施前必须采用本文修订后的摘要分层、状态机、旧写路径清单、ResourceReport 持久化字段和安全门禁；旧版本中的同名描述不再有效。
>
> **配套开发计划**：`docs/设计/边缘设备控制统一方案开发计划.md`。
>
> **规范优先级**：本统一方案是控制面的唯一规范入口；与被引用的历史设计文档冲突时，以本文件为准。历史文档只作为背景和证据，不再作为接口字段定义来源。
>
> **资源原则**：后端承担业务编排、厂商协议解析、权限、持久化和验证；ESP32 只实现有界、静态内存优先的 Channel 字节事务执行，不实现通用工作流语言、JSON 规则引擎或厂商协议解释器。

## 当前 master 重新评估摘要

当前代码不是原方案假设的完全未治理状态，而是“部分基础修复已完成，业务控制闭环仍未建立”：

- `RegisterBuiltInDrivers` 已转发 parser-aware 入口，测试和生产注册集合均为 7 个 Driver；Techfine `HardwareTypes` 已是 `uart`；
- 生产启动构造一个注入 Registry，tree/test-parser、nodemgr、databus 和 API 热路径均使用该实例；包级 GlobalRegistry 仅保留兼容定义，不在生产热路径使用；
- `PendingWrite` 已有请求关联、数据库记录、QoS2 发布和 read response 等待，但持久化失败只告警，恢复不重发且无法恢复原调用者，不能替代 CommandExecution/Outbox；
- ResourceReport 已有严格的顶层字段检查，但没有 boot ID、report timestamp、command engine revision/capabilities 的持久化事实；
- `/execute` write、change-address、raw channel write、REST/WS terminal write 和内部 device initialization 仍使用 legacy WriteCmd；
- legacy 固件仍会截断超长 TX、把非法 bus 回退到 UART0，UART RX timeout 固定为 1000ms；
- 当前后端、前端和既有 ESP32 host tests 全绿，但 host tests 尚未覆盖 WriteCmd strict decode、TX 超长拒绝、非法 bus 拒绝和控制 replay。

因此允许立即实施的第一批工作只有：基线修正、Registry 单一化/依赖注入、legacy 控制安全止血、真实 Wire Spec/黄金向量和 ResourceReport/性能基线。任何业务写动作继续保持关闭。

## 重新审核结论

**结论：APPROVED，已在项目目录的持久实现分支 `codex/edge-control-fixes` 落地 Phase 0-A/Phase 1 的安全修复与只读闭环加固；后续高风险动作仍须逐项门禁。**

本次审核确认：

- 设计边界与当前 master 的 Node/Channel/EdgeDevice/Driver/PeriphCmd 分层一致；
- request hash 与 wire digest、传输重投与物理 attempt 已明确分离；
- 单步状态机只保留协议可证明的状态，UNKNOWN、取消和重启语义无冲突；
- 所有当前物理 TX 入口均进入迁移清单，raw diagnostics 和内部控制边界明确；
- ResourceReport 的 boot、freshness 和 revision 已有明确协议及数据库落点；
- high/critical 动作的 topic 隔离、安全确认、实机证据和 NVS 门禁顺序一致；
- Phase 0-A 不依赖嘉佰达实机资料，也不会开放业务写动作，可安全独立交付。

该 APPROVED 只授权开始 Phase 0-A 工程实现，不代表后续 setter、BMS、清零或 reboot/reset 已通过上线审核。每个后续阶段仍需满足自己的门禁。

---

## 目录

1. [背景与问题](#1-背景与问题)
2. [设计目标与非目标](#2-设计目标与非目标)
3. [现有架构审查](#3-现有架构审查)
4. [架构边界与不变量](#4-架构边界与不变量)
5. [总体目标架构](#5-总体目标架构)
6. [统一设备动作模型](#6-统一设备动作模型)
7. [操作实例与状态数据模型](#7-操作实例与状态数据模型)
8. [命令执行状态机](#8-命令执行状态机)
9. [成功语义、错误模型与超时](#9-成功语义错误模型与超时)
10. [幂等、重放与结果不确定性](#10-幂等重放与结果不确定性)
11. [Channel 调度、排他与有界批次](#11-channel-调度排他与有界批次)
12. [协议设计](#12-协议设计)
13. [能力声明与 ResourceReport](#13-能力声明与-resourcereport)
14. [后端架构设计](#14-后端架构设计)
15. [REST 与 WebSocket API](#15-rest-与-websocket-api)
16. [前端架构与交互设计](#16-前端架构与交互设计)
17. [安全、权限与审计](#17-安全权限与审计)
18. [典型设备场景](#18-典型设备场景)
19. [可观测性](#19-可观测性)
20. [兼容与迁移策略](#20-兼容与迁移策略)
21. [测试与验证方案](#21-测试与验证方案)
22. [分阶段实施计划](#22-分阶段实施计划)
23. [验收标准](#23-验收标准)
24. [风险、决策与禁止事项](#24-风险决策与禁止事项)
25. [Driver 注册修复（阶段 0）](#25-driver-注册修复阶段-0)
26. [嘉佰达协议对照与修正](#26-嘉佰达协议对照与修正)
27. [代码证据索引](#27-代码证据索引)

---

# 1. 背景与问题

当前 EHomeSystem 的边缘设备前端主要用于展示采集数据。系统已经可以通过 ESP32 节点与下游设备进行字节级通信，也已经存在部分操作定义和前端按钮，但尚未形成一个可靠、通用、可审计的设备控制面。

需要支持的代表性能力包括：

- 雨量计累计值重置；
- BMS 重启；
- BMS 充电 MOS、放电 MOS 控制；
- BMS 保护参数和运行参数设置；
- 逆变器开机、关机、复位；
- 逆变器工作模式、输出电压、输出频率、充电电流、电池类型等设置；
- 其他设备的瞬时命令、目标状态控制、数值设置、枚举设置和多步骤配置。

这些能力表面上是"增加几个按钮"，实际包含完整的分布式控制问题：

1. 浏览器、后端、MQTT、ESP32、总线和目标设备之间存在多层异步边界；
2. MQTT 发布成功不等于 ESP32 接受；
3. ESP32 总线发送成功不等于目标设备接受；
4. 目标设备 ACK 成功也不一定等于设置已持久化或运行状态已改变；
5. 超时后命令可能已执行，也可能未执行；
6. 非幂等命令盲目重试可能产生二次破坏；
7. 控制命令可能与周期采集、初始化、终端命令交错；
8. 高风险操作需要权限、确认、审计和可追溯结果；
9. 不同设备协议差异很大，但前后端控制架构必须通用。

此外，后端 Driver 注册存在多项基础问题（双 Registry 分裂、元数据错误、测试与生产不一致），必须在控制架构实施前修复。

---

# 2. 设计目标与非目标

## 2.1 设计目标

### G1：统一能力模型

以一个版本化的设备动作定义描述：

- 动作是什么；
- 接受哪些参数；
- 风险等级；
- 幂等性质；
- 前置条件；
- 如何编译为设备协议帧；
- 如何解析 ACK；
- 如何验证最终状态；
- 前端应使用什么受控组件展示。

### G2：端到端可靠状态

把一次操作建模为持久化实体，明确区分：

- 请求已创建；
- 后端已接受；
- 已排队；
- MQTT 已分发；
- ESP32 已接受；
- 总线事务已执行；
- 目标协议已确认；
- 最终业务状态已验证；
- 明确失败；
- 结果未知。

### G3：通用而非设备硬编码

新增设备型号时：

- 不新增设备专用 REST 控制接口；
- 不新增设备专用 MQTT 消息类型；
- 不在通用前端增加型号分支；
- 只增加动作定义、必要的协议编译器/解析器和设备专用可视化。

### G4：安全可控

- 后端执行权威参数校验；
- 高风险操作强确认；
- 支持操作理由、近期登录或密码再确认；
- 记录 append-only 审计；
- 不允许浏览器提交任意原始命令帧。

### G5：设备端幂等和重放保护

- 相同命令 ID 和相同 payload 不重复执行；
- 相同命令 ID 和不同 payload 拒绝 collision；
- 过期命令不执行；
- 对高风险、at-most-once 命令保留跨重启的重放证据。

### G6：保持现有正确边界

- Channel 处理有协议帧的下游设备；
- GPIO/PWM 继续使用独立 PeriphCmd；
- ResourceReport 是节点实际硬件和执行引擎能力的唯一事实源；
- Driver 只负责设备协议知识，不负责 MQTT、权限、持久化和业务状态机。

### G7：Driver 注册单一化

- 生产运行路径只使用一个已注册的 `*drivers.Registry` 实例；测试可创建隔离 Registry；
- 测试环境与生产环境 Driver 注册一致；
- 修复已知元数据错误（Techfine/LK-TH01）。

### G8：协议与实现一致

- 所有 Driver 命令定义必须对照设备厂商协议文档验证；
- 架构设计方案中的协议假设必须与实际协议一致；
- 现有 Driver 代码的协议假设必须与协议文档对齐。

## 2.2 非目标

本设计不做以下事情：

1. 不把设备厂商协议解析下沉到 ESP32；
2. 不让 ESP32 理解"BMS""雨量计""逆变器"等业务概念；
3. 不把 GPIO/PWM 重新并入 Channel；
4. 不承诺分布式系统中的绝对 exactly-once；
5. 不用 QoS2 代替业务幂等和设备端去重；
6. 不允许后端下发 HTML、JavaScript、Vue 模板或任意动态组件路径；
7. 不为设备型号硬编码 ESP32 硬件资源；
8. 不在 ESP32 上实现厂商协议解析、动态工作流、脚本或表达式；
9. 不为每个控制命令创建 task、写 NVS 或发布逐 step MQTT 事件；
10. 不要求 PSRAM，不把服务器侧状态机完整复制到 ESP32；
11. 不在本设计阶段实现代码。

---

# 3. 现有架构审查

## 3.1 已具备的基础

### 3.1.1 领域层次已经存在

当前模型包含：

- `Node`：物理 ESP32 节点；
- `Channel`：UART/I2C/SPI 等总线端点；
- `DeviceConfig`：设备型号定义；
- `EdgeDevice`：Node、Channel、DeviceConfig 的实例化。

证据：

- `backend/internal/models/models.go:15-67`
- `backend/internal/models/models.go:74-107`
- `backend/internal/models/models.go:114-135`
- `backend/internal/models/models.go:154-185`

`Channel` 已明确排除 GPIO/PWM：

- `backend/internal/models/models.go:121-124`
- `backend/internal/api/handler_device.go:34-50`

### 3.1.2 Channel 透明字节事务已存在

当前实际 WriteCmd 字段为：

```text
field 1: request_id
field 2: channel_id
field 3: data
field 4: read_size
```

后端编码：

- `backend/internal/pendingwrite/pendingwrite.go:87-98`
- `backend/internal/nodemgr/sender.go:173-190`

ESP32 解码与分发：

- `esp32-collector/components/msg_handler/handler_writecmd.c:36-65`
- `esp32-collector/components/bus_manager/bus_manager.c:347-385`

总线执行：

- UART：`esp32-collector/components/bus_worker/bus_worker.c:161-193`
- SPI/I2C：`esp32-collector/components/bus_worker/bus_worker.c:250-283`

后端可以通过 request_id 等待 DataReport 读回：

- `backend/internal/pendingwrite/pendingwrite.go:150-226`
- `backend/internal/databus/consumers_heavy.go:41-54`

### 3.1.3 DeviceConfig 已有 Operations 字段

`DeviceConfig.Operations` 已可保存型号级操作定义：

- `backend/internal/models/models.go:172-175`

`POST /edge-devices/:id/execute` 已能：

- 加载 EdgeDevice 和 DeviceConfig；
- 查找操作；
- 渲染模板和 CRC；
- 对 read 操作等待响应；
- 解析部分响应。

证据：

- `backend/internal/api/handler_edge_device.go:569-681`
- `backend/internal/api/handler_edge_device.go:761-856`
- `backend/pkg/parser/template.go:24-49`
- `backend/pkg/parser/template.go:66-176`

### 3.1.4 Driver 已包含大量命令知识

BMS 已声明 MOS 命令：

- `backend/internal/drivers/jiabaida.go:622-638`

逆变器已声明：

- 查询命令：`backend/internal/drivers/inverter_techfine.go:787-874`
- 开机：`backend/internal/drivers/inverter_techfine.go:876-884`
- 输出电压/频率/电池类型/工作模式等：`backend/internal/drivers/inverter_techfine.go:886-978`
- 充电参数和系统复位：`backend/internal/drivers/inverter_techfine.go:981-1002`

### 3.1.5 前端已有动态操作原型

`OperationButtons.vue` 已支持：

- 动作按钮；
- 数值输入；
- 枚举输入；
- 简单字符串输入；
- 基于设备状态的禁用；
- 路由代际保护。

证据：

- `frontend-shared/src/views/edge-device/shared/OperationButtons.vue:1-78`
- `frontend-shared/src/views/edge-device/shared/OperationButtons.vue:97-159`
- `frontend-shared/src/views/edge-device/shared/OperationButtons.vue:163-241`

WebSocket store 已支持按事件类型订阅和重连：

- `frontend-shared/src/stores/websocket.ts:77-113`
- `frontend-shared/src/stores/websocket.ts:116-209`

## 3.2 当前主要缺口

### 3.2.1 Driver 命令与 DeviceConfig Operations 双轨

Driver 的 `CommandTemplate` 描述底层协议命令：

- `backend/internal/drivers/command_template.go:3-26`

但 `/execute` 只读取 `DeviceConfig.Operations`：

- `backend/internal/api/handler_edge_device.go:608-625`

Driver 中 `Schedulable=false` 的 BMS/逆变器控制命令虽然可列出，但没有统一执行桥：

- `backend/internal/api/handler_driver_commands.go:21-76`
- `frontend-shared/src/components/device/CommandList.vue:34-49`

### 3.2.2 写操作是 fire-and-forget

当前 write 分支调用 `SendWriteCommand(..., readSize=0)` 后立即继续：

- `backend/internal/api/handler_edge_device.go:684-700`

随后返回 `status=sent` 并计为 success：

- `backend/internal/api/handler_edge_device.go:749-759`

这只能证明后端尝试发布，不能证明目标设备已执行。

### 3.2.3 verify 失败仍返回成功

当前 `verify_operation` 的配置错误、发送失败、NACK 或解析失败只记录 warning：

- `backend/internal/api/handler_edge_device.go:702-747`

最终仍返回 success。因此当前验证不是权威闭环。

### 3.2.4 参数校验主要存在于前端

前端类型定义了：

- 参数类型；
- min/max/step；
- enum options；
- confirm。

证据：

- `frontend-shared/src/api/deviceConfig.ts:9-26`

后端 `OperationConfig` 不包含对等参数 schema：

- `backend/pkg/parser/template.go:24-49`

浏览器限制可以被直接调用 API 绕过。

### 3.2.5 没有业务操作状态机和真实历史

`PendingWriteRecord` 只保存底层写帧：

- `backend/internal/models/models.go:359-374`

缺少业务动作、actor、风险、状态、验证和步骤结果。

操作历史接口固定返回空数组：

- `backend/internal/api/handler_edge_device.go:560-567`

### 3.2.6 前端状态反馈不可靠

写操作只提示"命令已发送"：

- `frontend-shared/src/views/edge-device/shared/OperationButtons.vue:217-229`

详情页没有持久化操作 store，也没有在刷新/重连后恢复 pending 操作。

### 3.2.7 在线判断不权威

前端根据 EdgeDevice 快照判断离线：

- `frontend-shared/src/views/edge-device/shared/OperationButtons.vue:113-116`

后端执行前只检查 EdgeDevice enabled 和 Channel 有效，没有强制检查 Node 当前在线：

- `backend/internal/api/handler_edge_device.go:599-605`

### 3.2.8 协议文档与实现漂移

`docs/协议/二进制帧协议.md:137-159` 描述的 WriteCmd/WriteRsp 字段与当前 Go/C 实现不一致。任何控制协议扩展前必须先冻结真实 Wire Spec。

### 3.2.9 WriteCmd 缺少固件端幂等重放保护

PeriphCmd 已有 payload-aware replay/collision 机制；WriteCmd 直接解析和投递：

- `esp32-collector/components/msg_handler/handler_writecmd.c:38-66`

MQTT 重投、服务重启或调用方重试可能重复执行危险命令。

### 3.2.10 BMS MOS 位语义与工厂模式假设需修正

MOS 命令的描述（`jiabaida.go:622-637`）标注"需工厂模式"，但嘉佰达 V19 协议文档中 0xE1 MOS 命令为独立命令，未要求工厂模式前置。工厂模式仅用于 0xF2/0xF3 参数读写和 0x10/0x11 容量设置。详见第 26 节协议对照。

此外，0xE1 的 XX 同时携带充电和放电两个软件关闭位，不能安全拆成两个单布尔 setter；任一命令都可能释放另一位。本方案第 18.2 节改为一次提交完整双 bit policy。

> **⚠ 待验证**：需在实际硬件上确认 0xE1 是否需要工厂模式前置。若驱动代码的"需工厂模式"注释基于实际测试经验，应以硬件行为为准。

### 3.2.11 Driver 注册与元数据一致性

生产 composition root 现在只构造一个 Registry，并将其注入 API、NodeManager、DataBus 和 Action
Catalog；生产代码不再读取包级 `GlobalRegistry()`。`RegisterBuiltInDrivers` 已转发完整 parser-aware
注册入口，测试断言两个入口均注册 7 个 Driver（含 `sn3001_rain`）；Techfine `HardwareTypes` 为
`uart`。仍需保持这条单实例与完整注册集回归，禁止恢复双实例或缩减测试注册集。

### 3.2.12 用户写旁路不止 `/execute`

当前所有可能产生物理 TX 的入口必须分类治理：

- `/edge-devices/:id/execute` write：业务动作旁路；
- `/edge-devices/:id/change-address`：发送后立即修改实例地址，必须迁移为可验证 Action；
- `/channels/:channel_id/write`：raw diagnostics；
- REST/WS terminal write：raw diagnostics；
- device initialization：内部控制；
- 周期采集：内部只读；
- legacy `channel_id=0 + FC00` factory reset：危险旧协议入口。

业务动作必须进入 CommandExecution。raw diagnostics 使用独立权限、审计和默认关闭开关；内部控制不出现在 Action Catalog，但必须共享同一物理 bus worker/admission 边界。legacy factory reset 必须在任何高风险控制开放前移出普通 WriteCmd 或明确禁用。

### 3.2.13 ResourceReport 缺少可执行性快照身份

当前 ResourceReport 只更新 Node 的 capabilities、hardware_info、platform 和 dma_channels，没有独立的 `boot_id`、`resource_reported_at` 和 `command_engine_revision`。`LastSeen` 不能代替报告时间。未补齐这些字段前，后端无法证明 capability 属于当前 boot，也无法正确执行 freshness 门禁。

---

# 4. 架构边界与不变量

## 4.1 责任边界

| 组件 | 责任 | 不负责 |
|---|---|---|
| Node | 物理 ESP32 节点身份、在线状态、配置代际 | 下游设备业务动作定义 |
| ResourceReport | 实际硬件资源和命令执行引擎能力的唯一事实源 | 某型号 BMS/逆变器业务知识 |
| Channel | 总线端点、队列、排他、字节事务 | GPIO/PWM、本地业务权限 |
| EdgeDevice | 具体下游设备实例、地址、实例状态 | 型号级协议实现 |
| DeviceConfig | 型号配置、现场覆盖、动作策略 | 任意不受信任原始命令执行 |
| Driver | 协议知识、动作编译、ACK/响应解析 | MQTT、DB 生命周期、权限、审计 |
| PeriphCmd | Node 本机 GPIO/PWM | 雨量计、BMS、逆变器 |
| CommandExecution | 操作生命周期、幂等、步骤、验证、审计 | 厂商协议硬编码 |
| 前端 | 展示能力、收集参数、显示状态 | 决定最终权限、提交任意帧 |

## 4.2 必须保持的不变量

### I1：GPIO/PWM 与 Channel 分离

- GPIO/PWM 继续使用 `PeriphCmd/PeriphRsp`；
- 只有有协议帧的外部设备使用 Channel；
- 不新增 `periph_type=BMS/INVERTER/RAIN_GAUGE`。

### I2：硬件和执行能力来自 ResourceReport

- 后端不得按 ESP32-C6/S3 型号推测能力；
- 节点未上报时返回空能力或不可执行；
- ResourceReport 过期时高风险控制 fail-closed。

### I3：Driver 是可信协议编译器

浏览器只提交：

```text
action_id + structured params
```

不能提交：

- write_data；
- command_template；
- CRC 算法；
- 任意寄存器地址；
- 任意脚本。

### I4：期望值与确认值分离

- desired value 表示用户意图；
- confirmed value 表示设备已验证状态；
- MQTT publish 或总线 TX 成功不得提升 confirmed value；
- 无法确认时保留最后 confirmed value，并将操作标为 UNKNOWN。

### I5：型号级配置与实例级状态分离

不得将单个设备的：

- 地址；
- 波特率；
- MOS 状态；
- 逆变器设置；
- 运行目标值

写回共享 `DeviceConfig`。

### I6：所有控制路径共享 Channel 串行化边界

以下操作必须共享 `node_id + channel_id` 的调度边界：

- 周期采集；
- 用户 read；
- 用户 write；
- 写后 verify；
- 初始化；
- 多步骤事务；
- 调试终端写入。

### I7：单一 Driver Registry

生产路径只有一个已注册的 `*drivers.Registry`，所有热路径、tree 和 test-parser 共享同一实例。测试中的隔离 Registry 不受此约束。

---

# 5. 总体目标架构

```text
┌──────────────────────────────────────────────────────────────┐
│                     Frontend Control Plane                   │
│  ActionPanel / SettingsPanel / OperationTimeline / Store     │
└──────────────────────────────┬───────────────────────────────┘
                               │ action_id + params
                               ▼
┌──────────────────────────────────────────────────────────────┐
│                   Device Action API Layer                    │
│  Auth / Confirmation / Schema Validation / Availability      │
└──────────────────────────────┬───────────────────────────────┘
                               ▼
┌──────────────────────────────────────────────────────────────┐
│                 CommandExecution Service                     │
│  Durable State / Idempotency / Audit / Retry Policy / Verify │
└──────────────┬───────────────────────────────┬───────────────┘
               │                               │
               ▼                               ▼
┌─────────────────────────────┐   ┌────────────────────────────┐
│ Device Action Registry      │   │ Operation Repository       │
│ Driver + DeviceConfig merge │   │ Execution/Attempt/Step     │
└──────────────┬──────────────┘   └────────────────────────────┘
               │ compile plan
               ▼
┌──────────────────────────────────────────────────────────────┐
│                   Channel Command Scheduler                  │
│ Priority / Channel Exclusion / Sampling Coordination         │
└──────────────────────────────┬───────────────────────────────┘
                               │ ChannelCmdV2 / bounded ChannelBatchCmd
                               ▼
┌──────────────────────────────────────────────────────────────┐
│                      MQTT Control Topic                      │
└──────────────────────────────┬───────────────────────────────┘
                               ▼
┌──────────────────────────────────────────────────────────────┐
│                  ESP32 Command Engine                        │
│ Strict Decode / Replay / Deadline / Queue / Channel Lock     │
└──────────────────────────────┬───────────────────────────────┘
                               ▼
┌──────────────────────────────────────────────────────────────┐
│                UART / I2C / SPI Downstream Device            │
└──────────────────────────────────────────────────────────────┘
```

---

# 6. 统一设备动作模型

## 6.1 Action Definition Schema v1（规范版）

本节冻结唯一字段命名。旧文档中的 `execution_plan`、`verification_plan`、`risk_level` 等同义字段不得继续并存；迁移时统一映射到本节字段。

```text
ActionDefinitionV1
├── identity
├── semantics
├── input_schema
├── preconditions
├── execution          # 仅服务端可见
├── verification
├── safety
├── availability
└── presentation       # 仅声明受控 token
```

### 6.1.1 Identity

```json
{
  "id": "bms.set_mos_policy",
  "version": 1,
  "label": "MOS 软件控制策略",
  "description": "一次设置充电和放电 MOS 的完整软件关闭位",
  "category": "control"
}
```

要求：

- ID 稳定且不可复用为不同语义；
- 协议步骤变化或验证语义变化时增加 version；
- Execution 持久化时保存实际使用版本。

### 6.1.2 Semantics

```json
{
  "kind": "set",
  "idempotency_class": "set_absolute",
  "concurrency_policy": "serialize",
  "exclusive_scope": "channel"
}
```

`kind`：

- `query`：只读查询；
- `set`：设置绝对目标值；
- `action`：瞬时动作；
- `reset`：清零或恢复操作；
- `reboot`：重启并通过重连验证；
- `workflow`：多步骤事务；
- `batch`：批量设置。

`idempotency_class`：

- `read_only`；
- `set_absolute`；
- `reconcilable`；
- `at_most_once`。

### 6.1.3 Input schema

采用 JSON Schema 2020-12 的受限子集，字段名遵循标准关键字；不支持 `$ref`、远程 schema、正则执行扩展或自定义代码。

示例：

```json
{
  "type": "object",
  "required": ["charge_software_closed", "discharge_software_closed"],
  "properties": {
    "charge_software_closed": {
      "type": "boolean",
      "label": "软件关闭充电 MOS"
    },
    "discharge_software_closed": {
      "type": "boolean",
      "label": "软件关闭放电 MOS"
    },
    "priority": {
      "type": "string",
      "enum": ["user", "operator"],
      "default": "user",
      "label": "控制优先级"
    }
  },
  "additionalProperties": false
}
```

数值设置示例：

```json
{
  "type": "object",
  "required": ["current_a"],
  "properties": {
    "current_a": {
      "type": "integer",
      "minimum": 10,
      "maximum": 80,
      "multipleOf": 10,
      "unit": "A",
      "label": "总充电电流"
    }
  },
  "additionalProperties": false
}
```

后端必须校验：

- required；
- 类型；
- 整数与浮点区分；
- min/max；
- step/`multipleOf`；
- enum；
- 字符串长度和字符集；
- 禁止未知字段；
- 跨字段约束；
- 编码后的数值范围；
- 业务前置条件。

### 6.1.4 Preconditions

```json
{
  "all": [
    {"type": "node_online"},
    {"type": "resource_report_fresh"},
    {"type": "channel_enabled"},
    {"type": "device_state_fresh", "field": "current_a", "max_age_ms": 30000},
    {"type": "numeric_range", "field": "current_a", "minimum": -5, "maximum": 5},
    {"type": "protection_clear"}
  ]
}
```

只允许注册过的前置条件类型，不执行任意表达式。前端可展示不可用原因，但后端必须重新求值。

### 6.1.5 Safety policy

```json
{
  "level": "high",
  "confirmation": "show_current_and_target",
  "reason_required": true,
  "recent_auth_required": false,
  "max_retries": 0,
  "auto_reconcile": false,
  "cooldown_ms": 3000
}
```

| 风险等级 | 典型操作 | 要求 |
|---|---|---|
| low | 只读查询 | 无 |
| medium | 逆变器输出电压/频率设置 | 确认、参数校验 |
| high | MOS、输出电压、雨量清零 | 显示当前值和目标值，二次确认，通常要求理由 |
| critical | 重启、复位、固件升级 | 二次确认、理由、近期认证、审计 |

### 6.1.6 Execution（仅服务端）

```json
{
  "mode": "single",
  "compiler": "jiabaida.set_mos_policy",
  "required_engine_features": ["channel_cmd_v2"],
  "exclusive_scope": "channel"
}
```

`compiler` 必须引用随 Driver 注册的可信编译函数。Action Catalog API 不返回 `execution`，浏览器和 DeviceConfig overlay 均不能提供原始帧、命令模板、CRC 算法、寄存器地址、parser 名称或任意脚本。

执行模式只有：

- `single`：一个有界 Channel 字节事务，绝大多数查询和设置使用；
- `bounded_batch`：少量固定步骤，仅用于必须保持 Channel 排他或必须执行 finally 的协议；
- `backend_sequence`：后端逐个调度普通单步事务，不要求设备端原子性。

禁止把 `readback` 当作编译类型；它属于 verification。

### 6.1.7 Verification

```json
{
  "strategy": "protocol_response",
  "deadline_ms": 5000
}
```

验证策略：

- `protocol_response`：厂商协议响应足以确认该动作语义；
- `readback`：写后读回指定字段；
- `telemetry`：等待后续采集值；
- `reconnect`：仅证明设备恢复响应；
- `boot_change`：验证可观测代际变化；
- `composite`：组合已注册的有限谓词。

`reconnect` 不能单独证明“发生过重启”，除非同时观察到离线窗口、boot/uptime 变化或协议提供的重启证据。

### 6.1.8 Availability

```json
{
  "required_features": ["channel_cmd_v2"],
  "resource_report_max_age_ms": 60000,
  "requires_frozen_protocol": true
}
```

协议仍待硬件验证、能力未上报或 ResourceReport 过期时，动作必须 `available=false`，不能仅在文档中保留警告。

### 6.1.9 Presentation

```json
{
  "component": "form",
  "group": "bms_control",
  "order": 20
}
```

受控 token 仅包括 `button`、`switch`、`enum`、`number`、`form`、`settings_group`。未知 token fail-closed。后端不得下发 HTML、JavaScript、Vue 路径、CSS 或可执行条件表达式。

## 6.2 能力来源与合并规则

```text
Driver 内建动作定义（协议与安全下限）
                ↓
DeviceConfig overlay（只能收窄范围、加强策略、修改文案或禁用）
                ↓
EdgeDevice 实例状态 + Channel + ResourceReport
                ↓
当前 actor 权限
                ↓
EffectiveActionCatalog
```

合并必须满足：

1. Driver 是命令编译、响应解析和最低风险等级的唯一权威；
2. DeviceConfig 不得扩大参数范围、降低风险、取消验证或注入 raw command；
3. EdgeDevice 只提供地址、实例状态和 revision，不回写共享型号配置；
4. API 返回 schema、展示、状态和 unavailable reason，不返回执行细节；
5. 定义内容变化必须增加 action version；Execution 保存完整 identity 和规范化参数摘要。

---

# 7. 操作实例与状态数据模型

## 7.1 最小持久化模型

`CommandExecution` 保存业务权威状态：

```text
ID / CommandID / IdempotencyKey / RequestHash
EdgeDeviceID / NodeID / ChannelID / DeviceConfigID
ActionID / ActionVersion / NormalizedParamsJSON / ExpectedRevision
Actor / SourceIP / Reason / RiskLevel / IdempotencyClass
RequiredBootID / RequiredManifestID / RequiredCapabilityRevision / WireDigest
State / VerificationState / AttemptCount / DeadlineAt
ErrorDomain / ErrorCode / ResultJSON / ConfirmedStateJSON
CreatedAt / QueuedAt / DispatchedAt / AcceptedAt / CompletedAt
```

配套表：

- `CommandAttempt`：一次受控 dispatch，保存 attempt、boot ID、状态和错误；
- `CommandStepResult`：只为 bounded batch 或业务验证保存步骤摘要；
- `EdgeDeviceSettingState`：保存 desired、acknowledged、confirmed/observed、revision 和 pending execution；
- `CommandOutbox`：与 Execution 在同一数据库事务创建，负责可靠 dispatch；
- `CommandInbox`：以 `(command_id, attempt, event_seq)` 唯一去重设备事件。

表按纵向切片落地，不要求首个只读动作之前一次建完全部模型：

- 首个只读闭环必需：Execution、Attempt、Outbox、Inbox；
- 第一个 setter 增加 SettingState；
- 第一个 bounded batch 增加 StepResult；
- `SecurityAuditEvent` 复用现有模型和 writer，不另造一套审计表。

`SettingState` 必须区分：

- `desired`：用户目标；
- `acknowledged`：协议已接受的设置语义；
- `observed`：设备遥测到的物理状态；
- `confirmed`：动作定义规定的最终确认值。

不得把 MQTT publish、ESP32 accept 或总线 TX 直接写成 confirmed。

## 7.2 Outbox 与执行租约

创建操作时在一个数据库事务内写入 Execution、审计事件和 Outbox。Dispatcher 使用 `owner_id + lease_until + fencing_token` CAS 领取任务；同一 `node_id + channel_id` 同时只有一个有效调度租约。

MQTT 允许 at-least-once 发布。纯传输重投必须复用完全相同的 command ID、attempt、wire digest 和 envelope；设备事件先写 Inbox，再推动状态机，重复事件不得产生二次状态迁移。只有能够证明上一物理 attempt 未执行且动作策略允许时，才创建新的物理 attempt；at-most-once 永不自动创建新 attempt。

## 7.3 审计失败策略

- high/critical：创建审计或 Execution 持久化失败时 fail-closed，不下发；
- low 查询：可按显式配置降级，但必须告警；
- 普通日志只保存 request hash/wire digest 前缀和脱敏参数，不记录完整危险帧。

---

# 8. 命令执行状态机

无效请求在创建 Execution 前完成 schema/权限/availability 校验，并记录拒绝审计；被接受的请求从 `QUEUED` 开始：

```text
QUEUED → DISPATCHED → DEVICE_ACCEPTED → VERIFYING → SUCCEEDED
   ├→ CANCELLED           ├→ FAILED       ├→ FAILED
   ├→ FAILED              └→ UNKNOWN      └→ UNKNOWN
   └→ EXPIRED
```

单步协议没有 STARTED 事件，因此不设置无法被证据支持的 `RUNNING` 状态。未来 bounded batch 若需要 STARTED，必须增加明确协议事件后才能加入。附加终态：`SUPERSEDED`、`PARTIAL_FAILED`。所有迁移使用 expected state + attempt + boot ID + fencing token 做 CAS；stale ACK 只能影响零行。

结果判定矩阵：

| 已到达阶段 | 可证明未执行 | 可能已执行 | 结论 |
|---|---|---|---|
| QUEUED | 是 | 否 | CANCELLED/EXPIRED/FAILED |
| publish 失败且 Outbox 未确认发送 | 是 | 否 | FAILED 或继续调度 |
| 已 publish、未收到 accept，仍在 dispatch deadline 内 | 否 | 是 | 保持 DISPATCHED；按策略重投同一 envelope |
| 已 publish、未收到 accept，dispatch deadline 已过 | 否 | 是 | UNKNOWN，除非节点返回明确 EXPIRED/REJECTED |
| 已开始总线 TX | 否 | 是 | UNKNOWN 或按合法 NACK 判 FAILED |
| 协议 ACK 后验证不一致 | 否 | 是 | FAILED；无法完成验证则 UNKNOWN |

HTTP 生命周期与操作生命周期分离：创建接口返回 `202`，REST 是状态权威，WebSocket 仅用于通知。

取消仅允许在 `QUEUED` 且 Outbox 尚未被领取时成功。`DISPATCHED` 之后在没有设备取消协议的前提下返回冲突，不能把“用户不再等待”伪装成物理命令已取消。

---

# 9. 成功语义、错误模型与超时

必须区分五层：`MQTT_DISPATCHED`、`DEVICE_ACCEPTED`、`TRANSPORT_SUCCEEDED`、`PROTOCOL_ACKED`、`VERIFIED`。只有动作定义规定的最终层完成后才进入 `SUCCEEDED`。

统一错误域：`VALIDATION`、`AUTH`、`CAPABILITY`、`NODE_CONFIG`、`QUEUE`、`TRANSPORT`、`DEVICE_PROTOCOL`、`VERIFY`、`REPLAY`、`OUTCOME`。错误至少包含稳定 `code`、可重试标志和脱敏 detail。

超时分为：

```text
queue_ttl
mqtt_dispatch_timeout
node_accept_timeout
step_rx_timeout
batch_total_timeout
verification_timeout
reconnect_timeout
```

ESP32 只执行已经下发的单步/批次 deadline，不维护业务长超时。所有设备端 timeout 都必须受 ResourceReport 的上限约束；后端不得发送节点无法承受的 timeout 或缓冲长度。

---

# 10. 幂等、重放与结果不确定性

## 10.1 后端

客户端提交 `Idempotency-Key`。相同 actor、设备、动作和 key：

- `request_hash` 相同：返回已有 Execution；
- `request_hash` 不同：409 collision；
- 传输重投复用 command ID、attempt 和 wire envelope；只有动作策略允许且能证明未物理执行时才增加 attempt。

必须分离两类摘要：

- `request_hash`：action/version、actor scope、EdgeDevice、规范化参数、expected revision；它在节点重启或 ResourceReport 更新后仍保持稳定，用于 HTTP 幂等；
- `wire_digest`：协议版本、command ID、attempt、required boot/manifest/capability revision、Channel、TX/RX、deadline/timeout 和编译后的物理步骤；它用于 ESP32 exact replay/collision。

环境变化不得把相同用户请求误判为 idempotency collision。若 boot/manifest 已变化，旧 wire envelope 不得重新编译后沿用原 digest；应对原 Execution 做 reconcile/UNKNOWN，或由用户创建新的 Execution。

## 10.2 ESP32 轻量去重

设备端不保存完整业务对象，只保存固定大小条目：`command_id + payload_digest + state/result_code`。

- 普通 read/set：使用小型 RAM ring，容量由 ResourceReport 上报；重启后由后端 readback/reconcile；
- `at_most_once` high/critical：仅保存固定大小 NVS seen-ring；在物理执行前记录 `SEEN`，重启后相同命令返回 `REPLAY_UNKNOWN`，绝不自动重执行；
- NVS 记录必须发生在严格校验和执行槽预留之后、物理 TX 之前；NVS 写失败则明确 REJECTED，禁止继续执行；
- 不为每个普通命令写 NVS，不保存完整 payload/raw response，避免 flash 写放大；
- cache 满且存在 in-flight 条目时返回 BUSY，不淘汰运行中证据。

该策略优先保证“不重复危险物理动作”。在 NVS 已写入但执行结果未知的断电窗口，允许牺牲自动恢复并进入 UNKNOWN。

## 10.3 UNKNOWN

UNKNOWN 保留最后 confirmed value，展示目标和原因，优先提供安全的重新读取/对账动作。`at_most_once` UNKNOWN 禁止默认重试。

---

# 11. Channel 调度、排他与有界批次

## 11.1 统一边界

所有周期采集、用户查询/设置、初始化和调试命令继续进入现有 per-bus FreeRTOS 队列。ESP32 不新增第二套总线 worker；业务排他粒度是 `channel_id`。其他独立总线 worker 可继续运行，同一物理 bus worker 上的命令仍按现有队列串行等待。

普通动作使用单步事务。只有同时满足以下任一条件时才允许 bounded batch：

1. 中间步骤不能被周期采集插入；
2. 进入临时模式后必须执行 finally 清理；
3. 写与紧邻 readback 必须在同一 Channel 排他窗口完成。

## 11.2 Bounded batch 不是工作流语言

ESP32 只理解按顺序排列的字节事务：

```text
Batch
- command_id / digest / channel_id / deadline
- stop_on_error
- step_count (有界)
- steps[]: tx bytes, rx policy/size, timeout, delay, flags
```

允许的 step flags 只有 `NORMAL`、`FINALLY`。BMS 唤醒前导帧只是一个带 100ms delay 的普通 NORMAL step，ESP32 不需要理解“唤醒”语义。禁止条件分支、循环、变量、表达式、厂商寄存器语义、设备状态判断和回滚脚本。

执行规则：

- 先完成严格校验和资源预留，再接受命令；
- batch 期间仅暂停该 Channel 的新采集 admission；
- 按顺序执行 NORMAL，首个失败后跳过剩余 NORMAL，但仍执行 FINALLY；
- 结果在一个最终响应中返回有界 step 摘要；默认不为每步发布 MQTT 事件；
- finally 失败上报 `CLEANUP_FAILED`，后端将设备置为需要维护，不宣告成功。

## 11.3 性能约束

- 当前 `CMD_TX_MAX=128` 是实现基线；新协议不得假设更大缓冲，实际能力由 ResourceReport 上报；
- batch step 上限采用编译期常量，初始目标不超过 6，实际值按固件构建和内存测试上报；
- batch 还必须受 `max_batch_tx_bytes` 和 `max_batch_response_bytes` 总预算约束，不能只限制单 step；
- 同一节点允许的 active batch 数量可为 1；无关总线继续采集，同一物理 bus worker 的额外等待必须受 batch total timeout 限制；
- 使用固定池/静态数组或等价的有界分配，FreeRTOS 队列只传小型 descriptor/slot index，避免反复复制整个 batch；禁止按远端长度无界 malloc；
- MQTT 默认只发送 ACCEPTED/REJECTED 和一个 FINAL，步骤明细随 FINAL 聚合；
- 后端不得通过提高 step 数、payload 大小或 timeout 绕过设备能力上限。

---

# 12. 协议设计

## 12.1 Phase 0 冻结现有事实

现有 WriteCmd/WriteRsp/DataReport 先修正文档并建立 Go/C 黄金向量。旧 WriteCmd 仅用于兼容读取和低风险过渡，不承载新的危险动作语义。

## 12.2 ChannelCmdV2（默认路径）

单步消息只包含：

```text
command_id(固定 16 bytes)
payload_digest(固定长度)
attempt
boot_id / manifest_id
edge_device_id / channel_id
deadline_ms
tx_data
read_policy / read_size / max_read_size
rx_timeout_ms / post_tx_delay_ms
risk_class / flags
```

`payload_digest` 固定为 SHA-256 前 16 字节，输入是协议版本和所有影响物理执行的字段按 Wire Spec 规定顺序编码后的字节。Go/C 使用同一黄金向量；ESP32 只对收到的有界消息计算一次，不在轮询路径重复计算。若实测不满足性能门禁，必须通过协议版本变更重新选择算法，不能由不同固件自行替换。

设备返回：`ACCEPTED` 或 `REJECTED`，以及一个 `FINAL`。响应必须回显 command ID、digest、attempt、boot ID 和单调 event sequence；FINAL 包含 transport 状态、原始响应（有界）和 replay 标记。后端 Driver 解析厂商 ACK。

## 12.3 ChannelBatchCmd（按需能力）

仅在 `supports_bounded_batch=true` 时使用。顶层身份字段与 ChannelCmdV2 相同，附加有界 step 数组。不存在独立通用 `ChannelTxnEvent` 流；事件仍只有 accept/reject/final，避免 MQTT 放大和固件状态机膨胀。

## 12.4 严格解码

固件必须检查 required presence、wire type、duplicate singular field、clean EOF、canonical bool、长度/数值上限、deadline、boot/manifest、Channel 存在及总线类型。非法 bus 不得 fallback 到 UART0，超长 payload 不得截断。

控制消息使用 `schema_version` 精确协商。固件对当前已声明 schema 的未知字段 fail-closed；新增可选字段必须提升 schema minor，并且只有双方 ResourceReport/后端能力协商确认支持该 minor 后才能发送。旧固件静默忽略字段不能视为支持。这里的“严格拒绝”针对未协商 schema，不能与“同一 major 自动兼容任意 minor”同时成立。

## 12.5 早到事件

后端先事务性写入 Execution + Outbox，再 publish。Inbox 在 HTTP 返回前即可接收事件；前端按 execution ID 合并，HTTP continuation 不得覆盖更晚终态。

---

# 13. 能力声明与 ResourceReport

ResourceReport 的 `command_engine` 只描述执行能力，不包含 BMS/逆变器业务知识：

```json
{
  "protocol_version": 1,
  "boot_id": "...",
  "supports_channel_cmd_v2": true,
  "supports_bounded_batch": false,
  "supports_finally": false,
  "max_batch_steps": 0,
  "max_tx_bytes": 128,
  "max_rx_bytes": 256,
  "max_batch_tx_bytes": 0,
  "max_batch_response_bytes": 0,
  "max_step_timeout_ms": 5000,
  "max_batch_timeout_ms": 0,
  "ram_dedup_entries": 8,
  "persistent_at_most_once_entries": 0,
  "max_active_batches": 0
}
```

上例是形状而不是硬编码承诺。不同 C6/S3 构建根据实际静态配置、可用 heap 和已启用总线返回真实值。后端保存 report revision、reported_at 和 boot ID；高风险动作要求报告属于当前 boot 且未超过动作定义的 freshness TTL。

当前 Node 模型没有上述身份字段。Phase 0/2 必须明确迁移并原子更新：

```text
Node.BootID
Node.ResourceReportedAt
Node.CommandEngineRevision
Node.CommandEngineCapabilitiesJSON
```

`ResourceReportedAt` 取后端接收并成功持久化报告的时间；`LastSeen` 只表示节点最近通信，不能用于能力 freshness。`CommandEngineRevision` 对规范化 command_engine 内容计算，内容未变化时保持稳定；Execution 保存创建时实际使用的 revision。

## 13.1 固件资源预算门禁

新增执行能力必须提交构建目标上的测量结果，而不是只给理论估算：

- 固件体积增量；
- idle heap 与控制峰值 heap；
- 最大栈水位；
- 单步和最大 batch 的队列/静态池占用；
- 采集高负载下的命令延迟和丢样率；
- NVS 写频率与断电窗口行为。

建议门禁：控制功能不得依赖 PSRAM；不得新增常驻的逐命令 task；解码和 hash CPU 时间应远小于一次典型 UART/I2C 事务，且不能造成 watchdog、采集队列饥饿或其他 Channel 停顿。具体字节数和时延以每个固件构建的 ResourceReport 与实测基线为准。

---

# 14. 后端架构设计

## 14.1 模块职责

```text
deviceaction: action 定义、schema、Driver 编译、overlay、availability
commandexec: 状态机、repository、outbox、调度租约、dispatcher、inbox、recovery
verification: readback/telemetry/reconnect/boot-change
audit: 创建和状态变化审计
```

Driver 接口负责列出动作、编译有界 plan、解析步骤响应、评价协议成功和验证结果。Driver 不发布 MQTT、不操作业务表、不决定用户权限，也不返回前端可执行内容。

## 14.2 调度与恢复

Dispatcher 领取 outbox lease 后按 `node_id + channel_id` 串行 dispatch；租约过期可由其他 worker 使用更高 fencing token 接管。所有重发复用 command ID。

启动恢复时：

- 未 dispatch 的 Outbox 继续发送；
- 已 dispatch、无结果的 read/set 优先查询节点缓存或进入验证；
- at-most-once 只查询结果/对账，不自动重发；
- boot ID 变化且无持久证据时进入 UNKNOWN；
- 已验证成功后才执行实例级 post action，并使用 revision CAS。

---

# 15. REST 与 WebSocket API

规范 API：

```text
GET  /api/v1/edge-devices/:id/actions
POST /api/v1/edge-devices/:id/operations          # 202 + execution_id
GET  /api/v1/device-operations/:execution_id
GET  /api/v1/edge-devices/:id/operations
POST /api/v1/device-operations/:execution_id/cancel
POST /api/v1/edge-devices/:id/actions/:action_id/confirm
```

创建请求包含 `action_id`、`action_version`、`params`、`expected_revision`、`reason` 和可选 confirmation token；`Idempotency-Key` 是必需 header（只读查询可由客户端库自动生成）。

WebSocket 只发送 `device_operation_update` 摘要。页面加载、刷新和重连后必须重新查询 REST。

## 15.1 旧接口强制迁移门禁

现有 `/edge-devices/:id/operations` 占位实现和 `/execute` fire-and-forget 写分支不得与新控制面并存为可执行旁路：

1. Phase 1 首先删除占位 `pending`/空历史语义；
2. 旧 `/execute` read 可临时转发新服务并提供同步兼容响应；
3. 旧 `/execute` write 必须转发 CommandExecution 并返回 202，或在 feature flag 下返回 410；
4. 不可映射到可信 Driver action 的 raw Operation 标记 `migration_required`，禁止执行；
5. 新动作上线前必须有测试证明直接调用旧路径也无法绕过 schema、权限、确认、审计和幂等。

## 15.2 Raw diagnostics 与内部控制

以下入口不允许被遗漏在 `/execute` 迁移之外：

| 当前入口 | 目标处理 |
|---|---|
| `/channels/:channel_id/write` | `raw_diagnostics_enabled=false` 默认关闭；独立 capability、理由和完整审计；使用受限 transport，不宣告业务成功 |
| REST/WS terminal write | 与 raw channel write 使用同一策略和审计，不保留独立无门禁发送器 |
| `/edge-devices/:id/change-address` | 迁移为可信 Driver action，验证新地址后再 revision CAS 更新实例 |
| device initialization | 内部 actor，通过统一 Channel transport/admission；不出现在用户 Action Catalog |
| periodic sampling | 保留内部 read，继续共享物理 bus worker |
| legacy factory reset | 从普通 WriteCmd 兼容面移除或在固件构建中默认禁用 |

Phase 0 必须生成当前 DeviceConfig Operations 和上述入口的迁移清单，逐项标记 `mapped_read`、`mapped_action`、`raw_diagnostic`、`internal` 或 `disabled`。没有分类的写路径视为门禁失败。

---

# 16. 前端架构与交互设计

前端只消费 EffectiveActionCatalog：展示 current/desired/acknowledged/observed/confirmed、availability、风险确认和操作时间线。`OperationButtons` 与直接读取 `device_config.operations` 的 fallback 标记 deprecated。

交互要求：

- write 的参数表单与确认是两个阶段，参数化 write 不得绕过确认；
- 202 后展示 pending，不显示成功 toast；
- 只有 SUCCEEDED 显示成功；FAILED/UNKNOWN 分开呈现；
- UNKNOWN 默认提供“重新读取/对账”，不提供危险动作一键重试；
- Node 在线、WebSocket 连接和设备数据新鲜度分别展示；
- WebSocket 事件早于 HTTP 时按 execution ID 合并，终态不可被 pending 覆盖。

---

# 17. 安全、权限与审计

权限按 action capability，而不是仅按“管理员”粗粒度判断。high/critical 的确认 token 绑定 actor、设备、action/version、canonical params hash 和 expiry；参数变化后 token 失效。

最低策略：

- high：服务端确认、理由、cooldown/rate limit、完整审计；
- critical：增加近期认证，必要时双人或现场策略；
- 审计失败对 high/critical fail-closed；
- raw diagnostics 使用独立权限和审计，不复用普通控制 API；
- MQTT 节点 topic 最终使用节点级 ACL/mTLS，过渡期全局凭据不得作为长期安全边界。

BMS `YY=0xAA` 属于提升控制优先级的独立 capability。默认使用 `0x00`；只有站点显式启用、硬件验证通过并满足 critical 策略时才能使用 `0xAA`，不得在 Driver 中硬编码为全局默认。

安全能力的交付顺序：

- Phase 0：停止普通日志记录完整 TX/raw dangerous frame；raw diagnostics 默认关闭；legacy factory reset 关闭；
- Phase 1：实现服务端 confirmation token、reason、cooldown/rate limit 和现有单用户 actor 审计；
- 第一个 high/critical 动作启用前：节点 control topic 必须具备节点级 ACL/mTLS 或经安全评审确认的等效隔离；
- 后续运维阶段：增加近期认证体验、告警、dashboard 和人工 UNKNOWN 流程。

因此“MQTT ACL/mTLS 在最后阶段完成”只适用于低风险试验环境，不允许成为 high/critical 生产动作的后置工作。

---

# 18. 典型设备场景

## 18.1 雨量计累计值重置

```text
action_id: rain.reset_accumulated
kind: reset
risk: high
idempotency: at_most_once
execution: single
verification: readback accumulated value
```

协议和清零寄存器未在可信 Driver 中冻结前保持 unavailable。执行前展示当前累计值和数据时间；命令一旦可能到达设备便不自动重试。读回为零且响应时间晚于命令才成功；ACK 丢失但读回为零可通过对账收敛，既无 ACK 又无法读回则 UNKNOWN。

## 18.2 BMS 充电/放电 MOS

### 协议确认

对照嘉佰达 V19 协议原文：

**协议原文（第四节）**：
- 主机发送：`DD 5A E1 02 YY XX CHECKSUM_H CHECKSUM_L 77`
- BMS 响应：`DD E1 00 00 -- Checksum_H Checksum_L 77`
- 其中 XX 表示控制 MOS 的状态，YY 表示优先级别
- BIT0: 充电开关控制：1 关闭充电开关；0 为打开充电开关
- BIT1: 放电开关控制：1 关闭放电开关；0 为打开放电开关

**优先级**：

| YY 的值(优先级别) | XX 的值 | MOS 的动作 |
|---|---|---|
| 0x00-终端用户级 | 0x00 | 解除软件关闭 MOS 管动作 |
| 0x00-终端用户级 | 0x01 | 软件关闭充电 MOS，解除软件关闭放电 MOS |
| 0x00-终端用户级 | 0x02 | 软件关闭放电 MOS，解除软件关闭充电 MOS |
| 0x00-终端用户级 | 0x03 | 软件同时关闭充电 MOS 和放电 MOS |
| 0xAA-运营端 | 0x00 | 解除软件关闭 MOS 管动作 |
| 0xAA-运营端 | 0x01 | 软件关闭充电 MOS，解除软件关闭放电 MOS |
| 0xAA-运营端 | 0x02 | 软件关闭放电 MOS，解除软件关闭充电 MOS |
| 0xAA-运营端 | 0x03 | 软件同时关闭充电 MOS 和放电 MOS |

**优先级规则**：运营端的命令可以操作控制用户端的。如果用户端锁车了，运营端和用户端都可以解锁。如果运营端锁车了，用户端是无法解锁。运营端解锁后就可以恢复至用户锁车。

**⚠ 关键发现**：协议原文中 0xE1 MOS 命令**未要求工厂模式前置**。工厂模式（0x00 寄存器写入 0x5678 / 0x01 寄存器写入 0x2828 或 0x0000）仅用于：
- 设置标称容量和循环容量（0x10, 0x11）
- 读写参数（0xF2, 0xF3）— 协议 7.11 节明确："流程：进入工厂模式（指令00，数据0x5678）-> 发送操作指令-> 退出工厂模式"

当前 Driver 代码中 `jiabaida.go:622-637` 的 MOS 命令描述标注"需工厂模式，一次性触发"，这一假设**需在实际硬件上验证**。如果实际硬件不要求工厂模式，则架构设计 18.2 节中的执行计划（包含 enter/exit factory mode）应简化。

### 动作定义

```text
bms.set_mos_policy(
  charge_software_closed,
  discharge_software_closed,
  priority = "user"
)
```

必须一次提交两个控制位，不提供 toggle，也不把两个 bit 暴露为会互相覆盖的独立 setter：

```text
XX = (charge_software_closed ? bit0 : 0)
   | (discharge_software_closed ? bit1 : 0)
```

这里的语义是“软件关闭策略”，不是“强制物理 MOS 导通”。`fet_status` 是受保护状态、电流和硬件条件共同影响的物理观测值，必须与 acknowledged software policy 分开显示。

### 执行计划（待硬件验证后确认）

**方案 A（若 0xE1 无需工厂模式）**：
```text
1. 获取 Channel 排他权
2. 校验 BMS 数据新鲜、当前电流和保护状态
3. 若该型号需要休眠唤醒，执行 wake preamble + 100ms
4. 按两个完整 software-closed bit 编译并发送 0xE1
5. parse ACK，更新 acknowledged policy
6. 可选读取 0x03，更新 observed fet_status
7. 释放 Channel 排他权
```

**方案 B（若 0xE1 需工厂模式，与当前 driver 注释一致）**：
```text
1. 获取 Channel 排他权
2. 校验 BMS 数据新鲜、当前电流和保护状态
3. 若该型号需要休眠唤醒，执行 wake preamble + 100ms
4. enter factory mode (0x00=0x5678)
5. parse ACK
6. 发送包含两个完整 software-closed bit 的 0xE1
7. parse ACK
8. finally: 使用经硬件验证的 exit factory 帧
9. 可选读取 0x03，更新 observed fet_status
10. 释放 Channel 排他权
```

方案 B 使用 bounded batch，步骤总数必须不超过节点上报上限。若硬件验证无法确定正确的 exit 语义，动作保持 unavailable。

### 结果

- MOS ACK 失败：FAILED；
- exit factory 失败（仅方案 B）：FAILED/UNKNOWN + maintenance alert；
- ACK 成功：可以确认 software policy 已被协议接受；
- observed fet_status 与策略预期不一致：按保护状态解释并单独告警，不覆盖 acknowledged policy；
- 已发送后断线无法验证：UNKNOWN；
- 如果型号提供可读取的软件锁定位，则只有读回一致才更新 confirmed policy；否则 UI 明确显示 acknowledged，而不能伪装成物理 confirmed。

### 优先级策略

默认使用 YY=0x00。YY=0xAA 只有在站点显式启用运营端 capability、硬件验证通过且 critical 确认/审计满足时才允许；它不是 Driver 全局默认值。

## 18.3 BMS 重启

### 协议确认

嘉佰达 V19 协议 7.7 节：

- 主机发送：`DD 5A 0E 00 8118（固定码）CHECKSUM_H CHECKSUM_L 77`
- BMS 响应：`DD 0E 00 00 -- Checksum_H Checksum_L 77`
- BMS 收到指令后进入复位重启状态

**⚠ 注意**：协议原文中 0x0E 长度字段为 0x00 但数据内容为 0x8118（固定码），这存在不一致。需在实际硬件上确认正确的帧格式。

### 动作定义

```text
operation_id: bms.reboot
kind: reboot
risk: critical
idempotency: at_most_once
verification: reconnect/composite
```

### 验证

- 在 0x0E 长度/固定码未通过黄金向量和实机验证前，`bms.reboot` 必须 `available=false`；
- 若 BMS 可能休眠，编译计划必须包含已验证的 wake preamble；
- 命令发出后通信暂时中断；
- deadline 内重新收到 BMS 合法响应；
- 必须同时观察到离线窗口、uptime/restart_count/boot 标识变化之一；仅“后来又收到响应”不能证明重启发生；
- 不以同连接 WriteRsp 作为成功依据。

未重新响应：

- 若明确收到设备拒绝：FAILED；
- 若命令可能已执行：UNKNOWN。

必须与 ESP32 Node 自身重启区分。

## 18.4 逆变器开关机

使用绝对动作 `inverter.set_power_state(on)`，不使用 toggle。普通开关机优先使用单步 ChannelCmdV2，解析协议 ACK 后通过状态查询或新鲜遥测验证。若关机导致通信中断，动作定义必须明确“ACK 足够”还是需要其他可观测状态，不能把断线直接当成功。

系统复位/重启另建 critical、at-most-once 动作，要求 NVS seen-ring 能力；节点未上报该能力时动作 unavailable。

## 18.5 逆变器参数设置

输出电压、频率、充电电流、电池类型和工作模式都使用绝对值 setter。参数范围由 Driver 给出安全上下限，DeviceConfig 只能收窄。

单参数设置使用 `single + readback`。批量设置默认由后端 `backend_sequence` 顺序执行和逐项验证，不为了批量 UI 在 ESP32 增加通用事务能力；只有厂商协议明确要求同一 Channel 原子窗口时才使用 bounded batch。部分成功返回 `PARTIAL_FAILED`，不得宣称硬件回滚。

---

# 19. 可观测性

后端指标至少覆盖 operation 总量/终态/耗时、Channel 排队、accept 延迟、UNKNOWN、replay/collision、cleanup failure、审计失败和 ResourceReport stale。command ID、device ID 不作为 Prometheus 高基数标签。

ESP32 只上报低开销聚合计数：控制命令 accepted/rejected/completed、queue full、decode error、replay、batch cleanup failure、控制峰值 heap 和最小栈水位。默认不为每个 step 打 INFO 日志或发送独立 MQTT 事件；需要诊断时才临时提高日志级别。

操作时间线由后端 Execution/Inbox/FINAL 聚合生成，不要求 ESP32 维护长期历史。

---

# 20. 兼容与迁移策略

1. 保留现有采集、DataReport、Driver parser 和 PeriphCmd；控制面增量接入；
2. 先封堵旧 `/operations` 占位和 `/execute` 写旁路，再开放新动作；
3. Driver 的 schedulable read 继续采集；one-shot command 迁移为版本化 ActionDefinition；
4. DeviceConfig raw Operation 只能迁移为已注册 Driver action 的 overlay；无法确认语义的标记 `migration_required` 并禁用；
5. 分阶段新增 Execution/Attempt/Outbox/Inbox、SettingState、StepResult，不伪造旧成功历史；
6. 前端移除读取 `config.operations` 的 fallback，后端 Action Catalog 成为唯一权威；
7. 旧固件没有 `channel_cmd_v2` 时只能保留现有采集和受控只读兼容，不开放危险写操作。
8. Phase 1 的 dispatcher 尚未接入 ChannelCmdV2 时，旧 read 可以继续走明确标记的 `legacy_read_only` adapter；不得声称已经 bridge 到尚不存在的真实 transport。
9. legacy read adapter 必须在 Phase 2 首个只读 V2 闭环完成后退役；write 不得使用该 adapter。

---

# 21. 测试与验证方案

## 21.1 契约与后端

- 唯一 Action Schema v1、未知字段/token、overlay 不能扩大能力；
- 状态机 CAS、stale event、终态不可覆盖、UNKNOWN 保留 confirmed；
- Idempotency-Key 并发、collision、Outbox 崩溃窗口、lease/fencing 接管；
- 旧 API 直调不能绕过 schema、确认、权限、审计和幂等；
- Node/ResourceReport/Channel/boot/config generation 前置条件；
- ACK、readback、mismatch、reconnect、boot-change 和 parse failure。

## 21.2 ESP32 host tests

- strict decoder：截断、duplicate、wire type、unknown、超长、过期、错误 Channel/bus；
- 单步：accept/final、RX timeout、queue full、无截断和无 UART0 fallback；
- RAM replay：exact replay、collision、in-flight 不淘汰；
- NVS seen-ring：仅 at-most-once 写入，重启后返回 REPLAY_UNKNOWN 且不重执行；
- bounded batch：上限、顺序、stop-on-error、finally、中断/超时、无关总线不受影响；
- 消息数量：默认每命令不超过 accept/reject + final，不产生 per-step MQTT 风暴。

## 21.3 ESP32 性能回归

在每个受支持的 C6/S3 构建上记录变更前后基线：

1. 固件 binary 增量、idle heap、minimum free heap、各相关 task stack high-water mark；
2. 单步最大 payload 与最大 bounded batch 的峰值内存；
3. 周期采集满负载时的控制 p50/p95 延迟、采集延迟和丢样率；
4. 连续 malformed/duplicate 消息下 watchdog、CPU 和 MQTT 稳定性；
5. at-most-once 命令的 NVS 写次数、断电恢复和磨损估算。

验收以“不依赖 PSRAM、不新增逐命令常驻 task、不发生无界分配/队列增长、不饿死无关总线采集”为硬门禁。同一物理总线允许发生有界串行等待。具体 heap/延迟阈值在 Phase 0 用当前固件实测基线冻结，避免脱离构建配置硬编码数字。

## 21.4 前端与实机

- 202/pending/WS/刷新恢复、ACK-before-HTTP、FAILED/UNKNOWN 不显示成功；
- BMS MOS 两个控制位组合向量，证明任一操作不会无意释放另一位；
- BMS wake、MOS 工厂模式 A/B、复位帧、参数读写均有黄金向量和实机证据；
- 断网、ACK 丢失、后端/ESP32 重启、replay/collision、cleanup failure；
- UI、API、DB、MQTT、ESP32 FINAL、目标设备物理状态和审计相互一致。

---

# 22. 分阶段实施计划

## Phase 0-A：当前 master 安全止血

目标是在不开放任何新业务写动作的前提下，使现有基础可作为后续协议开发基线。

1. 生产单 Registry，并完成热路径依赖注入；保留测试隔离 Registry；
2. 更新已经完成的 7 Driver 注册事实，只处理剩余元数据；
3. legacy WriteCmd 对超长 TX、非法 Channel/bus、wrong wire type、duplicate field 和 dirty EOF fail-closed；
4. 删除非法 bus → UART0 fallback，RX timeout 由受限字段或构建能力决定；
5. `/execute` write、change-address、raw channel write、REST/WS terminal、deviceinit、sampling、legacy factory reset 全量分类；
6. raw diagnostics 默认关闭，危险原始帧不进入普通日志；
7. 修正真实 WriteCmd/WriteRsp/DataReport Wire Spec，并建立 Go/C 黄金与 malformed 向量。

门禁：所有当前测试通过；新增 legacy hardening host tests 通过；没有未分类物理 TX 路径；业务 write 仍关闭。

## Phase 0-B：协议证据与性能基线（可与 0-A 并行）

1. 归档嘉佰达 PDF/OCR、来源、hash 和硬件记录；
2. 验证 MOS 工厂模式、YY=0xAA、0x0E reset 帧和 wake 条件；
3. 在 C6/S3 支持构建测量 binary、heap、stack、队列、满负载采集延迟；
4. 冻结 request hash/wire digest、错误域、schema version 和能力协商规则。

嘉佰达硬件证据不阻塞通用只读平台，但相关写动作保持 unavailable。

## Phase 1：最小后端控制域

1. 新增 Execution、Attempt、Outbox、Inbox；复用 SecurityAuditEvent；
2. 实现稳定 request hash、Idempotency-Key collision、状态 CAS、lease/fencing 和恢复；
3. 建立最小 Action Registry、受限 schema、availability 和只读 Action Catalog；
4. 提供 202 创建、详情、历史和 operation update；
5. dispatcher 先接 fake transport；旧 read 显式保留 `legacy_read_only` adapter；
6. 封堵 `/operations` 占位和所有业务 write 旁路；raw diagnostics 执行独立门禁；
7. confirmation token、reason、cooldown/rate limit 在服务端实现。

门禁：dispatcher 关闭时不发布 MQTT；非法请求不创建 Outbox；重启恢复和早到事件测试通过；旧 write 不再谎报成功。

## Phase 2：ChannelCmdV2 与首个只读闭环

1. 增加 boot identity，以及 Node 的 ResourceReportedAt/CommandEngineRevision/capabilities 持久化；
2. 冻结 ChannelCmdV2 accept/reject/final 消息号和 schema version；
3. 实现严格解码、可配置 timeout、固定 RAM replay/collision 和有界 control slot；
4. 复用现有 bus worker，在 RX 完成后聚合 FINAL；
5. 完成首个稳定只读动作（优先 PRS-3001 `read_rainfall` 或已具备实机的等价 Modbus read）；
6. 完成 REST/DB/Inbox/Driver parse/WS 的 E2E，并退役 legacy read adapter。

门禁：相同 envelope 不重复物理执行；超长/非法 bus 不执行；C6/S3 性能回归通过；业务写仍关闭。

## Phase 3：前端控制面与切换

1. EffectiveActionCatalog、Operation Store、Timeline、确认对话框；
2. REST 首次加载、WS early event/reconnect、刷新恢复；
3. 移除 DeviceConfig Operations fallback 和旧 OperationButtons 执行能力；
4. FAILED/UNKNOWN 不显示成功，raw diagnostics 不进入通用控制面。

门禁：前端只显示 Phase 2 已验证的 read；旧客户端无法恢复 direct write。

## Phase 4：首个普通 setter

1. 选择可安全恢复、可 readback 的逆变器绝对值 setter；
2. 增加 SettingState，分离 desired/acknowledged/observed/confirmed；
3. 完成 schema、compiler、ACK parser、readback、审计、实机记录和独立开关；
4. 中风险以上参数变更强制服务端确认。

门禁：读回一致才 SUCCEEDED；verify 失败为 FAILED/UNKNOWN；灰度仅限测试设备。

## Phase 5：按需 batch 与 at-most-once

1. 只有 F2/F3 或 MOS 实机证明需要 factory/finally 时实现 bounded batch；
2. 此时增加 StepResult；
3. 只为 at-most-once high/critical 增加固定 NVS seen-ring；
4. 按顺序启用 BMS MOS、雨量清零、reboot/reset，每个动作独立门禁；
5. 第一个 high/critical 动作前完成节点级 MQTT topic 隔离。

## Phase 6：安全与运维强化

1. critical 近期认证体验；
2. UNKNOWN 人工处置；
3. 操作告警、指标和 dashboard；
4. raw diagnostics 定期复核、历史保留和脱敏策略。

截至 2026-07-19，第 2 项已实现：人工处置作为一条不可覆盖的附加结论保存，原 Execution 始终保留
`UNKNOWN`。可选结论限定为 `CONFIRMED_SUCCEEDED`、`CONFIRMED_FAILED` 和
`ACKNOWLEDGED_UNKNOWN`，并记录原因、操作者和时间；完全相同的请求可幂等重放，不同的第二次
结论必须冲突。处置写入与安全审计位于同一事务，统一前端可操作和展示，Prometheus 使用有限标签
统计成功与重放。

第 3 项已完成可观测性第一切片：后端以有界低基数结果统计 admission/dispatch，记录排队与 accept
时延直方图、ResourceReport stale 和安全审计写失败；监控 API 从 Execution/Outbox/人工处置记录汇总
持久状态，统一监控页显示活跃操作、待处理/租约 Outbox、未处置 UNKNOWN、过期能力和审计写失败。
此切片不包含 cleanup failure 与 NVS 指标、外部告警规则/通道；第 1、4 项也仍未完成，Phase 6
继续按原门禁推进。

---

# 23. 验收标准

## 23.1 架构验收

- [ ] 新设备动作不需要新 REST 路由；
- [ ] 新设备动作不需要新 MQTT 业务消息类型；
- [ ] 前端通用控制组件不含设备型号硬编码；
- [ ] GPIO/PWM 未进入 Channel 动作模型；
- [ ] ResourceReport 是执行引擎能力唯一事实源；
- [ ] Driver 不负责 MQTT、DB 生命周期和权限；
- [ ] 浏览器无法提交任意 raw command。
- [ ] 旧 `/execute` 和 `/operations` 不能成为旁路；
- [ ] ESP32 不包含厂商协议解析、动态脚本、条件表达式或通用工作流解释器；
- [ ] 普通动作使用 single，bounded batch 只有实际原子/cleanup 需求才能启用。

## 23.2 正确性验收

- [ ] publish、accept、transport、protocol ACK、verified 五层状态分离；
- [ ] 只有动作定义的最终 verification strategy 完成才显示业务成功；
- [ ] verify 失败不会返回 success；
- [ ] UNKNOWN 保留最后 confirmed value；
- [ ] stale ACK 不能覆盖较新意图；
- [ ] 相同幂等 key 不重复执行；
- [ ] collision 被拒绝；
- [ ] expired 命令不执行；
- [ ] 非法 Channel 不 fallback；
- [ ] payload 不静默截断；
- [ ] 多步骤 finally 可验证执行。
- [ ] Outbox 崩溃恢复和 lease/fencing 不产生不同 command ID 的重复物理执行；
- [ ] BMS MOS 任一更新不会无意改变另一 software-control bit。

## 23.3 前端验收

- [ ] 参数化危险 write 也需要确认；
- [ ] 显示 current、desired、pending、acknowledged、observed、confirmed；
- [ ] 显示 Node 在线、WS 在线和数据新鲜度的区别；
- [ ] 页面刷新可恢复 active operations；
- [ ] WebSocket 重连可恢复；
- [ ] ACK-before-HTTP 不覆盖终态；
- [ ] failure/unknown 不显示成功 toast；
- [ ] batch partial failure 逐项展示；
- [ ] 未知 presentation token fail-closed。

## 23.4 安全验收

- [ ] 所有 API 有权威会话校验；
- [ ] high/critical 有服务端强制确认；
- [ ] critical 可要求近期认证；
- [ ] confirmation token 绑定 params hash；
- [ ] 审计失败对 high/critical fail-closed；
- [ ] 普通日志不泄露 secret/raw dangerous command；
- [ ] MQTT 节点 topic ACL 生效。
- [ ] BMS YY=0xAA 默认关闭，并由独立 critical capability 控制。

## 23.5 实机验收

- [ ] 逆变器参数设置后 readback 一致；
- [ ] BMS MOS 两个 software-control bit 编译正确，acknowledged policy 与 observed fet_status 不混淆；
- [ ] 雨量清零不盲目重试；
- [ ] BMS/逆变器重启通过重连/代际验证；
- [ ] 断网和 ACK 丢失进入正确 UNKNOWN；
- [ ] 后端/ESP32 重启后不重复危险操作；
- [ ] 操作记录和审计与物理结果一致。
- [ ] MOS 工厂模式假设已硬件验证（方案 A/B 确定）。

## 23.6 ESP32 性能验收

- [ ] C6/S3 各支持构建均有变更前后 binary/heap/stack 基线；
- [ ] 不依赖 PSRAM，不新增逐命令常驻 task；
- [ ] 解码、队列和 batch 使用有界内存，远端输入不能扩大分配；
- [ ] 默认每命令只有 accept/reject + final，无逐步 MQTT 风暴；
- [ ] 普通命令不写 NVS，at-most-once seen-ring 写频率和磨损可接受；
- [ ] 采集满负载下无 watchdog、队列饥饿和无关总线停顿；同一物理总线等待有明确上限；
- [ ] 最大 batch 性能未突破 Phase 0 冻结阈值。

---

# 24. 风险、决策与禁止事项

## 24.1 当前 BLOCKER

### B1：协议文档漂移

在修正 Wire Spec 前不得扩展控制协议。

### B2：缺少持久化 CommandExecution

当前 pendingwrite 不能表示业务操作生命周期。

### B3：写操作假成功

当前 `sent` 不能对用户呈现为设备执行成功。

### B4：verify 失败仍成功

必须改成 FAILED/UNKNOWN。

### B5：固件端 WriteCmd 无重放保护

危险操作上线前必须使用 ChannelCmdV2 的 exact replay/collision；at-most-once high/critical 还必须具备固定 NVS seen-ring 能力。

### B6：BMS MOS 工厂模式假设未验证

当前 Driver 代码和架构设计假设 MOS 需要工厂模式，但协议原文不要求。需硬件验证后确定方案 A 或 B。

### B7：后端无权威参数 schema

前端校验不能作为安全边界。

### B8：无真实操作历史和审计闭环

破坏性控制上线前必须可追溯。

### B9：Driver 双 Registry 分裂

进程内两个 Registry 实例，虽当前 driver 无状态暂不暴露问题，但设计漏洞必须修复。

### B10：旧 API 存在控制旁路

占位 `/operations`、fire-and-forget `/execute` write、change-address、raw channel write 和 REST/WS terminal write 在迁移前必须分类、封堵或进入独立受审计路径；只处理 `/execute` 不满足门禁。

### B11：ESP32 性能基线未冻结

在 C6/S3 上测量 binary、heap、stack、队列和满负载采集影响前，不得承诺 batch 上限或启用危险动作。

### B12：厂商协议原始证据未归档

当前仓库未包含文档声明引用的嘉佰达 PDF/OCR 文件。Phase 0-B 必须补齐可追溯来源和 hash；在此之前，第 26 节只能视为待复核摘录。

### B13：幂等请求摘要与物理摘要混用

request hash 若包含 boot/manifest/deadline，会在环境变化后把相同用户请求误判为 collision；实施前必须按第 10 节拆分 request hash 与 wire digest。

### B14：ResourceReport 无当前 boot/freshness 持久化证据

Node 尚无 BootID、ResourceReportedAt 和 CommandEngineRevision；在补齐前所有依赖 command engine freshness 的动作 unavailable。

## 24.2 高风险问题

1. ESP32 RX timeout 与后端不一致；
2. payload 可能截断；
3. 非法 bus 可能 fallback；
4. Node offline 未在后端执行前 fail-closed；
5. 实例状态可能被写回共享 DeviceConfig；
6. DeviceConfig/Driver 双轨漂移；
7. 参数化 write 绕过确认；
8. 前端在线状态快照过期；
9. raw command 暴露给前端；
10. MQTT 使用全局凭据时缺少节点隔离；
11. 嘉佰达 0x0E 复位帧长度字段与数据内容不一致（协议文档 7.7 节）。

## 24.3 已冻结决策与剩余硬件决策

### D1：控制消息

已决定：新控制使用 `ChannelCmdV2`；旧 WriteCmd 只做兼容。只有实际需要原子/cleanup 时使用 `ChannelBatchCmd`，不设计通用 `ChannelTxnEvent` 流。

### D2：设备端高风险 replay 持久化策略

已决定：普通命令只使用 RAM ring；仅 at-most-once high/critical 使用固定 NVS seen-ring，记录 SEEN 后跨重启禁止重执行，结果不确定时返回 REPLAY_UNKNOWN。

### D3：验证放在 ESP32 还是后端

已决定：ESP32 只执行透明有界字节事务；后端 Driver 解析协议 ACK 和业务值；最终 predicate 在后端。需要 Channel 原子性的 readback 才进入 bounded batch。

### D4：旧 `/execute` 兼容周期

已决定：Phase 1 可保留明确命名的 `legacy_read_only` adapter；Phase 2 首个 V2 read 闭环后退役。write 必须转发 CommandExecution 返回 202，或 feature flag 下返回 410。不得保留独立 fire-and-forget 路径。

### D5：自动化主体权限

未来若加入自动化规则，必须使用独立 capability scope 和审计 actor，不能共享浏览器管理员语义。

### D6：MOS 工厂模式方案选择

A：0xE1 独立命令（与协议原文一致），简化执行流程。
B：工厂模式包裹（与当前 driver 注释一致），需硬件验证。

**建议**：Phase 0 中硬件验证，以实际硬件行为为准。

### D7：BMS MOS 优先级选择

已决定：默认 YY=0x00。YY=0xAA 由独立 critical capability 显式开启，且必须先硬件验证。

## 24.4 明确禁止

- 禁止为每种设备新增专用 MQTT 消息；
- 禁止把 BMS MOS 当作 ESP32 GPIO；
- 禁止让浏览器提交任意 hex；
- 禁止以后端 publish 成功宣告设备成功；
- 禁止 verify 失败只记 warning；
- 禁止 at-most-once UNKNOWN 自动重试；
- 禁止用 toggle 代替绝对目标状态；
- 禁止将实例设置写入共享 DeviceConfig；
- 禁止按芯片型号硬编码命令引擎能力；
- 禁止非法 Channel fallback 到默认 UART；
- 禁止 payload 静默截断；
- 禁止在 ESP32 上实现厂商协议解析、JSON/脚本/表达式或通用工作流解释器；
- 禁止为普通命令逐次写 NVS；
- 禁止默认发布逐 step MQTT 事件；
- 禁止把 YY=0xAA 硬编码为 BMS 全局默认；
- 禁止后端下发可执行 UI 脚本；
- 禁止只凭单元测试宣告控制闭环完成；
- 禁止在开发验证中触碰生产 `/opt/EHomeSystem` 或 `ehome-*`。

---

# 25. Driver 注册修复（阶段 0）

## 25.1 问题清单

### 问题 1：Techfine 多重注册 + 双 Registry 分裂

**调用链** (`backend/cmd/server/main.go:100-103`):
```go
driverRegistry := drivers.NewRegistry()                                       // line 100
drivers.RegisterBuiltInDriversWithParsers(driverRegistry, parserConfigs)      // line 101
drivers.RegisterBuiltInDriversWithParsers(drivers.GlobalRegistry(), parserConfigs) // line 102
```

**结果**：进程内同时存在两个 Registry 实例，各持有 6 个 driver。Register 实现为 map 覆盖不 panic，但产生两条 "Driver registered: techfine_inverter (...)" 日志，并留下设计漏洞。

### 已完成项：生产解析路径使用同一注入 Registry

`main.go` 构造的 `driverRegistry` 同时注入 API、NodeManager、DataBus 和 Action Catalog；生产源码
不再调用 `drivers.GlobalRegistry()`。默认构造器仍可为隔离测试创建 Registry，但不属于生产运行路径。

### 已完成项：`RegisterBuiltInDrivers` 测试与生产集合一致

当前 `RegisterBuiltInDrivers` 已转发 `RegisterBuiltInDriversWithParsers(registry, nil)`；测试断言两个入口都注册 `bmp280`、`jiabaida_bms`、`lk_th01`、`prs3001`、`sn3000`、`sn3001_rain`、`techfine_inverter` 七项。该项从待办删除，但保留回归测试。

### 问题 3：Techfine 元数据剩余差异

`backend/internal/drivers/inverter_techfine.go:21-25`:
- DeviceName "Techfine GB3024 逆变器" → 应为 "泰琪丰 GB3024 逆变器"
- OEM "Techfine" → 应为 "泰琪丰"
- HardwareTypes 当前已为 `["uart"]`，无需再改。

### 已完成项：LK-TH01 OEM 元数据

`backend/internal/drivers/builtin.go:113` 的 OEM 已从错误的“路科”修正为“蓝控”，并由 Driver
元数据测试锁定。

## 25.2 修复方案

### 阶段 0a-1：单 Registry 实例

**改动**：`backend/cmd/server/main.go:100-102`

改前：
```go
driverRegistry := drivers.NewRegistry()
drivers.RegisterBuiltInDriversWithParsers(driverRegistry, parserConfigs)
drivers.RegisterBuiltInDriversWithParsers(drivers.GlobalRegistry(), parserConfigs)
```

改后（composition root 目标形态）：
```go
driverRegistry := drivers.NewRegistry()
drivers.RegisterBuiltInDriversWithParsers(driverRegistry, parserConfigs)
// 将 driverRegistry 显式注入 API、nodemgr、databus 等所有使用方
```

**效果**：
- 生产只构造并注册一个 Registry；
- 不再初始化或读取 `drivers.GlobalRegistry()`；
- 重复注册日志消失。
- 包级 `drivers.Get/List` 调用改为注入实例的 `registry.Get/List`。

### 阶段 0a-2：元数据修正

**改动 1**：`backend/internal/drivers/builtin.go:113` — OEM "路科" → "蓝控"

**改动 2-3**：`backend/internal/drivers/inverter_techfine.go:22-23`
- DeviceName: "Techfine GB3024 逆变器" → "泰琪丰 GB3024 逆变器"
- OEM: "Techfine" → "泰琪丰"

元数据修改必须有现有产品资料或原独立方案确认；若证据不足，保留当前文本并登记待决，不影响通用控制平台。

### 阶段 0a-3：保留并增强测试一致性

**改动**：`backend/internal/drivers/builtin.go:295-301`

现有 7 Driver 集合测试保留；新增生产 composition root 只构造/注册一次、所有 handler/manager 使用同一 Registry 指针的断言。

### 阶段 0a-4（本阶段强制）：依赖注入重构

彻底消除 `drivers` 包级 `globalRegistry` 在生产路径中的使用，所有 driver 访问通过构造时注入的 `*Registry`；测试可继续创建隔离 Registry。当前 master 约有 12 个包级 Get/List/GlobalRegistry 调用点，工作量应按实际调用点重新估算，不沿用旧文档的“28 处测试”。ActionProvider/Compiler 引入前该项必须完成。

---

# 26. 嘉佰达协议对照与修正

## 26.1 协议概述

嘉佰达 V19 软件板通用协议，支持 RS485/RS232/UART 接口，波特率 9600bps（可定制），大端模式。

### 帧结构

**主机发送**：
| 起始位 | 读取位 | 命令码 | 长度 | 数据内容 | 校验 | 停止位 | CALLBACKID |
|---|---|---|---|---|---|---|---|
| 0xDD | 0xA5-读/0x5A-写 | 寄存器地址 | 数据长度 | 数据内容 | 校验和取反+1 | 0x77 | 4BYTE(可能不存在) |

**BMS 响应**：
| 起始位 | 命令码 | 状态位 | 长度 | 数据内容 | 校验 | 停止位 | CALLBACKID |
|---|---|---|---|---|---|---|---|
| 0xDD | 寄存器地址 | 00=成功/0x80=失败 | 数据长度 | 数据内容 | 校验和取反+1 | 0x77 | 4BYTE(可能不存在) |

### 校验算法

校验范围的数据段内容 + 长度字节 + 命令码字节的校验和，然后取反加 1，高位在前，低位在后。

### 休眠处理

平台下发指令前需提前发送 `00 00 00 00`，延时 100ms 后再发送实际数据。BMS 必须支持抛弃 `00 00 00 00` 数据。

## 26.2 命令清单对照

### 当前 Driver 已实现的命令

| 命令码 | 功能 | Driver 状态 | 协议位置 | 对照结果 |
|---|---|---|---|---|
| 0x03 | 读取基本信息 | ✅ 已实现 (read_basic_info) | 3.1 节 | ✅ 一致 |
| 0x04 | 读取单体电压 | ✅ 已实现 (read_cell_voltage) | 3.3 节 | ✅ 一致 |
| 0x05 | 读取硬件版本号 | ✅ 已实现 (read_hardware_version) | 3.4 节 | ✅ 一致 |
| 0x0F | 读取综合信息 | ✅ 已实现 (read_comprehensive) | 第六节 | ✅ 一致 |
| 0xAA | 读取保护历史次数 | ✅ 已实现 (read_protection_count) | 3.2 节 | ✅ 一致 |
| 0xE1 | 控制 MOS | ✅ 已实现 (3 个 write 命令) | 第四节 | ⚠ 见 26.3 |

### 当前 Driver 未实现但协议定义的命令

| 命令码 | 功能 | 协议位置 | 是否需要工厂模式 | 风险等级 | 建议优先级 |
|---|---|---|---|---|---|
| 0x0A | 恢复出厂设置参数 | 7.1 节 | 否 | critical | Phase 4 |
| 0x0B | 清除历史次数 | 7.2 节 | 否 | high | Phase 4 |
| 0x0C | 测试 MOS 管 / 读取测试 MOS 状态 | 7.3/7.5 节 | 否 | high | Phase 4 |
| 0x0D | 自动测试 EDV 功能 | 7.6 节 | 否 | medium | 后续 |
| 0x0E | 复位 BMS | 7.7 节 | 否 | critical | Phase 4 |
| 0xE6 | 清除告警状态 | 7.10 节 | 否 | medium | 后续 |
| 0xF0 | 自定义属性（读写） | 7.8 节 | 否 | low | 后续 |
| 0xF1 | 寻车指令 | 7.9 节 | 否 | low | 后续 |
| 0xF2 | 读参数（保护/运行参数） | 7.11 节 | **是** | low | Phase 3 |
| 0xF3 | 写参数（保护/运行参数） | 7.11 节 | **是** | high | Phase 3 |
| 0xF5 | 强制均衡模式 | 7.4 节 | 否 | medium | 后续 |
| 0xF6 | 读取/写入电池内阻 | 第八节 | 否 | low | 后续 |
| 0xF7 | 修正时间 | 第八节 | 否 | low | 后续 |
| 0xF8 | 修改上报时间间隔 | 第八节 | 否 | low | 后续 |
| 0xFA | 充电时间设置 | 第十一节 | 否 | high | 后续 |
| 0xFB | 放电时间设置 | 第十二节 | 否 | high | 后续 |
| — | 读写 SN 码 | 第十节 | 是 | medium | 后续 |
| 0x10 | 设置标称容量 | 第五节 | 是 | high | 后续 |
| 0x11 | 设置循环容量 | 第五节 | 是 | medium | 后续 |

## 26.3 MOS 命令 (0xE1) 对照分析

### 协议原文

```
主机发送：DD 5A E1 02 YY XX CHECKSUM_H CHECKSUM_L 77
BMS 响应：DD E1 00 00 -- Checksum_H Checksum_L 77
```

**协议原文未要求工厂模式前置**。

### 当前 Driver 实现

```go
// jiabaida.go:622-637
{ID: "close_discharge_mos", ..., WriteData: "DD5AE1020002FF1B77", Description: "关闭放电MOS管（需工厂模式，一次性触发）"},
{ID: "close_charge_mos",   ..., WriteData: "DD5AE1020001FF1C77", Description: "关闭充电MOS管（需工厂模式，一次性触发）"},
{ID: "release_mos",        ..., WriteData: "DD5AE1020000FF1D77", Description: "释放所有MOS管（需工厂模式，一次性触发）"},
```

三个 MOS 命令均使用 YY=0x00（终端用户级优先级）。

### 架构设计中的假设

架构设计 18.2 节执行计划包含 enter/exit factory mode，与 Driver 注释一致。

### 对照结论

| 项目 | 协议原文 | Driver 代码 | 架构设计 |
|---|---|---|---|
| 工厂模式要求 | 未要求 | 标注"需工厂模式" | 包含 enter/exit factory |
| 优先级 | 支持 0x00/0xAA | 全部使用 0x00 | 默认 0x00；0xAA 需独立授权 |

**⚠ 需硬件验证**：
1. 在实际 BMS 硬件上测试不进入工厂模式直接发送 0xE1 命令，确认是否可正常工作；
2. 确认 YY=0xAA（运营端）是否被支持；
3. 若硬件不要求工厂模式，采用方案 A 简化执行流程；
4. 若硬件要求工厂模式（与协议文档不一致但实际行为如此），保持方案 B。

### 建议

- 默认保持 YY=0x00；YY=0xAA 只有站点显式启用独立 critical capability 后使用；
- 在硬件验证结果出来前，不预选方案 B，也不开放该动作；验证后选择实际步骤最少且语义正确的方案；
- 0xE1 必须以完整双 bit software policy 编译，禁止两个独立 setter 互相覆盖。

## 26.4 复位命令 (0x0E) 对照分析

### 协议原文

```
主机发送：DD 5A 0E 00 8118（固定码）CHECKSUM_H CHECKSUM_L 77
```

**⚠ 不一致**：长度字段为 0x00 但数据内容为 0x8118（固定码，2 字节）。根据帧结构，长度=0 时不应有数据内容。需硬件验证正确帧格式。

## 26.5 工厂模式（0x00/0x01）对照

| 操作 | 协议原文 | Driver 实现 | 对照 |
|---|---|---|---|
| 进入工厂模式 | DD 5A 00 02 56 78 FF 30 77 | `FactoryModeEnterCmd()` | ✅ 一致 |
| 退出工厂模式（读，不初始化） | DD 5A 01 02 00 00 FF FD 77 | `FactoryModeExitForRead()` | ✅ 一致 |
| 退出工厂模式（写，初始化参数） | DD 5A 01 02 28 28 FF AD 77 | `FactoryModeExitForWrite()` | ✅ 一致 |

## 26.6 参数读写 (0xF2/0xF3) 对照

协议 7.11 节明确流程：

```
进入工厂模式（指令00，数据0x5678）
→ 发送操作指令（0xF2 读 / 0xF2 写 / 0xF3 写）
→ 退出工厂模式（读参数：0x01 数据 0x0000；写参数：0x01 数据 0x2828）
```

**0xF2 参数数据段**（53 字节，读/写共用）：包含单体过压保护值、单体欠压保护值、充电过流保护值、放电过流保护值、短路保护值、充电过温保护值、放电过温保护值、充电低温保护值、放电低温保护值、充电过流延时、放电过流延时、短路延时、充电过温延时、放电过温延时、充电低温延时、放电低温延时、均衡开启电压、均衡压差、GPS 关机电压、GPS 关机延时、标称容量、循环容量、单体满充电压、单体过放电压、自放电率、100%对应电压、90%对应电压等。

**0xF3 参数数据段**（52 字节，读/写共用）：包含功能配置、NTC 配置、电池串数、检流阻值、短路保护及延时、硬件过流保护及延时等。

**参数块末尾 2 字节 CRC-16 (Modbus, 多项式 0xA001)**。Driver 已实现 `CRC16Modbus()` (`jiabaida.go:572-585`)。

**⚠ 优先级**：0xF2/0xF3 参数读写是 BMS 最复杂的操作，需要有 finally 的 bounded batch（Phase 3），且必须进入/退出工厂模式。它是启用设备端批次能力的明确需求，而不是建设通用工作流引擎的理由。

---

# 27. 代码证据索引

| 结论 | 证据 |
|---|---|
| Node/Channel/EdgeDevice/DeviceConfig 模型 | `backend/internal/models/models.go:15-185` |
| DeviceConfig.Operations | `backend/internal/models/models.go:172-175` |
| GPIO/PWM 不属于 Channel | `backend/internal/models/models.go:121-124`、`backend/internal/api/handler_device.go:34-50` |
| WriteCmd 后端编码 | `backend/internal/pendingwrite/pendingwrite.go:87-98`、`backend/internal/nodemgr/sender.go:173-190` |
| WriteCmd 固件解码 | `esp32-collector/components/msg_handler/handler_writecmd.c:36-65` |
| Channel 命令分队列 | `esp32-collector/components/bus_manager/bus_manager.c:347-385` |
| UART 执行与 WriteRsp | `esp32-collector/components/bus_worker/bus_worker.c:161-193` |
| SPI/I2C transact | `esp32-collector/components/bus_worker/bus_worker.c:250-283` |
| 现有命令队列与 TX 上限（depth=16, TX=128） | `esp32-collector/components/bus_dma/include/cmd_queue.h:22-63` |
| 各总线队列深度 | `esp32-collector/main/app_state.c:84-90` |
| 现有 scheduler 静态 Channel 和单 task 模型 | `esp32-collector/components/scheduler/scheduler.h:18-118`、`esp32-collector/components/scheduler/scheduler.c:31-38` |
| Periph RAM replay/collision 可参考模式 | `esp32-collector/components/msg_handler/handler_periph.c:58-76,220-260` |
| PendingWrite 等待 readback | `backend/internal/pendingwrite/pendingwrite.go:150-226` |
| DataReport 关联 PendingWrite | `backend/internal/databus/consumers_heavy.go:41-54` |
| Driver CommandTemplate | `backend/internal/drivers/command_template.go:3-26` |
| BMS MOS 命令 | `backend/internal/drivers/jiabaida.go:622-638` |
| BMS 工厂模式 helper | `backend/internal/drivers/jiabaida.go:553-568` |
| BMS CRC16Modbus | `backend/internal/drivers/jiabaida.go:572-585` |
| 逆变器控制和设置 | `backend/internal/drivers/inverter_techfine.go:876-1005` |
| Driver commands API | `backend/internal/api/handler_driver_commands.go:21-132` |
| `/execute` 操作查找与编译 | `backend/internal/api/handler_edge_device.go:569-681` |
| write fire-and-forget | `backend/internal/api/handler_edge_device.go:684-700` |
| verify 只 warning | `backend/internal/api/handler_edge_device.go:702-747` |
| write 返回 sent/success | `backend/internal/api/handler_edge_device.go:749-759` |
| read PendingWrite 路径 | `backend/internal/api/handler_edge_device.go:761-856` |
| operation history 占位 | `backend/internal/api/handler_edge_device.go:560-567` |
| OperationConfig 后端字段 | `backend/pkg/parser/template.go:24-49` |
| 前端 OperationDef 字段 | `frontend-shared/src/api/deviceConfig.ts:9-26` |
| 前端动态操作原型 | `frontend-shared/src/views/edge-device/shared/OperationButtons.vue:1-241` |
| 参数化 write 绕过确认 | `frontend-shared/src/views/edge-device/shared/OperationButtons.vue:163-202` |
| 写操作只提示已发送 | `frontend-shared/src/views/edge-device/shared/OperationButtons.vue:217-229` |
| WS 类型订阅和重连 | `frontend-shared/src/stores/websocket.ts:77-209` |
| 详情页硬编码专用页面 | `frontend-shared/src/views/edge-device/EdgeDeviceDetailRouter.vue:32-65` |
| 旧协议文档漂移 | `docs/协议/二进制帧协议.md:137-159` |
| SecurityAuditEvent | `backend/internal/models/security_audit_event.go:5-20` |
| 审计写入器 | `backend/internal/audit/audit.go:16-63` |
| PeriphResult 可参考模式 | `backend/internal/nodemgr/handler_response.go:443-460` |
| Driver 单实例注入 | `backend/cmd/server/main.go:100-126` |
| LK-TH01 OEM（蓝控） | `backend/internal/drivers/builtin.go:113` |
| Techfine 元数据错误 | `backend/internal/drivers/inverter_techfine.go:21-25` |
| RegisterBuiltInDrivers 已统一 7 Driver | `backend/internal/drivers/builtin.go:360-401`、`backend/internal/drivers/drivers_test.go:478-526` |
| raw channel write | `backend/internal/api/handler_device.go:800-850` |
| REST terminal write | `backend/internal/api/handler_terminal.go:42-76` |
| change-address 发送后立即更新 DB | `backend/internal/api/handler_edge_device.go:1047-1059` |
| legacy TX 截断与非法 bus fallback | `esp32-collector/components/bus_manager/bus_manager.c:347-386` |
| UART RX timeout 固定 1000ms | `esp32-collector/components/bus_worker/bus_worker.c:115-125` |
| ResourceReport 未持久化 boot/report time/engine revision | `backend/internal/nodemgr/handler_resources.go:545-684`、`backend/internal/models/models.go:19-64` |
| 嘉佰达 V19 协议摘录 | 当前仅见本文整理；原 PDF/OCR 尚未归档，列为 B12 |

---

# 最终结论

EHomeSystem 已具备 Channel 透明传输、request_id、PendingWrite、DeviceConfig.Operations、Driver 命令、WebSocket，以及 GPIO/PWM 已验证的请求关联模式；但当前 PendingWrite 不是可靠业务执行域，legacy WriteCmd 也尚不具备危险动作所需的严格解码和 replay 保护。

正确的演进方向不是为雨量计、BMS 和逆变器分别增加一套控制代码，而是将现有能力收敛为：

```text
当前 master 安全止血与 Driver 依赖注入 (Phase 0-A)
→ 协议证据与 C6/S3 性能基线 (Phase 0-B)
→ 统一动作定义
→ 后端权威校验和风险策略
→ 最小 CommandExecution/Attempt + Outbox/Inbox
→ Channel 统一调度和排他
→ ESP32 轻量 ChannelCmdV2（默认单步）
→ 首个只读 E2E 和前端切换
→ 第一个可 readback setter 和 SettingState
→ 仅在必要场景使用 bounded ChannelBatchCmd
→ 目标设备协议 ACK
→ 读回/遥测/重连验证
→ confirmed state
→ WebSocket 通知
→ 操作历史和安全审计
```

**关键前置条件**：

1. **Phase 0-A** 必须立即执行；它不再是约 12 行的单点修复，而是包含 Registry DI、legacy strict decode、所有物理 TX 路径分类和 raw diagnostics 封堵；
2. **Phase 0-B（嘉佰达协议对照与性能基线）**必须在任何 BMS 控制实现前完成 — 特别是 MOS 工厂模式假设需硬件验证；
3. request hash 与 wire digest、单步状态机和取消语义必须在数据库迁移前冻结；
4. Node boot/report freshness/command engine revision 必须在 Action availability 生效前持久化；
5. 在封堵全部旧写旁路、完成业务状态机、设备端重放保护和节点 topic 隔离之前，不应开放雨量清零、BMS MOS/重启或逆变器复位等破坏性控制。

**嘉佰达协议对照关键发现**（详见第 26 节）：

| 发现 | 影响 | 行动 |
|---|---|---|
| 0xE1 MOS 协议原文不要求工厂模式，Driver 代码标注"需工厂模式" | 架构设计执行计划需根据硬件验证结果调整 | Phase 0-B 硬件验证，确定方案 A/B |
| 0x0E 复位帧长度字段与数据内容不一致 | 可能导致复位命令发送失败 | Phase 0-B 硬件验证正确帧格式 |
| 当前 Driver 缺失 15+ 个协议命令 | 功能不完整，需分阶段补充 | 按 Phase 5 之后的动作门禁逐步实现 |
| YY=0x00 用户级 vs 0xAA 运营端 | 0xAA 会提升控制权限 | 默认 0x00；0xAA 需独立 critical capability |
| 0xF2 参数块含 CRC-16 (Modbus) | 读写参数需要 CRC 校验 | Driver 已实现 CRC16Modbus，正确 |
| 0xF2/0xF3 参数读写必须工厂模式 | 需要有 finally 的 bounded batch | Phase 5 按实际需求实现 |
