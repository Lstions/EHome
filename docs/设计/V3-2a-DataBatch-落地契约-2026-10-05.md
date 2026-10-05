# V3 协议 2a（DataBatch）落地契约 —— 冻结版

**日期**：2026-10-05
**分支**：`feat/mem-psram-phase1`（含 main 的 06e7a2f0）
**上位设计**：`docs/设计/协议v3-私有变长二进制-评估与设计.md` §5/§6
**本文件地位**：**并行开发的唯一 wire 契约**。任何一方要改这里的一行，先改本文件并通知 Lead。

---

## 0. 两个必须先解决的冲突（Lead 已裁决）

### 0.1 消息类型号冲突：0x20 被占用两次

| 来源 | 主张 |
|---|---|
| V3 设计文档 §5.1/§6.2 | DataBatch = **0x20** |
| `feat/mem-psram-phase1` 的 WS-G（未合入 main） | `MSG_MEM_RPT` = **0x20** |

**裁决：DataBatch 保留 0x20；`MSG_MEM_RPT` 改为 0x21。**

理由：
- DataBatch 是用户明确要求的 V3 落地点，已在设计文档、收益表、迁移表里通篇使用 0x20；
- `MSG_MEM_RPT` **尚未合入 main、后端尚未解析**（后端对它只会打 `Unknown msg type` 警告），
  改号零成本；反之改 DataBatch 要动整份设计文档；
- 0x21 空闲（已核验：main 的 `frame_codec.h` 与 `backend/pkg/frame/frame.go` 都止于 0x1F）。

**执行**：固件侧 `frame_codec.h` 的 `MSG_MEM_RPT` → `0x21`（`mem_guard.c` 用的是宏，自动跟随）；
task-9（后端解析内存遥测）按 **0x21** 实现。

### 0.2 部署顺序风险：不要动协议版本字符串

V3 文档 §5.1 建议"step 0 把严格相等改为区间检查"。**本次不做版本字符串变更**，只做能力位：

- 旧后端（现场运行的 Sep 30 镜像）`parseHello` 要求 `protocol_version == "2.6"`；
  固件若改成 `"3.0"` 会**被旧后端拒绝 HelloAck**，设备直接失联。
- 因此**固件继续上报 `proto_ver=2.6`**，DataBatch 的启用完全由 HelloAck 的**能力位**驱动。
- 后端把 `ServerMaxProtocolVersion` 放宽到 `"3.0"` 是**兼容性增强**，可独立部署，不影响 2.6 设备。

> 结论：**能力位协商，而非版本号协商**。这样"后端先上线"与"固件先上线"都不会互相打死。

---

## 1. 能力位（HelloAck field 2 `features`）

`features` 当前后端恒发 0、固件 `(void)features` 忽略。现定义为位图：

| bit | 名称 | 语义 | 谁置位 |
|---:|---|---|---|
| 0 | `CAP_DATA_BATCH_V1` | 服务端**可解析** `0x20`；设备可对非关键遥测启用批量 | 后端 |
| 1 | `CAP_MANIFEST_BYTE_BUDGET` | 服务端保证 publish 前 Manifest ≤ 设备预算（R1 修复） | 后端（本次不置位） |
| 2..63 | 保留，必须为 0 | | |

**规则**：
- 固件**只在 bit0 = 1 时**才发 `0x20`；bit0 = 0 时**完全维持现状**（只发 `0x03`）。
- 后端必须能在**没有**该能力位的设备上正常收 `0x03`（既有路径不动）。
- 后端必须能在**收到** `0x20` 时解析（即使自己没置位——防御性，且便于灰度）。

---

## 2. DataBatch (0x20) 帧布局 —— 冻结

```
DataBatch (0x20) — ESP→SVR，只承载【非关键】周期样本

field 1 (varint)  count              — 本帧样本数，1..4，必填
field 2 (varint)  base_timestamp_us  — 批内第一个样本的微秒时间戳，必填
field 3 (varint)  first_sequence     — 批内第一个 sequence；第 i 个 = first + i，必填
field 4 (varint)  channel_id         — 批级路由元数据，必填
field 5 (bytes, repeated) sample × count
    sample 子消息:
      field 1 (varint) delta_us      — 相对 base_timestamp_us；**首样本必须为 0**
      field 2 (bytes)  raw_data      — 与 0x03 的 payload 同义，≤1024 B
field 6 (varint)  edge_device_id      — 可选，同 0x03 field 7
field 7 (varint)  command_template_id — 可选，同 0x03 field 9
field 8 (varint)  command_index       — 可选，同 0x03 field 8
```

