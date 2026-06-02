# ESP32 烧录与验证指南

## 环境要求

- ESP-IDF v5.0+
- Python 3.8+
- CMake 3.16+
- 串口工具 (esptool.py)

## 构建步骤

```bash
cd /home/sun/workspace/EHomeSystem/esp32-collector

# 设置ESP-IDF环境
. $HOME/esp/esp-idf/export.sh

# 配置目标芯片
idf.py set-target esp32s3

# 配置WiFi和MQTT
idf.py menuconfig
# → EHomeSystem Config → WiFi SSID/Password
# → EHomeSystem Config → MQTT Broker URL

# 编译
idf.py build

# 烧录
idf.py -p /dev/ttyUSB0 flash

# 监控串口
idf.py -p /dev/ttyUSB0 monitor
```

## 验证流程

### 1. 上电验证

```
[0.000] EHomeSystem Collector v2.0 starting...
[0.500] LED: 白色呼吸 (启动中)
[1.000] NVS initialized
[1.500] WiFi connecting...
[2.000] LED: 蓝色慢闪 (WiFi连接中)
```

### 2. WiFi连接验证

```
[5.000] WiFi connected, IP: 192.168.1.xxx
[5.500] LED: 青色慢闪 (MQTT连接中)
[6.000] MQTT connected
[6.500] LED: 绿色常亮 (正常运行)
```

### 3. Hello握手验证

```
[7.000] Sending Hello...
[7.100] Hello sent (32 bytes)
[7.200] ConfigManifest received
[7.300] Config applied, sending ConfigResult
[7.500] LED: 绿色常亮
```

### 4. 数据采集验证

```
[12.000] DataReport: ch=1, seq=1, raw=0102030405
[17.000] DataReport: ch=1, seq=2, raw=0102030405
[22.000] DataReport: ch=1, seq=3, raw=0102030405
... (每5s)
```

### 5. 心跳验证

```
[10.000] StatusReport: uptime=10s, status=online, channels=2
[15.000] StatusReport: uptime=15s, status=online, channels=2
... (每5s)
```

## 故障排查

### LED状态诊断

| LED状态 | 含义 | 处理 |
|---------|------|------|
| 白色呼吸 | 启动中 | 正常, 等待 |
| 黄色快闪 | WiFi未配置 | 进入配网模式 |
| 蓝色慢闪 | WiFi连接中 | 正常, 等待 |
| 红色双闪 | WiFi连接失败 | 检查SSID/密码 |
| 青色慢闪 | MQTT连接中 | 正常, 等待 |
| 红色三闪 | MQTT连接失败 | 检查broker地址 |
| 橙色慢闪 | Server离线 | 检查server状态 |
| 绿色常亮 | 正常运行 | 无需处理 |
| 紫色呼吸 | OTA升级中 | 正常, 等待 |
| 黄色单闪 | 采集错误 | 检查传感器 |
| 红色快闪 | 恢复出厂中 | 正常, 等待重启 |

### 串口命令

```bash
# 查看WiFi状态
AT+WIFI?

# 设置WiFi
AT+WIFI=ssid,password

# 恢复出厂
AT+FACTORY

# 查看设备信息
AT+INFO

# 查看MQTT状态
AT+MQTT?
```

## 验证检查清单

- [ ] ESP-IDF环境配置正确
- [ ] WiFi SSID/密码设置正确
- [ ] MQTT broker地址可达
- [ ] 串口连接正常
- [ ] 固件编译成功
- [ ] 烧录成功
- [ ] Hello消息发送成功
- [ ] ConfigManifest接收成功
- [ ] DataReport上报成功
- [ ] StatusReport心跳正常
- [ ] LED状态指示正确

## 预期结果

```
上电 → 白色呼吸 → 蓝色慢闪 → 青色慢闪 → 绿色常亮
  ↓
Hello → ConfigManifest → DataReport → StatusReport(每5s)
  ↓
系统正常运行, 数据正常上报
```
