# SPI BMP280 验证报告

**日期**: 2026-06-16  
**状态**: ✅ 验证成功

## 验证目标

通过 TCP 调试通道验证 ESP32-C6 的 SPI 通信功能，读取 BMP280 传感器的芯片 ID (0x58)。

## 硬件配置

- **ESP32-C6 开发板**
  - USB JTAG: /dev/ttyACM0
  - UART0 (CH340): /dev/ttyUSB0
- **BMP280 传感器** (SPI 连接)
  - CS: GPIO 13
  - MOSI: GPIO 10
  - MISO: GPIO 11
  - SCLK: GPIO 12
  - 频率: 1 MHz
  - 模式: SPI Mode 0 (CPOL=0, CPHA=0)

## 验证结果

### 1. SPI 总线初始化 ✅

```
BUS_DMA: SPI config: CS=13, mode=0, freq=1000000, MOSI=10, MISO=11, SCLK=12
BUS_DMA: SPI1 polled bus init (MOSI=10 MISO=11 SCLK=12)
BUS_DMA: SPI polled device init (CS=13 mode=0 freq=1000000) ref_count=1
BUS_MGR: ch=1 type=3 dma=0 idx=0 SUCCESS
```

### 2. BMP280 芯片 ID 读取 ✅

**自动采样结果**:
```
DataReport: 22 02 0058 → BMP280 芯片 ID = 0x58 ✅
```

**手动读取结果**:
```
MSG: WriteCmd: req=1, ch=1, len=2
MSG: Sending WriteRsp: req=1, success=1 ✅
```

### 3. 性能统计 ✅

```
BUS_WORKER: txn=12 err=0 (100%) no_ctx=0 ✅
```

- 事务成功率: 100%
- 错误数: 0
- 上下文缺失: 0

## 修复的 Bug

### Bug #1: "no ctx" 错误

**现象**: WriteCommand 返回错误码 4 ("no ctx")

**根因**: TCP 测试脚本中的 WriteCommand protobuf 字段映射错误，缺少 channel_id 字段

**修复**:
```python
# 错误的映射
def create_write_cmd(channel_id, reg_addr, read_size=1):
    cmd += encode_varint_field(1, channel_id)
    cmd += encode_bytes_field(2, bytes([reg_addr]))
    cmd += encode_varint_field(3, read_size)

# 正确的映射 (匹配 msg_handler.c)
def create_write_cmd(request_id, channel_id, write_data, read_size=1):
    cmd += encode_varint_field(1, request_id)    # F1: request_id
    cmd += encode_varint_field(2, channel_id)    # F2: channel_id
    cmd += encode_bytes_field(3, write_data)     # F3: data
    cmd += encode_varint_field(4, read_size)     # F4: read_size
```

**修改文件**: `scripts/tcp_debug.py`

---

### Bug #2: "bus err" (ESP_ERR_TIMEOUT)

**现象**: SPI 传输返回 ESP_ERR_TIMEOUT (错误码 258)

**根因**: bus_worker.c 中传递 `sizeof(rx)=256` 作为 rx_size，导致 SPI 全双工模式下 rxlength > length，违反 ESP-IDF SPI 驱动要求

**修复**:
```c
// 修复前
esp_err_t e = bus_dma_transact(ctx, cmd.tx_data, cmd.tx_len,
                               cmd.timeout_ms ? cmd.timeout_ms : 50,
                               rx, sizeof(rx), &rl);

// 修复后
size_t actual_rx_size = cmd.read_size;
if (actual_rx_size > sizeof(rx)) actual_rx_size = sizeof(rx);
size_t total_len = (cmd.tx_len > actual_rx_size) ? cmd.tx_len : actual_rx_size;
if (total_len == 0) total_len = 1;

esp_err_t e = bus_dma_transact(ctx, cmd.tx_data, total_len,
                               cmd.timeout_ms ? cmd.timeout_ms : 50,
                               rx, actual_rx_size, &rl);
```

**修改文件**: `esp32-collector/main/bus_worker.c`

---

### Bug #3: 配置加载失败

**现象**: 
- TCP 端口 8088 未监听
- 控制台无输出

**根因**: 
1. `CONFIG_DEBUG_TCP_ENABLED=n` 导致 TCP 服务器未编译
2. `CONFIG_ESP_CONSOLE_USB_SERIAL_JTAG=y` 与 USB JTAG 调试冲突

**修复**:
```
# sdkconfig.defaults
CONFIG_DEBUG_TCP_ENABLED=y
CONFIG_ESP_CONSOLE_UART_DEFAULT=y
```

**修改文件**: `esp32-collector/sdkconfig.defaults`

## SPI 通信协议细节

### BMP280 芯片 ID 读取

**SPI 事务格式** (全双工):
```
TX: [0xD0, 0x00]  # 寄存器地址 0xD0 + dummy byte
RX: [0xXX, 0x58]  # dummy + 芯片 ID (0x58)
```

**关键点**:
- BMP280 SPI 读取需要 2 字节事务
- 第一字节发送寄存器地址，同时接收 dummy 数据
- 第二字节发送 dummy，同时接收芯片 ID

## 测试工具

### TCP 调试脚本

**文件**: `scripts/tcp_debug.py`

**功能**:
1. 发送 ConfigManifest (SPI 配置)
2. 发送 WriteCommand (读取寄存器)
3. 解析 WriteRsp 和 DataReport

**使用示例**:
```bash
python3 scripts/tcp_debug.py
```

### UART 监控脚本

**文件**: `scripts/uart_monitor.py`

**功能**: 实时捕获 ESP32 UART 输出

**使用示例**:
```bash
python3 scripts/uart_monitor.py /dev/ttyUSB0
```

## 验证流程

1. **编译固件**
   ```bash
   cd esp32-collector
   idf.py build
   ```

2. **烧录固件**
   ```bash
   bash scripts/flash_full.sh /dev/ttyUSB0
   ```

3. **等待启动** (约 20 秒)

4. **运行 TCP 测试**
   ```bash
   python3 scripts/tcp_debug.py
   ```

5. **验证结果**
   - 检查 `BMP280 ID=0x58` 输出
   - 检查 `WriteRsp: req=1, success=1`
   - 检查 `txn=N err=0 (100%)`

## 后续工作

1. ✅ UART 回环测试 (已通过)
2. ✅ SPI BMP280 验证 (已通过)
3. ⏳ I2C 传感器验证 (待测试)
4. ⏳ 前后端集成测试
5. ⏳ E2E+CDP 端到端验证

## 结论

SPI BMP280 验证完全成功，所有修复的 Bug 均已验证通过。系统可以正确：
- 初始化 SPI 总线
- 配置 BMP280 通道
- 读取传感器数据
- 通过 TCP 返回结果
- 自动采样并上报数据
