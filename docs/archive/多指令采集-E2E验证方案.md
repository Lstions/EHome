# 多指令采集方案 — E2E 验证方案 v2.3

> **分支**: `feat/multi-command-collection`
> **日期**: 2026-06-21
> **目标**: 验证全部 4 个需求达成设计要求，无 bug

## 验证矩阵

| 需求 | API 验证 | 前端 UI 验证 | 固件协议验证 |
|------|---------|------------|------------|
| 1. TTL-485 多设备 | ConfigManifest field 9 编码 | EdgeDevice 列表多设备展示 | ESP32 解析 EdgeDeviceGroup |
| 2. 地址发现/修改 | ScanRequest Modbus + ChangeAddress | 扫描按钮 + 修改地址弹窗 | Modbus 扫描回调 + WriteCmd |
| 3. 自检报警 | StatusReport field 7 解析 | EdgeDevice 健康状态显示 | error_count 上报 |
| 4. 多指令独立频率 | DataReport field 7/8 路由 | EdgeDevice 详情 commands 展示 | scheduler 独立计时 |

## Phase A: API 层验证 (curl)

### A1: Hello protocol_version
```bash
# 验证 ESP32 发送的 Hello 包含 protocol_version=2
docker logs ehome-backend-dev --tail 50 | grep "proto="
# 预期: proto=2 或 proto=2.0
```

### A2: ConfigManifest 版本协商
```bash
# 查 Node 的 protocol_version
TOKEN=$(curl -s http://localhost:8081/api/v1/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin123"}' | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['token'])")

# 查所有 Node
curl -s http://localhost:8081/api/v1/nodes -H "Authorization: Bearer $TOKEN" | python3 -c "
import sys,json
nodes = json.load(sys.stdin)['data']
for n in nodes:
    print(f\"node_id={n['node_id']} proto={n.get('protocol_version','N/A')}\")
"

# 触发 ConfigManifest 下发
curl -s -X POST "http://localhost:8081/api/v1/nodes/1/config/sync" \
  -H "Authorization: Bearer $TOKEN"
# 预期: 后端日志显示 ConfigManifest 编码路径 (v1 或 v2)
```

### A3: 通道扫描 (Modbus)
```bash
# 创建 RS485 通道 (先查 node_id)
NODE_DEVICE_ID=$(curl -s http://localhost:8081/api/v1/nodes -H "Authorization: Bearer $TOKEN" | \
  python3 -c "import sys,json; print(json.load(sys.stdin)['data'][0]['node_id'])")

# 触发 Modbus 扫描
curl -s -X POST "http://localhost:8081/api/v1/channels/1/scan" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"scan_type":"modbus","start_addr":1,"end_addr":10,"timeout_ms":200}'
# 预期: 返回 request_id + 扫描已触发
# ESP32 日志: "ScanReq MODBUS: req=... start=1 end=10 timeout=200"
```

### A4: 地址修改 (ChangeAddress)
```bash
# 先查 EdgeDevice
curl -s http://localhost:8081/api/v1/edge-devices -H "Authorization: Bearer $TOKEN" | \
  python3 -c "import sys,json; [print(f\"id={d['id']} name={d['name']} addr={d['hardware_id']}\") for d in json.load(sys.stdin)['data']]"

# 修改地址 (需要 DeviceConfig 有 change_address_command 模板才成功)
curl -s -X POST "http://localhost:8081/api/v1/edge-devices/1/change-address" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"new_address":5}'
# 预期: 200 + message "地址修改命令已发送" 或 400 "不支持地址修改"
```

### A5: 健康状态 (StatusReport)
```bash
# 查 EdgeDevice 的 status 和 error_code
curl -s http://localhost:8081/api/v1/edge-devices -H "Authorization: Bearer $TOKEN" | \
  python3 -c "import sys,json; [print(f\"id={d['id']} status={d['status']} error={d.get('error_code',0)} last_data={d.get('last_data_at','N/A')}\") for d in json.load(sys.stdin)['data']]"
```

