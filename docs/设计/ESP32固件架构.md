# ESP32固件架构

> **状态**: 已实现（v2.7 现状：21 个 component、S3/C6 双目标、聚合根 + 依赖注入、事件驱动总线；测试 ~6000 行 host_tests）
> **版本**: v2.7
> **日期**: 2026-08-22
> **关联**: [多总线事件驱动架构.md](多总线事件驱动架构.md) | [DMA资源管理设计.md](DMA资源管理设计.md) | [节点.md](节点.md) | [GPIO与PWM外设控制.md](GPIO与PWM外设控制.md)

## 1. 架构总览

```
main/                    # 启动 + 应用外壳
├── main.c               # 启动、任务创建、状态心跳任务(5s)
├── app_state.c          # 聚合根 app_state_t + 启动身份(node_id 从 eFuse MAC 派生)
├── app_callbacks.c      # Wi-Fi/MQTT 生命周期 + 配置事务编排
└── hello_handshake.c    # Hello nonce 状态机（连接握手 supervisor）

components/              # 21 个组件（依赖无环）
├── frame/               # 二进制帧编解码（与 Go 共用语义头文件概念）
├── ehome_mqtt/          # MQTT topic/订阅/恢复（client_id=node_id, keepalive 30s）
├── msg_handler/         # 消息路由与字段定义（handler_hello/data/config/writecmd/periph/channel_cmd_v2）
├── config_mgr/          # 配置解析 + 双缓冲并发保护 + 事务化应用
├── scheduler/           # 定时采集（interval_ms 调度 + 队列分发）
├── bus_manager/         # 总线生命周期 + 控制器租约（lease policy）
├── bus_worker/          # 事件驱动 RX + 执行 + 背压
├── bus_dma/             # DMA 资源池（三级分配 + ref_count）
├── hw_profile/          # 硬件资源表（S3/C6 双硬件表）+ ResourceReport
├── sync_manager/        # 周期 sync（10min）+ 同步元数据持久化
├── ota/                 # OTA 下载/校验/回滚（A/B 分区）
├── log_stream/          # 日志流 Log V2（有界 ring）
├── gpio_ctrl/           # GPIO 直控状态机
├── pwm_ctrl/            # LEDC 定时器/通道管理
├── periph_owner/        # 外设资源所有权（引脚冲突防）
├── wifi_mgr/            # Wi-Fi 生命周期（凭据 NVS + SoftAP 配网引导）
└── uart0_boot/          # UART0 启动日志与调试
```

## 2. 核心设计模式

| 模式 | 落点 |
|------|------|
| 聚合根模式 | app_state_t：全局状态单一持有，组件间通过注入访问 |
| 依赖注入（DIP） | 组件经接口注入，不互相 include 实现 |
| 策略模式 | DMA 分配策略（不同硬件表差异收敛为数据） |
| 模板方法 | 多芯片硬件表（hw_tables S3/C6） |
| 事件驱动 | 总线 RX 事件 + 报告队列（v2.7） |

## 3. 总线数据链路（完整链路）

```
scheduler（interval_ms）
  → sample queue（per bus，有界）
  → bus_worker（lease 申请 → 事务执行 → lease 释放）
      ├─ TX: 命令帧（模板字节）经 DMA/TX 引擎发出
      ├─ RX: 事件驱动接收（UART 空闲检测分帧 / I2C 定长 / SPI 事务）
      └─ 结果 → report queue → report_task（三档池）
  → DataReport(0x03) → MQTT up
```

- CMD_WRITE（写命令）/ CMD_SAMPLE（采样）两类命令语义不同：写命令须 ACK 配对（request_id），采样按 interval 周期。
- 透明管道设计（v2.5+）：命令字节原样收发（raw_data），业务解析全在中心端。
- 背压：队列满丢弃 + skipped/rejected 计数（runtime_perf 上报）。

## 4. 启动与会话流程

```
上电 → NVS 读凭据 → Wi-Fi STA（失败→SoftAP+HTTP 配网页）
  → MQTT connect（client_id=node_id）
  → 订阅 nodes/{id}/down(QoS0) + control(QoS1)
  → Hello(0x01, 新 nonce) → HelloAck(0x12 同 nonce) → 握手完成
  → ResourceReport(0x19)
  → [等待 ConfigManifest 或周期 sync]
  → StatusReport 5s 心跳循环
```

- 重连：指数退避，3 次后销毁重建客户端；订阅 ACK 超时同样重建。
- 握手 nonce：每次发送（含超时重试）生成新非零 nonce；错误/过期 nonce 的 HelloAck 不得完成握手。
- BOOT 键长按 5s：清 NVS 重启（S3 GPIO0 / C6 GPIO9）。

## 5. 配置应用（事务化）

ConfigManifest(0x04) 应用顺序及失败语义见 [同步机制.md](同步机制.md) §4。容量上限：16 模板 / 8 通道 / 每通道 5 边缘设备 / 每设备 3 命令 / 8 DMA / 12 GPIO / 8 PWM。

## 6. OTA（组件 ota/）

- HTTPS 下载（Mozilla CRT bundle / 内网自签）+ SHA256 校验 + A/B 分区。
- 进度 OtaProg 持续上报；完成 mark-valid 30s；失败自动回滚；并发 supersede。
- 详见 [固件OTA.md](固件OTA.md)。

## 7. 测试架构

| 层 | 方式 | 规模 |
|----|------|------|
| unit（组件内 static 函数） | host tests 直接 .c 包含模式（含 FreeRTOS stub 基础设施） | ~6000 行 |
| 集成 | 固件双目标构建（S3+C6）为强制门禁 | 每发布必须 |
| 实机 | C6 专机（SN-3001 UART1 控制闭环）；S3 交叉构建 | 部分（见技术债） |

host test 基础设施：FreeRTOS task API 补全（xTaskCreatePinnedToCore/eTaskGetState 等）、file-static 变量重置、可控 esp_timer_get_time、stub 队列、ASAN。

## 8. 已知限制与债务

- S3 实机 heap/stack/满载数据未收集（脚本交叉构建仅编译验证）。
- IDF v6.0 vprintf hook 在 C6 crash——固件锁定 IDF v5.x，升级需重设计日志捕获入口。
- RAM 槽位（V2 命令去重）at-most-once，掉电即清（无 NVS）——设计选择，重启后 boot_id 变化自然失效。

## 9. 代码定位

| 关注点 | 主要实现 |
|--------|---------|
| 启动/任务/心跳 | main/main.c（status_task 5s:65） |
| 身份 | main/app_state.c |
| MQTT 生命周期 | components/ehome_mqtt/ehome_mqtt.c（topic 537-546） |
| 帧 | components/frame/frame_codec.h |
| 消息路由 | components/msg_handler/ |
| 配置事务 | components/config_mgr/（双缓冲） |
| 采集执行 | scheduler/ bus_manager/ bus_worker/ bus_dma/ |
| 资源上报 | components/hw_profile/ |
| OTA/日志/外设 | ota/ log_stream/ gpio_ctrl/ pwm_ctrl/ |
