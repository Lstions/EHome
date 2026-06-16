#!/bin/bash
# SPI BMP280 测试脚本 - 自动烧录并捕获完整日志

set -e

ESP32_DIR="/home/bcat/workspace/ehome-system/esp32-collector"
SCRIPT_DIR="/home/bcat/workspace/ehome-system"
LOG_FILE="/tmp/uart_spi_test.log"

echo "=== SPI BMP280 完整测试 ==="

# 清理旧日志
rm -f "$LOG_FILE"

# 1. 启动 UART 监控 (后台运行)
echo "[1/5] 启动 UART 监控..."
python3 "$SCRIPT_DIR/scripts/uart_monitor.py" "$LOG_FILE" 120 &

MONITOR_PID=$!
sleep 1  # 等待监控启动

# 2. 烧录固件 (完整烧录所有分区)
echo "[2/5] 烧录固件..."
# 先停止 UART 监控，释放串口
if [ ! -z "$MONITOR_PID" ]; then
    kill $MONITOR_PID 2>/dev/null || true
    sleep 1
fi
cd "$SCRIPT_DIR"
bash scripts/flash_full.sh 2>&1 | tail -5

# 重新启动 UART 监控
echo "重新启动 UART 监控..."
python3 "$SCRIPT_DIR/scripts/uart_monitor.py" "$LOG_FILE" 120 &
MONITOR_PID=$!
sleep 2  # 等待监控启动

# 3. 等待 ESP32 启动并连接 WiFi
echo "[3/5] 等待 ESP32 启动 (25秒)..."
sleep 25

# 4. 运行 TCP 测试
echo "[4/5] 运行 TCP 测试..."
cd "$SCRIPT_DIR"
python3 scripts/tcp_debug.py || true

# 5. 停止监控并分析日志
echo "[5/5] 停止监控并分析..."
sleep 3
kill $MONITOR_PID 2>/dev/null || true

echo ""
echo "=== 关键日志 ==="
grep -E "reg_bus_channel|bus_dma_init|SPI|CALLBACK|handle_config|abort|panic" "$LOG_FILE" | tail -20 || echo "未找到关键日志"

echo ""
echo "=== 完整日志保存在: $LOG_FILE ==="