### 2.1 不变量（双方都必须强制）
1. 一帧只属于**同一 channel / 同一 edge / 同一模板**（路由元数据放批级）。
2. `count` 必须等于 field 5 的实际出现次数，否则**整帧拒绝**（fail-closed）。
3. `count` 范围 1..4；越界整帧拒绝。
4. 首样本 `delta_us == 0`；其余 `delta_us > 0` 且累计不递减（单调）。
5. 单样本 `raw_data` ≤ 1024 B；空 `raw_data` 拒绝。
6. **不携带 `error_code`**：`report_is_critical()` 为真的样本（error_code≠0 或 request_id≠0）
   永远单独走 `0x03`，复用既有告警路径。
7. 未知 field 号：**跳过**（前向兼容）；已知 field 号重复出现：整帧拒绝。

### 2.2 帧长预算
n=4、300 B 样本 → ≈1,248 B，仍在 `report_tx` 的 1,400 B 编码缓冲内。
**硬上限**：编码缓冲剩余不足时**降 n**，不得截断。

### 2.3 QoS
`0x20` 走 **QoS0**（加入 `mqtt_publish_qos_for_frame()` 的 LogStream 分支）。
理由：遥测本就容忍丢帧，且批量化本身就是为了减少 PUBACK。

---

## 3. 聚合策略（固件侧，report_tx 任务内）

```
取出第一个 telemetry desc（记 start_us）
循环（最多 n-1 次，非阻塞）：
  取下一个 desc
  若 同 channel/edge/template 且 critical==false
     且 (ts - start_us) < WINDOW_MS：并入 batch
  否则：放回原队列（不丢），结束本批
发布 DataBatch
```

- `WINDOW_MS = 20`（100 Hz 每通道间隔 10 ms，通常能攒 2 个；不超 50 ms 上限）。
- `n ≤ 4`。
- 队列空即发，**不等待**。
- 关键样本**不参与**聚合，且不得被非关键样本挤掉（走独立队列，现有实现已分离）。
- **零新增静态 RAM**：复用现有 8×1 KiB 遥测池。

---

## 4. 后端改造（`v3-backend` 负责）

| 文件 | 改动 |
|---|---|
| `backend/pkg/frame/frame.go` | 新增 `MsgDataBatch = 0x20` + `MsgTypeName` 映射 |
| `backend/internal/nodemgr/handler_data_batch.go`（新建） | 严格解析 §2 不变量，**按样本扇出** N 个 DataEvent |
| `backend/internal/nodemgr/manager.go` | `case frame.MsgDataBatch:` 分发 |
| `backend/internal/nodemgr/handler_hello.go` | HelloAck `features` 置 bit0 |
| `backend/internal/nodemgr/sender.go` | `ServerMaxProtocolVersion` → `"3.0"`（放宽，兼容 2.6） |

**扇出语义**：每个样本生成一个与 `0x03` **完全相同**的 `DataEvent`（时间戳 = base + delta_us，
sequence = first + i）。前端零改动。

**指标**：新增 `data_batch_frames_total` / `data_batch_samples_total` / `data_batch_rejected_total`。

**验收**：单测覆盖 正常 / count 不符 / count 越界 / delta 非单调 / 首样本非 0 / 空 raw / 重复字段 /
未知字段跳过；构造一条**真实固件格式**的字节做锚点。

---

## 5. 固件改造（`v3-firmware` 负责）

| 文件 | 改动 |
|---|---|
| `components/frame/frame_codec.h` | `MSG_MEM_RPT` 0x20 → **0x21**；新增 `MSG_DATA_BATCH = 0x20` |
| `components/msg_handler/handler_hello.c` | 捕获 HelloAck `features`，暴露 `hello_get_server_caps()` |
| `components/bus_worker/bus_worker.c` | §3 聚合 + §2 编码 |
| `components/ehome_mqtt/ehome_mqtt.c` | `0x20` 走 QoS0 |
| `host_tests/` | 新增 batch 编码/不变量用例 |

**开关**：`hello_get_server_caps() & CAP_DATA_BATCH_V1` 为假时，代码路径必须与现状**逐字节一致**。

---

## 6. 验证（`loadtest` 负责，见其任务卡）

1. **实机**：S3（`30EDA0A9A808`，PSRAM 8MB/16MB flash，已刷 `s3p-n16`）+ C6（`F0F5BDFFFE02`）。
2. **压测**：2 路真实 UART @100 Hz（CP210X `ttyUSB0` / CH340 `ttyUSB1` 做从机）+ MQTT 层 5×100 Hz soak。
3. **内存门禁**：`mem_budget_check.py` + 实机 `MemReport`。
4. **判据**：`report_drop == 0`、无 PANIC/WDT、`largest` ≥ 型号 floor、p99 时延。

---

## 7. 变更记录
- 2026-10-05 23:3x：建立并冻结。裁决 0x20 归属（DataBatch）、MSG_MEM_RPT → 0x21、
  能力位协商替代版本号协商。
