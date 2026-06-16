#!/bin/bash
# 完整烧录脚本 - 烧录所有必要的分区

set -e

ESP32_DIR="/home/bcat/workspace/ehome-system/esp32-collector"
BUILD_DIR="$ESP32_DIR/build"
PORT="/dev/ttyACM0"
BAUD="460800"

echo "=== 完整烧录 ESP32-C6 ==="
echo "端口: $PORT"
echo "波特率: $BAUD"

# 检查必要的文件
if [ ! -f "$BUILD_DIR/bootloader/bootloader.bin" ]; then
    echo "错误: bootloader.bin 不存在"
    exit 1
fi

if [ ! -f "$BUILD_DIR/partition_table/partition-table.bin" ]; then
    echo "错误: partition-table.bin 不存在"
    exit 1
fi

if [ ! -f "$BUILD_DIR/ota_data_initial.bin" ]; then
    echo "错误: ota_data_initial.bin 不存在"
    exit 1
fi

if [ ! -f "$BUILD_DIR/ehome_collector.bin" ]; then
    echo "错误: ehome_collector.bin 不存在"
    exit 1
fi

echo ""
echo "烧录分区:"
echo "  - bootloader.bin -> 0x0"
echo "  - partition-table.bin -> 0x8000"
echo "  - ota_data_initial.bin -> 0xd000"
echo "  - ehome_collector.bin -> 0x10000"
echo ""

# 使用 esptool 烧录所有分区
cd "$BUILD_DIR"
python3 -m esptool \
    --chip esp32c6 \
    --port "$PORT" \
    --baud "$BAUD" \
    write_flash \
    0x0 bootloader/bootloader.bin \
    0x8000 partition_table/partition-table.bin \
    0xd000 ota_data_initial.bin \
    0x10000 ehome_collector.bin

echo ""
echo "✓ 烧录完成！"
