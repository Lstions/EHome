# 边缘设备架构质量评审 — BMS 驱动篇（jiabaida.go）

- 日期：2026-08-16
- 范围：`backend/internal/drivers/jiabaida.go`（1818 行，24 个 ControlAction）及其依赖的抽象层：
  `drivers/action.go`（能力接口）、`drivers/registry.go`（Driver 接口）、`drivers/command_template.go`、
  `deviceaction/definition.go`（注册/校验/默认启用推导）、`commandexec/channel_cmd_v2_transport.go`（信封/digest）、
  `commandexec/service.go`（VerifyFinal）、`esp32-collector/components/bus_worker.c`（固件批量执行）
- 性质：只分析不实现。行号均为当前工作区实测。

---

## 1. 总体结论（先给答案）

**骨架优秀，能支撑长远开发，属于同类边缘设备控制框架中少见的严谨设计。**
核心安全模型（声明式目录 + 服务端编译 + 证据门禁 + at-most-once + 读回对账）方向正确、层次清晰。
当前不存在会阻断演进的结构性错误。

但有 **5 笔需要在下一步开发前偿还的债**（详见 §5）：
1. 批量响应信封解码器放在驱动层且以 jiabaida 命名——放错抽象层；
2. `verification: readback` 在 set_mos_policy 上是弱化实现（只解析、未做位级对账）；
3. F2/F3 写后对账用整块 `bytes.Equal`，保留字节的非零回读会永久判失败；
4. ACK 步的校验不验校验和；
5. F2/F3 字段表在三处手工重复（schema/编译/解析），协议升级成本高。

另有 2 个中期演进压力点：参数 schema 缺数组表达力（30 个 resistance 标量 hack 已触及上限）、
可选能力接口数量膨胀（10+ 个）需要收敛策略。

---

## 2. 架构分层全景（实测链路）

```
HTTP 请求（actionID + params JSON）
  └─ deviceaction.Registry（definition.go:445 NewBuiltInRegistry）
       ├─ ControlActions() 声明 → schemaFromDriver 校验（schema 标量 only）
       ├─ 能力探测：Compiler / PlanCompiler / Verifier / AddressXxx 逐个类型断言（:469-529）
       ├─ 默认启用推导：set/reset 且 verifier+Verification+无 AvailabilityCode ⇒ 强制 enabled（:543-549）
       └─ Definition.Register fail-closed 校验（:100-160：MaxSteps≤8、AtMostOnce⇒high/critical、
          set 必须有 verifier、参数化必须有 compiler、bounded 必须有 plan 源）
  └─ commandexec（channel_cmd_v2_transport.go:111-176）
       ├─ CanonicalizeParams 复验持久化参数（service.go:409-421 VerifyFinal 同样复验）
       ├─ bounded_sequence ⇒ CompilePlanForAddress(params, edge.HardwareID) + 节点能力上限核对
       ├─ WireDigest 绑定 bootID/channel/deadline/step 字节 → PayloadDigest 防错配
       └─ MQTT → ESP32
  └─ 固件 bus_worker.c:806-848：逐步 TX→turnaround→collect→[kind|len_le|resp] 编码回传
  └─ VerifyFinal（service.go:403-425）：edge 身份五元组核对 + action 版本冻结 + 定义 VerifyForAddress
       └─ jiabaida.VerifyControlAction → decodeJiabaidaBatchRaw → 各步 ACK/读回对账 → ParseData 投影
```

分层结论：**职责切分正确**——驱动只做声明+字节编解码+响应绑定；
传输/重试/身份/digest 全在 commandexec；门禁推导在 deviceaction。
驱动无法绕过服务端校验制造命令帧（CompiledControlStep 注释明确"never decoded from a browser request"，action.go:64-65）。

---

## 3. 优点（按维度，附行号）

### 3.1 安全与正确性门禁 —— 9.5/10
- **声明式 Action Catalog**：ControlAction 纯声明（action.go:5-8 注释：no database/transport/authorization/user bytes），
  参数标量 schema 服务端二次校验（definition.go:462 schemaFromDriver → schema.Validate）。
- **能力接口可选化 = fail-closed**：parsing-only 驱动不实现 ControlActionProvider 就不可能暴露控制面
  （action.go:58-62）；参数化动作无 compiler 直接 panic 拒绝注册（definition.go:469-472）。
- **编译/验证分离且版本冻结**：VerifyFinal 校验 definition.Version == execution.ActionVersion、
  CanonicalizeParams 后与持久化 JSON 逐字节比对（service.go:417-421）——重放/降级路径被关死。
