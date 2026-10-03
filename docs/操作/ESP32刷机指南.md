# ESP32刷机指南

> **状态**: 已实现（v2.7 当前流程：S3/C6 双目标构建；NVS 配网；Log V2 远程日志）
> **版本**: v1.1
> **日期**: 2026-08-22
> **关联**: [../设计/ESP32固件架构.md](../设计/ESP32固件架构.md) | [../设计/节点.md](../设计/节点.md)

## 1. 环境要求

- ESP-IDF v6.1（本仓库已升级并在 C6/S3 双目标实编通过；旧的 v5.x 说明已过期）
- 目标芯片：ESP32-S3 或 ESP32-C6 开发板
- 串口驱动：CP210x（数据口 ttyUSB0）与 USB-Serial-JTAG（日志口 ttyACM0）可能并存，烧录用数据口

## 2. 构建步骤（双目标）

**请用仓库脚本构建，不要直接 `idf.py build`** —— 脚本会解析 broker 配置、拒绝占位地址，
并在链接后执行 Wi-Fi ISR 的 IRAM 安全检查：

```bash
cd esp32-collector

# 一次性：配置本部署的 broker（gitignored）
cp config/mqtt-broker.defaults.example config/mqtt-broker.defaults
$EDITOR config/mqtt-broker.defaults

# 构建（可 all 构建全部四个 profile）
./build_firmware.sh s3-n16      # 或 c6-n8 / c6-n16 / s3-n8 / all

# 烧录
idf.py -p /dev/ttyUSB0 flash monitor
```

直接 `idf.py build` 会跳过上述两道门禁，并沿用未配置的占位 broker。

要点：

- 双目标独立 sdkconfig：sdkconfig.defaults.s3 / sdkconfig.defaults.c6；分区表 partitions_*.csv。
- **MQTT broker 必须显式配置，仓库不存真实地址。** broker 是编译期常量、无运行时覆盖，
  因此由每个部署各自提供：

  ```bash
  cp config/mqtt-broker.defaults.example config/mqtt-broker.defaults
  # 编辑其中的 mqtt://<host>:<port>
  ./build_firmware.sh s3-n16
  ```

  `config/mqtt-broker.defaults` 已 gitignore；也可用 `EXTRA_SDKCONFIG_DEFAULTS=<file>`
  做一次性覆盖。未配置或仍是占位地址时构建**直接失败**，不会产出连不上 broker 的固件。
  内置默认值 `192.0.2.1` 属 TEST-NET-1（RFC 5737），保证不可路由。

  > 背景：2026-10-01 因提交的默认值指向开发机（`192.168.20.3`），按文档构建出的固件
  > 把设备指向开发机，生产侧长时间不可达。故改为强制显式配置。

- 固件双目标构建是发布门禁（S3 + C6 都必须过）。

## 3. 配网（NVS / SoftAP）

- 首次上电无 Wi-Fi 凭据：节点自启 SoftAP + HTTP 配网页，提交后凭据写入 NVS。
- 之后自动重连，无需再配网。
- 恢复出厂：**BOOT 键长按 5 秒**（S3 GPIO0 / C6 GPIO9）→ 清 NVS 重启（含 Wi-Fi 凭据与同步元数据）。

## 4. 验证流程

1. **上电**：RGB LED（WS2812）状态灯（见 §6 LED 诊断）。
2. **Wi-Fi**：获得 IP（SoftAP 页或日志）。
3. **Hello 握手**：中心端节点列表出现该节点 → 状态 online。
4. **数据采集**：配置通道 + 边缘设备后 DataPanel 有数据。
5. **心跳**：StatusReport **1s** 心跳；服务端**3s** 无心跳判离线（检测 ticker 1s），最坏离线可见时延 ≈5s（2026-10-03 由 5s/90s 收紧）。

## 5. 故障排查

### 5.1 LED 状态诊断

- 不同颜色/快闪代表启动阶段（连接中/已连接/OTA/异常）—— 按固件 RGB 状态机对照。

### 5.2 串口与日志

- ttyACM0（USB_SERIAL_JTAG）通常挂固件启动日志；ttyUSB0（CP210x）是数据总线口（HP_UART0）。
- **远程日志优先**：线上诊断用 Log V2（节点总览→系统日志 TAB），不必插串口。
- 抓串口日志：`python3 -c` + pyserial 读 /dev/ttyACM0（idf.py monitor 在无 timeout 环境不可用）。

### 5.3 常见问题

| 症状 | 诊断方向 |
|------|---------|
| 节点 offline | Wi-Fi 凭据 / broker 地址 / 心跳链（看日志 TAG） |
| 传感器无数据 err=1 | 物理层无应答：波特率不匹配 / 接线 / 从机地址（UART RX timeout 1001ms = 无应答） |
| ConfigManifest rejected | 模板/通道数超上限（16 模板等）——查 ResourceReport manifest_capacity |
| 数据全 0x00 | 波特率不匹配陷阱（扫波特率找非零帧） |
| 重启循环 | 事务应用失败进入安全状态；看 ConfigResult 错误码 |

## 6. 相关操作

- 配置同步人工触发：/nodes/:id/config/sync。
- I2C 扫描：节点总览总线配置 TAB 或 API。
- 固件 OTA：固件管理页上传 → 创建 OTA 任务（不必再插线刷机）。
