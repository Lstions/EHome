# V3-2a DataBatch 端到端验证（真实 MQTT，2026-10-06）

**目的**：关闭 V3-2a 最后一个未验证环节 —— 此前只有单测与锚点，**从未在真实 MQTT 上跑过**。
**方法**：本机开发栈（EMQX + PostgreSQL）+ **含 V3 改动的后端二进制**，用**固件生产编码器产出的真实字节**经 MQTT 发布，验证后端解析与扇出。

> ⚠️ 注意：这**不是**现场链路验证。现场 `192.168.20.6` 生产后端仍是 2026-09-30 镜像（未含 V3），固件也尚未上线；本次验证的是"V3 后端代码在真实 MQTT 上的行为"。

---

## 1. 被测对象

| 项 | 值 |
|---|---|
| 后端 | `backend/ehome-v3-test`（由当前 main 的 V3 代码构建） |
| broker | 本机 `127.0.0.1:1883`（EMQX 5.8.6） |
| 订阅 | `nodes/+/up` qos1（与现场一致） |
| 发布帧 | 固件生产编码器实跑产出的 **44 B 紧凑 n=4 锚点** |

**锚点帧**（`data_batch_encode()` 产出，非手推）：

```
20080410e807180720032a0808001204deadbeef2a06080a120201022a0508141201aa2a07081e1203556677
```

解码：count=4、ch=3、base_ts=1000、first_seq=7、deltas 0/10/20/30、raw `deadbeef`/`0102`/`aa`/`556677`、edge/template/index=0。

---

## 2. 正向：扇出成功

```
DEBUG [v3e2e-node-01] DataBatch: ch=3 base_ts=1000 first_seq=7 n=4 edge=0 cmd=0 template=0
```

- broker 侧 `delivered_msgs=1`（确认报文真的投递到后端，非只发出）；
- 后端解析出 **n=4** 并进入扇出路径；
- `edge=0` ⇒ 事件为 passive（不落库），与 `0x03` 且 edge=0 的语义一致 —— 这正是该锚点想钉住的分支。

---

## 3. 反向：6 条契约违规全部 fail-closed

同一真实帧做定点变异后发布，后端**整帧拒绝**并给出精确原因（不部分接受、不静默丢弃）：

| 变异 | 后端日志 |
|---|---|
| count=5（实际 4 样本） | `invalid DataBatch count 5: out of range 1..4` |
| count=0 | `invalid DataBatch count 0: out of range 1..4` |
| 首样本 delta=1 | `first sample delta_us 1, want 0` |
| delta 非单调（10 在 30 之后） | `sample 2 delta_us 10 is not monotonic (previous 30)` |
| 样本内字段号 0（畸形） | `sample 1: invalid field number: 0` |
| raw_data 长度为 0 | `sample 0: empty raw_data` |

每条变异之后**再发一次正常帧，仍解析成功** —— 证明拒绝路径没有污染后续状态。

---

## 4. 结论与边界

**已证明**：V3 后端的 DataBatch 解析与扇出在真实 MQTT 链路上工作，且 6 条契约不变量在**真实报文**上逐一 fail-closed。

**未证明（仍需现场）**：
1. 现场生产后端**未部署** V3 → 现场节点仍收不到能力位，固件不发 0x20；
2. 固件侧 `report_tx` 的聚合路径未在真机跑过（需能力位先置位）；
3. 前端展示未验证（扇出复用同一 DataEvent 路径，前端理论上零改动，但未实测）。

---

## 5. 复现步骤

```bash
# 1) 构建含 V3 的后端
cd backend && go build -o ehome-v3-test ./cmd/server/

# 2) 起临时容器（连本机开发栈）
docker run -d --name ehome-v3-verify --network ehomesystem_default \
  -e EHOME_SERVER_ADDR=:8099 -e EHOME_DB_HOST=postgres -e EHOME_DB_PORT=5432 \
  -e EHOME_DB_USER=ehome -e EHOME_DB_PASSWORD=ehome123 -e EHOME_DB_NAME=ehome \
  -e EHOME_DB_SSLMODE=disable -e MQTT_BROKER=tcp://emqx:1883 \
  -e EHOME_MQTT_CLIENT_ID=ehome-v3-verify -e LOG_LEVEL=debug \
  -v $PWD/backend/ehome-v3-test:/app/ehome-server:ro \
  --entrypoint /app/ehome-server golang:1.26.5

# 3) 发布锚点帧 / 变异帧（见本轮 /tmp/e2e_*.py）
```

## 6. 变更记录
- 2026-10-06 01:0x：建立。用固件真实字节在真实 MQTT 上验证扇出 + 6 条 fail-closed。
