# 嘉佰达 BMS 协议写能力重新分析（基于协议原件）

> 触发：少爷指出"可以写保护参数"，要求按协议重新分析 BMS 边缘设备功能。
> 证据来源（三方对照）：
> 1. 协议原件 OCR：/mnt/storage/Downloads/嘉佰达软件板通用协议20220509.pdf_by_PaddleOCR-VL-1.6.md（1033 行，V19，31 页）
> 2. 备份原件：/mnt/storage/群晖-备份/气象站/嘉佰达软件板通用协议20220509.md（3558 行）
> 3. 仓库门禁摘录：docs/协议/嘉佰达软件板通用协议20220509.md（SHA-256 3904ab49…，2026-07-19 审计）
> 4. 驱动源码：backend/internal/drivers/jiabaida.go（879 行，main）

## 0. 结论先行

少爷记得对：**协议明确支持写保护参数（0xF2 写）与写系统参数（0xF3 写）**，且写能力远不止这两条——
原件共定义 13 类写/控制指令。当前驱动只启用了 set_mos_policy（0xE1），其余写能力要么已实现但
fail-closed gate（0x0E 复位），要么**根本未注册进 Action Catalog**（F2/F3 写、F5、F6 写、F7、F8、FA、FB、SN 写等）。
这是当时的审慎决策（缺真机黄金向量），不是协议没有。

## 1. 写保护参数（0xF2 写）——协议原文事实

### 1.1 完整流程（原件 §7.11，V11 版本增加）

```
1. 进入工厂模式   DD 5A 00 02 56 78 FF 30 77   → 响应 DD 00 00 00 … 77
2. 写入参数(0xF2) DD 5A F2 <LEN=53> <53字节参数块> CHK_H CHK_L 77
                  → 成功响应 DD F2 00 00 CHK 77（长度 0）
3. 退出工厂模式   DD 5A 01 02 28 28 FF AD 77   → 响应 DD 01 00 <状态> … 77
                  （2828 = 退出并"初始化参数"，即使写入生效；
                    读参数退出用 0000，不改变参数本身）
```

- 未进工厂模式就写 → 响应状态 0x81（操作错误/无效操作，原件 §状态位说明）
- 通用校验和：覆盖 CMD+LEN+DATA，逐字节求和后取反加 1，高字节在前
- 参数块内部另有 CRC-16（初值 FFFF、多项式 A001、LSB-first），只用于参数数据块，不替代通用帧校验和

### 1.2 F2 参数块 53 字节布局（原件逐字节摘录）

| 字节 | 字段 | 单位 | 字节 | 字段 | 单位 |
|---|---|---|---|---|---|
| 0-1 | 单体过压保护值 | mV | 26-27 | 充电低温释放值 | 0.1K |
| 2-3 | 单体过压释放值 | mV | 28-29 | 放电高温保护值 | 0.1K |
| 4-5 | 单体欠压保护值 | mV | 30-31 | 放电高温释放值 | 0.1K |
| 6-7 | 单体欠压释放值 | mV | 32-33 | 放电低温保护值 | 0.1K |
| 8-9 | 整组过压保护值 | 10mV | 34-35 | 放电低温释放值 | 0.1K |
| 10-11 | 整组过压释放值 | 10mV | 36 | 充电高温延时 | S |
| 12-13 | 整组欠压保护值 | 10mV | 37 | 充电低温延时 | S |
| 14-15 | 整组欠压释放值 | 10mV | 38 | 放电高温延时 | S |
| 16 | 单体过压延时 | S | 39 | 放电低温延时（见歧义②） | S |
| 17 | 单体欠压延时 | S | 40-41 | 充电过流保护值（max 32676=326.76A） | 10mA |
| 18 | 整组过压延时 | S | 42 | 充电过流保护延时 | S |
| 19 | 整组欠压延时 | S | 43 | 充电过流释放延时 | S |
| 20-21 | 充电高温保护值 | 0.1K | 44-45 | 放电过流保护值（补码） | 10mA |
| 22-23 | 充电高温释放值 | 0.1K | 46 | 放电过流保护延时 | S |
| 24-25 | 充电低温保护值 | 0.1K | 47 | 放电过流释放延时 | S |
|  |  |  | 48 | 短路保护及延时（见原件细则） |  |
|  |  |  | 49 | 硬件过流保护及延时 |  |
|  |  |  | 50 | 短路释放时间 | S |
| 51-52 | CRC-16 校验码 |  |  |  |  |