- **at-most-once 语义有载体**：AtMostOnce + 注册期约束"AtMostOnce ⇒ Risk 必须 high/critical"（definition.go:114-116），
  对 RS485 半双工总线上"重试=二次物理写"的风险建模到位。
- **证据门禁（AvailabilityCode）**：15 个未实机验证的动作 protocol_unverified 可见但不可执行，
  且注册推导保持 disabled（definition.go:543-549）。"冻结 schema、开放执行待证据"的节奏是工业级做法。
- **工厂模式 RequiresFinally**：F2/F3/SN 写序列即使中途失败也强制退出工厂模式（CompiledControlPlan.RequiresFinally），
  读回在 0x2828 退出（参数生效点）之前完成的顺序有明确协议依据注释（jiabaida.go:589-592）。
- **地址绑定接口族**：ControlActionAddressCompiler/PlanCompilerForAddress/AddressVerifier 三级防共享总线误广播
  （action.go:99-123, 137-143）。
- **WireDigest**：digest 绑定 bootID/channel/deadline/实际 step 字节而非仅逻辑请求哈希（transport.go:180-200 注释）。

### 3.2 协议解析质量 —— 8/10
- ParseError 结构化错误码 + Raw 保留（jiabaida.go:1090-1120 区段），可分类告警。
- 校验和双模式正确：发送帧 CMD+LEN+DATA / 响应帧 LEN+DATA，注释声明"对照 8 个已知帧验证"。
- 错误响应帧（0x80/81/82）在解释 byte1 为命令字之前先行检测，含可选 CALLBACKID 的长度推导位置处理。
- 可变长度段（0x03 NTC 数、0x0F 温度数组+电芯数）边界防御（`tempOffset+1 < len(data)`）。
- decodeJiabaidaBatchRaw 严格拒绝 trailing bytes / 越界 step 数（>8）/ 零长度步。
- VerifyControlAction 把响应绑定到发起动作（"wrong-device or stale response"注释，:822-824），
  读动作侧 cmd 字节白名单匹配（:872-878）。

### 3.3 接口设计 —— 7.5/10
- Driver 基础接口 7 方法（registry.go:17-31）胖但都是必要元数据；控制面全部走可选接口，扩张不破坏旧驱动。
- XxxProvider/XxxCompiler/XxxVerifier 命名平行，语义可预测。
- CommandAwareDriver 解决同格式响应歧义（Techfine HPV/HPVB）且注释明确禁止 fallback（registry.go:88-104）。
- InitStep.Role 稳定语义标签替代脆弱的步骤名匹配（command_template.go:26-36）。

### 3.4 测试 —— 8.5/10
- 1290 行测试对 1818 行实现，37 个测试函数；含 golden vector、错 step count、错 ACK 命令、
  门禁动作越权启用检测（jiabaida_test.go:525-560 明确锁定默认启用/禁用矩阵）。

---

## 4. 缺陷与风险（分级）

### P0（正确性，实机启用前必须修）

**P0-1 F2/F3 整块对账在保留字节非零时永久失败**
- 位置：verifyExtendedBatchActions → `bytes.Equal(gotData, wantBlock)`（jiabaida.go write_protection/system_parameters 分支）。
- compileF3Block 对未声明区间（16-19、32-47）零填充；真机若在保留区返回任意非零字节，
  写成功也会被判"readback does not match"。F2 声明了全部 51 字节所以风险低，F3 风险实际存在。
- 方向：对账只比较已声明字段的字节区间（field-mask 比较），保留区忽略。

**P0-2 set_mos_policy 声明 readback 但未做位级对账**
- 位置：VerifyControlAction set_mos_policy 分支（:826-845）：只验证 ACK 帧形 + ParseData(readback) 成功，
  未断言读回 fet_status 的软件关闭位 == 请求参数（charge_software_closed/discharge_software_closed）。
- 后果：MOS 写失败但 BMS 仍返回旧状态帧时，动作会判 SUCCEEDED——与 `Verification: "readback"` 的目录声明不符。
- fet_status 位语义（哪个 bit 对应充电/放电软件关断）目前代码中完全缺失，需从 V19 协议补齐后加断言。
- 同类：bms_restart 的 observation 语义注释已诚实声明"离线窗口/uptime 属操作员职责"，可接受；但 MOS 这个是声明与实现不符。

**P0-3 ACK 步校验不验校验和**
- 位置：jiabaidaExpectZeroAck（:1150-1157）只查位置 0/1/2/3/6，跳过 CHK 字节（4/5）。
- 后果：RS485 噪声翻转 ACK 中部字节仍可通过；写步 ACK 的完整性实际未证明。
- 数据步有 jiabaidaResponseData 全帧校验，唯独 ACK 步裸奔。

