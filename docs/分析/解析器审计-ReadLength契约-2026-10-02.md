# 解析器审计报告：read_length 契约缺陷的系统性蔓延

> 日期：2026-10-02 ｜ 触发：真实 BMS 无数据事故（轮询路径已修）
> 范围：`backend/internal/drivers/` 全部驱动 + 固件定帧契约
> 方法：源码复核 + **真实设备实测**（在线 BMS 节点 `30EDA0A9A808`）

---

## 0. 结论

上轮 BMS 事故**不是孤例**，而是一类缺陷的第一次暴露。

固件把 `ReadLength`/`ReadSize` 当作**「最小长度下限」而非「期望长度」**，
且**在两处独立实施**——任一不满足就整帧丢弃：

| # | 路径 | 代码 | 后果 |
|---|---|---|---|
| A | 轮询采样 | `bus_worker.c:1345` | 丢整帧 + `error_code=3` + 空 raw（**设备静默**） |
| B | 受控操作 | `bus_worker.c:822` | 返回 false ⇒ `error_code=0x1400+i`（**操作失败**） |

判据：**声明的长度必须 ≤ 真实响应长度**，否则设备永久静默或操作永久失败。

复核全部驱动，发现 **1 个 P0 + 4 个 P1/P2**。其中 P0 已用真实设备**实测复现**。

---

## 1. 【P0，已实测复现】JBD 受控操作与单步读动作的长度同样错误

上轮**只修了轮询模板**（`GetCommandTemplates`），`ControlAction` 的 `ReadSize` **未修**。

### 1.1 实测证据（决定性）

对在线 BMS 发起 `read_basic_info`（单步动作，`ReadSize: 60`）：

```
POST /edge-devices/2/operations  {"action_id":"read_basic_info","params":{}}
→ status: FAILED, final_reason: final_failed, verified_result: []
```

数据库统计：**`read_basic_info` 6 次执行、6 次 FAILED（100%）**，最早可追溯到 10-01 23:49。

**关键对照**：同一台 BMS 的**轮询路径**（同一条 `0x03` 命令）在修复后正常出数。
⇒ 证明差异**只来自 `ReadSize`**，而非设备或协议问题。

### 1.2 规律：定长命令全对，变长命令全错

| 步骤 | 命令 | 声明 | 协议实长 | 判定 |
|---|---|---|---|---|
| `readback_test_status` | 0x0C | 9 | 2+7 = 9 | ✅ |
| `readback_custom` | 0xF0 | 13 | 6+7 = 13 | ✅ |
| `read_params` (F2) | 0xF2 | 60 | 53+7 = 60 | ✅ |
| `read_params` (F3) | 0xF3 | 59 | 52+7 = 59 | ✅ |
| `readback_resistance` | 0xF6 | 67 | 60+7 = 67 | ✅ |
| `readback_fet` | 0x03 | **60** | 30+2N | ❌ |
| `readback_basic` ×8 | 0x03 | **60** | 30+2N | ❌ |
| `readback_restart_count` | 0xAA | **40** | 24+7 = **31** | ❌ |
| 单步 `read_basic_info` | 0x03 | **60** | 30+2N | ❌ **实测 6/6 失败** |
| 单步 `read_comprehensive` | 0x0F | **100** | 31+2N+2M | ❌ |
| 单步 `read_cell_voltage` | 0x04 | **50** | 2M+7 | ❌ |
| 单步 `read_protection_count` | 0xAA | **40** | 31 | ❌ |
| 单步 `read_hardware_version` | 0x05 | 40 | 变长字符串 | ⚠️ 未验证 |

**症状**：长度**由协议唯一确定**的命令，`ReadSize` 全部精确正确；
长度**随设备变化**的命令（0x03/0x04/0x0F），一律写死一个偏大的值 ⇒ 必失败。
`0xAA` 属单纯笔误（40 vs 31）。

**实机参数**：该 BMS `NTC=3`、单体 `16` ⇒ 0x03 实长 36、0x04 实长 39、0x0F 实测 78。

### 1.3 影响面

受影响的是**已启用**（`enabled=true`/`available=true`）的动作：
`read_basic_info`、`read_comprehensive`、`read_cell_voltage`、`read_protection_count`、
`read_protection_parameters`、`read_system_parameters`、`set_mos_policy`、
`force_balance`、`clear_alarm`、`find_car`、`write_custom_attributes` 等
（凡带 `readback_*` 步骤的 8+ 个动作）。**它们在真机上目前全部无法成功。**

### 1.4 修复

与轮询模板同理，**改 `ReadSize: 0`**（走 10ms 空闲定帧）。固件已支持：

- `uart_collect_response`：`return expected == 0 || len >= expected;`（822 行）
- `legacy_write_args_valid`：`read_size <= 256` —— **无下界，0 合法**（已核实）

> 注意：`0x0C/0xF0/0xF2/0xF3/0xF6` 这几个**长度确定**的可以保留具体值，
> 但 0x03/0x04/0x0F/0xAA 必须改 0（0xAA 也可改 31，但改 0 更稳）。

---

## 2. 【P1】techfine 逆变器 `ReadSize: 256`：意图与固件语义相反

`inverter_techfine.go:49-57`：

```go
// UART worker returns the entire line-idle-delimited response.  The
// envelope cap protects memory and fails closed if the device exceeds it.
TXData: []byte(command), ReadSize: 256, RXTimeoutMS: 1000,
```

作者把 `ReadSize` 当**上限信封**；固件把它当**下限**：

