# 二进制帧协议规范

## v2.2 层级说明

v2.2 引入三层模型:

| 层级 | 概念 | 说明 |
|------|------|------|
| Node | 节点 | 物理边缘设备 (ESP32-C6/S3), MQTT 通信主体 |
| EdgeDevice | 边缘设备 | Node + Channel + DeviceConfig 的实例化 |
| DeviceConfig | 设备配置 | 设备型号的协议无关定义 |

MQTT topic 使用 `nodes/{id}/up|down`, 不再使用 v2.1 的 `devices/{id}/up|down`。

## 帧格式

```
┌──────────┬─────────────────────────────────────┐
│  type    │  fields...                          │
│  (1 B)   │  (key-value pairs, proto-style)     │
└──────────┴─────────────────────────────────────┘

type:    1 字节消息类型 (0x01-0x0D)
fields:  每个 field = [tag_byte] [value]

tag = (field_number << 3) | wire_type
  wire_type 0 = varint (整数/bool/enum)
  wire_type 2 = length-delimited (string/bytes/packed repeated)
```

## 消息类型

| Type        | Hex  | 方向      | 用途           |
|-------------|------|-----------|----------------|
| Hello       | 0x01 | ESP→SVR   | 启动握手       |
| StatusRpt   | 0x02 | ESP→SVR   | 心跳/状态      |
| DataRpt     | 0x03 | ESP→SVR   | 传感器数据上报 |
| ConfigMfst  | 0x04 | SVR→ESP   | 全量配置下发   |
| ConfigRslt  | 0x05 | ESP→SVR   | 配置应用确认   |
| WriteCmd    | 0x06 | SVR→ESP   | 交互命令       |
| WriteRsp    | 0x07 | ESP→SVR   | 命令确认       |
| Ping        | 0x08 | SVR→ESP   | 延迟测量       |
| Pong        | 0x09 | ESP→SVR   | 延迟响应       |
| OtaCmd      | 0x0A | SVR→ESP   | 固件升级命令   |
| OtaProg     | 0x0B | ESP→SVR   | OTA 进度       |
| ScanRpt     | 0x0C | ESP→SVR   | I2C 扫描结果   |
| ScanReq     | 0x0D | SVR→ESP   | I2C 扫描请求   |
