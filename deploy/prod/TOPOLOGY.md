# 台架拓扑与雨量计（实测固化）

> 状态：**已实测**（2026-09-17）；来源：本机 `lsusb`/`/dev/serial/by-id` + ESP32 串口日志 + 数据库读数 + 真实控制命令回执。

## 1. 两条独立总线

| 总线 | ESP32 引脚 | 波特率 | 对端 | 供数来源 |
|---|---|---|---|---|
| **UART0** | TX=GPIO16 / RX=GPIO17 | 9600 | CP2102 → 宿主 `/dev/ttyUSB0` | `scripts/uart0_bms_rain_simulator.py`（**主机侧模拟器**） |
| **UART1** | TX=GPIO20 / RX=GPIO21 | **4800** | 真实 SN-3001 雨量计（TTL 直连） | **真实硬件** |

关键点：真实雨量计接在 **UART1**，主机侧**看不到**它（它不经过 USB-UART 桥）。
主机上 `/dev/ttyUSB0` 只是 UART0 的桥，与真实雨量计无关。

## 2. 为什么容易误判

只跑模拟器时，UART0 也能产出 `rainfall` 读数（模拟器会应答 SN-3001 的 FC03 帧），
于是"有雨量数据"并不等于"真实雨量计在工作"。两者必须用**通道号**区分：

- `channel_id=1`（UART0）→ 模拟器，读数恒定（当前 `0.5 mm`，帧 `01030200057847`）；
- `channel_id=4`（UART1）→ 真实硬件，读数随环境变化（当前 `0 mm`，帧 `0103020000b844`）。

**判别实验（可复现）**：停掉模拟器后观察 40 秒 ——
UART0 数据立刻归零（0 行），而 UART1 **持续出数**（9 行，07:26:42→07:27:22）。
这同时证明了 UART1 是独立真实硬件，而不是模拟器的另一条路径。

## 3. 波特率陷阱（本次踩到）

第一次探测 UART1 用了 **9600**，结果是 12/12 超时，差点误判为"没有真实设备"。
真实 SN-3001-GYL-N01 默认是 **4800 8N1、Modbus 地址 1**
（见 `docs/archive/v4.0-重建前归档/验证/SN3001实机功能验证.md`）。
改成 4800 后立即通。

**结论：探测未知 Modbus 设备时，波特率必须按设备手册而不是按习惯取值。**

## 4. 双向控制已验证（真实硬件）

```
POST /api/v1/edge-devices/7055/operations
  {"action_id":"read_rainfall","params":{}}
  Header: Idempotency-Key: <必填>
→ 202 QUEUED → status=SUCCEEDED
  verified_result_json = [{"name":"rainfall","unit":"mm","value":0}]
```

链路：`前端/API → MQTT ChannelCmdV2 → ESP32-C6 UART1 → SN-3001 → 回执 → verified_result`。

> 注意：`POST /edge-devices/:id/operations` **必须带 `Idempotency-Key` 头**，
> 否则返回 400 `invalid command request`（源码：`commandexec.Service.Create` 在
> key 非法时返回 `ErrInvalidRequest`）。这只影响手工 curl，前端会自动带。

## 5. 当前配置

| 设备 | 通道 | 类型 | 说明 |
|---|---|---|---|
| 测试BMS | UART0 (ch1) | `jiabaida_bms` | 模拟器供数 |
| 雨量计(真实) | UART1 (ch4) | `sn3001_rain` | **真实硬件** |

> `测试雨量计`（UART0 上的重复模拟实例）已于 2026-09-17 经浏览器删除（软删，
> 2933 条历史数据按「保留历史数据」选项留存）。

BMS 仍需 UART0 模拟器（真实 BMS 未接）。启动方式见 `run-rain-simulator.sh`。

## 6. 实测发现的 UI 缺陷（未修，待处理）

**删除对话框的「通道」标签显示错误的值。**

`DeviceDeleteDialog.vue` 的 `channelLabel` 取的是 **edge_device 顶层**字段：

```js
const parts = [props.device.hardware_type?.toUpperCase(), props.device.hardware_id]
```

但 `hardware_id` 在边缘设备上表示 **Modbus 从站地址**（雨量计为 `"1"`），
不是总线名；`hardware_type` 顶层又为空。于是对话框把两者拼成了 `UART 1` ——
看起来像"UART1 总线"，实际该设备在 **UART1**(ch4)，而 `UART 1` 是**地址 1 的**
巧合拼串。若设备在 UART0 且地址为 1（即被删的 `测试雨量计`），同样显示 `UART 1`，
与它真实的 UART0 不符 —— 属于**会误导删除确认**的显示缺陷。

正确来源应是关联的 `device.channel.hardware_id`（该字段确实为 `"UART0"` / `"UART1"`）。

复现：`node deploy/prod/verify-channel-label-bug.mjs`（只读，确认对话框后取消，不提交删除）。

