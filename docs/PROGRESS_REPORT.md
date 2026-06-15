# EHome System 开发进度报告

## 项目概述
ESP32-C6 物联网采集器，支持多总线通信和远程管理

---

## 2026-06-16 开发进展（验证 + Bug修复）

### ✅ UART 串口验证（TCP + MQTT 全栈）
**提交:** `5c5914e` fix: UART端口分配跳过console, TCP ConfigManifest处理, 魔数替换宏

**硬件连接:** ESP32-C6 TX=GPIO20, RX=GPIO21 → CP210x USB-UART Bridge

**验证流程:**
1. ✅ TCP WriteCommand → ESP32 → UART1 → CP210x 正确接收 "Hello from ESP32!"
2. ✅ MQTT 全栈: REST API → MQTT → ESP32 → UART1 → CP210x
3. ✅ ConfigManifest 通过 TCP 下发触发 bus channel setup

**修复的关键 Bug:**

1. **bus_dma.c — UART 端口分配跳过 UART0 (HIGH)**
   - 问题: `uart_alloc_port()` 从 i=0 开始分配，导致第一个 UART 端口使用 UART0（console）
   - 表现: CP210x 收到 ESP32 调试日志而非 WriteCommand 数据
   - 修复: `for (int i = 1; ...)` 跳过槽位 0，确保 UART_NUM_1 或更高

2. **main.c — TCP 路径缺少 ConfigManifest 处理 (MEDIUM)**
   - 问题: `on_transport_msg` 回调只处理消息，不触发 `setup_bus_channels()`
   - 表现: TCP 下发的 SPI 配置不初始化总线
   - 修复: 添加与 `on_mqtt_msg` 对称的配置应用逻辑

3. **main.c — 代码重构 (MEDIUM)**
   - 魔数 `0x04` → `MSG_CONFIG_MFST` 宏
   - 提取 `handle_config_applied()` 公共函数（MQTT 和 TCP 回调共用）

### ⚠️ SPI BMP280 验证
**状态:** 调试中 — SPI 通道注册成功，bus 事务执行但未收到 BMP280 响应

**已确认:**
- ✅ SPI 通道通过 TCP ConfigManifest 注册成功（ConfigResult.success=1）
- ✅ WriteCommand 发送到 SPI 通道不报错（err=0 vs 之前的 258）
- ❌ 未收到 BMP280 chip ID (0x58)

**待排查:**
- BMP280 硬件连接（供电、CS/SCLK/MOSI/MISO 引脚）
- SPI mode (0 vs 3) 和频率匹配
- 或使用逻辑分析仪抓取 SPI 信号

### ✅ MQTT 全栈集成
- ESP32 连接 EMQX broker (192.168.1.10:1884)
- ConfigManifest 通过 MQTT 下发成功（2 个 UART 通道）
- 后端 Docker 环境运行正常（PostgreSQL、Redis、EMQX、前后端）
- REST API 可用（7 个通道已创建）

### 📈 代码统计（本次提交）
- 8 files changed, +675/-13
- bus_dma.c: UART 端口分配修复
- main.c: TCP ConfigManifest 处理 + 代码重构
- scripts/: TCP 验证、MQTT 验证、SPI 测试脚本

### 🔍 下一步
1. SPI BMP280 硬件调试（确认引脚连接 + 供电）
2. 修复后固件 SPI 端到端验证
3. 推送 master 分支
