# 边缘设备控制 ESP32 性能基线

## C6 N8 / ChannelCmdV2 single-step

测试日期：2026-07-19；目标：ESP32-C6 QFN40 rev 0.1、8MB flash、USB Serial/JTAG。

| 项目 | 实测/固定值 |
| --- | --- |
| 构建目标 | `./build_firmware.sh c6-n8` |
| 应用二进制 | `0x1521c0`，最小 app 分区 `0x380000`，剩余 `0x22de40`（62%） |
| 当前开发测试镜像 SHA-256 | `d22d72fae75cb9403c74a596fc75810047fe2c6a0e6b02311c20b3cc1206c16c` |
| control slot | 4 个固定 slot；每个约 480B（命令/TX 约 224B + final raw 上限 256B），总约 1.9KiB。执行中的槽绝不淘汰，已完成槽只保留最近 4 条用于 replay，满时淘汰最旧 Final |
| 队列增量 | 每个既有 `bus_cmd_t` 仅新增 `bool + slot index`；不新增 per-command task |
| V2 能力 | `supports_channel_cmd_v2=true`、`supports_finally=true`、batch=false、TX=128、RX=256、timeout=30000ms、RAM replay=4 |

后续 UART1 路由修复构建：`./build_firmware.sh c6-n8` 通过，应用二进制
`0x151c20`，剩余 `0x22e3e0`（62%），镜像 SHA-256 为
`5f3364f97321fdcabd6ce1bbe89e2dead56d6ca28cce578d38203448ad643bcb`；已烧录至
`/dev/ttyACM0` 并由 esptool 校验写入哈希。

## S3 N8 交叉构建

`./build_firmware.sh s3-n8` 已通过（未烧录）：应用二进制 `0x125ff0`（1204208）bytes，
8MB flash 最小 app 分区 `0x380000`，剩余 `0x25a010`（67%）；镜像 SHA-256 为
`9403207aa3af19ad1bd35d4a3e220e74bf75d99530cbe7c7d2fcb8a260ccb147`，ELF SHA-256 为
`dee67ab1dc6303d006794055a4ed6536bd92874f0dea2524f8450ee5c4fe1ed8`。

加入 V2 control-statistics 后再次通过当前脚本交叉构建（仍未烧录）：应用二进制
`0x1266b0`，最小 app 分区 `0x380000`，剩余 `0x259950`（67%），镜像 SHA-256 为
`d769c4034df36d07770ed76e8ce707aff0f8eb4be5c385cfc7584709e8d9b811`。这证明同一有界实现可在
S3 目标编译，不构成 S3 板卡的 heap、stack、满载或 watchdog 实机验收。

加入 `manifest_capacity` ResourceReport 字段后的当前源码再次以
`./build_firmware.sh s3-n8` 干净交叉构建：应用二进制 `0x126710`（1205911 bytes），最小 app
分区 `0x380000`，剩余 `0x2598f0`（67%）；产物文件为 1206032 bytes（包含填充），SHA-256 为
`4baaf9dea8ee8f5b1efa7d1a6c25d3a28454dde52c9885392d3d846d026a6e77`。当前 host tests 也为
16/16 通过，其中包含该字段的编码/解码覆盖。这仍仅证明 S3 目标可构建，不构成 S3 板卡性能验收。

以 `ce98086` 源码基线重新执行规定脚本 `./build_firmware.sh s3-n8`：生成
`build/s3-n8/ehome_collector.bin`（1206960 bytes），SHA-256
`674b6b24adb2a367c01cd4bc4fabf08deeac2e08369a120ab6ec58021744bfa3`。该轮仍是交叉编译，
没有连接或烧录 S3 硬件，不能作为 S3 性能、watchdog 或串口实机门禁的通过证据。

已完成实机验证：nonce 关联 HelloAck、ResourceReport、向不存在 channel 下发 V2 信封的安全 admission 拒绝，以及过期 deadline 的 `1006` 拒绝（均无物理 TX）。
最近一次复测对应本表 SHA-256：节点 `F0F5BDFFFE02`、boot `C329A2BBE43D47ED`；
资源报告给出 V2/Final/128/256/30000/RAM-4，过期请求收到
`ChannelCmdV2Ack(accepted=false,error_code=1006,event_sequence=2)`。

## C6 UART 物理闭环（GPIO16/17 ↔ `/dev/ttyUSB0`）

同日完成一次独立于业务 Driver 的安全 UART 模拟验证。设备资源报告确认：本固件将
GPIO16/17 定义为 **UART0**（UART1 为 GPIO20/21）；因此虽然外部接线被称作“UART1”，
测试清单按设备实际能力配置 UART0，9600 8N1、DMA 关闭。

1. 先读取配置基线（0 通道），再临时下发 channel `9001`；
2. 下发唯一身份的 ChannelCmdV2，TX=`a1 01 02`，`read_size=3`；
3. `/dev/ttyUSB0` 实测收到 `a1`、`01 02` 两段字节流，随即回写 `5a 01 02`；
4. C6 返回 `ChannelCmdV2Ack(success=true, sequence=9)` 及
   `ChannelCmdV2Final(success=true, error_code=0, raw=5a0102, sequence=10)`；