⚠ 与驱动 parse0xF2（读，jiabaida.go:682-711）逐字节对照：**读写布局完全一致**
（cell_ov_protect→data[0:2] … short_circuit_release→data[50]，len≥53）。
写编译器可直接复用读解析的逆映射，字段语义无需重新逆向。

### 1.3 原件内部歧义（当时 gate 的直接原因，门禁文档 §F2/F3 记录）

① **CRC 覆盖范围自相矛盾**：原文写"校验对象为 BYTE0~BYTE96"，但参数块只有 53 字节
   （BYTE0~BYTE52）。合理推断是 BYTE0~BYTE50（51 个参数字节），但无真机向量佐证。
② **OCR 字段串位**：BYTE39 在 OCR 中重复出现"充电低温延时"（应为放电低温延时）；
   F2/F3 小节标题与命令码互相矛盾（"读取参数(0xf3)"小节的主机帧写的是 DD A5 F2 00，
   校验 FF0D；响应命令码也写成 0xF2）。原件排版本身混乱，不能盲抄。
③ 无真实设备完整 F2/F3 请求/响应黄金向量、CRC 字节序、读回捕获。

## 2. 写系统参数（0xF3 写）

流程与 F2 相同（进工厂 → DD 5A F3 <LEN=52> <52字节块> CHK 77 → 退出 2828）。
52 字节块对应驱动 parse0xF3（jiabaida.go:717-738）：功能配置/NTC配置/串数/分流器电阻/
均衡启动电压/均衡压差/GPS关机电压+延时/标称容量/循环容量/满电空电电压/自放电率/
SOC100/SOC0 电压 + 尾部 CRC16。同样存在歧义①②③。

## 3. 协议写/控制指令全景 vs 驱动实现对照

| 指令 | 协议能力 | 驱动现状（jiabaida.go） |
|---|---|---|
| 0xE1 MOS 控制 | 写：双 bit 关闭位 + 用户/运营优先级（V10） | ✅ 已实现且 Enabled:true，实机验证 SUCCEEDED（唯一可用写） |
| 0x0E 复位 BMS | 写：DD 5A 0E 00 8118 CHK 77 | 🔒 已实现 compiler+verifier，gate：critical/observation，缺真机证据 |
| 0xF2 参数读 | 工厂模式读 53 字节 | 🔒 仅注册 read_protection_parameters，protocol_unverified |
| 0xF2 参数写 | **写 53 字节保护参数**（本报告 §1） | ❌ 未注册任何写 action |
| 0xF3 参数读/写 | 读/写 52 字节系统参数 | 🔒 读已注册；❌ 写未注册 |
| 0x0C 测试 MOS / 读测试 MOS 状态 | 写+读 | ❌ 未实现 |
| 0xF5 强制均衡 | 写：DD 5A F5 02 00 01 CHK 77 | ❌ 未实现 |
| 0xF1 寻车 | 写：0x1800 关 / 0x1801 开（蜂鸣 30S） | ❌ 未实现 |
| 0xF6 内阻读/写 | 读 N×2B；写固定 60B（30 串×2B，0.1mΩ 带符号） | 🔒 读解析已有（parse0xF6:744）；❌ 写未注册 |
| 0xF7 静态修正时间 | 写：2 字节，单位分钟 | ❌ 未实现 |
| 0xF8 上报间隔 | 写：6 字节 = 静态/充电/放电三个间隔（S） | ❌ 未实现（注意：与平台侧"指令频率"语义重叠，需产品界定） |
| 0xFA 充电时段 | 写：4 字节 = 延迟+时长（S），完成后 BMS 主动上报 DD FA 00 02 55 AA FE FF 77 | ❌ 未实现 |
| 0xFB 放电时限 | 写：3 字节 = 使能+天数，到期自动断放 | ❌ 未实现（高危：远程断电语义） |
| SN 码读/写 | 工厂模式 ASCII ≤31B | 🔒 读解析已有（parse0xA2:764）；❌ 写未注册 |
| 0x00/0x01 工厂模式进出 | 前置工作流 | ✅ 帧 helper 已存在（FactoryModeEnterCmd/ExitForRead/ExitForWrite，:781-795），未接 Action 链 |
| 0xEE 心跳（GPS） | BMS↔中控 EE 帧 | N/A（非主机职责） |

