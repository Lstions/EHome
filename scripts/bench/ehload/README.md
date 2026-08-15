# 边缘设备高频数据交互压测器（ehload）

EHomeSystem 性能压测留档工具，对应 `docs/测试/边缘设备性能修复测试方案.md` 第六节。
来源：2026-08-15 压测报告使用的 /tmp/ehload 工具集，归档入库以保证方案可复现。

## 组成

- `main.go` — 压测器本体（Go）：模拟 N 个节点，每节点独立 MQTT 连接，按指定速率上报
  DataReport (0x03) 帧。帧字段：f1=channelID f2=timestamp(ms) f3=sequence f4=raw(4B)
  f5=errorCode=0 f7=edgeDeviceID。
- `run_bench.sh` — 分级压测编排：S1-S4 四档串行，每档前后 DB 快照精确对账丢率，
  自动解析设备 id 起点（见下）。
- `sampler.py` — 资源采样器：每 2s 采样后端 CPU/RSS/线程、PG 写吞吐、表行数、
  日志行速率。

## 构建

```bash
cd scripts/bench/ehload && go build -o ehload .
```

## 用法

```bash
# 1. 准备数据（必须先跑，脚本会输出 device_base）
bash scripts/bench/prepare_bench_data.sh 1000

# 2. 单档压测（device-base 用 prepare 脚本输出的值）
./ehload --nodes 200 --rate 2 --duration 60 --qos 1 --device-base <device_base>   # 400 msg/s
# 或整档编排（自动解析 device-base，含对账）
./run_bench.sh
```

## ⚠️ device-base 陷阱

`prepare_bench_data.sh` 先 DELETE 再 INSERT，**DELETE 不重置 id 序列**，每次重跑设备 id 起点
递增（实测 3→1003→2003→…）。`--device-base` 必须匹配实际 `min(id)`，否则压测帧携带不存在的
设备 id，后端查不到记录（consumers_heavy.go record not found）整档丢弃，且 record not found
日志刷屏掩盖真相。`run_bench.sh` 已自动解析，单档手动跑请从 prepare 脚本输出取。

## 对账口径

- sensor_parser 处理数 = unified_data 增量 / 2（每事件写 2 个传感器字段行）
- db_persist 处理数 = device_data 增量 − unified_data 增量（parser 也写 device_data 1 行/事件）
- 后端日志速率/体积：`sampler.py` 采样 + 压测前后 `stat -c%s .logs/backend.log` 差值

## 验证后端日志降噪的注意事项

后端须以 `LOG_LEVEL=info` 运行（与生产一致）复测日志量——debug 级下热路径日志
（DataReport/Received msg/Parsed）仍会全量输出，无法体现 P0-2 降噪效果。
