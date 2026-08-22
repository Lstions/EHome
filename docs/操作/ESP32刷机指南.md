# ESP32刷机指南

> **状态**: 已实现（v2.7 当前流程：S3/C6 双目标构建；NVS 配网；Log V2 远程日志）
> **版本**: v1.1
> **日期**: 2026-08-22
> **关联**: [../设计/ESP32固件架构.md](../设计/ESP32固件架构.md) | [../设计/节点.md](../设计/节点.md)

## 1. 环境要求

- ESP-IDF v5.x（v6.0 勿用：vprintf hook 在 C6 crash）
- 目标芯片：ESP32-S3 或 ESP32-C6 开发板
- 串口驱动：CP210x（数据口 ttyUSB0）与 USB-Serial-JTAG（日志口 ttyACM0）可能并存，烧录用数据口

## 2. 构建步骤（双目标）

```bash
cd esp32-collector
# 选择目标（二选一）
idf.py set-target esp32s3   # 或 esp32c6
idf.py build
idf.py -p /dev/ttyUSB0 flash monitor
```

要点：

- 双目标独立 sdkconfig：sdkconfig.defaults.s3 / sdkconfig.defaults.c6；分区表 partitions_*.csv。
- MQTT broker URL 默认 CONFIG_COLLECTOR_MQTT_BROKER_URL —— 部署时用 sdkconfig 或 defaults 覆盖。
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
5. **心跳**：StatusReport 5s 心跳；90s 无心跳判离线。

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