5. 以空 ConfigManifest 恢复原始 0 通道基线，并收到成功 ConfigResult。

这证明了 MQTT → V2 admission/worker → C6 GPIO16/17 UART → USB 串口模拟器 → V2 Final
的物理读回链路，以及 V2 命令身份去重（重复 command identity 会回放既有 final）的行为。

## C6 UART1 / SN-3001-GYL-N01 待现场确认的只读尝试

用户报告 GPIO20(TX)/GPIO21(RX) 接至 SN-3001 雨量计。开发环境能够通过
`/dev/ttyACM0` 访问、烧录和 MQTT 控制该 C6，但没有任何独立证据证明该 C6 的 UART1
已实际连到、供电并能接收该 SN-3001 的 RS485 回帧。为使固件运行端口与资源表一致，已修复：
匹配表中固定 TX/RX 对时直接选择对应 UART 控制器，故 GPIO20/21 绑定 UART1；自定义引脚
对仍使用空闲端口分配。

依据该型号 485 手册的默认配置（地址 `0x01`、4800 8N1、Modbus RTU），已仅发送 FC03
读取寄存器 `0x0000` 的帧 `01 03 00 00 00 01 84 0a`，期望 7 字节响应。新固件资源报告
确认临时 channel `9002` 已应用，V2 Ack 成功，但 Final 为 `success=false,error_code=1`，
即等待 RX 超时且没有原始数据；随后已用空清单恢复 0 通道基线。这仅证明 C6 未收到回帧，
不能证明 SN-3001 链路存在或协议不匹配；未启用任何业务动作。

## 隔离开发环境真实 C6 / SN-3001 控制链验证

验证使用隔离 `ehome-dev`：PostgreSQL `5435`、Redis `6380`、EMQX `1884`、后端 `8082`。
为避免开发后端触及生产 broker，使用 `EXTRA_SDKCONFIG_DEFAULTS` 构建临时镜像，唯一覆盖为
`mqtt://192.168.20.3:1884`（镜像 SHA-256：
`d1ca6cfa87db080673f7b117c7360e5e148a73a2ff9d831702975f2e1dd76687`）。烧录后，真实 C6
`F0F5BDFFFE02` 在开发数据库上线并完成 Hello/HelloAck、ResourceReport、ConfigResult 和
LogStream；随后已停用预存的 BMS/UART0 fixture，以免误下发不相关采集。

开发 API 创建了 `sn3001_rain` 的 DeviceConfig、UART1/GPIO20/21/4800 channel 和零调度
EdgeDevice；C6 报告配置 `in_sync/applied`。只在开发实例中启用
`sn3001_rain/read_rainfall`，并由 `POST /edge-devices/:id/operations` 创建一次受审计命令。
ResourceReport 刷新后该动作目录为 available，审计记录显示 Outbox 已处理、Attempt 1 已发布
并在 C6 Final 后终态：`FAILED/final_failed`。Final 带成功码但 raw response 为空，Driver 因
无法解析空 Modbus 帧 fail-closed；这不是雨量读值成功。

早期验证后的“恢复生产镜像”记录已经失效：该 C6 被指定为专用测试设备，当前保持开发 MQTT
配置和开发验证固件；不会恢复生产固件。

尚未完成：SN-3001 的真实协议/read 值验证（需解释 C6 Final 成功但 raw 为空的 UART worker
行为，并排查 RS485 A/B、收发器方向、供电及实际波特率）、idle/峰值 heap、worker 栈水位、
满采集负载 p50/p95、ESP32-S3 对照实测。
没有这些证据前，不启用普通 setter、batch 或高风险动作。

## 最终 SN-3001 实机闭环（2026-07-19）

上述隔离验证中的空 raw 已定位并修复，不能再作为最终结论：一是强制同步接口曾依赖后端
瞬态在线缓存，可能返回成功却不发送 Manifest；二是 UART worker 在 V2 的 `post_tx_delay`
之后才登记待响应，SN-3001 的快速回帧会失去关联。现已改为 TX 完成即登记，并保证同一
UART 通道的前一笔请求响应未完成时不发送下一笔读请求。

在隔离 API→Outbox→ChannelCmdV2→C6 UART1(GPIO20/21, 4800 8N1)→SN-3001 链路中，
`sn3001_rain/read_rainfall` 的一次真实只读操作得到 `SUCCEEDED` Final。C6 日志记录
`V2 admitted`、`V2 queue`、`V2 execute`；后端收到 Final，严格 CRC/地址/功能码/字节数
校验通过。真实 Modbus RTU 回帧为 `01 03 02 00 00 B8 44`，累计雨量为 `0`。

该 C6 现作为专用测试设备，保留隔离开发 MQTT 配置和验证固件，以继续进行性能和协议回归；
不会刷回生产 `:1883` 固件。性能余量、S3 实测、setter/batch/high-risk 动作仍未完成。