### P1（结构债，下一驱动接入前应还）

**P1-1 批量信封解码器放错层 + 误导性命名**
- decodeJiabaidaBatchRaw（:1793-1818）解码的是 ChannelCmdV2 bounded-plan 通用信封，
  与嘉佰达毫无关系——固件 bus_worker.c:806-848 对任何驱动都产生同一格式。
- 后果：第二个 bounded 驱动（如 Modbus 写计划）出现时要么复制此函数、要么跨包引用 jiabaida 私有函数。
- 方向：更名 decodeBatchPlanEnvelope 并上移到 deviceaction 或 commandexec 层，作为 bounded 响应的标准解码器。

**P1-2 F2/F3 字段表三处重复**
- 同一字段集手工维护三份：jiabaidaF2Parameters（schema，~:300-360）、compileF2Block 的 offset 表（:436-490）、
  parse0xF2 的读投影（含换算 ×10、/100、0.1K→°C）。加一个字段要改 3 处 + 对账 mask。
- 方向：单一张 field descriptor（name/offset/width/scale/unit/min/max）驱动三者生成。测试已锁定行为，重构安全。

**P1-3 黄金帧字面量散布**
- 0x03 读帧 `DD A5 03 00 FF FD 77` 以字面量出现在 jiabaidaReadAction、GetCommandTemplates（WriteData hex）、
  以及 10+ 个 plan 步内联。jiabaidaReadFrame(0x03) helper 已存在却未被 plan 复用。
- 任何一处手误只能靠测试抓。方向：统一从 jiabaidaReadFrame 构造 + 常量注册表。

### P2（一致性/完备性，择机）

- **P2-1 GetSensorDefinitions 与解析输出不同步**：parse0x03 输出 software_version、temperature_N、
  parse0x04 输出 cell_voltage_N，GetSensorDefinitions（:1048-1061）只列 12 项静态定义。
  若该列表用于 HA Discovery，温度与单体电压不会被发现；若仅展示用途则无害——需确认消费者。
- **P2-2 parse0xAA 静默截断**：`if i*2+1 >= len(data) { break }` 无错误——截断的保护计数帧返回部分数据且判成功，
  与其他 parser 的 ErrDataTooShort 风格不一致。
- **P2-3 parse0x0F trailer 未解析**：布局注释声明 current_state/charge_capacity/runtime/sequence/humidity，
  实现静默跳过（注释诚实但功能缺位；0x0F 是唯一含 runtime 的命令，UI 若需要运行时长这里是缺口）。
- **P2-4 parse0x05 无长度校验**：空 DATA 返回空串 hardware_version 而非 ErrDataTooShort。
- **P2-5 命令字歧义残留**：verifyJiabaidaChecksum 用 raw[1]∈{A5,5A} 判发送帧。已文档化的命令字无 A5/5A 响应，
  实际风险≈0；但若固件某日回传未文档化命令字 0xA5 的响应会被误判为发送帧。ParseData 末段 default fail-closed 兜底，可接受，建议注释标注此假设。

### 观察项（非缺陷，记录语义）

- **Enabled 双事实源**：驱动声明 `Enabled:false`（3 处读动作等），registry 对"set/reset+完整链+无门禁"强制
  `enabled=true`（definition.go:543-549，默认启用原则 2026-08-14）。行为被测试锁定且注释详尽，
  但"驱动写 false、系统开 true"对后来者反直觉——建议驱动侧字段语义改为"rollout ready 声明"或在目录文档中显式说明推导规则优先。
- **单步/计划双编译器并存**：CompileControlAction（单步）目前仅 set_mos_policy 使用且其正式执行走
  CompileControlActionPlan；单步接口已是事实遗留，第二个参数化驱动出现前可考虑合并。

---

## 5. 长期演进压力点（能否支撑长远开发的真正考题）

1. **参数 schema 数组表达力**（最现实的天花板）：write_internal_resistance 用 resistance_1..resistance_30
   30 个标量参数绕过 schema 标量限制（注释诚实，:232-236）。这是权宜而非方案——Modbus 块写、多通道设置、
   均衡策略表迟早需要 `array`/定长向量类型。schemaFromDriver + schema.Validate 是集中扩展点，改动面可控，
   但应在第二个数组需求出现前定 schema 版本策略（现有 Version 字段可承接）。