读侧（0x03/0x04/0x05/0x0F/0xAA）维持前次分析结论：解析完整，5 条轮询模板可调度，
5 个读 action 仍 Enabled=false 等真机冻结。

## 4. 为什么当前没有写保护参数（历史决策，非遗漏）

门禁文档（docs/协议/嘉佰达软件板通用协议20220509.md §F2/F3）2026-07-19 明确记录：
"没有真实设备的完整 F2/F3 请求/响应黄金向量、CRC 字节序或读回捕获前，不实现参数写入、
batch 或工厂模式物理动作。" 同日 C6 UART0 模拟验证只覆盖了 5 条读帧，未授权任何写。

## 5. 若要实现写保护参数——启用条件（按既定 fail-closed + 默认启用原则）

1. **Action 形态**：bounded_sequence 至少 4 步：进工厂(0x00) → 写 F2 → 退出(2828 初始化)
   → 读回 F2 对账。Verification=readback，Risk 建议 critical（改保护参数=改电池安全边界），
   AtMostOnce。当前 F2/F3 读 action MaxSteps=3 不够，写需独立 action。
2. **解冻前置**：先用真机（或高保真模拟器扩展 0xF2 写响应 + CRC 行为）产出黄金向量，
   消解歧义①（CRC 覆盖 BYTE0~BYTE50 还是别的范围）与歧义②（BYTE39 字段归属）。
   模拟器 scripts/uart0_bms_rain_simulator.py 目前只支持读帧与 0xE1，需扩展。
3. **对账语义**：写后读回 53 字节逐字段比对（不能只信 DD F2 00 00 ACK）；退出帧 2828
   的响应状态码也要校验。失败必须可恢复（重读原参数快照留证）。
4. **安全门禁**：参数范围校验（编译期拒绝越界值，如充电过流 >326.76A）、操作历史留痕、
   recent_auth（已有机制复用）。
5. **产品拍板**：写保护参数属"配置电池安全边界"，是否对 operator 角色开放、是否需要
   双人复核，需少爷定。0xF8/0xFA/0xFB 与平台职责重叠或有断电风险，建议单独立项评估。

## 6. 修正前次分析

前次《BMS 边缘设备功能分析》说"受控操作共 9 个"——按协议原件，**协议层写能力共 13 类**，
驱动仅实现 2 类（E1 启用、0E gate），另有 11 类未注册。"BMS 没有写保护参数能力"的隐含
结论不成立，正确表述是：**协议支持，驱动按 fail-closed 未实现/未启用**。
## 7. 实现状态更新（2026-08-16）

本轮把协议层 13 类写能力在驱动侧全部落地为 fail-closed 受控 action，
并扩展模拟器支持工厂模式与写帧响应，为真机解冻铺路。

### 7.1 驱动侧（backend/internal/drivers/jiabaida.go）

新增 17 个 catalog action（全部 Enabled=false + AvailabilityCode=protocol_unverified，
真机证据冻结前不可用）：