```c
if (len > 0 && idle >= 10000) {
    memcpy(out, state->data, len);
    *out_len = len;
    return expected == 0 || len >= expected;   // expected=256 ⇒ 要求 len>=256
}
```

逆变器返回的是 `(HSTS ...)` 文本行，**远小于 256 字节** ⇒ 恒返回 false。
（`cap` 是独立参数，真正的内存上限由它承担，`ReadSize` 从不做上限。）

11 个 `read_*` 动作当前 `Enabled: false`，故**尚未暴露**；
**一旦启用将全部失败**。这是最值得优先处理的「潜伏」缺陷。

**修复**：`ReadSize: 0`（正合作者本意：交给空闲定帧）。

---

## 3. 【P1】`lk_th01` 模板永远创建不出来（静默跳过）

`builtin.go:140-149` 声明 `WriteData: ""`，而 `handler_edge_device.go:62`：

```go
if writeData == "" {
    return nil      // 静默跳过，不建模板、不报错、不告警
}
```

⇒ 该设备 `template_ids` 为空 ⇒ 调度器无命令可发 ⇒ **永不采集**。

这与本次 BMS 事故同属「静默失效」。代码里 `manifest_codec.go:243` 也过滤空 WriteData，
但**没有任何地方把「被丢弃的 schedulable 模板」变成可见错误**。

**待确认**：LK-TH01 是否为「无命令、纯被动上报」设备？
若是，应**不给它 Schedulable 模板**（或标记被动），而不是留一个必被静默丢弃的定义。

---

## 4. 【P2】SN3000 / PRS3001 `parseLegacy` 缺 CRC 校验

`SN3001RainDriver` 有严格 CRC（`builtin.go:342`），但这两个只查功能码：

```go
if raw[1] != 0x03 { return nil, ... }        // 只查 FC
direction := ...Uint16(raw[3:5]) / 10.0      // 直接取值，无 CRC
```

⇒ **噪声/半帧会被当有效数据入库**，污染数据与告警。SN3001 已给出正确示范。

---

## 5. 【P2】其余驱动的固定长度：正确但脆弱

| 驱动 | ReadLength | 协议实长 | 判定 |
|---|---|---|---|
| `sn3000` | 7 | 3+2+2 = 7 | ✅ |
| `prs3001` | 9 | 3+4+2 = 9 | ✅ |
| `sn3001_rain` | 7 | 3+2+2 = 7 | ✅ |
| `generic_modbus` | 9 | 3+2×2+2 = 9 | ✅ |
| `bmp280` / `generic_i2c` | 6 | I2C 裸读，无地址/CRC | ⚠️ 语义不同，需按 I2C 路径确认 |

Modbus 系长度由 FC+寄存器数唯一确定，**当前正确**；
但它们继承了与 JBD 相同的**失效模式**：长度一旦不匹配即静默无数据。
建议保留具体值并加注释写明推导依据。

---

## 6. 已验证正确的部分（避免误伤）

- **JBD `parse0x0F`**：用**真实 78 字节帧**逐字段核对 —— 总压 50.53 V、电流 1.32 A、
  RSOC 5%、容量 4.63/98.7 Ah、最高/最低单体 3180/3118 mV、循环 300、FET 3、
  3 路温度（30.15/28.15/32.05 °C）、16 路单体电压 3.118–3.180 V、8 字节 trailer —— **全部正确**。
- **`parse0x03` / `parse0xAA` / `parse0xF2` / `parse0xF3`** 的长度要求与文档一致。
- **Modbus CRC**（`sn3001_rain`、`generic_modbus`）实现正确。

---

## 7. 修复优先级

| 优先级 | 问题 | 影响 |
|---|---|---|
| **P0** | §1 JBD 动作 `ReadSize` 60/40/100/50 | 8+ 个已启用动作**在真机全失败**（实测 6/6） |
| **P1** | §2 techfine `ReadSize: 256` | 11 个动作**启用即全失败**（当前潜伏） |
| **P1** | §3 lk_th01 空 WriteData | 设备**永不采集**且无告警 |
| **P2** | §4 SN3000/PRS3001 缺 CRC | 脏数据入库 |
| **P2** | §5 固定长度注释 | 未来改型静默失效 |

---

## 8. 建议的防回退门禁

本轮两处静默失效的共因：**没有任何测试断言「声明长度与真实响应长度的关系」**。

建议新增跨驱动门禁：

1. 每个驱动的每个 `Schedulable` 模板断言 `WriteData != ""`
   —— 直接堵住 §3 那类「模板根本不会创建」的静默跳过；
2. 每个 `ControlAction` 的每个 `CompiledControlPlanStep`，断言 `ReadSize`
   要么为 0，要么有协议依据（长度确定型），
   —— 堵住 §1/§2 那类「写死一个过大值」；
3. 保留本轮新增的 `TestJiabaidaTemplatesUseIdleFraming`。

> 现有 `TestBuiltInDriversTemplatesAllSchedulable` 只断言 `Schedulable` 恒真，
> **既不检查 `WriteData` 非空、也不检查长度契约** —— 恰好漏掉本轮发现的两类缺陷。

---

## 9. 未决问题（需你确认）

1. **LK-TH01 的物理语义**：是否真的无下行命令？决定 §3 的修法。
2. **BMP280 / generic_i2c 的 `ReadLength: 6`**：I2C 路径是否与 UART 同用
   `read_size` 下限语义？需按 I2C 分支单独核实（本次未展开）。
3. **是否要我直接动手修 P0/P1**：改动集中在
   `jiabaida_control.go`、`inverter_techfine.go`、`builtin.go`，
   可连同回归测试一起提交。