2. **能力接口收敛**：可选接口已达 10+（Provider/Compiler/PlanCompiler/Verifier × 普通/Address 两族 + Calibration/Command/Template/InitSequence）。
   目前靠命名平行维持可读性；若再增 2-3 个横切能力（如 OTA、诊断），建议引入能力聚合结构体或按 Protocol Families 分组文档，防止类型断言链在 NewBuiltInRegistry 里继续膨胀（当前已 60 行断言，definition.go:457-529）。
3. **协议版本化**：V19→V20 时 F2/F3 字段表变动 + 黄金向量更新是最大工作量；P1-2 的表驱动改造是把该成本从
   O(3×字段数) 降到 O(字段数) 的关键。CurrentActions 已有 Version 字段，目录层版本冻结机制（VerifyFinal 拒版本错配）已就绪。
4. **驱动文件规模**：1818 行/24 action 尚可维护（分节注释清晰），但按当前节奏（8-16 新增一批 action）
   下一批就会越过 2500 行。建议按 catalog（声明）/compiler（编帧）/verifier（对账）/parser（解析）拆四文件，
   无接口变更风险。
5. **固件信封演进协调**：step≤8、resp≤256B、超时上限等由 node capability 上报并逐 step 核对
   （transport.go:128-134），这条能力协商链路是双向演进的正确基础，保持即可。

---

## 6. 多维评分

| 维度 | 评分 | 依据 |
|---|---|---|
| 安全/门禁设计 | 9.5/10 | 声明式目录+编译验证分离+证据门禁+at-most-once+wire digest；扣分：readback 弱化实现（P0-2）、ACK 不验校验和（P0-3） |
| 协议解析正确性 | 8/10 | 校验和双模式/错误帧先行/边界防御扎实；扣分：F3 整块对账（P0-1）、静默截断（P2-2/3） |
| 抽象/接口设计 | 7.5/10 | 可选能力接口模式正确；扣分：信封解码放错层（P1-1）、接口数膨胀、双编译器并存、Enabled 双源 |
| 可维护性/演进 | 7/10 | 测试锁定好、注释质量高；扣分：三处字段表（P1-2）、字面量散布（P1-3）、单文件规模临界 |
| 测试 | 8.5/10 | 37 测试/golden vector/负路径/门禁矩阵；缺：fet_status 位对账测试无从写起（因实现缺失） |
| **综合** | **8/10** | 骨架优秀、能支撑长远开发；P0 三项属"最后一公里对账完整性"，修完可达 9 |

---

## 7. 改进路线建议（仅设计，不实现）

1. **第一批（实机解封前，P0）**：
   a. jiabaidaExpectZeroAck 增加全帧校验和验证；
   b. F2/F3 对账改字段区间比较（忽略保留区）；
   c. 从 V19 协议补 fet_status 位定义，set_mos_policy verifier 增加位级断言 + 对应负路径测试。
2. **第二批（下一 bounded 驱动前，P1）**：
   a. decodeBatchPlanEnvelope 上移共享层；
   b. F2/F3 field-descriptor 表驱动，三处生成；
   c. 黄金帧统一由 frame builder 构造。
3. **第三批（schema 演进窗口）**：ParameterSchema 增加定长 array 类型（带 item 约束），
   resistance_30 迁移为 array 参数，action Version 升 v2。
4. **持续**：GetSensorDefinitions 消费者确认；驱动文件按 catalog/compiler/verifier/parser 拆分。

---

## 8. 证据索引（关键行号速查）

| 主题 | 位置 |
|---|---|
| ControlAction 声明契约 | drivers/action.go:9-45 |
| 可选能力接口族 | drivers/action.go:58-143 |
| 注册期 fail-closed 校验 | deviceaction/definition.go:100-160 |
| 默认启用推导 | deviceaction/definition.go:530-549 |
| WireDigest | commandexec/channel_cmd_v2_transport.go:180-200 |
| VerifyFinal 版本/身份冻结 | commandexec/service.go:403-425 |
| 固件批量信封 | esp32-collector/components/bus_worker.c:806-848 |
| 批量信封后端解码 | drivers/jiabaida.go:1793-1818 |
| ACK 弱校验 | drivers/jiabaida.go jiabaidaExpectZeroAck |
| F2/F3 整块对账 | drivers/jiabaida.go verifyExtendedBatchActions |
| set_mos_policy 弱 readback | drivers/jiabaida.go VerifyControlAction :826-845 |
| 三处字段表 | jiabaidaF2Parameters / compileF2Block / parse0xF2 |
| resistance 30 标量 hack | jiabaida.go jiabaidaResistanceParameters 注释 |