后端控制域随后增加运行时通道准入：仅有数据库 Channel 不再足以发起动作，必须由新鲜
ResourceReport 中的实际 `channels` 清单证明目标通道已应用且 enabled。成功 Final 的原始帧
仍只在后端用于 Driver 校验；操作历史保存并展示 `verified_result` 解析值，避免向浏览器
泄露通用 raw 控制字节。

## C6 运行时资源快照（空闲/低采集）

为使后续性能门禁使用真实数据，StatusReport 新增受限 runtime-performance 子帧：当前/最小
heap、scheduler/worker stack high-water（FreeRTOS words）和命令队列最小空位。后端严格解码后
写入 `Node.FreeHeapBytes` 与 `hardware_info.runtime_performance`；它不属于动作协议，也不开放
浏览器 raw 控制字节。

2026-07-19 在隔离 `ehome-dev`、C6 `F0F5BDFFFE02` 上实测（StatusReport 86B）：

| 指标 | 实测值 |
| --- | ---: |
| 当前 free heap | 172872 B |
| 自启动以来最小 free heap | 167100 B |
| scheduler 栈最小余量 | 3104 words |
| worker 栈最小余量 | 1592 words |
| 命令队列最小空位 | 8 |

该快照对应已启动网络、MQTT、worker、日志流和配置同步的真实 C6，但未施加满采集/并发控制负载；
因此只关闭“无运行时资源数据”的门禁，不替代 S3、p50/p95、长稳、watchdog 和队列压力验收。
对应固件仍由 `./build_firmware.sh c6-n8` 构建，应用二进制 `0x1520b0`，最小 app 分区剩余
`0x22df50`（62%）。

## C6 + SN-3001 连续只读时延（2026-07-19）

为验证 V2 replay 槽位的长期工作方式，主机回归测试新增“第 5 条已完成命令淘汰最旧 Final”的
覆盖。修复前 4 个 completed slot 永不释放，连续唯一命令在第 5 条后会被拒绝；修复后仅淘汰
最旧 completed slot，`QUEUED`/执行中的命令不会被淘汰，重复身份仍可在最近 4 条窗口内回放。

将该修复通过 `./build_firmware.sh c6-n8` 以开发 MQTT 覆盖配置构建，并经 esptool 写入专用
`/dev/ttyACM0` C6 后，对实际接在 UART1 GPIO20/21、4800 8N1 的 SN-3001 执行 40 次串行
FC03 只读（`01 03 00 00 00 01 84 0a`，每次使用唯一 V2 command identity）。40/40 均返回
7 字节有效 Modbus 帧，主机 MQTT 发布至 Final 的端到端结果为：

| 指标 | 实测值 |
| --- | ---: |
| 样本数 / 成功数 | 40 / 40 |
| p50 | 124.0 ms |
| p95 | 129.6 ms |
| 最大值 | 173.4 ms |

这是单设备、串行、只读的 C6 实测，包含开发 MQTT 和 V2 传输路径；它证明 replay 槽位不再在
4 次命令后耗尽，也提供 UART1/SN-3001 基线。它不是满采集、并发控制、长稳或 ESP32-S3 的
性能验收，相关门禁继续保持未通过。

## V2 控制统计实机回传（2026-07-19）

StatusReport 新增受限 `control_statistics` 子帧，仅包含当前 boot 内的聚合计数：`accepted`、
`rejected`、`completed`、`replayed`；不含命令 ID、参数、原始帧或目标设备身份。后端对四个
无符号 32 位字段严格解码，并持久化到 `hardware_info.control_statistics`。

将本节固件（`0x1522b0`，分区剩余 `0x22dd50`，开发测试镜像 SHA-256
`1325ff49ad3d1e4b1368744a88649dfa63b099350e91f2ac19d6823a135d17f2`）烧录到专用 C6 后，
对实际 SN-3001 执行上节 40 次唯一 V2 只读。下一次真实 StatusReport 在开发数据库持久化：

| 设备计数 | 实测值 |
| --- | ---: |
| accepted | 40 |
| rejected | 0 |
| completed | 40 |
| replayed | 0 |

因此该指标同时验证了 V2 Command → UART1 → SN-3001 → Final 的完成计数与后端 StatusReport
解码链。它是 boot-local 运行观测，不取代高风险动作所需的 NVS seen-ring 或审计证据。

## C6 Manifest 容量能力（2026-07-19）

容量协商版本按 `./build_firmware.sh c6-n8` 以开发 MQTT 覆盖配置构建并烧录专用 C6；应用二进制
`0x152320`，最小 app 分区剩余 `0x22dce0`（62%），镜像 SHA-256 为
`6090b69ea7f8789496f63dcfd3345f78d3a7ede74aa5d0e15e628a7bf30def1f`。ResourceReport 已由实际 C6
回传 `max_templates=16`、`max_channels=8`、`max_template_ids=8`。这组设备事实已用于后端 Manifest
发布前校验，并完成“临时 17 模板拒绝、恢复 5 模板成功同步”的实机闭环；详细结果见统一方案门禁状态。