## Phase B: 前端 UI 验证 (CDP browser)

前置: Chromium headless 已启动在 localhost:18800

### B1: 登录
```
browser_navigate → http://localhost:5174/login
browser_console → fetch API login → set token → window.location.href='/dashboard'
browser_snapshot → 确认在 dashboard
```

### B2: 节点详情 — EdgeDeviceGroup 展示
```
window.location.href='/node'
→ 点击节点进入详情
→ 检查"边缘设备"区域是否显示设备列表 + 硬件地址
→ browser_vision: "检查节点详情页的边缘设备区域：是否有设备名称、硬件地址列？"
```

### B3: 边缘设备列表 — 健康状态
```
window.location.href='/edge-device'
→ 检查设备卡片：状态标签 (active/warning/error)
→ 检查是否有"扫描地址"按钮 (仅 RS485/I2C 通道)
→ browser_vision: "检查边缘设备列表页：是否有状态标签？是否有操作按钮？"
```

### B4: 通道管理 — 扫描按钮
```
window.location.href='/channel'
→ 检查通道列表
→ 对于 UART RS485 通道：是否有"地址扫描"按钮
→ 对于 SPI/GPIO 通道：应无扫描按钮
→ browser_vision: "检查通道管理页：哪些行有扫描按钮？哪些没有？"
```

### B5: 地址修改弹窗
```
window.location.href='/edge-device/1'
→ 检查详情页是否有"修改地址"按钮
→ 点击 → 弹窗确认 (旧地址 → 新地址)
→ 关闭弹窗
```

## Phase C: 固件协议验证 (ESP32 串口日志)

```bash
# 连 C6 USB Serial JTAG
python3 -c "
import serial, time
c6 = serial.Serial('/dev/ttyACM0', 115200, timeout=1)
# 清缓冲
while c6.in_waiting: c6.read(c6.in_waiting)
start = time.time()
while time.time() - start < 30:
    line = c6.readline()
    if line:
        print(line.decode('utf-8', errors='replace').strip())
"
```

### C1: EdgeDeviceGroup 解析
ESP32 日志应出现:
- `CONFIG: channel[X]: ... edge_device_count=Y`
- `CONFIG: edge_device_group: id=Z hw_id=W cmd_count=N`
- `SCHEDULER: v2调度 edge_device=X commands=Y`

### C2: 独立计时
ESP32 日志应出现:
- `SCHEDULER: Stats: samples=N` (定期统计日志)

### C3: Modbus 扫描
当收到 ScanRequest 时:
- `MODBUS_SCAN: Scanning addresses 1-10, timeout=200ms`
- `MODBUS_SCAN: Found device at addr N`
- `MODBUS_SCAN: Scan complete: N devices found`

### C4: 健康上报
StatusReport 应包含:
- `ChannelHealth channel_id=X`
- `EdgeDeviceHealth edge_device_id=Y errors=Z`

## Phase D: 端到端场景

### D1: RS485 多设备场景
1. 创建 RS485 通道 (bus_config mode=rs485)
2. 创建 2 个 EdgeDevice (同一通道, 不同 hardware_id: 1 和 2)
3. 触发 ConfigManifest → 验证 ESP32 收到 2 个 EdgeDeviceGroup
4. 验证 scheduler 为每个 device 独立调度
5. 验证 DataReport 带 edge_device_id 路由正确

### D2: 多指令独立频率
1. EdgeDevice 配置 2 条 command (不同 interval_ms)
2. 触发 ConfigManifest → 验证 ESP32 收到 2 条 command
3. 验证每条 command 按自己的 interval_ms 独立计时
4. 验证 DataReport 的 command_index 正确区分

### D3: 自检报警
1. 断开传感器电源
2. 等待 StatusReport (60s 内)
3. 验证 consecutive_errors 递增
4. 验证 EdgeDevice status 变为 warning/error
5. 恢复传感器 → 验证 status 恢复 active