| 类别 | Action | 协议命令 | 形态 | 对账 |
|---|---|---|---|---|
| 工厂读 | read_protection_parameters / read_system_parameters | 进工厂→F2/F3 读→退工厂(0000) | bounded 3 步, RequiresFinally | readback |
| 工厂写 | write_protection_parameters / write_system_parameters | 进工厂→F2/F3 写→读回→退工厂(2828) | bounded 4 步, RequiresFinally | 逐字节读回对账 + write_ack |
| MOS 测试 | test_charge_mos / test_discharge_mos | 0C 写 0001/0002 + 读 0C | bounded 2 步 | readback（Risk=high, AtMostOnce：测试注入负载电流） |
| 均衡/寻车 | force_balance / find_car | F5 / F1(0x1801/0x1800) | bounded 2 步 | readback / ack |
| 告警/EDV | clear_alarm / auto_test_edv | E6(0x1881) / 0D | bounded 2 步 | ack |
| 配置 | write_custom_attributes / write_internal_resistance / write_sn | F0 / F6(60B) / A2(进工厂链) | bounded 2~4 步 | 逐字节/SN 读回对账 |
| 时间 | set_static_correction_time / set_report_interval / set_charge_time_window / set_discharge_time_limit | F7 / F8 / FA / FB | bounded 2 步 | ack |

关键实现点：
- **F2/F3 写读回逐字节对账**：jiabaidaResponseData 校验响应帧并提取 DATA 段，
  与 compileF2Block/compileF3Block 产出的块 bytes.Equal 比对，不满足即拒绝
  （不能只信 DD Fx 00 00 ACK，落实本文 §5.3 对账语义）。
- **write_sn 读回对账**：读回 serial_number 与写入值字符串比对。
- **内阻参数标量化**：deviceaction catalog schema 仅支持标量参数
  （schema.go 只接受 string/boolean/integer/number），故固定 30 串内阻表达为
  resistance_1..resistance_30 三十个 integer 参数（int16，0.1mΩ 带符号），
  而非 array。这是架构约束下的等价表达，非协议裁剪。
- **AtMostOnce 与 Risk 不变量**：deviceaction registry 硬约束
  AtMostOnce⇒Risk∈{high,critical}（definition.go:114）。MOS 测试/内阻/SN/
  F2F3 写等设为 high+AtMostOnce；幂等低危 setter（寻车/清告警/时间参数/
  自定义属性）去掉 AtMostOnce 允许安全重试。
- 新增 helper：jiabaidaExpectZeroAck（零长 ACK 校验）、jiabaidaResponseData、
  jiabaidaInt16Param、jiabaidaResistanceParameters。

### 7.2 模拟器（scripts/uart0_bms_rain_simulator.py）

扩展工厂模式状态机 + 写帧响应：
- 0x00 进入工厂（校验 0x5678）、0x01 退出工厂（0000/2828），
  未进工厂时 F2/F3/SN 读写返回错误帧 0x81。
- F2/F3 写：校验帧长与块内 CRC-16，通过后存储参数块；读回原样返回供对账。
  CRC 错误返回 0x82。
- A2 SN 写（≤31B ASCII）、F6 内阻写（60B）、F0 自定义属性写、
  0C MOS 测试写（更新测试状态）、F1/F5/E6/0D/F7/F8/FA/FB 写均返回零长 ACK。
- 新增 --sn 启动参数；离线冒烟验证（进/退工厂、F2 写+读回、CRC 拒绝、
  SN 写读、MOS 测试、原有读帧回归）全部通过。

### 7.3 测试与门禁

- jiabaida_test.go 新增：F2/F3 读帧黄金向量（FF0E/FF0D 校验和锚定协议 §7.11）、
  写 plan 形状/AtMostOnce 矩阵、F2/F3/SN/内阻/自定义属性 verifier 对账
  （含篡改读回拒绝、错误 ACK 拒绝、缺参拒绝）。
- deviceaction/definition_test.go 泛化不变量：V19 写能力必须以
  fail-closed bounded 形态存在；AtMostOnce⇒high/critical。
- `go test -count=1 ./...` 全部 29 包 PASS（2026-08-16）。

### 7.4 仍未解冻项（维持 fail-closed）

- 全部 17 个新 action 的 AvailabilityCode 仍为 protocol_unverified：
  缺真机（或等效高保真捕获）的黄金向量，尤其 F2/F3 写的真机读回与
  2828 退出响应状态码。
- 歧义①（F2 CRC 覆盖范围）与歧义②（BYTE39 字段归属）待真机证据消解。
- 0xF8/0xFA/0xFB 与平台职责重叠/断电风险，产品拍板前保持 gate。
- 前端控制页未接线新 action（后端先行，前端按 §5 安全门禁另立项）。
