#!/bin/bash
# EHomeSystem v2.0 - ESP32烧录与验证脚本

set -e

PROJECT_DIR="/home/sun/workspace/EHomeSystem/esp32-collector"
PORT="${1:-/dev/ttyUSB0}"

echo "========================================"
echo "EHomeSystem v2.0 - ESP32烧录与验证"
echo "========================================"
echo ""
echo "项目目录: $PROJECT_DIR"
echo "串口: $PORT"
echo ""

# 检查固件
echo "[1/5] 检查固件..."
if [ ! -f "$PROJECT_DIR/build/ehome_collector.bin" ]; then
    echo "错误: 固件未编译, 请先运行 idf.py build"
    exit 1
fi
echo "  ✓ 固件已编译"

# 检查串口
echo ""
echo "[2/5] 检查串口..."
if [ ! -e "$PORT" ]; then
    echo "警告: 串口 $PORT 不存在"
    echo "可用串口:"
    ls /dev/tty* 2>/dev/null | grep -E "ttyUSB|ttyACM" || echo "  无可用串口"
    echo ""
    echo "请连接ESP32开发板, 然后运行:"
    echo "  $0 /dev/ttyUSB0"
    exit 1
fi
echo "  ✓ 串口 $PORT 可用"

# 烧录固件
echo ""
echo "[3/5] 烧录固件..."
echo "  Bootloader → 0x0000"
echo "  Partition Table → 0x8000"
echo "  Application → 0x10000"
idf.py -p "$PORT" flash
echo "  ✓ 烧录完成"

# 验证固件
echo ""
echo "[4/5] 验证固件..."
echo "  读取flash并校验..."
esptool.py --chip esp32s3 -p "$PORT" verify_flash 0x10000 "$PROJECT_DIR/build/ehome_collector.bin"
echo "  ✓ 固件验证通过"

# 监控串口
echo ""
echo "[5/5] 监控串口输出..."
echo "  按 Ctrl+] 退出监控"
echo ""
echo "预期输出:"
echo "  [0.000] EHomeSystem Collector v2.0 starting..."
echo "  [0.500] NVS initialized"
echo "  [1.000] Hello sent (32 bytes): 010A0B..."
echo "  [1.500] Tasks created, running..."
echo "  [6.000] StatusReport sent (14 bytes)"
echo "  [11.000] DataReport sent (17 bytes)"
echo ""
idf.py -p "$PORT" monitor

echo ""
echo "========================================"
echo "烧录与验证完成!"
echo "========================================"
